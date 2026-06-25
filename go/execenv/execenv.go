// Package execenv defines ExecutionEnvironment — the core agentkit interface:
// "a mechanism that runs an agent session inside a container image." Everything
// above it (the orchestration core) is generic Go; everything below it
// (Docker / Docker-in-Docker / Kubernetes) is engine-specific plumbing.
//
// See docs/02-execution-environment.md.
package execenv

import (
	"context"
	"io"
	"time"
)

// ExecutionEnvironment runs agent sessions inside container images.
// Implementations decide the mechanism (a fresh Docker container, a DinD
// container, a Kubernetes pod, or an exec into a shared container); the
// orchestration core above it is identical across all of them.
type ExecutionEnvironment interface {
	// Provision makes a running instance of the in-image agent for a session and
	// returns a handle including the address to reach its HTTP server. The image
	// must already be present (see ImageRegistry.EnsurePresent).
	Provision(ctx context.Context, spec ProvisionSpec) (*Instance, error)

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

	// Recover lists the running instances this environment still manages (e.g.
	// labelled containers that survived a host restart) for re-adoption on
	// startup. Managed containers found stopped are reclaimed (destroyed) rather
	// than returned: the lifecycle is Running-or-Archived, so a stopped container
	// is reclaimable resource, and the session restores from its snapshot on next
	// use.
	Recover(ctx context.Context) ([]*Instance, error)

	// OnDestroy registers a callback fired when any instance is destroyed.
	OnDestroy(cb func(id InstanceID))

	// Capabilities describes what this environment supports.
	Capabilities() Capabilities
}

// ImageRef is an opaque, engine-scoped image identifier (tag or digest).
type ImageRef string

// InstanceID identifies a provisioned instance within an environment.
type InstanceID string

// ProvisionSpec is everything needed to start a session instance, engine-agnostic.
type ProvisionSpec struct {
	SessionID string
	Image     ImageRef
	Env       map[string]string
	Labels    map[string]string
	Resources ResourceLimits
	Mounts    []Mount // dev-only bind mounts; usually empty in production
	AgentPort int     // port the in-image agent listens on (e.g. 3010)
	// Network, if non-empty, attaches the instance to this Docker network instead
	// of the engine default. Used to isolate untrusted composition-image builds
	// (public egress, no internal-service reachability). Engines that don't model
	// networks may ignore it.
	Network string
}

// Instance is a live, addressable session runtime.
type Instance struct {
	ID        InstanceID
	SessionID string
	// Address is the base URL the host uses to reach the in-image agent's HTTP
	// server, e.g. "http://localhost:30007" (DinD), "http://sbx-abc:3010"
	// (shared network), or "http://10.1.2.3:3010" (pod IP).
	Address   string
	State     InstanceState
	Image     ImageRef
	CreatedAt time.Time
}

// InstanceState is the runtime lifecycle state.
type InstanceState string

const (
	StateStarting  InstanceState = "starting"
	StateRunning   InstanceState = "running"
	StateDestroyed InstanceState = "destroyed"
	StateError     InstanceState = "error"
)

// InstanceStatus is the live state reported by the environment.
type InstanceStatus struct {
	ID            InstanceID
	State         InstanceState
	ActiveQueryID string // in-flight query, for reconnect; empty if idle
	Address       string
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

// SnapshotOptions controls a Snapshot.
type SnapshotOptions struct {
	Tag       string
	ForceFull bool // capture the whole image rather than a diff
}

// DestroyOptions controls a Destroy.
type DestroyOptions struct {
	SkipSnapshot bool
}

// Backend names the placement mechanism (descriptive only — core never branches on it).
type Backend string

const (
	BackendDockerSocket Backend = "docker-socket" // shared host Docker daemon
	BackendDockerDinD   Backend = "docker-dind"
	BackendK8s          Backend = "k8s"
	BackendManaged      Backend = "managed" // Daytona/E2B/… (future)
)

// Tenancy is how many sessions an instance hosts — the ONLY axis the Runner branches on.
type Tenancy string

const (
	TenancyPerSession Tenancy = "per-session" // one container/pod per session
	TenancyShared     Tenancy = "shared"      // one container, many sessions (sandbox routes by SESSION_ID)
)

// IsolationTier is the trust boundary between sessions.
type IsolationTier int

const (
	TierProcess   IsolationTier = iota // shared container, isolation by file path only
	TierContainer                      // plain Docker / DinD
	TierVM                             // gVisor / Kata / Firecracker microVM
)

// Capabilities lets the orchestration core adapt policy to the engine.
type Capabilities struct {
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

// ResourceLimits are best-effort resource hints.
type ResourceLimits struct {
	CPUMillis int
	MemoryMB  int
	DiskMB    int
}

// Mount is a dev-only bind mount (e.g. hot-reloading the in-image agent source).
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}
