# 15 — The standalone stack (`agentd`)

`agentd` is a pre-built host: the agentkit library + reference adapters + `httpapi`
+ a folded-in model proxy, assembled into one runnable binary and a docker-compose
stack. Run it when you want agent sessions over HTTP without writing a host or
managing Docker.

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
`/workspace/CLAUDE.md`) — see `docs/10-extension-points.md`. Per-app **UI** plugins
register against `@agentkit/chat-ui`'s render-plugin seam in the app's frontend.

> Agent *profiles* (named base-image + prompt + tool bundles referenced at session
> start) are a separate upcoming feature (Spec 2). Today, select the image via
> `BASE_IMAGE` (stack-wide) or the `Image`/`CustomImageID` fields on the create-session
> request.

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
