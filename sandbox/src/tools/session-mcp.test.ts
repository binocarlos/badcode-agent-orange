// Session MCP server config: validation, ${VAR} resolution, and registry merge.
// Spec: docs/product/01-session-config.md §4 (work-plan item A3).
//
// The validation rules here MIRROR go/agentdb/sessions.go
// (MCPServerConfig.Validate / MCPServers.Validate). If one side changes, change
// both — the Go side is the source of truth.

import { describe, it, expect } from 'vitest';
import {
  validateSessionMCPServers,
  resolveSessionMCPServers,
  mcpServerToolSpec,
  MCPConfigError,
  MCPEnvResolutionError,
} from './registry.js';
import type { SessionMCPServers } from './registry.js';
import { DefaultToolRegistry } from './registry-impl.js';
import { SessionManager, InvalidMCPServersError } from '../services/session-manager.js';

// ---------------------------------------------------------------------------
// Validation — mirrors the Go table tests
// ---------------------------------------------------------------------------

describe('validateSessionMCPServers', () => {
  const valid: Array<{ name: string; servers: SessionMCPServers }> = [
    { name: 'empty set', servers: {} },
    {
      name: 'stdio with command only',
      servers: { gmail: { command: '/usr/local/bin/gmail-mcp' } },
    },
    {
      name: 'stdio with args and env',
      servers: {
        gmail: {
          command: 'gmail-mcp',
          args: ['--stdio'],
          env: { GMAIL_API_KEY: '${GMAIL_API_KEY}', MODE: 'live' },
        },
      },
    },
    {
      name: 'http with url only',
      servers: { notion: { url: 'https://notion.internal/mcp' } },
    },
    {
      name: 'http with header env-ref',
      servers: {
        notion: {
          url: 'https://notion.internal/mcp',
          headers: { Authorization: '${NOTION_AUTH}' },
        },
      },
    },
    {
      name: 'names with digits, dashes and underscores',
      servers: { 'a-b_c9': { command: 'x' }, '0start': { command: 'x' } },
    },
  ];

  for (const tc of valid) {
    it(`accepts ${tc.name}`, () => {
      expect(() => validateSessionMCPServers(tc.servers)).not.toThrow();
    });
  }

  const invalid: Array<{ name: string; servers: SessionMCPServers; match: RegExp }> = [
    {
      name: 'no transport',
      servers: { x: {} },
      match: /exactly one transport required/,
    },
    {
      name: 'both transports',
      servers: { x: { command: 'c', url: 'https://h/mcp' } },
      match: /mutually exclusive/,
    },
    {
      name: 'args without command',
      servers: { x: { url: 'https://h/mcp', args: ['--stdio'] } },
      match: /args require the stdio transport/,
    },
    {
      name: 'env without command',
      servers: { x: { url: 'https://h/mcp', env: { A: 'b' } } },
      match: /env requires the stdio transport/,
    },
    {
      name: 'headers without url',
      servers: { x: { command: 'c', headers: { A: 'b' } } },
      match: /headers require the http transport/,
    },
    {
      name: 'empty env key',
      servers: { x: { command: 'c', env: { '': 'v' } } },
      match: /env: empty key/,
    },
    {
      name: 'partial interpolation in a header (the §4.1 silent-credential trap)',
      servers: {
        x: { url: 'https://h/mcp', headers: { Authorization: 'Bearer ${TOKEN}' } },
      },
      match: /not a whole-value \$\{VAR\} reference/,
    },
    {
      name: 'partial interpolation in env',
      servers: { x: { command: 'c', env: { K: 'prefix-${A}-suffix' } } },
      match: /not a whole-value \$\{VAR\} reference/,
    },
    {
      name: 'nested reference',
      servers: { x: { command: 'c', env: { K: '${${A}}' } } },
      match: /not a whole-value \$\{VAR\} reference/,
    },
    {
      name: 'server name with a dot',
      servers: { 'bad.name': { command: 'c' } },
      match: /name must match/,
    },
    {
      name: 'server name starting with a dash',
      servers: { '-bad': { command: 'c' } },
      match: /name must match/,
    },
    {
      name: 'empty server name',
      servers: { '': { command: 'c' } },
      match: /name must match/,
    },
  ];

  for (const tc of invalid) {
    it(`rejects ${tc.name}`, () => {
      expect(() => validateSessionMCPServers(tc.servers)).toThrow(MCPConfigError);
      expect(() => validateSessionMCPServers(tc.servers)).toThrow(tc.match);
    });
  }

  it('names the offending server in the error', () => {
    expect(() => validateSessionMCPServers({ gmail: {} })).toThrow(/mcp server "gmail"/);
  });
});

