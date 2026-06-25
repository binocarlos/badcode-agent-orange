# 10 — Extension points: what a host application supplies

This is the doc a **host application author** reads. The library is generic; everything
product-specific is injected through a named interface or plugin. This page enumerates every seam, so
"what do I have to implement to use this?" has one answer.

There are three categories of extension:

1. **Engine adapters** — implement `ExecutionEnvironment` / `ImageRegistry`. Usually you pick a
   *shipped* adapter (Docker/DinD/K8s; localbuild/blobarchive/remote); you only write one for a new
   engine. ([02](02-execution-environment.md), [03](03-image-registry.md))
2. **Host service interfaces** — implement these against your stack (persistence, blobs, context,
   auth, costing). This is the bulk of what a host writes.
3. **Plugins** — in-image tool plugins ([08](08-tool-registry.md)) and browser render plugins
   ([09](09-frontend-components.md)).

## Host service interfaces (Go)

```go
package extension

import (
	"context"
	"io"
)

// SessionStore is durable identity: the host owns session/message/artifact/query-event
// rows. The library never persists — it calls these. Backed in Platinum by store.Store.
type SessionStore interface {
	GetSession(ctx context.Context, id string) (*Session, error)
	UpdateSession(ctx context.Context, s *Session) error
	// Snapshot handle round-trip (durable pointer for restore — see 03).
	SetSnapshotHandle(ctx context.Context, sessionID string, h imageregistry.Handle) error
	GetSnapshotHandle(ctx context.Context, sessionID string) (imageregistry.Handle, bool, error)
	// Compacted query-events for durable replay (the FTS-indexed JSONB rows today).
	PersistQueryEvents(ctx context.Context, sessionID, queryID string, events []events.Envelope, searchText string) error
	ListQueryEvents(ctx context.Context, sessionID string) ([]events.Envelope, error)
	// BlobStore for artifact/snapshot bytes — the host's storage driver.
	Blobs() BlobStore
}

// BlobStore is the byte backend (Azure/fs/hybrid in Platinum's storage.BlobStore).
type BlobStore interface {
	Write(ctx context.Context, container, path string, r io.Reader) error
	Read(ctx context.Context, container, path string) (io.ReadCloser, error)
	Exists(ctx context.Context, container, path string) (bool, error)
	Delete(ctx context.Context, container, path string) error
}

// OrgContextProvider assembles the system-prompt context for a turn. Platinum's impl
// merges cascading config + brand themes + persona; another product might return "".
// The Runner appends the result to systemPrompt; it never interprets it.
type OrgContextProvider interface {
	Context(ctx context.Context, scope ContextScope) (string, error)
}
type ContextScope struct{ Customer, Job, Persona, UserEmail string }

// ScopedClaimsIssuer mints the per-session token injected into the instance and forwarded
// on the message proxy. Platinum issues an HS256 JWT scoped to customer/job/session.
type ScopedClaimsIssuer interface {
	Issue(ctx context.Context, scope ContextScope, sessionID string) (token string, err error)
}

// TokenUsageLogger receives usage parsed from query_complete/result events. Platinum logs
// cost to token_usage_logs; other products may no-op.
type TokenUsageLogger interface {
	Log(ctx context.Context, sessionID string, usage Usage)
}

// ArtifactEnricher lets the host decorate artifact metadata before persistence (brand
// colours, publish paths, labels). Optional; nil = identity.
type ArtifactEnricher interface {
	Enrich(ctx context.Context, art *artifacts.Artifact) error
}

// Metrics is the pluggable metrics surface (Prometheus in Platinum). Optional.
type Metrics interface {
	ObserveLifecycle(phase string, seconds float64)
	SetGauge(name string, v float64)
	Inc(name string)
}
```

### Which are required vs optional

| Interface | Required? | Platinum impl | A new product |
|---|---|---|---|
| `SessionStore` | **Yes** | `store.Store` adapter | any DB |
| `BlobStore` | **Yes** (for artifacts/snapshots) | `storage.BlobStore` | S3/fs |
| `OrgContextProvider` | No (default `""`) | merged config + brand + persona | maybe `""` |
| `ScopedClaimsIssuer` | **Yes** (security) | HS256 JWT | any token |
| `TokenUsageLogger` | No (default no-op) | cost logging | no-op |
| `ArtifactEnricher` | No (default identity) | publish paths/brand | identity |
| `Metrics` | No (default no-op) | Prometheus | no-op |

## Constructing the Runner (the wiring)

