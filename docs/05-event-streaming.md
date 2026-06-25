# 05 — Event streaming, compaction, and the single reducer

The event stream is the spine of the system: it carries everything the agent does from the in-image
SDK loop, through the host, to the browser, and into durable storage for replay. The library
preserves the three properties that make the current implementation good — **one event vocabulary**,
**one persistence/compaction step**, and **one rendering reducer** — and moves the host-side half from
TypeScript into Go.

## The event vocabulary (one source of truth)

There is a single canonical list of SSE event types, defined once and mirrored across runtimes. Today
it appears in three places (`agent/src/types/index.ts`, the orchestrator's `compact-events.ts`
constants, `frontend/src/types/agent.ts`). The library makes the **Go** definition canonical and
generates/mirrors the TS copies.

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
	AskUser           Type = "ask_user"
	ArtifactRegistered Type = "artifact_registered"
	ArtifactsUpdated   Type = "artifacts_updated"
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
)

// Envelope is one server-sent event.
type Envelope struct {
	Type Type           `json:"type"`
	Data map[string]any `json:"data"`
	Timestamp string    `json:"timestamp,omitempty"`
}
```

**Application-specific events** (Platinum's `table_rendered`, `chart_rendered`, `dashboard_created`,
`webapp_ready`, `page_tool_request`, `settings_updated`) are **not** in the generic core. They are
registered by the host/tool plugins as **extension event types** the reducer and renderer dispatch by
name (see [08](08-tool-registry.md), [09](09-frontend-components.md)). The generic vocabulary is the
~20 events that every agent product needs; the long tail is plugin-defined.

## The pipeline (TypeScript orchestrator → Go)

Today `routes/sessions.ts` + `message-capture.ts` + `compact-events.ts` do the host-side event work in
TypeScript. The library reimplements this as `events.EventPipeline` in Go.

```go
package events

// EventPipeline consumes the in-image agent's SSE stream for one query, relays raw bytes
// to a client writer, and in parallel compacts + persists the events. It is the Go
// successor to the orchestrator's MessageCapture + persistQueryEvents + compactEvents.
type EventPipeline interface {
	// Run reads SSE frames from src (the in-image agent response body), writes them
	// verbatim to client (the browser), accumulates + compacts them, and persists via the
	// Sink on a cadence and at end-of-query. Honours the flush guard through Sink hooks.
	Run(ctx context.Context, q QueryContext, src io.Reader, client io.Writer) (Result, error)
}

// Sink is how the pipeline persists — implemented over the host's SessionStore. It also
// carries the flush-guard hooks so the orchestration core can block archiving mid-flush.
type Sink interface {
	BeginFlush(sessionID string)                  // increments pending-flush counter
	PersistQueryEvents(ctx context.Context, sessionID, queryID string, events []Envelope, searchText string) error
	EndFlush(sessionID string)                    // decrements
}
```

### Compaction (`compact-events.ts` → `events/compact.go`)

Ported verbatim because it's pure and correct:

- **Drop transient types** — `heartbeat`, `tool_progress`, `tool_input_delta`, `activity_update`,
  `system_status`, `hook_event`, `connected`. (Configurable set; defaults match today.)
- **Merge consecutive** `content_delta` and `thinking_delta` into one event each (concatenate deltas).
- **Drop empty** `user_message` (reconnect artifact).
- **`extractSearchText`** — concatenate user content + assistant content, cap ~10k chars, for FTS.

```go
func Compact(in []Envelope) []Envelope        // = compactEvents
func ExtractSearchText(in []Envelope) string  // = extractSearchText
```

These are the functions the host persists through, so a restored session replays *compacted* events —
identical to today.

### The flush guard, restated

The pipeline calls `Sink.BeginFlush`/`EndFlush` around every persist. The orchestration core's
archive transition checks the counter. This is why an idle session can be archived safely: the loop
*cannot* archive while a flush is in flight. Ported exactly from `state-machine.ts` + the
`incrementFlush/decrementFlush` calls scattered through `routes/sessions.ts`.

## Late-connect replay (two layers)

Replay exists at two layers and the library keeps both:

1. **In-image buffer** (`agent/src/services/stream-service.ts`) — the `StreamService` buffers up to
   2000 events per query and replays them to a consumer that attaches late or reconnects mid-query.
   This lives *inside the image* and is unchanged (it's part of `sandbox/`).
2. **Durable replay** (frontend `replayEvents.ts`) — if the live stream is irrecoverable, the browser
   loads the persisted compacted events from the host and re-runs them through the *same reducer* to
   reconstruct state. Unchanged, lives in `web/`.

The host (Go) sits between them: `Runner.Stream` proxies the in-image buffer for reconnects;
`SessionStore.ListQueryEvents` feeds durable replay.

## The single reducer (the invariant we must not break)

CLAUDE.md rule 12: **there is exactly one codepath that reconstructs UI from events** — the
`agentEventReducer` — and it serves live streaming and restored sessions identically. The library
treats this as a hard invariant:

- Live events and compacted-replayed events go through the **same** `agentEventReducer`
  (`web/src/agentEventReducer.ts`).
- The reducer is a **pure function** `(state, event) → state` with no side effects, so replay is
  deterministic.
- New event types are added **only** to the reducer (via the plugin dispatch), never via a separate
  reconstruction path.

See [09-frontend-components.md](09-frontend-components.md) for the reducer's state shape and how
plugins extend it without forking it.

## Tool-input-delta coalescing

`tool_input_delta` fires ~1600×/sec during large `write_file` calls. The in-image `StreamService`
coalesces them over 150ms before emitting. This stays in `sandbox/` (it's a producer-side concern).
The Go pipeline additionally *drops* them during compaction (they're transient), so they never reach
storage — only the live stream shows the typing-in preview.

## Token usage & marker side-effects

As the pipeline scans the live stream it invokes host hooks on specific events (today done inline in
`agent.go`'s `onLine` callback):

- **`artifact_registered`** → host pulls the file from the workspace and `ArtifactStore.Save`s it
  (see [06](06-artifacts.md)); the pipeline injects an `artifacts_updated` event so the browser
  refreshes.
- **token usage** in `query_complete`/`result` → `TokenUsageLogger.Log(...)` (host extension).
- **title-bot trigger** → host hook (Platinum generates a session title; generic hook so other
  products can ignore it).

These are **host hooks**, registered on the pipeline, not baked in — that's how the generic core stays
free of Platinum's costing and title logic. See [10-extension-points.md](10-extension-points.md).

## Mapping: today → library

| Today | Library |
|-------|---------|
| `agent/src/types/index.ts` SSE types | `events/events.go` (canonical) + mirrored TS in `sandbox/`/`web/` |
| `orchestrator/src/compact-events.ts` | `events/compact.go` (`Compact`, `ExtractSearchText`) |
| `orchestrator/src/message-capture.ts` (`MessageCapture`, `persistQueryEvents`) | `events/pipeline.go` (`EventPipeline`, `Sink`) |
| `routes/sessions.ts` flush tracking | flush-guard hooks on `Sink` + state machine in `go/session.go` |
| `agent.go` `proxySSEStream`, `onLine` callbacks | `EventPipeline.Run` tee + host hooks |
| `agent/src/services/stream-service.ts` (in-image buffer) | unchanged → `sandbox/` |
| `frontend/.../agentEventReducer.ts`, `replayEvents.ts` | unchanged → `web/` |
