# agentkit — a reusable agent-runtime library

> **Status: design complete (redesigned) · code ~45–50%.** This is a self-contained distillation of
> the best of Platinum's agent stack, reorganised around a **Go-native orchestration core** so it can
> be lifted into its own repository and reused to build *other* agentic products with different
> toolsets, frameworks, and container backends. Nothing in the Platinum repo imports this folder yet;
> the live system is untouched. This README is the **canonical overview**; the per-topic deep dives
> live in [`docs/`](docs/).

---

## Executive summary

agentkit runs **AI agents inside container images** and renders their output. It cleanly separates the
**generic machinery** of an agentic environment — *run an agent session inside an image, stream its
events, snapshot its filesystem, render the conversation* — from the **application-specific** parts
(which tools the agent has, which framework drives it, which image it runs, how org context is
assembled, where artifacts are published).

The central architectural move: **orchestration logic belongs in the host process, written once in
Go.** Today a separate TypeScript orchestrator owns Docker, lifecycle, archiving, and SSE relay, with
the Go API as a thin proxy in front of it. agentkit *inverts* that — the generic logic moves up into
Go, and the only things that vary sit behind small interfaces:

- **`ExecutionEnvironment`** — *"run an agent session inside an image"* (Docker / DinD / Kubernetes / managed).
- **`ImageRegistry`** — *"get images in, get snapshots out"* (local tar / blob diff-archive / remote registry).

With those two interfaces (plus a handful of host hooks), you build a new agent product by supplying an
**image**, a **tool set**, and a few **host adapters** — and get container orchestration, snapshotting,
event streaming, persistence, and a polished React chat UI for free.

### Top-line features

| Capability | What it gives you |
|---|---|
| **Go-native orchestration core** | Session lifecycle, idle-reaper, archive loop, recovery, flush guards — all in the host process. No separate orchestrator service. |
| **Two composable engine interfaces** | `ExecutionEnvironment` + `ImageRegistry`. Swap one implementation to move from laptop Docker → DinD → Kubernetes; the core never changes. |
| **Pluggable harness (per session)** | The agentic framework — Claude Agent SDK / Claude CLI / Gemini / Codex — is chosen *per session at runtime*. All harness binaries baked into one base image; no engine×harness matrix. |
| **Always-multi-session sandbox** | The in-image agent hosts N sessions keyed by ID. The *execution environment* decides whether to route many sessions to one sandbox or one-per-session; the sandbox never limits. |
| **Capability axis + trust gate** | `Backend` / `Tenancy` / `IsolationTier` axes, with a construction-time gate making "multi-tenant + untrusted on plain Docker" *unconstructable*. |
| **Fleet / placement layer** | Horizontal scaling across a pool of workers with sticky, durable session→worker binding — the core stays stateless across host replicas. |
| **Unified image model** | Three image kinds (core→app build, session-snapshot, curated user image) on **one snapshot primitive**, content-hash cached. |
| **One event vocabulary, one reducer** | A single canonical SSE event set and a single pure reducer that renders live *and* replayed sessions identically (CLAUDE.md rule 12). |
| **Artifacts ≠ snapshots** | Strict separation: artifacts are user-facing deliverables (`ArtifactStore`); snapshots are whole-filesystem images for resume/publish. |
| **Hermetic testability** | The whole runtime boots against in-memory mocks (no Docker, no registry, no network) and asserts on a recorded interaction log — millisecond integration tests. |

---

## Architecture: three runtimes, one contract

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  HOST PROCESS  (Go — your API server; in Platinum, goapi)                      │
│                                                                                │
│   ┌────────────────────────────────────────────────────────────────────────┐ │
│   │  agentkit Runner  (the public facade — the only thing the host calls)    │ │
│   │   CreateSession · SendMessage · Stream · Stop · Suspend/Resume · Destroy │ │
│   │   Snapshot · Status                                                      │ │
│   └───────────────┬──────────────────────────────┬───────────────────────────┘ │
│                   │                              │                              │
│   ┌───────────────▼───────────────┐   ┌──────────▼─────────────┐                │
│   │  Orchestration core (Go)       │   │  EventPipeline (Go)     │               │
│   │  • lifecycle state machine     │   │  • consume sandbox SSE  │               │
│   │  • idle reaper / archive loop  │   │  • compact + searchtext │               │
│   │  • flush guards, recovery      │   │  • persist via Store    │               │
│   │  • snapshot/restore coord.     │   │  • relay to client      │               │
│   └───┬───────────┬───────────────┘   └─────────┬──────────────┘                │
│       │           │                             │                               │
│   ┌───▼───────────▼───┐  ┌──────────────┐  ┌────▼────────────┐  ┌────────────┐  │
│   │ Fleet (placement) │  │ Image        │  │ SessionStore    │  │ Host       │  │
│   │  └─ Worker = an    │  │ Registry     │  │ ArtifactStore   │  │ extensions │  │
│   │     ExecutionEnv   │  │ (interface)  │  │ (interfaces)    │  │ • OrgCtx   │  │
│   │     (interface)    │  │              │  │                 │  │ • Claims   │  │
│   └───┬───────────────┘  └─────┬────────┘  └─────────────────┘  │ • TokenLog │  │
│       │ (engine-specific)      │ (build / save·load / push·pull) │ • Enricher │  │
└───────┼───────────────────────┼─────────────────────────────────┴────────────┘─┘
        ▼                       ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  CONTAINER ENGINE   Docker socket │ Docker-in-Docker │ Kubernetes │ managed     │
