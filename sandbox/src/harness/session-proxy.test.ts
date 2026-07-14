// Regression: the per-session proxy must preserve the upstream base URL's path
// prefix (e.g. <agentd>/agent-proxy) — dropping it lands model calls on the
// host's authenticated routes (401) instead of the model proxy.
import { describe, it, expect, afterEach } from 'vitest';
import http from 'node:http';
import type { AddressInfo } from 'node:net';
import { startSessionProxy } from './claude-agent-sdk.js';

function startUpstream(): Promise<{ server: http.Server; url: string; seen: { url?: string; sessionId?: string } }> {
  const seen: { url?: string; sessionId?: string } = {};
  const server = http.createServer((req, res) => {
    seen.url = req.url;
    seen.sessionId = req.headers['x-session-id'] as string;
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end('{"ok":true}');
  });
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address() as AddressInfo;
      resolve({ server, url: `http://127.0.0.1:${port}`, seen });
    });
  });
}

describe('startSessionProxy', () => {
  const closers: Array<() => void> = [];
  afterEach(() => {
    while (closers.length) closers.pop()!();
  });

  it('keeps the upstream path prefix and injects x-session-id', async () => {
    const upstream = await startUpstream();
    closers.push(() => upstream.server.close());
    const proxy = await startSessionProxy('sess-1', `${upstream.url}/agent-proxy`);
    closers.push(proxy.close);

    const resp = await fetch(`${proxy.baseURL}/v1/messages`, { method: 'POST', body: '{}' });
    expect(resp.status).toBe(200);
    expect(upstream.seen.url).toBe('/agent-proxy/v1/messages');
    expect(upstream.seen.sessionId).toBe('sess-1');
  });

  it('handles a prefix-less upstream unchanged', async () => {
    const upstream = await startUpstream();
    closers.push(() => upstream.server.close());
    const proxy = await startSessionProxy('sess-2', upstream.url);
    closers.push(proxy.close);

    const resp = await fetch(`${proxy.baseURL}/v1/messages`, { method: 'POST', body: '{}' });
    expect(resp.status).toBe(200);
    expect(upstream.seen.url).toBe('/v1/messages');
  });
});
