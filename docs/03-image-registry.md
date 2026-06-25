# 03 — `ImageRegistry`: get images in, get snapshots out

`ExecutionEnvironment` runs an image; `ImageRegistry` is how an image becomes available to run, and
how a snapshot of a running session is preserved and later retrieved. The two are **orthogonal** and
**compose**.

> **Definition.** An `ImageRegistry` makes images present for an engine to run, builds images from a
> context, and moves images in and out as durable artifacts — whether "durable" means a local tar on
> disk, a gzipped diff archive in blob storage, or a tag in a real container registry.

## The contract

```go
package imageregistry

import (
	"context"
	"io"

	"github.com/binocarlos/badcode-agent-orange/execenv"
)

// ImageRegistry provides images to an ExecutionEnvironment and persists images
// produced from it. Implementations range from "build locally, save to a tar"
// (dev / no registry) to "push/pull a real OCI registry" (production).
type ImageRegistry interface {
	// EnsurePresent guarantees the named image is available to the engine that will run
	// it (pull from a remote registry if needed; no-op if locally built/loaded).
	// Provision calls this implicitly via the orchestration core before starting.
	EnsurePresent(ctx context.Context, ref execenv.ImageRef) error

	// Build produces an image from a build context (a Dockerfile + files, or a base +
	// overlay of skill folders). Returns the resulting ref. The local-build adapter runs
	// `docker build`; a remote adapter may hand the context to a builder service.
	Build(ctx context.Context, spec BuildSpec) (execenv.ImageRef, error)

	// Resolve returns an existing ref for a BuildSpec's content hash WITHOUT building
	// (ok=false on cache miss). The Runner calls Resolve first and Build only on a miss, so
	// a returning user with unchanged customisations cache-hits instantly. Tags are
	// content-addressed (see "Image layers & tagging" below).
	Resolve(ctx context.Context, spec BuildSpec) (ref execenv.ImageRef, ok bool, err error)

	// Persist takes an in-engine image ref (typically from ExecutionEnvironment.Snapshot)
	// and stores it durably, returning a Handle that survives process/host restarts.
	// • local-tar adapter:   `docker save` → write the tar somewhere
	// • blob-archive adapter: commit-diff → tar → gzip → blob (today's suspend/restore)
	// • remote adapter:       `docker push` → return the registry reference as the handle
	Persist(ctx context.Context, ref execenv.ImageRef, opts PersistOptions) (Handle, error)

	// Materialize is the inverse of Persist: given a Handle, make the image present for
	// the engine and return a runnable ref (for ExecutionEnvironment.Provision).
	// • local-tar:    `docker load`
	// • blob-archive: download → gunzip → apply diff layers → commit (restoreFromArchive)
	// • remote:       the handle IS the ref; EnsurePresent pulls it
	Materialize(ctx context.Context, h Handle) (execenv.ImageRef, error)

	// Remove discards a persisted image (cleanup after a session is deleted).
	Remove(ctx context.Context, h Handle) error

	// Capabilities lets the orchestration core adapt (e.g. choose ForceFull snapshots
	// when the registry is diff-incapable).
	Capabilities() Capabilities
}

// Handle is an opaque, durable pointer to a persisted image. Its concrete meaning is
// adapter-specific (a file path, a blob path + metadata, a registry reference); the
// orchestration core stores it on the session row via SessionStore and passes it back to
// Materialize on restore. It must be JSON-serialisable.
type Handle struct {
	Kind string            // "local-tar" | "blob-archive" | "registry"
	Ref  string            // path / blobPath / registry-ref
	Meta map[string]string // adapter detail (base image id, layer count, sizes…)
}

type BuildSpec struct {
	// BaseImage is layered on; Overlays are directories copied in (e.g. skill folders,
	// the in-image agent build, CLAUDE.md). This is how skills are added at BUILD time
	// rather than at runtime — preserving the current model.
	BaseImage   execenv.ImageRef
	Overlays    []Overlay
	Dockerfile  string            // optional; mutually exclusive with BaseImage+Overlays
	BuildArgs   map[string]string
	Tag         string
	ContextDir  string

	// Layer names which stratum this build produces (core | app | user). Lets the
	// registry/fleet reason about cache affinity and diff bases (see "Image layers").
	Layer     ImageLayer
	// SourceKey is the identity of the inputs (e.g. user id, skill-set id) folded into the
	// content-hash tag so a returning user with unchanged inputs cache-hits.
	SourceKey string
}

type ImageLayer string

const (
	LayerCore ImageLayer = "core" // the agentkit base image (the in-image agent + harness binaries)
	LayerApp  ImageLayer = "app"  // base + product binaries/skills (e.g. Platinum's pt) — default launch image
	LayerUser ImageLayer = "user" // app + a curated set of artifacts (see "User images")
)

type Overlay struct {
	Source string // host dir
	Target string // image path, e.g. /workspace/.claude/skills/<name>
}

type PersistOptions struct {
	SessionID string
	// PreferDiff asks for the diff-archive fast path when supported (KB–MB) rather than a
	// full save (GBs). The blob-archive adapter honours it; others ignore it.
	PreferDiff bool
	BaseImage  execenv.ImageRef // for diff: the base to diff against
}

type Capabilities struct {
	SupportsDiff   bool // blob-archive: yes; local-tar/remote: no
	SupportsBuild  bool
	SupportsRemote bool
	// PortableHandles is true when a Handle produced here can be Materialized on a DIFFERENT
	// worker (blob-archive with a shared BlobStore, or a remote registry). local-tar is NOT
	// portable. Multi-worker fleets (see 13) require PortableHandles=true; validated at
	// Fleet construction.
	PortableHandles bool
}
```

