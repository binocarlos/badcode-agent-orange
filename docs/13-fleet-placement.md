# 13 — Fleet & placement: horizontal scaling across a pool of workers

> ### The word "worker" in this document
>
> A **worker here is a host that runs containers** — a DinD daemon, a K8s cluster, a managed
> sandbox provider. `fleet.Worker`, `Session.WorkerID`, `GetWorkerBinding`. Pure infrastructure.
>
> The product layer's **worker** is a completely different thing: a persona, a row of prompt +
> tools, `Session.Worker`, the `workers` table, documented in
> [`product/02-workers.md`](product/02-workers.md). The two columns sit next to each other on the
> session row and mean nothing alike. Confusing them has already cost time on this codebase.
>
> Nothing in `go/fleet/` knows the product layer exists.

> **Definition.** A **`Fleet`** is the layer between the `Runner` and a *pool* of
> `ExecutionEnvironment` **workers**. It answers one question — "for session S, which worker runs it,
> and where is that worker?" — and makes the binding **sticky** and **durable** so the orchestration
> core stays stateless across host replicas.

> **Maturity.** The `Fleet` seam is real, shipped, and on the hot path — every `Provision` goes
> through it, and `NewRunner` wraps a bare `Deps.Env` in a one-worker fleet so there is no
> non-fleet path left. But the **multi-worker** half is mostly interface: the only implementation
> is `fleet.NewMemory`, `cmd/agentd` registers exactly one worker, and several policies described
> below (health probing, drain, portability validation, load-aware placement) are **not
> implemented**. Each is marked inline. Read this as the design plus an honest status, not as
> behaviour you can rely on today.

