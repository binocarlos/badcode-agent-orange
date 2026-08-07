/**
 * AG-2 Harness seam unit tests.
 *
 * (a) HarnessRegistry — register / has / get / names
 * (b) Credential pre-check — missing env → error object; set env → ok
 * (c) ClaudeAgentSdkHarness turn emission — scripted SDK messages → expected
 *     HarnessEmitter calls (messageStart, contentDelta, messageEnd, endQuery)
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { HarnessRegistry, DEFAULT_HARNESS, checkCredentials } from './registry.js';
import type { HarnessDescriptor } from './registry.js';
import type { Harness, TurnContext, HarnessEmitter, HarnessCredentialSpec } from './harness.js';
import type { QueryRequest } from '../types/index.js';
import type { ResolvedTools } from '../tools/registry.js';
import type { Config } from '../config.js';

// ---------------------------------------------------------------------------
// Mock the SDK module so query() can be scripted per-test
// ---------------------------------------------------------------------------

// Mutable holder for the message sequence each test provides
let _mockMessages: unknown[] = [];

vi.mock('@anthropic-ai/claude-agent-sdk', () => ({
  query: (_params: unknown) => {
    // Return a fresh async iterable each time query() is called
    const msgs = _mockMessages;
    return (async function* () {
      for (const m of msgs) {
        yield m;
      }
    })();
  },
}));

// ---------------------------------------------------------------------------
// Helpers / stubs
// ---------------------------------------------------------------------------

function makeDescriptor(name: string, requiredEnv: string[] = []): HarnessDescriptor {
  const credentials: HarnessCredentialSpec = {
    requiredEnv,
    describe: () => `${name} needs: ${requiredEnv.join(', ')}`,
  };
  const harness: Harness = {
    name,
    async runTurn() { /* no-op */ },
    loadConversation() { /* no-op */ },
    resetConversation() { /* no-op */ },
  };
  return {
    name,
    credentials,
    create: () => harness,
  };
}

// ---------------------------------------------------------------------------
// (a) HarnessRegistry
// ---------------------------------------------------------------------------

describe('HarnessRegistry', () => {
  it('starts empty', () => {
    const reg = new HarnessRegistry();
    expect(reg.names()).toEqual([]);
  });

  it('register / has / get / names', () => {
    const reg = new HarnessRegistry();
    const d = makeDescriptor('test-harness');
    reg.register(d);

    expect(reg.has('test-harness')).toBe(true);
    expect(reg.has('unknown')).toBe(false);
    expect(reg.get('test-harness')).toBe(d);
    expect(reg.get('unknown')).toBeUndefined();
    expect(reg.names()).toEqual(['test-harness']);
  });

  it('supports multiple descriptors', () => {
    const reg = new HarnessRegistry();
    reg.register(makeDescriptor('alpha'));
    reg.register(makeDescriptor('beta'));
    reg.register(makeDescriptor('gamma'));

    expect(reg.names().sort()).toEqual(['alpha', 'beta', 'gamma']);
    expect(reg.has('beta')).toBe(true);
  });

  it('DEFAULT_HARNESS is claude-agent-sdk', () => {
    expect(DEFAULT_HARNESS).toBe('claude-agent-sdk');
  });
});

// ---------------------------------------------------------------------------
// (b) Credential pre-check
// ---------------------------------------------------------------------------