│                                                                                │
│   ┌──────────────────────────────────────────────────────────────────────┐   │
│   │  IMAGE  (agent image — deps, CLAUDE.md, .claude/skills, harness bins)   │   │
│   │  ┌────────────────────────────────────────────────────────────────┐   │   │
│   │  │  IN-IMAGE AGENT  (TypeScript — @agentkit/sandbox)               │   │   │
│   │  │   Control server (harness-agnostic, MULTI-SESSION):              │   │   │
│   │  │     POST /sessions · /sessions/:id/query-stream · /stream · ...   │   │   │
│   │  │     SessionManager · StreamService (buffer+replay) · ToolRegistry │   │   │
│   │  │   Harness adapters (per session):                                │   │   │
│   │  │     ClaudeAgentSdkHarness │ ClaudeCliHarness │ Gemini │ Codex …  │   │   │
│   │  └────────────────────────────────────────────────────────────────┘   │   │
│   └──────────────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────────────┘

        ▲  SSE event stream (text/event-stream)
        │
┌───────┴───────────────────────────────────────────────────────────────────────┐
│  BROWSER  (React — @agentkit/chat-ui)                                          │
│    useAgentSession → readSSEStream → agentEventReducer  (THE single codepath)   │
│    AgentChat · ToolCallGroup · AskUserCard · ArtifactPanel · render plugins     │
└────────────────────────────────────────────────────────────────────────────────┘
```

### Dependency direction

```
host app ──depends on──▶ agentkit/go ──defines──▶ interfaces ◀──implements── engine adapters (ship w/ lib)
                                     └──defines──▶ interfaces ◀──implements── host adapters (live in host app)
