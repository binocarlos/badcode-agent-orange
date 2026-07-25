# Agent Orange

Agent Orange is a **reusable runtime for durable, container-backed AI agent sessions, organised
into projects.** You configure a **session** — a system prompt, a base image, MCP tools, skills —
and the runtime provisions a container, runs an in-image agent harness inside it, and streams the
conversation back over SSE. Sessions are interactive (message back and forth), durable (state
persisted), and snapshot-able (commit a running session to a new image, then resume it later).

Everything else a product needs — workers, consultants, memory, review loops — is meant to be
*data and wiring on top of those session atoms*, never new engine machinery. The forward-looking
product spec is [`docs/product/17-product-spec.md`](docs/product/17-product-spec.md).

## Quickstart

One command brings up the API, chat UI, and container runtime (Docker required):

```sh
cp .env.example .env          # optionally set ANTHROPIC_API_KEY=sk-ant-...
docker compose up --build     # then open http://localhost:8080
```

With `ANTHROPIC_API_KEY` set you get a real agent; without one, a deterministic mock model replies
so the UI works offline. Full details — login/projects, the model-credential precedence, the
end-to-end test loop — are in [`README-stack.md`](README-stack.md).

This is the standalone demo, not how you embed the engine as a library. For that, see
[`docs/14-host-adapters.md`](docs/14-host-adapters.md) and `go/examples/standalone/`.

## The three components

| Component | Path | What it is |
|---|---|---|
| **Orchestration core** | [`go/`](go/) | The engine — Go module `github.com/binocarlos/badcode-agent-orange`. The `Runner`, session lifecycle, container control, image registry, persistence, and the event pipeline. This is the library a host app embeds. Entry type: `Runner` in `go/agentkit.go`. |
| **In-image agent** | [`sandbox/`](sandbox/) | TypeScript. The HTTP/SSE control server that runs *inside* each session container and wraps the model harness (`@anthropic-ai/claude-agent-sdk`). `sandbox/Dockerfile` builds the harness image. |
| **Chat UI** | [`web/`](web/) | React. A single event reducer that renders live or replayed sessions from the canonical event stream, identically. |

The engine sits behind two composable interfaces — `ExecutionEnvironment` (how a session container
runs: Docker / Docker-in-Docker / Kubernetes) and `ImageRegistry` (get images in, get snapshots
out). Swap an implementation to move from laptop Docker to a scaled fleet; the core doesn't change.

## Status

Private use for now — the source is bayesprice-owned (forked wholesale from an in-house Go runtime,
"agentkit"). A public release would need licensing resolved first.

Migration to a fully standalone repo is in progress: **`cd go && go build ./...` passes** (Go floor
is **1.25**, raised by the GCP SDK). GCP support is implemented and wired into `agentd` — a GCS
`BlobStore`, a pluggable registry-auth seam with an ADC provider, and config-driven backend
selection, all verified end-to-end against a live project. The `sandbox/` and `web/` packages have
not yet been `npm`-built in this fork. The plan and live status are in
[`MIGRATION.md`](MIGRATION.md).

## Documentation

Start at [`docs/01-architecture.md`](docs/01-architecture.md); the authoritative forward spec is
[`docs/product/17-product-spec.md`](docs/product/17-product-spec.md).

| Doc | Topic |
|---|---|
| [docs/01-architecture.md](docs/01-architecture.md) | The layered architecture; how Go owns orchestration |
| [docs/02-execution-environment.md](docs/02-execution-environment.md) | The container seam; Docker/DinD/K8s; capability axis + trust gate |
| [docs/03-image-registry.md](docs/03-image-registry.md) | Image build/save/load/push/pull; the unified image model; snapshot-as-image |
| [docs/05-event-streaming.md](docs/05-event-streaming.md) | Event vocabulary, compaction, persistence, the single reducer |
| [docs/06-artifacts.md](docs/06-artifacts.md) | `ArtifactStore`; the snapshot-vs-artifacts distinction |
| [docs/07-in-image-agent.md](docs/07-in-image-agent.md) | The multi-session control server contract; the harness seam |
| [docs/13-fleet-placement.md](docs/13-fleet-placement.md) | Fleet & placement — horizontal scaling across a worker pool |
| [docs/14-host-adapters.md](docs/14-host-adapters.md) | Every seam a host app implements (stores, context, claims, costing) |
| [docs/15-standalone-stack.md](docs/15-standalone-stack.md) | Running the standalone stack; library vs standalone |
| [docs/product/17-product-spec.md](docs/product/17-product-spec.md) | **The authoritative forward spec** — atoms, workers, memory, events |

Alongside the numbered docs:

- [installations/README.md](installations/README.md) — installation images (the derived-image tree, the overlay model, `imagetree`).
- [README-stack.md](README-stack.md) — running the standalone stack.
- [MIGRATION.md](MIGRATION.md) — standalone-ification + registry/GCP roadmap and live status.
- [CLAUDE.md](CLAUDE.md) — the operating guide for an agent working in this repo.
