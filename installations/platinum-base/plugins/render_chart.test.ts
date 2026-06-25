import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderChartPlugin } from './render_chart.js';

describe('render_chart plugin', () => {
  beforeEach(() => { vi.resetModules(); });

  it('wires the chart marker', () => {
    expect(renderChartPlugin.name).toBe('render_chart');
    expect(renderChartPlugin.marker?.key).toBe('__render_chart');
    expect(renderChartPlugin.marker?.event).toBe('chart_rendered');
  });

  it('toModelText includes chart keyword', () => {
    const txt = renderChartPlugin.marker!.toModelText({ title: 'Awareness' });
    expect(txt.toLowerCase()).toContain('chart');
  });

  it('tool schema does not declare customer or job', () => {
    const schema = (renderChartPlugin.sdkTool as unknown as { inputSchema: Record<string, unknown> }).inputSchema;
    expect(schema).not.toHaveProperty('customer');
    expect(schema).not.toHaveProperty('job');
  });

  it('marker carries customer/job from response headers', async () => {
    process.env.HOST_API_URL = 'http://host/api/v1';
    process.env.SESSION_TOKEN = 'tok';
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({}), {
      status: 200,
      headers: { 'X-Agent-Customer': 'acme', 'X-Agent-Job': 'q1' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAndRunTable } = await import('./shared.js');
    const result = await fetchAndRunTable({ spec: '{"top":"a","side":"b"}', title: 'Test' });
    const marker = JSON.parse(result[0].text);
    expect(marker.customer).toBe('acme');
    expect(marker.job).toBe('q1');
  });
});
