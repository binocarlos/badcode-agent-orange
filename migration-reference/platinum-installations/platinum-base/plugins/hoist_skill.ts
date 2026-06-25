import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import type { ToolPlugin, MarkerSpec } from './types.js';
import { promises as fs } from 'fs';
import * as path from 'path';

/** One bundled file: src is workspace-relative; dest is where Doc D lays it back down. */
export interface BundledFile {
  src: string;
  dest: string;
}

/** The manifest persisted as skill.manifest.json — single source of truth for Docs C/D. */
export interface SkillManifest {
  name: string;
  description: string;
  triggers: string[];
  keywords: string[];
  visibility: 'private' | 'organizational';
  requiresBuild: boolean;
  bundledFiles: BundledFile[];
  installScript?: string; // relative filename ("install.sh") when requiresBuild, else omitted
}

/** kebab-case: lowercase alphanumerics separated by single hyphens. */
export function isValidSkillName(name: string): boolean {
  return /^[a-z0-9]+(-[a-z0-9]+)*$/.test(name);
}

/**
 * Normalize a bundled-file `src` to a workspace-relative path. Accepts both
 * workspace-relative inputs ("scripts/gen.py") and absolute paths that point
 * inside the workspace ("/workspace/scripts/gen.py") — the model sometimes
 * supplies the latter. Absolute paths outside the workspace are returned as-is
 * so that assertContained() can reject them.
 */
export function toWorkspaceRelativeSrc(workspaceRoot: string, src: string): string {
  if (!path.isAbsolute(src)) return src;
  const rel = path.relative(path.resolve(workspaceRoot), path.resolve(src));
  return rel;
}

/** Throw if `resolved` is not inside `base` (after normalizing `..`). */
function assertContained(base: string, resolved: string, label: string): void {
  const b = path.resolve(base);
  const r = path.resolve(resolved);
  if (r !== b && !r.startsWith(b + path.sep)) {
    throw new Error(`${label} escapes the allowed directory`);
  }
}

/** Build YAML frontmatter matching the existing .claude/skills SKILL.md format. */
export function buildFrontmatter(input: {
  name: string;
  description: string;
  triggers: string[];
  keywords: string[];
}): string {
  const lines: string[] = ['---', `name: ${input.name}`, `description: ${input.description}`];
  if (input.triggers.length > 0) {
    lines.push('triggers:');
    for (const t of input.triggers) lines.push(`  - ${t}`);
  }
  if (input.keywords.length > 0) {
    lines.push(`keywords: [${input.keywords.join(', ')}]`);
  }
  lines.push('---');
  return lines.join('\n');
}

/** Build the manifest object; requiresBuild iff a non-empty install script is present. */
export function buildManifest(input: {
  name: string;
  description: string;
  triggers: string[];
  keywords: string[];
  bundledFiles: BundledFile[];
  installScript?: string;
  visibility: 'private' | 'organizational';
}): SkillManifest {
  const requiresBuild = !!(input.installScript && input.installScript.trim().length > 0);
  return {
    name: input.name,
    description: input.description,
    triggers: input.triggers,
    keywords: input.keywords,
    visibility: input.visibility,
    requiresBuild,
    bundledFiles: input.bundledFiles,
    installScript: requiresBuild ? 'install.sh' : undefined,
  };
}

export interface HoistArgs {
  name: string;
  description: string;
  triggers: string[];
  keywords: string[];
  body: string;
  bundledFiles: BundledFile[];
  installScript?: string;
  visibility: 'private' | 'organizational';
}

export interface HoistResult {
  artifactPath: string; // workspace-relative bundle dir, e.g. ".hoisted-skills/<name>"
  name: string;
  visibility: 'private' | 'organizational';
  requiresBuild: boolean;
  manifest: SkillManifest;
}

/**
 * Write the skill bundle under <workspaceRoot>/.hoisted-skills/<name>/ and return
 * the marker payload. Throws if a bundled file's src does not exist (no partial bundle).
 */
