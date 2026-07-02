# Slice C — reconciliation brief (plan ⇄ Foundation)

**Date:** 2026-07-01
**Read with:** `docs/superpowers/plans/2026-06-30-slice-C-manager-loop.md` (the plan) and
`go/orchestrator/contracts.go` (the frozen truth). This brief OVERRIDES the plan where they conflict.
Slice C is the biggest slice and its plan predates the Foundation, so it has the most overrides —
read this fully before writing code. All Slice-C code lands in package `go/orchestrator`.

Slice C builds the tick-based manager exchange: plan a goal → tickets → spawn workers fire-and-forget
under enforced floors → workers deliver a Result that flips the ticket to In-Review → next tick
verifies vs acceptance → Done or re-plan. In-memory/in-process doubles only (no DB, no network).

## A. DO NOT re-declare — these are already in `contracts.go` (would duplicate-declare and fail)

- **SKIP plan Task 1 (`types.go`) ENTIRELY.** `TicketStatus`+lanes, `ModelTier`+tiers,
  `FragmentKind`+kinds, `ResultStatus`+statuses, `Budget`, `Result`, `Ticket`, `HumanFeedback`, `Post`
  are ALL in `contracts.go`. Do NOT create `types.go`. (You may optionally add a `types_test.go` with
  the plan's `TestContractStringsAreFrozen` as coverage — it just asserts the existing constants.)
- **SKIP the Task-1 `runner.go` Scope evolution.** `Scope` is already in `contracts.go`, fully evolved
  (it has `Tier`, `Tools`, `Budget`, `Parent`, `TicketID`, AND `Prompt`, `Depth`). Do not edit
  `runner.go`'s Scope. The plan's `TestScopeRoundTripsThroughTicket` works against it as-is.
- In **`worker.go`**: OMIT the `type WorkerRuntime interface` and `type ResultSink interface` blocks —
  both are in `contracts.go`. Keep `InProcRuntime` and `TicketResultSink` (the impls) + their
  `var _ WorkerRuntime`/`var _ ResultSink` assertions.
- In **`verify.go`**: OMIT the `type Verdict struct` block — `Verdict{Pass bool; Reason string}` is in
  `contracts.go` (S-1). Keep the `Verify(...)` function; have it return `Verdict` (the contract one).

## B. Filename collision — DO NOT create or overwrite `router.go`

**Slice B already created `go/orchestrator/router.go`** (TierRouter + config). The `ModelRouter`
interface is in `contracts.go`. So plan Task 3 must NOT create `router.go`. Slice C only needs a
deterministic router *test double*: define `ScriptedRouter` (the `map[ModelTier]Model` from the plan)
in a **test-only file `scriptedrouter_test.go`** (package `orchestrator`, so every `*_test.go` in the
package can use it). Do NOT re-declare `ModelRouter`. Do NOT touch Slice B's `router.go`.

## C. Telemetry is now a ctx+error INTERFACE (E-1) — thread it everywhere

Foundation evolved Telemetry: it is an **interface** `Telemetry { Record(ctx, Run) (Run, error);
Runs(ctx) ([]Run, error) }`; the in-memory impl is `MemTelemetry`; `NewTelemetry()` returns
`*MemTelemetry`. The plan's Slice-C code uses the OLD shape (`*Telemetry` field, `Record(Run) Run`,
`Runs() []Run`). Fix throughout (`worker.go`, `manager.go`, and their tests):
- Struct fields `Telemetry *Telemetry` → `Telemetry Telemetry` (the interface).
- `rt.Telemetry.Record(Run{...})` → `run, err := rt.Telemetry.Record(ctx, Run{...})`; handle the error
  (fail loud — wrap and return).
- `rt.Telemetry.Runs()` → `runs, err := rt.Telemetry.Runs(ctx)` in tests; check the error.

## D. Consume the Foundation's frozen shapes (they resolved four of the plan's own "gaps found")

