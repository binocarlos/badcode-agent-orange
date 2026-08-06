# 01 — Architecture

Agent Orange is a reusable runtime for container-backed AI agent sessions. This document is the
map: the durable thesis behind the design, the layered picture, the package layout as it actually
ships, and how a message turn and a session's lifecycle flow through it. Read it first, then the
per-subsystem docs listed at the end.

## Two layers — read this before anything else

Agent Orange is **two stacked systems**, and this document describes only the lower one.

| | **Engine** (this doc set, `docs/01`–`docs/15`) | **Product layer** (`docs/product/`, `docs/18`) |
|---|---|---|
| Unit | a **session**: one container, one conversation | a **project**: workers, memory, events, schedules |
| Entry point | `Runner` (`go/agentkit.go`) — a library a host embeds | `agentd` (`go/cmd/agentd/`) — the service that uses it |
| Code | `go/` (minus `cmd/agentd`), `sandbox/`, `web/` chat | `go/agentdb/`, `go/cmd/agentd/`, `go/compose.go`, `web/` product pages |
| Storage | whatever the host's `RunnerStore` is (sqlite works) | **Postgres only** — the product layer is wired only when `DATABASE_URL` is set |
| Knows about | containers, images, SSE, snapshots, artifacts | workers, `project_events`, subscriptions, cron, memories, skills, the config log |

