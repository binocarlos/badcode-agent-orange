# 14 — Host adapters reference

This is the document a **host application author** reads when wiring agentkit into their product. It
covers every extension interface the library consumes, the exact method signatures (copied from source),
the contract each method must honour, lifecycle and gotchas, whether the interface is required or
optional, and which shipped implementation or mock to start from.

The companion wiring walkthrough is in [10 — Extension points](10-extension-points.md). The canonical
wiring example is `go/examples/server/main.go`.

---

## Quick-reference table

| Interface | Package | Required? | Shipped reference | Shipped mock |
|---|---|---|---|---|
| `SessionStore` | `extension` | **Yes** | `extension/sqlitestore` (`sqlitestore.Open`) | `agentkittest.NewMemStore()` |
| `BlobStore` | `extension` | **Yes** | `extension/filesblob` (`filesblob.NewBlobStore`) | `agentkittest.NewMemBlobs()` (via `MemStore.Blobs()`) |
| `ScopedClaimsIssuer` | `extension` | **Yes** | `extension/devclaims` (`devclaims.New`) — dev only | `agentkittest.StaticClaims{Token: "..."}` |
| `artifacts.ArtifactStore` | `artifacts` | **Yes** | `extension/filesblob` (`filesblob.NewArtifactStore`) | `artifacts.NewMock()` |
| `ExecutionEnvironment` | `execenv` | **Yes** (Fleet OR Env) | `execenv/docker` (`dockerdind.NewDinD`) | `execenv.NewMock()` |
| `ImageRegistry` | `imageregistry` | **Yes** | `imageregistry/blobarchive` (`blobarchive.New`) or `imageregistry/localbuild` (`localbuild.New`) | `imageregistry.NewMock()` |
| `OrgContextProvider` | `extension` | No — default `""` | host-written | leave `nil` |
| `TokenUsageLogger` | `extension` | No — no-op | host-written | leave `nil` |
| `ArtifactEnricher` | `extension` | No — identity | host-written | leave `nil` |
| `Metrics` | `extension` | No — no-op | host-written | leave `nil` |

**Required?** is grounded in `NewRunner` nil-handling in `go/agentkit.go`:

- `Fleet`/`Env` — one must be non-nil; `NewRunner` returns an error if both are nil.
- `Registry`, `Store`, `Artifacts`, `Claims` — no explicit nil-guard in `NewRunner`, but any nil here
  panics at first use. Treat as required.
- `OrgContext`, `TokenLogger`, `Enricher`, `Metrics` — documented in `Deps` as "optional"; the runner
  skips them when nil and the doc comment says "Default (nil) is a no-op / contributes '' / identity".
- `HTTPClient` — optional; nil is replaced with `&http.Client{}` (no timeout, correct for SSE).

---

## `extension.SessionStore`

**Source:** `go/extension/extension.go`

**Purpose.** Durable session identity. The library owns runtime state only; everything that must
survive a process restart (session rows, query events, snapshot handles, worker bindings) lives here.
`NewRunner` calls these methods; it never writes to its own persistent store.

```go
// SessionStore is durable identity — the host owns session/query-event rows and
// the snapshot handle. The library calls these; it never persists on its own.
// In Platinum this is an adapter over store.Store.
type SessionStore interface {
	GetSession(ctx context.Context, id string) (*Session, error)
	UpdateSession(ctx context.Context, s *Session) error

	SetSnapshotHandle(ctx context.Context, sessionID string, h imageregistry.Handle) error
	GetSnapshotHandle(ctx context.Context, sessionID string) (h imageregistry.Handle, ok bool, err error)

	PersistQueryEvents(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error
	ListQueryEvents(ctx context.Context, sessionID string) ([]events.Envelope, error)

	Blobs() BlobStore

	// Worker-binding methods: record the sticky session→worker placement so that
	// two host replicas behind a load balancer both resolve the same worker for a
	// session. The binding is durable identity — stored here alongside snapshot
	// handles rather than in fleet library memory.
	GetWorkerBinding(ctx context.Context, sessionID string) (workerID string, ok bool, err error)
	SetWorkerBinding(ctx context.Context, sessionID, workerID string) error
	ClearWorkerBinding(ctx context.Context, sessionID string) error
}
```