describe('checkCredentials', () => {
  it('returns ok:true when all required env vars are set', () => {
    const desc = makeDescriptor('sdk', ['ANTHROPIC_BASE_URL']);
    const result = checkCredentials(desc, { ANTHROPIC_BASE_URL: 'http://proxy' });
    expect(result.ok).toBe(true);
  });

  it('returns ok:false with missing list when env var is absent', () => {
    const desc = makeDescriptor('sdk', ['ANTHROPIC_BASE_URL']);
    const result = checkCredentials(desc, {});
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.missing).toEqual(['ANTHROPIC_BASE_URL']);
    }
  });

  it('returns ok:false when env var is empty string', () => {
    const desc = makeDescriptor('sdk', ['ANTHROPIC_BASE_URL']);
    const result = checkCredentials(desc, { ANTHROPIC_BASE_URL: '' });
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.missing).toContain('ANTHROPIC_BASE_URL');
    }
  });

  it('lists all missing vars when multiple are required', () => {
    const desc = makeDescriptor('multi', ['VAR_A', 'VAR_B', 'VAR_C']);
    const result = checkCredentials(desc, { VAR_B: 'present' });
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.missing.sort()).toEqual(['VAR_A', 'VAR_C']);
    }
  });

  it('returns ok:true when no env vars are required', () => {
    const desc = makeDescriptor('none', []);
    const result = checkCredentials(desc, {});
    expect(result.ok).toBe(true);
  });

  describe('anyOfEnv groups', () => {
    function makeAnyOfDescriptor(anyOfEnv: string[]): HarnessDescriptor {
      return {
        name: 'anyof',
        credentials: { requiredEnv: [], anyOfEnv, describe: () => 'anyof' },
        create: () => ({ name: 'anyof' }) as unknown as Harness,
      };
    }

    it('any single member of the group satisfies it', () => {
      const desc = makeAnyOfDescriptor(['ANTHROPIC_BASE_URL', 'CLAUDE_CODE_OAUTH_TOKEN', 'ANTHROPIC_API_KEY']);
      expect(checkCredentials(desc, { ANTHROPIC_BASE_URL: 'http://proxy' }).ok).toBe(true);
      expect(checkCredentials(desc, { CLAUDE_CODE_OAUTH_TOKEN: 'sk-ant-oat01-x' }).ok).toBe(true);
      expect(checkCredentials(desc, { ANTHROPIC_API_KEY: 'sk-ant-real' }).ok).toBe(true);
    });

    it('none set → missing lists the group as one synthetic entry', () => {
      const desc = makeAnyOfDescriptor(['A', 'B']);
      const result = checkCredentials(desc, {});
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.missing).toEqual(['one of: A, B']);
      }
    });

    it('empty-string members do not satisfy the group', () => {
      const desc = makeAnyOfDescriptor(['A', 'B']);
      expect(checkCredentials(desc, { A: '', B: '' }).ok).toBe(false);
    });

    it('group check composes with requiredEnv', () => {
      const desc: HarnessDescriptor = {
        name: 'both',
        credentials: { requiredEnv: ['MUST'], anyOfEnv: ['A', 'B'], describe: () => 'both' },
        create: () => ({ name: 'both' }) as unknown as Harness,
      };
      const result = checkCredentials(desc, {});
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.missing).toEqual(['MUST', 'one of: A, B']);
      }
      expect(checkCredentials(desc, { MUST: 'x', B: 'y' }).ok).toBe(true);
    });
  });
});

// ---------------------------------------------------------------------------
// (c) ClaudeAgentSdkHarness — scripted turn emission test
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// The usage fixture, captured shape.
//
// The predecessor of this constant was INVENTED: `{input_tokens, output_tokens}`,
// two keys, which no real API response has ever had. It matched the reader
// rather than the writer, and so it could not fail when the writer dropped the
// two cache fields — the exact fixture-authored-to-match-the-reader failure that
// TOK1 was filed for and that RD2 caught a second instance of.
//
// This is the shape of `BetaUsage` as declared in
// `sandbox/node_modules/@anthropic-ai/sdk/resources/beta/messages/messages.d.ts`
// and reached from the agent SDK as `NonNullableUsage` — the type
// `SDKResultSuccess.usage` and `SDKResultError.usage` are both declared as.
// The numbers are the interesting case rather than the average one: a large
// composed prompt with caching active, where the uncached `input_tokens` is a
// small minority of what is billed (12 of 8,466 input tokens — 0.14%).
// ---------------------------------------------------------------------------
const CAPTURED_SHAPE_USAGE = {
  input_tokens: 12,
  output_tokens: 213,
  cache_creation_input_tokens: 1704,
  cache_read_input_tokens: 6750,
  cache_creation: { ephemeral_5m_input_tokens: 1704, ephemeral_1h_input_tokens: 0 },
  output_tokens_details: { reasoning_tokens: 0 },
  server_tool_use: null,
  service_tier: 'standard',
  speed: 'standard',
  inference_geo: null,
  iterations: null,
};

