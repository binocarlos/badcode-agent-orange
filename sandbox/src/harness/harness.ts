// The harness seam: a pluggable interface for agentic frameworks.
// See agent-library/docs/12-harness.md for the design rationale.

import type { QueryRequest } from '../types/index.js';
import type { ResolvedTools } from '../tools/registry.js';
import type { Config } from '../config.js';
import type { StreamService } from '../services/stream-service.js';

// Re-export SandboxConfig alias so the rest of the harness layer can use it
// without reaching into config.ts directly.
export type SandboxConfig = Config;

/**
 * What credentials/config a harness needs — checked at session-start, before booting.
 * If any requiredEnv var is missing, the control server returns 424 before the harness runs.
 */
export interface HarnessCredentialSpec {
  /** Process env vars that must be non-empty for this harness to run. */
  requiredEnv: string[];
  /**
   * Alternative credential group: at least ONE of these env vars must be
   * non-empty (e.g. a model proxy URL, an OAuth token, or a real API key).
   * Empty/omitted = no group check.
   */
  anyOfEnv?: string[];
  /** Human-readable note surfaced in the start-session error if any var is missing. */
  describe(): string;
}

/**
 * Per-turn context handed to the harness by the control server.
 * The harness receives this on every runTurn() call — it never talks to StreamService directly.
 */
export interface TurnContext {
  sessionId: string;
  queryId: string;
  /** The control server owns cancellation; the harness honours this signal. */
  signal: AbortSignal;
  /** Typed SSE surface, pre-bound to (sessionId, queryId). */
  emit: HarnessEmitter;
  /** Resolved tool allowlist + MCP server map for this turn. */
  resolved: ResolvedTools;
  /** Proxy URL, host API URL, model defaults, etc. */
  config: SandboxConfig;
}

/**
 * Typed SSE event surface — the harness NEVER touches StreamService directly.
 * Constructed from (streamService, sessionId, queryId); all methods already have
 * queryId bound.
 */
export interface HarnessEmitter {
  // Message lifecycle
  messageStart(messageId: string, role?: string): void;
  contentDelta(messageId: string, delta: string): void;
  thinkingDelta(messageId: string, delta: string): void;
  messageEnd(messageId: string): void;

  // Tool lifecycle
  toolUseStart(toolCallId: string, toolName: string, input: Record<string, unknown>): void;
  toolUseEnd(toolCallId: string, output: string, isError?: boolean): void;
  toolProgress(toolUseId: string, toolName: string, elapsedSeconds: number, parentToolUseId: string | null): void;
  toolInputDelta(partialJson: string): void;

  // Hook / subagent / diagnostic events
  hookEvent(hookType: 'pre_tool' | 'post_tool' | 'tool_failure' | 'notification' | 'hook_response', payload: Record<string, unknown>): void;
  subagentEvent(event: 'start' | 'stop', agentId: string, agentType?: string, result?: string): void;
  activityUpdate(phase: string, label: string, details?: Record<string, unknown>): void;
  systemStatus(status: 'init' | 'compacting' | 'ready' | 'auth', details?: Record<string, unknown>): void;
  sessionInfo(info: { tools: string[]; model: string; mcpServers: { name: string; status: string }[] }): void;

  /** Generic extension/plugin event (for custom SSE event types). */
  event(type: string, data: unknown): void;

  /** Mark the query complete. */
  endQuery(
    status: string,
    result?: string,
    totalCostUsd?: number,
    usage?: { inputTokens: number; outputTokens: number },
    model?: string,
  ): void;

  /** Emit an error event (does not end the query — call endQuery too). */
  error(code: string, message: string): void;
}

/**
 * One stateful instance per session. The control server creates one Harness per
 * session (via HarnessDescriptor.create) and routes every runTurn() call to it.
 */
export interface Harness {
  /** Stable identifier, e.g. "claude-agent-sdk". */
  readonly name: string;

