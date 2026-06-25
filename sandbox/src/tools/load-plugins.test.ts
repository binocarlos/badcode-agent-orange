import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { loadProductPlugins } from './load-plugins.js';
import type { ToolPlugin } from './registry.js';

describe('loadProductPlugins', () => {
  let dir: string;
  const registered: ToolPlugin[] = [];
  const fakeRegistry = { register: (p: ToolPlugin) => registered.push(p) };

  beforeEach(async () => {
    registered.length = 0;
    dir = await mkdtemp(path.join(tmpdir(), 'plugins-'));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it('returns 0 and registers nothing when dir is empty string', async () => {
    const n = await loadProductPlugins('', fakeRegistry);
    expect(n).toBe(0);
    expect(registered).toHaveLength(0);
  });

  it('returns 0 when dir does not exist (no throw)', async () => {
    const n = await loadProductPlugins(path.join(dir, 'nope'), fakeRegistry);
    expect(n).toBe(0);
  });

  it('registers a plugin exported as default object', async () => {
    await writeFile(
      path.join(dir, 'one.mjs'),
      `export default { name: 'one', sdkTool: { name: 'one' } };`,
    );
    const n = await loadProductPlugins(dir, fakeRegistry);
    expect(n).toBe(1);
    expect(registered.map((p) => p.name)).toEqual(['one']);
  });

  it('registers all plugins from a default array export', async () => {
    await writeFile(
      path.join(dir, 'many.mjs'),
      `export default [
        { name: 'a', sdkTool: { name: 'a' } },
        { name: 'b', sdkTool: { name: 'b' } },
      ];`,
    );
    const n = await loadProductPlugins(dir, fakeRegistry);
    expect(n).toBe(2);
    expect(registered.map((p) => p.name).sort()).toEqual(['a', 'b']);
  });

  it('skips files that throw on import without aborting the rest', async () => {
    await writeFile(path.join(dir, 'bad.mjs'), `throw new Error('boom');`);
    await writeFile(
      path.join(dir, 'good.mjs'),
      `export default { name: 'good', sdkTool: { name: 'good' } };`,
    );
    const n = await loadProductPlugins(dir, fakeRegistry);
    expect(n).toBe(1);
    expect(registered.map((p) => p.name)).toEqual(['good']);
  });

  it('ignores non-plugin exports (no name/sdkTool)', async () => {
    await writeFile(path.join(dir, 'junk.mjs'), `export default { hello: 'world' };`);
    const n = await loadProductPlugins(dir, fakeRegistry);
    expect(n).toBe(0);
  });

  it('dedupes by name across files (barrel + individual files)', async () => {
    // Simulate a barrel that exports both plugins...
    await writeFile(
      path.join(dir, 'index.mjs'),
      `export default [
        { name: 'render_table', sdkTool: { name: 'render_table' } },
        { name: 'render_chart', sdkTool: { name: 'render_chart' } },
      ];`,
    );
    // ...and individual files that re-export the same plugins by name.
    await writeFile(
      path.join(dir, 'render_table.mjs'),
      `export default { name: 'render_table', sdkTool: { name: 'render_table' } };`,
    );
    await writeFile(
      path.join(dir, 'render_chart.mjs'),
      `export default { name: 'render_chart', sdkTool: { name: 'render_chart' } };`,
    );
    const n = await loadProductPlugins(dir, fakeRegistry);
    expect(n).toBe(2);
    expect(registered.map((p) => p.name).sort()).toEqual(['render_chart', 'render_table']);
  });
});
