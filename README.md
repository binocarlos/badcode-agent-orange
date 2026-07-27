# Agent Orange

**A runtime for durable, container-backed agent sessions, organised into projects, on which
self-improving arrangements of workers are composed with prompts alone.**

Two layers live in this repo.

**The runtime.** A *session* is a system prompt, a base image, MCP tools and skills. The runtime
provisions a container, runs an in-image agent harness inside it, and streams the conversation back
over SSE. Sessions are interactive (message back and forth), durable (state persisted), and
snapshot-able (commit a running session to a new image, then resume it later).

**The product layer.** A *project* is a namespace holding **workers** (rows of prompt + tools +
wiring), an append-only labeled **memory** with hybrid keyword/vector search, an **event** spine
with subscriptions and cron **schedules**, named and versioned **images** and **skills**, and an
append-only **config log** with point-in-time fold. Workers are woken by events or schedules,
composed into jobs, and can rewrite each other's prompts through MCP tools.

That last clause is the design's definition of done, and it closes. One worker rewrites another
worker's system prompt with a rationale, and the next job runs under the new prompt:
`e2e/features/acceptance-loop.spec.ts`, offline against the mock model. The same loop was observed
against the real Anthropic API on 2026-07-26, including the prompt-injection boundary holding —
an event whose text ordered the model to ignore its prompt was treated as content, not as
instructions.

## Where to start

Read in this order. Each step assumes the one before it.

1. **This file** — what the system is.
2. [`README-stack.md`](README-stack.md) — run it locally in one command, and look at it.
3. [`docs/01-architecture.md`](docs/01-architecture.md) — the runtime: the layers, and how Go owns
   orchestration.
4. [`docs/product/00-overview.md`](docs/product/00-overview.md) — the product layer's quick map,
   then [`docs/product/17-product-spec.md`](docs/product/17-product-spec.md) for the goal, the
   atoms and the binding principles P1–P8.
5. [`docs/18-workers-memory-events.md`](docs/18-workers-memory-events.md) — operating the product
   layer: workers, triggers, memory and briefings, the core tools, the config log, the env vars,
   the known limitations.
6. The engine reference docs below, by seam, when you need one.

Two documents are worth reading even though they are not designs:

- [`docs/product/06-work-plan.md`](docs/product/06-work-plan.md) — the 37-item build checklist, and
  after it a **Discovered Issues Log** of what was actually built and what surprised us. It is the
  most reliable record in the repo of the difference between the design and the code.
- [`MIGRATION.md`](MIGRATION.md) — how this repo became standalone, and the registry/GCP roadmap.

If you are an agent working in this repo, read [`CLAUDE.md`](CLAUDE.md) first instead — it is the
operating guide, and it carries the rules that keep the engine liftable.

## Quickstart

One command brings up the API, chat UI, and container runtime (Docker required):

```sh
cp .env.example .env          # optionally set ANTHROPIC_API_KEY=sk-ant-...
docker compose up --build     # then open http://localhost:8080
```

With `ANTHROPIC_API_KEY` set you get a real agent; without one, a deterministic mock model replies
so the UI works offline. The product layer is wired only when `DATABASE_URL` is set — compose
always sets it. Details, including login/projects, credential precedence and the e2e loop, are in
[`README-stack.md`](README-stack.md).

This is the standalone demo, not how you embed the engine as a library. For that, see
[`docs/14-host-adapters.md`](docs/14-host-adapters.md) and `go/examples/standalone/`.

## The three components

| Component | Path | What it is |
|---|---|---|
| **Orchestration core** | [`go/`](go/) | The engine — Go module `github.com/binocarlos/badcode-agent-orange`. The `Runner`, session lifecycle, container control, image registry, persistence, the event pipeline, and the whole product layer (`go/agentdb/`, `go/compose.go`, `go/cmd/agentd/`). This is the library a host app embeds. Entry type: `Runner` in `go/agentkit.go`. |
| **In-image agent** | [`sandbox/`](sandbox/) | TypeScript. The HTTP/SSE control server that runs *inside* each session container and wraps the model harness (`@anthropic-ai/claude-agent-sdk`). `sandbox/Dockerfile` builds the harness image. |
| **UI library** | [`web/`](web/) | React components: chat (one event reducer renders live and replayed sessions identically) plus the product-layer pages — project settings, workers, events and jobs, subscriptions and schedules, the changelog. No router and no app shell; the shell that composes it is `examples/web/`, which is what the stack serves. |