The engine has **no knowledge of the product layer**. It does not know what a worker is beyond
two opaque strings on the session row (`worker`, `composed_prompt`), it does not read
`project_events`, and it never starts a job. Everything that wakes a worker, matches a
subscription, fires a schedule or serves a memory tool lives in `go/cmd/agentd/` and is documented
in [`docs/product/`](product/00-overview.md) — start at
[`product/17-product-spec.md`](product/17-product-spec.md) (the authoritative spec) or
[`docs/18-workers-memory-events.md`](18-workers-memory-events.md) (the operator's view).

The two layers meet at exactly three seams, all of them in `go/`:

- **`ComposeJob`** (`go/compose.go`) — a pure function that turns a worker row, project settings and
  a triggering event into a `ComposedJob{Image, SystemPrompt, MCPServers, FirstMessage}`, which the
  caller feeds to `CreateSessionRequest`. It reads no store and writes no row. It lives in the
  engine module but is product vocabulary; `agentd` calls it, the `Runner` never does.
- **Two fields on the session row** — `worker` (which persona this session is) and
  `composed_prompt` (the exact system prompt it was composed with). When `composed_prompt` is set
  the `Runner` runs every turn with it, re-read off the row, instead of asking the host's
  `SessionContextProvider`. That is deliberate: a restore or an `agentd` restart cannot change a
  running job's prompt mid-life.
- **`Deps.WorkerEvents`** — an optional store the `Runner` appends `worker.finished` /
  `worker.failed` to when a session carries a worker. Leaving it nil is legal and correct for a
  host that embeds the engine without the product layer.

> ### Terminology trap: two different "workers"
>
> The word is overloaded and the two meanings were confused during the build.
>
> - **Fleet worker** — a *host that runs containers*. `fleet.Worker`, `Session.WorkerID`,
>   `GetWorkerBinding`. Infrastructure. See [13-fleet-placement.md](13-fleet-placement.md).
> - **Product worker** — a *persona*: a row of prompt + tools + wiring. `agentdb.Worker`,
>   `Session.Worker`, the `workers` table. See [`product/02-workers.md`](product/02-workers.md).
>
> They are unrelated. `Session.WorkerID` and `Session.Worker` are adjacent columns on the same row
> and mean completely different things; the comment in `go/agentdb/types.go` says so explicitly.

## The thesis

Three ideas hold the design together. Everything else follows from them.

**Orchestration is host-side Go, not a separate process.** The work of running a session — provision
a container, keep it reachable, capture and persist its events, snapshot it when it goes cold,
restore it on the next message — is generic host-side logic. It lives in the host application as an
embeddable Go library (the `Runner`), not in a standalone orchestrator service. The only code that
*must* run elsewhere is the part that touches a container engine (create a container, exec a command,
commit a layer), because that differs between Docker, Docker-in-Docker, and Kubernetes. So the
boundary is drawn there and kept small:

> **`ExecutionEnvironment` is "a mechanism that runs an agent session inside a container image."**
> Everything above it is generic Go orchestration. Everything below it is engine-specific plumbing.

**Two orthogonal interfaces compose.** The runtime rests on two contracts that are independent by
design. `ExecutionEnvironment` runs sessions inside images (provision / exec / snapshot / destroy /
status / recover). `ImageRegistry` moves images in and out (ensure-present / build / persist /
materialize). They compose: snapshotting a session is `ExecutionEnvironment.Snapshot()` (commit the
running container into an image ref) **then** `ImageRegistry.Persist()` (save that ref somewhere
durable). Restoring is `ImageRegistry.Materialize()` **then** `ExecutionEnvironment.Provision(fromImage:…)`.
Because they are orthogonal, you can swap the engine (laptop Docker → DinD → K8s) without touching
registry policy, and vice-versa.

**A snapshot is not an artifact.** Two different things produce persisted bytes from a session, and
the runtime keeps them apart. A **snapshot** is the *whole filesystem* of a session captured as an
image, so the session can be resurrected later (or published as a reusable app template) — an
`ExecutionEnvironment` + `ImageRegistry` concern. An **artifact** is an *individual user-facing file*
the agent deliberately produced (a report, a chart JSON, a generated web app), registered for
download — an `ArtifactStore` concern. Conflating them is a category error: a snapshot says "resume
this session"; an artifact says "here is the deliverable." Keeping the interfaces separate is what
lets a session be published as an app (snapshot) while its charts are pinned to a dashboard
(artifacts).

## Three runtimes, one contract

The **engine** spans three runtimes with deliberate, minimal boundaries between them: the Go host
process, the in-image agent (TypeScript), and the browser (React). (The product layer adds no
fourth runtime — it is more Go in the host process, plus more React pages in the browser.)

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  HOST PROCESS  (Go — your API server; embeds the agentkit module)              │
│                                                                                │
│   ┌────────────────────────────────────────────────────────────────────────┐ │
│   │  agentkit Runner  (the public facade — the only thing the host calls)    │ │
│   │   CreateSession · SendMessage · Stream · Stop · Resume · Snapshot        │ │
│   │   Destroy · Status · WriteWorkspaceFile · RunningSessions · Start · Close │ │
│   └───────────────┬──────────────────────────────┬───────────────────────────┘ │
│                   │                              │                              │
│   ┌───────────────▼───────────────┐   ┌──────────▼─────────────┐                │
│   │  Orchestration core (Go)       │   │  EventPipeline (Go)     │               │
│   │  • ensure-running / restore    │   │  • consume sandbox SSE  │               │
│   │  • archive loop (snapshot+drop)│   │  • compact + searchtext │               │
│   │  • flush guard, recovery       │   │  • persist via Store    │               │
│   │  • fleet placement             │   │  • relay to client      │               │
│   └───┬───────────┬───────┬────────┘   └─────────┬──────────────┘                │
│       │           │       │                      │                               │
│   ┌───▼──────┐ ┌──▼─────┐ ┌▼──────────┐   ┌──────▼────────┐  ┌────────────────┐  │
│   │ Fleet →  │ │ Image  │ │ Store /   │   │ Host          │  │ Host extensions│  │
│   │ Execution│ │Registry│ │ Artifact  │   │ extensions    │  │ • SessionCtx   │  │
│   │Environmnt│ │        │ │ Store     │   │               │  │ • Claims       │  │
│   └───┬──────┘ └──┬─────┘ └───────────┘   └───────────────┘  │ • TokenLogger  │  │
│       │           │                                          │ • Enricher     │  │
└───────┼───────────┼──────────────────────────────────────────────────────────────┘
        │ (engine)   │ (ensure/build · persist/materialize)
        ▼           ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  CONTAINER ENGINE   Docker socket │ Docker-in-Docker daemon │ Kubernetes API     │
│                                                                                │
│   ┌──────────────────────────────────────────────────────────────────────┐   │
│   │  IMAGE  (agent image — installed deps, CLAUDE.md, .claude/skills)       │   │
│   │  ┌────────────────────────────────────────────────────────────────┐   │   │
│   │  │  IN-IMAGE AGENT  (TypeScript — sandbox/)                         │   │   │
│   │  │    HTTP control server: /query-stream /stream/:id /cancel …      │   │   │
│   │  │    harness (Claude Agent SDK) → SSE event stream                 │   │   │
│   │  │    replay buffer · MCP tool server                              │   │   │
│   │  └────────────────────────────────────────────────────────────────┘   │   │
│   └──────────────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────────────┘

        ▲  SSE event stream (text/event-stream)
        │
┌───────┴───────────────────────────────────────────────────────────────────────┐
│  BROWSER  (React — web/)                                                       │
│    useAgentSession → readSSEStream → agentEventReducer (THE single codepath)    │
│    AgentChat · ToolCallGroup · AskUserCard · ArtifactPanel                      │
└────────────────────────────────────────────────────────────────────────────────┘
```

### Runtime 1 — Host process (Go)

The host embeds the `agentkit` Go module and calls the `Runner` from its own HTTP handlers. The
`Runner` owns the full session lifecycle by coordinating the interfaces below it:

- **`Runner`** (`go/agentkit.go`, `go/runner.go`) — the public facade. Its methods are
  `CreateSession`, `SendMessage`, `Stream`, `Stop`, `Resume`, `Destroy`, `Snapshot`,
  `WriteWorkspaceFile`, `Status`, `RunningSessions`, `Start`, `Close`. There is no `Suspend`: the
  lifecycle is running-or-archived, not warm-suspended (see below).
- **Orchestration core** — ensure-running / restore-on-demand, the archive loop, the flush guard,
  orphan recovery, and fleet placement.
- **`EventPipeline`** (`go/events/`) — consumes the in-image SSE stream, compacts events + extracts
  search text, persists via the store, and relays bytes to the browser.
- **Interfaces it depends on**: `ExecutionEnvironment` (via `Fleet`), `ImageRegistry`, the store
  seam (`RunnerStore`), `ArtifactStore`, and the host extensions (`ScopedClaimsIssuer`,
  `SessionContextProvider`, `ArtifactEnricher`, `Metrics`, `BlobStoreFactory`).
  `extension.TokenUsageLogger` is declared and accepted as `Deps.TokenLogger`, but **nothing in the
  module ever calls it** — it is an unwired seam, not a working costing hook.

### Runtime 2 — In-image agent (TypeScript, `sandbox/`)

The only code that must run inside the container. It is a small HTTP control server exposing the
sandbox contract (`POST /query-stream`, `GET /stream/:queryId`, `POST /cancel`,
`POST /load-conversation`, `GET /health`, workspace-file endpoints), driving a per-session **harness**
(the Claude Agent SDK by default; the harness is a per-session choice — see
[07-in-image-agent.md](07-in-image-agent.md)). It emits the canonical SSE event stream and buffers it
for late/reconnecting consumers. It knows nothing about Docker, blob storage, snapshots, or the
lifecycle — those are the host's job.

### Runtime 3 — Browser (React, `web/`)

A single `agentEventReducer` reconstructs the chat UI from events, identically for live streaming and
for restored/replayed sessions. The runtime preserves that single-codepath invariant absolutely; live
and replay must never diverge.

`web/` also ships the product layer's pages (workers, project settings, events/jobs, subscription and
schedule editors, the changelog). Those are ordinary JSON-over-HTTP screens — they are not driven by
the SSE reducer and have nothing to do with the invariant above. `web/` is a component library with
no router; the app shell that composes it is `examples/web/`.

## The dependency direction

```
host app ──depends on──▶ agentkit/go ──defines──▶ interfaces ◀──implements── engine adapters
                                     └──defines──▶ interfaces ◀──implements── host (Store, Claims…)
```

`agentkit/go` defines the interfaces and the generic orchestration that consumes them. **Engine
adapters** (Docker/DinD, OCI registry, blob archive) ship *with* the module because they are
generic. **Host adapters** (persistence, auth claims, session context, token logging) are supplied
by the host — that is where a host injects its specifics. The `Runner` is constructed with one
implementation of each; production passes real engine + host adapters, tests pass in-memory mocks.

## Package layout (`go/`)

```
go/
  agentkit.go              # Runner interface, Deps, Policy, request/handle types, NewRunner
  runner.go                # runnerImpl: lifecycle orchestration
  compose.go               # ComposeJob — product-layer job composition (called by agentd)
  snapshot_reaper.go       # snapshot-TTL sweep of the image catalogue
  progress.go              # per-session progress ops surfaced through Status
  skillcatalog.go          # SkillCatalog seam (hoisted/installed skills)
  agentkittest/            # in-memory MemStore + test helpers
  execenv/                 # ExecutionEnvironment interface + shared types
    docker/                #   socket + Docker-in-Docker adapters (both per-session)
    mock.go                #   in-memory MockExecutionEnvironment
  imageregistry/           # ImageRegistry interface, BuildSpec, Handle, mock
    ociregistry/           #   registry push/pull (pluggable auth)
    blobarchive/           #   snapshot-to-blob diff-archive adapter
    auth/                  #   registry auth: Static (basic) + GCP (ADC tokens)
  events/                  # Event vocabulary, SSE envelope, Sink, EventPipeline, compaction
  artifacts/               # ArtifactStore interface + Artifact type + tar helpers + mock
  fleet/                   # Fleet + Worker placement (pool of ExecutionEnvironments)
  agentdb/                 # engine tables (sessions/events/artifacts/skills/images) AND the
                           #   whole product layer (workers, memories, project_events,
                           #   subscriptions, schedules, config_events) — Postgres
  extension/               # host-implemented seams
    blobartifacts/ devclaims/ embedding/ filesblob/ gcsblob/ sqlitestore/
  httpapi/                 # optional net/http handlers a host can mount (Handlers)
  imagetree/               # derived-image tree model behind the imagetree CLI
  mockmodel/ modelproxy/   # deterministic offline model + the host-side model proxy
  titlebot/                # utility LLM helper (session titling)
  systemtest/ internal/    # Docker-dependent system tests; internal helpers
  cmd/                     # agentd (standalone API + product layer), imagetree
  examples/                # standalone, mockproxy, exampleimage
```

The module is self-contained: its path is `github.com/binocarlos/badcode-agent-orange` and it imports
nothing from any host app (CI enforces this). The `httpapi` package *does* ship mountable HTTP
handlers, but they are optional — the library is embedded, not run as a service of its own.

Two directories are the product layer rather than the engine, and are documented in
[`docs/product/`](product/00-overview.md), not here: the product tables and stores in `agentdb/`
(workers, memories, `project_events`, subscriptions, schedules, `config_events`), and the router,
scheduler, dispatch gate and core MCP tool server in `cmd/agentd/`.

## Control flow: a message turn, end to end

1. The **host handler** authenticates the user, then calls `runner.SendMessage(ctx, ref, msg, w)`.
2. The **Runner** ensures the session's instance is running: it resolves the session's worker via the
   `Fleet` (`WorkerForSession`, or `PlaceForSession` on the first message), and — if the container is
   gone — restores from the snapshot (`ImageRegistry.Materialize` → `ExecutionEnvironment.Provision`)
   and rehydrates the conversation.
3. The Runner resolves the turn's system prompt: if the session row carries a `composed_prompt`
   (a worker job — see "Two layers" above) that string is used verbatim, re-read off the row every
   turn; otherwise it asks the host's `SessionContextProvider`. It mints a per-session token via
   `ScopedClaimsIssuer` and POSTs the turn to the in-image agent's `/query-stream` at the address
   the `ExecutionEnvironment` reported.
4. The **in-image agent** drives the harness and emits SSE events.
5. The **EventPipeline** tees the stream: bytes relay straight to the client writer `w`; in parallel
   it compacts events, extracts search text, and persists via the store — each persist bracketed by
   the flush guard (`Sink.BeginFlush` / `EndFlush`) so the session cannot be archived mid-flush.
6. Marker events fire hooks the Runner registers on the pipeline: `artifact_registered` pulls the
   bytes from the workspace and saves them via `ArtifactStore` (optionally rewritten by
   `ArtifactEnricher`); `skill_hoisted` / `skill_installed` update the skill catalogue; `error`
   stashes the failure text so a worker job can report `worker.failed`.
7. When the turn ends, `SendMessage` returns; the browser's reducer holds the full conversation. If
   the session is a worker job, the Runner appends `worker.finished` (or `worker.failed`) to
   `Deps.WorkerEvents` — which is how the product layer's next worker gets woken.

## Control flow: lifecycle in the background

The lifecycle is **running-or-archived** — there is no warm suspended state. The Runner starts two
background loops, each only when configured:

- **Archive loop** (`runner.go`, started by `Start` when `Policy.ArchiveTimeout > 0`) — every 60s it
  finds sessions idle past `ArchiveTimeout`, skips any with pending flushes and any whose engine
  reports `SupportsSnapshot = false`, then runs snapshot → persist → destroy. The session's snapshot
  handle is recorded via the store so it can be restored later.
- **Snapshot-TTL reaper** (`snapshot_reaper.go`, started when `Policy.SnapshotReapInterval > 0`
  *and* `Deps.Snapshots` is set) — the other half of the same lifecycle: it retires catalogued
  image versions whose expiry has passed. The TTL itself is a product-layer setting
  (`project_settings.snapshot_ttl_days`); this loop is only how often the engine looks.

Neither loop is on by default *in the library* — a host that wants them sets the knobs. **`cmd/agentd`
sets both** (`cmd/agentd/gc.go`): `AGENTKIT_SESSION_IDLE_TIMEOUT` (30m) drives the archive loop and
`AGENTKIT_SNAPSHOT_REAP_INTERVAL` (6h) drives the reaper, each disabled by the literal `off`. So in
the standalone stack a session idle for half an hour is snapshotted and its container (and host
port) released, and expired snapshots are swept four times a day. The reaper additionally needs
`Deps.Snapshots`, which agentd wires only on Postgres — on the sqlite fallback there is no image
catalogue to sweep.

**Restore is lazy.** A destroyed session is brought back on its *next message*, inside
`ensureRunning`: materialize the snapshot, provision a fresh instance (possibly on a different
worker), and rehydrate the conversation by replaying persisted query-events into the in-image agent
before the first new turn.

**Recovery on startup.** `Start` calls `ExecutionEnvironment.Recover()` across every worker in the
fleet and re-adopts surviving instances, so a host restart never strands a running session. Managed
containers found stopped are reclaimed rather than re-adopted (the session will restore from its
snapshot on next use).

## Tenancy and placement

The Runner branches on exactly one capability axis — `Tenancy`:

- **`TenancyPerSession`** — one container/pod per session. Provision, snapshot, and destroy operate
  on it 1:1.
- **`TenancyShared`** — one container hosts many sessions; the sandbox routes by session ID. Snapshot
  is gated off for shared instances, so the archive loop skips them.

The Runner implements both branches, but **no shipped adapter declares `TenancyShared`**: the two
Docker adapters (`execenv/docker` — socket and DinD) both report `TenancyPerSession`. The shared
branch is exercised only by tests and by the mock. Treat it as a supported shape of the interface,
not as something you can select today.

Placement is delegated to the `Fleet` (`go/fleet/`), a pool of `ExecutionEnvironment` workers —
*fleet* workers, hosts that run containers, not the product layer's personas. A single-worker
deployment needs no fleet: passing `Deps.Env` alone wraps it as a one-worker fleet via a shim. The
trust gate lives in `fleet.Register` — a shared-tenancy environment below the required isolation
tier (without `TrustedWorkload`) is rejected there, and `NewRunner` surfaces it as a construction
error because the shim registers.

## Why this boundary and not another

Three places to draw the engine boundary were considered:

1. **At "exec a shell command"** — too low. The agent needs a long-lived HTTP server with a replay
   buffer running inside; modelling streaming over repeated one-shot execs is painful.
2. **At "run an agent session inside an image"** — chosen. The engine provisions an instance, returns
   an address, and the host speaks the sandbox HTTP contract to it. Snapshot / destroy / status /
   recover are the other verbs. Small, sufficient, and each verb maps cleanly onto Docker, DinD, and
   K8s.
3. **At "the whole orchestrator"** — too high. It bakes engine, storage, and lifecycle policy into
   the boundary, so you cannot reuse the policy without the plumbing.

Option 2 keeps *policy* (when to archive, how to capture events, how to restore) generic and in Go,
while *mechanism* (how to start a container vs a pod) stays in the adapter. See
[02-execution-environment.md](02-execution-environment.md) for the exact method set and the mapping
table.

## How to read the rest

### The engine (these docs)

- **[02-execution-environment.md](02-execution-environment.md)** — the `ExecutionEnvironment`
  contract and the Docker adapters.
- **[03-image-registry.md](03-image-registry.md)** — `ImageRegistry`: ensure-present / build /
  persist / materialize, and the OCI + blob-archive adapters.
- **[05-event-streaming.md](05-event-streaming.md)** — the canonical **session** SSE vocabulary and
  the compaction pipeline. (Not the product event spine — that is `product/04`.)
- **[06-artifacts.md](06-artifacts.md)** — the `ArtifactStore` and the snapshot-vs-artifact split.
- **[07-in-image-agent.md](07-in-image-agent.md)** — the sandbox control server and the harness seam.
- **[13-fleet-placement.md](13-fleet-placement.md)** — the `Fleet`, *fleet* workers, and placement.
- **[14-host-adapters.md](14-host-adapters.md)** — the host-implemented seams (store, claims, context)
  a host application author must supply.
- **[15-standalone-stack.md](15-standalone-stack.md)** — running the demo stack end to end.

### The product layer (a different system — do not read these five docs as the whole picture)

- **[18-workers-memory-events.md](18-workers-memory-events.md)** — the operator's guide: project
  settings, workers, triggers, memory, the core tools, images/skills, the config log.
- **[`product/00-overview.md`](product/00-overview.md)** — the quick map of the spec folder.
- **[`product/17-product-spec.md`](product/17-product-spec.md)** — the authoritative spec: goal,
  atoms, binding principles, non-goals. Component designs are `product/01`–`product/09`; the
  Discovered Issues Log in `product/06-work-plan.md` is the best record of what was actually built.

---

**Provenance.** This design was ported from an in-house TypeScript orchestrator ("Platinum"), which
bundled generic orchestration logic together with Docker and blob-storage plumbing. The port pulled
the generic logic up into Go and left a small contract to the engine. The original TypeScript source
is kept for reference in `migration-reference/` — do not build or import it.
