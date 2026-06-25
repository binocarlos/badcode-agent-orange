# 01 — Architecture

> **Redesign note (current).** The diagram below shows the v0 shape. Several refinements now apply and
> are specified in their own docs: (1) the in-image agent is a **multi-session, harness-agnostic control
> server** — the agentic framework (Claude Agent SDK / CLI / Gemini / Codex) is a **per-session**
> choice behind the harness seam ([12](12-harness.md)), not hard-wired; (2) the host composes a
> **`Fleet`** of `ExecutionEnvironment` *workers* for horizontal scaling ([13](13-fleet-placement.md)),
> and `Capabilities` splits into `Backend`/`Tenancy`/`IsolationTier` with a trust gate
> ([02](02-execution-environment.md)). Images stratify into core→app + session-snapshot + curated user
> images on one snapshot primitive ([03](03-image-registry.md), [06](06-artifacts.md)). Read those
> alongside this diagram.

## Three runtimes, one contract

The library spans three runtimes, but the boundaries between them are now deliberate and minimal.

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  HOST PROCESS  (Go — your API server; in Platinum, goapi)                      │
│                                                                                │
│   ┌────────────────────────────────────────────────────────────────────────┐ │
│   │  agentkit Runner  (the public facade — the only thing the host calls)    │ │
│   │    CreateSession · SendMessage · StreamEvents · Stop · Resume · Destroy  │ │
│   └───────────────┬──────────────────────────────┬───────────────────────────┘ │
│                   │                              │                              │
│   ┌───────────────▼───────────────┐   ┌──────────▼─────────────┐                │
│   │  Orchestration core (Go)       │   │  EventPipeline (Go)     │               │
│   │  • lifecycle state machine     │   │  • consume sandbox SSE  │               │
│   │  • idle reaper / archive loop  │   │  • compact + searchtext │               │
│   │  • flush guards, recovery      │   │  • persist via Store    │               │
│   │  • snapshot/restore coord.     │   │  • relay to client      │               │
│   └───┬───────────────────┬────────┘   └─────────┬──────────────┘                │
│       │                   │                      │                               │
│   ┌───▼──────────┐  ┌─────▼─────────┐   ┌────────▼────────┐  ┌────────────────┐  │
│   │ Execution    │  │ Image         │   │ SessionStore    │  │ Host extensions│  │
│   │ Environment  │  │ Registry      │   │ ArtifactStore   │  │ • OrgContext   │  │
│   │ (interface)  │  │ (interface)   │   │ (interfaces)    │  │ • TokenLogger  │  │
│   └───┬──────────┘  └─────┬─────────┘   └─────────────────┘  │ • Enricher     │  │
│       │                   │                                  │ • ClaimsIssuer │  │
│       │                   │                                  └────────────────┘  │
└───────┼───────────────────┼──────────────────────────────────────────────────────┘
        │ (engine-specific)  │ (build / save·load / push·pull)
        ▼                   ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  CONTAINER ENGINE   Docker socket │ Docker-in-Docker daemon │ Kubernetes API     │
│                                                                                │
│   ┌──────────────────────────────────────────────────────────────────────┐   │
│   │  IMAGE  (the agent image — installed deps, CLAUDE.md, .claude/skills)   │   │
│   │  ┌────────────────────────────────────────────────────────────────┐   │   │
│   │  │  IN-IMAGE AGENT  (TypeScript — agentkit/sandbox)                 │   │   │
│   │  │    Fastify server: /query-stream /stream/:id /cancel /health     │   │   │
│   │  │    AgentService → Claude Agent SDK → SSE event stream            │   │   │
│   │  │    StreamService (buffer + replay) · MCP tool server (plugins)   │   │   │
│   │  └────────────────────────────────────────────────────────────────┘   │   │
│   └──────────────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────────────┘

        ▲  SSE event stream (text/event-stream)
        │