The v0 design assumed one `ExecutionEnvironment` per deployment. That cannot scale horizontally. The
`Fleet` generalises it without changing the per-worker interface: **each worker IS an
`ExecutionEnvironment`; the `Fleet` composes above it.** A single-worker deployment is just a one-worker
fleet, so every existing deployment shape ([14](14-host-adapters.md#deployment-shapes)) keeps working unchanged and the Runner
gains exactly one new seam.

## Worker

```go
package fleet

type Worker struct {
    ID     string                       // stable, persisted in the binding
    Env    execenv.ExecutionEnvironment // the per-worker placement primitive
    Caps   execenv.Capabilities
    Labels map[string]string            // zone, gpu, image-affinity, …
}
```

A worker maps to a unit of compute that does its own internal scheduling:

- **DinD:** one daemon host = one worker. Scaling out = adding workers.
- **Kubernetes:** the whole cluster/namespace = **one** worker (the K8s scheduler places pods
  internally). Scaling out = more pods on that one worker. (Recommended; namespaces-as-workers is the
  alternative, deferred.)
- **Managed (Daytona/E2B):** the provider = one worker; the provider scales internally.

## The `Fleet` interface (the seam the Runner calls)

```go
type Fleet interface {
    // PlaceForSession returns the worker a session runs on, creating a sticky binding on
    // first placement and returning the existing one thereafter.
    PlaceForSession(ctx context.Context, sessionID string, hint PlacementHint) (*Worker, error)

    // WorkerForSession returns the already-bound worker (no placement); error if none.
    WorkerForSession(ctx context.Context, sessionID string) (*Worker, error)

    // Rebind moves a session to a new worker (restore-to-different-worker, drain). Persists it.
    Rebind(ctx context.Context, sessionID string, hint PlacementHint) (*Worker, error)

    // Register / Deregister manage membership (discovery adapters call these).
    Register(ctx context.Context, w *Worker) error
    Deregister(ctx context.Context, workerID string, mode DrainMode) error

    Workers(ctx context.Context) ([]*Worker, error)
}

type PlacementHint struct {
    PreferWorkerID string            // sticky-restore hint; honoured if healthy
    Labels         map[string]string // affinity (image cached here, zone, …)
    Tenancy        execenv.Tenancy
}

type DrainMode int
const ( DrainGraceful DrainMode = iota; DrainImmediate )
```

## Placement policy (pluggable)

```go
type PlacementPolicy interface {
    Pick(candidates []*Worker, hint PlacementHint) (*Worker, error) // for a NEW session
}
```

Shipped: `LeastLoaded` (the default `NewMemory` installs) and `RoundRobin`. Both honour
`hint.PreferWorkerID` first — a sticky-restore hint wins over load balancing, and is not
load-checked.

**`LeastLoaded` does not currently balance anything.** Its load counter is moved by
`Acquire`/`Release`, and **the Runner never calls either** — only `fleet_test.go` does. With every
count stuck at zero it picks the first candidate in ID-sorted order, i.e. it behaves as a stable
"first worker". Relatedly, its `MaxConcurrent` field is its own: **`agentkit.Policy.MaxConcurrent`
is read by nothing in the module** and is not copied into the policy. Neither matters with one
worker; both must be fixed before a second one is added.

Affinity-aware policies (prefer a worker that already has the app image cached — see
[03](03-image-registry.md)) slot in here without touching the `Fleet` or `Runner`.

> Not to be confused with the product layer's concurrency limits — `project_settings.
> max_concurrent_jobs` and a worker row's `max_instances`. Those gate *how many jobs a project may
> run*, are enforced in `cmd/agentd/dispatch.go`, and have nothing to do with placement.

## The sticky session→worker binding (where it is persisted)

The binding is durable identity, so — like the snapshot handle — it lives on the host's
`agentkit.RunnerStore` ([14](14-host-adapters.md#agentkitrunnerstore)), **not** in library memory. This is the single
most important statelessness decision: two host replicas behind a load balancer both resolve the same
worker for a session because both read `RunnerStore`.

```go
// agentkit.RunnerStore gains:
GetWorkerBinding(ctx, sessionID string) (workerID string, ok bool, err error)
SetWorkerBinding(ctx, sessionID, workerID string) error
ClearWorkerBinding(ctx, sessionID string) error
```

An in-memory cache is an optimisation, not the source of truth. `fleet.NewMemory` provides an
in-memory `Fleet` for tests/single-host (mirroring `agentkittest.NewMemStore`).

## How Provision/Resume/Snapshot route through the Fleet

`Deps.Fleet` is the seam the Runner uses. `Deps.Env` remains as a single-worker convenience: when
`Fleet` is nil and `Env` is set, `NewRunner` wraps it via `fleet.NewMemory` + `Register` under the
worker ID `"local"`. Both nil is a construction error. `ensureRunning` ([01](01-architecture.md)):

1. Resolve worker: `WorkerForSession`; if that errors — no binding *or* a binding naming a worker
   that is no longer registered — `PlaceForSession`, which re-places and overwrites the binding.
2. Operate on `worker.Env` exactly as today (Provision/Resume/Status/Snapshot/Destroy).
3. The in-memory `instances` map keys by `sessionID` and records the `workerID`, so subsequent calls
   reach the same `Env` without re-resolving.

Two things the Runner does **not** do, both visible at `runner.go`'s call sites: it never calls
`Fleet.Rebind` (re-placement happens through `PlaceForSession`'s worker-gone fallthrough instead),
and it always passes an empty `PlacementHint{}` — so `PreferWorkerID`, `Labels` and `Tenancy` are
never populated on the way in.

`Recover` iterates **all** workers' `Env.Recover()` and re-adopts, cross-checking against `RunnerStore`
bindings.

## Worker health, drain, and loss

- **Health — not implemented.** `memFleet.healthyCandidates()` is named for the intent but only
  filters out *drained* workers. There is no probe of any kind: no daemon ping, no API
  reachability check, no `Status` call. A dead worker stays a placement candidate until something
  deregisters it. (`execenv.CapacityReporter` — [02](02-execution-environment.md) — is the nearest
  live signal, and the fleet does not consult it either.)
- **Drain — partially implemented, and not as described.** `Deregister` **removes the worker from
  the map in both modes**; `DrainGraceful` additionally marks it drained. There is no
  snapshot-and-rebind, and nothing marks bindings stale. The practical consequence: after a
  "graceful" deregister, `WorkerForSession` for a session still bound to that worker returns an
  error rather than letting the session finish. The intended semantics — graceful stops new
  placement and lets bound sessions drain to a snapshot boundary — are unbuilt.
- **Loss (worker dies) = the restore path *iff a snapshot exists*.** A bound session whose worker is
  gone falls through `ensureRunning`: `WorkerForSession` errors, `PlaceForSession` re-places on a
  healthy worker (overwriting the binding — not via `Rebind`, which nothing calls), then the
  snapshot handle is read from `RunnerStore`; if present → `Materialize` + `Provision` there
  (**a lost worker is just an extreme drain** — which is *why* restore-portability, below, is
  mandatory). **If the session was never
  snapshotted** (`GetSnapshotHandle` returns `ok=false` — the common case for an active session that
  was never suspended), there is nothing to restore: the session is **unrecoverable** and must be
  re-created. The workspace written since the last snapshot is the RPO gap; an aggressive
  `ArchiveTimeout` or snapshot-before-drain narrows it but cannot eliminate it for an abrupt crash.

## The restore-portability invariant (critical)

A snapshot `Handle` must be **worker-portable** for cross-worker restore to work:

- `blobarchive` handles (blob path + base-image-id meta) ARE portable **iff** every worker shares the
  same `BlobStore` and can pull/rebuild the same base image.
- `local-tar` handles are **NOT** portable (a tar on worker A's disk is invisible to worker B).

So the rule is: **multi-worker fleets require a portable registry** (`blobarchive` with a shared blob
store, or `remote`); `localbuild`/`local-tar` is single-worker only.
`imageregistry.Capabilities.PortableHandles` ([03](03-image-registry.md)) exists to express it.

**It is not enforced.** `fleet.NewMemory` carries a `TODO(AG-6)` where the validation should be, and
the `Fleet` is never handed an `ImageRegistry` to ask. Nothing stops you registering two workers
against a non-portable registry; the failure would appear as a cross-worker restore that cannot find
its bytes. (The code comment on that TODO also claims `PortableHandles` does not exist yet — it does,
in `imageregistry/registry.go`. Only the check is missing.)

## Future backends prove the interface is open

Adding Daytona/E2B/Firecracker touches **zero** lines of `Runner`/`Fleet`/core: each is an
`execenv.ExecutionEnvironment` registered as a `Worker` (with `Backend`/`Tenancy`/`IsolationTier`
capabilities — [02](02-execution-environment.md)). Stronger isolation (gVisor/Kata) is a *runtime swap*
under Docker/K8s — its only upstream effect is that the trust gate ([02](02-execution-environment.md))
now permits `TenancyShared` on that worker. The additions over a single-worker design are the pool +
per-session placement, plus the snapshot/persist split that enables cross-worker restore.

## Risks / open decisions

- **RPO on worker loss:** workspace written since the last snapshot is lost if a worker dies before
  archiving (same as today's DinD reality). Mitigation: shorter `ArchiveTimeout`, or snapshot-before-drain.
- **Sticky vs rebalance:** sessions are sticky to their worker until snapshot/restore; there is no live
  migration of a running container. Rebalancing happens only across a snapshot boundary.
- **K8s granularity:** one cluster = one worker (recommended) vs namespaces-as-workers (deferred).
- **Shared-tenancy snapshot ban:** a `TenancyShared` worker declares `SupportsSnapshot=false`
  ([02](02-execution-environment.md), [03](03-image-registry.md)); such workers cannot host sessions that
  need snapshot/restore, so placement must not put a snapshot-requiring session on a shared worker.
  Moot in practice today: **no shipped `ExecutionEnvironment` declares `TenancyShared`** — both
  Docker adapters are per-session — so the case exists only in tests. `PlacementHint.Tenancy` is
  likewise accepted by the interface and ignored by both shipped policies.
