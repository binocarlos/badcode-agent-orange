# 05 — Session event streaming & rendering

> **Scope.** This document describes the **session** event stream: what one agent turn emits inside
> one container, and how it reaches the browser and durable storage. It is `go/events/`.
>
> It is **not** the product layer's event spine. Those are two unrelated systems that both use the
> word "event":
>
> | | **Session events** (this doc) | **Project events** (`docs/product/04`) |
> |---|---|---|
> | Type | SSE frames — `content_delta`, `tool_use_start`, … | rows in `project_events` — `worker.finished`, `config.changed`, external POSTs |
> | Transport | `text/event-stream`, streamed live | ordinary JSON routes; the router **polls** the table every 3s |
> | Lifetime | one query | append-only, forever |
> | Stored as | `agent_query_events` (compacted) | `project_events` + `event_deliveries` |
> | Purpose | render a conversation | wake a worker |
> | Code | `go/events/`, `sandbox/`, `web/src/agentEventReducer.ts` | `go/agentdb/events.go`, `go/cmd/agentd/router.go` |
>
> Nothing in `go/events/` knows subscriptions, the router or schedules exist. Read
> [`product/04-events-and-schedules.md`](product/04-events-and-schedules.md) for that half.
>
> The one place they touch: when a session that carries a product worker finishes, the **Runner**
> appends a `worker.finished` (or `worker.failed`) row to the project spine via `Deps.WorkerEvents`,
> with the rendered transcript as its text. That is a write into the other system, not part of this
> vocabulary.

The session event stream is the spine of a conversation: it carries everything the agent does from
the in-image SDK loop, through the host, to the browser, and into durable storage for replay. Three
properties hold it together — **one event vocabulary**, **one persistence/compaction step**, and
**one rendering reducer**. The host-side half of the pipeline lives in Go (`go/events/`); the
producer-side buffer and the browser reducer live in the TypeScript packages (`sandbox/`, `web/`).

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
  `system_status`, `hook_event`, `connected`. The set is the package-level `transientTypes` map in
  `events/compact.go`; there is no configuration surface for it.
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
`RunnerStore.ListQueryEventsFlat` feeds durable replay.

### The two writes that make a turn survive a crash (D2 / doc 22 RD6+RD24)

The pipeline persists on a **cadence** (`Policy.EventFlushCadence`, default 2s) as well as at
`query_complete`. Without the cadence the model's output existed only in a slice inside
`pipeline.Run` until the turn ended: kill agentd mid-turn and the transcript kept the human's
question — `seedUserMessage` writes that before the turn is dispatched — and nothing else.

`Runner.Stream` runs its SSE through a pipeline too, so what the in-image buffer replays to a
reconnecting client is **written down**, not just rendered. Three rules keep that from
double-recording a turn:

- **One writer per session at a time.** `SendMessage` owns the turn it dispatched; a stream
  attachment persists only when nobody else is. Both attachments receive every event the sandbox
  sends, so two writers would record the same words twice.
- **Append, never replace.** A reconnect sees only what the sandbox buffered *since the previous
  stream detached*, so writing it as the whole turn would erase the earlier half. `events.Splice`
  joins the new events onto the stored ones, absorbing any overlap.
- **Only against a store that can read one turn back** — the optional
  `ListQueryEventsFlatForQuery` capability on `RunnerStore`. Without it the stream relays and
  persists nothing, exactly as before.

What is still lost: events the dying process read off the socket but had not yet flushed. The
sandbox buffers only while no stream is attached, so those are in neither place.

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

## Marker side-effects

A `MarkerHook` is `func(ctx, QueryContext, Envelope)` registered against one event type at
`events.NewPipeline(...)`. The pipeline fires it as that event streams past — it is a side channel,
not part of compaction or relay. `NewRunner` registers exactly four:

| Event | Hook | What it does |
|---|---|---|
| `artifact_registered` | `onArtifactRegistered` | pulls the file (or, for `artifactType: "webapp"`, the whole containing directory) out of the workspace and `ArtifactStore.Save`s it — see [06](06-artifacts.md) |
| `skill_hoisted` | `onSkillHoisted` | records a skill the session lifted out of its workspace |
| `skill_installed` | `onSkillInstalled` | records a skill installed live into the session |
| `error` | `onQueryError` | stashes the failure text so a worker job can report `worker.failed` |

A host that supplies its own `Deps.Events` pipeline replaces all four; there is no way to add a
fifth without constructing the pipeline yourself.

Two things this section used to claim, which are not true of the code:

- **No `artifacts_updated` is ever emitted.** The type is declared in the vocabulary (Go, `sandbox/`,
  `web/`) and `web/src/useAgentSession.ts` handles it, but nothing in the repo produces one. The
  browser learns about new artifacts from `artifact_registered` instead.
- **`extension.TokenUsageLogger` is never called.** The interface exists, `Deps.TokenLogger` accepts
  an implementation, and no code path in the module invokes `Log`. Token usage in
  `query_complete` is not extracted or reported by the engine.

Session titling is likewise **not** a pipeline hook: `httpapi/stream.go` fires `titlebot.Generate`
in a goroutine after `SendMessage` returns, when the session has no title yet and a `ChatClient` is
configured. See [14-host-adapters.md](14-host-adapters.md).

## Rendering in the web package

The `web/` package turns the event stream into a polished chat UI. It preserves the same invariant the
pipeline does — **one reducer, one rendering codepath, live and replayed alike** — and keeps
product-specific widgets out of the generic core behind a render-plugin seam.

`web/` contains more than this: the product layer's screens (`WorkersPage`, `EventsPage`,
`ProjectSettingsPage`, `ChangelogView`, the subscription and schedule editors) and their hooks
(`useWorkers`, `useEvents`, `useSchedules`, `useConfigLog`) also live there. Those poll JSON routes
and never touch `agentEventReducer` — the invariant below is about the chat surface only.

### The single reducer, on the client

`web/src/agentEventReducer.ts` is a pure `(state, event) => state` function that reconstructs the
entire conversation UI from events. It serves three callers identically: live SSE
(`useAgentSession`), durable replay (`replayEvents` re-runs persisted compacted events), and tests.
Because it is pure and side-effect-free, replay is deterministic — a restored session looks exactly
like the live one. Any second reconstruction path is a bug.

The reducer's `AgentEventState` holds `messages[]`, `isStreaming`, `error`, `artifacts[]`,
`currentMessage`, keyed maps `toolCalls`/`askedQuestions`, continuation-splitting state that
interleaves text→tool→text within one turn, `activityStatus`, `toolInputBuffer`, `todos`,
`sessionInfo`, `subagentEvents`, `installedSkills`.

It **also** holds product-coupled fields — `renderedTables`, `renderedCharts`, `createdDashboards`,
`pendingPageToolRequest`, `pendingSettingsUpdate`. These were intended to move out to plugin state
and did not: they are core `AgentEventState` today. The separation below is real but partial.

### The render-plugin seam

Extension event types (`table_rendered`, `chart_rendered`, `dashboard_created`, `webapp_ready`,
`page_tool_request`, `settings_updated`) are handled **both** inline in the core reducer *and*
dispatched to registered plugins — the reducer's own docstring says so. A render plugin is declared
in `web/src/plugins.ts`:

```ts
interface RenderPlugin<TState = unknown> {
  // Extension event types this plugin handles.
  eventTypes: string[];
  // Initial plugin state.
  init(): TState;
  // Fold a plugin event into plugin-scoped state. Pure — replay-safe.
  reduce(state: TState, event: AgentSSEEvent): TState;
  // Render the plugin's artifact inline, attached to a tool call.
  render(props: { state: TState; toolCallId: string; sessionId: string }): ReactNode;
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
