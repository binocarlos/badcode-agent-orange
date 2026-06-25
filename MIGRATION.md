# Agent Orange — Migration Plan

**What this is.** Agent Orange is founded on **agentkit**, the in-house Go agent runtime previously
living in the Platinum monorepo (`/home/kai/projects/bayesprice/Platinum/agent-library`,
module `github.com/binocarlos/badcode-agent-orange`). This document tracks turning that import into a
standalone Agent Orange that can **build installation images and push them to a variety of image
registries — Google Cloud Artifact Registry first.**

**Provenance / IP note.** agentkit is bayesprice-owned. This repo is for **private** use right now,
which is fine. A future *public* release (the original "Agent Orange as art-object" idea) would
require resolving licensing/ownership first — parked until then.

---

## Phase 0 — Foundation imported ✅ (done)

- Copied agentkit (`agent-library/`) into this repo root. **`cd go && go build ./...` passes** (Go 1.24).
- Installation definitions copied to `installations/` (`platinum-base`, `core-v1`, `core-v2`).
- Platinum host-side image pipeline copied to `migration-reference/` (to port, not to keep):
  `goapi-installations/` (resolution logic), `build-installations-manifest.py`,
  `build-sandbox-agentkit.sh`, `stack`, `agent_v2.go` (ImageResolver), `agent_save_session_image.go`,
  `agentdeps.go`, and the ACR auth design doc.
- Fresh local git repo (no remote).

**What is NOT yet working:** the TS in-image agent (`sandbox/`) and React UI (`web/`) have not had
`npm install`/build run; the installation image-build path is Platinum-host code (not yet ported);
registry auth is basic-only (no GCP).

---

## The image lifecycle (how it works today, from the source)

```
DEFINE        installations/<name>/{installation.json, Dockerfile, overlay/}
BUILD         docker build -f Dockerfile --build-arg BASE_IMAGE=<parent> -t <ref> .   (HOST does this; agent-library Build() is stubbed)
PUSH          imageregistry.Persist() -> ociregistry: docker push   |   blobarchive: docker save->gzip->blob
RESOLVE       installation name -> ImageResolver -> <registry>@<digest> or <registry>/...:dev
LAUNCH        Runner.Provision({Image: ref}) -> ExecutionEnvironment(DinD) -> Registry.EnsurePresent (pull) -> docker run
SNAPSHOT      Runner.Snapshot()->docker commit -> Registry.Persist() -> CustomImage row (lineage tracked)
```

Key code (in this repo):
- Registry interface: `go/imageregistry/registry.go` (`EnsurePresent`, `Build`, `Resolve`, `Persist`, `Materialize`, `Remove`, `Capabilities`).
- OCI adapter (push/pull): `go/imageregistry/ociregistry/ociregistry.go` — **basic auth only**, `Build()` stubbed.
- Blob adapter (snapshots): `go/imageregistry/blobarchive/blobarchive.go` — full archive (diff fast-path is a TODO).
- Custom-image catalog: `go/agentdb/customimages.go`.
- Image tree / dependency order: `go/imagetree/`, `go/cmd/imagetree/`.
- Installation definitions: `installations/`.
- Resolver + manifest (reference to port): `migration-reference/goapi-installations/`, `migration-reference/build-installations-manifest.py`.

---

## Phase 1 — Standalone-ify the runtime

Goal: this repo builds and runs end-to-end with no Platinum coupling.

1. **Re-module.** Rename Go module `github.com/binocarlos/badcode-agent-orange` → the Agent Orange path
   (decide: e.g. `github.com/badcode/agent-orange` or keep `agentkit`). Update `go/go.mod` + all
   import prefixes. Re-run `go build ./... && go vet ./...`.
2. **Build the in-image agent + UI.** `npm install` + build in `sandbox/` (TS in-image agent) and
   `web/` (React UI); run their test suites. Confirm `sandbox/Dockerfile` builds `agent-orange-sandbox`.
3. **Stand up the example host.** Run `go/examples/standalone/main.go` against real DinD with the
   mock model proxy (per `examples/README.md`) — proves provision → message → stream → snapshot.
4. **Liftability gate.** Port the CI check that forbids host-app imports (it already exists in
   `.github/`); make it the Agent Orange invariant.
5. **De-Platinum naming.** Genericize `platinum-*` identifiers in docs/config that are cosmetic
   (leave functional installation work to Phase 2).

