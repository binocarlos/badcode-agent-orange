# 14 — Host adapters reference

This is the single document a **host application author** reads when wiring Agent Orange into their
product. It answers "what do I have to implement to use this?" in one place: every extension
interface the library consumes, the exact method signatures (copied from source), the contract each
method must honour, lifecycle and gotchas, whether the interface is required or optional, and which
shipped implementation or mock to start from. It closes with the deployment shapes those adapters
compose into.

The library is generic; everything product-specific is injected through a named interface or plugin.

The canonical wiring example is `go/examples/standalone/main.go` (a runnable host that drives a real
DinD daemon). The pre-built HTTP host is `go/cmd/agentd` — see [15 — Standalone stack](15-standalone-stack.md).

---

## Three categories of extension

1. **Engine adapters** — implement `execenv.ExecutionEnvironment` and `imageregistry.ImageRegistry`.
   Usually you pick a *shipped* adapter (Docker/DinD; blobarchive/ociregistry) and only write one for
   a brand-new engine. ([02](02-execution-environment.md), [03](03-image-registry.md))
2. **Host service interfaces** — implement these against your stack (persistence, blobs, auth,
   context, costing). This is the bulk of what a host writes.
3. **Plugins** — in-image tool plugins ([07](07-in-image-agent.md)) and browser render plugins
   ([05](05-event-streaming.md#rendering-in-the-web-package)), plus the base image the sessions launch from.

For placing sessions across more than one worker (sticky placement, horizontal scale), see
[13 — Fleet placement](13-fleet-placement.md); this doc constructs a single-worker Runner and notes
where the fleet seam takes over.

---

## Quick-reference table

| Interface | Package | Required? | Shipped reference | Shipped mock |
|---|---|---|---|---|
| `RunnerStore` | `agentkit` | **Yes** | `agentdb.Store` (Postgres, `agentdb.Open`) · `extension/sqlitestore` (`sqlitestore.Open`, dev) | `agentkittest.NewMemStore()` |
| `extension.BlobStore` | `extension` | **Yes** (byte backend for artifacts + snapshots) | `extension/filesblob` (`filesblob.NewBlobStore`) · `extension/gcsblob` (`gcsblob.NewBlobStore`) | `agentkittest.NewMemBlobs()` |
| `extension.BlobStoreFactory` | `extension` | Only when building user images (`Deps.Blobs`) | `extension/gcsblob` (`gcsblob.NewBlobStoreFactory`) | `agentkittest.NewMemBlobsFactory()` |
| `extension.ScopedClaimsIssuer` | `extension` | **Yes** | `extension/devclaims` (`devclaims.New`) — dev only | `agentkittest.StaticClaims{Token: "..."}` |
| `artifacts.ArtifactStore` | `artifacts` | **Yes** | `extension/blobartifacts` (`blobartifacts.New`) · `filesblob.NewArtifactStore` | `artifacts.NewMock()` |
| `execenv.ExecutionEnvironment` | `execenv` | **Yes** (Fleet OR Env) | `execenv/docker` (`dockerdind.NewDinD`) | `execenv.NewMock()` |
| `imageregistry.ImageRegistry` | `imageregistry` | **Yes** | `imageregistry/blobarchive` (`blobarchive.New`) · `imageregistry/ociregistry` (`ociregistry.New`) | `imageregistry.NewMock()` |
| `extension.SessionContextProvider` | `extension` | No — default `""` | host-written | leave `nil` |
| `extension.TokenUsageLogger` | `extension` | No — no-op | host-written | leave `nil` |
| `extension.ArtifactEnricher` | `extension` | No — identity | host-written | leave `nil` |
| `extension.Metrics` | `extension` | No — no-op | host-written | leave `nil` |

**Required?** is grounded in `NewRunner` nil-handling in `go/agentkit.go`:

- `Fleet`/`Env` — one must be non-nil; `NewRunner` returns an error if both are nil. A bare `Env` is
  wrapped as a one-worker fleet automatically (the shim).
- `Registry`, `Store`, `Artifacts`, `Claims` — no explicit nil-guard in `NewRunner`, but any nil here
  panics at first use. Treat as required.
- `Blobs` (the `BlobStoreFactory`) — required only when the Runner builds **user images**
  (`BuildUserImage`); nil otherwise disables artifact copy-in for user images.
- `SessionContext`, `TokenLogger`, `Enricher`, `Metrics` — documented in `Deps` as optional; the
  runner skips them when nil (contributes `""` / no-op / identity).
- `Events` — optional; nil builds a `Store`-backed event sink.
- `HTTPClient` — optional; nil is replaced with `&http.Client{}` (no timeout, correct for SSE).

Note that the plain `extension.BlobStore` is **not** itself a `Deps` field. It is a building block:
you pass one into `filesblob.NewArtifactStore`/`blobartifacts.New` (the artifact store) and into
`blobarchive.New` (the snapshot registry). The `BlobStoreFactory` is the only blob type wired
directly into `Deps` (as `Deps.Blobs`).

---

## Constructing the Runner (the wiring)

`agentkit.NewRunner(Deps) (Runner, error)` — the fields below mirror `go/agentkit.go` exactly:

```go
runner, err := agentkit.NewRunner(agentkit.Deps{
	// Engine placement — set EITHER Env (wrapped as a one-worker fleet) OR Fleet (13).
	Env:      dindEnv,   // execenv.ExecutionEnvironment
	Registry: registry,  // imageregistry.ImageRegistry

	// Required host services.
	Store:     store,     // agentkit.RunnerStore
	Artifacts: artStore,  // artifacts.ArtifactStore
	Claims:    claims,    // extension.ScopedClaimsIssuer

	// Blob factory — required only for user-image builds; nil otherwise.
	Blobs: nil, // extension.BlobStoreFactory

	// Optional; nil falls back to the documented default.
	SessionContext: nil, // extension.SessionContextProvider → ""
	TokenLogger:    nil, // extension.TokenUsageLogger      → no-op
	Enricher:       nil, // extension.ArtifactEnricher       → identity
	Metrics:        nil, // extension.Metrics               → no-op

	Policy: agentkit.Policy{
		BaseImage:      "agentkit-sandbox:dev",
		AgentPort:      3010,             // in-image agent port (default 3010)
		ArchiveTimeout: 24 * time.Hour,   // 0 disables the idle snapshot+destroy loop
		MaxConcurrent:  20,
		SessionEnv: map[string]string{    // model-provider config for the in-image agent
			"ANTHROPIC_BASE_URL": "http://model-proxy:4000",
		},
	},
})
if err != nil { /* ... */ }
runner.Start(ctx) // begin control loops + recover orphaned instances
```

Tests swap every adapter for its mock and leave optional fields nil:

```go
runner, _ := agentkit.NewRunner(agentkit.Deps{
	Env:       execenv.NewMock(),
	Registry:  imageregistry.NewMock(),
	Store:     agentkittest.NewMemStore(),
	Artifacts: artifacts.NewMock(),
	Claims:    agentkittest.StaticClaims{Token: "test-token"},
})
```

Every mock records its calls, so a host test can assert the exact interaction log (which dependency
was called, with what args, in what order) — the same hermetic discipline the library's own suites use.

---

## `agentkit.RunnerStore`

**Source:** `go/agentkit.go`

**Purpose.** Durable session identity. The library owns runtime state only; everything that must
survive a process restart (session rows, compacted query events, snapshot handles, worker bindings)
lives here. `NewRunner` and the fleet call these methods; the library never writes to a store of its
own.

```go
// RunnerStore is the minimal DB surface the Runner and Fleet require. Both
// *agentdb.Store and agentkittest.MemStore satisfy this interface.
type RunnerStore interface {
	GetSession(ctx context.Context, id string) (*agentdb.Session, error)
	UpdateSession(ctx context.Context, session *agentdb.Session) (*agentdb.Session, error)

	PersistQueryEventsFlat(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error
	ListQueryEventsFlat(ctx context.Context, sessionID string) ([]events.Envelope, error)

	GetSnapshotHandle(ctx context.Context, sessionID string) (imageregistry.Handle, bool, error)
	SetSnapshotHandle(ctx context.Context, sessionID string, h imageregistry.Handle) error

	// Worker-binding methods record the sticky session→worker placement so that two
	// host replicas behind a load balancer both resolve the same worker for a session.
	GetWorkerBinding(ctx context.Context, sessionID string) (string, bool, error)
	SetWorkerBinding(ctx context.Context, sessionID, workerID string) error
	ClearWorkerBinding(ctx context.Context, sessionID string) error
}
```

**Contract / lifecycle.**

- `GetSession` is called before every runner operation to load the durable row. Return a typed error
  (not `nil, nil`) if the session is not found.
- `UpdateSession` upserts the changed session row and returns the stored copy. Callers may not have
  created the row yet if the host has a separate creation path, so implementations should upsert.
- `SetSnapshotHandle` / `GetSnapshotHandle` round-trip the opaque `imageregistry.Handle` (stored as
  JSON on the session row). The runner reads the handle on Resume to restore from a snapshot; write it
  atomically with the session state.
- `PersistQueryEventsFlat` upserts the full compacted `[]events.Envelope` slice for a `(sessionID,
  queryID)` pair. Existing rows for the same pair are overwritten (no append). `searchText` is the
  full-text search hint — store it alongside for FTS indexing.
- `ListQueryEventsFlat` must return all events for a session across all query IDs, ordered by
  insertion time, flat.
- Worker-binding methods support sticky placement in multi-worker fleets ([13](13-fleet-placement.md)).
  A single-worker host can keep them trivial; the reference stores use a dedicated table.

**Gotchas.**

- `GetSession` returning a wrapped `sql.ErrNoRows` is correct; returning `nil, nil` is not — the
  runner will dereference the nil pointer.
- `PersistQueryEventsFlat` is called during the active query (compaction). Keep it non-blocking;
  avoid long transactions.

**Shipped references.**
- `agentdb.Store` — the production store (Postgres). Construct with `agentdb.Open(postgresURL)`. This
  is what `agentd` uses.
- `extension/sqlitestore.Store` — SQLite via `modernc.org/sqlite` (pure Go, no cgo). Open with
  `sqlitestore.Open(path)`; also exposes a `Blobs()` helper returning a `filesblob` store rooted next
  to the DB file. Suitable for single-server dev and local examples.

**Shipped mock.** `agentkittest.NewMemStore()` — in-memory, goroutine-safe. Call `store.Seed(sess)`
to pre-populate an `agentdb.Session` row.

---

## `extension.BlobStore` and `extension.BlobStoreFactory`

**Source:** `go/extension/extension.go`

**Purpose.** Byte backend for artifacts and snapshot archives. Keys are opaque strings scoped to one
namespace (a session or a global bucket); the interface is intentionally minimal so it maps cleanly to
GCS, S3, Azure Blob Storage, or a local filesystem. A `BlobStoreFactory` mints namespace-scoped
`BlobStore`s — one per session, or one for a named global namespace — and is what the Runner uses for
user-image artifact copy-in.

```go
// BlobStore is the byte backend for a single scoped namespace (a session or a
// global bucket). Keys are opaque strings; the factory binds account+container.
type BlobStore interface {
	Write(ctx context.Context, key string, r io.Reader) error
	Read(ctx context.Context, key string) (io.ReadCloser, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// BlobStoreFactory creates BlobStore instances scoped to a session or a global
// namespace.
type BlobStoreFactory interface {
	ForSession(ctx context.Context, sessionID string) (BlobStore, error)
	Global(namespace string) BlobStore
}
```

**Contract / lifecycle.**

- `Write` must create or overwrite atomically (or as close as the backend allows). It consumes `r`
  fully. On a mid-write failure the blob may be corrupt — callers do not retry partial writes; return
  an error and let the caller decide.
- `Read` returns an open `io.ReadCloser`; the caller closes it. Return a typed "not found" error (not
  `nil, nil`).
- `Exists` must not open the blob (no read-on-existence check). Used in hot paths.
- `Delete` must be idempotent — if the blob does not exist, return `nil`.
- `List` returns the keys under `prefix` in the store's namespace.
- Treat `key` as an opaque string; do not normalise it beyond what the backend requires.
- `BlobStoreFactory.ForSession` resolves the customer/job from the session row and returns a store
  bound to that container; `Global` binds a named namespace.

**Gotchas.**

- The `filesblob` reference impl guards against path traversal (rejects `..` components and verifies
  the resolved path stays under the root). A custom local-FS impl must replicate the guard; the cloud
  SDKs (GCS/S3/Azure) handle it at the SDK level.
- The same `BlobStore` bytes are shared by the artifact store and the `blobarchive` registry. When you
  construct both, back them with the same root/bucket so snapshot archives and artifacts are
  interoperable.

**Shipped references.**
- `extension/filesblob.BlobStore` — stores blobs under `root/<key>` on the local filesystem. Construct
  with `filesblob.NewBlobStore(root string)`.
- `extension/gcsblob.BlobStore` — Google Cloud Storage. Construct a single store with
  `gcsblob.NewBlobStore(client, bucket, prefix)`, or a factory with
  `gcsblob.NewBlobStoreFactory(ctx, gcsblob.Config{Bucket, Prefix}, opts...)`.

**Shipped mocks.** `agentkittest.NewMemBlobs()` (a `BlobStore`) and `agentkittest.NewMemBlobsFactory()`
(a `BlobStoreFactory` over a shared `MemBlobs`).

---

## `extension.ScopedClaimsIssuer`

**Source:** `go/extension/extension.go`

**Purpose.** Mints the per-session bearer token injected into the sandbox container environment and
forwarded on the message proxy. The in-image agent and any tool that calls back to the host API use
this token. It must be unforgeable and scoped to the session's tenant.

```go
// ScopedClaimsIssuer mints the per-session token injected into the instance and
// forwarded on the message proxy.
type ScopedClaimsIssuer interface {
	Issue(ctx context.Context, scope ContextScope, sessionID string) (token string, err error)
}
```

The `ContextScope` struct (also in `extension`):

```go
// ContextScope identifies who/what a turn is for.
type ContextScope struct {
	Customer  string
	Job       string
	Persona   string
	UserEmail string
}
```

**Contract / lifecycle.**

- Called once per `CreateSession` and once per `Resume`. The resulting token is held in memory only
  (not persisted) and injected as an environment variable into the sandbox container.
- The token is forwarded as a Bearer token on outbound tool calls from the sandbox back to the host
  API. The host's auth middleware must validate and trust it.
- Short-lived is correct. The runner re-issues on Resume so a session that resumes after hours gets a
  fresh token.

**Gotchas.**

- Do NOT use the reference issuer in production. `devclaims` is clearly labelled dev-only (single
  static secret, no rotation, no audience checks).
- The token escapes to the container environment. Use a secret isolated to the agent subsystem, not
  shared with broader infrastructure keys.

**Shipped reference.** `extension/devclaims.Issuer` — dev-only HS256 JWT issuer. Construct with
`devclaims.New(secret []byte)` (TTL 1 hour) or `devclaims.NewWithTTL(secret, ttl)`. **Do not use in
production.**

**Shipped mock.** `agentkittest.StaticClaims{Token: "test-token"}` — returns the same fixed string on
every call.

---

## `artifacts.ArtifactStore`

**Source:** `go/artifacts/artifacts.go`

**Purpose.** Persists and retrieves individual user-facing files the agent produces (reports, charts,
generated web apps). Distinct from session snapshots (whole-filesystem images for suspend/resume); see
[06 — Artifacts](06-artifacts.md) for the full state machine.

```go
type ArtifactStore interface {
	// Save upserts metadata (dedup on SessionID+FilePath) and, when content is
	// non-nil, persists bytes and sets Status=Extracted. Preserves the
	// live -> extracted, never-regress rule and write-once Source.
	//
	// When art.IsDir is true, content MUST be a tar stream: the impl untars it and
	// writes one blob per regular file under the artifact's blob PREFIX (BlobPath).
	// When art.IsDir is false, content is stored as a single blob.
	Save(ctx context.Context, art *Artifact, content io.Reader) (*Artifact, error)

	// Load returns metadata plus an open reader for the bytes. reader is nil if the
	// artifact is metadata-only (e.g. Lost).
	Load(ctx context.Context, artifactID string) (*Artifact, io.ReadCloser, error)

	// List returns all artifacts for a session.
	List(ctx context.Context, sessionID string) ([]*Artifact, error)

	// MarkLost flags still-Live artifacts for a session as Lost — but PROMOTES to
	// Extracted any that already have a BlobPath (the bytes are safe even though
	// the instance is gone).
	MarkLost(ctx context.Context, sessionID string) error

	// CaptureFolder slurps a named set of files (or a single file) from the supplied
	// reader (typically a tar stream from ExecutionEnvironment.Exec+tar or the
	// in-image /workspace/files/* endpoint), saves all bytes as a single artifact
	// identified by (sessionID, name), and returns the saved artifact.
	CaptureFolder(ctx context.Context, sessionID, name string, content io.Reader) (*Artifact, error)
}
```

**The `Artifact` struct** (from `go/artifacts/artifacts.go`):

```go
type Artifact struct {
	ID           string
	SessionID    string
	FilePath     string // dedup key with SessionID
	ArtifactType string // "file" | "code" | "image" | "data" | "webapp" (extensible)
	Status       Status
	BlobPath     string
	Label        string
	Description  string
	MimeType     string
	FileSize     int64
	Source       string            // "tool" | "auto" | "upload" — write-once
	Meta         map[string]string // host-specific fields live here to keep the type portable
}
```

**Status state machine:**

```
live ─┬─→ extracted          (bytes successfully persisted)
      ├─→ extraction_failed  (persist retries exhausted)
      └─→ lost               (instance destroyed before extraction; no blob)

extracted → [terminal] served from blob
lost      → [terminal] 410 Gone
```

**Contract / lifecycle.**

- `Save` is the upsert: dedup key is `SessionID + FilePath`. Never regress `extracted` → `live`.
  `Source` is write-once — ignore the caller's value once set. When `art.IsDir` is true, `content`
  must be a tar stream stored one blob per entry under `BlobPath`.
- `MarkLost` is called by the runner on `Destroy`. It must promote rather than lose if a `BlobPath`
  already exists (bytes are safe).
- `Load` returns `(art, nil, nil)` for metadata-only artifacts (status `lost`, no bytes).
- `CaptureFolder` is a building block for user images: it saves a tar stream as one artifact entry so
  the runner can seed a new container image from the blob.

**Gotchas.**

- The `Meta` bag is for host-specific fields (publish paths, brand colours, preview URLs). Keep them
  out of the `Artifact` struct so the library type stays portable across products.

**Shipped references.** `extension/blobartifacts.ArtifactStore` — blob bytes (any `extension.BlobStore`)
plus an in-process metadata map. Construct with `blobartifacts.New(blobs extension.BlobStore)`, or use
the filesystem convenience `filesblob.NewArtifactStore(blobs *filesblob.BlobStore)` (which returns a
`*blobartifacts.ArtifactStore`). The metadata map is **not** durable across restarts (by design for
dev/local); for production, back the metadata rows with your host DB and keep bytes in the `BlobStore`.

**Shipped mock.** `artifacts.NewMock()` — fully in-memory, enforces the same status semantics and the
never-regress / write-once rules.

---

## `execenv.ExecutionEnvironment`

**Source:** `go/execenv/execenv.go`

**Purpose.** Runs agent sessions inside container images. The orchestration core above it is
engine-agnostic; only this interface and its concrete adapter know about Docker, K8s, or any other
runtime. All session lifecycle operations (provision, suspend, resume, snapshot, exec, destroy) flow
through it.

```go
type ExecutionEnvironment interface {
	// Provision makes a running instance of the in-image agent for a session and
	// returns a handle including the address to reach its HTTP server. The image
	// must already be present (see ImageRegistry.EnsurePresent).
	Provision(ctx context.Context, spec ProvisionSpec) (*Instance, error)

	// Suspend stops the instance while preserving its filesystem so Resume can
	// bring it back cheaply. Idempotent if already suspended.
	Suspend(ctx context.Context, id InstanceID) error

	// Resume restarts a suspended instance and blocks until its agent is healthy.
	Resume(ctx context.Context, id InstanceID) (*Instance, error)

	// Exec runs a one-off command inside the instance (workspace listing, secret
	// scan, snapshot prep) — not the agent turn itself.
	Exec(ctx context.Context, id InstanceID, cmd []string, opts ExecOptions) (*ExecResult, error)

	// Snapshot captures the instance's filesystem as an image and returns a ref.
	Snapshot(ctx context.Context, id InstanceID, opts SnapshotOptions) (ImageRef, error)

	// Destroy tears the instance down and releases its resources.
	Destroy(ctx context.Context, id InstanceID, opts DestroyOptions) error

	// Status reports the live runtime state of an instance.
	Status(ctx context.Context, id InstanceID) (*InstanceStatus, error)

	// Recover lists instances this environment still manages (e.g. labelled
	// containers that survived a host restart) for re-adoption on startup.
	Recover(ctx context.Context) ([]*Instance, error)

	// OnDestroy registers a callback fired when any instance is destroyed.
	OnDestroy(cb func(id InstanceID))

	// Capabilities describes what this environment supports.
	Capabilities() Capabilities
}
```

**Key supporting types** (from `go/execenv/execenv.go`):

```go
// Capabilities lets the orchestration core adapt policy to the engine.
type Capabilities struct {
	SupportsSuspend  bool
	SupportsSnapshot bool
	SupportsExec     bool

	// IsolatedPerSession is deprecated: derived as Tenancy == TenancyPerSession.
	IsolatedPerSession bool

	Backend       Backend       // placement mechanism (descriptive; metrics/logging)
	Tenancy       Tenancy       // per-session vs shared — the Runner branches on this
	IsolationTier IsolationTier // trust boundary between sessions
}
```

**Contract / lifecycle.**

- `Provision` must block until the in-image agent is healthy at `Instance.Address` and return an
  `*Instance` whose `Address` is reachable from the host.
- `Suspend` is optional (`Capabilities.SupportsSuspend`). If unsupported, return `nil` — the runner
  falls back to snapshot-and-destroy.
- `Resume` must re-provision from a suspended state and again block until healthy.
- `Exec` backs workspace listing, secret scanning, and artifact capture (via tar). If `SupportsExec`
  is false, snapshot-prep and folder capture are unavailable.
- `Snapshot` captures the *running* filesystem; the result is passed to `ImageRegistry.Persist`.
- `OnDestroy` callbacks fire in (or just after) `Destroy`. The runner registers one to call
  `ArtifactStore.MarkLost`.
- `Recover` is called by `Runner.Start` to re-adopt orphaned instances from a previous process.
  Return an empty slice (not an error) if nothing to recover.

**Trust gate.** The Runner's trust gate (enforced in `fleet.Register`, and therefore at `NewRunner`
when `Env` is auto-wrapped) prevents shared-tenancy environments (`TenancyShared`) from running where
`Policy.TrustedWorkload` is false unless `IsolationTier >= TierVM`. Violating it returns an error from
`NewRunner`.

**Shipped reference.** `execenv/docker.DinD` — Docker-in-Docker adapter, provisions per-session
containers over TCP. Construct with `dockerdind.NewDinD(dockerdind.DinDConfig{DockerHost,
PortRangeStart, PortRangeEnd, GatewayIP, ...})`. See `go/execenv/docker/dind.go`.

**Shipped mock.** `execenv.NewMock()` — in-memory, records every call. Supports pointing at an
`httptest.Server` for sandbox HTTP contract tests.

---

## `imageregistry.ImageRegistry`

**Source:** `go/imageregistry/registry.go`

**Purpose.** Makes images available to the execution environment and durably persists images produced
by running sessions (snapshots for suspend/resume, user images for personalisation). Orthogonal to
`ExecutionEnvironment` — the two are composed, not merged.

```go
type ImageRegistry interface {
	// EnsurePresent guarantees the image is available to the engine that will run
	// it (pull if needed; no-op if locally built/loaded).
	EnsurePresent(ctx context.Context, ref execenv.ImageRef) error

	// Build produces an image from a build context and returns the resulting ref.
	Build(ctx context.Context, spec BuildSpec) (execenv.ImageRef, error)

	// Resolve returns an existing ref for a BuildSpec WITHOUT building (ok=false on
	// cache miss). Tags are content-addressed, so a returning user with unchanged
	// customisations cache-hits instantly.
	Resolve(ctx context.Context, spec BuildSpec) (ref execenv.ImageRef, ok bool, err error)

	// Persist stores an in-engine image ref durably and returns a Handle that
	// survives process/host restarts.
	Persist(ctx context.Context, ref execenv.ImageRef, opts PersistOptions) (Handle, error)

	// Materialize is the inverse of Persist: make the persisted image present for
	// the engine and return a runnable ref.
	Materialize(ctx context.Context, h Handle) (execenv.ImageRef, error)

	// Remove discards a persisted image.
	Remove(ctx context.Context, h Handle) error

	// Capabilities lets the orchestration core adapt (e.g. force full snapshots
	// when the registry cannot do diffs).
	Capabilities() Capabilities
}
```

**The `Handle` struct** (from `go/imageregistry/registry.go`):

```go
// Handle is an opaque, durable, JSON-serialisable pointer to a persisted image.
// Its concrete meaning is adapter-specific. The core stores it on the session row
// and passes it back to Materialize on restore.
type Handle struct {
	Kind string            `json:"kind"` // "local-tar" | "blob-archive" | "registry"
	Ref  string            `json:"ref"`  // path / blobPath / registry-ref
	Meta map[string]string `json:"meta,omitempty"`
}
```

**The `Capabilities` struct:**

```go
type Capabilities struct {
	SupportsDiff   bool
	SupportsBuild  bool
	SupportsRemote bool
	// PortableHandles is true when a Handle produced here can be Materialized on a
	// DIFFERENT worker (blob-archive with a shared BlobStore, or a remote registry).
	// Multi-worker fleets require PortableHandles=true; validated at Fleet construction.
	PortableHandles bool
}
```

**Contract / lifecycle.**

- `EnsurePresent` is called before every `Provision`. Idempotent and cheap when the image is already
  present. For `blobarchive` this is a no-op (the image is loaded by `Materialize`).
- `Build` produces user images on a cache miss and must yield a runnable ref accessible to the engine.
- `Resolve` is the cache-hit fast path. If the content-hash tag exists (locally or in the registry),
  return it with `ok=true` so `Build` is skipped.
- `Persist` / `Materialize` are the snapshot round-trip for suspend/resume. The returned `Handle` is
  stored via `RunnerStore.SetSnapshotHandle` and passed back to `Materialize` on `Resume`.
- `PortableHandles` must be `true` for multi-worker fleets ([13](13-fleet-placement.md)).

**Gotchas.**

- `Handle` is stored as JSON on the session row. Changing its structure breaks existing persisted
  handles — treat it as a durable contract.
- `blobarchive` requires the Docker daemon at the same `dockerHost` the `DinD` environment uses; both
  share the same `DOCKER_HOST` in the standalone stack.

**Shipped references.**
- `imageregistry/blobarchive.Registry` — Docker save → gzip → `BlobStore`. Portable handles.
  Construct with `blobarchive.New(dockerHost string, blobs extension.BlobStore)`.
- `imageregistry/ociregistry.Registry` — push/pull a real OCI registry. Remote, portable handles.
  Construct with `ociregistry.New(ociregistry.Config{DockerHost, Registry, Auth, ...})`. `Auth` is an
  `imageregistry/auth.Provider`: `auth.Static(username, password)` for basic auth, or
  `auth.GCP(ctx)` for Google Artifact Registry via Application Default Credentials.

**Shipped mock.** `imageregistry.NewMock()` — in-memory, records every call; `Capabilities()` returns
`PortableHandles: true` so fleet tests pass.

---

## `extension.SessionContextProvider`

**Source:** `go/extension/extension.go`

**Purpose.** Resolves the per-session context (system prompt append and base image) for a turn. The
runner appends the returned `SystemPrompt` to the session system prompt before sending the message to
the in-image agent; it never interprets the content.

```go
// SessionContext carries the resolved per-session context.
type SessionContext struct {
	SystemPrompt string
	BaseImage    string
}

// SessionContextProvider assembles the per-session context for a turn. The Runner
// appends the result to systemPrompt and never interprets it. Default (nil)
// contributes "".
type SessionContextProvider interface {
	Resolve(ctx context.Context, scope ContextScope) (*SessionContext, error)
}
```

**Required?** No. Pass `nil` in `Deps.SessionContext`; the runner contributes an empty context
segment.

**Contract.** Must be fast (called on the hot path of every `SendMessage`). Return an empty
`SystemPrompt` if there is nothing to add — do not error for missing config. Errors propagate to the
caller as a 500-equivalent.

---

## `extension.TokenUsageLogger`

**Source:** `go/extension/extension.go`

**Purpose.** Receives token usage parsed from `query_complete` / `result` SSE events emitted by the
in-image agent. The runner calls `Log` after each completed query; a host typically writes a cost row.

```go
// TokenUsageLogger receives usage for costing. Default (nil) is a no-op.
type TokenUsageLogger interface {
	Log(ctx context.Context, sessionID string, usage Usage)
}

// Usage is token usage parsed from query_complete/result events.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalCostUSD float64
	Model        string
}
```

**Required?** No. Pass `nil` in `Deps.TokenLogger`; usage is silently discarded.

**Contract.** `Log` must not block for long — it is called synchronously in the event-pipeline
compaction path. Fire-and-forget to a channel or goroutine if the write is slow.

---

## `extension.ArtifactEnricher`

**Source:** `go/extension/extension.go`

**Purpose.** Lets the host decorate `artifacts.Artifact` metadata before it is persisted (publish
paths, brand colours, preview labels). Called by the runner just before each `ArtifactStore.Save`.

```go
// ArtifactEnricher lets the host decorate artifact metadata before persistence.
// Default (nil) is identity.
type ArtifactEnricher interface {
	Enrich(ctx context.Context, art *artifacts.Artifact) error
}
```

**Required?** No. Pass `nil` in `Deps.Enricher`; the artifact is saved as-is.

**Contract.** Mutate `art` in place. Do not replace the pointer — the caller holds the same pointer.
Errors fail the save.

---

## `extension.Metrics`

**Source:** `go/extension/extension.go`

**Purpose.** Pluggable metrics surface. The runner calls these on lifecycle transitions, active
session count changes, and turn completions; a host typically adapts them to Prometheus.

```go
// Metrics is the pluggable metrics surface. Default (nil) is a no-op.
type Metrics interface {
	ObserveLifecycle(phase string, seconds float64)
	SetGauge(name string, v float64)
	Inc(name string)
}
```

**Required?** No. Pass `nil` in `Deps.Metrics`; all instrumentation is skipped.

**Contract.** All three methods must be non-blocking and must not panic. `phase` and `name` are
library-internal strings — the host may map them to labels, ignore unknown names, or relay them
verbatim.

---

## Plugin seams

Beyond the Go interfaces, two plugin surfaces let a product extend behaviour end to end:

- **In-image tool plugins** ([07](07-in-image-agent.md)) — a typed tool (`sdkTool` + marker) registered
  into the sandbox's tool registry. The plugin's output is a schema-validated, code-constructed event
  the product frontend renders. Reference bundles live in each project's `installations/<name>/plugins/`.
- **Browser render plugins** ([05](05-event-streaming.md#rendering-in-the-web-package)) — a `RenderPlugin` (event types +
  reduce + render) registered into the chat UI. It renders the extension events a tool plugin emits.
- **Extension event types** — the names that flow tool plugin → SSE → render plugin
  (`table_rendered`, …). Declared once per product, dispatched by name end to end.

### The app image contract

The sandbox base image (`sandbox/Dockerfile` → `agentkit-sandbox:<tag>`) owns the harness: Node
runtime, the agent SDK/CLI, the control server, `/workspace`'s existence, port 3010, the healthcheck,
and `IS_SANDBOX`. An app image is built `FROM` it (parameterize the tag with `ARG BASE_IMAGE`) and
never reinstalls or re-pins any harness concern. It adds exactly three ingredients:

| Ingredient | What it is | Where it goes |
|---|---|---|
| **Plugins** | Typed tools with a product contract (UI rendering, host mutations) — code, because the event shape is guaranteed. | `PRODUCT_PLUGINS_DIR=/app/product-plugins` |
| **Environment** | Binaries and packages behind Bash (CLIs, python stack, helper libs) — capabilities whose output is text/files for the model. | `/usr/local/bin`, apt/pip/npm layers, `/workspace/lib` |
| **Knowledge** | Skills + CLAUDE.md — text mapping the model onto the environment and plugins. | `/workspace/.claude/skills/`, `/workspace/CLAUDE.md` |

Rules:

- The base never writes into the extension paths; app images populate them; dev-mode mounts
  (`Policy.Mounts`) override exactly these same paths.
- The system prompt composes in three non-colliding layers: the SDK built-in preset + per-session host
  `append` (from `SessionContextProvider`) + the image-baked `/workspace/CLAUDE.md`. There is no
  CLAUDE.md merging.
- Skills are only valid in images whose Dockerfile provides the environment they assume — skills and
  environment ship together in the app layer.
- The "should this be a plugin?" test: **must the product understand the result?** A search CLI's
  output is for the model (environment); a render tool's output is for the user's screen (plugin).

Reference layering (sandbox harness → `core` → `example` → per-project) lives in
`installations/core` and `installations/example`; see `installations/README.md` (derived-image tree) and
`installations/README.md`.

---

## Deployment shapes

The same orchestration core runs in three shapes by composing one `ExecutionEnvironment` with one
`ImageRegistry`. Everything above those two interfaces — lifecycle, reaper, archive loop, flush guard,
recovery, event pipeline, artifact store, the in-image agent, the web UI — is written once and does
not change between shapes.

### A — Dev / tests: mock or single Docker

The cheapest setup, and the default for unit and integration tests: `execenv.NewMock()` +
`imageregistry.NewMock()` + `agentkittest.NewMemStore()` + `artifacts.NewMock()` +
`agentkittest.StaticClaims{}`. No Docker, no database, runs in milliseconds. A local dev loop can
instead point a single Docker daemon at a shared container; suspend is off and snapshots (if used) are
local. This is the wiring in the "tests swap every adapter for its mock" snippet above.

### B — DinD + blob archive (what runs today)

This is what `agentd` and the standalone stack run **today**. Real adapters:

- **Engine:** `execenv/docker` DinD — one container per session via a Docker-in-Docker daemon; the
  host reaches each agent on `localhost:<leasedPort>` from a port pool; `Recover()` re-adopts labelled
  containers on host restart.
- **Registry:** `imageregistry/blobarchive` (Docker save → gzip → `BlobStore`, portable handles), or
  `imageregistry/ociregistry` for push/pull against a real registry with `auth.Static` (basic) or
  `auth.GCP` (Artifact Registry via ADC).
- **Blobs:** `extension/filesblob` (local FS) or `extension/gcsblob` (GCS).
- **Store:** `extension/sqlitestore` (single-server dev) or `agentdb.Store` (Postgres, production).
- **Model proxy:** key injection and model-id rewrite run as a small host-side service the session's
  `ANTHROPIC_BASE_URL` points at (`go/cmd/agentd/modelproxy.go`).

`agentd` selects these backends from the environment (`AGENTKIT_BLOB_BACKEND=fs|gcs`,
`AGENTKIT_REGISTRY_BACKEND=blobarchive|ociregistry`, plus `GCS_BUCKET`, `OCI_REGISTRY` /
`GCP_REGION`+`GCP_PROJECT`+`GCP_AR_REPO`); defaults preserve the local fs stack. See
`go/cmd/agentd/backends.go` and [15 — Standalone stack](15-standalone-stack.md).

### C — Kubernetes + remote registry (future)

The shape the architecture is designed to make possible, but which has **no shipped
`ExecutionEnvironment` adapter yet**. Each session would be a Pod (`Provision` creates it,
`Suspend`/`Resume` scale it, `Destroy` deletes it); addressing is by Service/pod-IP, so the DinD port
pool is simply absent. The registry half already exists: `ociregistry` gives remote push/pull
(`EnsurePresent` = pull, `Persist` = push) with portable handles, so a fleet spanning K8s workers is
supported the moment a pod-based environment adapter is written. Snapshotting on K8s is the open
question ([02 — Execution environment](02-execution-environment.md)): workspace-only volume snapshots,
a buildkit sidecar, or stateless sessions — chosen per product and signalled via
`Capabilities.SupportsSnapshot`.

For multi-worker placement across any of these shapes, see [13 — Fleet placement](13-fleet-placement.md).

---

## Wiring example

The canonical reference wiring is `go/examples/standalone/main.go` (drives a real DinD daemon
directly). Abbreviated, with real signatures:

```go
// Reference adapters (dev/single-server flavour of shape B).
store, _    := sqlitestore.Open(filepath.Join(dataDir, "sessions.db")) // or agentdb.Open(pgURL)
blobs       := filesblob.NewBlobStore(filepath.Join(dataDir, "blobs"))
artStore    := filesblob.NewArtifactStore(blobs)
claims      := devclaims.New([]byte(secret))          // DEV ONLY
registry, _ := blobarchive.New(dockerHost, blobs)
dindEnv, _  := dockerdind.NewDinD(dockerdind.DinDConfig{
	DockerHost:     dockerHost,
	PortRangeStart: 30001,
	PortRangeEnd:   30100,
	GatewayIP:      "172.17.0.1",
})

runner, _ := agentkit.NewRunner(agentkit.Deps{
	Env:       dindEnv,   // wrapped as a one-worker fleet; pass Fleet directly to scale (13)
	Registry:  registry,
	Store:     store,
	Artifacts: artStore,
	Claims:    claims,
	// Blobs, SessionContext, TokenLogger, Enricher, Metrics: nil → defaults
	Policy: agentkit.Policy{
		BaseImage: "agentkit-sandbox:dev",
		AgentPort: 3010,
		SessionEnv: map[string]string{"ANTHROPIC_BASE_URL": modelProxyURL},
	},
})
runner.Start(ctx)
```

To expose the Runner over HTTP, wrap it in `httpapi` (this is what `agentd` does):

```go
api, _ := httpapi.New(httpapi.Config{
	Runner:    runner,
	Store:     store,
	Artifacts: artStore,
	Identity:  identityFromRequest, // host maps the request to an Identity
})
http.ListenAndServe(":8099", authMiddleware(api.Mux()))
```

For tests, swap every adapter for its mock and leave optional fields nil (see the "tests swap"
snippet under *Constructing the Runner* above).

---

## See also

- [13 — Fleet placement](13-fleet-placement.md) — placing sessions across multiple workers.
- [15 — Standalone stack](15-standalone-stack.md) — the pre-built `agentd` host and one-command stack.
- [02 — Execution environment](02-execution-environment.md) — `ExecutionEnvironment` in depth.
- [03 — Image registry](03-image-registry.md) — `ImageRegistry` in depth.
- [06 — Artifacts](06-artifacts.md) — `ArtifactStore` state machine and extraction patterns.
- `installations/README.md` — installations and image layering (derived-image tree, overlays, `imagetree`).
</content>
</invoke>