- **S-4 worker-completion (plan gap #3).** `contracts.go` provides `const EscalatePrefix = "ESCALATE:"`
  and `func ClassifyWorkerOutput(raw) (ResultStatus, string)`. In `InProcRuntime.Spawn`, DROP the local
  `escalatePrefix` const and the inline `strings.CutPrefix`; use
  `status, text := ClassifyWorkerOutput(out)` and build the `Result` from that. `TicketResultSink`
  keeps its status mapping (Done→In-Review; Escalated/Failed→Needs-Human).
- **S-1 Verdict (plan gap #4).** Use `contracts.go`'s `Verdict{Pass, Reason}`; do not define your own.
- **S-3 FeedbackApplier (plan gap #5).** The plan errored on `ticket:`/`run:` targets. The Foundation
  froze the rule instead: `fragment:<id>` → that fragment id directly; `ticket:<id>` / `run:<id>` →
  default to the **`routing-guidance`** fragment. Implement THIS rule in `ApplyHumanFeedback`
  (`fragment.go`) — do NOT return "unsupported target". Also provide a small type that satisfies the
  `FeedbackApplier` interface (`Apply(ctx, HumanFeedback) (revisionID string, err error)`) by resolving
  the TargetRef via that rule and calling `WriteFragment`; add `var _ FeedbackApplier = ...`.
- **S-2 Triggerer (plan gap #7).** Keep `ManagerExchange.Tick(ctx) (TickReport, error)` as the plan has
  it. That signature CANNOT satisfy `Triggerer{Tick(ctx) error}` (same name, different return). So add a
  thin adapter type, e.g. `type ExchangeTrigger struct{ Exchange *ManagerExchange }` with
  `func (t ExchangeTrigger) Tick(ctx) error { _, err := t.Exchange.Tick(ctx); return err }` and
  `var _ Triggerer = ExchangeTrigger{}`. (Slice E's `/api/trigger` binds to a `Triggerer`.)

## E. E-3 composition — DEFERRED to Slice F (documented, not silent)

The contract (E-3) says composition is orchestrator-side: the manager composes `Scope.Prompt` and the
`WorkerRuntime` runs that prompt with no board access. **For Slice C, keep the plan's structure:** the
in-proc runtime holds `Board` and composes internally (`Compose(board, s.Template, s.Input)`), and the
manager sets `Template`/`Input` on the worker Scope. Rationale: `InProcRuntime` is a throwaway dev
double; the seam signature `Spawn(ctx, Scope)` is unchanged; forcing the compose-outside restructure
through the manager + telemetry-pinning now (in the most complex slice) is unnecessary risk. **Slice F
will honor E-3** — its DinD runtime genuinely can't read the board, so the manager will compose →
`Scope.Prompt` and both runtimes will consume `Scope.Prompt` at that point. This is tracked debt for
Slice F, recorded in the session-state doc.

## F. Reuse existing package/test helpers — do not redeclare

- `fragOp(kind, id, body)` is defined in `memboard_test.go`; `SeedFragment(id, body)` in `seed.go`;
  `Compose`, `MemBoard`/`NewMemBoard`, `ScriptedModel`/`Rule`, `ApplyFeedback` already exist. Reuse
  them. Watch for test-helper name collisions across `*_test.go` files (Go fails on duplicate
  package-level funcs) — the build will catch any; fix by reusing the existing one.

## G. Build these (plan Tasks 2, 4–11, with the overrides above)

- **Task 2** `memtickets.go` — `MemTickets`, the in-memory `TicketStore` double (Slice A only shipped
  the Postgres impl; the manager tests need an in-mem one). New; no conflict.
- **Task 4** `floors.go` — `SpawnLedger` (depth / per-scope spawns / shared tree-tokens). New; the
  ledger stays authoritative for depth (plan gaps #1/#2), even though `Scope.Depth` now also exists.
- **Task 5** `worker.go` — `InProcRuntime` + `TicketResultSink` (interfaces omitted per §A; S-4 per §D;
  Telemetry per §C; compose-internal per §E).
- **Task 6** `verify.go` — `Verify(...)` returning `contracts.Verdict` (Verdict decl omitted per §A).
- **Task 7** `fragment.go` — `WriteFragment` + `ApplyHumanFeedback` (S-3 rule per §D) + a
  `FeedbackApplier` impl. Refactor `feedback.go`'s `ApplyFeedback` to route through `WriteFragment`
  (behaviour unchanged) if convenient.
- **Tasks 8–10** `manager.go` — `ManagerExchange`, `plan`, `reconcile`, `Tick(ctx)(TickReport,error)`,
  `TickReport`, `plannedTicket`, floor-enforced spawn (refusal fails loud → Needs-Human) + the
  `ExchangeTrigger` adapter (§D). Telemetry per §C.
- **Task 11** `manager_narrative_test.go` — the end-to-end demo as a test (goal → ticket → draft →
  In-Review → verify → Done, plus a floor refusal).

## H. Guardrails / definition of done

- Consume all `contracts.go` types/interfaces VERBATIM. If you think one must change, STOP and report
  (contract change = escalate, not a local edit).
- stdlib only — no new deps; deterministic ids (`s1`,`t1`,`r1`,`run1`), offline, no network/DB/Docker.
- `go build ./...`, `go vet ./...`, `go test ./orchestrator/...` all green at the end.
- Liftability: no host-app imports. Do NOT commit, push, or touch `main` — leave changes in the
  working tree for the parent to review and commit. Do not modify `migration-reference/` or delete files.
