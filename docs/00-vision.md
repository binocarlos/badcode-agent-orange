# 00 — Vision

## The problem we're solving

Platinum has grown a genuinely good agentic environment: agents run in isolated containers, stream
rich events (text, thinking, tool calls, tables, charts, artifacts), can be suspended/resumed, have
their whole filesystem snapshotted into reusable images, and render in a polished React chat. That
machinery is *valuable on its own* — most of it has nothing to do with cross-tabulation or Carbon.

But it's **entangled** across four codebases in ways that make it impossible to reuse:

- The **orchestration logic** (container lifecycle, suspend/resume, archive/restore, event capture,
  flush guards, idle reaping) lives in a **standalone TypeScript orchestrator process** that is
  hard-wired to Docker via `dockerode` and to Azure blob storage.
- The **Go API** is a ~2,850-line thin HTTP proxy in front of that orchestrator, *plus* a lot of
  genuinely generic logic (artifact state machine, archive coordination, SSE relay) intermixed with
  Platinum specifics (org context, brand themes, personas, token costing).
- The **in-image agent** mixes generic Agent-SDK plumbing with Platinum tools (`render_table`,
  `create_dashboard`, the `pt` CLI).
- The **frontend** has a beautiful, single-codepath event reducer and component set — about 85%
  reusable — but the table/chart widgets bind it to Carbon.

You cannot stand up "the same kind of agent product, but for a different domain" without forking all
four. That's the problem.

## The goal

Extract a **reusable agent-execution library** that captures the generic machinery and exposes the
application-specific parts as **interfaces / plugins**. With it, you can build a new agentic product
by supplying:

1. an **image** (what's installed, what `CLAUDE.md`/skills ship inside it),
2. a **tool set** (which MCP tools the agent has, and how their results render),
3. a few **host hooks** (how to assemble context, where to persist, how to authorise),

…and get container orchestration, snapshotting, event streaming, persistence, and a rendering UI for
free. Different toolsets, different image backgrounds, same runtime.

## The central architectural move: orchestration belongs in the host process, in Go

The single most important decision is that **the orchestrator is not a separate process**. The logic
it performs — "create a session, keep it alive, suspend it when idle, archive it when cold, restore
it on demand, capture and persist its events" — is generic host-side logic. It belongs *in the host
application*, written once in Go.

What genuinely *cannot* be in the host process is **the part that touches the container engine** —
creating a container, exec-ing a command, committing a layer — because that differs fundamentally
between Docker, Docker-in-Docker, and Kubernetes. So we draw the boundary there and make it small:

> **`ExecutionEnvironment` is "a mechanism that runs an agent session inside a container image."**
> Everything above it is generic Go orchestration. Everything below it is engine-specific plumbing.

The current TypeScript orchestrator is, in this framing, *one possible thing on the wrong side of the
boundary*: it bundles generic orchestration logic together with Docker plumbing. We pull the generic
logic up into Go and leave a thin, well-defined contract to the engine.

There still has to be **some** code running *inside* the image — something that receives "run this
agent turn" and actually drives the Claude Agent SDK. That stays in TypeScript (the `sandbox/`
package). But it shrinks to "the agent process," not "the orchestrator."

## The two composable interfaces

The runtime is built on two orthogonal interfaces that **compose**:

### `ExecutionEnvironment` — run commands inside images

The verbs an orchestrator actually needs from a container engine, and nothing more:

- **Provision** a running instance for a session (DinD: create+start a container; single-container:
  ensure/exec into the shared container; K8s: create a pod) and tell me how to reach the in-image
  agent.
- **Exec** a one-off command inside it (workspace listing, secret scan, snapshot prep).
- **Suspend / Resume** it (stop/start; scale to zero / back).
- **Snapshot** its current filesystem into an image reference.
- **Destroy** it.
- **Status / Recover** — live state, and re-adopt orphans after a restart.

How each verb is realised is the implementation's business. "Run this agent session" might launch a
new container (DinD) or exec into an existing one (single-container) — the caller doesn't care.

### `ImageRegistry` — save and load images

Getting images **in** (so the engine can run them) and **out** (so a snapshot survives):

- **EnsurePresent / Pull** — make a base image available to the engine.
- **Build** — produce an image from a build context (local `docker build`, or hand off to a builder).
- **Save / Load** — export/import an image as a stream (`docker save`/`load`) — the local, no-registry path.
- **Push / Pull** — registry-backed transfer.

These compose: snapshotting a session = `ExecutionEnvironment.Snapshot()` (commit the running
container into an image) **then** `ImageRegistry.Persist()` (save the image somewhere durable).
Restoring = `ImageRegistry.Materialize()` **then** `ExecutionEnvironment.Provision(fromImage:…)`.

The compositions that make sense:

| Environment | Registry | Use case |
|-------------|----------|----------|
| Single container | Local build | Dev / tests — images built locally, no isolation, exec into one container |
| Docker-in-Docker | Local build + blob-archive | Current Platinum staging/prod — full daemon, snapshots saved as diff archives to blob storage |
| Kubernetes | Remote registry | Scaled production — pods pull from a registry, snapshots pushed back |

You would not normally pair Kubernetes with a local image builder, or single-container with a remote
registry — but nothing in the interfaces *forbids* it. They're orthogonal by design.

## Snapshot-as-image is first class — and distinct from artifacts

Two different things produce persisted bytes from a session, and the library keeps them apart:

- **Snapshot** = the *whole filesystem* of a running session, captured as an **image** so the session
  can be resurrected later (or published as a reusable app template). This is an `ExecutionEnvironment`
  + `ImageRegistry` concern. Today this is the `docker commit` → `docker diff`/`getArchive` → tar →
  gzip → blob "diff archive" dance; in the library it's `Snapshot()` + `Persist()`, and the diff-archive
  trick becomes *one* `ImageRegistry` implementation.
- **Artifacts** = *individual user-facing files* the agent deliberately produced (a report, a chart
  JSON, a generated web app), registered for download/preview. This is an `ArtifactStore` concern.

Conflating them is a category error: a snapshot is "resume this session"; an artifact is "here is the
deliverable." Keeping the interfaces separate is what lets, e.g., a session be published as an app
(snapshot) while its charts are pinned to a dashboard (artifacts).

## What ships, and in what form

The deliverable is **comprehensive design docs + a full, copied-out codebase** staged in this folder,
ready to become its own repo. Concretely:

- **`go/`** — a standalone Go module (`go.mod` of its own, importing nothing from Platinum) holding
  the orchestration core, the two engine interfaces, the `Runner` facade, `EventStreamer`,
  `ArtifactStore`, the host-extension interfaces, and in-memory mocks for all of them. The interfaces
  and types are real and compile; the Docker/DinD/K8s implementation bodies are filled in from the
  provenance map ([90](90-provenance-map.md)) in staged passes.
- **`sandbox/`** — the in-image TypeScript agent, copied from `agent/src/` with the Platinum tools
  factored out behind a tool-plugin seam.
- **`web/`** — the React rendering package, copied from `frontend/src/`'s agent components with the
  Carbon widgets factored out behind a render-plugin seam.

## Migration decision (locked)

When Platinum adopts this library it goes **native Go DinD directly**: the `ExecutionEnvironment` is
implemented as native Go driving the Docker-in-Docker daemon (porting the orchestrator's lifecycle),
with **no interim wrapper** over the existing TypeScript orchestrator — which is retired at the flip.
Platinum consumes agentkit **in-repo first** (root `go.mod` `replace` for the Go module, yarn
workspaces for `@agentkit/chat-ui` + `@agentkit/sandbox`) and the folder is lifted out to its own repo
**last**, once parity is reached. Full phasing: [91-migration-plan.md](91-migration-plan.md).

## Goals

- **Reusability** — build a new agentic product by supplying an image, a toolset, and a few hooks.
- **Engine portability** — the same orchestration runs on a laptop Docker, a DinD daemon, or K8s by
  swapping one interface implementation.
- **Testability** — the whole runtime boots against in-memory mocks (no Docker, no registry, no
  network), so a host app can integration-test its agent flows in milliseconds. (Mirrors the
  interface-refactor's recorder-based hermetic-test philosophy.)
- **A clean generic/specific seam** — every Platinum-ism is a named extension point, not a hard-coded
  assumption.
- **Faithful distillation** — copy the *behaviour* that works today (the artifact state machine, the
  flush-guarded archive, the late-connect replay buffer, the single reducer) rather than reinventing.

## Non-goals (for this library)

- **Not** an external dependency that Platinum is refactored *into* right now. It's a copy; Platinum
  adopting it is a deliberate, later, separately-planned step ([91](91-migration-plan.md)).
- **Not** a re-architecture of the in-image agent's relationship to the Claude Agent SDK — that
  contract is good and is preserved.
- **Not** a generic container-orchestration framework. It does exactly what agent sessions need; the
  `ExecutionEnvironment` surface is deliberately small.
- **Not** image-registry plumbing as a product — `ImageRegistry` is "enough to get an agent image in
  and a snapshot out," not a general OCI toolkit.
- **Not** in scope this pass: deleting or rewiring any Platinum code. The existing orchestrator,
  `agent.go`, and frontend keep running exactly as they do.

## How to read the rest

Read [01-architecture.md](01-architecture.md) next for the layered picture, then the two core
interface docs ([02](02-execution-environment.md), [03](03-image-registry.md)) which carry the bulk
of the new design. [04](04-session-orchestration.md) covers the logic moving up from TypeScript into
Go. [10](10-extension-points.md) is the doc a *host application author* reads to understand exactly
what they must supply.