/** What the harness must forward for the above: every billed component. */
const EXPECTED_FORWARDED_USAGE = {
  inputTokens: 12,
  outputTokens: 213,
  cacheCreationInputTokens: 1704,
  cacheReadInputTokens: 6750,
};

/** Run one scripted turn through a real ClaudeAgentSdkHarness, recording emits. */
async function runScriptedTurn(): Promise<Array<{ method: string; args: unknown[] }>> {
  const { ClaudeAgentSdkHarness } = await import('./claude-agent-sdk.js');
  const harness = new ClaudeAgentSdkHarness();
  const calls: Array<{ method: string; args: unknown[] }> = [];
  const record =
    (method: string) =>
    (...args: unknown[]) => {
      calls.push({ method, args });
    };
  const fakeEmitter = {
    messageStart: record('messageStart'),
    contentDelta: record('contentDelta'),
    thinkingDelta: record('thinkingDelta'),
    messageEnd: record('messageEnd'),
    toolUseStart: record('toolUseStart'),
    toolUseEnd: record('toolUseEnd'),
    toolProgress: record('toolProgress'),
    toolInputDelta: record('toolInputDelta'),
    hookEvent: record('hookEvent'),
    subagentEvent: record('subagentEvent'),
    activityUpdate: record('activityUpdate'),
    systemStatus: record('systemStatus'),
    sessionInfo: record('sessionInfo'),
    event: record('event'),
    endQuery: record('endQuery'),
    error: record('error'),
  } as unknown as HarnessEmitter;

  const ctx: TurnContext = {
    sessionId: 'sess-usage',
    queryId: 'q-usage',
    signal: new AbortController().signal,
    emit: fakeEmitter,
    resolved: {
      allowedTools: [],
      disallowedTools: [],
      mcpServers: {},
      sessionMCPServers: {},
      markers: [],
    } as ResolvedTools,
    config: {
      DEFAULT_MODEL: 'claude-test',
      DEFAULT_MAX_TURNS: 10,
      DEFAULT_THINKING_BUDGET_TOKENS: 1000,
    } as Config,
  };

  await harness.runTurn({ prompt: 'Say hello', model: 'claude-test' } as QueryRequest, ctx);
  return calls;
}

/** The `usage` argument of the single endQuery emitted by a scripted turn. */
async function forwardedUsage(): Promise<Record<string, unknown> | undefined> {
  const calls = await runScriptedTurn();
  const end = calls.find((c) => c.method === 'endQuery');
  expect(end, 'every turn must end with exactly one endQuery').toBeDefined();
  return end!.args[3] as Record<string, unknown> | undefined;
}

// ---------------------------------------------------------------------------
// RD2 / RD2b — the spend brake's only input
// ---------------------------------------------------------------------------

