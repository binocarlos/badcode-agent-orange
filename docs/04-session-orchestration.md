# 04 — Session orchestration: the logic that moves into Go

This is the heart of the inversion. The generic orchestration *logic* that today lives in the
TypeScript orchestrator (`orchestrator/src/sandbox-manager.ts`, `routes/sessions.ts`,
`state-machine.ts`) is reimplemented **in Go**, in the host process, on top of the
`ExecutionEnvironment` + `ImageRegistry` interfaces. The `Runner` is the public facade over it.

## The `Runner` facade

The host application calls only the `Runner`. It is the successor to both `agent.go`'s handlers and
the orchestrator's routes — one Go object, no separate process.

```go
package agentkit

import (
	"context"
	"io"
)

// Runner is the public entry point: the host's HTTP handlers call this and nothing else.
// It owns the full lifecycle of an agent session by coordinating the ExecutionEnvironment,
// ImageRegistry, EventPipeline, ArtifactStore, SessionStore and host extensions.
type Runner interface {
	// CreateSession provisions an instance for a (host-persisted) session and returns a
	// handle. The host persists the session row itself via SessionStore BEFORE calling
	// this — the Runner owns runtime, the host owns durable identity. Mirrors today's
	// createAgentSession orphan-cleanup ownership.
	CreateSession(ctx context.Context, req CreateSessionRequest) (*SessionHandle, error)

	// SendMessage runs one turn: ensures the instance is running (resume or restore as
	// needed), enriches via host extensions, POSTs the turn to the in-image agent, tees
	// the SSE stream to w while compacting + persisting in parallel, and runs marker
	// hooks (artifacts, tokens). Blocks until the turn ends or ctx fires.
	SendMessage(ctx context.Context, ref SessionRef, msg SendMessageRequest, w io.Writer) error

	// Stream attaches to a session's current (or specified) query stream and copies SSE
	// bytes to w — for reconnecting clients. Replays the in-image buffer first.
	Stream(ctx context.Context, ref SessionRef, opts StreamOptions, w io.Writer) error

	// Stop cancels the in-flight query (abortController) without tearing down the instance.
	Stop(ctx context.Context, ref SessionRef) error

	// Suspend / Resume / Destroy expose lifecycle control for the host (and admin tools).
	Suspend(ctx context.Context, ref SessionRef) error
	Resume(ctx context.Context, ref SessionRef) (*SessionHandle, error)
	Destroy(ctx context.Context, ref SessionRef) error

	// Snapshot forces an archive now (app-publishing, admin) and returns the durable
	// handle, which the host stores on the session.
	Snapshot(ctx context.Context, ref SessionRef) (imageregistry.Handle, error)

	// Status reports combined runtime + durable state.
	Status(ctx context.Context, ref SessionRef) (*SessionStatus, error)
}
```

`SessionRef`, `CreateSessionRequest`, `SendMessageRequest`, `StreamOptions` carry the scoped token,
session id, customer/job, model, attachments — the same fields as the interface-refactor's
`agent.Runner` stubs ([../docs/interface-refactor/06-agent.md](../../docs/interface-refactor/06-agent.md)),
now extended for Go-native orchestration. See `go/runner.go`.

## What the implementation owns (ported from TypeScript)

The concrete `Runner` implementation (`runnerImpl` in `go/runner.go`) contains the generic logic
ported from the orchestrator:

### 1. The session state machine (`state-machine.ts` → `go/session.go`)

The exact transition table and the **flush guard** are preserved:

```
starting  → running | suspended | destroyed
running   → suspended | destroyed
suspended → starting | running | archiving | destroyed | persistence_failed
archiving → destroyed | suspended | persistence_failed
destroyed → (terminal)
persistence_failed → destroyed
```

- **Flush guard:** cannot transition to `archiving` while `pendingFlushCount > 0`. The
  `EventPipeline` increments the counter before a persist and decrements after, so a session can never
  be archived with un-persisted events in flight. This is the single most important correctness
  invariant in the current system and it ports verbatim (atomic counter + mutex).