```

`agentkit/go` defines interfaces and the generic orchestration that consumes them. **Engine adapters**
(Docker/DinD/K8s, local/blob/remote) ship *with* the library because they're generic. **Host adapters**
(persistence, org context, token logging, auth) live in the *host app* — that's where a product injects
its specifics. Tests pass mocks; production passes real engine + real host adapters.

### A message turn, end to end

1. The **host handler** authenticates the user and calls `runner.SendMessage(ctx, ref, msg, w)`.
2. The **Runner** resolves the session's worker (via the `Fleet`) and ensures its instance is running —
   *resume* if suspended, *restore from snapshot* (`ImageRegistry.Materialize` → `Provision`) if destroyed.
3. It enriches via host extensions (`OrgContextProvider`), mints a scoped token (`ScopedClaimsIssuer`),
   and POSTs the turn to the in-image agent's `/sessions/:id/query-stream`.
4. The **in-image agent** boots the session's harness, drives the model, and emits an SSE stream.
5. The **EventPipeline** tees that stream: raw bytes relay straight to the browser; in parallel it
   compacts + extracts search text and persists via `SessionStore` — guarded by the flush counter so
   the session can't be archived mid-flush.
6. Marker events (`artifact_registered`, `table_rendered`, …) fire host hooks: artifact bytes are
   pulled and saved via `ArtifactStore`; token usage reported via `TokenUsageLogger`.
7. The browser's single reducer has reconstructed the full conversation — identically to how it would
   replay it later from storage.

---

## The parts of the system

### 1 — `ExecutionEnvironment`: run an agent session inside an image  ([docs/02](docs/02-execution-environment.md))

**The** core interface. *"Given an image and a session spec, make a running instance of the in-image
agent reachable over HTTP; let me exec into it, suspend/resume it, snapshot it, and destroy it."* How —
a fresh container, a pod, or an exec into a shared container — is the implementation's concern.

```
Provision · Suspend · Resume · Exec · Snapshot · Destroy · Status · Recover · OnDestroy · Capabilities
```

Each verb maps cleanly onto single-container, Docker-in-Docker, and Kubernetes — the orchestration core
above it *never branches on engine type*. The DinD column is the closest to today's behaviour (most of
`orchestrator/src/sandbox-manager.ts` *is* the DinD adapter).

**The capability axis + trust gate.** `Capabilities` expresses **three orthogonal axes**, not a single
boolean:

- **`Backend`** — `docker-socket` / `docker-dind` / `k8s` / `managed`. Descriptive only; the core never branches on it.
- **`Tenancy`** — `per-session` vs `shared`. The *only* axis the Runner branches on (reuse-a-sandbox vs provision-per-session).
- **`IsolationTier`** — `process` / `container` / `vm` (gVisor/Kata/Firecracker). The trust boundary.

The "four execution environments" are these axes crossed, **not** four implementations. The **trust
gate**, validated at construction:

> `Tenancy == TenancyShared` requires `TrustedWorkload == true` **OR** `IsolationTier >= TierVM`.

This makes "multi-tenant + untrusted on plain Docker" *unconstructable* — fail-fast at startup, not a
runtime branch. And **shared tenancy ⇒ no snapshot** (a file diff is not attributable to a single
session when sessions are multiplexed), so a `TenancyShared` environment reports `SupportsSnapshot=false`.

### 2 — `ImageRegistry`: get images in, get snapshots out  ([docs/03](docs/03-image-registry.md))

Orthogonal to and composing with `ExecutionEnvironment`. *"Make images present for an engine to run,
build images from a context, and move images in and out as durable artifacts."*

```
EnsurePresent · Build · Resolve · Persist · Materialize · Remove · Capabilities
```

Snapshotting a session = `ExecutionEnvironment.Snapshot()` (commit the running container → image) **then**
`ImageRegistry.Persist()` (save it durably). Restoring = `Materialize()` **then** `Provision(fromImage:…)`.
The famous **diff-archive** optimisation (`docker diff` + `getArchive` → tar → gzip → blob, KB–MB
instead of GBs) is *entirely contained* in the `blobarchive` adapter — the core only knows
`Persist`/`Materialize`.

Three shipped adapters: **`localbuild`** (dev/DinD — `docker save`/`load`), **`blobarchive`** (today's
Platinum prod — diff archive to an injected `BlobStore`), **`remote`** (K8s — registry push/pull, sketch).

**The unified image model — three image kinds on one snapshot primitive:**

| Kind | What | How built |
|---|---|---|
| **Core → App** | agentkit base (in-image agent + all harness binaries) + product binaries/skills (`pt`, `CLAUDE.md`) | `Build` (BaseImage + Overlays), at build/CI time |
| **Session-snapshot** | the *whole filesystem* of a running isolated session | `Snapshot` → `Persist` (diff), on suspend/archive |
| **User image** | an App image + a *curated* set of artifacts, snapshotted | the **same `Snapshot` primitive** — launch throwaway container, copy artifacts in, snapshot |

Session-snapshots and user images differ only in *what's in the container when you snapshot it*.
Content-hash tagging makes `Resolve` cache hits exact (a returning user with unchanged customisations
cache-hits instantly, no rebuild). The diff base is the **launch image** (not always `Policy.BaseImage`).

### 3 — `Fleet` & placement: horizontal scaling  ([docs/13](docs/13-fleet-placement.md))

The layer between the `Runner` and a *pool* of `ExecutionEnvironment` **workers**. **Each worker IS an
`ExecutionEnvironment`; the Fleet composes above it** — so a single-worker deployment is just a
one-worker fleet, and every recipe keeps working unchanged.

```
PlaceForSession · WorkerForSession · Rebind · Register · Deregister · Workers
```

The **sticky session→worker binding is durable** (persisted on the host's `SessionStore`, not in library
memory) — the single most important statelessness decision: two host replicas behind a load balancer
both resolve the same worker for a session. Placement policy is pluggable (`LeastLoaded` default,
`RoundRobin`; affinity-aware policies slot in). A **lost worker is just an extreme drain** — a bound
session whose worker is gone is restored on a healthy worker *iff a snapshot exists* — which is why the
**restore-portability invariant** holds: **multi-worker fleets require a portable registry**
(`blobarchive` with a shared blob store, or `remote`), validated at Fleet construction.

### 4 — Session orchestration: the `Runner`  ([docs/04](docs/04-session-orchestration.md))

The public facade and the heart of the inversion. The generic logic that today lives in the TypeScript
orchestrator is reimplemented **in Go**, in the host process:

- **Session state machine** with the **flush guard** (cannot transition to `archiving` while
  `pendingFlushCount > 0`) — the single most important correctness invariant, ported verbatim.
- **Control loops** — idle reaper (`Suspend` after `SuspendTimeout`) and archive loop
  (snapshot+persist+destroy after `ArchiveTimeout`), both flush-guard-aware.
- **Recovery** — `ExecutionEnvironment.Recover()` re-adopts orphaned instances on host restart.
- **Ensure-running / restore-on-demand** — resolves the worker via the Fleet, then resumes / restores /
  provisions; **tenancy-aware** (per-session provisions one instance; shared reuses + routes via `/sessions`).
- **Conversation reload on resume** — reloads persisted history and POSTs `/load-conversation`.

Durable identity (session rows, messages, artifacts, snapshot handle), auth, org context, and HTTP
routing stay the **host's** job. The library ships no HTTP server of its own.

### 5 — Event streaming, compaction, and the single reducer  ([docs/05](docs/05-event-streaming.md))

The spine of the system. The library preserves three properties and moves the host-side half into Go:

- **One event vocabulary** — ~20 generic SSE event types defined canonically in `events/events.go`
  (message/tool lifecycle, `ask_user`, `artifact_registered`, status). **Application-specific events**
  (`table_rendered`, `dashboard_created`, …) are *not* in core — they're plugin-defined extension types.
- **One persistence/compaction step** — `EventPipeline` consumes the in-image SSE stream, relays raw
  bytes to the client, and in parallel **compacts** (drop transients, merge consecutive deltas) +
  extracts search text + persists via a `Sink` (which carries the flush-guard hooks).
- **One rendering reducer** — live events and compacted-replayed events go through the *same* pure
  `agentEventReducer`. A second reconstruction path is treated as a bug.

Late-connect replay exists at two layers: the in-image 2000-event buffer (reconnect) and durable replay
from storage (irrecoverable stream).

### 6 — Artifacts (and why they are not snapshots)  ([docs/06](docs/06-artifacts.md))

The agent produces two very different kinds of persisted bytes; conflating them is the most common way
these systems get muddled. **Snapshot** = whole filesystem as an image (resume/publish the session).
**Artifact** = a single user-facing file the agent deliberately produced (download/preview/pin).

`ArtifactStore` — `Save · Load · List · MarkLost` — carries a hard-won status state machine ported
verbatim:

```
live ─┬─→ extracted          (bytes uploaded to blob)
      ├─→ extraction_failed  (retries exhausted)
      └─→ lost               (instance destroyed before extraction)