## The snapshot/restore flow, decomposed

The single most intricate behaviour in the current system is suspend→archive→restore
(`sandbox-manager.ts` lines ~886–1285). The library decomposes it cleanly across the two interfaces.
Today it is one tangled method; here each half lives where it belongs.

**Archive (snapshot a cold session so it can be resurrected):**

```
orchestration core, archive loop:
  ref     := execenv.Snapshot(ctx, instanceID, {PreferDiff via registry caps})   // commit → image
  handle  := registry.Persist(ctx, ref, {SessionID, PreferDiff:true, BaseImage}) // diff→tar→gzip→blob
  store.UpdateSnapshot(sessionID, handle)                                         // durable pointer
  execenv.Destroy(ctx, instanceID, {SkipSnapshot:true})                           // reclaim resources
```

**Restore (resurrect on the next message):**

```
orchestration core, ensureRunning when destroyed:
  handle  := store.GetSnapshot(sessionID)              // the durable pointer
  ref     := registry.Materialize(ctx, handle)         // download→gunzip→apply diff→commit
  inst    := execenv.Provision(ctx, {Image: ref, ...}) // start from the restored image
```

The famous **diff-archive** optimisation — `docker diff` + `getArchive` to capture only changed files
(KB–MB) instead of `docker save` of the whole image (GBs), with OCI-vs-legacy tar parsing and the
"restored container → force full save" heuristic — is **entirely contained in the `blobarchive`
adapter**. The orchestration core only knows `Persist`/`Materialize`. A different registry (a real
OCI registry) handles the same flow with `push`/`pull` and never thinks about diffs.

