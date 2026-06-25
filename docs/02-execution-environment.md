# 02 — `ExecutionEnvironment`: run an agent session inside an image

This is **the** core interface. Everything else in the library is policy layered on top of it.

> **Definition.** An `ExecutionEnvironment` is a mechanism that, given a container image and a session
> spec, makes a running **instance** of the in-image agent reachable over HTTP, and lets you exec into
> it, suspend/resume it, snapshot it, and destroy it. *How* it does that — a new container, a pod, or
> an exec into a shared container — is the implementation's concern.

## The contract

```go
package execenv

import (
	"context"
	"io"
	"time"
)

// ExecutionEnvironment runs agent sessions inside container images. Implementations
// decide the mechanism (a fresh Docker container, a Docker-in-Docker container, a
// Kubernetes pod, or an exec into a shared container); the orchestration core above
// it is identical across all of them.
type ExecutionEnvironment interface {
	// Provision makes a running instance of the in-image agent for a session and
	// returns a handle that includes the address to reach its HTTP server. The image
	// must already be present (see ImageRegistry.EnsurePresent). Provision is the
	// generalisation of the orchestrator's createSandbox: for DinD it creates+starts a
	// container with a leased host port; for single-container dev it ensures the shared
	// container is up and returns its in-network address; for K8s it creates a pod and
	// returns its service address.
	Provision(ctx context.Context, spec ProvisionSpec) (*Instance, error)

	// Suspend stops the instance while preserving its filesystem so Resume can bring it
	// back cheaply (docker stop; scale pod to zero). Idempotent if already suspended.
	Suspend(ctx context.Context, id InstanceID) error

	// Resume restarts a suspended instance and blocks until its agent reports healthy
	// (or ctx/timeout fires). Returns the (possibly changed) address.
	Resume(ctx context.Context, id InstanceID) (*Instance, error)

	// Exec runs a one-off command inside the instance and returns its result. Used for
	// workspace listing, secret scanning, and snapshot preparation — not for the agent
	// turn itself (that goes over the HTTP contract to the in-image agent).
	Exec(ctx context.Context, id InstanceID, cmd []string, opts ExecOptions) (*ExecResult, error)

	// Snapshot captures the instance's current filesystem as an image and returns a
	// reference to it. The reference is engine-native (a committed Docker image id; an
	// image built from a pod's layer). Persisting that reference durably is the
	// ImageRegistry's job — Snapshot only produces it. This is the
	// "save the image from a running session" operation.
	Snapshot(ctx context.Context, id InstanceID, opts SnapshotOptions) (ImageRef, error)

	// Destroy tears the instance down and releases its resources (port, container, pod).
	// skipSnapshot=true is the fast path when the caller has already snapshotted or does
	// not care to preserve state.
	Destroy(ctx context.Context, id InstanceID, opts DestroyOptions) error

	// Status reports the live runtime state of an instance.
	Status(ctx context.Context, id InstanceID) (*InstanceStatus, error)

	// Recover lists instances this environment is still managing (e.g. containers
	// labelled by this library that survived a host restart) so the orchestration core
	// can re-adopt them on startup.
	Recover(ctx context.Context) ([]*Instance, error)

	// OnDestroy registers a callback fired whenever an instance is destroyed (by Destroy
	// or by the engine itself). The orchestration core uses it to fire artifact
	// mark-lost and clear in-memory maps. Mirrors SandboxManager.onDestroy.
	OnDestroy(func(id InstanceID))

	// Capabilities describes what this environment supports, so the orchestration core
	// can adapt policy (e.g. skip the idle reaper if Suspend is unsupported).
	Capabilities() Capabilities
}

// ImageRef is an opaque, engine-scoped image identifier (a tag or digest). It is
// produced by Snapshot and by ImageRegistry, and consumed by Provision.
type ImageRef string

type InstanceID string

// ProvisionSpec is everything needed to start a session instance. It is engine-agnostic;
// each adapter maps it onto its own primitives.
type ProvisionSpec struct {
	SessionID string
	Image     ImageRef          // base image, or a snapshot/restore image
	Env       map[string]string // injected env (session token, IDs, model, base URLs)
	Labels    map[string]string // for Recover() and bookkeeping
	Resources ResourceLimits    // cpu/mem/disk hints (honoured where the engine supports it)
	// Mounts are dev-only bind mounts (e.g. hot-reloading the in-image agent source).
	// Production images carry everything baked in, so Mounts is usually empty.
	Mounts []Mount
	// AgentPort is the port the in-image agent listens on inside the container (3010).
	AgentPort int
}

// Instance is a live, addressable session runtime.
type Instance struct {
	ID        InstanceID
	SessionID string
	// Address is the base URL the host uses to reach the in-image agent's HTTP server,
	// e.g. "http://localhost:30007" (DinD host-port), "http://sbx-abc:3010" (shared
	// network DNS), or "http://10.1.2.3:3010" (pod IP). The orchestration core appends
	// the sandbox contract paths (/query-stream, /stream/:id, ...).
	Address   string
	State     InstanceState
	Image     ImageRef  // the image this instance is running
	CreatedAt time.Time
}

type InstanceState string

const (
	StateStarting  InstanceState = "starting"
	StateRunning   InstanceState = "running"
	StateSuspended InstanceState = "suspended"
	StateDestroyed InstanceState = "destroyed"
	StateError     InstanceState = "error"
)

type InstanceStatus struct {
	ID    InstanceID
	State InstanceState
	// ActiveQueryID is the in-flight query if the in-image agent reports one (for
	// reconnect support). Empty if idle.
	ActiveQueryID string
	Address       string
}

type ExecOptions struct {
	WorkingDir string
	Env        map[string]string
	Stdin      io.Reader
	Timeout    time.Duration
}

type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

type SnapshotOptions struct {
	// Tag is an optional human-readable tag to apply to the produced image.
	Tag string
	// ForceFull asks for a complete capture rather than a diff (the orchestrator forces
	// this for restored containers whose diff is unreliable — see 03).
	ForceFull bool
}

type DestroyOptions struct {
	SkipSnapshot bool
}

type Capabilities struct {
	SupportsSuspend  bool // shared-tenancy environments may not (the container is shared)
	SupportsSnapshot bool // MUST be false for Tenancy==TenancyShared (see "Capability axis" below)
	SupportsExec     bool

	// Backend identifies the placement mechanism (descriptive; for metrics/logging/policy).
	Backend Backend
	// Tenancy is the axis the Runner branches on: does this environment place one sandbox
	// per session, or route many sessions into one shared sandbox?
	Tenancy Tenancy
	// IsolationTier is the trust boundary between sessions/tenants. Plain Docker is not safe
	// for untrusted bash when multi-tenant; the trust gate (below) enforces this.
	IsolationTier IsolationTier
}

// Backend names the placement mechanism (descriptive only — the core never branches on it).
type Backend string

const (
	BackendDockerSocket Backend = "docker-socket" // shared host Docker daemon
	BackendDockerDinD   Backend = "docker-dind"
	BackendK8s          Backend = "k8s"
	BackendManaged      Backend = "managed" // Daytona/E2B/… (future)
)

// Tenancy is how many sessions an instance hosts. This is what the Runner reads to decide
// reuse-a-sandbox vs provision-per-session (see 04, 13).
type Tenancy string

const (
	TenancyPerSession Tenancy = "per-session" // one container/pod per session
	TenancyShared     Tenancy = "shared"      // one container, many sessions (sandbox routes by SESSION_ID)
)

// IsolationTier is the strength of the boundary between sessions. file-path isolation only
// (process), namespace/cgroup (container: plain Docker/DinD), or a microVM (vm: gVisor/Kata/
// Firecracker). It gates which tenancy modes are safe for untrusted workloads.
type IsolationTier int

const (
	TierProcess   IsolationTier = iota // shared container, isolation by file path only
	TierContainer                      // plain Docker / DinD
	TierVM                             // gVisor / Kata / Firecracker microVM
)

type ResourceLimits struct {
	CPUMillis int
	MemoryMB  int
	DiskMB    int
}

type Mount struct {
	Source   string // host path
	Target   string // container path
	ReadOnly bool
}
```

