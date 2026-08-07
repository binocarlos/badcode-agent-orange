# 15 — The standalone stack (`agentd`)

`agentd` is a pre-built host: the agentkit library + reference adapters + `httpapi`
+ a folded-in model proxy, assembled into one runnable binary and a docker-compose
stack. Run it when you want agent sessions over HTTP without writing a host or
managing Docker.

`agentd` also hosts the **product layer** — projects, workers, memory, subscriptions,
schedules, images, skills and the config log. **This document covers running the stack:
which components `agentd` wires, which environment variables it reads, and how it fails.
[`18-workers-memory-events.md`](18-workers-memory-events.md) covers operating what runs on
it** — project settings, workers, triggers, memory, the core tools and the config log.
Where a topic touches both, the mechanism is described here and the operating rules there.

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
  blank for dev-open mode (local demo only). A token carrying a `sid` claim is a
  *session* token, a different credential class signed with a different key — the
  API refuses it, so do not mint app tokens with `sid`.
- **Streaming.** The app's API server opens the agentd SSE stream and relays it to
  its own frontend. Keep `agentd` private.

## Model routing

`agentd` injects `ANTHROPIC_BASE_URL=<self>/agent-proxy` and a dummy key into every
session container (the same `Policy.SessionEnv` seam Platinum uses). The real key
lives only in `agentd`'s env and is injected upstream by `/agent-proxy` — it never
enters a container. With no key, `/agent-proxy` serves canned mock responses.

Point at a non-Anthropic upstream with `ANTHROPIC_UPSTREAM_URL` — one of the two variables
compose does not forward, so it needs its own `environment:` line (below).

## Customize the agent image (base image + plugins)

The Runner launches `BASE_IMAGE` per session. Build your own on top of
`agentkit-sandbox` and add tools via the app-image contract
(`/app/product-plugins`, `/workspace/lib`, `/workspace/.claude`,
`/workspace/CLAUDE.md`) — see `docs/14-host-adapters.md`. Per-app **UI** plugins
register against `@agentkit/chat-ui`'s render-plugin seam in the app's frontend.

Selection order, as the Runner applies it (`resolveLaunchImage`, `go/runner.go`):

    explicit Image  >  worker image pointer  >  CustomImageID  >
    project_settings.base_image  >  BASE_IMAGE (stack-wide, reaching agentd as AGENTKIT_IMAGE)

