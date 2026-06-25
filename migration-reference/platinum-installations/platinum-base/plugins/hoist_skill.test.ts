import { describe, it, expect } from 'vitest';
import { mkdtempSync, writeFileSync, mkdirSync, readFileSync, existsSync, rmSync } from 'fs';
import { tmpdir } from 'os';
import { join } from 'path';
import { isValidSkillName, buildFrontmatter, buildManifest, writeSkillBundle, hoistSkillPlugin } from './hoist_skill.js';

describe('hoist_skill pure builders', () => {
  it('isValidSkillName accepts kebab-case, rejects others', () => {
    expect(isValidSkillName('my-skill')).toBe(true);
    expect(isValidSkillName('skill')).toBe(true);
    expect(isValidSkillName('a1-b2')).toBe(true);
    expect(isValidSkillName('My-Skill')).toBe(false);
    expect(isValidSkillName('my_skill')).toBe(false);
    expect(isValidSkillName('my skill')).toBe(false);
    expect(isValidSkillName('-leading')).toBe(false);
    expect(isValidSkillName('')).toBe(false);
  });

  it('buildFrontmatter emits valid YAML frontmatter with all fields', () => {
    const fm = buildFrontmatter({
      name: 'graph-gen',
      description: 'Generate graphs',
      triggers: ['make a graph', 'plot'],
      keywords: ['graph', 'chart'],
    });
    expect(fm.startsWith('---\n')).toBe(true);
    expect(fm.trimEnd().endsWith('---')).toBe(true);
    expect(fm).toContain('name: graph-gen');
    expect(fm).toContain('description: Generate graphs');
    expect(fm).toContain('  - make a graph');
    expect(fm).toContain('keywords: [graph, chart]');
  });

  it('buildManifest sets requiresBuild from install script presence', () => {
    const withBuild = buildManifest({
      name: 'g', description: 'd', triggers: [], keywords: [],
      bundledFiles: [{ src: 'scripts/g.py', dest: 'scripts/g.py' }],
      installScript: 'apt-get install -y ffmpeg', visibility: 'organizational',
    });
    expect(withBuild.requiresBuild).toBe(true);
    expect(withBuild.installScript).toBe('install.sh');
    expect(withBuild.visibility).toBe('organizational');
    expect(withBuild.bundledFiles).toEqual([{ src: 'scripts/g.py', dest: 'scripts/g.py' }]);

    const noBuild = buildManifest({
      name: 'g', description: 'd', triggers: [], keywords: [],
      bundledFiles: [], installScript: '   ', visibility: 'private',
    });
    expect(noBuild.requiresBuild).toBe(false);
    expect(noBuild.installScript).toBeUndefined();
  });
});

