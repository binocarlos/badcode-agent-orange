import { z } from 'zod';

// ============================================
// Request Schemas
// ============================================

export const QueryRequestSchema = z.object({
  prompt: z.string().min(1).describe('The user prompt to send to the agent'),
  systemPrompt: z.string().optional().describe('Custom system prompt for this query'),
  tools: z.array(z.string()).optional().describe('Allowed SDK tool names (e.g. WebSearch, WebFetch)'),
  model: z.string().optional().describe('Model override'),
  maxTurns: z.number().optional().describe('Max agent turns override'),
  attachments: z.array(z.object({
    mimeType: z.string(),
    base64Content: z.string(),
    fileName: z.string(),
  })).optional().describe('File attachments with base64-encoded content'),
  planMode: z.enum(['none', 'suggest', 'require']).optional().describe('Plan enforcement mode: none (default), suggest (prompt only), require (block tools until TodoWrite)'),
  harness: z.string().optional().describe('Harness name; defaults to claude-agent-sdk'),
});

export type QueryRequest = z.infer<typeof QueryRequestSchema>;

// ============================================
// SSE Event Types — mirrors go/events/events.go
// GENERIC types only. Application-specific events (table_rendered,
// dashboard_created, ...) are extension types registered by tool plugins;
// they are NOT listed here.
// ============================================

export type SSEEventType =
  // Message lifecycle
  | 'message_start'
  | 'content_delta'
  | 'thinking_delta'
  | 'message_end'
  // Tool lifecycle
  | 'tool_use_start'
  | 'tool_use_end'
  | 'tool_progress'
  | 'tool_input_delta'
  // Interaction & artifacts (generic)
  | 'ask_user'
  | 'artifact_registered'
  | 'artifacts_updated'
  // Status / diagnostics
  | 'system_status'
  | 'session_info'
  | 'activity_update'
  | 'hook_event'
  | 'subagent_event'
  | 'user_message'
  | 'heartbeat'
  // Terminal
  | 'query_complete'
  | 'error'
  // Connection marker (dropped in compaction)
  | 'connected';

export interface SSEEvent {
  type: SSEEventType | string; // string allows extension event types from plugins
  data: unknown;
  timestamp: string;
}

// ============================================
// Detailed SSE Event Data Types
// ============================================

export interface ContentDeltaEvent {
  type: 'content_delta';
  data: {
    delta: string;
    messageId: string;
  };
  timestamp: string;
}

export interface ToolUseStartEvent {
  type: 'tool_use_start';
  data: {
    toolCallId: string;
    toolName: string;
    input: Record<string, unknown>;
  };
  timestamp: string;
}

export interface ToolUseEndEvent {
  type: 'tool_use_end';
  data: {
    toolCallId: string;
    output: string;
  };
  timestamp: string;
}

/**
 * The billed token components of one turn, as `query_complete` carries them.
 *
 * This is the ONLY thing the product's spend brake can measure, so it has to
 * be the whole bill. The provider's usage object (`BetaUsage` in
 * `@anthropic-ai/sdk`) splits input across THREE separately-billed fields, and
 * `input_tokens` is only one of them:
 *
 *   input_tokens                 "The number of input tokens which were used."
 *   cache_creation_input_tokens  "The number of input tokens used to create the cache entry."
 *   cache_read_input_tokens      "The number of input tokens read from the cache."
 *
 * None of the three includes the others. Until 2026-07-29 the harness forwarded
 * only the first, so with a large composed prompt and caching active — where
 * most input arrives as cache reads — the ledger reported a plausible fraction
 * of true spend and `daily_tokens_hard` fired far too late or never.
 *
 * The other members of `BetaUsage` are deliberately NOT forwarded because they
 * are not additional billed tokens: `cache_creation` is a TTL breakdown OF
 * `cache_creation_input_tokens`, `output_tokens_details` is a read-only
 * decomposition of `output_tokens` (which the SDK documents as "the inclusive,
 * authoritative total used for billing"), `server_tool_use` counts requests
 * rather than tokens, and `iterations` re-states per-iteration subtotals.
 * Summing any of them would double-count.
 *
 * Key spelling is camelCase because that is what `go/agentdb/token_usage.go`
 * reads and what every stored envelope carries; the cache fields are optional
 * so envelopes written before this change stay readable.
 */
export interface TokenUsage {
  inputTokens: number;
  outputTokens: number;
  cacheCreationInputTokens?: number;
  cacheReadInputTokens?: number;
}

export interface QueryCompleteEvent {
  type: 'query_complete';
  data: {
    queryId: string;
    status: 'completed' | 'error' | 'cancelled';
    result?: string;
    totalCostUsd?: number;
    usage?: TokenUsage;
    model?: string;
  };
  timestamp: string;
}

export interface ToolProgressEvent {
  type: 'tool_progress';
  data: {
    toolUseId: string;
    toolName: string;
    elapsedSeconds: number;
    parentToolUseId: string | null;
  };
  timestamp: string;
}

export interface SystemStatusEvent {
  type: 'system_status';
  data: {
    status: 'init' | 'compacting' | 'ready' | 'auth';
    details?: Record<string, unknown>;
  };
  timestamp: string;
}

export interface SessionInfoEvent {
  type: 'session_info';
  data: {
    tools: string[];
    model: string;
    mcpServers: { name: string; status: string }[];
  };
  timestamp: string;
}

export type HookEventType = 'pre_tool' | 'post_tool' | 'tool_failure' | 'notification' | 'hook_response';

export interface HookEventData {
  type: 'hook_event';
  data: {
    hookType: HookEventType;
    payload: Record<string, unknown>;
  };
  timestamp: string;
}

export interface SubagentEvent {
  type: 'subagent_event';
  data: {
    event: 'start' | 'stop';
    agentId: string;
    agentType?: string;
    result?: string;
  };
  timestamp: string;
}

// ============================================
// API Response Types
// ============================================

export interface ApiResponse<T> {
  success: boolean;
  data?: T;
  error?: {
    code: string;
    message: string;
  };
}

export interface QueryStartResponse {
  queryId: string;
  streamUrl: string;
}
