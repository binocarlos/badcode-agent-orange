import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderTablePlugin } from './render_table.js';

describe('render_table plugin', () => {
  beforeEach(() => { vi.resetModules(); });

  it('has the right name and marker wiring', () => {
    expect(renderTablePlugin.name).toBe('render_table');
    expect(renderTablePlugin.marker?.key).toBe('__render_table');
    expect(renderTablePlugin.marker?.event).toBe('table_rendered');
  });

  it('marker.toEvent maps payload → SSE event data', () => {
    const payload = {
      __render_table: true,
      platinumData: { cells: [] },
      title: 'Awareness',
      customer: 'acme',
      job: 'wave1',
      spec: '{"top":"Age"}',
    };
    const ev = renderTablePlugin.marker!.toEvent(payload);
    expect(ev.title).toBe('Awareness');
    expect(ev.customer).toBe('acme');
    expect(ev.job).toBe('wave1');
    expect(ev.platinumData).toEqual({ cells: [] });
  });

  it('marker.toModelText returns compact non-JSON text', () => {
    const txt = renderTablePlugin.marker!.toModelText({ title: 'Awareness' });
    expect(typeof txt).toBe('string');
    expect(txt).not.toContain('platinumData');
    expect(txt.toLowerCase()).toContain('table');
  });

  it('tool schema does not declare customer or job', () => {
    const schema = (renderTablePlugin.sdkTool as unknown as { inputSchema: Record<string, unknown> }).inputSchema;
    expect(schema).not.toHaveProperty('customer');
    expect(schema).not.toHaveProperty('job');
  });

  it('marker carries customer/job from response headers (via fetchAndRunTable)', async () => {
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