The two product-layer links are **bound at launch, every launch**. A worker's `image` pointer
and `project_settings.base_image` both resolve through the §13 image catalogue, and they fail
in deliberately different ways — a worker pointer at an image nobody burned fails the job,
while a `base_image` the catalogue does not hold is used verbatim as a registry reference (which
is what keeps `ghcr.io/acme/base:v1` and the stack's own `agentkit-sandbox:dev` working). The
full table, and what the failures say, is in
[`18-workers-memory-events.md`](18-workers-memory-events.md) § "Images and skills".

Two consequences for anyone running the stack. `project_settings.base_image` applies to **every**
session in the project, not only to worker jobs — a project whose `base_image` names a catalogue
image that cannot be served fails its sessions rather than quietly using `BASE_IMAGE`. And
`CustomImageID` now ranks *below* a worker pointer, which reverses the older httpapi contract
that the caller's custom image always wins; `agentd` wires no `Deps.CustomImages`, so that link
is inert here in any case.

## The store — and why the product layer needs Postgres

`agentd` picks its store from `DATABASE_URL`: set → Postgres (`agentdb`, self-migrating);
unset → a local sqlite file, kept for zero-dependency demos. The compose stack always
sets it, and the image is `pgvector/pgvector:pg16` so `CREATE EXTENSION vector` works
without an image swap.

Migrations run at boot and are **safe under concurrent boots** (since 2026-07-26). `agentdb`
takes a Postgres session-level advisory lock — key `fnv64a("agentdb:migrations")` — on one pinned
connection, and holds it across the whole read-and-apply: creating the tracking table, reading the
applied set, and running what is missing. Replicas starting together therefore serialise; the ones
that wait re-read the applied set on the far side of the lock, find their work already done, and
boot having applied nothing. A replica that waits logs `another process is migrating this
database`, and gives up after 5 minutes rather than hanging silently. The lock is released on every
exit path, including a migration that fails mid-way — a leaked one would wedge every later boot.

Before that change the check-then-insert had no lock at all, and two processes booting together
would both decide the same migration was pending: the loser died on
`agentdb_migrations_pkey`, or earlier on `pg_type_typname_nsp_index` from two concurrent
`CREATE TABLE IF NOT EXISTS`. The same race also hit `go test ./agentdb/... ./cmd/agentd/...`,
which runs one binary per package in parallel against one database.

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
| Artifact metadata | in-process map (`extension/blobartifacts`) — **lost on restart**, bytes orphaned in the blob store. With `DATABASE_URL` it is the `agent_artifacts` table and survives ([06](06-artifacts.md)) |

`agentd` logs each of these at boot (`no DATABASE_URL — event routing, schedules and
request_human_attention are unavailable`; `core mcp DISABLED (no DATABASE_URL)`;
`artifacts=in-process index — NOT durable`), but nothing fails at *use* time — a stack accidentally on sqlite looks healthy and quietly does nothing.
The sqlite store also drops the product columns on the sessions table (`mcp_servers` is
carried, `worker` and `composed_prompt` are not), which is the mechanical reason the fallback
is not a supported product-layer configuration.

**The memory system (spec §7) requires Postgres.** Every memory store method —
`CreateMemory`, `GetMemory`, `NewestMemory` (the briefing lookup) and `SearchMemories` —
returns `ErrMemoryRequiresPostgres` against a non-Postgres dialect. Memory leans on jsonb
label selectors, `tsvector` ranking and (optionally) pgvector, and a keyword-ish sqlite
imitation would answer searches with plausible but incomplete results. A store that silently
forgets is worse than no store, so the fallback refuses loudly instead. If you want memory,
keep `DATABASE_URL` set.

Inside Postgres, two things *do* degrade quietly, and both keep the result shape
identical (§7.6.5):

- **No pgvector** — migration 022 adds `content_embedding` + its hnsw index only when
  the extension is available; without it search drops the semantic leg and ranks on
  keyword + recency.
- **No embedding provider** — `AGENTKIT_EMBEDDING_BACKEND` is `none` by default, so
  memories are stored with a NULL embedding and search is keyword + recency. `mock` selects
  the deterministic offline embedder; a real embedder is host code implementing
  `extension/embedding.Provider`. A typo in the value is a boot error, never a silent fall
  back to `none`. `docker-compose.yml` forwards it, so setting it in `.env` reaches `agentd`
  — it did not until 2026-07-26, and until then the shipped stack always ran `none` whatever
  `.env` said.

## Idle session containers are reclaimed after 30 minutes

A session holds a running container inside DinD, and one host port with it. **Since 2026-07-26
that container is reclaimed once the session has been idle for 30 minutes** — `agentd` sets
`Policy.ArchiveTimeout` from `AGENTKIT_SESSION_IDLE_TIMEOUT` and the Runner's archive loop
snapshots the container and drops it. Until then it set neither of the Runner's GC loops, so a
session held its container from creation until a human deleted it, and the pool below only ever
drained.

**Archiving is not deleting.** The session row, its events and its snapshot handle all survive;
the next message restores the container from the snapshot (`ensureRunning`) and the
conversation continues. A returning user pays one image materialise and sees nothing else. What
is reclaimed is a container, its memory, and the host port.

Two things are deliberately never archived mid-flight: a session with a turn in progress
(however long the model has been thinking) and a session with events still being flushed.

```
[agentd] session gc: containers idle >30m0s are snapshotted and released (the session survives
and resumes from its snapshot on the next message)
```

Set `AGENTKIT_SESSION_IDLE_TIMEOUT` to change it, or to `off` to restore the old behaviour.
Below `1m` or above `720h` is a boot error, as is a bare number (`30` is not a duration) — the
sweep runs once a minute, so a shorter setting promises a precision it does not have.

The ceiling is still exact while sessions are live: `agentd` leases each live session one
host port from a pool that defaults to `30001-30100`, so the **101st concurrent
session cannot start**, and neither can any after it until one is released — by a delete, or
now by the archive sweep.

The pool is configurable — `AGENTKIT_PORT_RANGE_START` / `AGENTKIT_PORT_RANGE_END`, defaulting
to `30001` and `30100` so an existing deployment is unchanged (`cmd/agentd/portrange.go`).
`agentd` prints the effective pool at boot:

```
[agentd] session port pool=30001-30100 (100 concurrent sessions max on this host; one live session holds one port until it is deleted or reclaimed for idleness)
```

Set **both or neither**: one alone is a boot error rather than an operator's number silently
paired with a default from the other end. So are a non-numeric bound, a start above the end, a
port outside `1024-65535`, and a range wider than 10000 ports — a pool that cannot work stops
`agentd` at boot instead of starting and failing every session.

Narrowing it is how the exhaustion path below is *exercised* rather than merely read: a test
stack setting `AGENTKIT_PORT_RANGE_START=40000` / `AGENTKIT_PORT_RANGE_END=40002` reaches the
real error, at a real caller, on the fourth session instead of the 101st.

One firing schedule can drain the pool on its own. Fifty-three abandoned `* * * * *` rows left
by dead test runs held every port between them; before blaming the code, check
`select count(*) from schedules where enabled`. Schedules that repeatedly fail to *provision*
now retire themselves after five consecutive firings — see
[`18-workers-memory-events.md`](18-workers-memory-events.md) § "Wire what wakes a worker".

That ceiling is a legitimate limit; failing to recognise it is not. It used to surface as
"session has no running instance and no snapshot — session must be re-created", which describes
a *lost session* and invites a re-create that fails identically — it was misdiagnosed twice, once
as a Docker container limit and once as an image-resolution bug. It now says what is actually
true (the count and the range are the *configured* pool, so the message matches the boot log):

```
cannot start session "<id>" on this host: execution environment is at capacity: the host port
pool is exhausted — all 100 ports in 30001-30100 are leased to live sessions, and a session
holds its port until it is deleted, so every further session on this host will fail the same
way until one is released (a host capacity limit, not a lost or broken session)
```

The error wraps `execenv.ErrNoCapacity`, so a host can map it to a 503 with `errors.Is` rather
than string-matching. `agentd` also logs `[dind] WARNING: host port pool nearly exhausted` on
every provision once fewer than ten ports remain — watch for that before the cliff.

Delete sessions you have finished with (`DELETE /agent/session/{id}`; the e2e suite does this
in teardown). `./e2e/run-stack-e2e.sh clean` clears leftovers, and **restarts `agentd`**
afterwards — that restart is not optional. Removing containers out from under a running
`agentd` leaves its placement state naming instances that no longer exist, and it then
refuses to provision *any* new session, including brand-new ones, until restarted. The script
does the restart for you; if you clear containers by hand, do it yourself — and wait for the
removals to settle first: `docker rm -f` returns before the daemon has finished, and an
`agentd` that boots while a removal is still in flight dies on `removal … already in progress`.

Deleting is still the right thing to do with a session you are finished with: archiving keeps
the row, the events and a snapshot blob for ever.

### Snapshot images are reaped every 6 hours

`project_settings.snapshot_ttl_days` stamps an `expires_at` on every image burned by
`image_create`, and `agentd` now runs the reaper that honours it (`agentkit.SnapshotReaper`,
`go/snapshot_reaper.go`): `Deps.Snapshots` is the Postgres store and
`Policy.SnapshotReapInterval` comes from `AGENTKIT_SNAPSHOT_REAP_INTERVAL`, default **6h**. The
expiry is per project and stamped per row, so the interval is only how often we *look*; a
promise measured in days does not need a minutely sweep, and a pass that finds nothing still
costs queries.

```
[agentd] snapshot TTL reaper: sweeping every 6h0m0s (expiry itself is per project —
project_settings.snapshot_ttl_days, 0 = never)
```

Expired versions lose their **bytes** and keep their **record** as a tombstone (§13.7): history
and the version high-water mark survive, and resolving a reaped version fails with
`ErrCustomImageReaped` rather than pointing at nothing. Bytes are deleted *before* the
tombstone is written, never the reverse — the other order orphans the blob for ever.

One exception, and it is the one that keeps a running deployment running: a version a session
**launched from** inside the project's current `snapshot_ttl_days` window is *deferred*, not
reaped, and the log says which image and how long ago it was used. An image in daily use is
therefore never deleted out from under the worker pinned to it; stop launching from it and it is
reaped on the first pass after the window.

It needs `DATABASE_URL`: the catalogue it sweeps is Postgres-only, so on the SQLite fallback
the loop is not started and the boot log says so. `off` disables it. Session *resume* snapshots
(`agent_sessions.snapshot_handle`, written by the archive loop above) carry no TTL and are not
in the reaper's scope.

### Deleting a session mid-create no longer leaks its port

The pool used to lose ports to something no operator could see. `agentd` answers
`POST /agent/session` with `status: "creating"` and provisions in a **background goroutine**
(the pull can take minutes). Delete the session inside that window and the delete found no
container to destroy — the container had not been created yet — so it arrived seconds later
owned by nobody: no session row, no tracked instance. Nothing reaped it. Not the archive loop
(it iterates sessions), not any cleanup keyed on a row, not any count that trusts the database.
It simply held one of the pool's ports until a human ran `docker ps`.

One prompt delete produced one such orphan — including a test that fails in milliseconds, or a
teardown that deletes what it just created. An e2e run could report "0 ports in use" and have
three orphans behind it, because both the count and the reaper start from the database and the
orphan is precisely the container the database has forgotten.

The Runner now cancels an in-flight create instead: `Destroy` marks the create (it never
*waits* for it — a delete must not block behind a slow image pull), and the create checks that
mark before provisioning, right after the container exists, and again after the harness boots,
destroying what it built and releasing the port. `CreateSession` then returns
`agentkit.ErrSessionDeleted`, which is not a create failure and is not recorded as one.

The predicate is "a delete for **this** session arrived while **this** create was in flight" —
in-process state about a container the runner itself just created. It is deliberately *not*
"a container whose session row is missing": a missing row also describes a restore in progress,
a re-provision, a container belonging to another host in a fleet, and — indistinguishably
through the store interface — a database that is merely erroring. A sweep on that predicate
would delete live work, which is worse than the leak. Two consequences of the narrower choice,
both intentional:

- A container orphaned by an **earlier** run of `agentd` (or by a crash between provisioning and
  tracking) is still not reaped. `./e2e/run-stack-e2e.sh clean` remains the tool for those.
- A host that deletes a session row **without** calling `Runner.Destroy` still leaks. Deleting
  through the API always calls it.

## A session that failed to start says why

Capacity was only one cause. `agentd` provisions in a **background goroutine** (the POST returns
`status: "creating"` immediately so the UI can render a download bar), and that goroutine used to
keep nothing from a failure but `status = "error"` — the reason was neither logged nor stored. So
every non-capacity failure also came back as "no running instance and no snapshot — session must
be re-created", and the good diagnostics written for those causes reached nobody.

The reason is now recorded on the session row (`agent_sessions.create_error`, migration 032),
logged by `agentd`, and returned three ways:

- on the **next message**, over SSE:

  ```
  session "<id>" never started: ensure image present: project_settings.base_image =
  "definitely-not-an-image:v9" (project "acme") names no image in the §13 catalogue, so it was
  used as a literal registry reference and that reference failed: <docker error> — fix the
  cause, then create the session again
  ```

- on `GET /agent/session/{id}`, as `create_error` beside `status` — a UI showing `error` with no
  explanation is the same defect one layer up;
- in `agentd`'s log, as `session <id> failed to start: …`.

Two rules keep the stored reason honest:

- **Only permanent facts about the session's configuration are stored.** A capacity failure
  (`execenv.ErrNoCapacity`) is a fact about the *host* at one instant — it stops being true the
  moment a session is deleted — so it is never written to the row, and never overwrites a reason
  already there.
- **Live capacity is asked first and wins.** When the host is full, the message above is the
  capacity message, wrapping `errors.Is(err, execenv.ErrNoCapacity)`; the stored reason is
  consulted only once the environment says it has room. A session with a broken `base_image` on a
  saturated host is told about the saturation first, and about the `base_image` as soon as a port
  frees up — both true, in the order that can be acted on.

A successful create clears the reason, so it can never outlive its cause. A session with no
instance, no snapshot, no stored reason, on a host with room is genuinely lost, and only that
case still says **"must be re-created"**.

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

## Stack environment variables

`.env.example` and `docker-compose.yml` carry the full commentary. The product-layer subset
(`AGENTKIT_MCP_ENV`, `AGENTKIT_PUBLIC_BASE_URL`, `AGENTKIT_EMBEDDING_BACKEND`,
`AGENTKIT_PORT_RANGE_*`, `AGENTKIT_SESSION_IDLE_TIMEOUT`, `AGENTKIT_SNAPSHOT_REAP_INTERVAL`,
`AGENTKIT_MOCK_MODEL_SCRIPT*`) is tabulated in
[`18-workers-memory-events.md`](18-workers-memory-events.md) § "Environment variables". The
stack-level ones:

| Variable | Effect |
| --- | --- |
| `DATABASE_URL` | Postgres → the product layer exists; unset → sqlite and it does not (above). Compose always sets it |
| `BASE_IMAGE` | the stack-wide default launch image, reaching `agentd` as `AGENTKIT_IMAGE` (default `agentkit-sandbox:dev`) |
| `AGENTKIT_JWT_SECRET` | HS256 secret `agentd` verifies incoming API JWTs with. Blank = dev-open mode, which also registers `/dev/token`. Required as soon as any login mode is on |
| `AGENTKIT_SESSION_JWT_SECRET` | **Optional.** The signing key for per-session container tokens (`SESSION_TOKEN`, verified by the core MCP server). Unset — the normal case — it is **derived** from `AGENTKIT_JWT_SECRET` by HMAC-SHA256, so a container's token is not a project credential and no deployment has to configure anything. Set it only to roll the session key independently of the API secret; setting it to the *same* value as `AGENTKIT_JWT_SECRET` re-opens that hole and `agentd` warns at boot |
| `GOOGLE_CLIENT_ID` / `AGENTKIT_TEST_LOGIN` | enable Google login (`POST /auth/google`) and password login (`POST /auth/password`). Either one requires `AGENTKIT_JWT_SECRET` and a project map (`AGENTKIT_PROJECT_MAP` / `_FILE`), and replaces `/dev/token` with `POST /auth/project-token`. `AGENTKIT_TEST_LOGIN` grants **all** projects — test/dev only |
| `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` | model credentials. Both blank = mock model. Both set = the API key wins (proxy mode) |
| `AGENTKIT_SELF_URL` | how a session container nested in DinD reaches `agentd` (`http://172.17.0.1:8099`). **Not** a browser-reachable URL — see permalinks below |

**Two variables `agentd` reads that `docker-compose.yml` does not forward.** Setting either in
`.env` does nothing in the compose stack; each needs its own `environment:` line on the `agentd`
service, exactly like an `AGENTKIT_MCP_ENV` credential:

- **`TZ`** — the zone every cron expression is evaluated in (`agentd` logs it at boot as
  `zone=…`). Unset means UTC, which is what the shipped stack always runs. Schedules are
  stack-local, so this is the one knob that changes when every schedule in every project fires.
- **`ANTHROPIC_UPSTREAM_URL`** — the model endpoint `/agent-proxy` forwards to, default
  `https://api.anthropic.com`.

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