export async function writeSkillBundle(workspaceRoot: string, args: HoistArgs): Promise<HoistResult> {
  if (!isValidSkillName(args.name)) {
    throw new Error(`invalid skill name "${args.name}" — must be kebab-case (lowercase, hyphen-separated)`);
  }
  // Normalize any absolute-but-inside-workspace src to a workspace-relative path
  // so all downstream joins (access, copy, manifest) are consistent.
  args = {
    ...args,
    bundledFiles: args.bundledFiles.map(f => ({ ...f, src: toWorkspaceRelativeSrc(workspaceRoot, f.src) })),
  };
  // Verify every bundled file exists BEFORE writing anything (fail-fast, no partial bundle).
  for (const f of args.bundledFiles) {
    const abs = path.join(workspaceRoot, f.src);
    assertContained(workspaceRoot, abs, `bundled file src "${f.src}"`);
    try {
      await fs.access(abs);
    } catch {
      throw new Error(`bundled file not found: ${f.src}`);
    }
  }

  const relBundle = path.join('.hoisted-skills', args.name);
  const bundleDir = path.join(workspaceRoot, relBundle);
  // Fresh bundle dir (overwrite any previous hoist of the same name).
  await fs.rm(bundleDir, { recursive: true, force: true });
  await fs.mkdir(bundleDir, { recursive: true });

  const manifest = buildManifest(args);

  // SKILL.md = frontmatter + body.
  const skillMd = `${buildFrontmatter(args)}\n\n${args.body.trim()}\n`;
  await fs.writeFile(path.join(bundleDir, 'SKILL.md'), skillMd, 'utf8');

  // Manifest.
  await fs.writeFile(
    path.join(bundleDir, 'skill.manifest.json'),
    JSON.stringify(manifest, null, 2) + '\n',
    'utf8',
  );

  // Optional install.sh.
  if (manifest.requiresBuild) {
    await fs.writeFile(path.join(bundleDir, 'install.sh'), args.installScript!.trimEnd() + '\n', { mode: 0o755 });
  }

  // Copy bundled files into files/<dest>.
  for (const f of args.bundledFiles) {
    const destRel = f.dest && f.dest.length > 0 ? f.dest : f.src;
    const destAbs = path.join(bundleDir, 'files', destRel);
    assertContained(path.join(bundleDir, 'files'), destAbs, `bundled file dest "${destRel}"`);
    await fs.mkdir(path.dirname(destAbs), { recursive: true });
    await fs.copyFile(path.join(workspaceRoot, f.src), destAbs);
  }

  return {
    artifactPath: relBundle,
    name: args.name,
    visibility: args.visibility,
    requiresBuild: manifest.requiresBuild,
    manifest,
  };
}

const hoistSkillMarker: MarkerSpec = {
  key: '__skill_hoisted',
  event: 'skill_hoisted',
  toEvent: (p) => ({
    artifactPath: p.artifactPath,
    name: p.name,
    visibility: p.visibility,
    requiresBuild: p.requiresBuild,
    manifest: p.manifest,
  }),
  toModelText: (p) =>
    `Packaged skill "${p.name}"${p.requiresBuild ? ' (requires a build step)' : ''} and registered it as a downloadable bundle.`,
};

export const hoistSkillPlugin: ToolPlugin = {
  name: 'hoist_skill',
  marker: hoistSkillMarker,
  sdkTool: tool(
    'hoist_skill',
    'Package work just completed into a reusable skill bundle. Call this ONCE, after you have interviewed the user (via ask_user) to agree the name, description, triggers/keywords, which files belong, any install step, and visibility. Writes a SKILL.md + manifest + optional install.sh + copies of the bundled files, and registers the bundle as a downloadable artifact. Do NOT call this before the interview is complete.',
    {
      name: z.string().min(1).describe('kebab-case skill name (lowercase, hyphen-separated)'),
      description: z.string().min(1).describe('One-line description / trigger summary for the frontmatter'),
      body: z.string().min(1).describe('The SKILL.md markdown body (prose after the frontmatter) — the honed instructions. When referencing bundled files, use their installed path /workspace/.claude/skills/<name>/files/<dest> (every bundled file is copied into the skill\'s files/ subdir); never a bare /workspace/<file> or a session-relative path, which will not exist after install.'),
      triggers: z.array(z.string()).default([]).describe('Natural-language phrases that should trigger this skill'),
      keywords: z.array(z.string()).default([]).describe('Keywords for skill discovery'),
      bundled_files: z.array(z.object({
        src: z.string().min(1).describe('Workspace-relative path of a file that belongs to the skill'),
        dest: z.string().optional().describe('Where composition should place it (defaults to src)'),
      })).default([]).describe('Files (scripts/assets) the skill depends on'),
      install_script: z.string().optional().describe('Optional shell script run at image-build time (e.g. apt-get install ...). Omit if nothing must be installed.'),
      visibility: z.enum(['private', 'organizational']).default('organizational').describe('private = only you; organizational = your whole customer. (public requires separate promotion.)'),
    },
    async (args) => {
      const workspaceRoot = process.env.WORKSPACE_DIR ?? '/workspace';
      try {
        const result = await writeSkillBundle(workspaceRoot, {
          name: args.name as string,
          description: args.description as string,
          body: args.body as string,
          triggers: (args.triggers as string[]) ?? [],
          keywords: (args.keywords as string[]) ?? [],
          bundledFiles: ((args.bundled_files as Array<{ src: string; dest?: string }>) ?? []).map(f => ({
            src: f.src,
            dest: f.dest ?? f.src,
          })),
          installScript: args.install_script as string | undefined,
          visibility: args.visibility as 'private' | 'organizational',
        });
        return {
          content: [{
            type: 'text' as const,
            text: JSON.stringify({
              __skill_hoisted: true,
              artifactPath: result.artifactPath,
              name: result.name,
              visibility: result.visibility,
              requiresBuild: result.requiresBuild,
              manifest: result.manifest,
            }),
          }],
        };
      } catch (error) {
        return {
          content: [{
            type: 'text' as const,
            text: `Failed to hoist skill: ${error instanceof Error ? error.message : 'Unknown error'}`,
          }],
          isError: true,
        };
      }
    },
  ),
};

export default hoistSkillPlugin;
