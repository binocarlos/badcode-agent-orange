// Spawn-time behaviour of session MCP config in ClaudeAgentSdkHarness.
// Spec: docs/product/01-session-config.md §4 (work-plan item A3).
//
// These tests capture the options actually handed to the SDK's query() — the
// moment MCP processes are spawned — so they prove the two properties §4
// cares about: session config wins on name collision, and an unresolved
// credential prevents the spawn entirely.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { Harness, TurnContext, HarnessEmitter } from './harness.js';
import type { QueryRequest } from '../types/index.js';
import type { ResolvedTools, SessionMCPServers } from '../tools/registry.js';
import type { Config } from '../config.js';

// Capture every query() invocation's options instead of running the model.
const queryCalls: Array<Record<string, unknown>> = [];

vi.mock('@anthropic-ai/claude-agent-sdk', () => ({
  query: (params: { options: Record<string, unknown> }) => {
    queryCalls.push(params.options);
    return (async function* () {
      /* no messages — the turn ends immediately */
    })();
  },
}));

const { ClaudeAgentSdkHarness } = await import('./claude-agent-sdk.js');

// ---------------------------------------------------------------------------
// Harness fixtures
// ---------------------------------------------------------------------------

interface EmitCall {
  method: string;
  args: unknown[];
}

function makeEmitter(calls: EmitCall[]): HarnessEmitter {
  const rec =
    (method: string) =>
    (...args: unknown[]) =>
      calls.push({ method, args });
  return {
    messageStart: rec('messageStart'),
    contentDelta: rec('contentDelta'),
    thinkingDelta: rec('thinkingDelta'),
    messageEnd: rec('messageEnd'),
    toolUseStart: rec('toolUseStart'),
    toolUseEnd: rec('toolUseEnd'),
    toolProgress: rec('toolProgress'),
    toolInputDelta: rec('toolInputDelta'),
    hookEvent: rec('hookEvent'),
    subagentEvent: rec('subagentEvent'),
    activityUpdate: rec('activityUpdate'),
    systemStatus: rec('systemStatus'),
    sessionInfo: rec('sessionInfo'),
    event: rec('event'),
    endQuery: rec('endQuery'),
    error: rec('error'),
  } as HarnessEmitter;
}

/** A stand-in for the in-image registry's SDK-instance server. */
const inImageUiServer = { type: 'sdk', name: 'ui', instance: {} } as never;

function makeCtx(
  sessionMCPServers: SessionMCPServers,
  calls: EmitCall[],
  allowedTools: string[] = ['mcp__ui__write_file'],
): TurnContext {
  const resolved: ResolvedTools = {
    allowedTools,
    disallowedTools: [],
    mcpServers: { ui: inImageUiServer },
    sessionMCPServers,
    markers: [],
  };
  return {
    sessionId: 'sess-mcp',
    queryId: 'q-mcp',
    signal: new AbortController().signal,
    emit: makeEmitter(calls),
    resolved,
    config: {
      DEFAULT_MODEL: 'claude-test',
      DEFAULT_MAX_TURNS: 10,
      DEFAULT_THINKING_BUDGET_TOKENS: 1000,
    } as Config,
  };
}

const req: QueryRequest = { prompt: 'Say hello' };

// ---------------------------------------------------------------------------

