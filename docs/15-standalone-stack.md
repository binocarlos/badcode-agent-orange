# 15 — The standalone stack (`agentd`)

`agentd` is a pre-built host: the agentkit library + reference adapters + `httpapi`
+ a folded-in model proxy, assembled into one runnable binary and a docker-compose
stack. Run it when you want agent sessions over HTTP without writing a host or
managing Docker.

`agentd` also hosts the **product layer** — projects, workers, memory, subscriptions,
schedules, images, skills and the config log. This document covers running the stack;
`docs/18-workers-memory-events.md` covers operating what runs on it.

## Library vs standalone — pick ONE

| | Library integration | Standalone stack |
|---|---|---|
| Who is the host? | your own Go server imports `agentkit` | `agentd` |
| Do you run `agentd`? | **No** | **Yes** |
| Adapters | you write them | shipped reference adapters |
| Docker/DinD | you manage it | the stack manages it |
| Model proxy | you mount `modelproxy.Handler` or write your own | `agentd` mounts it at `/agent-proxy` |

`agentd` is the *alternative* to integrating as a library — not a component of it.
Platinum (goapi) is the proof you can build a host without `agentd`.

## How apps integrate (the deployment topology)

    app frontend  ──vends──▶  @agentkit/chat-ui (NPM) + app render plugins
        │  HTTP/SSE (same origin)
        ▼
    app API server   — issues a JWT agentd trusts; picks an image; PROXIES the SSE stream
        │  HTTP/SSE   (agentd is PRIVATE — never browser-exposed)
        ▼
    agentd stack     — owns all Docker/infra; shared by all apps

- **Auth = JWT delegation.** Apps mint an HS256 JWT (claims `email`, `customer`,
  `job`) signed with `AGENTKIT_JWT_SECRET`; `agentd` only verifies. Leave the secret
  blank for dev-open mode (local demo only).
- **Streaming.** The app's API server opens the agentd SSE stream and relays it to
  its own frontend. Keep `agentd` private.

## Model routing

`agentd` injects `ANTHROPIC_BASE_URL=<self>/agent-proxy` and a dummy key into every
session container (the same `Policy.SessionEnv` seam Platinum uses). The real key
lives only in `agentd`'s env and is injected upstream by `/agent-proxy` — it never
enters a container. With no key, `/agent-proxy` serves canned mock responses.

Point at a non-Anthropic upstream with `ANTHROPIC_UPSTREAM_URL`.

## Customize the agent image (base image + plugins)

The Runner launches `BASE_IMAGE` per session. Build your own on top of
`agentkit-sandbox` and add tools via the app-image contract
(`/app/product-plugins`, `/workspace/lib`, `/workspace/.claude`,
`/workspace/CLAUDE.md`) — see `docs/14-host-adapters.md`. Per-app **UI** plugins
register against `@agentkit/chat-ui`'s render-plugin seam in the app's frontend.

Selection order, as the Runner applies it: `Image` > `CustomImageID` on the create-session
request > `BASE_IMAGE` (stack-wide, reaching `agentd` as `AGENTKIT_IMAGE`). Worker jobs
additionally compose an image from `project_settings.base_image` and pass it as the explicit
`Image`, so a project can override the stack default for its workers without touching `.env`.

> The named-image layer of the product spec (§13 — `image_create`, `name:version`, a worker's
> `image` pointer) is built at the store and tool level but **not yet bound at launch**: a
> worker with `image` set fails its job loudly rather than launching from that image
> (work-plan item I4). Use `project_settings.base_image` until it lands. See
> `docs/18-workers-memory-events.md` § "Known limitations".

## The store — and why the product layer needs Postgres

`agentd` picks its store from `DATABASE_URL`: set → Postgres (`agentdb`, self-migrating);
unset → a local sqlite file, kept for zero-dependency demos. The compose stack always
sets it, and the image is `pgvector/pgvector:pg16` so `CREATE EXTENSION vector` works
without an image swap.

