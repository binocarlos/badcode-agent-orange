import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

describe('install_skill', () => {
  let workspace: string;
  beforeEach(async () => { vi.resetModules(); workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'inst-')); process.env.WORKSPACE_DIR = workspace; });
  afterEach(async () => { await fs.rm(workspace, { recursive: true, force: true }); });

  const b64 = (s: string) => Buffer.from(s).toString('base64');

  it('lays the bundle and emits the marker', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes('/skills?q=')) return new Response(JSON.stringify({ skills: [{ id: 's1', name: 'hello-skill', visibility: 'organizational', requires_build: false }] }), { status: 200 });
      return new Response(JSON.stringify({ name: 'hello-skill', files: [
        { path: 'SKILL.md', content: b64('# hello') },
        { path: 'files/scripts/hello.py', content: b64("print('x')") },
      ] }), { status: 200 });
    }));
    const { installSkillPlugin } = await import('./install_skill.js');
    const res = await installSkillPlugin.sdkTool.handler({ name: 'hello-skill' }, {} as any);
    expect(await fs.readFile(path.join(workspace, '.claude/skills/hello-skill/SKILL.md'), 'utf8')).toContain('# hello');
    expect(await fs.readFile(path.join(workspace, '.claude/skills/hello-skill/files/scripts/hello.py'), 'utf8')).toContain("print('x')");
    expect(res.content[0].text).toContain('__skill_installed');
    expect(res.content[0].text).toContain('hello-skill');
  });

  it('rejects a path that escapes the skill dir', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.includes('/skills?q=')) return new Response(JSON.stringify({ skills: [{ id: 's1', name: 'evil', visibility: 'organizational', requires_build: false }] }), { status: 200 });
      return new Response(JSON.stringify({ name: 'evil', files: [{ path: '../../escape.sh', content: b64('x') }] }), { status: 200 });
    }));
    const { installSkillPlugin } = await import('./install_skill.js');
    const res = await installSkillPlugin.sdkTool.handler({ name: 'evil' }, {} as any);
    expect(res.isError).toBe(true);
    expect(res.content[0].text).toContain('escapes');
  });
});
