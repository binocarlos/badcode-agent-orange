# 05 — Event streaming & rendering

The event stream is the spine of the system: it carries everything the agent does from the in-image
SDK loop, through the host, to the browser, and into durable storage for replay. Three properties hold
it together — **one event vocabulary**, **one persistence/compaction step**, and **one rendering
reducer**. The host-side half of the pipeline lives in Go (`go/events/`); the producer-side buffer and
the browser reducer live in the TypeScript packages (`sandbox/`, `web/`).

## The event vocabulary (one source of truth)

There is a single canonical list of SSE event types, defined once in Go (`go/events/events.go`) and
mirrored into the TypeScript packages (`sandbox/`, `web/`).

```go
package events

type Type string

const (
	// Message lifecycle
	MessageStart  Type = "message_start"
	ContentDelta  Type = "content_delta"
	ThinkingDelta Type = "thinking_delta"
	MessageEnd    Type = "message_end"
	// Tool lifecycle
	ToolUseStart   Type = "tool_use_start"
	ToolUseEnd     Type = "tool_use_end"
	ToolProgress   Type = "tool_progress"
	ToolInputDelta Type = "tool_input_delta"
	// Interaction & artifacts (generic)
	AskUser            Type = "ask_user"
	ArtifactRegistered Type = "artifact_registered"
	ArtifactsUpdated   Type = "artifacts_updated"
	SkillHoisted       Type = "skill_hoisted"
	SkillInstalled     Type = "skill_installed"
	// Status / diagnostics
	SystemStatus  Type = "system_status"
	SessionInfo   Type = "session_info"
	ActivityUpdate Type = "activity_update"
	HookEvent     Type = "hook_event"
	SubagentEvent Type = "subagent_event"
	UserMessage   Type = "user_message"
	Heartbeat     Type = "heartbeat"
	// Terminal
	QueryComplete Type = "query_complete"
	Error         Type = "error"
	// Transport
	Connected Type = "connected"
)

// Envelope is one server-sent event.
type Envelope struct {
	Type Type           `json:"type"`
	Data map[string]any `json:"data"`
	Timestamp string    `json:"timestamp,omitempty"`
}
```

**Application-specific events** (e.g. `table_rendered`, `chart_rendered`, `dashboard_created`,
`webapp_ready`, `page_tool_request`, `settings_updated`) are **not** in the generic core. They are
registered by the host/tool plugins as **extension event types** the reducer and renderer dispatch by
name (see [07](07-in-image-agent.md), and "Rendering in the web package" below). The generic vocabulary
is the ~20 events that every agent product needs; the long tail is plugin-defined.

## The pipeline (Go host side)

The host-side event work — relay, compact, persist — is `events.EventPipeline` in Go.

```go
package events

// EventPipeline consumes the in-image agent's SSE stream for one query, relays raw bytes
// to a client writer, and in parallel compacts + persists the events.
type EventPipeline interface {
	// Run reads SSE frames from src (the in-image agent response body), writes them
	// verbatim to client (the browser), accumulates + compacts them, and persists via the
	// Sink on a cadence and at end-of-query. Honours the flush guard through Sink hooks.
	Run(ctx context.Context, q QueryContext, src io.Reader, client io.Writer) (Result, error)
}

// Sink is how the pipeline persists — implemented over the host's RunnerStore. It also
// carries the flush-guard hooks so the orchestration core can block archiving mid-flush.
type Sink interface {
	BeginFlush(sessionID string)                  // increments pending-flush counter
	PersistQueryEvents(ctx context.Context, sessionID, queryID string, events []Envelope, searchText string) error
	EndFlush(sessionID string)                    // decrements
}
```

### Compaction (`events/compact.go`)

Compaction is pure and deterministic:

- **Drop transient types** — `heartbeat`, `tool_progress`, `tool_input_delta`, `activity_update`,
  `system_status`, `hook_event`, `connected`. (Configurable set.)
- **Merge consecutive** `content_delta` and `thinking_delta` into one event each (concatenate deltas).
- **Drop empty** `user_message` (reconnect artifact).
- **`ExtractSearchText`** — concatenate user content + assistant content, cap ~10k chars, for FTS.

```go
func Compact(in []Envelope) []Envelope
func ExtractSearchText(in []Envelope) string
```

These are the functions the host persists through, so a restored session replays *compacted* events —
identical to the live stream.

### The flush guard, restated

The pipeline calls `Sink.BeginFlush`/`EndFlush` around every persist. The orchestration core's
archive transition checks the counter. This is why an idle session can be archived safely: the loop
*cannot* archive while a flush is in flight.

## Late-connect replay (two layers)

Replay exists at two layers, and both are kept:

1. **In-image buffer** — the `StreamService` (in `sandbox/`) buffers up to 2000 events per query and
   replays them to a consumer that attaches late or reconnects mid-query. This lives *inside the image*.
2. **Durable replay** (`replayEvents` in `web/`) — if the live stream is irrecoverable, the browser
   loads the persisted compacted events from the host and re-runs them through the *same reducer* to
   reconstruct state.

