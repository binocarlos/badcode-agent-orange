// The HTTP/SSE contract between the Go Runner and the in-image agent.
//
// The SSE event vocabulary mirrors the canonical Go definition in
// ../../go/events/events.go — keep them in sync (the Go side is the source of
// truth; this is the TS mirror). See ../../docs/05-event-streaming.md and
// ../../docs/07-in-image-agent.md.

/** Generic SSE event types every agent product needs. Application-specific
 *  events (table_rendered, dashboard_created, ...) are registered by tool/render
 *  plugins as extension types and are deliberately NOT listed here. */
export type AgentEventType =
  | "message_start"
  | "content_delta"
  | "thinking_delta"
  | "message_end"
  | "tool_use_start"
  | "tool_use_end"
  | "tool_progress"
  | "tool_input_delta"
  | "ask_user"
  | "artifact_registered"
  | "artifacts_updated"
  | "system_status"
  | "session_info"
  | "activity_update"
  | "hook_event"
  | "subagent_event"
  | "user_message"
  | "heartbeat"
  | "query_complete"
  | "error"
  | "connected";

/** One server-sent event. Extension event types use a plain string. */
export interface AgentEvent {
  type: AgentEventType | string;
  data: Record<string, unknown>;
  timestamp?: string;
}

/** POST /query-stream request body. systemPrompt has the host's org context
 *  already appended by the Runner. */
export interface QueryRequest {
  prompt: string;
  systemPrompt?: string;
  tools?: string[]; // allowlist; empty = all
  model?: string;
  maxTurns?: number;
  planMode?: "none" | "suggest" | "require";
  attachments?: Attachment[];
  harness?: string; // harness name; defaults to claude-agent-sdk
}

export interface Attachment {
  mimeType: string;
  base64Content: string;
  fileName: string;
}

/** POST /sessions request body — the Runner boots a session here.
 *  Note `mcp_servers` is snake_case: it mirrors the Go JSON tags on
 *  agentdb.MCPServers exactly (docs/product/01-session-config.md §4.2). */
export interface CreateSessionRequest {
  sessionId: string;
  harness?: string; // harness name; defaults to claude-agent-sdk
  model?: string;
  maxTurns?: number;
  mcp_servers?: Record<string, MCPServerConfig>;
}

/** One session-scoped MCP server. Exactly ONE transport: stdio
 *  (`command`/`args`/`env`) or http (`url`/`headers`). Values in `env` and
 *  `headers` may be whole-value `${VAR}` references — the NAME of an env var of
 *  the session container, resolved inside the container at MCP-process spawn
 *  time. Never secret values (§4.4), which is what makes this config safe to
 *  persist and display. Mirrors go/agentdb.MCPServerConfig field-for-field. */
export interface MCPServerConfig {
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
}

/** GET /health response. */
export interface HealthResponse {
  status: "ok";
  sessionId: string;
}

/** Environment variables the ExecutionEnvironment injects into the image.
 *  (Documented here so the contract is explicit; see docs/07.) */
export interface SandboxEnv {
  SESSION_ID: string;
  SESSION_TOKEN: string;
  ANTHROPIC_BASE_URL?: string; // host model proxy (key injection); unset = direct Anthropic API
  CLAUDE_CODE_OAUTH_TOKEN?: string; // subscription credential for direct mode (from `claude setup-token`)
  HOST_API_URL?: string; // host API for tool callbacks (was GOAPI_URL)
  DEFAULT_MODEL?: string;
  PORT?: string; // default 3010
}