**`DATABASE_URL` is the switch for the entire product layer**, not just for memory.
Everything in `docs/18-workers-memory-events.md` lives in the product-layer tables, so on
the sqlite fallback `agentd` wires none of it:

| Component | Without `DATABASE_URL` |
| --- | --- |
| Event router | not started — events are never routed, subscriptions never fire |
| Scheduler | not started — schedules never fire |
| Core MCP server (`memory_*`, `worker_*`, `image_*`, `skill_*`, management, `config_history`) | **not mounted at all** |
| `SessionContextProvider` | not installed — project base image, project prompt and project/worker MCP defaults silently do not apply |
| `worker.finished` / `worker.failed` emitters | left nil — no internal events |
| `POST /agent/attention` | not mounted (404) |

`agentd` logs each of these at boot (`no DATABASE_URL — event routing, schedules and
request_human_attention are unavailable`; `core mcp DISABLED (no DATABASE_URL)`), but nothing
fails at *use* time — a stack accidentally on sqlite looks healthy and quietly does nothing.
The sqlite store also drops the product columns on the sessions table (`mcp_servers` is
carried, `worker` and `composed_prompt` are not), which is the mechanical reason the fallback
is not a supported product-layer configuration.

**The memory system (spec §7) requires Postgres.** On sqlite, `CreateMemory` /
`GetMemory` / `SearchMemories` all fail with `ErrMemoryRequiresPostgres` — memory
leans on jsonb label selectors, `tsvector` ranking and (optionally) pgvector, and a
keyword-ish sqlite imitation would answer searches with plausible but incomplete
results. A store that silently forgets is worse than no store, so the sqlite fallback
refuses loudly instead. If you want memory, keep `DATABASE_URL` set.

Inside Postgres, two things *do* degrade quietly, and both keep the result shape
identical (§7.6.5):

- **No pgvector** — migration 022 adds `content_embedding` + its hnsw index only when
  the extension is available; without it search drops the semantic leg and ranks on
  keyword + recency.
- **No embedding provider** — `AGENTKIT_EMBEDDING_BACKEND` is `none` by default, so
  memories are stored with a NULL embedding and search is keyword + recency. `mock` selects
  the deterministic offline embedder; a real embedder is host code implementing
  `extension/embedding.Provider`. A typo in the value is a boot error, never a silent fall
  back to `none`. **`docker-compose.yml` does not currently forward this variable**, so the
  shipped stack always runs `none` — setting it means adding one `environment:` line to the
  `agentd` service.

## Session containers are not reaped on a timer

A session holds a running container inside DinD until the **session is deleted**. Nothing
expires them, so they accumulate: roughly a hundred was enough, during one e2e run, to make
`agentd` stop being able to provision anything, with `image_create` reporting "session has no
running instance and no snapshot". That reads exactly like a product bug and is not one.

Delete sessions you have finished with (`DELETE /agent/session/{id}`; the e2e suite does this
in teardown). `./e2e/run-stack-e2e.sh clean` clears leftovers, and **restarts `agentd`**
afterwards — that restart is not optional. Removing containers out from under a running
`agentd` leaves its placement state naming instances that no longer exist, and it then
refuses to provision *any* new session, including brand-new ones, until restarted. The script
does the restart for you; if you clear containers by hand, do it yourself.

Whether long-lived idle sessions should reap their containers is an open product question.

## Session permalinks

`AGENTKIT_PUBLIC_BASE_URL` is the externally reachable base of the **web UI** — not
`AGENTKIT_SELF_URL`, which is a DinD bridge address only containers can reach. Permalinks are
minted as `<base>/p/<project>/s/<session>` and travel a long way from the browser: into memory
search results, config-log entries, image and skill provenance, and
`request_human_attention` webhooks. It must be a URL the recipient can actually open. Default
`http://localhost:8080`; it must be an absolute http(s) URL with no query or fragment, or
`agentd` refuses to boot.

## MCP credentials into sessions