The engine sits behind two composable interfaces — `ExecutionEnvironment` (how a session container
runs: Docker / Docker-in-Docker) and `ImageRegistry` (get images in, get snapshots out). Swap an
implementation to move from laptop Docker to a scaled fleet; the core does not change.

## Status

Private use for now — the source is bayesprice-owned (forked wholesale from an in-house Go runtime,
"agentkit"). A public release would need licensing resolved first.

The product layer is built: every item in [`docs/product/06-work-plan.md`](docs/product/06-work-plan.md)
is ticked, and the acceptance loop closes both offline and against the real API. What is
deliberately *not* done is the first production seeding — the BadCode marketing manager of §8.8 —
because that is a production act rather than a verification.

All three packages build and test; the gates are `go build ./... && go vet ./... && go test ./...`
in `go/` (Go floor **1.25**, raised by the GCP SDK) and each JS package's own `typecheck` + `test`
scripts. `.github/workflows/ci.yml` runs all three on pushes to `main` and on pull requests, and
enforces the liftability invariant (no Platinum imports from the `go/` module).

GCP support is implemented and wired into `agentd`: a GCS `BlobStore`, a pluggable registry-auth
seam with an Application Default Credentials provider, and config-driven backend selection.
Artifact upload and snapshot push+pull were recorded verified against the live project on
2026-06-25 — see [`MIGRATION.md`](MIGRATION.md) §4a/§4b for the evidence and its limits.

## Documentation

| Doc | Topic |
|---|---|
| [docs/01-architecture.md](docs/01-architecture.md) | The layered architecture; how Go owns orchestration |
| [docs/02-execution-environment.md](docs/02-execution-environment.md) | The container seam; Docker/DinD; capability axis + trust gate |
| [docs/03-image-registry.md](docs/03-image-registry.md) | Image build/save/load/push/pull; the unified image model; snapshot-as-image |
| [docs/05-event-streaming.md](docs/05-event-streaming.md) | Event vocabulary, compaction, persistence, the single reducer |
| [docs/06-artifacts.md](docs/06-artifacts.md) | `ArtifactStore`; the snapshot-vs-artifacts distinction |
| [docs/07-in-image-agent.md](docs/07-in-image-agent.md) | The multi-session control server contract; the harness seam |
| [docs/13-fleet-placement.md](docs/13-fleet-placement.md) | Fleet & placement — horizontal scaling across a worker pool |
| [docs/14-host-adapters.md](docs/14-host-adapters.md) | Every seam a host app implements (stores, context, claims, costing) |
| [docs/15-standalone-stack.md](docs/15-standalone-stack.md) | Running the standalone stack; library vs standalone |
| [docs/18-workers-memory-events.md](docs/18-workers-memory-events.md) | **Operating the product layer** — workers, memory, events, schedules, tools |
| [docs/product/](docs/product/) | The product spec: `00-overview` (map), `17-product-spec` (entry point), `01`–`09` (component designs), `06-work-plan` (checklist + Discovered Issues Log) |

The numbered engine docs have deliberate gaps (there is no 04, 08–12, 16, 17 at the top level;
`17` is the product spec, under `docs/product/`).

Alongside the numbered docs:

- [installations/README.md](installations/README.md) — installation images: the derived-image tree, the overlay model, `imagetree`.
- [README-stack.md](README-stack.md) — running the standalone stack.
- [MIGRATION.md](MIGRATION.md) — standalone-ification + registry/GCP roadmap and live status.
- [CLAUDE.md](CLAUDE.md) — the operating guide for an agent working in this repo.