### 2. The control loops (`sandbox-manager.ts` reaper/archive loops)

Two goroutines started by the Runner, both honouring the flush guard:

- **Idle reaper** — every ~30s, `Suspend` instances idle past `SuspendTimeout` (default 5m), skipping
  any with pending flushes and any whose engine reports `SupportsSuspend=false`.
- **Archive loop** — every ~60s, snapshot+persist+destroy instances cold past `ArchiveTimeout`
  (default 24h), again skipping pending-flush instances and engines without `SupportsSnapshot`.

Both emit metrics (port count, suspensions, archive success/fail) — the metrics surface from
`metrics.ts` ports to a pluggable `Metrics` interface so the host wires Prometheus.

### 3. Recovery (`recoverContainers`)

On Runner start, `ExecutionEnvironment.Recover()` returns surviving instances; the Runner re-adopts
them into its in-memory map, health-checks running ones, and resumes the control loops. A host restart
never strands a session.

### 4. Ensure-running / restore-on-demand (`ensureRunning` + `restoreFromArchive`)

Before any message, the Runner resolves the session's **worker** (via the `Fleet` — see
[13](13-fleet-placement.md)) and then ensures the instance can accept work. The Runner is constructed
with `deps.Fleet` (a bare `ExecutionEnvironment` is wrapped as a one-worker fleet via a shim, with
nil-fallback per [91](91-migration-plan.md)), so single-worker deployments are unchanged:

```
worker := fleet.WorkerForSession(sessionID)         // or PlaceForSession on first run
env    := worker.Env
switch instance.State {
case running:    use it
case suspended:  env.Resume → running
case destroyed:  handle, ok := store.GetSnapshotHandle(); if !ok { unrecoverable — re-create }
                 ref := registry.Materialize(handle); env.Provision({Image: ref}); mark "restored"  // may land on a NEW worker
case none:       (first message after a host-side create) Provision from the launch image
}
```

This subsumes today's split between `agent.go`'s `ensureSandboxAvailable` (lazy restore) and the
orchestrator's `ensureRunning` (resume) — one place, in Go.

