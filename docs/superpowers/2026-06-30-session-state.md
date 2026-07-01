# Agent Orange v1 — Session State & Resume Point (2026-06-30)

**Purpose:** a durable resume point so work continues after a context compaction (or in a fresh
session) with nothing lost. All load-bearing state is in files + git — this doc is the map.

---

## Where we are: PLANNING PHASE COMPLETE. Next = ENGINEERING PHASE.

A red-team → intent-interview → design → planning session produced a complete, reconciled plan for
**Agent Orange v1** and a working **Slice 0**. The next step is the engineering build, starting with
the **foundation** and **one proof slice**, then a fan-out for the rest.

**Branch:** `slice0-learning-loop`. **Slice 0 is built, committed, and green** (`go/orchestrator/`).

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

**Do NOT fan out all six slices at once.** Proceed in this order:

1. **Foundation (do this first, by the main agent, not parallelized):**
   - Create `go/orchestrator/contracts.go` declaring ALL shared types + interfaces from
     `v1-contracts.md` §3/§4/§5 **once** (per §10b F-1).
   - Apply the Slice-0 interface evolutions (§10b E-1..E-5): `Telemetry` gains `ctx`+`error`;
     `Scope` gains `Prompt`+`Depth`; `BoardStore` gains `Revisions(ctx)`; `Ticket.PublishedRef`;
     `Post` dedup key. Update Slice-0 `telemetry.go`/`runner.go`/tests to match.
   - Add frozen shapes: `Verdict`, `Triggerer`, `FeedbackApplier`, the worker-completion convention.
   - Verify `go build ./...` + `go vet ./...` + `go test ./orchestrator/...` all green. Commit.
2. **Proof slice — Slice A (Postgres board)** end-to-end, following its plan + §10b, to shake out the
   agent+plan+contracts→clean-code process. Review it. If clean → high confidence.
3. **Fan out the remaining five (B, C, D, E, F)** — worktree-isolated engineering agents, each
   against the foundation + its plan + §10b. Then integrate, run the full suite, report.

**Guardrails for autonomous build:** branch (not main); no push/PR/deploy; no real credentials or
publishing; TDD; `go build ./...` stays green; stop-and-surface at genuine forks.

## Suggested resume prompt (paste after compaction)

> Resume the Agent Orange v1 engineering phase. Read `docs/superpowers/2026-06-30-session-state.md`
> and `docs/superpowers/specs/2026-06-30-v1-contracts.md` (esp. §10b). Then do the **Foundation**
> step from the session-state "NEXT ACTION": create `go/orchestrator/contracts.go` and apply the
> Slice-0 interface evolutions, keep the build + tests green, and commit. Then build **Slice A** as
> the proof slice. Do not fan out all six at once. Work autonomously within the stated guardrails.
