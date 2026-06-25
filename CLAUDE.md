# CLAUDE.md — operating guide for Agent Orange

Read this first. It tells an agent what Agent Orange is, how the repo is laid out, how to build and
run it, and the rules to keep it healthy.

## What Agent Orange is

Agent Orange is a **reusable runtime for container-backed AI agent sessions**. You create a
**session** configured with a system prompt, a base image, MCP tools, and skills; the runtime
provisions a container, runs an in-image agent harness inside it, and streams the conversation back
over SSE. Sessions are interactive (message back and forth), durable (state persisted), and
snapshot-able (commit a running session to a new image).

It was forked wholesale from an in-house Go runtime ("agentkit"); this repo is now the canonical
Agent Orange. Three pieces:

- **Go orchestration core** (`go/`) — module `github.com/binocarlos/badcode-agent-orange`. The
  `Runner`, session lifecycle, container control, image registry, persistence, event pipeline. This
  is the library a host app embeds.
- **In-image agent** (`sandbox/`, TypeScript) — the control server that runs *inside* each session
  container and wraps the model harness (`@anthropic-ai/claude-agent-sdk`).
- **Chat UI** (`web/`, React) — renders live or replayed sessions from the canonical event stream.

> **Status / provenance.** Private use for now (the source is bayesprice-owned — a public release
> would need licensing resolved first). Migration to standalone is in progress: **`go build ./...`
> passes**; the `sandbox`/`web` packages have not been `npm`-built in this fork yet; registry auth is
> basic-only (no GCP yet). The plan and current state live in **`MIGRATION.md`** — read it before
> doing migration work.

## Repo map

| Path | What |
| --- | --- |
| `go/` | The engine (Go module). Entry type: `Runner` in `go/agentkit.go`. CLIs in `go/cmd/` (`agentd`, `imagetree`). Runnable examples in `go/examples/` (`standalone`, `mockproxy`, `exampleimage`). |
| `sandbox/` | In-image agent (TS). The HTTP/SSE control server + harness adapter that runs inside a session container. `sandbox/Dockerfile` builds the harness image. |
| `web/` | React chat UI. The single event reducer drives live + replay identically. |
| `installations/` | **Example** base images (`core`, `example`) — see `installations/README.md`. Real per-project images live in their own project repos. |
| `docs/` | Numbered architecture docs (`00-vision` … `16-derived-images`, `90-provenance-map`, `91-migration-plan`). Start at `01-architecture.md`. |
| `migration-reference/` | **Reference only — do NOT build or import.** Platinum host-side image pipeline + the original Platinum installations, kept to port from. May contain host-app coupling. |
| `deploy/`, `docker-compose*.yml`, `README-stack.md` | The standalone stack (run it with one command — below). |
| `mock-server/`, `e2e/`, `examples/` | Mock model server, end-to-end tests, example host + web bits. |
| `MIGRATION.md` | The standalone-ification + registry-agnostic + GCP roadmap and live status. |

## Run it (standalone stack)

One command brings up API + chat UI + container runtime (Docker required):

```sh
cp .env.example .env          # optionally set ANTHROPIC_API_KEY=sk-ant-...
docker compose up --build     # then open http://localhost:8080
```

- With `ANTHROPIC_API_KEY` → a real agent. Without → a deterministic mock model (works offline).
- Services: `web` (UI), `agentd` (API+orchestrator), `dind` (Docker-in-Docker, one container per
  session), `init-sandbox` (builds the sandbox image into DinD).
- Point sessions at a custom base image with `BASE_IMAGE=<image>` in `.env` (see
  `installations/README.md` and `docs/15-standalone-stack.md`).

This is the *standalone demo*, not how you embed the engine as a library — for that, see
`docs/14-host-adapters.md` and `go/examples/standalone/`.

## Build & test the engine

```sh
cd go
go build ./...     # must stay green
go vet ./...
go test ./...      # some suites (systemtest/e2e) need Docker available
```

In-image agent + UI (not yet wired in this fork — Phase 1 of MIGRATION.md):

```sh
cd sandbox && npm install && npm test
cd web && npm install && npm test
```

## Core concepts (where to look)

- **Runner** (`go/agentkit.go`, `go/runner.go`) — create/message/stream/snapshot a session. The API surface a host calls.
- **ExecutionEnvironment** (`go/execenv/`) — the container seam; Docker + Docker-in-Docker adapters. `docs/02-execution-environment.md`.
- **ImageRegistry** (`go/imageregistry/`) — `EnsurePresent`/`Build`/`Persist`/`Materialize` (pull/build/push/restore). Adapters: `ociregistry` (registry push/pull — **basic auth only today**), `blobarchive` (snapshot to blob). `Build()` is stubbed (host builds). `docs/03-image-registry.md`, `docs/16-derived-images.md`.
- **Installations** — layered base images sessions launch from: sandbox harness → `core` → `example` → per-project. `installations/README.md`.
- **Harness** (`sandbox/`) — wraps `@anthropic-ai/claude-agent-sdk`; pluggable per session. `docs/12-harness.md`.
- **Events / streaming** (`go/events/`) — one canonical SSE event vocabulary; `web/` reduces it. `docs/05-event-streaming.md`.
- **Persistence** (`go/agentdb/`, `go/extension/`) — sessions/events/artifacts; host-implemented store seams (Postgres in prod). `docs/14-host-adapters.md`.
- **Multi-tenancy** — `ContextScope{Customer, Job, ...}` + scoped tokens; auth is delegated to the host. `docs/10-extension-points.md`.

## Rules for working in this repo

1. **Liftability invariant.** The `go/` module must import **nothing** from any host app — CI
   (`.github/workflows/ci.yml`) enforces this. Keep the engine self-contained.
2. **Module path** is `github.com/binocarlos/badcode-agent-orange`. Don't reintroduce the old
   `bayes-price/agentkit` path or any Platinum coupling.
3. **`migration-reference/` is reference, not code.** Don't build it, import it, or wire it into the
   module. Port *from* it deliberately (see `MIGRATION.md`).
4. **Installation Dockerfiles never set** `CMD`/`ENTRYPOINT`/`EXPOSE`/`HEALTHCHECK` — the sandbox
   base owns those. Installations only add environment, tools, and `/workspace` content.
5. **Keep `go build ./...` green** and add tests with changes — the codebase is heavily tested
   (follow the existing table-test patterns).
6. **Migration work is phased** (`MIGRATION.md`): standalone-ify → genericize installations →
   registry-agnostic build+push → **GCP Artifact Registry (priority)** → automation. The one thing
   gating GCP is a pluggable registry-auth seam; today auth is hardcoded basic user/pass.

## Deeper context

- `docs/00-vision.md`, `docs/01-architecture.md` — what it is and how it fits together.
- `docs/15-standalone-stack.md`, `README-stack.md` — running it.
- `docs/16-derived-images.md`, `installations/README.md` — installations / image layering.
- `MIGRATION.md` — current status + the registry/GCP roadmap.