```

with three non-obvious rules the real impl *must* keep: **never regress `extracted` → `live`**;
**`MarkLost` promotes to `extracted` if a blob already exists**; **`Source` is write-once**. The
redesign generalises single-file capture to **named folder/file-set capture** — the building block for
user images.

### 7 — The in-image agent: multi-session control server + harness seam  ([docs/07](docs/07-in-image-agent.md), [docs/12](docs/12-harness.md))

The only code that *must* run inside the container (TypeScript, `@agentkit/sandbox`). It splits into two
cleanly separated parts:

- **The control server (harness-agnostic, multi-session).** Owns session lifecycle + routing by session
  ID, the SSE stream + replay buffer, the tool-plugin seam, and credential/proxy plumbing. It hosts **N
  concurrent sessions** and never limits the count — the *execution environment* decides routing. Knows
  *nothing* about Docker, suspend/resume, archives, or Azure.

- **Harness adapters (per session).** A **harness** is the thing that drives the model through a turn:
  Claude Agent SDK, Claude CLI, Gemini CLI, Codex. agentkit treats it as a **first-class, per-session
  choice**:

  > **All supported harness binaries are baked into the base image.** The harness is chosen *per
  > session, at runtime, by name.* No execution-environment × harness matrix.

  At session-start the control server resolves the harness, runs a **credential pre-check** (a sandbox
  that physically can't run the requested harness refuses *the session*, not the turn), and instantiates
  it. `ClaudeAgentSdkHarness` (today's SDK `query()` loop) is the only adapter shipped now; the seam is
  open for the rest.

**Multi-session correctness:** routing/state/abort key by session ID (one active turn per session;
cross-session turns run in parallel); the outbound proxy header uses **`AsyncLocalStorage`** so a global
`fetch` patch knows *which* session is making a model call.

The HTTP contract (the stable boundary the Go Runner talks to):

```
POST /sessions · DELETE /sessions/:id · GET /health
POST /sessions/:id/query-stream · GET /sessions/:id/stream/:queryId · POST /sessions/:id/cancel
POST /sessions/:id/load-conversation · /reset-conversation
GET /workspace/files[...] · POST /workspace/scan-secrets · /snapshot · /diff
```

(Flat v0 routes remain as back-compat shims during migration.)

### 8 — Tools: internal vs external, and the plugin seam  ([docs/08](docs/08-tool-registry.md))

Tools are where a generic runtime becomes a *specific* product.

- **Internal tools** run inside the sandbox and execute for real: `Bash`, `Read`, `Grep`, `Skill`, the
  `pt` CLI. *Which* internal tools exist is an **image** decision.
- **External / app-handled tools** don't execute in the sandbox — they return a **marker payload** that
  the PostToolUse hook turns into an SSE event for the host/UI: `ask_user`, `render_table`,
  `create_dashboard`, `register_artifact`. The marker→event mapping is *data* (a `ToolPlugin`
  declaration), not a hard-coded `if/else` ladder.

The core ships the four generic app-handled tools (`ask_user`, `write_file`, `view_image`,
`screenshot_url`); a product registers the rest as plugins. `ToolRegistry.resolve()` is the single place
that builds the SDK options block for a turn.

### 9 — Frontend: the rendering package  ([docs/09](docs/09-frontend-components.md))

`@agentkit/chat-ui` — the React code that turns an event stream into a polished chat (~85% reusable from
`frontend/src/`). The crown jewel is the **single pure `agentEventReducer`** — `(state, event) => state`,
serving live SSE, durable replay, and tests *identically*. Carbon table/chart widgets factor out behind
a **render-plugin seam**:

```ts
interface RenderPlugin<TState> {
  eventTypes: string[];                                   // extension events this plugin owns
  reduce(state: TState, event: AgentSSEEvent): TState;    // fold into plugin-scoped (side-channel) state
  render(props: { event: TState; toolCallId; sessionId }): React.ReactNode;
}
```

The host parameterises endpoints, the model list, side-effect callbacks, theme, and the plugin array via
props/provider — no dependency on Platinum app state, routing, or contexts.

### 10 — Extension points: what a host supplies  ([docs/10](docs/10-extension-points.md))

The doc a host-application author reads. Three categories: **engine adapters** (usually pick a shipped
one), **host service interfaces** (the bulk of what you write), and **plugins** (tool + render).

| Host interface | Required? | Platinum impl |
|---|---|---|
| `SessionStore` | **Yes** | `store.Store` adapter |
| `BlobStore` | **Yes** | `storage.BlobStore` |
| `ScopedClaimsIssuer` | **Yes** (security) | HS256 JWT |
| `OrgContextProvider` | No (default `""`) | merged config + brand + persona |
| `TokenUsageLogger` | No (no-op) | cost logging |
| `ArtifactEnricher` | No (identity) | publish paths / brand |
| `Metrics` | No (no-op) | Prometheus |

Every mock embeds a shared `Recorder`, so a host asserts the exact interaction log — the same hermetic-
test discipline as the interface-refactor's `testharness`.

---

## Deployment recipes  ([docs/11](docs/11-deployment-recipes.md))

The same core runs in three shapes by composing one `ExecutionEnvironment` with one `ImageRegistry`:

| | **A: Dev / tests** | **B: Staging/prod today** | **C: Scaled production** |
|---|---|---|---|
| `ExecutionEnvironment` | `dockerlocal` (or mock) | `dockerdind` | `k8senv` |
| `ImageRegistry` | `localbuild` (or mock) | `blobarchive` | `remote` |
| Isolation | shared | per-container | per-pod |
| Suspend | off | stop/start | scale |
| Snapshot | local tar | diff archive → blob | registry push (or n/a) |
| **Core / Runner / EventPipeline / sandbox / web** | **same** | **same** | **same** |

Only the bottom rows differ, and they're all behind the two engine interfaces. Everything above them —
the genuinely valuable, hard-won logic — is written once. Platinum adopts the library at **Recipe B**.

---

## The three shippable pieces

| Piece | Path | Language | Package | Role |
|---|---|---|---|---|
| **Orchestration core** | [`go/`](go/) | Go | `github.com/bayes-price/agentkit` | Session lifecycle, the two engine interfaces, the `Runner`, `EventPipeline`, `ArtifactStore`, `Fleet`, host-extension seams, in-memory mocks. |
| **In-image agent** | [`sandbox/`](sandbox/) | TypeScript | `@agentkit/sandbox` | The multi-session control server + harness adapters that run *inside* each image. |
| **Chat UI** | [`web/`](web/) | React/TS | `@agentkit/chat-ui` | The single event reducer + components that render a conversation from its event stream, live or replayed. |

---

## Implementation status (snapshot)

Design is ~100%. The build plan — the **AG-1…AG-9** sub-issue series in
[`docs/interface-refactor/stages/06-agent/`](../docs/interface-refactor/stages/06-agent/README.md) — is
complete: the Go library, the in-image harness seam + multi-session control server, the Docker engine,
the image model, and the standalone reference host are all implemented and tested.

| Area | Status |
|---|---|
| All Go interfaces + in-memory mocks + `Recorder` | ✅ |
| `events/` pipeline + compaction · `artifacts/` status machine (+ tests) | ✅ |
| `web/` single reducer + functional render-plugin seam (+ tests) | ✅ |
| `execenv` capability axis (`Backend`/`Tenancy`/`IsolationTier`) + trust gate (AG-1) | ✅ |
| `sandbox/` harness seam (`Harness`/`HarnessRegistry`/`ClaudeAgentSdkHarness`) (AG-2) | ✅ |
| `sandbox/` multi-session control server + AsyncLocalStorage proxy (AG-3) | ✅ |
| `fleet/` placement (`LeastLoaded`/`RoundRobin`) + durable bindings + tenancy-aware `Runner` (AG-4) | ✅ |
| `execenv/docker` DinD + socket adapters (AG-5) — *socket live-verified* | ✅ |
| `imageregistry/{localbuild,blobarchive}` + image model + folder artifacts (AG-6) — *blobarchive live-verified* | ✅ |
| User images via the snapshot primitive (AG-7) | ✅ |
| Standalone reference host + Dockerfile + mock proxy (AG-8, the **usable-standalone milestone**) | ✅ |
| **Integration layer** — `httpapi` mountable handlers (all routes, 28 tests) | ✅ |
| **Reference adapter pack** — `extension/{sqlitestore,filesblob,devclaims}` (opt-in, dep-isolated) | ✅ |
| **`web/`** `AgentChatProvider` + `AgentSessionList` + context hooks (60 tests) | ✅ |
| **`examples/server`** (httpapi + reference adapters + DinD) · **`examples/web`** (Vite app, builds) | ✅ |
| Base image (bash/ripgrep/git **+ Claude Code CLI baked in**, `IS_SANDBOX=1`) + example demo image | ✅ |
| Host-adapters reference doc ([docs/14](docs/14-host-adapters.md)) | ✅ |
| **Full agentic turn live through `examples/server`** (frontend-shaped httpapi → DinD → SSE) | ✅ |

**Live-verified — a full agentic turn end to end.** Against a real DinD daemon + a mock model proxy, a
client POST to the `examples/server` `httpapi` handler drives the whole loop: `CreateSession` →
`SendMessage` streams `connected → message_start → content_delta → query_complete` and the model proxy
is hit. Getting there hardened four real seams (all with tests): `Provision` now gates on the agent's
HTTP `/health` before returning (the userland proxy accepts TCP before the agent listens, so an
immediate `POST /sessions` raced and reset); the agent is addressed via `127.0.0.1` (IPv6-`localhost`
proxy reset); `query-stream` sends `attachments: []` not `null`; and `Policy.SessionEnv` plumbs
model-provider config (`ANTHROPIC_BASE_URL`/key) into each session. The base image now bakes in the
Claude Code CLI with `IS_SANDBOX=1`. Remaining follow-ups (tracked in the AG sub-issues): the
blobarchive **diff fast-path** (the full-archive path ships) and `localbuild.Build` (docker-build).

---

## Quickstart — use agentkit in another project

Add the module:

```bash
go get github.com/bayes-price/agentkit
```

Construct a `Runner` from one implementation of each dependency. This is the **exact** wiring the
reference host uses (`go/examples/standalone/main.go`); swap the in-memory mocks for your own host
adapters in production.

```go
import (
    "github.com/bayes-price/agentkit"
    "github.com/bayes-price/agentkit/agentkittest"      // dev: in-memory host adapters
    "github.com/bayes-price/agentkit/artifacts"
    dockerdind "github.com/bayes-price/agentkit/execenv/docker"
    "github.com/bayes-price/agentkit/fleet"
    "github.com/bayes-price/agentkit/imageregistry"
)

