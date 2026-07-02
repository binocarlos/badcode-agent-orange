# Slice E — reconciliation brief (plan ⇄ Foundation)

**Date:** 2026-07-01
**Read with:** `docs/superpowers/plans/2026-06-30-slice-E-watch-surface.md` (the plan) and
`go/orchestrator/contracts.go` (the frozen truth). This brief OVERRIDES the plan where they conflict.
Slice E is the last autonomous slice: the §8 watch/approve/note HTTP API (new package
`go/orchestrator/watchapi`) + a thin embedded web client + a demo command. It ties C and D together.

## A. ⛔ SKIP plan Task 1 ENTIRELY — do NOT create or overwrite `contracts.go`/`contracts_test.go`

The plan's Task 1 creates `go/orchestrator/contracts.go` (and `contracts_test.go`) with the shared
types. **Both files already exist from the Foundation** and hold all of `TicketStatus`, `Ticket`,
`HumanFeedback`, `Post`, `TicketStore` (plus much more). **Creating/overwriting `contracts.go` would
destroy the Foundation keystone.** Do NOT touch it. Skip Task 1 completely — the plan itself says
"if Slice A has already landed them, delete this file and import theirs." Type coverage already exists
in `contracts_test.go`/`ticket_test.go`/`tier_test.go`; add none.

## B. SKIP plan Task 2 — reuse Slice C's `MemTickets` (not a new `MemTicketStore`)

Slice C shipped an in-memory `TicketStore` double: `MemTickets`, constructed with `NewMemTickets()`.
The plan's Task 2 makes a second one named `MemTicketStore`/`NewMemTicketStore()`. **Do NOT create it.**
Everywhere the plan wires `orchestrator.NewMemTicketStore()` (e.g. the test Config), use
`orchestrator.NewMemTickets()`. It satisfies `orchestrator.TicketStore`.

## C. SKIP plan Task 3's `MemBoard.Revisions()` — it already exists (E-2)

Foundation E-2 already added `Revisions(ctx) ([]agentdb.BoardRevision, error)` to the
`agentdb.BoardStore` interface, and `MemBoard` (and Slice A's `PgBoard`) already implement it. Do NOT
re-add it. For the watchapi timeline port, either define a narrow `RevisionLister` interface
(`Revisions(ctx) ([]agentdb.BoardRevision, error)`) — which `MemBoard`/`PgBoard` both satisfy — or just
use `agentdb.BoardStore`. For the JSON wire shape, `orchestrator.RevisionDTO`
(`{id, author, message, ts}`) already exists in `contracts.go` — reuse it or a local projection; either
is fine, just map `agentdb.BoardRevision` → the DTO in the handler (resolves the plan's gap #3).

## D. Port alignment (Task 4) — bind to the REAL C/D types (critical)

The handlers depend on small ports. Align them so the actual Slice-C/D impls satisfy them:

- **Trigger port → use `orchestrator.Triggerer` (method `Tick(ctx) error`).** The plan's port/fake uses
  a `Trigger(ctx) error` method — WRONG name. Slice C's real `ExchangeTrigger` has `Tick(ctx) error`
  (it satisfies the frozen `orchestrator.Triggerer`). So the trigger port MUST be
  `orchestrator.Triggerer` (or a local port whose method is `Tick`), and the `fakeTrigger` must
  implement `Tick(ctx) error` (rename its `Trigger` method). Otherwise `ExchangeTrigger` won't bind in
  the demo/Slice F.
- **Feedback port → use `orchestrator.FeedbackApplier` (`Apply(ctx, HumanFeedback) (string, error)`).**
  Slice C's `HumanFeedbackApplier` satisfies it; the plan's `fakeFeedback.Apply` already matches. Good.
- **Approve/Reject ports → match Slice D's `ApprovalService` method signatures** so it binds directly:
  `Approve(ctx, id string) (ref string, err error)` and
  `Reject(ctx, id, note string) (HumanFeedback, error)`. Define the port(s) with exactly those
  signatures (one `Approver` port with both, or an `Approver` + a `Rejecter`); `*ApprovalService`
  (Slice D) must satisfy them. Adjust the fakes accordingly.

## E. Telemetry is a ctx+error interface (E-1)

The `/api/runs` handler reads the run log. The `TelemetryReader` port must read via
`Runs(ctx) ([]orchestrator.Run, error)` (E-1), NOT `Runs() []Run`. Wire `orchestrator.NewTelemetry()`
(which returns `*MemTelemetry`, satisfying it). Config's telemetry field is the reader port (or
`orchestrator.Telemetry`).

## F. Build these (plan Tasks 4–14, with the overrides above)

New package `go/orchestrator/watchapi`:
- **Task 4** skeleton — `Config`, the ports (§D/§E aligned), `New` (guards required deps), `Mux()`
  registering the eight §8 routes (stubbed 501 until each task), shared-bearer-token `auth` middleware
  (empty token disables it for local dev), `writeJSON`/`writeErr`, and `fakes_test.go` recording fakes
  (with `fakeTrigger.Tick`, `orchestrator.NewMemTickets()`).
- **Tasks 5–11** the eight §8 handlers: `GET /api/tickets?status=`, `POST /api/tickets/{id}/approve`
  (→ Approver, never touches a Connector), `POST /api/tickets/{id}/reject {note?}` (→ Rejecter,
  returns/surfaces HumanFeedback), `POST /api/feedback {target_ref, note}` (→ FeedbackApplier),
  `GET /api/board/revisions` (→ RevisionDTO projection), `GET /api/board/current`, `GET /api/runs`
  (→ ctx+error TelemetryReader), `POST /api/trigger` (→ Triggerer.Tick). Match the §8 shapes exactly.
- **Task 12** full-Mux end-to-end httptest (the watch story over HTTP).
- **Task 13** the thin embedded web client (`go:embed` HTML + vanilla `fetch`; no npm/React) at `/`.
- **Task 14** a runnable demo command (boot the surface over the Slice-0/loop pieces, offline).

## G. Guardrails / definition of done

- Consume all `contracts.go` types/interfaces VERBATIM; the API must implement contracts §8 exactly.
  If you think a contract must change, STOP and report (escalate, not a local edit).
- Handlers depend on interfaces (ports), never concrete impls; Slice E never touches a `Connector`
  (the gate stays in Slice D's `ApprovalService`, injected via the Approve port).
- stdlib only — no new deps; web client is embedded static assets (no npm/React build); every test
  offline/deterministic via `httptest` + fakes.
- `go build ./...`, `go vet ./...`, `go test ./orchestrator/...` (incl. `./orchestrator/watchapi/...`)
  green at the end. Liftability: no host-app imports. Do NOT commit/push/touch `main` — leave changes
  in the working tree for review. Do not modify `migration-reference/` or delete files. Above all: do
  NOT recreate or overwrite `contracts.go`.