```go
runner := agentkit.NewRunner(agentkit.Deps{
	// engine (pick shipped adapters)
	Env:      dockerdind.New(dockerdind.Config{PortRange: [2]int{30001, 30100}, Network: "sandbox"}),
	Registry: blobarchive.New(blobStore, blobarchive.Config{Account: "sessions", Container: "archives"}),

	// host services
	Store:       platinumStore{db},          // SessionStore
	Artifacts:   artifacts.NewBlobStore(...), // ArtifactStore (06)
	OrgContext:  platinumOrgContext{...},     // OrgContextProvider
	Claims:      platinumJWT{secret},         // ScopedClaimsIssuer
	TokenLogger: platinumTokenLog{db},        // optional
	Enricher:    platinumEnricher{...},       // optional
	Metrics:     promMetrics{},               // optional

	// policy
	Policy: agentkit.Policy{
		SuspendTimeout: 5 * time.Minute,
		ArchiveTimeout: 24 * time.Hour,
		BaseImage:      "platinum-sandbox:dev",
		MaxConcurrent:  20,
	},
})
```

Tests swap the engine + stores for mocks and pass no-op extensions:

```go
runner := agentkit.NewRunner(agentkit.Deps{
	Env: execenv.NewMock(), Registry: imageregistry.NewMock(),
	Store: agentkittest.NewMemStore(), Artifacts: artifacts.NewMock(),
	Claims: agentkittest.StaticClaims("test-token"),
	// OrgContext/TokenLogger/Enricher/Metrics left nil → library defaults
})
```

Every mock embeds the shared `Recorder`, so the host asserts the exact interaction log (which
dependency was called, with what args, in what order) — the same hermetic-test discipline as the
interface-refactor's `testharness`.

## Plugin seams (recap)

- **In-image tool plugins** ([08](08-tool-registry.md)) — `ToolPlugin{ sdkTool, marker }`, registered
  into the sandbox's `ToolRegistry`. Platinum's `render_table` bundle is the reference.
- **Browser render plugins** ([09](09-frontend-components.md)) — `RenderPlugin{ eventTypes, reduce,
  render }`, registered into `<AgentChatProvider plugins>`. Platinum's Carbon table/chart/dashboard
  widgets are the reference.
- **Extension event types** — the names that flow tool plugin → SSE → render plugin
  (`table_rendered`, …). Declared once per product, dispatched by name end to end.

## The app image contract

The core image (`agent-library/sandbox/Dockerfile` → `agentkit-sandbox:<tag>`) owns the
harness: Node runtime, the pinned Claude Code CLI, the control server, `/workspace`'s
existence, port 3010, the healthcheck, and `IS_SANDBOX`. An app image is built `FROM` it
(parameterize the tag with `ARG BASE_IMAGE` so a monorepo build and a registry-published
base are interchangeable) and never reinstalls or re-pins any harness concern.

An app image adds exactly three ingredients:

| Ingredient | What it is | Where it goes | Self-documents via |
|---|---|---|---|
| **Plugins** | Typed tools with a product contract — UI rendering, host mutations. Code, because the event shape is guaranteed (schema-validated input, code-constructed events the product frontend renders). | `PRODUCT_PLUGINS_DIR=/app/product-plugins` | tool description + schema |
| **Environment** | Binaries and packages behind Bash (CLIs, python stack, helper libs). Capabilities with no structured product contract — output is text/files for the model. | `/usr/local/bin`, apt/pip/npm layers, `/workspace/lib` | the knowledge layer |
| **Knowledge** | Skills + CLAUDE.md — text mapping the model onto the environment and plugins. | `/workspace/.claude/skills/`, `/workspace/CLAUDE.md` | skill frontmatter; is itself the doc |

Rules:

- The core never writes into the extension paths; app images populate them; dev-mode
  mounts (`Policy.Mounts`) override exactly these same paths.
- The system prompt composes in three non-colliding layers: the `claude_code` preset
  (SDK built-in) + per-session host `append` (from `SessionContextProvider`) + the
  image-baked `/workspace/CLAUDE.md`. There is no CLAUDE.md merging.
- Skills are only valid in images whose Dockerfile provides the environment they assume
  (a skill that says "run `pt vars`" requires the image that installs `pt`) — skills and
  environment ship together in the app layer.
- The "should this be a plugin?" test: **must the product understand the result?**
  A search CLI's output is for the model — environment. A render tool's output is for
  the user's screen — plugin.

Reference implementation: Platinum's `installations/<name>/` (Dockerfile + `plugins/` +
`workspace-lib/`), e.g. `installations/core-v1/`.

## The contract summary (one table)

| Seam | Where it lives | Platinum reference |
|---|---|---|
| `ExecutionEnvironment` | shipped adapter or host | `execenv/docker` (DinD) |
| `ImageRegistry` | shipped adapter or host | `imageregistry/blobarchive` |
| `SessionStore` / `BlobStore` | host | `store.Store`, `storage.BlobStore` |
| `OrgContextProvider` | host | merged config + brand + persona |
| `ScopedClaimsIssuer` | host | `issueAgentScopedJWT` |
| `TokenUsageLogger` / `ArtifactEnricher` / `Metrics` | host (optional) | token logs, files publish, Prometheus |
| Tool plugins | in-image (with image) | `render_table`, `create_dashboard`, `generate_pptx`, `pt` |
| Render plugins | browser (with web app) | `InlinePlatinumTable`, `InlineDashboard` |
| The image itself | host build | `platinum-sandbox` + `CLAUDE.md` + skills |
