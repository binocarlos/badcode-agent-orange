import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { writeFile, mkdir } from 'node:fs/promises';
import path from 'node:path';
import type { ToolPlugin } from '../registry.js';

/**
 * Infer artifact type from file extension for auto-registration.
 */
export function inferArtifactType(ext: string, filePath?: string): string {
  if (['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg'].includes(ext)) return 'image';
  if (['.html', '.htm'].includes(ext)) {
    // Only treat HTML in dist/ as webapp; source HTML has unresolved imports.
    if (filePath && /\bdist\//.test(filePath)) return 'webapp';
    return 'code';
  }
  if (['.csv', '.json', '.tsv'].includes(ext)) return 'data';
  if (['.js', '.ts', '.py', '.r', '.sql', '.sh', '.css'].includes(ext)) return 'code';
  return 'file';
}

/**
 * write_file — create or overwrite a file at the given path.
 * Generic builtin: replaces the SDK's own Write tool (which requires reading first).
 * Auto-registers written files under /workspace/ as artifacts via __artifact_registered marker.
 */
export const writeFileTool: ToolPlugin = {
  name: 'write_file',
  sdkTool: tool(
    'write_file',
    'Create or overwrite a file at the given path. Creates parent directories if needed. Use this to write files in the workspace.',
    {
      file_path: z.string().min(1).describe('Absolute path to the file to write'),
      content: z.string().describe('The full content to write to the file'),
    },
    async (args) => {
      try {
        await mkdir(path.dirname(args.file_path), { recursive: true });
        await writeFile(args.file_path, args.content, 'utf-8');

        const content: { type: 'text'; text: string }[] = [{
          type: 'text' as const,
          text: `Successfully wrote ${args.content.length} characters to ${args.file_path}`,
        }];

        // Auto-register files under /workspace/ as artifacts
        if (args.file_path.startsWith('/workspace/')) {
          const relativePath = args.file_path.replace(/^\/workspace\//, '');
          const ext = path.extname(args.file_path).toLowerCase();
          content.push({
            type: 'text' as const,
            text: JSON.stringify({
              __artifact_registered: true,
              file_path: relativePath,
              artifact_type: inferArtifactType(ext, relativePath),
              label: path.basename(args.file_path),
              description: '',
            }),
          });
        }

        return { content };
      } catch (error) {
        return {
          content: [{
            type: 'text' as const,
            text: `Failed to write file: ${error instanceof Error ? error.message : 'Unknown error'}`,
          }],
          isError: true,
        };
      }
    }
  ),
  // write_file emits __artifact_registered markers (handled inline by agent-service
  // artifact auto-registration logic), not a single top-level marker — no marker spec needed here.
};
