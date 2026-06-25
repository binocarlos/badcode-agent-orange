// Package agentkit is the public entry point of the reusable agent-execution
// runtime. A host application constructs a Runner with one implementation of each
// dependency (engine + registry + host services) and calls it from its HTTP
// handlers. In production the deps are real adapters; in tests they are the
// in-memory mocks, so the whole runtime boots hermetically.
//
// See docs/01-architecture.md and docs/04-session-orchestration.md.
package agentkit

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/fleet"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// Version is the library version (pre-release while staged in-repo).
const Version = "0.0.0-staged"

// Harness identifies the agentic framework to use for a session.
// It is a per-session choice, fixed at session-create time.
// See agent-library/docs/12-harness.md.
type Harness string

const (
	// HarnessClaudeAgentSDK is the default harness (the @anthropic-ai/claude-agent-sdk
	// query() loop). An empty Harness value resolves to this in the sandbox.
	HarnessClaudeAgentSDK Harness = "claude-agent-sdk"

	// HarnessClaudeCLI drives the `claude` CLI binary via child_process
	// (future — not yet implemented in the sandbox).
	HarnessClaudeCLI Harness = "claude-cli"

	// HarnessGeminiCLI drives the `gemini` CLI binary via child_process
	// (future — not yet implemented in the sandbox).
	HarnessGeminiCLI Harness = "gemini-cli"

	// HarnessCodex drives the `codex` CLI binary via child_process
	// (future — not yet implemented in the sandbox).
	HarnessCodex Harness = "codex"
)

