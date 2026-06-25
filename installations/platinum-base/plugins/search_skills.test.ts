import { describe, it, expect, vi, beforeEach } from 'vitest';

describe('search_skills', () => {
  beforeEach(() => vi.resetModules());
  it('calls the gateway with q and returns names', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({
      skills: [{ id: 's1', name: 'alpha', description: 'd', visibility: 'organizational', requires_build: false }], count: 1,
    }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const { searchSkillsPlugin } = await import('./search_skills.js');
    const res = await searchSkillsPlugin.sdkTool.handler({ query: 'alph' }, {} as any);
    const calls = fetchMock.mock.calls as unknown as [string, RequestInit?][];
    expect(calls[0][0]).toContain('/skills?q=alph');
    expect(res.content[0].text).toContain('alpha');
  });
});