**Contract / lifecycle.**

- `GetSession` is called before every runner operation to load the durable row. Return a typed error
  (not nil + panic) if the session is not found.
- `UpdateSession` is called when the runner changes session state (e.g. suspending, resuming, archiving).
  Implementations should upsert — callers may not have created the row yet if the host has a separate
  creation path.
- `SetSnapshotHandle` / `GetSnapshotHandle` round-trip the opaque `imageregistry.Handle` JSON. The
  runner reads the handle on Resume to restore from a snapshot; write it atomically with the session
  state.
- `PersistQueryEvents` upserts the full compacted `[]events.Envelope` slice for a `(sessionID,
  queryID)` pair. Existing rows for the same pair are overwritten (no append). `searchText` is the
  full-text search hint — store it alongside for FTS indexing.
- `ListQueryEvents` must return all events for a session across all query IDs, ordered by insertion
  time (rowid / insertion order), flat.
- `Blobs()` must return the same `BlobStore` the host uses for artifact bytes — the runner uses it to
  read/write blobs for the host's storage layer.
- Worker-binding methods support sticky placement in multi-worker fleets. A single-worker host can
  store these in memory (the reference `sqlitestore` uses a dedicated DB table).

**Gotchas.**

- `GetSession` returning `sql.ErrNoRows` (wrapped) is correct; returning `nil, nil` is not — the
  runner will dereference the nil pointer.
- `PersistQueryEvents` is called during the active query (compaction). Keep it non-blocking; avoid
  long transactions.

**Shipped reference.** `extension/sqlitestore.Store` — SQLite via `modernc.org/sqlite` (pure Go, no
cgo). Open with `sqlitestore.Open(path)`. Also exposes `Blobs()` returning a `filesblob.BlobStore`
rooted next to the DB file. Suitable for single-server dev and local examples; for production, adapt
to your host DB.

**Shipped mock.** `agentkittest.NewMemStore()` — in-memory, goroutine-safe. Call `store.Seed(sess)`
to pre-populate a session row. The mock's `Blobs()` returns a `*MemBlobs` (also in `agentkittest`).

---

## `extension.BlobStore`

**Source:** `go/extension/extension.go`

**Purpose.** Byte backend for artifacts and snapshot archives. `container` / `path` mirror the
customer / job / blob model used throughout Platinum (e.g. `("sessions", "archives/abc123.tar.gz")`).
The interface is intentionally minimal — no listing, no metadata — so it maps cleanly to Azure Blob
Storage, S3, GCS, or a local filesystem.

```go
// BlobStore is the byte backend for artifacts and snapshots (Azure/fs/hybrid in
// Platinum's storage.BlobStore). container/path mirror the customer/job/blob model.
type BlobStore interface {
	Write(ctx context.Context, container, path string, r io.Reader) error
	Read(ctx context.Context, container, path string) (io.ReadCloser, error)
	Exists(ctx context.Context, container, path string) (bool, error)
	Delete(ctx context.Context, container, path string) error
}
```

**Contract / lifecycle.**

- `Write` must create or overwrite atomically (or as close as the backend allows). It consumes `r`
  fully. If the underlying write fails midway, the blob may be corrupt — callers do not retry partial
  writes; return an error and let the caller decide.
- `Read` returns an open `io.ReadCloser`; the caller closes it. Return a typed "not found" error (not
  `nil, nil`).
- `Exists` must not open the blob (no read-on-existence check). Used in hot paths.
- `Delete` must be idempotent — if the blob does not exist, return `nil`.
- `container` and `path` must be treated as opaque keys. Do not normalise them beyond what the backend
  requires.

**Gotchas.**

- The `filesblob` reference impl guards against path-traversal by rejecting `..` components and
  verifying the resolved path stays under the root. Production backends (Azure/S3) do this at the SDK
  level, but a custom impl over a local FS should replicate the guard.