This is the payoff of separating the interfaces: the *policy* ("archive cold sessions, restore on
demand") is generic Go in the core; the *mechanism* ("how do bytes get durable") is swappable.

## The shipped adapters

| Adapter | `EnsurePresent` | `Build` | `Persist` | `Materialize` | Pairs with |
|---------|-----------------|---------|-----------|---------------|------------|
| **`ociregistry`** | `docker pull` (or force-pull with `AlwaysPull`) | not supported | `docker push` to registry | pull from registry handle | DinD + registry (dev: `registry:5000`, prod: ACR) |
| **`blobarchive`** | no-op | not supported | commit-diff → tar → gzip → **blob** | download → gunzip → apply diff → commit | DinD (Platinum prod/staging) |
| **`remote`** *(sketch)* | `docker pull` | push build to a builder | `docker push` | handle == ref; pull on EnsurePresent | Kubernetes |

> **Note:** The `localbuild` adapter (tar-on-disk via `docker save`/`docker load`) was removed in the
> registry-everywhere refactor. Use `ociregistry` with `registry:5000` for local dev (images are
> force-pulled from the local registry on each session launch).

`blobarchive` is essentially today's `azure-upload.ts` + the diff-extraction half of
`sandbox-manager.ts`, repackaged as an `ImageRegistry`. The blob backend itself is an injected
`BlobStore` interface (the host supplies it — Platinum passes its `storage.BlobStore`), so the library
isn't bound to Azure.

## Composition: why the interfaces are separate

A naïve design would put "archive to Azure" directly on the environment. That couples lifecycle policy
to a storage backend and a snapshot mechanism, which is exactly the entanglement we're undoing. By
splitting:

- You can run **DinD + blobarchive** (staging/prod) or **DinD + ociregistry** (dev with `registry:5000`) by swapping the registry only.
- You can run **K8s + remote** without the core learning anything new — `Persist` becomes a push.
- The **mock registry** round-trips `Snapshot` refs in memory, so suspend/restore is testable with no
  Docker and no blob storage at all.

The orchestration core is constructed with one of each:

```go
runner := agentkit.NewRunner(agentkit.Deps{
	Env:      dockerdind.New(cfg),          // ExecutionEnvironment
	Registry: blobarchive.New(blobStore),   // ImageRegistry
	Store:    platinumSessionStore{db},     // SessionStore (host)
	Events:   events.NewPipeline(...),      // EventStreamer/pipeline
	Artifacts: blobArtifactStore{...},      // ArtifactStore (host)
	OrgContext: platinumOrgContext{...},    // extension
	// ...
})
```

Tests pass `execenv.NewMock()` + `imageregistry.NewMock()` and everything else mock — see
[10-extension-points.md](10-extension-points.md).

## The unified image model: three image kinds on one snapshot primitive

App images follow the app image contract in [10-extension-points.md](10-extension-points.md#the-app-image-contract).

There are **three** kinds of image in the system, and they are built two different ways. Getting this
distinction right is what keeps the model simple.

| Kind | What it is | How it's built | When |
|------|-----------|----------------|------|
| **Core → App** image | The agentkit base (in-image agent + all harness binaries) layered with product binaries/skills (e.g. Platinum's `pt`, `CLAUDE.md`, skill folders) | `ImageRegistry.Build` (BaseImage + Overlays / Dockerfile) | **build/CI time**, pushed to the registry. The App image is the default launch image (`Policy.BaseImage`). |
| **Session-snapshot** image | The *whole filesystem* of a running, **isolated** session, captured as an image layer (copy-on-write diff) | `execenv.Snapshot` → `ImageRegistry.Persist` (diff-archive) | on suspend/archive (see "snapshot/restore flow" above) |
| **User** image | A *curated* image: an App image + a named set of artifacts copied in, then snapshotted | the **same snapshot primitive** — launch a throwaway container from the App image, copy the artifacts in, snapshot, persist | out-of-band (no LLM); cached by content hash |

The key insight (and the user's refinement): **session-snapshot images and user images are built by the
same `Snapshot` primitive**, differing only in *what is in the container when you snapshot it*. A
session snapshot captures a live session as-is; a user image captures a throwaway container seeded with
curated artifacts ([06](06-artifacts.md)). `Build` (Dockerfile/overlays) is reserved for the build-time
Core→App layers.

### Build-time layering (Core → App) — unchanged via Overlays

```go
ref, _ := registry.Build(ctx, imageregistry.BuildSpec{
	Layer:     imageregistry.LayerApp,
	BaseImage: "agentkit-sandbox:base",                 // the core image
	Overlays: []imageregistry.Overlay{
		{Source: "./bin/pt",             Target: "/usr/local/bin/pt"},
		{Source: "./skills/forecasting", Target: "/workspace/.claude/skills/forecasting"},
		{Source: "./CLAUDE.md",          Target: "/workspace/CLAUDE.md"},
	},
	Tag: "platinum-sandbox:v123",
})
```

There is deliberately **no runtime "install skill" method** — skills are an image concern. Per-customer
skill sets are App-image overlays at build time.

### User images — curated artifacts, built via snapshot

A *user image* lets a user save a useful capability (a script, a set of files) they produced in the
agent and re-launch from it later. It is built by the orchestration core (not the LLM):

```
Runner.BuildUserImage(ctx, {BaseImage: appImage, Artifacts: [...named artifact refs...], Name}):
  1. resolve content-hash tag = hash(BaseImage + sorted artifact identities)
  2. if Resolve(spec) hits → return the cached ref            // returning user, unchanged inputs
  3. miss: Provision a throwaway instance from BaseImage
  4.       copy the named artifacts in (from the BlobStore — see 06 folder-slurp)
  5.       execenv.Snapshot → ImageRegistry.Persist          // same primitive as session-snapshot
  6.       Destroy the throwaway instance
```

`CreateSessionRequest` resolves its launch image as: explicit `Image` override > resolved user image >
App default (`Policy.BaseImage`). Build timing is **prewarm + cache-by-hash** (an out-of-band
`Runner.PrewarmUserImage` called when the user edits customisations; launch resolves-then-builds only on
a true miss).

### Two rules that fall out of this model

1. **Shared tenancy cannot snapshot** — and therefore cannot produce session-snapshot OR user images.
   When many sessions share one container ([02](02-execution-environment.md)), a file diff is not
   attributable to a single session, so `Snapshot`/`BuildUserImage` **error**. The environment reports
   `SupportsSnapshot=false`. Only `TenancyPerSession` (one container per session) supports these.
2. **The diff base is the launch image, not always `Policy.BaseImage`.** A session launched from a user
   image must diff against *that* image, or the diff archive is wrong. The Runner records the launch
   image on the instance/session and passes it as `PersistOptions.BaseImage` (fixing the v0
   hardcode of `Policy.BaseImage`).

### Content-hash tagging (so cache hits are deterministic)

Adopt the OpenHands-style triple so rebuilds are minimised and `Resolve` is exact:
- **Versioned tag** (`…:v123`) — human, deploy-pinned (the App image / `Policy.BaseImage`).
- **Content-hash tag** (`…:<sha256(base digest + sorted overlay/artifact hashes + build args)>`) — the
  cache key `Resolve`/`Build` compute. A tag with that hash already in the registry ⇒ cache hit, no build.

## Open design questions (flagged for implementation)

- **Handle portability.** A `blob-archive` handle is meaningful only with the same `BlobStore`; a
  `registry` handle is portable anywhere with pull access; a `local-tar` handle is single-host. This is
  now first-class via `Capabilities.PortableHandles`: **multi-worker fleets ([13](13-fleet-placement.md))
  require `PortableHandles=true`** (validated at Fleet construction), because a lost/drained worker
  restores a session on a *different* worker via its handle. Mixing adapters across a session's lifetime
  remains unsupported.
- **Diff base drift.** The diff archive is valid only against the base image it was diffed from; the
  handle records `base_image_id` (as today) and `Materialize` validates it. If the base image changes,
  fall back to a full save on the *next* snapshot.
- **Build determinism on remote.** `remote.Build` needs a builder service (buildkit/Kaniko); left as a
  sketch with the interface in place.