describe('writeSkillBundle', () => {
  function makeWorkspace(): string {
    const ws = mkdtempSync(join(tmpdir(), 'hoist-ws-'));
    mkdirSync(join(ws, 'scripts'), { recursive: true });
    writeFileSync(join(ws, 'scripts', 'gen.py'), 'print("hi")');
    return ws;
  }

  it('writes SKILL.md, manifest, install.sh, and copied bundled files', async () => {
    const ws = makeWorkspace();
    try {
      const result = await writeSkillBundle(ws, {
        name: 'graph-gen',
        description: 'Generate graphs',
        triggers: ['plot'],
        keywords: ['graph'],
        body: '# Graph Gen\n\nRun scripts/gen.py.',
        bundledFiles: [{ src: 'scripts/gen.py', dest: 'scripts/gen.py' }],
        installScript: 'apt-get install -y ffmpeg',
        visibility: 'organizational',
      });

      const bundleDir = join(ws, result.artifactPath);
      const skillMd = readFileSync(join(bundleDir, 'SKILL.md'), 'utf8');
      expect(skillMd).toContain('name: graph-gen');
      expect(skillMd).toContain('# Graph Gen');
      const manifest = JSON.parse(readFileSync(join(bundleDir, 'skill.manifest.json'), 'utf8'));
      expect(manifest.requiresBuild).toBe(true);
      expect(manifest.bundledFiles).toEqual([{ src: 'scripts/gen.py', dest: 'scripts/gen.py' }]);
      expect(readFileSync(join(bundleDir, 'install.sh'), 'utf8')).toContain('ffmpeg');
      expect(readFileSync(join(bundleDir, 'files', 'scripts', 'gen.py'), 'utf8')).toBe('print("hi")');
      expect(result.name).toBe('graph-gen');
      expect(result.requiresBuild).toBe(true);
      expect(result.artifactPath).toBe('.hoisted-skills/graph-gen');
    } finally {
      rmSync(ws, { recursive: true, force: true });
    }
  });

  it('omits install.sh when no install script, and errors on a missing bundled file', async () => {
    const ws = makeWorkspace();
    try {
      const ok = await writeSkillBundle(ws, {
        name: 'no-build', description: 'd', triggers: [], keywords: [],
        body: 'body', bundledFiles: [], visibility: 'private',
      });
      expect(existsSync(join(ws, ok.artifactPath, 'install.sh'))).toBe(false);

      await expect(writeSkillBundle(ws, {
        name: 'broken', description: 'd', triggers: [], keywords: [],
        body: 'body', bundledFiles: [{ src: 'does/not/exist.py', dest: 'x.py' }],
        visibility: 'private',
      })).rejects.toThrow(/does\/not\/exist\.py/);
    } finally {
      rmSync(ws, { recursive: true, force: true });
    }
  });

  it('accepts an absolute src that points inside the workspace', async () => {
    const ws = makeWorkspace();
    try {
      const result = await writeSkillBundle(ws, {
        name: 'abs-src', description: 'd', triggers: [], keywords: [],
        body: 'body',
        bundledFiles: [{ src: join(ws, 'scripts', 'gen.py'), dest: 'gen.py' }],
        visibility: 'private',
      });
      const bundleDir = join(ws, result.artifactPath);
      // copied to files/<dest>
      expect(readFileSync(join(bundleDir, 'files', 'gen.py'), 'utf8')).toBe('print("hi")');
      // manifest src normalized to workspace-relative
      const manifest = JSON.parse(readFileSync(join(bundleDir, 'skill.manifest.json'), 'utf8'));
      expect(manifest.bundledFiles).toEqual([{ src: 'scripts/gen.py', dest: 'gen.py' }]);
    } finally {
      rmSync(ws, { recursive: true, force: true });
    }
  });

  it('rejects a bundled file dest that escapes the bundle dir', async () => {
    const ws = makeWorkspace();
    try {
      await expect(writeSkillBundle(ws, {
        name: 'escaper', description: 'd', triggers: [], keywords: [],
        body: 'body', bundledFiles: [{ src: 'scripts/gen.py', dest: '../../../escape.py' }],
        visibility: 'private',
      })).rejects.toThrow(/escapes/);
    } finally { rmSync(ws, { recursive: true, force: true }); }
  });

  it('rejects a bundled file src that escapes the workspace', async () => {
    const ws = makeWorkspace();
    try {
      await expect(writeSkillBundle(ws, {
        name: 'escaper2', description: 'd', triggers: [], keywords: [],
        body: 'body', bundledFiles: [{ src: '../../outside.py', dest: 'x.py' }],
        visibility: 'private',
      })).rejects.toThrow(/escapes/);
    } finally { rmSync(ws, { recursive: true, force: true }); }
  });
});

describe('hoist_skill plugin wiring', () => {
  it('has the right name and marker wiring', () => {
    expect(hoistSkillPlugin.name).toBe('hoist_skill');
    expect(hoistSkillPlugin.marker?.key).toBe('__skill_hoisted');
    expect(hoistSkillPlugin.marker?.event).toBe('skill_hoisted');
  });

  it('marker.toEvent passes through the payload fields', () => {
    const payload = {
      __skill_hoisted: true,
      artifactPath: '.hoisted-skills/g',
      name: 'g',
      visibility: 'organizational',
      requiresBuild: true,
      manifest: { name: 'g' },
    };
    const ev = hoistSkillPlugin.marker!.toEvent(payload);
    expect(ev.artifactPath).toBe('.hoisted-skills/g');
    expect(ev.name).toBe('g');
    expect(ev.visibility).toBe('organizational');
    expect(ev.requiresBuild).toBe(true);
    expect(ev.manifest).toEqual({ name: 'g' });
  });

  it('marker.toModelText is compact, non-JSON, names the skill', () => {
    const txt = hoistSkillPlugin.marker!.toModelText({ name: 'graph-gen', requiresBuild: false });
    expect(typeof txt).toBe('string');
    expect(txt).not.toContain('{');
    expect(txt).toContain('graph-gen');
  });
});