- `BlobStore` is returned by `SessionStore.Blobs()`. Do not construct a second, independent
  `BlobStore` instance that points to the same storage but with a different root — the `blobarchive`
  registry and the artifact store share the same `BlobStore` in the reference server; they must be the
  same object (or at least the same root) so blobs are interoperable.

**Shipped reference.** `extension/filesblob.BlobStore` — stores blobs under `root/<container>/<path>`
on the local filesystem. Construct with `filesblob.NewBlobStore(root string)`. `sqlitestore.Store`
wraps one of these at `<dbdir>/blobs` and exposes it via `Blobs()`.

**Shipped mock.** `agentkittest.MemBlobs` (accessed via `agentkittest.NewMemStore().Blobs()`).

---

## `extension.ScopedClaimsIssuer`

**Source:** `go/extension/extension.go`

**Purpose.** Mints the per-session bearer token injected into the sandbox container environment and
forwarded on the message proxy. The in-image agent and any tool that calls back to the host API use
this token. It must be unforgeable and scoped to the session's tenant.

```go
// ScopedClaimsIssuer mints the per-session token injected into the instance and
// forwarded on the message proxy. Platinum issues an HS256 JWT scoped to
// customer/job/session.
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

- Called once per `CreateSession` and once per `Resume`. The resulting token is stored in memory only
  (not persisted) and injected as an environment variable into the sandbox container.
- The token is forwarded as a Bearer token on outbound tool calls from the sandbox back to the host
  API. The host's auth middleware must validate and trust it.
- TTL: short-lived is correct. The runner re-issues on Resume so a session that resumes after hours
  gets a fresh token.

**Gotchas.**

- Do NOT use this in production with a static secret. The reference `devclaims` package is clearly
  labelled dev-only (single static secret, no rotation, no audience checks).
- The token escapes to the container environment. Use a secret that is isolated to the agentkit
  subsystem, not shared with broader infrastructure keys.

**Shipped reference.** `extension/devclaims.Issuer` — dev-only HS256 JWT issuer. Construct with
`devclaims.New(secret []byte)`. TTL is 1 hour. **Do not use in production.**

**Shipped mock.** `agentkittest.StaticClaims{Token: "test-token"}` — returns the same fixed string
on every call.

---

## `artifacts.ArtifactStore`

**Source:** `go/artifacts/artifacts.go`

**Purpose.** Persists and retrieves individual user-facing files the agent produces (reports, charts,
generated web apps). Distinct from session snapshots (whole-filesystem images for suspend/resume); see
[06 — Artifacts](06-artifacts.md) for the full state machine explanation.

```go
// ArtifactStore persists and retrieves agent artifacts. The real impl wraps a
// metadata store (rows) + a BlobStore (bytes); the mock keeps both in memory with
// identical semantics.
type ArtifactStore interface {
	// Save upserts metadata (dedup on SessionID+FilePath) and, when content is
	// non-nil, uploads bytes and sets Status=Extracted. Preserves the
	// live -> extracted, never-regress rule and write-once Source.
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

	// CaptureFolder slurps a named set of files (or a single file — the degenerate
	// case) from the supplied reader (typically a tar stream produced by
	// ExecutionEnvironment.Exec+tar or the in-image /workspace/files/* endpoint),
	// saves all bytes as a single artifact identified by (sessionID, name), and
	// returns the saved artifact.  The content is stored under the artifact's
	// FilePath = name (the caller can choose any path-safe name).
	//
	// This is the generalised folder/file-set capture described in docs/06-artifacts.md
	// and is the building block for user images (AG-7).
	CaptureFolder(ctx context.Context, sessionID, name string, content io.Reader) (*Artifact, error)
}
```

**The `Artifact` struct** (from `go/artifacts/artifacts.go`):

```go
// Artifact is the generic, portable artifact shape (a redefinition of Platinum's
// types.AgentArtifact so the library imports nothing from goapi).
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
live ─┬─→ extracted          (bytes successfully uploaded to blob)
      ├─→ extraction_failed  (upload retries exhausted)
      └─→ lost               (instance destroyed before extraction; no blob)