The host (Go) sits between them: `Runner.Stream` proxies the in-image buffer for reconnects;
`RunnerStore.ListQueryEvents` feeds durable replay.

## The single reducer (the invariant we must not break)

**There is exactly one codepath that reconstructs UI from events** — the `agentEventReducer` — and it
serves live streaming and restored sessions identically. This is a hard invariant:

- Live events and compacted-replayed events go through the **same** `agentEventReducer`
  (`web/src/agentEventReducer.ts`).
- The reducer is a **pure function** `(state, event) → state` with no side effects, so replay is
  deterministic.
- New event types are added **only** to the reducer (via the plugin dispatch), never via a separate
  reconstruction path.

The next section covers how the `web/` package renders these events and how plugins extend the reducer
without forking it.

## Tool-input-delta coalescing

`tool_input_delta` fires ~1600×/sec during large `write_file` calls. The in-image `StreamService`
coalesces them over 150ms before emitting. This stays in `sandbox/` (it's a producer-side concern).
The Go pipeline additionally *drops* them during compaction (they're transient), so they never reach
storage — only the live stream shows the typing-in preview.

## Token usage & marker side-effects

As the pipeline scans the live stream it invokes host hooks on specific events:

- **`artifact_registered`** → host pulls the file from the workspace and `ArtifactStore.Save`s it
  (see [06](06-artifacts.md)); the pipeline injects an `artifacts_updated` event so the browser
  refreshes.
- **token usage** in `query_complete`/`result` → `TokenUsageLogger.Log(...)` (host extension).
- **title-bot trigger** → host hook (a host may generate a session title; generic hook so other
  products can ignore it).

These are **host hooks**, registered on the pipeline, not baked in — that's how the generic core stays
free of any one product's costing and title logic. See [14-host-adapters.md](14-host-adapters.md).

## Rendering in the web package

The `web/` package turns the event stream into a polished chat UI. It preserves the same invariant the
pipeline does — **one reducer, one rendering codepath, live and replayed alike** — and keeps
product-specific widgets out of the generic core behind a render-plugin seam.

### The single reducer, on the client

`web/src/agentEventReducer.ts` is a pure `(state, event) => state` function that reconstructs the
entire conversation UI from events. It serves three callers identically: live SSE
(`useAgentSession`), durable replay (`replayEvents` re-runs persisted compacted events), and tests.
Because it is pure and side-effect-free, replay is deterministic — a restored session looks exactly
like the live one. Any second reconstruction path is a bug.

The reducer's `AgentEventState` keeps a generic shape: `messages[]`, `isStreaming`, `error`,
`artifacts[]`, `currentMessage`, keyed maps `toolCalls`/`askedQuestions`, continuation-splitting state
that interleaves text→tool→text within one turn, `activityStatus`, `toolInputBuffer`, `todos`,
`sessionInfo`, `subagentEvents`. Product-coupled maps (rendered tables/charts/dashboards) are **not**
core state — they become render-plugin state.

### The render-plugin seam

The reducer and components dispatch **extension event types** (`table_rendered`, `chart_rendered`,
`dashboard_created`, `webapp_ready`, `page_tool_request`, `settings_updated`) to registered plugins
instead of handling them inline. A render plugin is declared in `web/src/plugins.ts`:

```ts
interface RenderPlugin<TState = unknown> {
  // Extension event types this plugin owns.
  eventTypes: string[];
  // Fold a plugin event into plugin-scoped state (kept in a side-channel map keyed by
  // toolCallId/messageId), so the core reducer state stays generic.
  reduce(state: TState, event: AgentSSEEvent): TState;
  // Render the plugin's artifact inline, given the tool call it attaches to.
  render(props: { event: TState; toolCallId: string; sessionId: string }): React.ReactNode;
}
```

- **Generic core** handles the ~20 generic event types and renders messages, thinking blocks, content,
  tool-call cards, `ask_user` cards, artifact previews, the input toolbar, and stuck-detection banners.
- **Product render plugins** own the app-specific widgets (e.g. a Carbon-backed table/chart/dashboard
  bundle) and register into the library's `<AgentChatProvider plugins={[...]}>`.

### Parameterisation (the host wires these)

`useAgentSession` and `AgentChat` take their product-isms as props/config, not imports:

- **API base + endpoints** — `createSession`/`sendMessage`/`stream`/`reconnect`/`artifacts`/`upload`
  paths (props with sensible defaults).
- **Model list** — `models: {id,label}[]` instead of a hard-coded list.
- **Side-effect callbacks** — `onToolResult`, `onSessionTitle`, `onArtifactsUpdated` (products pass
  no-ops if unused).
- **Render plugins** — the array above.
- **Theme** — the MUI theme is the host's; the library uses semantic tokens, not a literal palette.

This is what keeps `web/` free of any dependency on a specific product's app state, routing, or
contexts — those entanglements are replaced by props/provider.

---

> **Provenance.** The host-side pipeline (`go/events/`) and the `web/` reducer/components were ported
> from an in-house TypeScript runtime; that original host now lives under `migration-reference/` for
> reference only.