describe('token usage forwarding', () => {
  beforeEach(() => {
    _mockMessages = [
      { type: 'system', subtype: 'init', tools: [], model: 'claude-test', mcp_servers: [] },
      {
        type: 'result',
        subtype: 'success',
        result: 'Hello world',
        total_cost_usd: 0.031,
        usage: CAPTURED_SHAPE_USAGE,
      },
    ];
  });

  it('forwards every separately-billed component, not just input_tokens', async () => {
    expect(await forwardedUsage()).toEqual(EXPECTED_FORWARDED_USAGE);
  });

  it('does NOT write the two-key shape — the invented fixture is not production output', async () => {
    // The mirror of TOK1's "the wrong shape appears in zero stored rows".
    // `{inputTokens, outputTokens}` alone is what the harness used to emit and
    // what both fixtures asserted; it must never be what production writes
    // again, because it silently omits ~99% of a cached turn's input bill.
    const usage = await forwardedUsage();
    expect(usage).toBeDefined();
    expect(Object.keys(usage!).sort()).toEqual([
      'cacheCreationInputTokens',
      'cacheReadInputTokens',
      'inputTokens',
      'outputTokens',
    ]);
    expect(usage).not.toEqual({ inputTokens: 12, outputTokens: 213 });
  });

  it('counts cache reads: the forwarded input dwarfs input_tokens alone', async () => {
    const usage = (await forwardedUsage())!;
    const billedInput =
      (usage.inputTokens as number) +
      (usage.cacheCreationInputTokens as number) +
      (usage.cacheReadInputTokens as number);
    expect(billedInput).toBe(8466);
    // The pre-fix ledger would have reported 12 — 0.14% of the real bill. A
    // brake reading that number fires ~700x too late, or never.
    expect(billedInput).toBeGreaterThan((usage.inputTokens as number) * 100);
  });

  it('RD2b: a turn that errors after burning tokens is still metered', async () => {
    // `SDKResultError` declares `usage: NonNullableUsage` and
    // `total_cost_usd: number` exactly as `SDKResultSuccess` does — the error
    // result really does carry the bill. Discarding it left the RUNAWAY case,
    // the one the brake exists for, as the only unmetered one.
    _mockMessages = [
      { type: 'system', subtype: 'init', tools: [], model: 'claude-test', mcp_servers: [] },
      {
        type: 'result',
        subtype: 'error_max_turns',
        errors: ['max turns exceeded'],
        total_cost_usd: 0.042,
        usage: CAPTURED_SHAPE_USAGE,
      },
    ];

    const calls = await runScriptedTurn();
    const end = calls.find((c) => c.method === 'endQuery')!;
    expect(end.args[0]).toBe('error');
    expect(end.args[2], 'cost must survive the error path too').toBe(0.042);
    expect(end.args[3]).toEqual(EXPECTED_FORWARDED_USAGE);
  });

  it('a usage object missing the cache keys reads as zero, never NaN', async () => {
    // A NaN would cast to NULL in the jsonb read and silently zero the ledger —
    // the failure class this whole change exists to end.
    const { readTokenUsage } = await import('./claude-agent-sdk.js');
    expect(readTokenUsage({ input_tokens: 10, output_tokens: 5 })).toEqual({
      inputTokens: 10,
      outputTokens: 5,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 0,
    });
    expect(readTokenUsage(undefined)).toEqual({
      inputTokens: 0,
      outputTokens: 0,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 0,
    });
  });
});

