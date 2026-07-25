# CLAUDE.md — operating guide for Agent Orange

Read this first. It tells an agent what Agent Orange is, how the repo is laid out, how to build and
run it, and the rules to keep it healthy.

## Who

BadCode is two people: **Kai Davenport** (main developer) and **Jack** (lead creative
designer). Either may be the user in a session. BadCode's marketing manager is the first real
Agent Orange use case (`docs/product/17-product-spec.md` §8.8).

## What Agent Orange is

Agent Orange is a **reusable runtime for container-backed AI agent sessions**. You create a
**session** configured with a system prompt, a base image, MCP tools, and skills; the runtime
provisions a container, runs an in-image agent harness inside it, and streams the conversation back
over SSE. Sessions are interactive (message back and forth), durable (state persisted), and
snapshot-able (commit a running session to a new image).

On top of that runtime sits the **product layer**: a project is a namespace holding *workers*
(rows of prompt + tools + wiring), an append-only labeled *memory*, an *event* spine with
subscriptions and cron *schedules*, named *images* and *skills*, and a *config log* recording
every management mutation. Workers are woken by events or schedules, composed into jobs, and can
rewrite each other's prompts through MCP tools — that self-improvement loop is the design's
definition of done. Spec: `docs/product/`. Operating guide: `docs/18-workers-memory-events.md`.

It was forked wholesale from an in-house Go runtime ("agentkit"); this repo is now the canonical
Agent Orange. Three pieces:

- **Go orchestration core** (`go/`) — module `github.com/binocarlos/badcode-agent-orange`. The
  `Runner`, session lifecycle, container control, image registry, persistence, event pipeline. This
  is the library a host app embeds.
- **In-image agent** (`sandbox/`, TypeScript) — the control server that runs *inside* each session
  container and wraps the model harness (`@anthropic-ai/claude-agent-sdk`).
- **Chat UI** (`web/`, React) — a component *library* (no router, no app shell) rendering live or
  replayed sessions from the canonical event stream, plus the product-layer pages. The shell that
  composes it is `examples/web/`, which is what the stack serves.

