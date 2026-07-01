# Agent Orange v1 — Session State & Resume Point (2026-06-30)

**Purpose:** a durable resume point so work continues after a context compaction (or in a fresh
session) with nothing lost. All load-bearing state is in files + git — this doc is the map.

---

## Where we are: FOUNDATION + PROOF SLICE (A) DONE. Next = FAN OUT B–E, then F (supervised).

A red-team → intent-interview → design → planning session produced a complete, reconciled plan for
**Agent Orange v1** and a working **Slice 0**. The engineering build has now begun:

- **✅ Foundation** (commit `fe10103`): `go/orchestrator/contracts.go` (all §3/§4/§5 shared types +
  seams, once) + the §10b evolutions (E-1 Telemetry ctx+error, E-2 `BoardStore.Revisions`, E-3
  `Scope.Prompt/Depth`, E-4 `Ticket.PublishedRef`, E-5 `Post.DedupeKey`) + frozen shapes (`Verdict`,
  `Triggerer`, `FeedbackApplier`, S-4 `ClassifyWorkerOutput`). Slice 0 evolved to match. Green.
- **✅ Slice A — the proof slice** (commit `167fb3a`): `go/orchestrator/pgstore` (`PgBoard`,
  `PgTicketStore`, `PgTelemetry`) + `agentdb` row models + migrations 022–024. Built by ONE
  engineering subagent from the plan + a reconciliation brief; reviewed, whole-module green.
- **Reconciliation brief** (`docs/superpowers/2026-07-01-slice-A-reconciliation.md`, commit `14daef9`)
  — the template for the remaining slices (see the two findings below).

**Branch:** `slice0-learning-loop`. Whole module: `go build ./...` + `go vet ./...` green;
`go test ./orchestrator/... ./agentdb/...` green.

### Two findings from the proof (they change the fan-out)

1. **Every slice plan needs a reconciliation brief FIRST.** The plans predate the Foundation, so each
   has tasks that redo Foundation work (Slice A's Tasks 1–3 were already done in `contracts.go` and
   would have duplicate-declared) and stale signatures (pre-E-1 Telemetry). The main agent (holding
   the Foundation context) must write a short brief per slice before dispatching — do NOT hand a raw
   plan to an engineering agent. This is cheap and it is what made Slice A land clean.
2. **Slice F is NOT an autonomous build step.** It is the supervised deploy (real GCP/DinD, real
   credentials, real publishing) — forbidden by the build guardrails. Autonomous work covers B, C, D,
   E (the code); F is a human-in-the-loop step for later.

## What the system is (one paragraph)

A single-tenant "manager-as-HR" agent **organization** (Go) that turns a vague human goal into
autonomous work over a pluggable worker runtime, and **improves its own playbook from human
feedback** (later, an autonomous Consultant — the *same* `trigger→scope→write_fragment` loop). Built
to be a genuinely novel, working system whose **legible self-improvement story** is the deliverable
(credibility + content; success is *taste/narrative*, not a metric — the oracle problem is demoted).
First real deployment: **BadCode marketing**, a single box we watch, workers in the existing agentkit
DinD on GKE, one social channel behind an **un-bypassable publish-approval gate**.

## The docs (read in this order)

1. `docs/AGENTS_DESIGN.md` — index.
2. `docs/ARCHITECTURE.md` — authoritative design (esp. §6D primitives).
3. `docs/superpowers/specs/2026-06-30-objectives-and-build-path.md` — objectives + Phase-3 findings + why Option B / thin-slice.
4. `docs/superpowers/specs/2026-06-30-execution-coordination-model.md` — execution mechanics + the learning loop.
5. `docs/superpowers/specs/2026-06-30-v1-deployment-plan.md` — v1 shape + build slices A–F.
6. **`docs/superpowers/specs/2026-06-30-v1-contracts.md` — THE KEYSTONE.** Frozen interfaces + the
   **§10b second-pass reconciliation** (post-fan-out fixes) + **D1 (RESOLVED: DinD workers for v1).**
   Everything below builds against this. Read §10b carefully — it supersedes the raw slice plans.
7. `docs/superpowers/plans/2026-06-30-slice0-learning-loop.md` — the built Slice 0 (also the plan FORMAT).
8. `docs/superpowers/plans/2026-06-30-slice-{A..F}-*.md` — the six slice plans (written against the
   contracts; **apply §10b where it supersedes them**).

