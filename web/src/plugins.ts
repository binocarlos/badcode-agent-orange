// The render-plugin seam (browser). Keeps the agentEventReducer and chat
// components generic: application-specific event types (table_rendered,
// chart_rendered, dashboard_created, webapp_ready, ...) are handled by registered
// plugins rather than inline in the core. Platinum's Carbon table/chart/dashboard
// widgets register here. See ../../docs/09-frontend-components.md.

import type { ReactNode } from "react";

/** One server-sent event as seen by the browser (mirror of the Go events.Envelope
 *  and the sandbox AgentEvent). */
export interface AgentSSEEvent {
  type: string;
  data: Record<string, unknown>;
  timestamp?: string;
}

/** A render plugin owns one or more extension event types: it folds them into
 *  plugin-scoped state (kept in a side-channel keyed by toolCallId/messageId, so
 *  the core reducer state stays generic) and renders them inline. */
export interface RenderPlugin<TState = unknown> {
  /** Extension event types this plugin handles, e.g. ["table_rendered"]. */
  eventTypes: string[];
  /** Initial plugin state. */
  init(): TState;
  /** Fold a plugin event into plugin-scoped state. Pure — replay-safe. */
  reduce(state: TState, event: AgentSSEEvent): TState;
  /** Render the plugin's artifact inline, attached to a tool call. */
  render(props: { state: TState; toolCallId: string; sessionId: string }): ReactNode;
}

/** A tool formatter customises how a tool call is labelled/summarised in the UI
 *  (e.g. Platinum's `pt` CLI command parsing). Generic formatters ship in core. */
export interface ToolFormatter {
  /** Return a display name for a tool call, or undefined to fall through. */
  displayName?(toolName: string, input: unknown): string | undefined;
  /** Return a one-line summary, or undefined to fall through. */
  summary?(toolName: string, input: unknown): string | undefined;
}

/** Everything a host injects into <AgentChatProvider>. */
export interface AgentChatConfig {
  apiBaseUrl: string;
  models: { id: string; label: string }[];
  plugins?: RenderPlugin[];
  toolFormatters?: ToolFormatter[];
  endpoints?: Partial<AgentChatEndpoints>;
  /** Async or sync token provider — used by Provider contexts for session-list API calls. */
  getAuthToken?: () => Promise<string> | string;
  onToolResult?: (toolCallId: string, output: unknown) => void;
  onSessionTitle?: (sessionId: string, title: string) => void;
  onArtifactsUpdated?: (sessionId: string) => void;
  onSnapshotComplete?: (sessionId: string, progress: { op: string; phase: string; err?: string }) => void;
}

/** Endpoint paths, overridable; defaults match the conventional /agent/* shape. */
export interface AgentChatEndpoints {
  createSession: string;                        // POST
  sendMessage: (sessionId: string) => string;   // POST (SSE)
  reconnect: (sessionId: string) => string;     // GET (SSE)
  status: (sessionId: string) => string;        // GET
  cancel: (sessionId: string) => string;        // POST
  getSession: (sessionId: string) => string;    // GET
  deleteSession: (sessionId: string) => string; // DELETE
  restore: (sessionId: string) => string;       // POST
  messages: (sessionId: string) => string;      // GET
  queryEvents: (sessionId: string) => string;   // GET
  artifacts: (sessionId: string) => string;     // GET/POST
  upload: (sessionId: string) => string;        // POST
  listSessions: string;                         // GET
  searchMessages: string;                       // GET
}

export const DEFAULT_ENDPOINTS: AgentChatEndpoints = {
  createSession: "/agent/session",
  sendMessage: (id) => `/agent/session/${id}/message`,
  reconnect: (id) => `/agent/session/${id}/reconnect`,
  status: (id) => `/agent/session/${id}/status`,
  cancel: (id) => `/agent/session/${id}/cancel`,
  getSession: (id) => `/agent/session/${id}`,
  deleteSession: (id) => `/agent/session/${id}`,
  restore: (id) => `/agent/session/${id}/restore`,
  messages: (id) => `/agent/session/${id}/messages`,
  queryEvents: (id) => `/agent/session/${id}/query-events`,
  artifacts: (id) => `/agent/session/${id}/artifacts`,
  upload: (id) => `/agent/session/${id}/upload`,
  listSessions: "/agent/sessions",
  searchMessages: "/agent/messages/search",
};

// The AgentChatProvider, the reducer's plugin dispatch, and the components are
// part of the frontend copy per ../../docs/90-provenance-map.md.