extracted → [terminal] served from blob
lost      → [terminal] 410 Gone
```

**Contract / lifecycle.**

- `Save` is the upsert: dedup key is `SessionID + FilePath`. Never regress `extracted` → `live`. If
  an existing row is `extracted` and the caller passes `Status=live`, keep `extracted`. `Source` is
  write-once — ignore the caller's value once it is set.
- `MarkLost` is called by the runner on `Destroy`. It must promote rather than lose if a `BlobPath`
  already exists (bytes are safe).
- `Load` returns `(art, nil, nil)` for metadata-only artifacts (status `lost`, no bytes). The caller
  handles the nil reader.
- `CaptureFolder` is the building block for user images: it saves an entire tar stream as one artifact
  entry so the runner can seed a new container image from the blob.

**Gotchas.**

- The `Meta` bag is for host-specific fields (publish paths, brand colours, office-preview URLs).
  Do not add host fields to the `Artifact` struct — put them in `Meta` so the library type remains
  portable across products.

**Shipped reference.** `extension/filesblob.ArtifactStore` — filesystem bytes + in-process metadata
map. Construct with `filesblob.NewArtifactStore(blobs *filesblob.BlobStore)`. The metadata map is
**not** durable across restarts (by design for dev/local use). For production, back the metadata rows
with your host DB and use the `BlobStore` for bytes.

**Shipped mock.** `artifacts.NewMock()` — fully in-memory, enforces the same status semantics and
the never-regress / write-once rules.

---

## `execenv.ExecutionEnvironment`

**Source:** `go/execenv/execenv.go`

**Purpose.** Runs agent sessions inside container images. The orchestration core above it is
engine-agnostic; only this interface and its concrete adapter know about Docker, K8s, or any other
runtime. All session lifecycle operations (provision, suspend, resume, snapshot, exec, destroy) flow
through this interface.

```go
// ExecutionEnvironment runs agent sessions inside container images.
// Implementations decide the mechanism (a fresh Docker container, a DinD
// container, a Kubernetes pod, or an exec into a shared container); the
// orchestration core above it is identical across all of them.
type ExecutionEnvironment interface {
	// Provision makes a running instance of the in-image agent for a session and
	// returns a handle including the address to reach its HTTP server. The image
	// must already be present (see ImageRegistry.EnsurePresent).
	Provision(ctx context.Context, spec ProvisionSpec) (*Instance, error)

	// Suspend stops the instance while preserving its filesystem so Resume can
	// bring it back cheaply. Idempotent if already suspended.
	Suspend(ctx context.Context, id InstanceID) error

	// Resume restarts a suspended instance and blocks until its agent is healthy
	// (or ctx fires). Returns the (possibly changed) instance.
	Resume(ctx context.Context, id InstanceID) (*Instance, error)

	// Exec runs a one-off command inside the instance (workspace listing, secret
	// scan, snapshot prep) — not the agent turn itself.
	Exec(ctx context.Context, id InstanceID, cmd []string, opts ExecOptions) (*ExecResult, error)

	// Snapshot captures the instance's filesystem as an image and returns a ref.
	// Persisting the ref durably is the ImageRegistry's job.
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
	// Retained for one release for back-compat; use Tenancy instead.
	IsolatedPerSession bool

	// Backend identifies the placement mechanism (descriptive; for metrics/logging).
	Backend Backend
	// Tenancy is the axis the Runner branches on: per-session vs shared.
	Tenancy Tenancy
	// IsolationTier is the trust boundary between sessions. The trust gate at
	// NewRunner enforces that shared-tenancy without TrustedWorkload requires TierVM.
	IsolationTier IsolationTier
}

// ExecOptions configures a one-off command.
type ExecOptions struct {
	WorkingDir string
	Env        map[string]string
	Stdin      io.Reader
	Timeout    time.Duration
}