// 1. An ExecutionEnvironment — how a session container runs (here: Docker-in-Docker).
env, _ := dockerdind.NewDinD(dockerdind.DinDConfig{
    DockerHost: "tcp://localhost:2375", PortRangeStart: 30000, PortRangeEnd: 30100, GatewayIP: "172.17.0.1",
})

// 2. A durable SessionStore (host-implemented; MemStore for dev) — owns session rows + worker bindings.
store := agentkittest.NewMemStore()

// 3. A Fleet — one or many workers; scale out by registering more. The trust gate guards
//    multi-tenant-untrusted-on-plain-Docker at construction.
f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: true})
_ = f.Register(ctx, &fleet.Worker{ID: "dind-1", Env: env, Caps: env.Capabilities()})

// 4. The Runner — the single object your HTTP handlers call.
runner, err := agentkit.NewRunner(agentkit.Deps{
    Fleet:     f,
    Registry:  imageregistry.NewMock(), // prod: imageregistry/blobarchive over your BlobStore
    Store:     store,
    Artifacts: artifacts.NewMock(),     // prod: your ArtifactStore
    Claims:    agentkittest.StaticClaims{Token: "dev"}, // prod: a real scoped-JWT issuer
    Policy:    agentkit.Policy{BaseImage: "myapp-sandbox:dev", AgentPort: 3010},
})
if err != nil { /* trust-gate or wiring error */ }
_ = runner.Start(ctx)         // background control loops + crash recovery
defer runner.Close()