---

## Phase 2 — Genericize installation image-building

Goal: keep the ability to build custom base images per installation, without Platinum's product specifics.

1. **Generic base.** Recast `installations/platinum-base` as `installations/agent-orange-base`:
   keep the language/runtime stack + skill-baking mechanism; make the Platinum-specific layers
   (`pt` CLI, `product-plugins`, `webviz`) **optional overlays**, not hardcoded.
2. **Installation template.** Document the contract (`installation.json` + `Dockerfile` +
   `overlay/`) as the reusable shape; keep `core-v1`/`core-v2` as worked examples or replace with
   a single `example` installation.
3. **Port the build orchestrator.** Bring the dependency-ordered build (`imagetree` already in `go/`)
   + manifest generation (`migration-reference/build-installations-manifest.py`) into a first-class
   Agent Orange command (see Phase 3) instead of the Platinum `stack` bash.

---

## Phase 3 — Registry-agnostic build + push (the core ask)

Goal: build an installation image and push it to **any** OCI registry, selected by config.

1. **Auth abstraction (the real blocker).** `ociregistry` currently hardcodes basic
   username/password. Introduce a `RegistryAuth` seam that yields credentials per push/pull, with
   implementations:
   - `StaticBasic{user, pass}` — today's behaviour (ACR, Docker Hub, generic).
   - `BearerToken{tokenProvider}` — short-lived tokens (this is the GCP path; see Phase 4).
   - `DockerConfigHelper` — delegate to the host's `~/.docker/config.json` credential helpers.
   File: `go/imageregistry/ociregistry/ociregistry.go` (config + `New()`), new `go/imageregistry/auth/`.
2. **Implement `Build()`.** Make `localbuild.Build()` actually run `docker build` from a `BuildSpec`
   (BaseImage + Dockerfile/overlays), tag, and hand off to `Persist()` for push. (Currently stubbed.)
   File: `go/imageregistry/` (localbuild), wired through `registry.go`'s `Build`.
3. **One CLI to build+push.** A `cmd/ao` (or extend `cmd/imagetree`) command:
   `ao installations build <name> --registry <url> [--push]` → resolves ancestry → docker build →
   tag `<registry>/<name>:<tag>` → push via the chosen `RegistryAuth` → record digest in the manifest.
4. **Registry selection by config.** Generalize the `V2ImageRegistry`/`V2RegistryURL` switch
   (`migration-reference/agentdeps.go`, `goapi-installations/installations.go`) so a registry +
   auth method is chosen from config/env, not hardcoded to ACR. Support the local `registry:5000`
   dev path unchanged.
5. **Verify** push→resolve→pull→launch against a generic OCI registry (local `registry:5000` first).

---

## Phase 4 — Google Cloud (priority) 🎯

Two independent pieces. Note the runtime already has everything they plug into: `go/artifacts/`
(full `ArtifactStore` — Save/Load/List/MarkLost/CaptureFolder, dir artifacts, mock, tests) and the
`extension.BlobStore`/`BlobStoreFactory` seam (`go/extension/extension.go`). Only a filesystem impl
(`go/extension/filesblob`) ships; there is **no cloud backend in the module** (Azure lived in the
Platinum host and was not copied). go.mod has no cloud deps yet.

### 4a. Artifacts + snapshots → Google Cloud Storage ✅ (impl done; wiring pending)

**One GCS-backed `BlobStore` serves BOTH** the `ArtifactStore` (artifact bytes) and `blobarchive`
(snapshot tarballs) — they both write through `extension.BlobStore`. Implemented once.

1. ✅ `go/extension/gcsblob/` — `extension.BlobStore` + `BlobStoreFactory` over Google Cloud Storage
   (`cloud.google.com/go/storage`), mirroring `extension/filesblob`. `Config{Bucket, Prefix}`;
   `ForSession` → `session/<id>` prefix, `Global(ns)` → `<ns>` prefix. `List` returns store-relative
   keys (round-trips through Read). Pure key-logic unit tests + an env-gated integration test
   (`//go:build gcs`, skips unless `GCS_TEST_BUCKET` set; works against a real bucket or
   `fake-gcs-server` via `STORAGE_EMULATOR_HOST`).
