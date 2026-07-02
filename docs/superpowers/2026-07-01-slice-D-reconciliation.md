# Slice D — reconciliation brief (plan ⇄ Foundation)

**Date:** 2026-07-01
**Read with:** `docs/superpowers/plans/2026-06-30-slice-D-connector-approval.md` (the plan) and
`go/orchestrator/contracts.go` (the frozen truth). This brief OVERRIDES the plan where they conflict.
All Slice-D code lands in package `go/orchestrator` (+ one example command).

Slice D ships v1's single most important safety property: **publishing is un-bypassable.** A worker
drafts → the draft becomes a `PendingPost` on a Needs-Human ticket → a human **Approve** is the ONLY
code path that calls `Connector.Publish` → **Reject** carries an optional note as `HumanFeedback` and
never publishes. Offline via a `FakeConnector`; the one real channel adapter stays parameterized.

## A. DO NOT re-declare — already in `contracts.go`

- **Plan Task 1 (`connector.go`): OMIT the `type Post struct` and `type Connector interface` blocks.**
  Both are in `contracts.go`. Keep only `FakeConnector` (+ its `var _ Connector` assertion). NOTE: the
  contract `Post` has an extra field **`DedupeKey string`** (E-5) beyond the plan's Channel/Text/Media —
  the plan's `Post{Channel, Text}` literals still compile (DedupeKey defaults ""); you WILL set it in
  Approve (see §B).
- The prerequisite the plan's "Contract gaps G1" worried about (TicketStatus/Ticket/TicketStore not
  existing yet) is moot — the Foundation + Slice A provide them. Consume, don't declare.

## B. Wire the Foundation's E-4 / E-5 into the Approve path (resolves the plan's own G2 and G5)

The plan flagged G2 (no home for the published ref) and G5 (no publish idempotency key). The
Foundation resolved both — use them in `approval.go` Task 4:
- **E-4 · `Ticket.PublishedRef`.** On a successful `Approve` publish, set `ticket.PublishedRef = ref`
  before the Update that moves it to Done (persist the channel's returned ref as attribution). Add an
  assertion to the approve test.
- **E-5 · `Post.DedupeKey`.** Before publishing, set the pending `Post.DedupeKey = ticketID` (the
  ticket id is the idempotency key, so a publish retry can't double-post). Set it when you unmarshal
  the stored `PendingPost` and hand it to `Connector.Publish`.

## C. Telemetry is a ctx+error INTERFACE (E-1)

`NewApprovalService(ts, c, tel Telemetry)` takes the `Telemetry` **interface**, not `*Telemetry`.
Any `tel.Record(...)` call is `Record(ctx, Run) (Run, error)` — pass ctx and handle the error.
`NewTelemetry()` returns `*MemTelemetry`, which satisfies the interface (tests can pass it directly).

## D. Reuse Slice C's `MemTickets` — do not create `fakeTicketStore`

Slice C shipped `MemTickets` (an in-memory `TicketStore` double). The plan's Task-4 test defines its
own `fakeTicketStore` and even says "If Slice A ships an in-memory TicketStore, reuse it and delete
this helper." **Reuse `MemTickets`** in the Slice-D tests; do NOT add `fakeTicketStore` (a second full
TicketStore double is redundant and risks confusion). Adapt the test setup to `MemTickets`' API
(`NewMemTickets()` / `Create` / `Get` / `Update` / `List`).

## E. Build these (plan Tasks 1–6, with the overrides above)

- **Task 1** `connector.go` — `FakeConnector` only (Post/Connector omitted per §A) + test.
- **Task 2** `channel_connector.go` — `ChannelConnector`, ONE parameterized real adapter with a
  `TODO(channel)` for the deferred channel SDK/HTTP; stdlib only, no live network in tests. As written.
  (When it builds the outgoing request it may also carry `Post.DedupeKey`; the channel is deferred, so
  a `TODO(channel)` note is fine.)
- **Task 3** `workertools.go` — `WorkerSyscall` + `SyscallJobFinished`/`SyscallEscalateToHuman` +
  `WorkerToolset()` + `IsWorkerTool()` — the closed worker surface with NO publish tool (C2 floor,
  part 1). New, no contract conflict. As written.
- **Task 4** `approval.go` — `FilePendingPost` + `ApprovalService` (`Approve` the sole publisher,
  `Reject` never publishes), with the E-4/E-5 wiring (§B) and the Telemetry interface (§C). `Approve`
  publishes exactly once, guards double-publish, and on connector failure leaves the ticket
  Needs-Human (retryable). `Reject` clears the PendingPost → Todo and returns
  `HumanFeedback{TargetRef:"ticket:<id>", Note:note}` (it does NOT auto-apply guidance — that's Slice
  E's `/api/feedback`). Reuse `MemTickets` in tests (§D).
- **Task 5** `boundary_test.go` — the C2 structural test: assert (e.g. via reflect) that the worker
  path (`Runner`) has no `Connector` field and that `ApprovalService` is the sole holder — a worker
  cannot reach `Publish`. As written; adjust field/type references to the actual structs.
- **Task 6** `go/examples/approvalgate/main.go` — the human-watchable draft→pending→approve/reject
  demo, offline with the `FakeConnector`. As written.

## F. Guardrails / definition of done

- Consume `Post`/`Connector`/`Ticket`/`TicketStore`/`HumanFeedback`/`TicketStatus` VERBATIM from
  `contracts.go`. If you think one must change, STOP and report (contract change = escalate).
- **The keystone invariant:** `Connector.Publish` must be reachable ONLY from `ApprovalService.Approve`.
  No worker/Runner/manager path may hold a `Connector`. The boundary test must assert this.
- stdlib only — no new deps (the channel SDK lands later behind `TODO(channel)`); offline/deterministic
  tests (fake refs `fake://post/<n>`); secrets never in the board/fragments.
- `go build ./...`, `go vet ./...`, `go test ./orchestrator/...` green at the end. Liftability: no
  host-app imports. Do NOT commit/push/touch `main` — leave changes in the working tree for review.
  Do not modify `migration-reference/` or delete files.