// ExecResult is the outcome of an Exec.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}
```

**Contract / lifecycle.**

- `Provision` must block until the in-image agent is healthy (i.e. returns a successful health check
  at `Instance.Address`). Return an `*Instance` whose `Address` is reachable from the host.
- `Suspend` is optional (`Capabilities.SupportsSuspend`). If unsupported, return `nil` — the runner
  will fall back to snapshot-and-destroy.
- `Resume` must re-provision from a suspended state and again block until healthy.
- `Exec` is used by the runner for workspace listing, secret scanning, and artifact capture (via tar).
  If `SupportsExec` is false, certain features (snapshot-prep, folder capture) are unavailable.
- `Snapshot` captures the *running* filesystem. The runner calls this before archiving; the result is
  passed to `ImageRegistry.Persist`.
- `OnDestroy` callbacks fire synchronously in the Destroy call (or from an asynchronous cleanup
  goroutine). The runner registers a callback to call `ArtifactStore.MarkLost`.
- `Recover` is called by `Runner.Start` to re-adopt orphaned instances from a previous process.
  Return an empty slice (not an error) if nothing to recover.

**Trust gate.** The runner's trust gate (enforced in `fleet.Register`) prevents shared-tenancy
containers (`TenancyShared`) from running on workers where `Policy.TrustedWorkload` is false unless
`IsolationTier >= TierVM`. Violating this returns an error from `NewRunner`.

**Shipped reference.** `execenv/docker.DinD` — Docker-in-Docker adapter, provisions per-session
containers over TCP. Construct with `dockerdind.NewDinD(dockerdind.DinDConfig{...})`. See
`go/execenv/docker/dind.go` for `DinDConfig` fields.

**Shipped mock.** `execenv.NewMock()` (in `go/execenv/mock.go`) — in-memory, records every call.
Supports `AddrOverride` to point at an `httptest.Server` for sandbox HTTP contract tests.

---

## `imageregistry.ImageRegistry`

**Source:** `go/imageregistry/registry.go`

**Purpose.** Makes images available to the execution environment and durably persists images produced
by running sessions (snapshots for suspend/resume, user images for personalisation). Orthogonal to
`ExecutionEnvironment` — the two are composed, not merged.

```go
// ImageRegistry provides images to an ExecutionEnvironment and persists images
// produced from it. Implementations range from "build locally, save to a tar"
// (dev) to "push/pull a real OCI registry" (production).
type ImageRegistry interface {
	// EnsurePresent guarantees the image is available to the engine that will run
	// it (pull if needed; no-op if locally built/loaded).
	EnsurePresent(ctx context.Context, ref execenv.ImageRef) error

	// Build produces an image from a build context and returns the resulting ref.
	Build(ctx context.Context, spec BuildSpec) (execenv.ImageRef, error)

	// Resolve returns an existing ref for a BuildSpec WITHOUT building (ok=false
	// on cache miss). The Runner calls Resolve first and Build only on a miss, so
	// a returning user with unchanged customisations cache-hits instantly. Tags are
	// content-addressed: sha256(base digest + sorted overlay/artifact hashes + build
	// args + SourceKey).
	Resolve(ctx context.Context, spec BuildSpec) (ref execenv.ImageRef, ok bool, err error)

	// Persist stores an in-engine image ref durably and returns a Handle that
	// survives process/host restarts.
	Persist(ctx context.Context, ref execenv.ImageRef, opts PersistOptions) (Handle, error)

	// Materialize is the inverse of Persist: make the persisted image present for
	// the engine and return a runnable ref.
	Materialize(ctx context.Context, h Handle) (execenv.ImageRef, error)

	// Remove discards a persisted image.
	Remove(ctx context.Context, h Handle) error

	// Capabilities lets the orchestration core adapt (e.g. choose ForceFull
	// snapshots when the registry cannot do diffs).
	Capabilities() Capabilities
}
```

**The `Handle` struct** (from `go/imageregistry/registry.go`):

```go
// Handle is an opaque, durable, JSON-serialisable pointer to a persisted image.
// Its concrete meaning is adapter-specific (file path, blob path + metadata,
// registry reference). The orchestration core stores it on the session row and
// passes it back to Materialize on restore.
type Handle struct {
	Kind string            `json:"kind"` // "local-tar" | "blob-archive" | "registry"
	Ref  string            `json:"ref"`  // path / blobPath / registry-ref
	Meta map[string]string `json:"meta,omitempty"`
}
```

**The `Capabilities` struct** (from `go/imageregistry/registry.go`):

```go
// Capabilities describes the registry's abilities.
type Capabilities struct {
	SupportsDiff   bool
	SupportsBuild  bool
	SupportsRemote bool
	// PortableHandles is true when a Handle produced here can be Materialized on a
	// DIFFERENT worker (blob-archive with a shared BlobStore, or a remote registry).
	// local-tar is NOT portable. Multi-worker fleets require PortableHandles=true;
	// validated at Fleet construction.
	PortableHandles bool
}
```

**Contract / lifecycle.**

- `EnsurePresent` is called before every `Provision`. It must be idempotent and cheap when the image
  is already present. For `blobarchive`, this is a no-op (the image is loaded by `Materialize`).
- `Build` is called for user images on a cache miss. It must produce a runnable image ref accessible
  to the execution environment. `blobarchive` delegates to the Docker daemon via the docker client.
- `Resolve` is the cache-hit fast path. If the content-hash tag exists locally (or in the registry),
  return it with `ok=true` so `Build` is skipped.
- `Persist` / `Materialize` are the snapshot round-trip for suspend/resume. The returned `Handle` is
  stored durably in `SessionStore.SetSnapshotHandle` and passed back to `Materialize` on `Resume`.
- `PortableHandles` must be `true` for multi-worker fleets. `localbuild` produces `local-tar` handles
  that are NOT portable — only use it on a single-worker setup.

**Gotchas.**

- `Handle` is stored as JSON in the session store. Changing its structure breaks existing persisted
  handles — treat it as a durable contract.
- `blobarchive` requires the Docker daemon at the same `dockerHost` used by `DinD`. In `examples/server`
  both share the same `DOCKER_HOST`.

**Shipped references.**
- `imageregistry/blobarchive.Registry` — Docker save → gzip → `BlobStore`. Portable handles.
  Construct with `blobarchive.New(dockerHost, blobs, container string)`.
- `imageregistry/localbuild.Registry` — Docker save → tar file on disk. Non-portable handles (local
  filesystem only). Construct with `localbuild.New(dockerHost, saveDir string)`.

**Shipped mock.** `imageregistry.NewMock()` (in `go/imageregistry/mock.go`) — in-memory, records
every call. `Capabilities()` returns `PortableHandles: true` so fleet tests pass.

---

## `extension.OrgContextProvider`

**Source:** `go/extension/extension.go`

**Purpose.** Assembles the host-specific system-prompt context for a turn. In Platinum this merges
cascading config, brand themes, and persona. The runner appends the returned string to the session
system prompt before sending the message to the in-image agent; it never interprets the content.

```go
// OrgContextProvider assembles the system-prompt context for a turn. Platinum
// merges cascading config + brand themes + persona; the Runner appends the result
// to systemPrompt and never interprets it. Default (nil) contributes "".
type OrgContextProvider interface {
	Context(ctx context.Context, scope ContextScope) (string, error)
}
```

**Required?** No. Pass `nil` in `Deps.OrgContext`; the runner contributes an empty string for the
context segment.

**Contract.** Must be fast (called on the hot path of every `SendMessage`). Return `""` if there is
nothing to add — do not return an error for a missing config. Errors propagate to the caller as a
500-equivalent.

---

## `extension.TokenUsageLogger`

**Source:** `go/extension/extension.go`

**Purpose.** Receives token usage parsed from `query_complete` / `result` SSE events emitted by the
in-image agent. In Platinum this writes a cost row to `token_usage_logs`. The runner calls `Log` after
each completed query.

```go
// TokenUsageLogger receives usage for costing. Default (nil) is a no-op.
type TokenUsageLogger interface {
	Log(ctx context.Context, sessionID string, usage Usage)
}
```

The `Usage` struct (also in `extension`):

```go
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
compaction path. Fire-and-forget to a channel or goroutine if the underlying write is slow.