┌───────┴───────────────────────────────────────────────────────────────────────┐
│  BROWSER  (React — agentkit/web)                                               │
│    useAgentSession → readSSEStream → agentEventReducer (THE single codepath)    │
│    AgentChat · ToolCallGroup · AskUserCard · ArtifactPanel · render plugins     │
└────────────────────────────────────────────────────────────────────────────────┘
```

### Runtime 1 — Host process (Go)

Everything that used to be split between `goapi/pkg/server/agent.go` (thin proxy) **and** the
TypeScript `orchestrator/` (the logic) collapses into one Go layer with a clean internal structure:

- **`Runner`** — the public facade. The host's HTTP handlers call this and nothing else. It is the
  spiritual successor to both `agent.go`'s handlers and the orchestrator's routes.
- **Orchestration core** — the generic lifecycle engine: the state machine
  (`orchestrator/src/state-machine.ts`), idle reaper + archive loop
  (`sandbox-manager.ts` control loops), flush guards, container recovery, snapshot/restore
  coordination. *This is the code that moves up from TypeScript into Go.*
- **`EventPipeline`** — consumes the in-image SSE stream, runs compaction
  (`orchestrator/src/compact-events.ts`), extracts search text, persists via `SessionStore`, and
  relays bytes to the browser. Successor to `message-capture.ts` + `agent.go`'s `proxySSEStream`.
- **Interfaces it depends on**: `ExecutionEnvironment`, `ImageRegistry`, `SessionStore`,
  `ArtifactStore`, `EventStreamer`, and the host-extension interfaces.

### Runtime 2 — In-image agent (TypeScript)

The only code that *must* run inside the container. Copied near-verbatim from `agent/src/`:

- A small Fastify server exposing the **sandbox contract**: `POST /query-stream`, `GET /stream/:queryId`,
  `POST /cancel`, `POST /load-conversation`, `POST /reset-conversation`, `GET /health`,
  `GET /workspace/files[...]`, `POST /workspace/scan-secrets`.
- `AgentService` — drives the Claude Agent SDK `query()` loop with hooks.
- `StreamService` — SSE delivery with a replay buffer for late/reconnecting consumers.
- An MCP **tool server** built from registered **tool plugins** (Platinum's `render_table` etc. become
  plugins; the generic core ships `ask_user`, `write_file`, `view_image`, `screenshot_url`).

It shrinks from "the orchestrator's container" to "the agent process." It no longer knows about
Docker, Azure, suspend/resume, or archives — those are the host's job now.

### Runtime 3 — Browser (React)

Copied from `frontend/src/`'s agent components. The crown jewel is the **single `agentEventReducer`**
(CLAUDE.md rule 12): one pure function reconstructs the UI from events, identically for live streaming
and restored/replayed sessions. The library preserves that invariant absolutely. Carbon-specific
table/chart widgets become **render plugins** keyed by event type.

## The dependency direction

```
host app ──depends on──▶ agentkit/go ──defines──▶ interfaces ◀──implements── engine adapters
                                    └──defines──▶ interfaces ◀──implements── host (Store, OrgContext…)
