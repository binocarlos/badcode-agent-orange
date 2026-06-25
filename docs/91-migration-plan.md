# 91 — Migration plan: Platinum adopts agentkit (and becomes its source)

**This staging pass changes nothing in the running Platinum system.** `agent-library/` is
self-contained and un-imported. This doc is the *library-internal* companion to the interface-refactor
plan ([../../docs/interface-refactor/92-migration-plan.md](../../docs/interface-refactor/92-migration-plan.md),
the **Agent track A0–A9**) — it details how the agent system is migrated **as** this library: built
here, consumed by Platinum, then lifted out into its own repo.

The strategy is deliberate: the migration and the library extraction are the **same work**. Platinum
consumes agentkit exactly as a future separate product would (host adapters + plugins), so by the time
Platinum reaches parity the folder is already a clean library to lift out. **Platinum is the source of
the shared library.**

Same safety invariants as the interface-refactor: nil-fallback dependency injection, introduce-then-
flip, verbatim-wrapper real impls, and the existing suite + E2E browser tests as the confidence signal.

## Locked decisions

- **Native Go DinD directly.** The first real `ExecutionEnvironment` is native Go talking to the DinD
  Docker daemon (porting `orchestrator/src/sandbox-manager.ts`). There is **no** interim wrapper over
  the existing TS orchestrator; the orchestrator process is retired once the flip lands.
- **In-repo consumption, lift-out last.** goapi consumes the Go module via a root `go.mod`
  `replace github.com/binocarlos/badcode-agent-orange => ./agent-library/go`; the TS packages are consumed via
  **yarn workspaces** at the repo root. The final phase flips these local links to published versions.

> **Redesign update (current).** The library-build scope (Phase 1 below) was expanded by the
> architecture redesign: a **pluggable harness seam** ([12](12-harness.md)), an **always-multi-session**
> sandbox control server ([07](07-in-image-agent.md)), the **capability axis split + trust gate**
> ([02](02-execution-environment.md)), a **`Fleet`/placement layer** for horizontal scaling
> ([13](13-fleet-placement.md)), and a **unified image model** (core→app + session-snapshot + curated
> user images on one snapshot primitive — [03](03-image-registry.md), [06](06-artifacts.md)). The
> exploded, agent-followable sub-issues for this are the **AG-1…AG-9** series in
> [`docs/interface-refactor/stages/06-agent/`](../../docs/interface-refactor/stages/06-agent/README.md),
> which supersede the old AL-3/4/7/8. The Platinum-adoption phases (2–6) below are unchanged in spirit;
> the harness/tenancy fields are additive to the contracts they already describe.

---

## Phase 0 — Staged (done) · maps to A0

`agent-library/` exists: docs + a Go module that builds/tests standalone + sandbox/web scaffolds +
the provenance map. Nothing in `goapi/`, `orchestrator/`, `frontend/`, `agent/` imports it; the live
system is byte-for-byte unchanged. **Exit:** `cd agent-library/go && go test ./...` green; docs reviewed.

## Phase 1 — Fill the Go implementations + a vanilla reference host · maps to A2–A3

Work entirely *inside the library*, behind unit tests with the mocks. No Platinum exposure.

Fill order (each builds on the last; mock-testable with no Docker until the Docker adapter). This now
follows the **AG-1…AG-9** sub-issue series ([`stages/06-agent`](../../docs/interface-refactor/stages/06-agent/README.md)):
1. `events/` + `artifacts/` unit tests — done (AL-1 `0cf08539a`, AL-2 `e11db13c1`). *(reference impls present)*
2. `sandbox/` generic core + tool-plugin seam — done (AL-5 `89f51c3c9`); `web/` React + render-plugin
   seam — done (AL-6 `91dd50e4a`).
3. **AG-1 capability axis split** (`Backend`/`Tenancy`/`IsolationTier` + `Policy.TrustedWorkload` trust
   gate) — pure Go, mock-testable.
4. **AG-2 harness seam** (control server + `Harness` iface + `ClaudeAgentSdkHarness` + credential
   pre-check; Go `Harness` field) and **AG-3 multi-session** sandbox (`SessionManager`, session-scoped
   routes + shims, AsyncLocalStorage per-session proxy header) — tsc + unit.
5. **AG-4 tenancy-aware Runner + `fleet/`** (Worker/Fleet/PlacementPolicy, sticky binding on
   `SessionStore`, Runner→Fleet rewire) — Go + mock.
