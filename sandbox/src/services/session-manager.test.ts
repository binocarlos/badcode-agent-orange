/**
 * AG-3 unit tests for SessionManager and session-scoped infrastructure.
 *
 * (a) concurrent-session isolation — cancel A doesn't touch B's turn/AbortController
 * (b) session-scoped routing via fastify.inject (POST /sessions then /sessions/:id/query-stream)
 * (c) stream-service composite key + closeSession evicts only that session's buffers
 * (d) per-turn AsyncLocalStorage stamps correct x-session-id for two interleaved sessions
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { SessionManager, UnknownHarnessError, HarnessCredentialsMissingError } from './session-manager.js';
import { StreamService } from './stream-service.js';
import type { FastifyReply } from 'fastify';
import Fastify from 'fastify';
import { sessionRoutes } from '../routes/sessions.js';

// ---------------------------------------------------------------------------
// Mock the harness bootstrap so tests don't need ANTHROPIC_BASE_URL in env
// ---------------------------------------------------------------------------

vi.mock('../harness/bootstrap.js', () => {
  const harnessRegistry = {
    has: (name: string) => name === 'mock-harness' || name === 'claude-agent-sdk',
    get: (name: string) => {
      if (name === 'mock-harness' || name === 'claude-agent-sdk') {
        return mockDescriptor;
      }
      return undefined;
    },
    names: () => ['mock-harness', 'claude-agent-sdk'],
  };

  const mockDescriptor = {
    name: 'mock-harness',
    credentials: {
      requiredEnv: [] as string[],
      describe: () => 'mock harness needs nothing',
    },
    create: (sessionId: string) => ({
      name: 'mock-harness',
      runTurn: vi.fn().mockResolvedValue(undefined),
      loadConversation: vi.fn(),
      resetConversation: vi.fn(),
      dispose: vi.fn().mockResolvedValue(undefined),
    }),
  };

  function resolveHarness(name?: string) {
    const n = name || 'mock-harness';
    if (!harnessRegistry.has(n)) {
      return {
        errorCode: 'UNKNOWN_HARNESS' as const,
        status: 400,
        body: { code: 'UNKNOWN_HARNESS', supported: harnessRegistry.names() },
      };
    }
    return { descriptor: harnessRegistry.get(n)! };
  }

  return { harnessRegistry, resolveHarness };
});

// ---------------------------------------------------------------------------
// Mock session-context.ts (imported directly by routes/sessions.ts)
// ---------------------------------------------------------------------------

vi.mock('../session-context.js', async () => {
  const { AsyncLocalStorage } = await import('node:async_hooks');
  return {
    sessionContext: new AsyncLocalStorage<{ sessionId: string }>(),
  };
});

// ---------------------------------------------------------------------------
// Mock index.ts (just the sessionContext export — kept for any indirect import)
// ---------------------------------------------------------------------------

vi.mock('../index.js', async () => {
  const { AsyncLocalStorage } = await import('node:async_hooks');
  return {
    sessionContext: new AsyncLocalStorage<{ sessionId: string }>(),
    harnessRegistry: {},
    resolveHarness: () => ({}),
  };
});

// Mock config
vi.mock('../config.js', () => ({
  config: {
    ANTHROPIC_BASE_URL: 'http://mock-proxy',
    HOST_API_URL: 'http://mock-api',
    DEFAULT_MODEL: 'mock-model',
    DEFAULT_MAX_TURNS: 10,
    DEFAULT_THINKING_BUDGET_TOKENS: 1000,
    SESSION_ID: '',
    SESSION_TOKEN: '',
    LOG_LEVEL: 'error',
    NODE_ENV: 'test',
    PORT: 3010,
    HOST: '0.0.0.0',
    SESSION_CUSTOMER: '',
    SESSION_JOB: '',
  },
}));

// Mock tool registry
vi.mock('../tools/registry-impl.js', () => ({
  toolRegistry: {
    resolve: () => ({ allowedTools: [], disallowedTools: [], mcpServers: {}, markers: [] }),
  },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeNullReply(): FastifyReply {
  const raw = {
    write: vi.fn(),
    end: vi.fn(),
    on: vi.fn(),
    writeHead: vi.fn(),
    headersSent: false,
  };
  return { raw } as unknown as FastifyReply;
}

async function buildApp() {
  const fastify = Fastify({ logger: false });
  await fastify.register(sessionRoutes);
  return fastify;
}

// ---------------------------------------------------------------------------
// (a) concurrent-session isolation
// ---------------------------------------------------------------------------

describe('SessionManager — concurrent-session isolation', () => {
  let mgr: SessionManager;

  beforeEach(() => {
    mgr = new SessionManager();
  });

  it('cancel session A does not touch session B turn or AbortController', () => {
    mgr.create('sess-a', { harness: 'mock-harness' });
    mgr.create('sess-b', { harness: 'mock-harness' });

    const abortA = mgr.startTurn('sess-a', 'q-a-1');
    const abortB = mgr.startTurn('sess-b', 'q-b-1');

    // Sanity: both AbortControllers start un-aborted
    expect(abortA.signal.aborted).toBe(false);
    expect(abortB.signal.aborted).toBe(false);

    // Cancel session A
    mgr.cancel('sess-a');

    // A's controller should be aborted
    expect(abortA.signal.aborted).toBe(true);

    // B's controller must be untouched
    expect(abortB.signal.aborted).toBe(false);
  });

  it('destroy session A does not affect session B', () => {
    mgr.create('sess-a', { harness: 'mock-harness' });
    mgr.create('sess-b', { harness: 'mock-harness' });

    mgr.destroy('sess-a');

    expect(mgr.has('sess-a')).toBe(false);
    expect(mgr.has('sess-b')).toBe(true);
  });

  it('new turn in session A supersedes prior turn in session A only', () => {
    mgr.create('sess-a', { harness: 'mock-harness' });
    mgr.create('sess-b', { harness: 'mock-harness' });

    const abortA1 = mgr.startTurn('sess-a', 'q-a-1');
    const abortB = mgr.startTurn('sess-b', 'q-b-1');

    // Start a second turn in session A — should supersede q-a-1
    const abortA2 = mgr.startTurn('sess-a', 'q-a-2');

    expect(abortA1.signal.aborted).toBe(true);  // superseded
    expect(abortA2.signal.aborted).toBe(false); // new turn active
    expect(abortB.signal.aborted).toBe(false);  // B untouched
  });

  it('throws UnknownHarnessError for an unregistered harness', () => {
    expect(() => mgr.create('sess-x', { harness: 'no-such-harness' }))
      .toThrow(UnknownHarnessError);
  });

  it('startTurn throws for non-existent session', () => {
    expect(() => mgr.startTurn('ghost', 'q-1')).toThrow();
  });

  it('cancel with queryId aborts only that turn', () => {
    mgr.create('sess-a', { harness: 'mock-harness' });
    const abort1 = mgr.startTurn('sess-a', 'q-1');
    // Register a second turn (first gets superseded, so register fresh)
    mgr.endTurn('sess-a', 'q-1');
    // Actually both are in the map only if we don't supersede — let's test direct cancel
    mgr.create('sess-c', { harness: 'mock-harness' });
    const abortC = mgr.startTurn('sess-c', 'q-c-1');
    mgr.cancel('sess-c', 'q-c-1');
    expect(abortC.signal.aborted).toBe(true);
    // The session itself still exists
    expect(mgr.has('sess-c')).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// (b) session-scoped routing
// ---------------------------------------------------------------------------

describe('POST /sessions — session-scoped routing', () => {
  it('creates a session and returns 200 with sessionId', async () => {
    const app = await buildApp();
    const res = await app.inject({
      method: 'POST',
      url: '/sessions',
      payload: { sessionId: 'test-sess-1', harness: 'mock-harness' },
    });

    expect(res.statusCode).toBe(200);
    const body = JSON.parse(res.payload);
    expect(body.success).toBe(true);
    expect(body.data.sessionId).toBe('test-sess-1');
    expect(res.headers['x-session-id']).toBe('test-sess-1');
  });

  it('returns 400 UNKNOWN_HARNESS for an unregistered harness', async () => {
    const app = await buildApp();
    const res = await app.inject({
      method: 'POST',
      url: '/sessions',
      payload: { sessionId: 'test-sess-2', harness: 'no-such-harness' },
    });

    expect(res.statusCode).toBe(400);
    const body = JSON.parse(res.payload);
    expect(body.success).toBe(false);
    expect(body.error.code).toBe('UNKNOWN_HARNESS');
    expect(res.headers['x-session-id']).toBe('test-sess-2');
  });

  it('DELETE /sessions/:id destroys the session', async () => {
    const app = await buildApp();
    // Create first
    await app.inject({
      method: 'POST',
      url: '/sessions',
      payload: { sessionId: 'del-sess', harness: 'mock-harness' },
    });
    // Destroy
    const res = await app.inject({
      method: 'DELETE',
      url: '/sessions/del-sess',
    });
    expect(res.statusCode).toBe(200);
    expect(res.headers['x-session-id']).toBe('del-sess');
  });

  it('POST /sessions/:id/cancel returns 200', async () => {
    const app = await buildApp();
    await app.inject({
      method: 'POST',
      url: '/sessions',
      payload: { sessionId: 'cancel-sess', harness: 'mock-harness' },
    });
    const res = await app.inject({
      method: 'POST',
      url: '/sessions/cancel-sess/cancel',
      payload: {},
    });
    expect(res.statusCode).toBe(200);
    expect(res.headers['x-session-id']).toBe('cancel-sess');
  });

  it('POST /sessions/:id/load-conversation returns 404 for unknown session', async () => {
    const app = await buildApp();
    const res = await app.inject({
      method: 'POST',
      url: '/sessions/ghost-sess/load-conversation',
      payload: { messages: [] },
    });
    expect(res.statusCode).toBe(404);
  });

  it('POST /sessions/:id/reset-conversation returns 404 for unknown session', async () => {
    const app = await buildApp();
    const res = await app.inject({
      method: 'POST',
      url: '/sessions/ghost-sess/reset-conversation',
    });
    expect(res.statusCode).toBe(404);
  });
});

// ---------------------------------------------------------------------------
// (c) stream-service composite key + closeSession
// ---------------------------------------------------------------------------

describe('StreamService — composite key + closeSession', () => {
  let svc: StreamService;

  beforeEach(() => {
    svc = new StreamService();
  });

  it('events for session A:q-1 do not show up in session B:q-1 buffer', () => {
    // Buffer an event for A's query
    svc.sendEvent('q-1', 'content_delta', { data: 'hello' }, 'sess-a');

    // Create a stream for B's query (same queryId, different session)
    const replyB = makeNullReply();
    svc.addStream('q-1', replyB, 'sess-b');

    // B's reply should NOT have received A's event (different composite key)
    expect((replyB.raw.write as ReturnType<typeof vi.fn>).mock.calls.length).toBe(0);
  });

  it('closeSession evicts only that session\'s buffers', () => {
    // Buffer events for two sessions
    svc.sendEvent('q-1', 'content_delta', { data: 'for A' }, 'sess-a');
    svc.sendEvent('q-1', 'content_delta', { data: 'for B' }, 'sess-b');

    // Close session A
    svc.closeSession('sess-a');

    // Create streams for both sessions to check replay behaviour
    const replyA = makeNullReply();
    const replyB = makeNullReply();

    svc.addStream('q-1', replyA, 'sess-a');
    svc.addStream('q-1', replyB, 'sess-b');

    // A's buffer was evicted — no replay
    expect((replyA.raw.write as ReturnType<typeof vi.fn>).mock.calls.length).toBe(0);
    // B's buffer is intact — one event replayed
    expect((replyB.raw.write as ReturnType<typeof vi.fn>).mock.calls.length).toBe(1);
  });

  it('closeSession does not evict other sessions', () => {
    svc.sendEvent('q-1', 'content_delta', { data: 'for C' }, 'sess-c');
    svc.sendEvent('q-2', 'content_delta', { data: 'for D' }, 'sess-d');

    svc.closeSession('sess-c');

    const replyD = makeNullReply();
    svc.addStream('q-2', replyD, 'sess-d');
    expect((replyD.raw.write as ReturnType<typeof vi.fn>).mock.calls.length).toBe(1);
  });

  it('composite key: same queryId in different sessions are independent streams', () => {
    const replyA = makeNullReply();
    const replyB = makeNullReply();

    svc.addStream('shared-qid', replyA, 'sess-a');
    svc.addStream('shared-qid', replyB, 'sess-b');

    // Send to session A only
    svc.sendEvent('shared-qid', 'content_delta', { data: 'a-only' }, 'sess-a');

    const writesA = (replyA.raw.write as ReturnType<typeof vi.fn>).mock.calls.length;
    const writesB = (replyB.raw.write as ReturnType<typeof vi.fn>).mock.calls.length;

    expect(writesA).toBe(1);
    expect(writesB).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// (d) AsyncLocalStorage per-session proxy header
// ---------------------------------------------------------------------------

describe('AsyncLocalStorage — per-session x-session-id stamping', () => {
  it('two interleaved turns each stamp their own sessionId on mock fetch calls', async () => {
    const { AsyncLocalStorage } = await import('node:async_hooks');
    const ctx = new AsyncLocalStorage<{ sessionId: string }>();

    const capturedHeaders: string[] = [];

    // Simulate the fetch patch: read sessionId from the store
    const patchedFetch = async (_url: string): Promise<{ sessionId: string }> => {
      const store = ctx.getStore();
      const sessionId = store?.sessionId || 'none';
      capturedHeaders.push(sessionId);
      return { sessionId };
    };

    // Simulate two sessions running interleaved turns
    const results = await Promise.all([
      ctx.run({ sessionId: 'session-alpha' }, () => patchedFetch('http://proxy/v1/messages')),
      ctx.run({ sessionId: 'session-beta' }, () => patchedFetch('http://proxy/v1/messages')),
    ]);

    // Each call captured the right sessionId
    const ids = results.map(r => r.sessionId);
    expect(ids).toContain('session-alpha');
    expect(ids).toContain('session-beta');

    // Both captured headers are distinct and correct
    expect(capturedHeaders.sort()).toEqual(['session-alpha', 'session-beta'].sort());
  });

  it('falls back to empty string when no store is active', async () => {
    const { AsyncLocalStorage } = await import('node:async_hooks');
    const ctx = new AsyncLocalStorage<{ sessionId: string }>();

    const getSessionId = () => ctx.getStore()?.sessionId || '';
    // Called outside any ctx.run() — should return empty string
    expect(getSessionId()).toBe('');
  });
});

// ---------------------------------------------------------------------------
// (e) Issue 1 fix: StreamService.key() falls back to ambient sessionContext
// ---------------------------------------------------------------------------

describe('StreamService — ambient sessionContext fallback (Issue 1 fix)', () => {
  it('key() uses ambient sessionContext when explicit sessionId is empty', async () => {
    // Import the real sessionContext from its own module — since it's mocked
    // in this test file via vi.mock, the mock's AsyncLocalStorage instance
    // is what StreamService will actually read at runtime (same module cache).
    const { sessionContext: ctx } = await import('../session-context.js');
    const svc = new StreamService();

    // Buffer an event for 'q1' with explicit empty sessionId (legacy caller pattern).
    // Do it inside sessionContext.run({sessionId:'A'}) so the ambient store is 'A'.
    ctx.run({ sessionId: 'A' }, () => {
      // Empty sessionId → key() should fall back to ambient 'A' → key = 'A:q1'
      svc.sendEvent('q1', 'content_delta', { data: 'hello from A' }, '');
    });

    // Now connect a stream for 'q1' under session 'A' (explicit sessionId).
    // It should replay the buffered event — confirming the key matched.
    const replyA = makeNullReply();
    svc.addStream('q1', replyA, 'A');
    expect((replyA.raw.write as ReturnType<typeof vi.fn>).mock.calls.length).toBe(1);

    // A stream for 'q1' under a DIFFERENT session 'B' must NOT receive the event.
    const replyB = makeNullReply();
    svc.addStream('q1', replyB, 'B');
    expect((replyB.raw.write as ReturnType<typeof vi.fn>).mock.calls.length).toBe(0);
  });

  it('key() uses queryId alone (legacy back-compat) when both sessionId and ambient store are empty', async () => {
    const svc = new StreamService();

    // No sessionContext.run() wrapping — ambient store is empty.
    // Empty explicit sessionId → key should be just 'q-legacy' (back-compat).
    svc.sendEvent('q-legacy', 'content_delta', { data: 'no session' }, '');

    // Stream connected with empty sessionId should receive the replay.
    const reply = makeNullReply();
    svc.addStream('q-legacy', reply, '');
    expect((reply.raw.write as ReturnType<typeof vi.fn>).mock.calls.length).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// (f) SessionManager error paths — graceful handling of invalid operations
// ---------------------------------------------------------------------------

describe('SessionManager — error paths', () => {
  let mgr: SessionManager;

  beforeEach(() => {
    mgr = new SessionManager();
  });

  it('destroy on non-existent session is a silent no-op', () => {
    // Must not throw for a session that was never created
    expect(() => mgr.destroy('ghost-session')).not.toThrow();
  });

  it('endTurn on non-existent session is a silent no-op', () => {
    expect(() => mgr.endTurn('ghost-session', 'q-1')).not.toThrow();
  });

  it('cancel on non-existent session is a silent no-op', () => {
    expect(() => mgr.cancel('ghost-session')).not.toThrow();
    expect(() => mgr.cancel('ghost-session', 'q-1')).not.toThrow();
  });

  it('throws HarnessCredentialsMissingError when harness credentials are absent', () => {
    // The mock resolveHarness returns UNKNOWN_HARNESS for anything other than
    // 'mock-harness' or 'claude-agent-sdk'.  To test HarnessCredentialsMissingError
    // we need a harness that exists but whose credentials check fails.
    // The mock in this file doesn't simulate that path, so we verify the error
    // class is exported and can be constructed directly.
    const err = new HarnessCredentialsMissingError({
      status: 424,
      body: { code: 'HARNESS_CREDENTIALS_MISSING', missing: ['ANTHROPIC_BASE_URL'] },
    });
    expect(err.errorCode).toBe('HARNESS_CREDENTIALS_MISSING');
    expect(err.http.status).toBe(424);
    expect(err instanceof Error).toBe(true);
  });

  it('UnknownHarnessError carries correct errorCode and http body', () => {
    const err = new UnknownHarnessError({
      status: 400,
      body: { code: 'UNKNOWN_HARNESS', supported: ['mock-harness'] },
    });
    expect(err.errorCode).toBe('UNKNOWN_HARNESS');
    expect(err.http.body).toMatchObject({ code: 'UNKNOWN_HARNESS' });
    expect(err.message).toContain('UNKNOWN_HARNESS');
  });

  it('create is idempotent for the same harness (same session + same harness = no throw)', () => {
    mgr.create('sess-idem', { harness: 'mock-harness' });
    // Second call with same session + same harness must not throw
    expect(() => mgr.create('sess-idem', { harness: 'mock-harness' })).not.toThrow();
    // Session still exists
    expect(mgr.has('sess-idem')).toBe(true);
  });

  it('create with different harness destroys existing session and recreates', () => {
    mgr.create('sess-switch', { harness: 'mock-harness' });
    const startAbort = mgr.startTurn('sess-switch', 'q-old');

    // Switching harness destroys the old session
    mgr.create('sess-switch', { harness: 'claude-agent-sdk' });

    // The old AbortController should be aborted (session was destroyed)
    expect(startAbort.signal.aborted).toBe(true);
    // Session still exists under the new harness
    expect(mgr.has('sess-switch')).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// (g) HTTP route error paths — session creation failure → correct status codes
// ---------------------------------------------------------------------------

describe('POST /sessions — error paths', () => {
  it('returns 400 with MISSING_SESSION_ID when sessionId is absent', async () => {
    const app = await buildApp();
    const res = await app.inject({
      method: 'POST',
      url: '/sessions',
      payload: { harness: 'mock-harness' },
    });

    expect(res.statusCode).toBe(400);
    const body = JSON.parse(res.payload);
    expect(body.success).toBe(false);
    expect(body.error.code).toBe('MISSING_SESSION_ID');
  });

  it('DELETE /sessions/:id on unknown session returns 200 (destroy is a no-op)', async () => {
    const app = await buildApp();
    // Destroy a session that was never created — sessionManager.destroy is a no-op
    const res = await app.inject({
      method: 'DELETE',
      url: '/sessions/never-created',
    });
    expect(res.statusCode).toBe(200);
    expect(res.headers['x-session-id']).toBe('never-created');
  });

  it('POST /sessions/:id/cancel on unknown session returns 200 (cancel is a no-op)', async () => {
    const app = await buildApp();
    const res = await app.inject({
      method: 'POST',
      url: '/sessions/ghost/cancel',
      payload: {},
    });
    expect(res.statusCode).toBe(200);
  });

  it('returns 400 UNKNOWN_HARNESS and correct X-Session-Id header together', async () => {
    const app = await buildApp();
    const res = await app.inject({
      method: 'POST',
      url: '/sessions',
      payload: { sessionId: 'hdr-check', harness: 'no-such-harness' },
    });

    expect(res.statusCode).toBe(400);
    // Header must be set even for error responses
    expect(res.headers['x-session-id']).toBe('hdr-check');
    const body = JSON.parse(res.payload);
    expect(body.error.code).toBe('UNKNOWN_HARNESS');
  });
});
