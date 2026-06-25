import { FastifyInstance } from 'fastify';
import { readdir, stat, readFile } from 'fs/promises';
import { join, relative, extname } from 'path';
import { v4 as uuidv4 } from 'uuid';
import { createReadStream } from 'fs';

const WORKSPACE_DIR = '/workspace';

// Simple mime type map
export const MIME_TYPES: Record<string, string> = {
  '.ts': 'application/typescript',
  '.tsx': 'application/typescript',
  '.js': 'application/javascript',
  '.jsx': 'application/javascript',
  '.json': 'application/json',
  '.csv': 'text/csv',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.svg': 'image/svg+xml',
  '.html': 'text/html',
  '.css': 'text/css',
  '.md': 'text/markdown',
  '.py': 'application/x-python',
  '.txt': 'text/plain',
  '.pdf': 'application/pdf',
  '.xml': 'application/xml',
  '.yaml': 'application/x-yaml',
  '.yml': 'application/x-yaml',
  '.sh': 'application/x-sh',
  '.sql': 'application/sql',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
  '.wasm': 'application/wasm',
  '.map': 'application/json',
};

export function getMimeType(filePath: string): string {
  const ext = extname(filePath).toLowerCase();
  return MIME_TYPES[ext] || 'application/octet-stream';
}

interface FileInfo {
  name: string;
  path: string;
  size: number;
  mtime: string;
  mimeType: string;
}

// Directories to skip during workspace listing and diff detection.
// These are generated/dependency folders that should never be registered as artifacts.
export const SKIP_DIRS = new Set(['node_modules', 'venv', '.venv', '__pycache__', 'env', '.env']);

// Recursively list files, skipping dotfiles and dependency/generated folders
async function listFilesRecursive(dir: string, baseDir: string): Promise<FileInfo[]> {
  const results: FileInfo[] = [];
  try {
    const entries = await readdir(dir, { withFileTypes: true });
    for (const entry of entries) {
      if (entry.name.startsWith('.') || SKIP_DIRS.has(entry.name)) continue;
      if (entry.isSymbolicLink()) continue;
      const fullPath = join(dir, entry.name);
      if (entry.isDirectory()) {
        const subFiles = await listFilesRecursive(fullPath, baseDir);
        results.push(...subFiles);
      } else if (entry.isFile()) {
        const fileStat = await stat(fullPath);
        const relativePath = relative(baseDir, fullPath);
        results.push({
          name: entry.name,
          path: relativePath,
          size: fileStat.size,
          mtime: fileStat.mtime.toISOString(),
          mimeType: getMimeType(entry.name),
        });
      }
    }
  } catch {
    // Directory may not exist or be unreadable
  }
  return results;
}

// In-memory snapshot storage
const snapshots = new Map<string, Map<string, { size: number; mtimeMs: number }>>();

// --- Secret scanning support ---

// Sensitive filename patterns
const SENSITIVE_FILE_PATTERNS = [
  /^\.env/,          // .env, .env.local, .env.production
  /\.key$/,
  /\.pem$/,
  /\.p12$/,
  /\.pfx$/,
  /^id_rsa/,
  /credential/i,
  /secret/i,
];

// Content patterns to grep for
const SENSITIVE_CONTENT_PATTERNS = [
  /API_KEY\s*=/,
  /SECRET_KEY\s*=/,
  /TOKEN\s*=/,
  /PASSWORD\s*=/,
  /PRIVATE.KEY/,
];

// Dirs to skip during secret scanning — includes dotfiles dirs except .git is explicit
const SCAN_SKIP_DIRS = new Set(['node_modules', 'venv', '.venv', '__pycache__', 'env', '.git']);

interface SecretFinding {
  path: string;
  reason: string;
  severity: 'warning' | 'critical';
}

// Recursive walker that includes dotfiles (for secret scanning)
async function listAllFilesRecursive(dir: string, baseDir: string): Promise<{ path: string; name: string; size: number }[]> {
  const results: { path: string; name: string; size: number }[] = [];
  try {
    const entries = await readdir(dir, { withFileTypes: true });
    for (const entry of entries) {
      if (SCAN_SKIP_DIRS.has(entry.name)) continue;
      if (entry.isSymbolicLink()) continue;
      const fullPath = join(dir, entry.name);
      if (entry.isDirectory()) {
        const subFiles = await listAllFilesRecursive(fullPath, baseDir);
        results.push(...subFiles);
      } else if (entry.isFile()) {
        const fileStat = await stat(fullPath);
        results.push({
          name: entry.name,
          path: relative(baseDir, fullPath),
          size: fileStat.size,
        });
      }
    }
  } catch {
    // Directory may not exist or be unreadable
  }
  return results;
}