6. **AG-5 `execenv/docker`** (native Go docker-socket + DinD, per-session tenancy first): port
   `sandbox-manager.ts`'s create/suspend/resume/snapshot/destroy/recover + the `PortAllocator`, using a
   Go Docker client (moby `github.com/docker/docker/client`). Integration-tested vs a real daemon.
7. **AG-6 image model** (app build + session-snapshot diff-base fix + generalized folder artifacts +
   shared-tenancy snapshot ban + `PortableHandles`) and **AG-7 user images** (`BuildUserImage` via
   launch+copy-artifacts+snapshot; content-hash cache; prewarm). `blobarchive` takes an injected
   `BlobStore` (so it isn't bound to Azure).
8. **AG-8 reference host** — a real *multi-session* turn on DinD with harness selection (the milestone).
9. `k8senv` / `remote` / managed (Daytona/E2B) / shared-tenancy multiplex — documented-as-future,
   deferred until a product needs them.

**Vanilla reference host (A3):** a minimal Go `main` + minimal web under `agent-library/examples/` that
constructs a `Runner` (DinD + localbuild/blobarchive + trivial in-memory host adapters) and runs a real
agent turn against the Claude SDK on a dind daemon. This **proves the native-Go-DinD path standalone**,
*before* any Platinum handler depends on it — the primary de-risking step for the networking change in
Phase 3.

**Exit:** library integration suite green; the reference host completes a real turn end-to-end on DinD.

## Phase 2 — Platinum consumes the library (additive) · maps to A1 + A4

Make Platinum depend on agentkit and implement the host side, **without flipping any handler** (the
nil-fallback `Deps` keeps today's orchestrator path live).

1. **Wire the Go module (A1).** Add to the root `go.mod`:
   `replace github.com/binocarlos/badcode-agent-orange => ./agent-library/go` + a `require`. Verified: the root
   module governs `goapi/`, so this is picked up by `go build ./goapi/...`, `./stack test_pre_pr`, and
   the Dockerfile. Add the agent fields to `server.Deps` (nil-fallback → old path). Note: the Docker
   client agentkit now pulls in adds entries to the **root** `go.sum`.
2. **Host adapters (A4)** in a new `goapi/pkg/agenthost`, each a **verbatim wrapper** over existing
   code (so the suite stays a valid oracle):

   | agentkit interface | Platinum impl wraps |
   |---|---|
   | `extension.SessionStore` | `store.Store` agent methods (`store_agent_*.go`) |
   | `extension.BlobStore` | `storage.BlobStore` |
   | `extension.OrgContextProvider` | `loadOrgContext` (merged config + brand + persona) |
   | `extension.ScopedClaimsIssuer` | `issueAgentScopedJWT` |
   | `extension.TokenUsageLogger` | token-cost logging path |
   | `extension.ArtifactEnricher` | files-publish / brand metadata |
   | `artifacts.ArtifactStore` | `goapi/pkg/artifacts` over store + BlobStore (status state machine) — **absorbs the old Files/Artifacts PR** |

3. **Construct the `Runner`** (DinD + blobarchive + adapters) in `serve.go` and unit-test it, but leave
   `Deps.AgentRunner` unset in production wiring (still the old path). The type-adapter between
   `store.Store`'s `types.AgentSession` and agentkit's `Session` lives here — the single conversion
   point for Platinum-specific columns.

**Confidence:** `./stack test_pre_pr` green (additive); adapter unit tests; agentkit hermetic tests
with the real adapters.

## Phase 3 — The flip · maps to A5

Repoint `server/agent.go`'s session lifecycle (create/send/stream/stop/resume/destroy/snapshot) to
`s.deps.AgentRunner`. **This is the single behaviour-bearing change** and where goapi starts driving
Docker directly.

- **DinD networking (the key task & risk).** Today the orchestrator is co-located with dind
  (`network_mode: service:dind`, `DOCKER_HOST=tcp://localhost:2375`) so it can drive the daemon *and*
  reach per-session sandbox ports on localhost. When goapi takes over it must reach the daemon and the
  sandbox ports the same way — give goapi access to the dind network (or have dind expose ports and
  address them through the engine adapter). Phase 1's reference host validates this from a Go process
  first.
- Reversible: nil-fallback means reverting the wiring (or the PR) restores the orchestrator path.
- Optional belt-and-braces: shadow-run the `Runner` against a session while the orchestrator stays
  authoritative, comparing event/persistence/archive output, before making the `Runner` authoritative —
  especially for snapshot/restore parity.

**Confidence:** existing suite + **sandbox E2E (north star)**; a hermetic agent-session test with
`MockRunner` (no orchestrator, no Docker).

## Phase 4 — Swap the in-image agent and the frontend · maps to A6–A7

- **Sandbox image (A6):** copy `agent/src` → `agent-library/sandbox` (generic core + the tool-plugin
  seam); Platinum's `render_table`/`render_chart`/`create_dashboard`/`generate_pptx` + the `pt` binary
  become a Platinum-owned tool-plugin bundle layered on top. Re-point the image build: the rsync
  source / workspace entry becomes `@agentkit/sandbox`, with `pt` installed as an image step.
- **Frontend (A7):** add a root `package.json` with yarn `workspaces` (frontend, orchestrator, agent,
  `agent-library/web`, `agent-library/sandbox`); copy `frontend/src/components/agent/*` + the
  `agentEventReducer`/`useAgentSession` hooks → `agent-library/web` (generic + the render-plugin seam);
  the Carbon table/chart/dashboard widgets (`InlinePlatinumTable`, `InlineDashboard`) become **render
  plugins**. `AgentChatPage` mounts the library chat with the plugin set. The single-reducer invariant
  (one codepath, live + replay) is preserved — plugin dispatch is additive, never a second path.

**Confidence:** sandbox E2E; frontend `tsc`+test + E2E rendering assertions.

## Phase 5 — Retire the orchestrator · maps to A8

Once every handler runs through the `Runner` and the in-image/frontend swaps are in:
- Remove the `platinum-orchestrator` compose service + its image from `docker-compose*.yml`,
  `Dockerfile`, and deploy (the **dind** service stays — goapi/agentkit drive it).
- `goapi/pkg/server/agent.go` is now thin handlers over the `Runner`; the HTTP-proxy plumbing is gone.

**Exit:** orchestrator no longer deployed; full suite + E2E green; staging soak.

## Phase 6 — Lift agentkit out to its own repo · maps to A9

The end goal. agentkit moves to its own repository (tag `v0.x`); Platinum drops the root `go.mod`
`replace` and the workspace entries and instead `require`s the published `agentkit` Go module +
`@agentkit/*` npm versions. Platinum becomes **just another consumer** of the shared library, which is
now usable by other products with different toolsets and image backgrounds.

**Exit:** full suite green against published deps; no local `replace`/workspace links to `agent-library/`.

---

## Risk register

| Risk | Mitigation |
|---|---|
| **DinD networking** — goapi must reach the dind daemon + per-session sandbox ports once it drives Docker (the orchestrator got this via `network_mode: service:dind`) | the **vanilla reference host (Phase 1)** proves the path from a Go process first; give goapi dind-network access or expose+address ports via the engine adapter; flip is reversible |
| Snapshot/restore parity (diff-archive: OCI/legacy, force-full heuristic) | `blobarchive` is a *verbatim* port; optional shadow-compare archives; validate before making the `Runner` authoritative |
| `server/agent.go` flip touches ~2,850 LOC | nil-fallback + construct-then-flip (Phase 2 builds, Phase 3 flips); `Runner`/`MockRunner` already merged; single-PR revert |
| New heavy Go dependency (moby Docker client) | isolated to `execenv/docker`; adds root `go.sum` entries via the `replace`; pin the client version |
| Frontend regressions in the single reducer | reducer copied verbatim in shape; replay determinism preserved; render-plugin dispatch is additive — never a second reconstruction path |
| Library/Platinum type drift | the `agenthost` type-adapter is the single conversion point; library wire types are frozen |
| **Multi-session concurrency** — N `query()` loops + N event buffers in one Node process | one-active-turn-per-session; per-(sessionId,queryId) abort; buffer eviction on `DELETE /sessions/:id`; host execenv caps N per shared container |
| **Per-session proxy header** — a global `fetch` patch can't tell which session is calling the model | `AsyncLocalStorage` per turn ([07](07-in-image-agent.md)); load-test two sessions vs distinct mock proxies asserting the right `x-session-id` |
| **Fleet sticky placement / worker loss** — un-snapshotted workspace lost if a worker dies | binding persisted on `SessionStore`; lost worker = restore-via-snapshot on a new worker; multi-worker requires `PortableHandles`; tune `ArchiveTimeout` |
| **Shared-tenancy snapshot ban** — a multiplexed container can't attribute a file diff to one session | `TenancyShared` reports `SupportsSnapshot=false`; placement keeps snapshot-requiring sessions off shared workers ([02](02-execution-environment.md)) |

## What the staging pass commits Platinum to

Nothing. Phase 0 is documentation + an un-imported module. Phases 1–6 are a future program, each
separately planned and approved, each behaviour-preserving until the single Phase-3 flip (itself
reversible). The value delivered now is the design + staged code that make those phases mechanical.