// ---------------------------------------------------------------------------
// ${VAR} resolution at spawn time (§4.1, §4.4)
// ---------------------------------------------------------------------------

describe('resolveSessionMCPServers', () => {
  it('resolves whole-value ${VAR} refs in stdio env from the container environment', () => {
    const out = resolveSessionMCPServers(
      {
        gmail: {
          command: 'gmail-mcp',
          args: ['--stdio'],
          env: { GMAIL_API_KEY: '${GMAIL_API_KEY}', MODE: 'live' },
        },
      },
      { GMAIL_API_KEY: 'secret-value' },
    );

    expect(out.gmail).toEqual({
      type: 'stdio',
      command: 'gmail-mcp',
      args: ['--stdio'],
      env: { GMAIL_API_KEY: 'secret-value', MODE: 'live' },
    });
  });

  it('resolves whole-value ${VAR} refs in http headers', () => {
    const out = resolveSessionMCPServers(
      {
        notion: {
          url: 'https://notion.internal/mcp',
          headers: { Authorization: '${NOTION_AUTH}', 'X-Fixed': 'literal' },
        },
      },
      { NOTION_AUTH: 'Bearer abc123' },
    );

    expect(out.notion).toEqual({
      type: 'http',
      url: 'https://notion.internal/mcp',
      headers: { Authorization: 'Bearer abc123', 'X-Fixed': 'literal' },
    });
  });

  it('omits env/headers entirely when not supplied', () => {
    const out = resolveSessionMCPServers(
      { a: { command: 'x' }, b: { url: 'https://h/mcp' } },
      {},
    );
    expect(out.a).toEqual({ type: 'stdio', command: 'x' });
    expect(out.b).toEqual({ type: 'http', url: 'https://h/mcp' });
  });

  it('never emits the literal ${VAR} string — an unset variable throws', () => {
    expect(() =>
      resolveSessionMCPServers(
        { gmail: { command: 'g', env: { GMAIL_API_KEY: '${GMAIL_API_KEY}' } } },
        {}, // GMAIL_API_KEY not present
      ),
    ).toThrow(MCPEnvResolutionError);
  });

  it('fails loudly naming the server, key, and variable', () => {
    let err: unknown;
    try {
      resolveSessionMCPServers(
        { gmail: { command: 'g', env: { GMAIL_API_KEY: '${GMAIL_API_KEY}' } } },
        {},
      );
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(MCPEnvResolutionError);
    const message = (err as Error).message;
    expect(message).toContain('mcp server "gmail"');
    expect(message).toContain('GMAIL_API_KEY');
    expect(message).toMatch(/unset or empty/);
    // Points the operator at the fix (§4.4 propagation path)
    expect(message).toContain('AGENTKIT_MCP_ENV');
  });

  it('treats an EMPTY variable as unset — never spawns with an empty credential', () => {
    expect(() =>
      resolveSessionMCPServers(
        { notion: { url: 'https://h/mcp', headers: { Authorization: '${NOTION_AUTH}' } } },
        { NOTION_AUTH: '' },
      ),
    ).toThrow(MCPEnvResolutionError);
  });

  it('does not consult the environment for literal values', () => {
    const out = resolveSessionMCPServers(
      { a: { command: 'x', env: { MODE: 'live' } } },
      { MODE: 'should-not-be-used' },
    );
    expect((out.a as { env: Record<string, string> }).env.MODE).toBe('live');
  });
});

// ---------------------------------------------------------------------------
// Registry merge + allowedTools extension (§4.3)
// ---------------------------------------------------------------------------

describe('DefaultToolRegistry.resolve with session MCP servers', () => {
  it('is unchanged when no session servers are supplied', () => {
    const reg = new DefaultToolRegistry();
    const resolved = reg.resolve();

    expect(resolved.sessionMCPServers).toEqual({});
    expect(resolved.allowedTools).toContain('mcp__ui__write_file');
    expect(resolved.allowedTools.some(t => t.endsWith('__*'))).toBe(false);
  });

  it('extends allowedTools with mcp__<name>__* for a stdio server', () => {
    const reg = new DefaultToolRegistry();
    const resolved = reg.resolve(undefined, { gmail: { command: 'gmail-mcp' } });

    expect(resolved.allowedTools).toContain('mcp__gmail__*');
    // In-image builtins survive alongside it
    expect(resolved.allowedTools).toContain('mcp__ui__write_file');
  });

  it('extends allowedTools for an http server', () => {
    const reg = new DefaultToolRegistry();
    const resolved = reg.resolve(undefined, { notion: { url: 'https://h/mcp' } });

    expect(resolved.allowedTools).toContain('mcp__notion__*');
  });

  it('extends allowedTools even when the turn supplies an explicit allowlist', () => {
    const reg = new DefaultToolRegistry();
    const resolved = reg.resolve(['Bash'], { gmail: { command: 'g' } });

    expect(resolved.allowedTools).toContain('Bash');
    expect(resolved.allowedTools).toContain('mcp__gmail__*');
  });

  it('carries session servers through UNRESOLVED for spawn-time resolution', () => {
    const reg = new DefaultToolRegistry();
    const servers: SessionMCPServers = {
      gmail: { command: 'g', env: { K: '${GMAIL_API_KEY}' } },
    };
    const resolved = reg.resolve(undefined, servers);

    // ${VAR} refs must still be intact here — resolution happens at spawn.
    expect(resolved.sessionMCPServers.gmail.env).toEqual({ K: '${GMAIL_API_KEY}' });
  });

  it('override precedence: a session server shadowing an in-image name drops its dead tool entries', () => {
    const reg = new DefaultToolRegistry();
    const resolved = reg.resolve(undefined, { ui: { url: 'https://replacement/mcp' } });

    // The builtin `ui` server is replaced at spawn, so its per-tool entries
    // would point at tools that no longer exist.
    expect(resolved.allowedTools).not.toContain('mcp__ui__write_file');
    expect(resolved.allowedTools).not.toContain('mcp__ui__ask_user');
    expect(resolved.allowedTools).toContain('mcp__ui__*');
  });

  it('leaves non-shadowed builtin entries alone when shadowing one name', () => {
    const reg = new DefaultToolRegistry();
    const resolved = reg.resolve(undefined, { gmail: { command: 'g' } });

    expect(resolved.allowedTools).toContain('mcp__ui__write_file');
    expect(resolved.allowedTools).toContain('mcp__ui__ask_user');
  });

  it('supports several servers of mixed transports at once', () => {
    const reg = new DefaultToolRegistry();
    const resolved = reg.resolve(undefined, {
      gmail: { command: 'gmail-mcp' },
      notion: { url: 'https://h/mcp' },
    });

    expect(resolved.allowedTools).toContain('mcp__gmail__*');
    expect(resolved.allowedTools).toContain('mcp__notion__*');
    expect(Object.keys(resolved.sessionMCPServers).sort()).toEqual(['gmail', 'notion']);
  });
});

describe('mcpServerToolSpec', () => {
  it('produces the SDK server-level spec form', () => {
    expect(mcpServerToolSpec('gmail')).toBe('mcp__gmail__*');
  });
});

// ---------------------------------------------------------------------------
// Session create accepts mcp_servers and validates it up front (§4.2, §4.5)
// ---------------------------------------------------------------------------

describe('SessionManager — mcp_servers on session create', () => {
  it('rejects invalid config as a 400 rather than letting it reach a turn', () => {
    const mgr = new SessionManager();
    let err: unknown;
    try {
      mgr.create('sess-bad', {
        mcpServers: { gmail: { command: 'g', url: 'https://h/mcp' } },
      });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(InvalidMCPServersError);
    expect((err as InvalidMCPServersError).http.status).toBe(400);
    expect((err as InvalidMCPServersError).http.body.code).toBe('INVALID_MCP_SERVERS');
    // Nothing was created
    expect(mgr.has('sess-bad')).toBe(false);
  });

  it('stores valid config on the session, unresolved', () => {
    const prev = process.env.ANTHROPIC_API_KEY;
    process.env.ANTHROPIC_API_KEY = 'test-key';
    try {
      const mgr = new SessionManager();
      mgr.create('sess-ok', {
        mcpServers: { gmail: { command: 'g', env: { K: '${GMAIL_API_KEY}' } } },
      });
      expect(mgr.get('sess-ok')!.mcpServers.gmail.env).toEqual({ K: '${GMAIL_API_KEY}' });
    } finally {
      if (prev === undefined) delete process.env.ANTHROPIC_API_KEY;
      else process.env.ANTHROPIC_API_KEY = prev;
    }
  });

  it('re-supplies config on re-provision of an existing session (§4.5 resume path)', () => {
    const prev = process.env.ANTHROPIC_API_KEY;
    process.env.ANTHROPIC_API_KEY = 'test-key';
    try {
      const mgr = new SessionManager();
      mgr.create('sess-resume', { mcpServers: { gmail: { command: 'g' } } });
      // Same harness → idempotent create, but MCP config is refreshed.
      mgr.create('sess-resume', { mcpServers: { notion: { url: 'https://h/mcp' } } });

      expect(Object.keys(mgr.get('sess-resume')!.mcpServers)).toEqual(['notion']);
    } finally {
      if (prev === undefined) delete process.env.ANTHROPIC_API_KEY;
      else process.env.ANTHROPIC_API_KEY = prev;
    }
  });
});
