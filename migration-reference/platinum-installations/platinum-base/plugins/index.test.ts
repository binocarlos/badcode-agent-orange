import { describe, it, expect } from 'vitest';
import plugins from './index.js';

describe('sandbox-plugins barrel', () => {
  it('exports all Platinum tool plugins as a flat array', () => {
    const names = plugins.map((p) => p.name).sort();
    expect(names).toEqual(
      ['create_dashboard', 'generate_pptx', 'hoist_skill', 'install_skill', 'register_artifact', 'render_chart', 'render_table', 'render_tables', 'search_skills'].sort(),
    );
  });
  it('every plugin has a name and sdkTool', () => {
    for (const p of plugins) {
      expect(typeof p.name).toBe('string');
      expect(p.sdkTool).toBeTruthy();
    }
  });
});