> **Status / provenance.** Private use for now (the source is bayesprice-owned — a public release
> would need licensing resolved first). All three packages build and test clean in this fork:
> `go build ./... && go vet ./... && go test ./...` (Go floor is now **1.25**, raised by the GCP
> SDK), `cd sandbox && npm ci && npm test` (157 tests), `cd web && npm ci && npm test` (543 tests).
> The **product layer is built** — 33 of `docs/product/06-work-plan.md`'s 37 items are merged and
> the acceptance loop (§8.7: a worker rewrites another worker's prompt) closes offline in
> `e2e/features/acceptance-loop.spec.ts`. **I4** has now landed too — a worker's `image` pointer
> is bound end to end (`worker.image > project base_image > global`, resolved at every launch,
> failing the job rather than substituting an image), proved by
> `e2e/features/image-curation.stack.spec.ts`. The open items are **G1** (the acceptance e2e,
> landed but not yet ticked), **G2** (docs), and **G3** (live run with a real key, then the first
> production seeding).
> GCP support is implemented and **wired into `agentd`**: a GCS `BlobStore` (`extension/gcsblob`),
> a pluggable registry-auth seam with an ADC provider (`imageregistry/auth`, `auth.GCP`), and
> config-driven backend selection (`cmd/agentd/backends.go`, env: `AGENTKIT_BLOB_BACKEND`,
> `AGENTKIT_REGISTRY_BACKEND`, …). Defaults preserve the local fs stack; blob upload and
> snapshot push+pull were verified against the live project on 2026-06-25 (MIGRATION.md §4a/§4b).
> The plan and current state live in **`MIGRATION.md`** — read it before doing migration work.

## Repo map

| Path | What |
| --- | --- |
| `go/` | The engine (Go module). Entry type: `Runner` in `go/agentkit.go`. CLIs in `go/cmd/` (`agentd`, `imagetree`). Runnable examples in `go/examples/` (`standalone`, `mockproxy`, `exampleimage`). |
| `sandbox/` | In-image agent (TS). The HTTP/SSE control server + harness adapter that runs inside a session container. `sandbox/Dockerfile` builds the harness image. |
| `web/` | React component library: chat (one event reducer drives live + replay identically) plus the product-layer pages — project settings, workers, events/jobs, subscriptions + schedules editors, changelog. No router; the app shell is `examples/web/`. |
| `installations/` | **Example** base images (`core`, `example`) — see `installations/README.md`. Real per-project images live in their own project repos. |
| `docs/` | Numbered architecture docs, consolidated 2026-07-22 (numbering has deliberate gaps): `01-architecture`, `02-execution-environment`, `03-image-registry`, `05-event-streaming`, `06-artifacts`, `07-in-image-agent`, `13-fleet-placement`, `14-host-adapters`, `15-standalone-stack`, `18-workers-memory-events` (the product layer, from an operator's seat — read it before touching workers/memory/events code). Start at `01`. The authoritative product spec is `docs/product/17-product-spec.md` (entry point: goal, atoms, principles, § map) + `docs/product/00`–`09` (`00-overview` = quick map; component designs; original § numbers preserved). The research trail and executed plan records live beside the spec as dated files in the same folder. |
| `migration-reference/` | **Reference only — do NOT build or import.** Platinum host-side image pipeline + the original Platinum installations, kept to port from. May contain host-app coupling. |
| `deploy/`, `docker-compose*.yml`, `README-stack.md` | The standalone stack (run it with one command — below). |
| `mock-server/`, `e2e/`, `examples/` | Mock model server; end-to-end tests (**`e2e/features/` + `playwright.stack.config.ts` is the current rig, run against the compose stack — `e2e/tests/` is the older Vite+mock-server one**); example host + `examples/web/` (the app shell the stack actually serves — `web/` is a component library with no router). |
| `MIGRATION.md` | The standalone-ification + registry-agnostic + GCP roadmap and live status. |

## Run it (standalone stack)

One command brings up API + chat UI + container runtime (Docker required):

```sh
cp .env.example .env          # optionally set ANTHROPIC_API_KEY=sk-ant-...
docker compose up --build     # then open http://localhost:8080
```

- With `ANTHROPIC_API_KEY` → a real agent. Without → a deterministic mock model (works offline).
- Services: `web` (UI), `agentd` (API+orchestrator+router+scheduler), `dind` (Docker-in-Docker,
  one container per session), `init-sandbox` (builds the sandbox image into DinD), `postgres`
  (pgvector image — sessions *and* the whole product layer).
- Point sessions at a custom base image with `BASE_IMAGE=<image>` in `.env` (see
  `installations/README.md` and `docs/15-standalone-stack.md`).
- **The product layer is wired only when `DATABASE_URL` is set.** On the sqlite fallback the
  router never routes, schedules never fire, the core MCP server is not mounted and project
  settings do not apply — and nothing fails at use time. Compose always sets it.
- **Sessions hold a running container until deleted**; nothing reaps them on a timer, and enough
  of them stops the stack provisioning anything. Delete sessions you finish with;
  `./e2e/run-stack-e2e.sh clean` clears leftovers and restarts `agentd` (which that command
  requires — see `README-stack.md`).
- The stack serves a **built** image of `examples/web`, so UI edits are invisible to browser
  tests until `docker compose up -d --build web`.

This is the *standalone demo*, not how you embed the engine as a library — for that, see
`docs/14-host-adapters.md` and `go/examples/standalone/`.

## Build & test the engine

```sh
cd go
go build ./...     # must stay green
go vet ./...
go test ./...      # some suites (systemtest/e2e) need Docker available
```

`agentdb`'s **live-Postgres** cases skip unless `AGENTKIT_TEST_POSTGRES_URL` is set — a green
`go test ./...` therefore does *not* prove the pgvector/jsonb-selector paths. Set it (a throwaway
database, not a shared one: an unmerged migration on a sibling branch has broken other agents'
runs before) when touching `agentdb`.

In-image agent + UI (both green):

```sh
cd sandbox && npm ci && npm test   # 157 tests
cd web && npm ci && npm test       # 543 tests
```

`sandbox/` tracks both `package-lock.json` and a stale `yarn.lock`; `npm` keeps the yarn
lockfile in sync, so an install dirties the tree — `git checkout sandbox/yarn.lock` after.
Same footgun in `examples/web/`. `web/`'s `npm run build` is `tsc --noEmit`: it typechecks and
emits no `dist/`.

## Core concepts (where to look)

- **Runner** (`go/agentkit.go`, `go/runner.go`) — create/message/stream/snapshot a session. The API surface a host calls.
- **ExecutionEnvironment** (`go/execenv/`) — the container seam; Docker + Docker-in-Docker adapters. `docs/02-execution-environment.md`.
- **ImageRegistry** (`go/imageregistry/`) — `EnsurePresent`/`Build`/`Persist`/`Materialize` (pull/build/push/restore). Adapters: `ociregistry` (registry push/pull — pluggable auth via `imageregistry/auth`: `auth.Static` basic or `auth.GCP` ADC tokens), `blobarchive` (snapshot to blob). `Build()` is stubbed (host builds). `docs/03-image-registry.md`, `installations/README.md`.
- **Installations** — layered base images sessions launch from: sandbox harness → `core` → `example` → per-project. `installations/README.md`.
- **Harness** (`sandbox/`) — wraps `@anthropic-ai/claude-agent-sdk`; pluggable per session. `docs/07-in-image-agent.md`.
- **Events / streaming** (`go/events/`) — one canonical SSE event vocabulary; `web/` reduces it. `docs/05-event-streaming.md`.
- **Persistence** (`go/agentdb/`, `go/extension/`) — sessions/events/artifacts; host-implemented store seams (Postgres in prod). `docs/14-host-adapters.md`.
- **Multi-tenancy** — `ContextScope{Customer, Job, ...}` + scoped tokens; auth is delegated to the host. The `customer` claim **is** the product layer's project. `docs/14-host-adapters.md`.

Product layer (all Postgres-only, all in `go/agentdb/` + `go/cmd/agentd/`):

- **Job composition** (`go/compose.go`) — one pure `ComposeJob`: image, prompt concatenation, MCP merge, event-as-first-message. Heavily pinned by test, including the core preamble byte-for-byte. `docs/product/02-workers.md`.
- **Memory** (`go/agentdb/memories.go`) — append-only, K8s labels + selectors, hybrid keyword/vector RRF search (§7.6). One selector parser; images and skills reuse it.
- **Router + scheduler + dispatch gate** (`go/cmd/agentd/router.go`, `scheduler.go`, **`dispatch.go`** — one gate shared by both; capacity is decided in exactly one place).
- **Core MCP server** (`go/cmd/agentd/mcpserver.go`) — JSON-RPC over POST `/mcp`, outside the JWT middleware, authenticated by session token. **Do not write a second one**: adding tools is one `mcp_*.go` file plus one `srv.register(...)` line in `main.go`.
- **Config log** (`go/agentdb/config_events.go`) — every configuration mutation writes through `WithConfigEvent` in the same transaction, and `TestMutationsAreLogged` fails if any store mutation method can bypass it. Emission of `config.changed` is a post-commit hook.

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
   registry-agnostic build+push → **GCP (priority)** → automation. Phase 4 is **done and verified
   against the live project** (`webkit-servers` / `europe-west1`, repo `agent-orange`, bucket
   `webkit-servers-agent-orange`): GCS `BlobStore` in `extension/gcsblob`, ADC registry auth in
   `imageregistry/auth`, selection in `cmd/agentd/backends.go`, with artifact→bucket and
   snapshot push+pull→Artifact Registry both confirmed. Phases 2, 3 and 5 remain, plus two loose
   ends in Phase 1 (the `examples/standalone` DinD run and de-Platinum naming).

## Deeper context

- `docs/01-architecture.md` — what it is and how it fits together.
- `docs/15-standalone-stack.md`, `README-stack.md` — running it.
- `docs/18-workers-memory-events.md` — operating the product layer: project settings, workers,
  triggers, memory and briefings, the core tools, images/skills, the config log, the env vars,
  and the current known limitations.
- `installations/README.md` — installations / image layering (derived-image tree, overlay model, `imagetree`).
- `docs/product/17-product-spec.md` — the authoritative product spec entry point (goal, atoms,
  principles, non-goals); component designs incl. the work-plan checklist in `docs/product/`.
  **`docs/product/06-work-plan.md`'s Discovered Issues Log is the best record of what was
  actually built and what surprised us** — read it before assuming anything about the layer.
- `MIGRATION.md` — current status + the registry/GCP roadmap.
