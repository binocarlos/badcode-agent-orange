# Slice A — reconciliation brief (plan ⇄ Foundation)

**Date:** 2026-07-01
**Why this exists:** the six slice plans were written against the *pre*-reconciliation contracts.
The **Foundation** commit (`go/orchestrator/contracts.go` + the §10b interface evolutions) has since
landed. This brief is the delta an implementer applies so the Slice-A plan produces clean, compiling
code instead of duplicate declarations. **It is also the template for the B–F reconciliation** —
each slice gets one of these before it is built.

Read alongside: `docs/superpowers/plans/2026-06-30-slice-A-postgres-board.md` (the plan) and
`go/orchestrator/contracts.go` (the frozen truth).

## What the Foundation already did (so the plan must NOT redo it)

The Foundation landed the keystone `contracts.go` and evolved Slice 0. Concretely:

- **Plan Task 1 is already satisfied.** `TicketStatus` + all status constants, `Ticket` (WITH the
  extra `PublishedRef` field), and the `TicketStore` interface are declared **in `contracts.go`**, not
  in a new `ticket.go`. **DO NOT create `go/orchestrator/ticket.go`** — it would redeclare these and
  fail to compile. (You MAY add `go/orchestrator/ticket_test.go` with the plan's vocabulary + JSON
  round-trip tests as extra coverage; add a `PublishedRef` assertion to the round-trip.)
- **Plan Task 2 is already done, and better.** `Telemetry` is an interface and the concrete impl is
  `MemTelemetry` — but with the **E-1 evolution applied**: the signature is
  `Record(ctx context.Context, r Run) (Run, error)` and `Runs(ctx context.Context) ([]Run, error)`.
  **DO NOT touch `telemetry.go`.** The plan's Task-2 code shows the OLD (no ctx/error) signature —
  ignore it.
- **Plan Task 3 is already done.** `Runner.Telemetry` is the `Telemetry` interface. **Skip Task 3.**

## The deltas to APPLY (plan code is stale on these five points)

1. **E-1 · `Telemetry` is ctx+error (resolves the plan's own "Contract gap #1").**
   `PgTelemetry` (Task 8) must implement:
   ```go
   func (t *PgTelemetry) Record(ctx context.Context, r orchestrator.Run) (orchestrator.Run, error)
   func (t *PgTelemetry) Runs(ctx context.Context) ([]orchestrator.Run, error)
   ```
   It is **no longer best-effort** — return the real gorm error instead of `log.Printf`+drop. Use the
   passed `ctx` (not `context.Background()`). Update the Task-8 test and the Task-9 test to the new
   signature (`tel.Record(ctx, Run{...})` returns `(Run, error)`; `tel.Runs(ctx)` returns
   `([]Run, error)` — check both errors). Drop the `log` import.

2. **E-2 · `agentdb.BoardStore` now requires `Revisions(ctx) ([]agentdb.BoardRevision, error)`.**
   `PgBoard` (Task 6) must implement it or `var _ agentdb.BoardStore = (*PgBoard)(nil)` fails. Add:
   ```go
   // Revisions returns the append-only log in ascending seq order (the story timeline).
   func (b *PgBoard) Revisions(ctx context.Context) ([]agentdb.BoardRevision, error) {
       var revs []agentdb.BoardRevision
       if err := b.db.WithContext(ctx).Order("seq asc").Find(&revs).Error; err != nil {
           return nil, fmt.Errorf("pgboard: revisions: %w", err)
       }
       return revs, nil
   }
   ```
   Add a parity test mirroring `orchestrator.TestMemBoardRevisionsInSeqOrder` (in
   `contracts_test.go`): two appends → `Revisions` returns them in seq order with author/message intact.

3. **E-4 · `Ticket.PublishedRef` must persist.** Three touch-points:
   - `agentdb.Ticket` row (Task 4): add
     `PublishedRef string `​`json:"published_ref" gorm:"type:varchar(255);not null;default:''"`​`.
   - Migration `023_tickets` (Task 5): add column `published_ref VARCHAR(255) NOT NULL DEFAULT ''`.
   - `toRow`/`fromRow` (Task 7): map `PublishedRef` both ways. Add a `PublishedRef` assertion to the
     ticket CRUD test.

4. **Task 10 (fragments rename) is DEFERRED.** Keep `board_prompt_fragments`. Do not add migration 025.
   (Optional in the plan; deferring keeps the surface small. Revisit if the team wants the v1 name.)

5. **F-2 board-aggregate Go-model cleanup stays out of scope** (plan "Contract gap #3" agrees). The
   §0 collapse migration (Task 5) drops the three *tables*; the `agentdb.BoardStaff/BoardEventType/
   BoardPipeline` Go models + their `agentdb/board_test.go` AutoMigrate/round-trip tests stay put for
   now. Do not delete them in this slice.

## Everything else: build Tasks 4–9 as written

Tasks 4, 5, 6, 7, 8, 9 are otherwise correct and their verbatim code is good. The seq mechanism
(`MAX(seq)+1` in a mutex-serialized transaction, deterministic `r{seq}` / `run{seq}` ids) is the
right call for the single-writer v1 and gives `MemBoard`/`MemTelemetry` parity — keep it. Fast tests
run on sqlite via `AutoMigrate`; the live-Postgres path is the env-gated Task-9 test (`t.Skip` when
`AGENTKIT_TEST_POSTGRES_URL` is unset).

## Definition of done

- New sub-package `go/orchestrator/pgstore` with `PgBoard`, `PgTicketStore`, `PgTelemetry`.
- `agentdb`: `Ticket` + `TelemetryRun` row models (with `published_ref`), migrations 022–024.
- `go build ./...`, `go vet ./...`, and `go test ./orchestrator/... ./agentdb/...` all green.
- Parity tests prove `PgBoard`≡`MemBoard` and `PgTelemetry`≡`MemTelemetry` on the shared assertions.
- No host-app imports (liftability). No commits to `main`; no push.