**Tenancy-aware provisioning.** The Runner branches on `worker.Caps.Tenancy`
([02](02-execution-environment.md)):
- `TenancyPerSession` → one instance per session (today's behaviour): Provision a fresh instance the
  session owns; Suspend/Snapshot/Destroy operate on it 1:1.
- `TenancyShared` → the Runner **reuses** a shared instance on that worker and routes the session to it
  via `POST /sessions` (below). The `instances` map lets many session IDs point at one `*Instance`;
  `Destroy` of one session is a `DELETE /sessions/:id`, not a container teardown (unless it is the last
  session). Suspend/Snapshot are gated off for shared instances (`SupportsSnapshot=false`), so the
  archive/idle loops skip them.

**Harness create call.** After Provision + health, `CreateSession` calls `POST {addr}/sessions` with
`{ sessionId, harness, model, maxTurns }` ([12](12-harness.md)). The sandbox boots the named harness and
runs its credential pre-check; `UNKNOWN_HARNESS`/`HARNESS_CREDENTIALS_MISSING` map to a typed
`ErrHarnessUnavailable` so the host cleans up the orphan session row (see "Orphan cleanup ownership").

**Snapshot diff base.** The Runner records the **launch image** on the instance and passes *that* as
`PersistOptions.BaseImage` when snapshotting (a session launched from a user image diffs against the
user image, not `Policy.BaseImage`) — see [03](03-image-registry.md).

### 5. Keep-alive + the message proxy (`routes/sessions.ts` message route)

During a turn the Runner touches the instance every ~15s so the idle reaper doesn't suspend mid-query,
POSTs the turn to `<instance.Address>/query-stream`, and tees the SSE response: bytes to the client
writer, events to the `EventPipeline`. On completion it clears the keep-alive and records last-activity.

### 6. Conversation reload on resume (`markAsResumed` / `/load-conversation`)

When an instance is resumed/restored, its in-memory conversation history is gone. The Runner reloads
it from `SessionStore` (the persisted query-events/messages) and POSTs `/load-conversation` to the
in-image agent before the first new turn — preserving multi-turn coherence. Ported from the
orchestrator's resumed-session flow.

## What stays the host's job (NOT in the library core)

- **Durable identity.** The host persists the session row, messages, artifacts, and the snapshot
  handle via `SessionStore`. The Runner never owns durable state — exactly the boundary the current
  Go side already maintains. (See [10](10-extension-points.md) for `SessionStore`.)
- **Auth.** The host authenticates the user and mints the scoped token (via `ScopedClaimsIssuer`);
  the Runner just forwards it as instance env + on the message proxy.
- **Org context.** Assembled by the host's `OrgContextProvider` and passed in; the Runner appends it
  to the system prompt without understanding it.
- **HTTP routing.** The host owns its `/agent/*` routes and calls the Runner inside them. The library
  ships no HTTP server of its own (it's a library, not a service) — though `go/httpadapter` offers
  optional Fiber/net-http handler helpers a host can mount.

## Orphan cleanup ownership

A subtlety the current code gets right and the library preserves: on `CreateSession`, if `Provision`
fails, the **host** deletes the orphaned session row — because the host owns the row. The Runner
surfaces the provision error; the host's handler does the `store.DeleteAgentSession`. The library
documents this contract rather than reaching into the host's store.

## Testing

Because the Runner depends only on interfaces, a host integration-tests its agent flows with:

```go
env := execenv.NewMock()
reg := imageregistry.NewMock()
store := agentkittest.NewMemStore()        // in-memory SessionStore
arts := artifacts.NewMock()
runner := agentkit.NewRunner(agentkit.Deps{Env: env, Registry: reg, Store: store, Artifacts: arts, /* mock extensions */})

// drive a turn; the MockExecutionEnvironment's instance serves a scripted SSE stream,
// or the EventPipeline is fed a scripted stream directly.
var buf bytes.Buffer
_ = runner.SendMessage(ctx, ref, msg, &buf)

// assert on recorders:
env.AssertCalled("Provision", sessionID)
store.AssertSnapshotPersisted(sessionID)        // after archive
arts.CallsTo("Save")                            // marker hook fired
```

No Docker, no registry, no blob storage, no network — runs in milliseconds. This is the same
recorder-based hermetic-test philosophy as the interface-refactor's `testharness`, now covering the
whole agent runtime.

## Mapping: today → library

| Today | Library |
|-------|---------|
| `orchestrator/src/sandbox-manager.ts` create/suspend/resume/destroy | `execenv/docker` adapter (mechanism) + Runner (policy) |
| `sandbox-manager.ts` reaper/archive loops, state machine, recovery | Runner orchestration core (Go) |
| `sandbox-manager.ts` suspend/archive/restore (commit/diff/save/blob) | `execenv.Snapshot` + `imageregistry/blobarchive` |
| `orchestrator/src/routes/sessions.ts` routes | Runner methods (Go) |
| `orchestrator/src/state-machine.ts` | `go/session.go` state machine |
| `goapi/pkg/server/agent.go` handlers (thin proxy) | thin host handlers calling `Runner` |
| `agent.go` `ensureSandboxAvailable`, `proxySSEStream` | Runner `ensureRunning` + `EventPipeline` |
| `agent.go` `loadOrgContext`, `issueAgentScopedJWT` | host extensions (`OrgContextProvider`, `ScopedClaimsIssuer`) |

The net effect: **the TypeScript orchestrator process disappears**; its logic becomes Go inside the
host, its Docker plumbing becomes the `execenv/docker` adapter, and its Azure plumbing becomes the
`imageregistry/blobarchive` adapter.