## How each verb maps onto each engine

The whole point of the interface is that the orchestration core never branches on engine type. Here
is the mapping the three shipped adapters implement (derived from the current
`orchestrator/src/sandbox-manager.ts` behaviour):

| Verb | Single container (dev) | Docker-in-Docker | Kubernetes |
|------|------------------------|------------------|------------|
| **Provision** | Ensure the one shared container is up; create a session workspace dir; return `http://<shared>:3010` (sessions multiplex by `SESSION_ID`) | `docker.createContainer` from image with a leased host port (`PortAllocator`) + bridge-gateway env; `start`; return `http://localhost:<hostPort>` | Create a Pod (+ ephemeral Service) from image; return the service/pod address `http://<ip>:3010` |
| **Suspend** | no-op or unsupported (`Capabilities.SupportsSuspend=false`) | `container.stop({t:5})`, retain the leased port | scale the pod's owning resource to 0 (or annotate for the controller) |
| **Resume** | no-op (already running) | `container.start`; `waitForHealthy` poll of `/health` | scale back to 1; wait for readiness probe |
| **Exec** | `docker exec` in the shared container, scoped to the session workspace | `docker exec` in the session container | `pods/exec` subresource |
| **Snapshot** | **unsupported** — `Tenancy=shared` reports `SupportsSnapshot=false`; a file diff is not session-attributable when sessions are multiplexed (see "trust gate" + [03](03-image-registry.md)) | `docker commit` → `docker diff`+`getArchive` (fast diff) or `docker save` (full) | build an image from the pod (e.g. `kubectl debug`/buildkit) or rely on a PVC snapshot |
| **Destroy** | remove the session workspace dir; container stays | `container.stop`+`remove`; release the port | delete the Pod/Service |
| **Status** | report shared-container health + per-session active query | `container.inspect` + agent `/status` | pod phase + agent `/status` |
| **Recover** | list session dirs | `docker.listContainers` filtered by label `agentkit.managed=true` (today `platinum.orchestrator=true`), re-adopt + re-lease ports | list pods by label selector |