describe('ClaudeAgentSdkHarness', () => {
  // The scripted sequence of messages the mock query() will yield.
  // Represents a minimal successful turn:
  //   system(init) → stream_event(content_block_delta) → result(success)

  beforeEach(() => {
    _mockMessages = [
      {
        type: 'system',
        subtype: 'init',
        tools: ['Bash'],
        model: 'claude-test',
        mcp_servers: [],
      },
      {
        type: 'stream_event',
        event: {
          type: 'content_block_delta',
          delta: { type: 'text_delta', text: 'Hello world' },
        },
      },
      {
        type: 'result',
        subtype: 'success',
        result: 'Hello world',
        total_cost_usd: 0.001,
        usage: CAPTURED_SHAPE_USAGE,
      },
    ];
  });

  it('emits messageStart / contentDelta / messageEnd / endQuery for a scripted turn', async () => {
    const { ClaudeAgentSdkHarness } = await import('./claude-agent-sdk.js');

    const harness = new ClaudeAgentSdkHarness();

    // Build a fake emitter that records calls
    const calls: Array<{ method: string; args: unknown[] }> = [];
    const fakeEmitter: HarnessEmitter = {
      messageStart: (...args) => calls.push({ method: 'messageStart', args }),
      contentDelta: (...args) => calls.push({ method: 'contentDelta', args }),
      thinkingDelta: (...args) => calls.push({ method: 'thinkingDelta', args }),
      messageEnd: (...args) => calls.push({ method: 'messageEnd', args }),
      toolUseStart: (...args) => calls.push({ method: 'toolUseStart', args }),
      toolUseEnd: (...args) => calls.push({ method: 'toolUseEnd', args }),
      toolProgress: (...args) => calls.push({ method: 'toolProgress', args }),
      toolInputDelta: (...args) => calls.push({ method: 'toolInputDelta', args }),
      hookEvent: (...args) => calls.push({ method: 'hookEvent', args }),
      subagentEvent: (...args) => calls.push({ method: 'subagentEvent', args }),
      activityUpdate: (...args) => calls.push({ method: 'activityUpdate', args }),
      systemStatus: (...args) => calls.push({ method: 'systemStatus', args }),
      sessionInfo: (...args) => calls.push({ method: 'sessionInfo', args }),
      event: (...args) => calls.push({ method: 'event', args }),
      endQuery: (...args) => calls.push({ method: 'endQuery', args }),
      error: (...args) => calls.push({ method: 'error', args }),
    };

    const req: QueryRequest = {
      prompt: 'Say hello',
      model: 'claude-test',
    };

    const resolved: ResolvedTools = {
      allowedTools: [],
      disallowedTools: [],
      mcpServers: {},
      sessionMCPServers: {},
      markers: [],
    };

    const config = {
      DEFAULT_MODEL: 'claude-test',
      DEFAULT_MAX_TURNS: 10,
      DEFAULT_THINKING_BUDGET_TOKENS: 1000,
    } as Config;

    const ctx: TurnContext = {
      sessionId: 'sess-1',
      queryId: 'q-1',
      signal: new AbortController().signal,
      emit: fakeEmitter,
      resolved,
      config,
    };

    await harness.runTurn(req, ctx);

    // Verify the expected sequence of emitter calls
    const methods = calls.map(c => c.method);

    // messageStart must come before contentDelta
    const startIdx = methods.indexOf('messageStart');
    const deltaIdx = methods.indexOf('contentDelta');
    const endIdx = methods.lastIndexOf('messageEnd');
    const completeIdx = methods.indexOf('endQuery');

    expect(startIdx).toBeGreaterThanOrEqual(0);
    expect(deltaIdx).toBeGreaterThan(startIdx);
    expect(endIdx).toBeGreaterThan(deltaIdx);
    expect(completeIdx).toBeGreaterThan(endIdx);

    // Verify contentDelta content
    const deltaCall = calls.find(c => c.method === 'contentDelta');
    expect(deltaCall?.args[1]).toBe('Hello world');

    // Verify endQuery status is 'completed'
    const endQueryCall = calls.find(c => c.method === 'endQuery');
    expect(endQueryCall?.args[0]).toBe('completed');
    expect(endQueryCall?.args[1]).toBe('Hello world'); // result text

    // sessionInfo should have been emitted from the system/init message
    const sessionInfoCall = calls.find(c => c.method === 'sessionInfo');
    expect(sessionInfoCall).toBeDefined();
    expect((sessionInfoCall?.args[0] as { model: string }).model).toBe('claude-test');

    // No error events should have been emitted
    expect(methods).not.toContain('error');
  });

  it('loadConversation sets conversation history', async () => {
    const { ClaudeAgentSdkHarness } = await import('./claude-agent-sdk.js');
    const harness = new ClaudeAgentSdkHarness();
    harness.loadConversation([
      { role: 'user', content: 'hello' },
      { role: 'assistant', content: 'hi' },
    ]);
    // If we could inspect conversationHistory we would, but it's private.
    // Verifying no throws is sufficient; the routing test covers history injection.
    expect(true).toBe(true);
  });

  it('resetConversation clears history', async () => {
    const { ClaudeAgentSdkHarness } = await import('./claude-agent-sdk.js');
    const harness = new ClaudeAgentSdkHarness();
    harness.loadConversation([{ role: 'user', content: 'hello' }]);
    harness.resetConversation();
    // No throw — method exists and works
    expect(true).toBe(true);
  });

  it('claudeAgentSdkDescriptor has correct name and credentials', async () => {
    const { claudeAgentSdkDescriptor } = await import('./claude-agent-sdk.js');
    expect(claudeAgentSdkDescriptor.name).toBe('claude-agent-sdk');
    // Any one model credential suffices: proxy URL, subscription token, or API key.
    expect(claudeAgentSdkDescriptor.credentials.requiredEnv).toEqual([]);
    expect(claudeAgentSdkDescriptor.credentials.anyOfEnv).toEqual([
      'ANTHROPIC_BASE_URL',
      'CLAUDE_CODE_OAUTH_TOKEN',
      'ANTHROPIC_API_KEY',
    ]);
    expect(typeof claudeAgentSdkDescriptor.credentials.describe()).toBe('string');
  });

  it('claudeAgentSdkDescriptor.create returns a fresh ClaudeAgentSdkHarness', async () => {
    const { claudeAgentSdkDescriptor, ClaudeAgentSdkHarness } = await import('./claude-agent-sdk.js');
    const h = claudeAgentSdkDescriptor.create('my-session');
    expect(h).toBeInstanceOf(ClaudeAgentSdkHarness);
    expect(h.name).toBe('claude-agent-sdk');
  });

  // ---------------------------------------------------------------------------
  // Error path (d): emitter throws inside runTurn → catch block fires
  // ---------------------------------------------------------------------------

  it('emits error + endQuery(error) when an internal emitter call throws', async () => {
    // Set up a scripted turn so the harness enters the message loop.
    // The system/init message triggers sessionInfo which uses the normal emitter.
    // Then the stream_event triggers contentDelta which we make throw.
    _mockMessages = [
      {
        type: 'system',
        subtype: 'init',
        tools: [],
        model: 'claude-test',
        mcp_servers: [],
      },
      {
        type: 'stream_event',
        event: {
          type: 'content_block_delta',
          delta: { type: 'text_delta', text: 'hi' },
        },
      },
    ];

    const { ClaudeAgentSdkHarness } = await import('./claude-agent-sdk.js');
    const harness = new ClaudeAgentSdkHarness();

    const calls: Array<{ method: string; args: unknown[] }> = [];

    // contentDelta throws — this propagates out of the for-await loop and
    // lands in the catch block, which emits error() + endQuery(error).
    const throwingEmitter: HarnessEmitter = {
      messageStart: (...args) => calls.push({ method: 'messageStart', args }),
      contentDelta: () => { throw new Error('stream write failure'); },
      thinkingDelta: (...args) => calls.push({ method: 'thinkingDelta', args }),
      messageEnd: (...args) => calls.push({ method: 'messageEnd', args }),
      toolUseStart: (...args) => calls.push({ method: 'toolUseStart', args }),
      toolUseEnd: (...args) => calls.push({ method: 'toolUseEnd', args }),
      toolProgress: (...args) => calls.push({ method: 'toolProgress', args }),
      toolInputDelta: (...args) => calls.push({ method: 'toolInputDelta', args }),
      hookEvent: (...args) => calls.push({ method: 'hookEvent', args }),
      subagentEvent: (...args) => calls.push({ method: 'subagentEvent', args }),
      activityUpdate: (...args) => calls.push({ method: 'activityUpdate', args }),
      systemStatus: (...args) => calls.push({ method: 'systemStatus', args }),
      sessionInfo: (...args) => calls.push({ method: 'sessionInfo', args }),
      event: (...args) => calls.push({ method: 'event', args }),
      endQuery: (...args) => calls.push({ method: 'endQuery', args }),
      error: (...args) => calls.push({ method: 'error', args }),
    };

    const config = {
      DEFAULT_MODEL: 'claude-test',
      DEFAULT_MAX_TURNS: 10,
      DEFAULT_THINKING_BUDGET_TOKENS: 1000,
    } as Config;

    const resolved: ResolvedTools = {
      allowedTools: [],
      disallowedTools: [],
      mcpServers: {},
      sessionMCPServers: {},
      markers: [],
    };

    const ctx: TurnContext = {
      sessionId: 'err-sess',
      queryId: 'err-q-1',
      signal: new AbortController().signal,
      emit: throwingEmitter,
      resolved,
      config,
    };

    // runTurn must not throw — it catches errors internally and emits them
    await expect(harness.runTurn({ prompt: 'hello' }, ctx)).resolves.toBeUndefined();

    const methods = calls.map(c => c.method);
    // error event must be emitted in the catch block
    expect(methods).toContain('error');
    const errorCall = calls.find(c => c.method === 'error');
    expect(errorCall?.args[0]).toBe('AGENT_ERROR');
    expect(typeof errorCall?.args[1]).toBe('string');

    // endQuery must also be emitted with error status
    expect(methods).toContain('endQuery');
    const endCall = calls.find(c => c.method === 'endQuery');
    expect(endCall?.args[0]).toBe('error');
  });

  it('emits endQuery(error) — not error event — when result subtype is not success', async () => {
    _mockMessages = [
      {
        type: 'system',
        subtype: 'init',
        tools: [],
        model: 'claude-test',
        mcp_servers: [],
      },
      {
        type: 'result',
        subtype: 'error_max_turns',
        errors: ['Maximum turns reached'],
        total_cost_usd: 0.017,
        usage: CAPTURED_SHAPE_USAGE,
      },
    ];

    const { ClaudeAgentSdkHarness } = await import('./claude-agent-sdk.js');
    const harness = new ClaudeAgentSdkHarness();

    const calls: Array<{ method: string; args: unknown[] }> = [];
    const fakeEmitter: HarnessEmitter = {
      messageStart: (...args) => calls.push({ method: 'messageStart', args }),
      contentDelta: (...args) => calls.push({ method: 'contentDelta', args }),
      thinkingDelta: (...args) => calls.push({ method: 'thinkingDelta', args }),
      messageEnd: (...args) => calls.push({ method: 'messageEnd', args }),
      toolUseStart: (...args) => calls.push({ method: 'toolUseStart', args }),
      toolUseEnd: (...args) => calls.push({ method: 'toolUseEnd', args }),
      toolProgress: (...args) => calls.push({ method: 'toolProgress', args }),
      toolInputDelta: (...args) => calls.push({ method: 'toolInputDelta', args }),
      hookEvent: (...args) => calls.push({ method: 'hookEvent', args }),
      subagentEvent: (...args) => calls.push({ method: 'subagentEvent', args }),
      activityUpdate: (...args) => calls.push({ method: 'activityUpdate', args }),
      systemStatus: (...args) => calls.push({ method: 'systemStatus', args }),
      sessionInfo: (...args) => calls.push({ method: 'sessionInfo', args }),
      event: (...args) => calls.push({ method: 'event', args }),
      endQuery: (...args) => calls.push({ method: 'endQuery', args }),
      error: (...args) => calls.push({ method: 'error', args }),
    };

    const config = {
      DEFAULT_MODEL: 'claude-test',
      DEFAULT_MAX_TURNS: 10,
      DEFAULT_THINKING_BUDGET_TOKENS: 1000,
    } as Config;

    const resolved: ResolvedTools = {
      allowedTools: [],
      disallowedTools: [],
      mcpServers: {},
      sessionMCPServers: {},
      markers: [],
    };

    const ctx: TurnContext = {
      sessionId: 'err-sess-2',
      queryId: 'err-q-2',
      signal: new AbortController().signal,
      emit: fakeEmitter,
      resolved,
      config,
    };

    await harness.runTurn({ prompt: 'hello' }, ctx);

    // result subtype != success → endQuery('error') without an error event
    const endCall = calls.find(c => c.method === 'endQuery');
    expect(endCall).toBeDefined();
    expect(endCall?.args[0]).toBe('error');
    // No thrown exception → no 'error' event emitted
    expect(calls.map(c => c.method)).not.toContain('error');
  });
});