---

## `extension.ArtifactEnricher`

**Source:** `go/extension/extension.go`

**Purpose.** Lets the host decorate `artifacts.Artifact` metadata before it is persisted. Platinum
adds publish paths, brand colours, and office-preview labels. Called by the runner just before each
`ArtifactStore.Save`.

```go
// ArtifactEnricher lets the host decorate artifact metadata before persistence
// (brand colours, publish paths, labels). Default (nil) is identity.
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

**Purpose.** Pluggable metrics surface. In Platinum this is a Prometheus `prometheus.Registerer`
adapter. The runner calls these on lifecycle transitions, active session count changes, and turn
completions.

```go
// Metrics is the pluggable metrics surface (Prometheus in Platinum). Default
// (nil) is a no-op.
type Metrics interface {
	ObserveLifecycle(phase string, seconds float64)
	SetGauge(name string, v float64)
	Inc(name string)
}
```

**Required?** No. Pass `nil` in `Deps.Metrics`; all instrumentation is silently skipped.

**Contract.** All three methods must be non-blocking and must not panic. `phase` and `name` values
are library-internal strings — the host may map them to metric labels, ignore unknown names, or relay
them verbatim.

---

## Not yet defined / future

The following interfaces appear in the implementation plan (Task 8.1 list) but **do not exist in the
current source**. They are documented here as future seams only:

- **`Claims` / `ScopedClaims`** as a named type — currently defined as `ScopedClaimsIssuer` in
  `extension.go` (see above); the plan refers to it under multiple names but there is only one
  interface.
- No additional interfaces beyond those listed above were found in `go/extension/extension.go`,
  `go/agentkit.go`, `go/artifacts/artifacts.go`, `go/execenv/execenv.go`, or
  `go/imageregistry/registry.go` at the time of writing. All plan-listed names
  (`OrgContextProvider`, `TokenUsageLogger`, `ArtifactEnricher`, `Metrics`) are present and
  documented above.

---

## Wiring example

The canonical reference wiring is `go/examples/server/main.go`. Abbreviated:

```go
// Reference adapters
store, _    := sqlitestore.Open(filepath.Join(dataDir, "sessions.db"))
blobs       := filesblob.NewBlobStore(filepath.Join(dataDir, "blobs"))
artStore    := filesblob.NewArtifactStore(blobs)
claims      := devclaims.New([]byte(secret))   // DEV ONLY
registry, _ := blobarchive.New(dockerHost, blobs, "agentkit-snapshots")
dindEnv, _  := dockerdind.NewDinD(dockerdind.DinDConfig{DockerHost: dockerHost, ...})

