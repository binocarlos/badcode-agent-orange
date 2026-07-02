# v1 Contracts §10c — Remediation Amendment (post-review, 2026-07-02)

**Status:** FROZEN amendment to `2026-06-30-v1-contracts.md`. Supersedes §10b where they conflict.
**Provenance:** the 2026-07-02 adversarial review of the A–E build (8 finder angles → 18 verified
findings: 15 CONFIRMED, 3 PLAUSIBLE, 0 refuted). Every item below traces to a confirmed finding or a
missing primitive identified in the design review. Same rules as ever: implementers consume these
shapes VERBATIM; a further contract change is stop-and-escalate.

**Execution order (three passes, sequential):**
1. **Foundation′** — §A, §B, §C, §J (contracts.go + the Model-signature ripple; everything compiles).
2. **Wave 1** — §I (storage + gate fixes; migration 025).
3. **Wave 2** — §D, §E, §F, §G, §H (loop primitives + the goal→published e2e test).

---

## §A · The Model seam surfaces usage (fixes: welded metering, char-based budgets)

```go
// Usage is the token cost of one model call. Offline doubles report a
// deterministic pseudo-usage so budget mechanics stay testable.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}
func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens }

// Model — EVOLVED (was: Run(ctx, prompt) (string, error)).
type Model interface {
	Run(ctx context.Context, prompt string) (string, Usage, error)
}
```

- **Ripples to every impl and caller** (ScriptedModel, AnthropicModel, errModel, Runner, Verify,
  ApplyFeedback, InProcRuntime, ManagerExchange, examples, all tests). Callers that don't need usage
  discard it; the compiler finds every site.
- `AnthropicModel` returns real usage from the response frame and KEEPS its internal `Meter`
  (pre-dispatch probe + post-call charge) — but the post-call charge error is **no longer discarded**:
  at/over ceiling the charge must still be RECORDED (see §I-5) so spend is never uncounted.
- `ScriptedModel` returns deterministic pseudo-usage: `Usage{InputTokens: int64(len(prompt)/4),
  OutputTokens: int64(len(reply)/4)}` (floor 1 each when non-empty) — keeps ledger/budget tests
  meaningful offline. `errModel` returns zero usage.
- `InProcRuntime` sets `Result.TokensUsed = usage.Total()` (drops the `len(prompt)+len(out)` char
  estimate) and charges the SpawnLedger with the same number — one currency for both floors.

## §B · Ticket evolutions (fixes: disconnected publish chain; verbatim retries)

```go
// Disposition — what a verified artifact is FOR (consulted by reconcile).
type Disposition string
const (
	DispositionInternal Disposition = "internal" // verify-pass → Done (default, "" ≡ internal)
	DispositionPublish  Disposition = "publish"  // verify-pass → FilePendingPost → needs_human
)

// Ticket gains:
Disposition  Disposition // what passing work becomes (see §D lane rules)
AttemptNotes []string    // accumulated failure context: verify Reasons, reject notes, human answers
```

- `plannedTicket` (manager planning schema) gains optional `"disposition"`; when empty, the ticket
  takes `ManagerExchange.DefaultDisposition` (config; BadCode marketing sets `publish`).
- `ManagerExchange` gains config `Channel string` (the single v1 channel name) and
  `DefaultDisposition Disposition`.
- **AttemptNotes reach the retry prompt:** when chooseAndSpawn dispatches a ticket with
  `len(AttemptNotes) > 0`, it composes `scope.Input = t.Objective + "\n\nFeedback on previous
  attempts (address ALL of it):\n- " + strings.Join(notes, "\n- ")`. This is the ticket-level
  learning loop: verify Reasons (§D), reject notes (§D), and human answers (§E) all land here.

## §C · Run attribution (fixes: un-joinable telemetry)

```go
// Run gains:
TicketID  string // the ticket this run served ("" for manager-exchange-level runs)
SessionID string // the worker session id ("" for non-worker runs)
```

Recorded at every site: `InProcRuntime.Spawn` sets both (s.TicketID, the minted sid); manager
plan/refuse runs set TicketID where known. `agentdb.TelemetryRun` row + migration 025 (§I-6) +
watchapi `RunDTO` gain `ticket_id`/`session_id`.

## §D · The ticket state machine — ONE transition function (fixes: smeared lanes, attempts bypass, stranded in_progress, spend-halt handling)

New `go/orchestrator/transitions.go`:

```go
// TicketEvent is everything that can move a ticket between lanes.
type TicketEvent string
const (
	EvSpawned      TicketEvent = "spawned"       // todo → in_progress
	EvDelivered    TicketEvent = "delivered"     // in_progress → in_review (ResultDone)
	EvEscalated    TicketEvent = "escalated"     // in_progress → needs_human (worker question/failure)
	EvVerifyPassed TicketEvent = "verify_passed" // in_review → done (internal) | needs_human+PendingPost (publish)
	EvVerifyFailed TicketEvent = "verify_failed" // in_review → todo (+note, +attempt) | needs_human at cap
	EvRejected     TicketEvent = "rejected"      // needs_human → todo (+note, +attempt) | needs_human at cap
	EvApproved     TicketEvent = "approved"      // needs_human → done (+PublishedRef)
	EvAnswered     TicketEvent = "answered"      // needs_human → todo (+note; NO attempt increment)
	EvSpawnFailed  TicketEvent = "spawn_failed"  // in_progress → todo (transient revert; NO attempt increment)
	EvFloorRefused TicketEvent = "floor_refused" // todo → needs_human (fail-loud)
)
```