// 5. Create a session (choose a harness) and run a turn — SSE streams to the writer.
_, _ = runner.CreateSession(ctx, agentkit.CreateSessionRequest{
    SessionID: "s1", Harness: agentkit.HarnessClaudeAgentSDK,
})
_ = runner.SendMessage(ctx, agentkit.SessionRef{SessionID: "s1"},
    agentkit.SendMessageRequest{Content: "Hello"}, os.Stdout)
```

What a **host** supplies (replacing the dev mocks): a `SessionStore` + `BlobStore` over your DB/object
store, a `ScopedClaimsIssuer` (per-session token), and optionally `OrgContextProvider` /
`TokenUsageLogger` / `ArtifactEnricher` / `Metrics` ([docs/10](docs/10-extension-points.md)). The
in-image agent ships generic tools; product tools/render widgets plug in via the tool- and
render-plugin seams ([docs/08](docs/08-tool-registry.md), [docs/09](docs/09-frontend-components.md)).
Run the full live example with `go/examples/standalone` — see [`examples/README.md`](examples/README.md).

### Full-stack integration — HTTP server + reference adapters + web UI

The snippet above wires the `Runner` directly with in-memory mocks. A real product mounts the library's
**`httpapi`** handlers under its *own* authenticated routes, backed by the **reference adapter pack**
(persistent, each adapter an opt-in subpackage that pulls only its own dependency). This is exactly the
shape of [`go/examples/server`](go/examples/server) (backend) and [`examples/web`](examples/web)
(frontend) — copy them as your starting templates.

**Backend** — reference adapters + `httpapi.New` mounted under your auth middleware:

```go
import (
    "github.com/bayes-price/agentkit/httpapi"
    "github.com/bayes-price/agentkit/extension/sqlitestore"
    "github.com/bayes-price/agentkit/extension/filesblob"
    "github.com/bayes-price/agentkit/extension/devclaims"
    "github.com/bayes-price/agentkit/imageregistry/blobarchive"
)

