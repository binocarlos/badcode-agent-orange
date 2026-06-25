import { describe, it, expect, vi, beforeEach } from 'vitest';

describe('shared env + table URLs', () => {
  beforeEach(() => { vi.resetModules(); });

  it('uses HOST_API_URL with the /agent-gw base and scope-less run URL', async () => {
    process.env.HOST_API_URL = 'http://host/api/v1';
    process.env.SESSION_TOKEN = 'tok';
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({}), { status: 200, headers: { 'X-Agent-Customer': 'acme', 'X-Agent-Job': 'q1' } }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAndRunTable } = await import('./shared.js');
    await fetchAndRunTable({ spec: '{"top":"a","side":"b"}', title: 't' });
    const calledURL = String((fetchMock.mock.calls as unknown[][])[0][0]);
    expect(calledURL).toContain('/api/v1/agent-gw/tables/run');
    expect(calledURL).not.toContain('/acme/');
  });
});