`Transition(t *Ticket, ev TicketEvent, note string)` is the ONLY place that mutates
`Status`/`Attempts`/`AttemptNotes` (and stamps `UpdatedAt`). Rules it centralizes:

- **Attempts accounting is uniform:** `EvVerifyFailed` AND `EvRejected` increment `Attempts` and
  append the note; at `Attempts >= maxAttempts` both go `needs_human` instead of `todo`. (Fixes the
  Reject bypass.) `EvAnswered` and `EvSpawnFailed` do NOT increment (an answer is new information; a
  transient error is not the model's failure).
- **maxAttempts zero-value:** `const DefaultMaxAttempts = 2`; a `ManagerExchange.MaxAttempts` of 0
  means the default, enforced in one place (Transition's caller passes the resolved value).
- **Spawn-error handling (fixes stranding):** in chooseAndSpawn — floor refusal (`ErrMaxDepth`/
  `ErrMaxSpawns`/`ErrTreeExhausted`/`ErrUnknownParent`) → `EvFloorRefused`; `ErrSpendCeiling` →
  `EvSpawnFailed` (revert to todo) then STOP the spawn loop for this tick (budget-halt, retried when
  the ceiling lifts); any other error → `EvSpawnFailed` (revert), record a telemetry run, continue
  with the next ticket. A ticket can no longer be stranded `in_progress`.
- **The disposition hop (fixes the disconnected chain):** `EvVerifyPassed` with
  `Disposition == DispositionPublish` → build `Post{Channel: exchange.Channel, Text: result.Output}`
  and `FilePendingPost` (→ `needs_human` with PendingPost) instead of Done. This is the ONE wire
  connecting the manager loop to the approval gate.

`TicketResultSink`, `reconcile`, `chooseAndSpawn`, `ApprovalService.{Approve,Reject,Answer}` all
route through `Transition` (ApprovalService may apply the returned mutation via its own store handle;
the rules live in one function).

## §E · Answer — the escalation resume (fixes: needs_human roach motel)

```go
// ApprovalService gains (same human-action home as Approve/Reject):
Answer(ctx context.Context, ticketID, text string) error
```

Valid on a `needs_human` ticket (with or without a PendingPost — answering a pending post implicitly
rejects-with-guidance is NOT the semantics; if a PendingPost exists, Answer errors and directs the
operator to approve/reject). Applies `EvAnswered`: text appended to `AttemptNotes`, `Result` cleared,
status → `todo`. §8 gains:

```
POST /api/tickets/{id}/answer {text}   → answers an escalation; ticket re-enters the queue
```

watchapi adds the route + an `Answerer` port (satisfied by `*ApprovalService`), handler mirrors
reject's shape (400 empty text, 404 unknown/invalid-state, 200 on success).

## §F · Capacity is in-flight, not lifetime (fixes: permanent dispatch deadlock)

`SpawnLedger` gains `Release(sessionID string)`: decrements the session's PARENT spawn count
(floor 0) and is idempotent per session. `InProcRuntime.Spawn` calls `Release(sid)` after
`Sink.Deliver` returns (success or failure of the model call — any terminal outcome frees the slot).
`Budget.MaxSpawns` is thereby an in-flight fan-out cap, per the original §7 intent. Depth records and
the tree-token ledger are unchanged (tokens genuinely accumulate).

## §G · Incremental planning + parse robustness (fixes: one-shot plan cliff, bootstrap wedge)

- `plan()` runs EVERY tick (not only when zero tickets). The plan prompt includes a compact summary
  of existing non-terminal tickets (`- [status] title`) and instructs: return ONLY a JSON array of
  NEW tickets needed; `[]` when none.
- **Title dedup:** planned tickets whose `Title` matches any existing ticket (any status) are
  skipped — the ScriptedModel double returns the same array every tick, and this guard is also the
  v1 answer to model-side duplication. (Documented limitation: title is the identity key.)
- **Fence-stripping parse:** extract the substring from the first `[` to the last `]` before
  `json.Unmarshal` (handles ```json fences and prose preambles). A reply with no parseable array is
  NOT a tick error: record a telemetry run (`Scope: "manager-plan-unparseable"`), plan 0, continue
  the tick. The next tick retries naturally.

## §H · The verify protocol is structured (fixes: substring coin-flip oracle)

`verifyTemplate` instructs: *"Reply with exactly `PASS: <one-line reason>` or `FAIL: <one-line
reason>` as the FIRST line."* Parsing: take the first non-empty line, uppercase; `HasPrefix "PASS"`
→ pass; `HasPrefix "FAIL"` → fail; anything else → `Verdict{Pass: false, Reason: "unparseable
verdict: " + firstLine}` (conservative: unparseable never advances work; it burns an attempt and
surfaces via AttemptNotes). `strings.Contains` is banned from verify.

## §I · Storage-layer required behaviors (Wave 1)

1. **Empty-JSON round-trip (the gate-bypass bug):** `PgTicketStore.fromRow` maps a stored `"{}"`
   (and `"[]"`/empty) in `Scope`/`Result`/`PendingPost` back to `nil` json.RawMessage, so every
   `len(...)==0` emptiness guard behaves identically on Mem and Pg stores. (A real Post/Scope/Result
   never marshals to bare `{}`.) Parity test: escalated-ticket-approve errors "no pending post" on
   BOTH stores.
2. **Guarded Update (no phantom upserts):** `PgTicketStore.Update` uses an explicit
   `Updates`/`Where(id)` with a `RowsAffected == 0 → error` check — same fail-loud semantics as
   `MemTickets.Update`. gorm `Save` is banned for updates.
3. **Seq allocation reconciled:** keep `MAX(seq)+1` (the sqlite fast-test story requires a
   driver-portable explicit seq) but wrap the insert in a **retry-on-unique-violation loop** (re-read
   MAX, re-derive id, retry; bounded attempts) in BOTH `PgBoard.Append` and `PgTelemetry.Record` —
   cross-process safe. UPDATE the stale CONTRACT comment on `agentdb.BoardRevision` to describe this
   convention (the "let Postgres assign seq" guidance is superseded; note that the BIGSERIAL default
   is intentionally unused).
4. **One fold:** extract the revision-fold loop into `agentdb.FoldFragments(revs []BoardRevision,
   targetRev string) (Board, error)`; `MemBoard.AsOf` and `PgBoard.AsOf` both call it. (Kills the
   dev/prod fold-divergence risk.) Ops with entity types other than `prompt_fragment` remain
   skipped — but `Append` in both stores now REJECTS a changeset containing an op whose EntityType
   is not `prompt_fragment` (fail-loud instead of silently discarding writes; the deferred entity
   types return post-v1 with the bus).
5. **Spend is always counted:** `MemSpendMeter.Charge` records the spend BEFORE returning
   `ErrSpendCeiling` when the recording pushes it over — i.e. the check is "was ALREADY at/over
   ceiling before this charge" (unchanged) but a charge made past the probe is never dropped;
   `AnthropicModel`'s post-call charge handles/logs-and-records rather than `_ =` discarding.
6. **Migration `025_remediation`:** `ALTER TABLE tickets ADD COLUMN IF NOT EXISTS disposition
   VARCHAR(20) NOT NULL DEFAULT ''`, `ADD COLUMN IF NOT EXISTS attempt_notes JSONB NOT NULL DEFAULT
   '[]'`; `ALTER TABLE runs ADD COLUMN IF NOT EXISTS ticket_id VARCHAR(36) NOT NULL DEFAULT ''`,
   `ADD COLUMN IF NOT EXISTS session_id VARCHAR(36) NOT NULL DEFAULT ''`. gorm rows updated to
   match, column-for-column.
7. **watchapi reject ordering (non-retryable note loss):** the handler applies the note via
   `FeedbackApplier` BEFORE calling `Rejecter.Reject`; a feedback failure → 500 with the pending
   post INTACT (fully retryable). Additionally `ApplyHumanFeedback` for `ticket:`/`run:` targets
   SEEDS the `routing-guidance` fragment (via `WriteFragment` with the note as the initial body)
   when it does not exist yet, instead of erroring — a fresh board must not reject its first lesson.

## §J · Deletions

- `orchestrator.RevisionDTO` (contracts.go) — dead code, drifted from the served shape. DELETE;
  `watchapi`'s DTO is the wire truth for §8.

## §K · Explicitly deferred (recorded, not forgotten)

Outcome ingestion (channel signals → feedback; needs a prompt-injection design pass) · fold
caching / materialized board state (perf; fine at v1 scale) · pagination on `/api/runs` +
`/api/board/revisions` (+ drop Prompt from the list DTO) · Tick mutual exclusion lease (single-box
v1 accepts the small race; the state-machine CAS-style guards reduce the blast radius) · TicketDTO
snake_case projection · dead-field cleanup (`Scope.Tools`, `TickReport` consumers).

## Acceptance for the whole remediation

`go build ./... && go vet ./... && go test ./...` green, plus a NEW end-to-end narrative test
(Wave 2) proving the previously-disconnected chain: goal → plan → worker draft → verify pass with
`DispositionPublish` → PendingPost on needs_human → `Approve` → FakeConnector records the publish →
ticket Done with `PublishedRef` — and its sibling: verify FAIL → AttemptNotes reach the retry
prompt → improved draft passes.