runner, _ := agentkit.NewRunner(agentkit.Deps{
    Env:       dindEnv,
    Registry:  registry,
    Store:     store,
    Artifacts: artStore,
    Claims:    claims,
    // OrgContext, TokenLogger, Enricher, Metrics: nil → defaults
    Policy: agentkit.Policy{BaseImage: "agentkit-example:dev", AgentPort: 3010},
})
runner.Start(ctx)

api, _ := httpapi.New(httpapi.Config{
    Runner:    runner,
    Store:     store,
    Artifacts: artStore,
    Identity:  identityFromRequest,
})
http.ListenAndServe(":8099", devAuthMiddleware(api.Mux()))
```

For tests, swap every adapter for its mock and leave optional fields nil:

```go
runner, _ := agentkit.NewRunner(agentkit.Deps{
    Env:       execenv.NewMock(),
    Registry:  imageregistry.NewMock(),
    Store:     agentkittest.NewMemStore(),
    Artifacts: artifacts.NewMock(),
    Claims:    agentkittest.StaticClaims{Token: "test-token"},
})
```

See also:
- [10 — Extension points](10-extension-points.md) — conceptual overview and the required/optional
  table from the spec.
- [02 — Execution environment](02-execution-environment.md) — `ExecutionEnvironment` in depth.
- [03 — Image registry](03-image-registry.md) — `ImageRegistry` in depth.
- [06 — Artifacts](06-artifacts.md) — `ArtifactStore` state machine and extraction patterns.