describe('ClaudeAgentSdkHarness — session MCP servers at spawn', () => {
  let harness: Harness;
  let calls: EmitCall[];

  beforeEach(() => {
    queryCalls.length = 0;
    calls = [];
    harness = new ClaudeAgentSdkHarness();
    delete process.env.GMAIL_API_KEY;
    delete process.env.NOTION_AUTH;
  });

  it('merges a stdio server over the in-image registry, resolving ${VAR} from the container env', async () => {
    process.env.GMAIL_API_KEY = 'secret-value';

    await harness.runTurn(
      req,
      makeCtx(
        {
          gmail: {
            command: 'gmail-mcp',
            args: ['--stdio'],
            env: { GMAIL_API_KEY: '${GMAIL_API_KEY}' },
          },
        },
        calls,
      ),
    );

    expect(queryCalls).toHaveLength(1);
    const mcpServers = queryCalls[0].mcpServers as Record<string, unknown>;

    // In-image server survives; session server is added alongside.
    expect(mcpServers.ui).toBe(inImageUiServer);
    expect(mcpServers.gmail).toEqual({
      type: 'stdio',
      command: 'gmail-mcp',
      args: ['--stdio'],
      env: { GMAIL_API_KEY: 'secret-value' },
    });
  });

  it('merges an http server, resolving ${VAR} in headers', async () => {
    process.env.NOTION_AUTH = 'Bearer abc123';

    await harness.runTurn(
      req,
      makeCtx(
        {
          notion: {
            url: 'https://notion.internal/mcp',
            headers: { Authorization: '${NOTION_AUTH}' },
          },
        },
        calls,
      ),
    );

    expect((queryCalls[0].mcpServers as Record<string, unknown>).notion).toEqual({
      type: 'http',
      url: 'https://notion.internal/mcp',
      headers: { Authorization: 'Bearer abc123' },
    });
  });

  it('session config WINS on name collision with the in-image registry', async () => {
    await harness.runTurn(
      req,
      makeCtx({ ui: { url: 'https://replacement/mcp' } }, calls),
    );

    const mcpServers = queryCalls[0].mcpServers as Record<string, unknown>;
    expect(mcpServers.ui).toEqual({ type: 'http', url: 'https://replacement/mcp' });
    expect(mcpServers.ui).not.toBe(inImageUiServer);
  });

  it('passes allowedTools through with the session server entries intact', async () => {
    await harness.runTurn(
      req,
      makeCtx({ gmail: { command: 'g' } }, calls, [
        'mcp__ui__write_file',
        'mcp__gmail__*',
      ]),
    );

    expect(queryCalls[0].allowedTools).toEqual(['mcp__ui__write_file', 'mcp__gmail__*']);
  });

  it('leaves query() untouched when no session servers are configured', async () => {
    await harness.runTurn(req, makeCtx({}, calls));

    expect(queryCalls[0].mcpServers).toEqual({ ui: inImageUiServer });
  });

  // -- the load-bearing case -------------------------------------------------

  it('NEVER spawns when a ${VAR} reference is unset — query() is not called at all', async () => {
    // GMAIL_API_KEY deliberately absent
    await harness.runTurn(
      req,
      makeCtx(
        { gmail: { command: 'g', env: { GMAIL_API_KEY: '${GMAIL_API_KEY}' } } },
        calls,
      ),
    );

    expect(queryCalls).toHaveLength(0);
  });

  it('fails loudly on an unset ${VAR}: emits AGENT_ERROR and ends the query in error', async () => {
    await harness.runTurn(
      req,
      makeCtx(
        { gmail: { command: 'g', env: { GMAIL_API_KEY: '${GMAIL_API_KEY}' } } },
        calls,
      ),
    );

    const errorCall = calls.find(c => c.method === 'error');
    expect(errorCall).toBeDefined();
    expect(errorCall!.args[0]).toBe('AGENT_ERROR');
    expect(String(errorCall!.args[1])).toContain('GMAIL_API_KEY');
    expect(String(errorCall!.args[1])).toMatch(/unset or empty/);

    const endCall = calls.find(c => c.method === 'endQuery');
    expect(endCall).toBeDefined();
    expect(endCall!.args[0]).toBe('error');
  });

  it('does not spawn when a ${VAR} resolves to an empty string', async () => {
    process.env.NOTION_AUTH = '';

    await harness.runTurn(
      req,
      makeCtx(
        {
          notion: {
            url: 'https://h/mcp',
            headers: { Authorization: '${NOTION_AUTH}' },
          },
        },
        calls,
      ),
    );

    expect(queryCalls).toHaveLength(0);
    expect(calls.some(c => c.method === 'error')).toBe(true);
  });
});