store, _    := sqlitestore.Open(filepath.Join(dataDir, "sessions.db")) // SessionStore (pure-Go SQLite)
blobs       := filesblob.NewBlobStore(filepath.Join(dataDir, "blobs")) // BlobStore (filesystem)
artStore    := filesblob.NewArtifactStore(blobs)                       // ArtifactStore (filesystem)
claims      := devclaims.New([]byte(secret))                           // dev-only HS256 ScopedClaimsIssuer
registry, _ := blobarchive.New(dockerHost, blobs, "agentkit-snapshots")

runner, _ := agentkit.NewRunner(agentkit.Deps{
    Fleet: f, Registry: registry, Store: store, Artifacts: artStore, Claims: claims,
    Policy: agentkit.Policy{BaseImage: "myapp-example:dev", AgentPort: 3010},
})
_ = runner.Start(ctx)

// Mount the library's handlers under YOUR auth. The IdentityFunc reads the principal your
// middleware already verified — the host owns authentication; the library owns runtime.
api, _ := httpapi.New(httpapi.Config{
    Runner: runner, Store: store, Artifacts: artStore,
    Identity: func(r *http.Request) (httpapi.Identity, error) {
        p, _ := principalFromContext(r.Context()) // set by your middleware from a verified JWT
        return httpapi.Identity{UserEmail: p.email, Customer: p.customer}, nil
    },
})
http.ListenAndServe(":8099", devAuthMiddleware(api.Mux())) // your middleware wraps the Mux
```

`Mux()` registers every route (`POST /agent/session`, `…/{id}/message` (SSE), `…/{id}/stream`, status,
history, artifacts, …). Session-by-ID handlers assume your middleware **already authorized that session
for the principal** — the host owns the durable session catalog, the library owns runtime. Each
interface's contract and the reference/mock to use are in
[docs/14-host-adapters.md](docs/14-host-adapters.md).

**Frontend** — the provider owns data/context; the components render it:

```tsx
import { AgentChatProvider, AgentSessionList, AgentChat } from "@agentkit/chat-ui";

<AgentChatProvider config={{
  apiBaseUrl: "http://localhost:8099",
  getAuthToken: () => `Bearer ${token}`,            // resolved per request (sync or async)
  models: [{ id: "claude-opus-4-5", label: "Opus" }],
}}>
  <AgentSessionList />   {/* the session catalog */}
  <AgentChat />          {/* the live conversation — reads provider context, no props needed */}
