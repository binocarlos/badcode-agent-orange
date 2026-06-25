import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import type { ToolPlugin, MarkerSpec } from './types.js';
import { env } from './shared.js';

const execFileP = promisify(execFile);

function assertContained(base: string, resolved: string, label: string): void {
  const b = path.resolve(base), r = path.resolve(resolved);
  if (r !== b && !r.startsWith(b + path.sep)) throw new Error(`${label} escapes the allowed directory`);
}

interface BundleResponse { name: string; files: Array<{ path: string; content: string }> }

async function resolveSkill(name: string, explicitId?: string): Promise<{ id: string; visibility?: string; requiresBuild?: boolean }> {
  if (explicitId) return { id: explicitId };
  const resp = await fetch(`${env.HOST_API_URL}/skills?q=${encodeURIComponent(name)}`, { headers: { Authorization: `Bearer ${env.SESSION_TOKEN}` } });
  if (!resp.ok) throw new Error(`skill lookup failed (${resp.status})`);
  const body = await resp.json() as { skills: Array<{ id: string; name: string; visibility?: string; requires_build?: boolean }> };
  const m = body.skills.find(s => s.name === name) ?? body.skills[0];
  if (!m) throw new Error(`no skill named "${name}" is visible to this session`);
  return { id: m.id, visibility: m.visibility, requiresBuild: m.requires_build };
}

const installSkillMarker: MarkerSpec = {
  key: '__skill_installed',
  event: 'skill_installed',
  toEvent: (p) => ({ id: p.id, name: p.name, visibility: p.visibility, requires_build: p.requiresBuild, installLog: p.installLog }),
  toModelText: (p) => `Installed skill "${p.name}" into the session. It is usable from your next message.`,
};

export const installSkillPlugin: ToolPlugin = {
  name: 'install_skill',
  marker: installSkillMarker,
  sdkTool: tool(
    'install_skill',
    'Install a reusable skill from the catalog into THIS running session. Downloads the bundle, lays it into the workspace, and runs its install step if present. Usable from the NEXT message. Use search_skills first to find the exact name.',
    { name: z.string().min(1).describe('Skill name (kebab-case)'), id: z.string().optional().describe('Optional skill id to disambiguate') },
    async (args) => {
      const workspaceRoot = process.env.WORKSPACE_DIR ?? '/workspace';
      const name = args.name as string;
      try {
        const { id, visibility, requiresBuild } = await resolveSkill(name, args.id as string | undefined);
        const bundleResp = await fetch(`${env.HOST_API_URL}/skills/${encodeURIComponent(id)}/bundle`, { headers: { Authorization: `Bearer ${env.SESSION_TOKEN}` } });
        if (!bundleResp.ok) return { content: [{ type: 'text' as const, text: `Failed to fetch bundle (${bundleResp.status}): ${await bundleResp.text()}` }], isError: true };
        const bundle = await bundleResp.json() as BundleResponse;

        const skillDir = path.join(workspaceRoot, '.claude', 'skills', name);
        await fs.rm(skillDir, { recursive: true, force: true });
        await fs.mkdir(skillDir, { recursive: true });
        let hasInstall = false;
        for (const f of bundle.files) {
          const dest = path.join(skillDir, f.path);
          assertContained(skillDir, dest, `bundle file "${f.path}"`);
          await fs.mkdir(path.dirname(dest), { recursive: true });
          await fs.writeFile(dest, Buffer.from(f.content, 'base64'));
          if (f.path === 'install.sh') hasInstall = true;
        }
        let installLog: string | undefined;
        if (hasInstall) {
          await fs.chmod(path.join(skillDir, 'install.sh'), 0o755);
          try {
            const { stdout, stderr } = await execFileP('sh', ['-c', 'cd "$0" && sh ./install.sh 2>&1', workspaceRoot]);
            installLog = (stdout || '') + (stderr || '');
          } catch (e: any) {
            return { content: [{ type: 'text' as const, text: `install.sh failed for "${name}": ${e?.stdout || e?.message || 'unknown error'}` }], isError: true };
          }
        }
        return { content: [{ type: 'text' as const, text: JSON.stringify({ __skill_installed: true, id, name, visibility, requiresBuild, installLog }) }] };
      } catch (e) {
        return { content: [{ type: 'text' as const, text: `Failed to install skill "${name}": ${e instanceof Error ? e.message : 'unknown error'}` }], isError: true };
      }
    },
  ),
};

export default installSkillPlugin;