  /**
   * Execute one agent turn. Emits all events through ctx.emit; honours ctx.signal
   * for cancellation. Does NOT end the query — endQuery is called inside runTurn
   * when the turn completes.
   */
  runTurn(req: QueryRequest, ctx: TurnContext): Promise<void>;

  /**
   * Seed this harness's conversation history from persisted messages.
   * Called by the host runner on session resume.
   */
  loadConversation(messages: Array<{ role: 'user' | 'assistant'; content: string }>): void;

  /**
   * Clear conversation history (phase transitions).
   */
  resetConversation(): void;

  /**
   * Graceful teardown on session destroy (optional).
   */
  dispose?(): Promise<void>;
}

/**
 * Concrete implementation of HarnessEmitter that delegates to StreamService,
 * with sessionId and queryId already bound.
 */
export class BoundHarnessEmitter implements HarnessEmitter {
  constructor(
    private readonly streamService: StreamService,
    private readonly sessionId: string,
    private readonly queryId: string,
  ) {}

  messageStart(messageId: string, role = 'assistant'): void {
    this.streamService.sendEvent(this.queryId, 'message_start', { messageId, role }, this.sessionId);
  }

  contentDelta(messageId: string, delta: string): void {
    this.streamService.sendContentDelta(this.queryId, messageId, delta, this.sessionId);
  }

  thinkingDelta(messageId: string, delta: string): void {
    this.streamService.sendThinkingDelta(this.queryId, messageId, delta, this.sessionId);
  }

  messageEnd(messageId: string): void {
    this.streamService.sendEvent(this.queryId, 'message_end', { messageId }, this.sessionId);
  }

  toolUseStart(toolCallId: string, toolName: string, input: Record<string, unknown>): void {
    this.streamService.sendToolUseStart(this.queryId, toolCallId, toolName, input, this.sessionId);
  }

  toolUseEnd(toolCallId: string, output: string, isError?: boolean): void {
    this.streamService.sendToolUseEnd(this.queryId, toolCallId, output, isError, this.sessionId);
  }

  toolProgress(toolUseId: string, toolName: string, elapsedSeconds: number, parentToolUseId: string | null): void {
    this.streamService.sendToolProgress(this.queryId, toolUseId, toolName, elapsedSeconds, parentToolUseId, this.sessionId);
  }

  toolInputDelta(partialJson: string): void {
    this.streamService.sendToolInputDelta(this.queryId, partialJson, this.sessionId);
  }

  hookEvent(hookType: 'pre_tool' | 'post_tool' | 'tool_failure' | 'notification' | 'hook_response', payload: Record<string, unknown>): void {
    this.streamService.sendHookEvent(this.queryId, hookType, payload, this.sessionId);
  }

  subagentEvent(event: 'start' | 'stop', agentId: string, agentType?: string, result?: string): void {
    this.streamService.sendSubagentEvent(this.queryId, event, agentId, agentType, result, this.sessionId);
  }

  activityUpdate(phase: string, label: string, details?: Record<string, unknown>): void {
    this.streamService.sendActivityUpdate(this.queryId, phase, label, details, this.sessionId);
  }

  systemStatus(status: 'init' | 'compacting' | 'ready' | 'auth', details?: Record<string, unknown>): void {
    this.streamService.sendSystemStatus(this.queryId, status, details, this.sessionId);
  }

  sessionInfo(info: { tools: string[]; model: string; mcpServers: { name: string; status: string }[] }): void {
    this.streamService.sendSessionInfo(this.queryId, info, this.sessionId);
  }

  event(type: string, data: unknown): void {
    this.streamService.sendEvent(this.queryId, type, data, this.sessionId);
  }

  endQuery(
    status: string,
    result?: string,
    totalCostUsd?: number,
    usage?: { inputTokens: number; outputTokens: number },
    model?: string,
  ): void {
    this.streamService.endQuery(this.queryId, status, result, totalCostUsd, usage, model, this.sessionId);
  }

  error(code: string, message: string): void {
    this.streamService.sendError(this.queryId, code, message, this.sessionId);
  }
}
