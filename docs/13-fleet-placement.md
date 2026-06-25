# 13 — Fleet & placement: horizontal scaling across a pool of workers

> **Definition.** A **`Fleet`** is the layer between the `Runner` and a *pool* of
> `ExecutionEnvironment` **workers**. It answers one question — "for session S, which worker runs it,
> and where is that worker?" — and makes the binding **sticky** and **durable** so the orchestration
> core stays stateless across host replicas.

The v0 design assumed one `ExecutionEnvironment` per deployment. That cannot scale horizontally. The
`Fleet` generalises it without changing the per-worker interface: **each worker IS an
`ExecutionEnvironment`; the `Fleet` composes above it.** A single-worker deployment is just a one-worker
fleet, so every existing recipe ([11](11-deployment-recipes.md)) keeps working unchanged and the Runner
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

Shipped: `LeastLoaded` (default; honours `Policy.MaxConcurrent` per worker) and `RoundRobin`.
Affinity-aware policies (prefer a worker that already has the user/app image cached — see
[03](03-image-registry.md)) slot in here without touching the `Fleet` or `Runner`.

## The sticky session→worker binding (where it is persisted)

The binding is durable identity, so — like the snapshot handle — it lives on the host's
`extension.SessionStore` ([10](10-extension-points.md)), **not** in library memory. This is the single
most important statelessness decision: two host replicas behind a load balancer both resolve the same
worker for a session because both read `SessionStore`.

```go
// extension.SessionStore gains:
GetWorkerBinding(ctx, sessionID string) (workerID string, ok bool, err error)
SetWorkerBinding(ctx, sessionID, workerID string) error
ClearWorkerBinding(ctx, sessionID string) error
```

An in-memory cache is an optimisation, not the source of truth. `fleet.NewMemory` provides an
in-memory `Fleet` for tests/single-host (mirroring `agentkittest.NewMemStore`).

## How Provision/Resume/Snapshot route through the Fleet

`deps.Env` becomes `deps.Fleet` (a single `ExecutionEnvironment` is wrapped as a one-worker fleet via a
shim, with nil-fallback per the migration discipline in [91](91-migration-plan.md)). `ensureRunning`
([04](04-session-orchestration.md)) becomes:

1. Resolve worker: `WorkerForSession`; if none, `PlaceForSession`.
2. Operate on `worker.Env` exactly as today (Provision/Resume/Status/Snapshot/Destroy).
3. The in-memory `instances` map keys by `sessionID` and records the `workerID`, so subsequent calls
   reach the same `Env` without re-resolving.

`Recover` iterates **all** workers' `Env.Recover()` and re-adopts, cross-checking against `SessionStore`
bindings.

## Worker health, drain, and loss

- **Health:** the `Fleet` excludes unhealthy workers from `PlacementPolicy.Pick` (a cheap per-worker
  probe — daemon ping / K8s API reachability).
- **Drain (`Deregister(DrainGraceful)`):** stop placing new sessions; let bound sessions finish; on
  idle, snapshot-and-rebind (Persist → clear binding → next message re-places elsewhere).
  `DrainImmediate` snapshots in place if possible, else marks bindings stale.
- **Loss (worker dies) = the restore path *iff a snapshot exists*.** A bound session whose worker is
  gone falls through `ensureRunning`: read the snapshot handle from `SessionStore`; if present →
  `Rebind` to a healthy worker → `Materialize` + `Provision` there (**a lost worker is just an extreme
  drain** — which is *why* restore-portability, below, is mandatory). **If the session was never
  snapshotted** (`GetSnapshotHandle` returns `ok=false` — the common case for an active session that
  was never suspended), there is nothing to restore: the session is **unrecoverable** and must be
  re-created. The workspace written since the last snapshot is the RPO gap; an aggressive
  `ArchiveTimeout` or snapshot-before-drain narrows it but cannot eliminate it for an abrupt crash.

## The restore-portability invariant (critical)

A snapshot `Handle` must be **worker-portable** for cross-worker restore to work:

- `blobarchive` handles (blob path + base-image-id meta) ARE portable **iff** every worker shares the
  same `BlobStore` and can pull/rebuild the same base image.
- `local-tar` handles are **NOT** portable (a tar on worker A's disk is invisible to worker B).

So the rule, validated at `Fleet` construction: **multi-worker fleets require a portable registry**
(`blobarchive` with a shared blob store, or `remote`); `localbuild`/`local-tar` is single-worker only.
This is surfaced via `imageregistry.Capabilities.PortableHandles` ([03](03-image-registry.md)).

## Future backends prove the interface is open

Adding Daytona/E2B/Firecracker touches **zero** lines of `Runner`/`Fleet`/core: each is an
`execenv.ExecutionEnvironment` registered as a `Worker` (with `Backend`/`Tenancy`/`IsolationTier`
capabilities — [02](02-execution-environment.md)). Stronger isolation (gVisor/Kata) is a *runtime swap*
under Docker/K8s — its only upstream effect is that the trust gate ([02](02-execution-environment.md))
now permits `TenancyShared` on that worker. (Sanity-checked against OpenHands V1 `SandboxService`: same
per-worker verb set; our additions are the pool + per-session placement, plus the snapshot/persist split
that enables cross-worker restore.)

## Risks / open decisions

- **RPO on worker loss:** workspace written since the last snapshot is lost if a worker dies before
  archiving (same as today's DinD reality). Mitigation: shorter `ArchiveTimeout`, or snapshot-before-drain.
- **Sticky vs rebalance:** sessions are sticky to their worker until snapshot/restore; there is no live
  migration of a running container. Rebalancing happens only across a snapshot boundary.
- **K8s granularity:** one cluster = one worker (recommended) vs namespaces-as-workers (deferred).
- **Shared-tenancy snapshot ban:** a `TenancyShared` worker declares `SupportsSnapshot=false`
  ([02](02-execution-environment.md), [03](03-image-registry.md)); such workers cannot host sessions that
  need snapshot/restore, so placement must not put a snapshot-requiring session on a shared worker.