MCP configuration stored in the database only ever *names* a variable
(`{"env": {"GMAIL_API_KEY": "${GMAIL_API_KEY}"}}`), so nothing secret is ever persisted or
displayed. `AGENTKIT_MCP_ENV` is the comma-separated **allowlist of variable names** `agentd`
forwards into every session container, where the sandbox resolves the references at MCP-server
spawn time. It is an allowlist by design: naming one of `agentd`'s own secrets
(`AGENTKIT_JWT_SECRET`, `ANTHROPIC_API_KEY`, `DATABASE_URL`, …) is refused at boot. A listed
name that is unset is warned about and not forwarded — the MCP server referencing it then
fails loudly at spawn rather than authenticating with an empty credential.

Compose cannot forward a dynamic set of names, so each credential needs **two** entries: its
name in `AGENTKIT_MCP_ENV`, and its own `environment:` line on the `agentd` service.

One exception to "no `agentd` secret reaches a session": in subscription mode
`CLAUDE_CODE_OAUTH_TOKEN` legitimately does, because the in-image CLI authenticates with it.

## Storage backends (local default, or Google Cloud)

`agentd` selects its blob and image-registry backends from env (see
`cmd/agentd/backends.go`). Defaults reproduce the offline local stack:

| Concern | Default | Google Cloud |
| --- | --- | --- |
| Artifact bytes + snapshots | filesystem (`filesblob`) under `AGENTKIT_DATA` | `AGENTKIT_BLOB_BACKEND=gcs` + `GCS_BUCKET` → `gcsblob` |
| Session-snapshot images | blob-archive tarballs in the BlobStore | `AGENTKIT_REGISTRY_BACKEND=ociregistry` → Artifact Registry |

GCP example (matches `.env.example`):

```sh
AGENTKIT_BLOB_BACKEND=gcs
GCS_BUCKET=webkit-servers-agent-orange

AGENTKIT_REGISTRY_BACKEND=ociregistry
AGENTKIT_REGISTRY_AUTH=gcp            # ADC OAuth2 token (default); or 'basic'
GCP_REGION=europe-west1
GCP_PROJECT=webkit-servers
GCP_AR_REPO=agent-orange             # → europe-west1-docker.pkg.dev/webkit-servers/agent-orange
```

The two choices are independent: you can put blobs in GCS while keeping
blob-archive images, or vice versa.

### Provisioning (idempotent)

`deploy/gcp/setup.sh` provisions everything a fresh project needs — enables the
APIs, creates the Artifact Registry repo + GCS bucket if missing, creates a
runtime **service account**, and grants it least-privilege IAM
(`storage.objectAdmin` on the bucket, `artifactregistry.writer` on the repo). It
is safe to re-run; every step checks first.

```sh
deploy/gcp/setup.sh                      # SA + IAM + resources (no secret emitted)
deploy/gcp/setup.sh --emit-key ./key.json   # also write a SA key for local/CI
```

### Giving `agentd` credentials (ADC)

`agentd` authenticates with **Application Default Credentials**: it uses one
credential both to reach the GCS bucket *and* to mint the short-lived OAuth2
token it forwards to the Docker daemon for Artifact Registry push/pull (registry
auth is client-side — the daemon does the transfer, agentd supplies the token).
Three ways to deliver ADC, in order of preference:

1. **Workload identity** (GKE / Cloud Run / GCE) — nothing to mount; the metadata
   server supplies tokens automatically.
2. **Your own gcloud login** (local dev) — run `gcloud auth application-default
   login` once, then mount `~/.config/gcloud` into the container (commented in
   `docker-compose.yml`). No service-account key needed.
3. **Service-account key** (CI / no metadata server) — `setup.sh --emit-key`, then
   set `GOOGLE_APPLICATION_CREDENTIALS` and mount the key (commented in
   `docker-compose.yml`).

There is no `gcloud auth configure-docker` step: agentd talks to the daemon via
the Docker API and supplies the token itself, so the CLI credential helper would
not be consulted.
