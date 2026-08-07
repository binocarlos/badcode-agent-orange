# 03 — `ImageRegistry`: get images in, get snapshots out

`ExecutionEnvironment` runs an image; `ImageRegistry` is how an image becomes available to run, and
how a snapshot of a running session is preserved and later retrieved. The two are **orthogonal** and
**compose**.

> **Definition.** An `ImageRegistry` makes images present for an engine to run, builds images from a
> context, and moves images in and out as durable artifacts — whether "durable" means a local tar on
> disk, a gzipped archive in blob storage, or a tag in a real container registry.

> **Scope.** This is the engine half only: opaque refs and opaque handles. The product layer adds a
> project-scoped catalogue of **named, versioned, labeled** images on top of these same primitives,
> plus the resolution rules that decide which image a worker launches from — see
> [The named-image catalogue](#the-named-image-catalogue-product-layer-13) below and
> [docs/product/08-images-and-skills.md](product/08-images-and-skills.md) §13. Nothing in that layer
> extends the `ImageRegistry` interface.

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
	// overlay of skill folders). Returns the resulting ref. NOTE: no shipped adapter
	// implements this — both return an error and report SupportsBuild:false. A remote
	// adapter would hand the context to a builder service.
	Build(ctx context.Context, spec BuildSpec) (execenv.ImageRef, error)

	// Resolve returns an existing ref for a BuildSpec's content hash WITHOUT building
	// (ok=false on cache miss). The Runner calls Resolve first and Build only on a miss, so
	// a returning user with unchanged customisations cache-hits instantly. Tags are
	// content-addressed (see "Image layers & tagging" below).
	Resolve(ctx context.Context, spec BuildSpec) (ref execenv.ImageRef, ok bool, err error)

	// Persist takes an in-engine image ref (typically from ExecutionEnvironment.Snapshot)
	// and stores it durably, returning a Handle that survives process/host restarts.
	// • blob-archive adapter: `docker save` → gzip → blob
	// • remote adapter:       `docker push` → return the registry reference as the handle
	Persist(ctx context.Context, ref execenv.ImageRef, opts PersistOptions) (Handle, error)

	// Materialize is the inverse of Persist: given a Handle, make the image present for
	// the engine and return a runnable ref (for ExecutionEnvironment.Provision).
	// • blob-archive: blob → gunzip → `docker load`
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
// orchestration core stores it on the session row via RunnerStore and passes it back to
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
	LayerApp  ImageLayer = "app"  // base + product binaries/skills — the default launch image
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

The single most intricate behaviour in the system is archive→restore. The library decomposes it
cleanly across the two interfaces, so each half lives where it belongs.

**Archive (snapshot a cold session so it can be resurrected):**

```
orchestration core, archive loop (runner.go:Snapshot):
  ref     := execenv.Snapshot(ctx, instanceID, {ForceFull: !caps.SupportsDiff})   // commit → image
  handle  := registry.Persist(ctx, ref, {SessionID, PreferDiff: caps.SupportsDiff,
                                         BaseImage: instance.Image})              // → durable bytes
  store.SetSnapshotHandle(sessionID, handle)                                      // durable pointer
  execenv.Destroy(ctx, instanceID, {SkipSnapshot:true})                           // reclaim resources
```

**Restore (resurrect on the next message):**

```
orchestration core, ensureRunning when destroyed:
  handle  := store.GetSnapshotHandle(sessionID)        // the durable pointer
  ref     := registry.Materialize(ctx, handle)         // adapter-specific; blobarchive: gunzip→docker load
  inst    := execenv.Provision(ctx, {Image: ref, ...}) // start from the restored image
```

> **The diff-archive fast path is designed for but not implemented.** `PersistOptions.PreferDiff`
> and `Capabilities.SupportsDiff` exist so that an adapter *can* capture only changed files (KB–MB
> via `docker diff` + `getArchive`) instead of a whole-image `docker save` (GBs). No shipped
> adapter does: `blobarchive.Capabilities()` returns `SupportsDiff: false` and `Persist` always
> does the full save (the source marks the fast path FOLLOW-UP), and `ociregistry` reports
> `SupportsDiff: false` too. Because the Runner passes `PreferDiff: caps.SupportsDiff`, the flag is
> always false today. Do not read anything below as describing shipped diff behaviour.

The point stands regardless of which mechanism an adapter picks: the orchestration core only knows
`Persist`/`Materialize`. `blobarchive` moves gzipped tars to a `BlobStore`; `ociregistry` handles the
same flow with `push`/`pull`. This is the payoff of separating the interfaces — the *policy*
("archive cold sessions, restore on demand") is generic Go in the core; the *mechanism* ("how do
bytes get durable") is swappable.

## The shipped adapters

| Adapter | `EnsurePresent` | `Build` | `Persist` | `Materialize` | Pairs with |
|---------|-----------------|---------|-----------|---------------|------------|
| **`ociregistry`** | `docker pull` (or force-pull with `AlwaysPull`) | not supported | `docker push` to registry | pull from registry handle | DinD + registry (dev: `registry:5000`, prod: Artifact Registry) |
| **`blobarchive`** | no-op | not supported | `docker save` → gzip → **blob** | blob → gunzip → `docker load` | DinD (prod/staging) |
| **`remote`** *(sketch)* | `docker pull` | push build to a builder | `docker push` | handle == ref; pull on EnsurePresent | Kubernetes |

> **Note:** The `localbuild` adapter (tar-on-disk via `docker save`/`docker load`) was removed in the
> registry-everywhere refactor. Use `ociregistry` with `registry:5000` for local dev (images are
> force-pulled from the local registry on each session launch).

`blobarchive` packages `docker save`/`docker load` plus blob upload behind the `ImageRegistry`
interface. The blob backend itself is an injected `BlobStore` interface (the host supplies it —
`extension/filesblob` and `extension/gcsblob` ship), so the library isn't bound to any one storage
provider.

## Composition: why the interfaces are separate

A naïve design would put "archive to Azure" directly on the environment. That couples lifecycle policy
to a storage backend and a snapshot mechanism, which is exactly the entanglement we're undoing. By
splitting:

- You can run **DinD + blobarchive** (staging/prod) or **DinD + ociregistry** (dev with `registry:5000`) by swapping the registry only.
- You can run **K8s + remote** without the core learning anything new — `Persist` becomes a push.
- The **mock registry** round-trips `Snapshot` refs in memory, so archive/restore is testable with no
  Docker and no blob storage at all.

The orchestration core is constructed with one of each:

```go
env, _      := dockerdind.NewDinD(dockerdind.DinDConfig{DockerHost: dockerHost, /* ... */})
registry, _ := blobarchive.New(dockerHost, blobStore)

runner, err := agentkit.NewRunner(agentkit.Deps{
	Env:       env,                  // ExecutionEnvironment
	Registry:  registry,             // ImageRegistry
	Store:     hostRunnerStore{db},  // RunnerStore (host)
	Artifacts: hostArtifactStore{},  // artifacts.ArtifactStore (host)
	Claims:    hostClaimsIssuer{},   // extension.ScopedClaimsIssuer (host)
	// Optional, product layer: Images (the §13 resolver), Snapshots (the TTL
	// reaper's catalogue), SessionContext, WorkerEvents — see 14.
})
if err != nil { /* fail-fast: trust gate / portability validation */ }
```

The full `Deps` surface, with which fields are required and what nil means, is in
[14-host-adapters.md](14-host-adapters.md).

Tests pass `execenv.NewMock()` + `imageregistry.NewMock()` and everything else mock — see
[14-host-adapters.md](14-host-adapters.md).

## The unified image model: three image kinds on one snapshot primitive

App images follow the app image contract in [14-host-adapters.md](14-host-adapters.md#the-app-image-contract).

There are **three** kinds of image in the system, and they are built two different ways. Getting this
distinction right is what keeps the model simple.

| Kind | What it is | How it's built | When |
|------|-----------|----------------|------|
| **Core → App** image | The agentkit base (in-image agent + all harness binaries) layered with product binaries/skills (a product CLI, `CLAUDE.md`, skill folders) | host Dockerfiles (`installations/`); `ImageRegistry.Build` reserves the shape but is unimplemented | **build/CI time**, pushed to the registry. The App image is the default launch image (`Policy.BaseImage`). |
| **Session-snapshot** image | The *whole filesystem* of a running, **isolated** session, captured as an image layer | `execenv.Snapshot` → `ImageRegistry.Persist` | on archive (see "snapshot/restore flow" above), and on demand via `Runner.Snapshot` — which is what the product layer's `image_create` calls |
| **User** image | A *curated* image: an App image + a named set of artifacts copied in, then snapshotted | the **same snapshot primitive** — launch a throwaway container from the App image, copy the artifacts in, snapshot, persist | out-of-band (no LLM); cached by content hash |

The key insight (and the user's refinement): **session-snapshot images and user images are built by the
same `Snapshot` primitive**, differing only in *what is in the container when you snapshot it*. A
session snapshot captures a live session as-is; a user image captures a throwaway container seeded with
curated artifacts ([06](06-artifacts.md)). `Build` (Dockerfile/overlays) is reserved for the build-time
Core→App layers.

### Build-time layering (Core → App) via Overlays

> **Not shipped.** `Build` errors on both shipped adapters (`SupportsBuild: false`); the App-image
> layering below is done by the host's own Dockerfiles — see `installations/README.md`. The snippet
> shows the shape the interface reserves for it, not a call that works today.

```go
ref, _ := registry.Build(ctx, imageregistry.BuildSpec{
	Layer:     imageregistry.LayerApp,
	BaseImage: "agentkit-sandbox:base",                 // the core image
	Overlays: []imageregistry.Overlay{
		{Source: "./bin/tool",           Target: "/usr/local/bin/tool"},
		{Source: "./skills/forecasting", Target: "/workspace/.claude/skills/forecasting"},
		{Source: "./CLAUDE.md",          Target: "/workspace/CLAUDE.md"},
	},
	Tag: "app-sandbox:v123",
})
```

`ImageRegistry` has no "install skill" method, and build-time overlays remain the way a skill set is
baked into an App image. That is no longer the *only* way a skill reaches a container: the product
layer's `skill_install` tool writes a skill's `SKILL.md` into `/workspace/.claude/skills/<name>/`
inside the **running** session and executes its `install_sh` there
(`sandbox/src/tools/skill-install.ts`, route `POST /skills/install`). Making that durable is what
`image_create` is for — burn the session, and the installed skill is in the resulting image. See
[docs/product/08-images-and-skills.md](product/08-images-and-skills.md) §14 and
[07](07-in-image-agent.md).

### User images — curated artifacts, built via snapshot (roadmap)

> **Status: future / roadmap — not yet implemented.** The `BuildUserImage` / `PrewarmUserImage`
> orchestration verbs below are a design sketch, not `Runner` methods that exist today (the
> `Runner` interface in `go/agentkit.go` has neither; only unexported helpers
> `cachedUserImage`/`snapshotPersistCache` survive in `runner.go`, with no caller). The shipped
> answer to "save a useful environment and re-launch from it" is instead the product layer's §13
> image catalogue — see the next section. This section records the intended shape so the
> `Snapshot`/`Persist` primitives stay sufficient for it.

A *user image* lets a user save a useful capability (a script, a set of files) they produced in the
agent and re-launch from it later. The intent is that it is built by the orchestration core (not the
LLM):

```
BuildUserImage(ctx, {BaseImage: appImage, Artifacts: [...named artifact refs...], Name}):   // roadmap
  1. resolve content-hash tag = hash(BaseImage + sorted artifact identities)
  2. if Resolve(spec) hits → return the cached ref            // returning user, unchanged inputs
  3. miss: Provision a throwaway instance from BaseImage
  4.       copy the named artifacts in (from the BlobStore — see 06 folder-slurp)
  5.       execenv.Snapshot → ImageRegistry.Persist          // same primitive as session-snapshot
  6.       Destroy the throwaway instance
```

Today's launch-image resolution is explicit and ordered (`resolveLaunchImage` in `go/runner.go`):

**explicit `Image` > `SessionContext.WorkerImage` (§13 pointer) > `CustomImageID` (materialized via
`Deps.CustomImages`) > `SessionContext.BaseImage` > `Policy.BaseImage`.**

The links fail deliberately differently — see *The named-image catalogue* below. A resolved-user-image
tier would slot in ahead of the base default, with build timing of **prewarm + cache-by-hash** so a
launch resolves-then-builds only on a true cache miss.

### Two rules that fall out of this model

1. **Shared tenancy cannot snapshot** — and therefore cannot produce session-snapshot OR user images.
   When many sessions share one container ([02](02-execution-environment.md)), a file diff is not
   attributable to a single session, so a snapshot **errors** (`SupportsSnapshot=false`). Only
   `TenancyPerSession` (one container per session) supports these. See [02](02-execution-environment.md)
   for the trust gate and capability axes.
2. **The diff base is the launch image, not always `Policy.BaseImage`.** A session launched from a
   catalogue or user image must diff against *that* image, or a diff archive would be wrong. The
   Runner reads the launch image back off the `execenv.Instance` (`inst.Image`, set by `Provision`)
   and passes it as `PersistOptions.BaseImage`, falling back to `Policy.BaseImage` only when the
   instance carries none. No shipped adapter diffs yet, so today this only lands in the handle's
   `base_image_id` metadata — but it is the field a diff adapter would need, and it is correct now.

### Content-hash tagging (so cache hits are deterministic)

Use a content-hash triple so rebuilds are minimised and `Resolve` is exact:
- **Versioned tag** (`…:v123`) — human, deploy-pinned (the App image / `Policy.BaseImage`).
- **Content-hash tag** (`…:<sha256(base digest + sorted overlay/artifact hashes + build args)>`) — the
  cache key `Resolve`/`Build` compute. A tag with that hash already in the registry ⇒ cache hit, no build.

## The named-image catalogue (product layer, §13)

Everything above is the engine's view: a `Handle` is opaque, and the Runner is handed a launch image
by whoever calls it. The **product layer** adds the missing half — a project-scoped catalogue that
gives snapshots *names* an operator and a worker can both write down. It sits entirely on the
primitives above and adds no interface to `ImageRegistry`.

Authoritative spec: [docs/product/08-images-and-skills.md](product/08-images-and-skills.md) §13. This
section states only what it means for this document's machinery.

**Where it lives.** `agent_custom_images` carries two catalogues in one table
(`go/agentdb/customimages.go`): the pre-existing latest-wins host-built rows (version `0`) and the
§13 rows (version `>= 1`), keyed `(project, name, version)`. The project namespace is the `customer`
column, mapped in that one file. §13 rows add `labels` (jsonb, K8s-style selectors), creation
provenance (`created_by_worker` / `created_by_session`), `expires_at`, `last_resumed_at` and a
`reaped_at` tombstone.

**Versions are allocated by the store, never supplied.** `CreateCustomImage` reads the high-water
mark for `(project, name)` and inserts `max+1`, retrying on the unique-index collision two
simultaneous burns produce — so versions are monotonic and gap-free, and a caller cannot fabricate an
identity. Tombstoned versions still count toward the mark: reaping bytes must not make a number
reusable. There is no `UpdateCustomImage` and no delete: publishing an improved environment is a new
version under the same name.

**`image_create` is a naming layer over `Snapshot`/`Persist`.** The MCP tool
(`go/cmd/agentd/mcp_images.go`) identifies the calling session *from its token* — a session can only
snapshot itself, and there is no argument with which to name another — then calls `Runner.Snapshot`,
which is the same `execenv.Snapshot` → `ImageRegistry.Persist` path the archive loop uses. The
`imageregistry.Handle` that comes back is JSON-marshalled into the catalogue row's
`registry_handle`. Order is validate → snapshot → record: a failed record after a good snapshot
leaves orphaned bytes the reaper collects, whereas the reverse would leave a row pointing at nothing.
The catalogue row and its `config_events` entry are written in one transaction.

**Resolution (§13.3), and the rule that it never falls back.** `ResolveCustomImage(project, ref)`
takes one text field:

| Reference | Resolves to |
|---|---|
| `toolbox` (bare name) | the **latest** version of that name in the project — a floating pointer, so curation can publish improvements without editing a worker row |
| `toolbox:7` | exactly version 7 |

Every failure is loud and typed — `ErrCustomImageNotFound`, `ErrCustomImageReaped`,
`ErrCustomImageUnmaterialisable`, `ErrCustomImageInvalid` — and **nothing substitutes another
image**. A floating reference resolves the newest version and then insists on it; there is no
"nearest live version" search, because a worker that was pointed at an environment and quietly got a
different one is the drift the catalogue exists to prevent.

**How it reaches this document's launch path.** `agentd`'s `catalogueImageResolver`
(`go/cmd/agentd/imageresolver.go`) implements `agentkit.ImageResolver`: resolve → decode the handle →
`ImageRegistry.Materialize` → best-effort `last_resumed_at` stamp. The *same object* is bound twice —
as the job composer's `ImageResolver` and as `agentkit.Deps.Images` — so a worker job and an
interactive chat with that worker cannot launch from different environments. Two columns feed it, with
different contracts:

- **`worker.image`** — a pointer and nothing else. It arrives on `extension.SessionContext.WorkerImage`
  and **every** resolution failure fails the launch with `agentkit.ErrLaunchImageUnresolvable`,
  including "no `Deps.Images` is wired".
- **`project_settings.base_image`** — a pointer **or** a literal registry reference. It arrives on
  `SessionContext.ProjectBaseImage` and is only consulted when it is the string that won the chain.
  Exactly one outcome falls through to using it verbatim: `agentkit.ErrImageRefNotInCatalogue`,
  i.e. "that names no image of mine" — which is how the standalone stack's `agentkit-sandbox:dev`
  keeps working. Reaped, unmaterialisable, or a database that will not answer all fail the launch.

`imageOrigin` (`go/runner.go`) travels with the resolved image so a later pull or provision failure
can name the configuration field an operator actually wrote, and say whether the value was resolved
through the catalogue or used literally.

**Reaping is storage GC, not curation.** `project_settings.snapshot_ttl_days` stamps `expires_at` at
burn time from the project's setting (a promise — later TTL changes do not retroactively move it; `0`
means never). Each sweep reads the TTL fresh to decide how far back to *look*, and each row's stamped
`expires_at` decides whether it may actually go. `agentkit.SnapshotReaper` (`go/snapshot_reaper.go`, driven by
`Policy.SnapshotReapInterval` over `Deps.Snapshots`) deletes the bytes through `ImageRegistry.Remove`
**first**, then calls `MarkCustomImageReaped` to tombstone the row. That order is deliberate: a crash
between them leaves a resolvable record pointing at missing bytes for one cycle, which the next pass
fixes, whereas the reverse would orphan bytes forever. The record survives so history, provenance and
the version high-water mark stay intact, and resolving a tombstone reports `ErrCustomImageReaped`
instead of failing obscurely. **Use defers the reap** (RD9): a version whose `last_resumed_at` falls
inside the project's current TTL window is spared for that pass and reported as `Deferred`, with a
log line naming it — the stamped `expires_at` is not rewritten, so the row stays honest, but an image
a worker launches from daily is no longer deleted out from under it. The deferral lapses by itself
once the launches stop. Reaping and the `last_resumed_at` stamp both write **outside** the
config-event seam on purpose, so `Deps.Snapshots` must be a store without
`agentdb.InstallConfigEventGuard` armed.

## Open design questions (flagged for implementation)

- **Handle portability.** A `blob-archive` handle is meaningful only with the same `BlobStore`; a
  `registry` handle is portable anywhere with pull access; a `local-tar` handle is single-host. This is
  now first-class via `Capabilities.PortableHandles`: **multi-worker fleets ([13](13-fleet-placement.md))
  require `PortableHandles=true`** (validated at Fleet construction), because a lost/drained worker
  restores a session on a *different* worker via its handle. Mixing adapters across a session's lifetime
  remains unsupported.
- **The diff fast path itself.** `PreferDiff` / `SupportsDiff` are in the interface and nothing
  implements them; `blobarchive.Persist` is a full `docker save`. Session snapshots are therefore
  GB-scale, which is the cost that bounds `snapshot_ttl_days` and the reaper above.
- **Diff base drift.** A diff archive would be valid only against the base image it was diffed from.
  The handle *does* record `base_image_id` in `Meta`, but nothing validates it —
  `blobarchive.Materialize` checks only `Handle.Kind`. A diff adapter would have to add the check and
  fall back to a full save on the *next* snapshot when the base has moved.
- **`Build` is unimplemented everywhere.** Both shipped adapters return an error from `Build`
  ("use a pre-built image pushed to the registry") and report `SupportsBuild: false`; image building
  is the host's job (`installations/README.md`). `Resolve` is real in both, but with `Build`
  erroring the Resolve-then-Build cache dance has no second half. `remote.Build` would need a builder
  service (buildkit/Kaniko); left as a sketch with the interface in place.