## Key decisions already made (do not reopen)

- **§6D dispatch-vs-compose** is the center of gravity; structure only what the runtime routes on.
- **Fire-and-forget + tick reconciliation** for v1 (no event bus yet); one `thread.finished`-style
  driver is the deferred generalization.
- **Learning loop = human feedback now, Consultant later** (same loop, trigger swapped).
- **Success = legible narrative, not metrics** → the versioned board earns its keep as "show your work."
- **v1 workers run in the existing agentkit DinD** (D1). Two model paths feed one `SpendMeter`:
  orchestrator scopes call the `Model` seam precisely; DinD workers meter softly from usage frames.
- **Publish-approval gate is un-bypassable** (a worker cannot publish; only an approved ticket does).
- **API key, never subscription OAuth** (subscription automation is disallowed; verified 2026-06).

## Residual risks (why we are NOT firing all six agents blind)

1. Slice plans were written against the *pre*-reconciliation contracts; each must **apply §10b** —
   a per-slice reconciliation step (e.g. the `Telemetry` ctx+error change ripples into A/C/E).
2. The plans were validated by *summary*, not a full line-audit against each other.
3. "Fire all six and expect clean integration" is the big-bang risk (Phase-3 finding C1). Contracts
   reduce it; running code proves it. Integration is discovered, not planned.

## NEXT ACTION (the resume instruction)

Foundation ✅ and the Slice-A proof ✅ are done and clean. Remaining:

1. **Fan out B, C, D, E** — for EACH: the main agent writes a reconciliation brief (finding #1) like
   `2026-07-01-slice-A-reconciliation.md`, then dispatches an engineering agent against
   (foundation + plan + brief). Because parallel agents share the working tree, a parallel fan-out
   needs **worktree isolation** (`isolation: 'worktree'`); a sequential brief→dispatch→review→commit
   rhythm (as Slice A proved) needs none. Slices are near-independent at the contract seam (each builds
   against interfaces + test doubles): B=model/router/spend, C=manager loop, D=connector+gate,
   E=watch surface (C leans on `Triggerer`/`FeedbackApplier`). Review each; keep the module green.
2. **Slice F — supervised deploy (NOT autonomous).** Real GCP/DinD, secrets, live publishing. Do this
   with the human in the loop after B–E are integrated.

### Tracked debt for Slice F (from the Slice-C reconciliation)

- **E-3 composition is deferred to Slice F.** Slice C's in-proc `WorkerRuntime` composes internally
  (it holds `Board`) and the manager sets `Template`/`Input` on the worker `Scope` — the pragmatic
  choice for a throwaway dev double. Slice F's DinD runtime genuinely can't read the board, so when it
  lands the manager must compose → set `Scope.Prompt`, and BOTH runtimes must consume `Scope.Prompt`
  (dropping the in-proc runtime's `Board`/`Compose`). The seam signature `Spawn(ctx, Scope)` is
  unchanged; this is a bounded, known refactor at the F boundary.

### Progress log
- ✅ Foundation `fe10103` · ✅ Slice A `167fb3a` · ✅ Slice B `48044ea` · ⏳ Slice C (in progress).
- Each slice: main agent writes a `2026-07-01-slice-{X}-reconciliation.md` brief, dispatches one
  engineering agent, then the main agent reviews the full diff + runs the suite + commits.

**Guardrails for autonomous build:** branch (not main); no push/PR/deploy; no real credentials or
publishing; build against mock/scripted model offline; TDD; `go build ./...` stays green;
stop-and-surface at genuine forks (the parallel-vs-sequential fan-out mode is one such fork).

## Suggested resume prompt (paste after compaction)

> Resume the Agent Orange v1 engineering phase. Read `docs/superpowers/2026-06-30-session-state.md`
> and `docs/superpowers/specs/2026-06-30-v1-contracts.md` (esp. §10b). Then do the **Foundation**
> step from the session-state "NEXT ACTION": create `go/orchestrator/contracts.go` and apply the
> Slice-0 interface evolutions, keep the build + tests green, and commit. Then build **Slice A** as
> the proof slice. Do not fan out all six at once. Work autonomously within the stated guardrails.