</AgentChatProvider>
```

`AgentChatProvider` instantiates the single `useAgentSession` hook (and the single `agentEventReducer`)
internally; `AgentChat` falls back to provider context when props are omitted (props still override).
All endpoint paths are overridable via `config.endpoints`.

### Lift-out checklist (to its own repo)

The Go module already imports **nothing** from Platinum (CI enforces this), so the lift is mechanical:
1. Move `agent-library/` to a new repo root.
2. Drop the consumer's root `go.mod` `replace` and the yarn workspace links; depend on published
   versions (`go get github.com/bayes-price/agentkit@vX.Y.Z`; `@agentkit/sandbox` + `@agentkit/chat-ui`
   from the registry).
3. The CI workflow ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) moves with it unchanged.

---

## Documentation map

| Doc | Topic |
|-----|-------|
| [00-vision.md](docs/00-vision.md) | Why this exists; the generic-vs-specific split; goals & non-goals |
| [01-architecture.md](docs/01-architecture.md) | The layered architecture; the three runtimes; how Go owns orchestration |
| [02-execution-environment.md](docs/02-execution-environment.md) | **The core interface**; Docker/DinD/K8s; capability axis + trust gate |
| [03-image-registry.md](docs/03-image-registry.md) | Image build/save/load/push/pull; the unified image model; snapshot-as-image |
| [04-session-orchestration.md](docs/04-session-orchestration.md) | The orchestration logic moved into Go; lifecycle, reapers, recovery; the `Runner` |
| [05-event-streaming.md](docs/05-event-streaming.md) | Event vocabulary, compaction, persistence, the single reducer |
| [06-artifacts.md](docs/06-artifacts.md) | `ArtifactStore`; the snapshot-vs-artifacts distinction; folder capture |
| [07-in-image-agent.md](docs/07-in-image-agent.md) | The multi-session control server contract |
| [08-tool-registry.md](docs/08-tool-registry.md) | Internal vs external tools; the marker pattern; tool plugins |
| [09-frontend-components.md](docs/09-frontend-components.md) | The React rendering package; the render-plugin seam |
| [10-extension-points.md](docs/10-extension-points.md) | Every seam a host app implements (stores, context, claims, costing) |
| [11-deployment-recipes.md](docs/11-deployment-recipes.md) | Composition recipes: dev, DinD, Kubernetes |
| [12-harness.md](docs/12-harness.md) | **The harness seam** — pluggable agentic frameworks, per session |
| [13-fleet-placement.md](docs/13-fleet-placement.md) | **Fleet & placement** — horizontal scaling across a worker pool |
| [90-provenance-map.md](docs/90-provenance-map.md) | Every library file → its source in Platinum (the copy plan) |
| [91-migration-plan.md](docs/91-migration-plan.md) | How Platinum adopts the library without disruption |

---

## Migration decision (locked)

When Platinum adopts agentkit it goes **native Go DinD directly**: the `ExecutionEnvironment` is
implemented as native Go driving the Docker-in-Docker daemon (porting the orchestrator's lifecycle),
with **no interim wrapper** over the existing TypeScript orchestrator — which is retired at the flip.
Platinum consumes agentkit **in-repo first** (root `go.mod` `replace` for the Go module, yarn workspaces
for `@agentkit/chat-ui` + `@agentkit/sandbox`), and the folder is lifted out to its own repo **last**,
once parity is reached. Full phasing in [docs/91-migration-plan.md](docs/91-migration-plan.md).

This library is the deep-dive expansion of the Agent domain from
[`docs/interface-refactor/06-agent.md`](../docs/interface-refactor/06-agent.md): orchestration moves
*into* Go behind `ExecutionEnvironment` + `ImageRegistry`, and the deliverable is a copied-out, reusable
codebase rather than in-place stubs.

---

## Testing

### Unit Tests (no Docker required)

    # Go — modelproxy, titlebot, events, fleet, artifacts, runner, httpapi, etc.
    cd go && go test ./... -count=1

    # Sandbox — session manager, stream service, harness utilities, built-in tools, plugin loader
    cd sandbox && yarn test

    # Chat UI — event reducer, tool formatters, ToolCallGroup, ArtifactPanel, useFileAttachments, etc.
    cd web && yarn test

    # Mock server — SSE streaming helpers (chunkText, sseEvent, countAssistantMessages)
    cd mock-server && yarn test

### Go Integration Tests (requires Docker)

    # Build the sandbox image first
    docker build -t agentkit-sandbox:systemtest sandbox/

    # Run integration tests
    cd go && go test -tags=integration ./systemtest/ -v -timeout 10m

### Browser E2E Tests (requires Docker + DinD)

Prerequisites:

1. DinD running:

        docker run -d --privileged -p 2375:2375 -e DOCKER_TLS_CERTDIR="" docker:27-dind

2. Sandbox image built and loaded into DinD:

        docker build -t agentkit-sandbox:dev sandbox/
        docker save agentkit-sandbox:dev | docker -H tcp://localhost:2375 load

Run tests:

    cd e2e && yarn test

This starts the mock server, Go example server, and web dev server automatically via global setup, then
runs Playwright tests against them.

### Run everything

    # All unit tests across all packages
    cd go && go test ./... -count=1
    cd sandbox && yarn test
    cd web && yarn test
    cd mock-server && yarn test

    # Typechecks
    cd sandbox && yarn typecheck
    cd web && npx tsc --noEmit
    cd mock-server && yarn typecheck

### Test Architecture

```
Unit Tests (fast, no infra)
├── go/            → go test ./... -count=1
├── sandbox/       → yarn test (vitest)
├── web/           → yarn test (vitest)
└── mock-server/   → yarn test (vitest)

Go System Tests (Docker required)
└── go/systemtest/ → go test -tags=integration

Browser E2E Tests (Docker + DinD)
└── e2e/           → playwright test
    ├── examples/server (Go HTTP API)
    ├── examples/web (React UI)
    └── mock-server (fake Anthropic API)
```