// RunnerStore is the minimal DB surface the Runner and Fleet require. Both
// *agentdb.Store and agentkittest.MemStore satisfy this interface.
type RunnerStore interface {
	GetSession(ctx context.Context, id string) (*agentdb.Session, error)
	UpdateSession(ctx context.Context, session *agentdb.Session) (*agentdb.Session, error)
	PersistQueryEventsFlat(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error
	ListQueryEventsFlat(ctx context.Context, sessionID string) ([]events.Envelope, error)
	GetSnapshotHandle(ctx context.Context, sessionID string) (imageregistry.Handle, bool, error)
	SetSnapshotHandle(ctx context.Context, sessionID string, h imageregistry.Handle) error
	GetWorkerBinding(ctx context.Context, sessionID string) (string, bool, error)
	SetWorkerBinding(ctx context.Context, sessionID, workerID string) error
	ClearWorkerBinding(ctx context.Context, sessionID string) error
}

// Deps holds one implementation of every dependency the Runner needs. Engine and
// registry are usually shipped adapters (execenv/docker, imageregistry/...); host
// services are implemented by the host. Optional fields fall back to safe
// defaults when nil (mirroring the interface-refactor's nil-fallback wiring).
type Deps struct {
	// Fleet is the placement layer (pool of workers). If Fleet is non-nil it is
	// used directly. If Fleet is nil and Env is non-nil, Env is wrapped as a
	// one-worker fleet (shim) — preserving pre-AG-4 construction. If both are nil
	// NewRunner returns an error.
	Fleet fleet.Fleet

	// Env is the single-worker convenience field. When Fleet is nil, it is
	// automatically wrapped as a one-worker fleet via fleet.NewMemory + Register.
	// Kept for back-compat; new callers can set Fleet directly instead.
	Env execenv.ExecutionEnvironment

	// Required persistence.
	Registry  imageregistry.ImageRegistry
	Store     RunnerStore
	Artifacts artifacts.ArtifactStore
	Claims    extension.ScopedClaimsIssuer

	// Blobs is the blob storage factory. Required when BuildUserImage is used.
	// Optional otherwise (nil disables artifact copy-in for user images).
	Blobs extension.BlobStoreFactory

	// Optional.
	Events         events.EventPipeline             // nil -> built from a Store-backed sink
	SessionContext extension.SessionContextProvider // nil -> contributes ""
	TokenLogger    extension.TokenUsageLogger       // nil -> no-op
	Enricher       extension.ArtifactEnricher       // nil -> identity
	Metrics        extension.Metrics                // nil -> no-op
	HTTPClient     *http.Client                     // nil -> a no-timeout client for SSE
	SkillCatalog   SkillCatalog                     // nil -> hoisted skills captured as artifacts but not cataloged
	CustomImages   CustomImageCatalog               // nil -> custom-image launch ids are ignored (base fallback)

	Policy Policy
}

// Policy is the generic lifecycle configuration (the knobs that used to live in
// the orchestrator's config.ts).
type Policy struct {
	BaseImage      string
	ArchiveTimeout time.Duration // 0 disables the archive loop (idle snapshot + destroy)
	MaxConcurrent  int
	AgentPort      int // in-image agent port (default 3010)

	// SessionEnv is a static set of environment variables injected into every
	// session container the Runner provisions (merged in sessionEnv; per-session
	// keys like SESSION_ID take precedence). This is how a host supplies the
	// model-provider configuration the in-image agent requires — e.g.
	// ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY, an outbound proxy, or feature flags.
	// Without it the agent has no way to reach a model endpoint. The reference
	// server (examples/server) populates this from its own environment.
	SessionEnv map[string]string

	// Mounts is a static set of bind mounts applied to every session container the
	// Runner provisions. This is the dev-mode hot-reload mechanism (skills, plugins,
	// control-server source) — leave empty in production. Sources must be paths
	// visible to the engine's Docker daemon (for DinD: paths inside the DinD
	// container, not the host). Deliberately excluded from user-image throwaway
	// builds so dev binds never alter snapshotted images.
	Mounts []execenv.Mount

	// TrustedWorkload declares that the workloads run in this Runner are trusted
	// (e.g. an internal dev box or a known-safe CI job). When true, shared-tenancy
	// environments at TierContainer are permitted. When false, shared-tenancy
	// requires IsolationTier >= TierVM to prevent untrusted bash in a multi-tenant
	// container.
	TrustedWorkload bool
}

// ArtifactRef identifies a named artifact or folder in the ArtifactStore /
// BlobStore that can be copied into a user image. Container is the logical
// storage container (e.g. a session ID or bucket name), Path is the blob path
// or folder prefix, and Target is the destination path inside the image.
type ArtifactRef struct {
	Container string // logical storage container / session ID
	Path      string // blob path or folder prefix in the BlobStore
	Target    string // destination path inside the image (e.g. /workspace/data)
}

// CustomImageCatalog resolves a stored custom-image id to its durable registry
// handle, visibility-checked for the caller. Implemented by the host (goapi).
type CustomImageCatalog interface {
	// Resolve returns the handle and ok=true when the image exists and is visible
	// to the caller; ok=false (with nil error) when it does not exist or is not
	// visible (no existence leak). A non-nil error is an infrastructure failure.
	Resolve(ctx context.Context, id, callerEmail, callerCustomer string) (imageregistry.Handle, bool, error)
}

// Runner is the public facade: the host's HTTP handlers call this and nothing
// else. It owns the full lifecycle of a session by coordinating the engine,
// registry, event pipeline, artifact store, session store and host extensions.
type Runner interface {
	// CreateSession provisions an instance for a (host-persisted) session row. The
	// host persists the row BEFORE calling this and, on error, deletes the orphan —
	// the Runner owns runtime, the host owns durable identity.
	CreateSession(ctx context.Context, req CreateSessionRequest) (*SessionHandle, error)

	// SendMessage runs one turn end to end: ensure running, enrich, POST to the
	// in-image agent, tee SSE to w while compacting + persisting, fire marker hooks.
	SendMessage(ctx context.Context, ref SessionRef, msg SendMessageRequest, w Writer) error

	// Stream attaches to a session's (or a query's) stream for a reconnecting client.
	Stream(ctx context.Context, ref SessionRef, opts StreamOptions, w Writer) error

	// Stop cancels the in-flight query without tearing the instance down.
	Stop(ctx context.Context, ref SessionRef) error

	// Resume / Destroy expose lifecycle control to the host. Resume brings a
	// session back to running (reusing a live container or restoring from its
	// snapshot); there is no warm suspended state.
	Resume(ctx context.Context, ref SessionRef) (*SessionHandle, error)
	Destroy(ctx context.Context, ref SessionRef) error

	// Snapshot forces an archive now and returns the durable handle (app publishing).
	Snapshot(ctx context.Context, ref SessionRef) (imageregistry.Handle, error)

	// WriteWorkspaceFile writes content to /workspace/<relPath> in the running
	// instance (mkdir -p parent, then cat >). Used to bake a focus into CLAUDE.md
	// before snapshotting a session as an image.
	WriteWorkspaceFile(ctx context.Context, ref SessionRef, relPath string, content []byte) error

	// Status reports combined runtime + durable state.
	Status(ctx context.Context, ref SessionRef) (*SessionStatus, error)

	// RunningSessions returns the set of session IDs whose execution instance is
	// currently live (running). Cheap: iterates the runner's managed-instance set
	// (only live/recently-active sessions), so callers can flag live sessions in a
	// list without a per-session Status round-trip.
	RunningSessions(ctx context.Context) (map[string]bool, error)

	// Start begins the background control loops (idle reaper, archive loop) and
	// recovers orphaned instances. Call once after construction.
	Start(ctx context.Context) error

	// Close stops the control loops.
	Close() error
}

// Writer is io.Writer; aliased so callers needn't import io for the common case.
type Writer = interface{ Write(p []byte) (int, error) }

// SessionRef identifies and authorises a session for Runner calls.
type SessionRef struct {
	SessionID   string
	ScopedToken string // per-session token; if empty the Runner mints one via Claims
}

// CreateSessionRequest carries the config to provision a session instance.
type CreateSessionRequest struct {
	SessionID    string
	Persona      string
	Customer     string
	Job          string
	UserEmail    string
	Model        string
	SystemPrompt string
	MaxTurns     int
	// Image is an explicit base-image override (takes highest precedence; E2E use).
	Image string

	// CustomImageID, when non-empty, selects a built custom image as the launch
	// image. Resolved via Deps.CustomImages + Registry.Materialize.
	// Lower precedence than Image; on resolve/materialize failure, falls back to base.
	CustomImageID string

	// Harness selects the agentic framework for the session.
	// Empty value is equivalent to HarnessClaudeAgentSDK (the sandbox default).
	// See agent-library/docs/12-harness.md.
	Harness Harness
}

// SendMessageRequest is one user turn.
type SendMessageRequest struct {
	Content     string
	Customer    string
	Job         string
	Persona     string
	Model       string
	Attachments []Attachment
}

// Attachment is a file sent with a message.
type Attachment struct {
	MimeType      string
	Base64Content string
	FileName      string
}

// StreamOptions selects which query stream to read.
type StreamOptions struct {
	QueryID     string
	IsReconnect bool
}

// SessionHandle is the runtime handle returned by CreateSession/Resume.
type SessionHandle struct {
	SessionID string
	Address   string
	State     string
}

// SessionStatus is combined runtime + durable state.
type SessionStatus struct {
	SessionID     string
	RuntimeState  string
	ActiveQueryID string
	// SandboxAddress is the network address of the running sandbox container
	// (e.g. "http://sandbox-host:31001"). Empty when the instance is not running.
	// Exposed so hosts can proxy workspace-file requests directly to the sandbox.
	SandboxAddress string
	// HasSnapshot is true when a durable snapshot handle exists for the session,
	// meaning it can be resumed after being destroyed.
	HasSnapshot bool `json:"has_snapshot"`
	// Progress is the live snapshot/restore progress for this session, or nil when
	// no operation is in flight (or it completed more than the store's TTL ago).
	Progress *OpProgress `json:"progress,omitempty"`
}

// ChatClient is a minimal interface for utility LLM calls (titlebot, summarisation).
// The host provides an implementation wrapping their LLM provider SDK.
type ChatClient interface {
	Complete(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// ChatRequest is a simple chat completion request.
type ChatRequest struct {
	Model       string
	Messages    []ChatMessage
	MaxTokens   int
	Temperature float64
}

// ChatMessage is a single message in a chat request.
type ChatMessage struct {
	Role    string
	Content string
}

// ChatResponse is the result of a chat completion call.
type ChatResponse struct {
	Content      string
	InputTokens  int
	OutputTokens int
}

// NewRunner constructs the default Runner from deps.
//
// Fleet wiring (one-worker shim):
//   - If deps.Fleet is set, it is used as-is.
//   - If deps.Fleet is nil and deps.Env is non-nil, deps.Env is wrapped as a
//     one-worker fleet via fleet.NewMemory + Register (the shim). The trust gate
//     (AG-1) is enforced inside fleet.Register, so a shared-tenancy environment
//     that fails the gate returns a non-nil error here.
//   - If both are nil, NewRunner returns an error.
func NewRunner(deps Deps) (Runner, error) {
	if deps.Fleet == nil {
		if deps.Env == nil {
			return nil, fmt.Errorf("agentkit: Deps.Fleet and Deps.Env are both nil — one is required")
		}
		// Shim: wrap the single ExecutionEnvironment as a one-worker fleet.
		// The trust gate is enforced inside fleet.Register.
		f := fleet.NewMemory(deps.Store, &fleet.MemFleetOptions{
			TrustedWorkload: deps.Policy.TrustedWorkload,
		})
		w := &fleet.Worker{
			ID:   "local",
			Env:  deps.Env,
			Caps: deps.Env.Capabilities(),
		}
		if err := f.Register(context.Background(), w); err != nil {
			// Surface as a NewRunner error so the four trust-gate tests continue to
			// pass: they call NewRunner(minimalDeps(env, ...)) and expect an error.
			return nil, fmt.Errorf("agentkit: %w", err)
		}
		deps.Fleet = f
	}

	if deps.Policy.AgentPort == 0 {
		deps.Policy.AgentPort = 3010
	}
	if deps.HTTPClient == nil {
		// No timeout: SSE turns are long-lived. Mirrors the dedicated sseClient in
		// goapi/pkg/server/agent.go.
		deps.HTTPClient = &http.Client{}
	}
	return newRunnerImpl(deps), nil
}