The DinD column is the closest to today's behaviour — most of `sandbox-manager.ts` *is* the DinD
adapter, and porting it is the bulk of [90-provenance-map.md](90-provenance-map.md)'s `execenv/docker`
entry.

## DinD vs single-container: the differences that the adapter (not the core) absorbs

The current orchestrator branches on `config.dindEnabled` in ~a dozen places. In the library those
branches collapse into two adapter implementations sharing a common Docker client helper:

- **Networking.** DinD leases a host port from a pool and the host reaches the agent on
  `localhost:<port>`; the gateway IP is injected so the agent can call back to the host. Single
  container puts everything on a shared Docker network and reaches the agent by container DNS name.
  → Absorbed by `Provision` returning the right `Address` and setting the right callback env.
- **Port allocation.** Only DinD needs the `PortAllocator`. → It lives in `execenv/docker` and is
  simply unused by the single-container adapter. `Capabilities` doesn't even need to express it.
- **Isolation/tenancy.** DinD = one container per session (`TenancyPerSession`); single-container =
  many sessions in one container (`TenancyShared`). → Expressed via the `Tenancy` axis (above), which
  the in-image agent already supports (the sandbox is *always* multi-session-capable and routes by
  session ID; see [07](07-in-image-agent.md)). The execution environment decides whether to route more
  than one session to a given sandbox; the sandbox never limits sessions.

## The capability axis model & the trust gate

`Capabilities` expresses **three orthogonal axes** rather than the old single `IsolatedPerSession`
boolean, because they vary independently:

- **`Backend`** — descriptive (docker-socket / dind / k8s / managed). The core never branches on it.
- **`Tenancy`** — `per-session` vs `shared`. This is the *only* axis the Runner branches on for
  reuse-vs-provision ([04](04-session-orchestration.md)).
- **`IsolationTier`** — `process` / `container` / `vm`. The trust boundary.

The user's "four execution environments" are these axes crossed, **not** four implementations:
shared-socket-singleton-multiplex = `docker-socket` + `shared`; shared-socket-one-per-session =
`docker-socket` + `per-session`; DinD-one-per-session = `docker-dind` + `per-session`; K8s-pod-per-session
= `k8s` + `per-session`. Two backends × the tenancy flag — no combinatorial explosion.

**The trust gate (security boundary).** Plain Docker (`TierContainer` or below) is **not** safe for
untrusted bash when multi-tenant — a shared kernel and container-escape risk mean one session could
read/poison another's files. `Policy` (in `agentkit.go`) gains `TrustedWorkload bool`, validated at
`NewRunner`/`Fleet` construction:

> `Tenancy == TenancyShared` requires `TrustedWorkload == true` **OR** `IsolationTier >= TierVM`.

This makes "multi-tenant + untrusted on plain Docker" **unconstructable** — fail-fast at startup, not a
runtime branch. `TenancyShared` is for trusted environments (e.g. an internal dev box where prompt
injection is not a threat) or microVM-isolated workers.

**Shared tenancy ⇒ no snapshot.** A `TenancyShared` environment **must** report `SupportsSnapshot=false`
(and reject snapshot/user-image-save requests): with many sessions multiplexed into one container, a
filesystem diff is **not attributable to a single session**, so a session-snapshot would be meaningless.
Only true per-container isolation makes the diff session-attributable. See
[03](03-image-registry.md) and [06](06-artifacts.md).

## What is *not* on this interface (and why)

- **Azure / blob storage.** Persisting a snapshot is `ImageRegistry`'s job ([03](03-image-registry.md)),
  not the environment's. `Snapshot` returns an in-engine `ImageRef`; durability is composed on top.
- **Event streaming.** The environment exposes an `Address`; the orchestration core speaks the sandbox
  HTTP/SSE contract over it. The environment doesn't model events.
- **Lifecycle policy.** *When* to suspend/archive is the orchestration core's decision
  ([04](04-session-orchestration.md)); the environment only provides the mechanisms.
- **Auth / org context.** Injected as `Env` by the core from host extensions; the environment just
  passes env through.

Keeping these off the interface is what makes a `MockExecutionEnvironment` trivial (in-memory map of
instances, no Docker) and what lets the same orchestration core run on a laptop and on K8s unchanged.

## The mock

`execenv.MockExecutionEnvironment` (in `execenv/mock.go`) keeps instances in a map, hands out
`mock://instance/<id>` addresses, records every call on an embedded `Recorder`, and lets tests script
`Status` results and force errors. It makes `Snapshot` return a deterministic `mock-image:<session>`
ref so the `ImageRegistry` mock can round-trip it. With it, the entire orchestration core and `Runner`
are testable with zero containers. See [04](04-session-orchestration.md#testing) and
[10](10-extension-points.md) for the hermetic-test recipe.

## Open design questions (flagged for implementation)

- **Streaming exec.** Some snapshot-prep flows stream large output. `Exec` returns buffered
  `[]byte`; if a use case needs streaming we'll add an `ExecStream` variant rather than complicate
  `Exec`.
- **Address stability across resume.** DinD may reuse the leased port on resume (the orchestrator
  retains it); K8s pod IPs change. `Resume` returns a fresh `Instance` precisely so the core never
  caches a stale address.
- **K8s snapshotting** is the least settled — committing a running pod into an image has no
  first-class primitive. Options: a sidecar buildkit, a CSI volume snapshot for the workspace only,
  or requiring stateless sessions on K8s. The adapter declares `SupportsSnapshot` accordingly and the
  archive loop honours it.