2. ✅ Auth via Application Default Credentials only — workload identity / gcloud / SA key via
   `GOOGLE_APPLICATION_CREDENTIALS`. No credentials configured in-package; tests inject
   `option.ClientOption`. (Adding the SDK raised the module's Go floor to **1.25**, via
   `google.golang.org/api`; CI `go-version` bumped 1.22 → 1.25.)
3. ✅ Wired in `cmd/agentd` (config-driven, `cmd/agentd/backends.go`): `AGENTKIT_BLOB_BACKEND=gcs`
   + `GCS_BUCKET`/`GCS_PREFIX` selects `gcsblob`; the artifact store is now the generic
   `extension/blobartifacts` (works over any `extension.BlobStore`; `filesblob.NewArtifactStore`
   delegates to it). Defaults preserve the local fs stack. Compose + `.env.example` + `docs/15`
   updated; agentd Dockerfile bumped to go1.25. ✅ Verified end-to-end (2026-06-25): the live stack
   (`blobs=gcs`) uploaded an artifact → object present in `gs://webkit-servers-agent-orange` and read
   back byte-identical.

### 4b. Images → Artifact Registry ✅ (auth seam done; end-to-end pending)

1. ✅ **Auth provider seam.** New `go/imageregistry/auth` package: `auth.Provider` returns
   `Credentials{Username, Password}` per push/pull (no longer captured once), with `auth.Static` for
   basic auth and `auth.GCP(ctx)` for Artifact Registry. `auth.GCP` uses ADC
   (`google.FindDefaultCredentials`, cloud-platform scope): username `oauth2accesstoken`, password a
   short-lived access token that the oauth2 TokenSource caches and refreshes before expiry.
   `ociregistry.Config` gained an `Auth auth.Provider` field; when nil it falls back to
   Username/Password static (backward-compatible — the local registry:2 path is unchanged).
   `EnsurePresent`/`Persist`/`Materialize` now resolve auth dynamically per call.
2. ✅ **Registry URL shape + host wiring.** `cmd/agentd/backends.go`: `AGENTKIT_REGISTRY_BACKEND=ociregistry`
   + `AGENTKIT_REGISTRY_AUTH=gcp` + `GCP_REGION`/`GCP_PROJECT`/`GCP_AR_REPO` (or an explicit
   `OCI_REGISTRY`) builds `ociregistry.Config{Registry, Auth: auth.GCP}`. URL shape
   `<region>-docker.pkg.dev/<project>/<repo>`.
3. ✅ **End-to-end (2026-06-25):** the live stack (`registry=ociregistry`, `auth=gcp`) snapshotted a
   session → image pushed to `europe-west1-docker.pkg.dev/webkit-servers/agent-orange/<sid>:latest`
   (confirmed via `gcloud artifacts docker images list`). Note: AR *pull* (Materialize) short-circuits
   to the local image when present, so it wasn't force-exercised; it uses the same proven `auth.GCP`
   credential as the push.

---

## Phase 5 — Automation & hardening (later)

- CI job: build + push installation images, record digests, on change.
- `blobarchive` diff fast-path (currently full-archive only).
- Multi-registry resolution (more than one registry at once).
- Optional remote builder (Cloud Build / Kaniko / buildkit) to replace local `docker build`.

---

## Decisions

- ✅ **Module path** = `github.com/binocarlos/badcode-agent-orange` (done).
- ✅ **Installations** = engine-owned examples (`installations/core`, `installations/example`);
  per-project images live in each project's own repo.
- ✅ **GCP auth method** = Application Default Credentials (ADC / workload identity). gcsblob configures
  no credentials; the host's runtime environment supplies them.
- ✅ **Go floor bumped to 1.25** (GCP SDK requires it via `google.golang.org/api`); CI updated.
- ⬜ **GCP deployment specifics (still needed to wire/verify):** project id, region, **Artifact
  Registry repo name**, **GCS bucket name**.

## Status

- [x] Phase 0 — foundation imported, Go core builds
- [ ] Phase 1 — standalone-ify
- [ ] Phase 2 — genericize installations
- [ ] Phase 3 — registry-agnostic build + push
- [x] Phase 4 — GCP (GCS blobs + Artifact Registry): engine seams + agentd wiring + provisioning,
      verified end-to-end against the live project (artifact→bucket, snapshot→AR)
- [ ] Phase 5 — automation & hardening