export async function workspaceRoutes(app: FastifyInstance) {
  // GET /workspace/files — list all files recursively
  app.get('/workspace/files', async (_request, reply) => {
    const files = await listFilesRecursive(WORKSPACE_DIR, WORKSPACE_DIR);
    return reply.send({ files });
  });

  // GET /workspace/files/* — download a file by path
  app.get('/workspace/files/*', async (request, reply) => {
    const params = request.params as { '*': string };
    const filePath = params['*'];
    if (!filePath) {
      return reply.status(400).send({ error: 'file path is required' });
    }

    const fullPath = join(WORKSPACE_DIR, filePath);
    // Security: ensure path doesn't escape workspace
    if (!fullPath.startsWith(WORKSPACE_DIR)) {
      return reply.status(403).send({ error: 'access denied' });
    }

    try {
      const fileStat = await stat(fullPath);
      if (!fileStat.isFile()) {
        return reply.status(404).send({ error: 'not a file' });
      }

      const mimeType = getMimeType(filePath);
      reply.header('Content-Type', mimeType);
      reply.header('Content-Length', fileStat.size);
      reply.header('Content-Disposition', `inline; filename="${filePath.split('/').pop()}"`);

      const stream = createReadStream(fullPath);
      return reply.send(stream);
    } catch {
      return reply.status(404).send({ error: 'file not found' });
    }
  });

  // POST /workspace/snapshot — capture filesystem metadata snapshot
  app.post('/workspace/snapshot', async (_request, reply) => {
    const snapshotId = uuidv4();
    const fileMap = new Map<string, { size: number; mtimeMs: number }>();

    const files = await listFilesRecursive(WORKSPACE_DIR, WORKSPACE_DIR);
    for (const file of files) {
      fileMap.set(file.path, {
        size: file.size,
        mtimeMs: new Date(file.mtime).getTime(),
      });
    }

    snapshots.set(snapshotId, fileMap);
    return reply.send({ snapshotId });
  });

  // POST /workspace/diff — compare current state to snapshot
  app.post<{ Body: { snapshotId: string } }>('/workspace/diff', async (request, reply) => {
    const { snapshotId } = request.body;
    if (!snapshotId) {
      return reply.status(400).send({ error: 'snapshotId is required' });
    }

    const snapshot = snapshots.get(snapshotId);
    if (!snapshot) {
      return reply.status(404).send({ error: 'snapshot not found' });
    }

    const currentFiles = await listFilesRecursive(WORKSPACE_DIR, WORKSPACE_DIR);
    const currentMap = new Map<string, { size: number; mtimeMs: number }>();
    for (const file of currentFiles) {
      currentMap.set(file.path, {
        size: file.size,
        mtimeMs: new Date(file.mtime).getTime(),
      });
    }

    const newFiles: string[] = [];
    const modified: string[] = [];
    const deleted: string[] = [];

    // Check for new and modified files
    for (const [path, current] of currentMap) {
      const prev = snapshot.get(path);
      if (!prev) {
        newFiles.push(path);
      } else if (prev.size !== current.size || prev.mtimeMs !== current.mtimeMs) {
        modified.push(path);
      }
    }

    // Check for deleted files
    for (const path of snapshot.keys()) {
      if (!currentMap.has(path)) {
        deleted.push(path);
      }
    }

    // Clean up snapshot
    snapshots.delete(snapshotId);

    return reply.send({ new: newFiles, modified, deleted });
  });

  // POST /workspace/scan-secrets — scan workspace for secrets and sensitive files
  app.post('/workspace/scan-secrets', async (_request, reply) => {
    const findings: SecretFinding[] = [];
    const files = await listAllFilesRecursive(WORKSPACE_DIR, WORKSPACE_DIR);

    for (const file of files) {
      // Check filename against sensitive patterns
      const filenameMatch = SENSITIVE_FILE_PATTERNS.find(p => p.test(file.name));
      if (filenameMatch) {
        const isPrivateKey = /\.key$|\.pem$|\.p12$|\.pfx$|^id_rsa/.test(file.name);
        const isEnv = /^\.env/.test(file.name);
        findings.push({
          path: file.path,
          reason: `Sensitive filename pattern: ${file.name}`,
          severity: isPrivateKey || isEnv ? 'critical' : 'warning',
        });
        continue; // Don't also scan content for files already flagged by name
      }

      // For text files under 100KB, scan content
      if (file.size > 0 && file.size < 100 * 1024) {
        const ext = extname(file.name).toLowerCase();
        const textExtensions = new Set([
          '', '.ts', '.tsx', '.js', '.jsx', '.json', '.py', '.txt', '.md',
          '.html', '.css', '.sh', '.sql', '.yaml', '.yml', '.xml', '.cfg',
          '.ini', '.conf', '.toml', '.env', '.properties', '.rb', '.go',
          '.java', '.rs', '.c', '.cpp', '.h', '.hpp', '.cs',
        ]);
        if (!textExtensions.has(ext)) continue;

        try {
          const fullPath = join(WORKSPACE_DIR, file.path);
          const content = await readFile(fullPath, 'utf-8');
          for (const pattern of SENSITIVE_CONTENT_PATTERNS) {
            if (pattern.test(content)) {
              findings.push({
                path: file.path,
                reason: `Content matches pattern: ${pattern.source}`,
                severity: 'warning',
              });
              break; // One finding per file is enough
            }
          }
        } catch {
          // File unreadable, skip
        }
      }
    }

    return reply.send({ findings });
  });
}
