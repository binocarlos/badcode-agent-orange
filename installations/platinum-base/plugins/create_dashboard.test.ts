import { describe, it, expect } from 'vitest';
import { createDashboardPlugin } from './create_dashboard.js';

describe('create_dashboard plugin', () => {
  it('wires the dashboard marker', () => {
    expect(createDashboardPlugin.name).toBe('create_dashboard');
    expect(createDashboardPlugin.marker?.key).toBe('__dashboard_created');
    expect(createDashboardPlugin.marker?.event).toBe('dashboard_created');
  });
});