```

- `agentkit/go` defines interfaces and the generic orchestration that consumes them.
- **Engine adapters** (Docker/DinD/K8s, local/registry) implement `ExecutionEnvironment` /
  `ImageRegistry`. These ship *with* the library (in `go/engine/...`) because they're generic.
- **Host adapters** (persistence, org context, token logging, auth claims) implement the
  host-extension interfaces. These live in the *host app*, not the library — they're where Platinum
  injects its specifics.

This is the same "controller glues implementations" model as the interface-refactor
([../docs/interface-refactor/00-overview.md](../../docs/interface-refactor/00-overview.md)), applied
one level out: the `Runner` is constructed with one implementation of each interface; tests pass
mocks; production passes real engine + real host adapters.

## Control flow: a message turn, end to end

1. **Host handler** authenticates the user, then calls `runner.SendMessage(ctx, ref, msg)`.
2. **Runner** ensures the session's instance is running (resume via `ExecutionEnvironment` if
   suspended; restore from a snapshot via `ImageRegistry`+`ExecutionEnvironment` if destroyed).
3. Runner asks the host extensions for enrichment (`OrgContextProvider.Context(...)`), mints a
   scoped token (`ScopedClaimsIssuer`), and POSTs the turn to the in-image agent's `/query-stream`
   over the address the `ExecutionEnvironment` reported.
4. The **in-image agent** drives the SDK and emits SSE events.
5. The **EventPipeline** tees the stream: bytes relay straight to the browser; in parallel it
   compacts + extracts search text and, on a cadence + at the end, persists via `SessionStore`
   (guarded by the flush counter so the session can't be archived mid-flush).
6. Marker events (`artifact_registered`, `table_rendered`, …) trigger host hooks: artifact bytes are
   pulled from the workspace and saved via `ArtifactStore`; token usage is reported via
   `TokenUsageLogger`.
7. When the turn ends, the Runner returns; the browser's reducer has the full conversation.

## Control flow: lifecycle in the background

The orchestration core runs two control loops (ported from `sandbox-manager.ts`):

- **Idle reaper** — suspends instances idle longer than `SuspendTimeout` (skipping any with pending
  flushes), via `ExecutionEnvironment.Suspend`.
- **Archive loop** — for instances cold longer than `ArchiveTimeout`, runs the snapshot+persist+destroy
  sequence and updates the session's snapshot state via `SessionStore`. Also skips pending-flush
  instances.

On startup, **recovery** (`ExecutionEnvironment.Recover`) re-adopts orphaned instances so a host
restart doesn't strand running sessions.

## Why this boundary and not another

We considered three places to draw the engine boundary:

1. **At "exec a shell command"** (lowest). Too low: the agent needs an HTTP server with a replay
   buffer running inside, not one-shot commands. Modelling streaming over repeated exec is painful.
2. **At "run an agent session inside an image" (chosen).** The engine provisions an instance, returns
   an address, and the host talks the sandbox HTTP contract to it. Suspend/resume/snapshot/destroy
   are the other verbs. Small, sufficient, and each verb maps cleanly onto Docker, DinD, and K8s.
3. **At "the whole orchestrator"** (highest — today's TS process). Too high: it bakes Docker, Azure,
   and lifecycle policy into the boundary, so you can't reuse the policy without the plumbing.

Option 2 keeps *policy* (when to suspend, how to capture events, how to retry an archive) generic and
in Go, while *mechanism* (how to start a container vs a pod) stays in the adapter. See
[02-execution-environment.md](02-execution-environment.md) for the exact method set and the mapping
table.

## File/package layout (target)

```
agent-library/
  go/
    go.mod                         # module github.com/binocarlos/badcode-agent-orange  (own module)
    agentkit.go                    # package doc + version
    session.go                     # Session, SessionState, SessionSpec, SessionStore iface
    runner.go                      # Runner facade interface + the orchestration impl
    execenv/
      execenv.go                   # ExecutionEnvironment interface + shared types
      docker/                      # single-container + DinD adapters (dockerode → Go SDK)
      kubernetes/                  # K8s adapter (sketch)
      mock.go                      # in-memory MockExecutionEnvironment + Recorder
    imageregistry/
      registry.go                  # ImageRegistry interface + BuildSpec/ImageRef
      localbuild/                  # docker build/save/load adapter
      blobarchive/                 # diff-archive-to-blob adapter (today's suspend/restore)
      remote/                      # registry push/pull adapter (sketch)
      mock.go
    events/
      events.go                    # Event types, SSE envelope, EventStreamer iface
      compact.go                   # compactEvents + extractSearchText (ported from TS)
      pipeline.go                  # EventPipeline (consume → compact → persist → relay)
      mock.go
    artifacts/
      artifacts.go                 # ArtifactStore iface + AgentArtifact type + status rules
      mock.go
    extension/
      extension.go                 # OrgContextProvider, TokenUsageLogger, ArtifactEnricher,
                                    #   ScopedClaimsIssuer, SessionStore (host-implemented seams)
    internal/recorder/recorder.go  # shared {Method,Args} recorder for mocks (from mockutil)
  sandbox/                         # in-image TS agent (see 07)
  web/                             # React rendering package (see 09)
  docs/                            # this directory
```

The Go module is **self-contained**: its own `go.mod`, no import path into `goapi/...`. Wire types it
needs (the artifact shape, the event shape) are *redefined* in the library, not imported from
`goapi/pkg/types` — that's what makes it liftable. See [90-provenance-map.md](90-provenance-map.md)
for the type-by-type origin.
