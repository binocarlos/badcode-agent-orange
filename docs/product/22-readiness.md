# Readiness — what has to be true before people use this

*Written 2026-07-29, from Kai's ask: "do as much work up front as possible to guarantee the success
of the system when we actually do start using it with people." Status: **audit programme in
flight; findings and items land below as they arrive.** Companion to
[`13-work-plan-self-improvement.md`](./13-work-plan-self-improvement.md), which carries the
experiment workstream; this file carries the production-readiness one.*

## 1. The bet this document is hedging

Everything we are confident about is **mock-shaped**. Well over a thousand tests are green, and
almost all of them run against a scripted model that says what we told it to say. The one time
this system met reality — the live calibration of 2026-07-28 — three defects surfaced inside forty
minutes, and none of them were exotic. That is the base rate to plan against: *contact with
reality finds things at a rate our test suite does not.*

The first users are people, not harnesses. A harness that hits a defect reports it. A person hits
the same defect and concludes the product does not work.

## 2. The failure class that actually threatens us

Not crashes. **Silent success** — something reports success, or reads zero, or quietly does
nothing, and nothing fails at use time. Four confirmed instances, all found by accident:

| Instance | What reported success | How long it hid | How it was found |
| --- | --- | --- | --- |
| TOK1 | Three token readers summed 0 across 942 real rows; `daily_tokens_*` never fired | Weeks — the product's only spend brake was inert | A rig needed the number for something else |
| Empty-session delivery | A delivery recorded `ok` for a session in which the model said nothing | Until it killed a live run | The 2026-07-28 calibration |
| sqlite fallback | Without `DATABASE_URL`: router never routes, schedules never fire, core MCP not mounted, settings do not apply — no error | Unknown; documented, not detected | Reading the code |
| Helper thaw | A whole-object PUT dropped `frozen`, silently unfreezing a frozen worker | Never fired — no test froze a worker first | A sibling item's review |

The common root is not carelessness. It is that **a reader and a writer disagreed, and only the
reader was tested** — usually against a fixture that no production writer could have produced.
TOK1's corrected pattern (`go/agentdb/token_usage.go`: a captured real envelope, plus a test
pinning that the wrong shape appears in zero stored rows) is the template for the fix.

**Standing rule, promoted from that lesson:** a fixture must be *captured* from a real writer, not
authored to match the reader. An invented fixture is a silent-success generator.

**Second standing rule, learned while auditing (RD25):** *a tool reporting "no matches" is not
evidence of absence.* `grep` silently skips files it deems binary, and five `web/src` files qualify
— so a clean search over that directory has been meaningless for as long as those bytes have been
there. The orchestrator's own first attempt to verify this used `$'\x00'`, which bash collapses to
an empty string, and consequently reported **every** file as affected. Both failures are the same
shape as the defects we are hunting: a confident negative that was never true. Verify a negative
with a second, differently-built method before believing it.

## 3. The audit programme (2026-07-29, in flight)

Three read-only sweeps, no stack, no writes — evidence before items, deliberately:

1. **Silent success** — more members of the class above, across `agentdb`, `cmd/agentd`, `httpapi`,
   `compose.go`, `events`, `sandbox`. Judgement test: *if this were broken now, what would tell
   us?*
2. **Durability** — what a real user loses and when, across the session lifecycle (archive loop,
   idle timeout, snapshot TTL reaper, sweep, delete). Deliverable includes a plain durability
   table, which the repo does not currently have anywhere. Prompted by the live run's discovery
   that `agent_query_events` held zero rows for every session, including successful ones.
3. **First run** — the first hour of someone who has not read the source: documented commands
   against actual code, `.env.example` completeness, every config whose absence degrades silently,
   the actual text of the errors a newcomer hits, and whether there is a path from empty project
   to running worker without hand-editing anything.

## 4. The readiness bar

Not "no known bugs" — that bar is never met and pretending otherwise is how people ship anyway.
The bar is:

1. **No silent failures on the paths a first user walks.** Everything that can fail either fails
   loudly or is visible in the UI. Degradation is announced.
2. **Nothing a user made disappears without them being told**, and the durability table is written
   down so we can answer "is that gone?" without reading source.
3. **Every error a newcomer can reach says what to do next**, not only what went wrong.
4. **The first-run path works end to end without hand-editing** — empty project → topology →
   worker → first job → visible result.
5. **The brakes are watched firing.** Every ceiling and gate has a test that observes it fire, and
   is proven non-vacuous by breaking it (the TOK1 revert-and-fail discipline, now standard).

## 5. Items

*Populated from the audits. Nothing is listed here until there is evidence for it — the work plan's
rule against pre-ticked, pre-invented items applies to this file too. Execution rules are the work
plan's: isolated worktrees, validation commands run verbatim, sequential merges, and the
do-not-touch list (`.env`, `ao-test-pg`, production seeding, the real API).*

### From the silent-success audit (2026-07-29)

All four verified by the orchestrator against the source before filing — writer *and* reader read
in each case.

- [x] **RD1 — A schedule disables itself permanently on a transient database error, and records a
  false reason.** *Fixed and merged 2026-07-29. Three durable sites, not two — the survey found
  `dispatch.go:281` (`GetProjectEvent`) with the same shape 60 lines below the known one. The
  dispatch sites now retry rather than fail, matching the contract already stated in three places
  in that file and used by the three sibling store reads immediately following. Non-vacuity proven
  per site; the "still disables for a genuinely missing worker" twin keeps passing.* *Worst first-user failure found: silent, permanent, and actively
  misdirecting.* `go/cmd/agentd/scheduler.go:203-209` treats **any** `GetWorker` error as "the
  worker no longer exists", disables the schedule (a durable config mutation) and writes a
  `schedule_disable` config event reading `worker <name> no longer exists` while the worker sits
  right there. Every other caller in the codebase classifies correctly with
  `errors.Is(err, agentdb.ErrWorkerNotFound)` — `topology_apply.go:210`, `sessioncontext.go:152`,
  `httpapi/workers.go:157`, seven sites in `mcp_management.go`; the scheduler is the sole
  exception. It also runs BEFORE `ClaimFiring`, so the `ScheduleMaxProvisionFailures` streak
  safeguard is bypassed entirely: one blip during the due second, not five failures. A user's
  daily job stops happening forever and the config log misdirects whoever investigates.
  **Fix:** disable only on `ErrWorkerNotFound`; on any other error log and return so the next tick
  retries. Same unclassified-error shape at `dispatch.go:219-224` marks a delivery `failed`
  permanently with the trigger already consumed — fix both, and add a test that a transient store
  error leaves the schedule enabled.
  *Validation:* go suite + live-Postgres (work plan F1's command).
- [x] **RD2 — The token ledger counts only uncached input tokens, so the spend brake still
  under-reads real spend.** *Fixed and merged 2026-07-29. Metered on tokens (all input components
  summed), not cost — the settings are named and displayed in tokens, and a cost ceiling would
  silently re-scale every project's brake when list prices move; `totalCostUsd` is now written on
  the error path too, so a cost-denominated ceiling later is a reader change. Backward compatible:
  pre-fix two-key envelopes read exactly their old values, pinned by a live-PG test. Non-vacuity:
  reverting the reader SQL produced 7 live-PG failures headed "the brake is under-reading real
  spend by 99%"; reverting the writer, 5 sandbox failures; reverting `readUsage`, 4 web failures.* TOK1 fixed the jsonb *path*; it did not fix *which fields* are
  written. `sandbox/src/harness/claude-agent-sdk.ts:693-701` forwards only
  `usage.input_tokens` / `usage.output_tokens`, but the SDK's usage object also carries
  `cache_creation_input_tokens` and `cache_read_input_tokens` — both billed, neither included in
  `input_tokens`. With a large composed prompt and caching active, most input arrives as cache
  reads, so `daily_tokens_hard` fires far later than intended or never. **This is worse than
  TOK1's zero**: it reads as a plausible non-zero number, and nobody investigates a counter that
  appears to work. Both existing fixtures are *invented* — `sandbox/src/harness/harness.test.ts:216`
  and `mock-server/src/streaming.ts:47` produce usage objects with only two keys, which no real
  API response has ever had. Exactly the pattern `go/agentdb/token_usage.go` exists to end.
  **Fix:** forward the whole usage object and sum every input component in `usageInputSQL`; or
  meter on `data.totalCostUsd`, already stored and already truthful. Capture a real envelope as
  the fixture; pin that a two-key usage object is not what production writes.
  **RD2b, same file:** the non-success paths (`:706`, `:717`) discard usage entirely, so a turn
  that burns tokens and then hits `error_max_turns` contributes 0 — the runaway case is the one
  that goes unmetered.
  *Validation:* sandbox typecheck + tests, go suite + live-Postgres, and the budget-gate e2e
  proven non-vacuous by reverting (TOK1's discipline).
- [ ] **RD3 — `memory_create` reports `"embedded": true` while storing a row with no embedding,
  permanently.** `go/agentdb/memories.go:137` drops the vector from the INSERT when the
  `content_embedding` column is absent; `go/cmd/agentd/mcp_memory.go:288` reports
  `Embedded: vec != nil` — true whenever the *embedder* returned a vector, regardless of what was
  stored. The comment two lines above states the correct principle ("CreateMemory returns the row
  as the database holds it… that is what is echoed") and the next line violates it. The column can
  genuinely be absent: migration `022_memories` (`migrations.go:293-304`) swallows a failed
  `CREATE EXTENSION vector` with `RAISE NOTICE`, which is exactly what happens on managed Postgres
  where the app role lacks the privilege — **the GCP deployment direction**. Memories are
  append-only with no update path by design, so those rows can never be embedded later and search
  silently degrades to keyword-only forever. Second route: `memoryHasVectorColumn`
  (`memories.go:373-386`) latches `sync.Once` on `err == nil && n > 0`, so one transient query
  error pins it false for the process lifetime and blames an absent column.
  **Fix:** distinguish "query failed" from "column absent" so the flag cannot latch on error;
  derive `embedded` from what the store actually wrote; fail the create when a vector was supplied
  and cannot be stored (matching the write path's existing fail-hard-on-embedder-error decision).
  *Validation:* go suite + live-Postgres, including a case with the vector column absent.
- [x] **RD4 — A worker's config change can be recorded as a human edit.** *Fixed and merged
  2026-07-29, at **two** seams because neither alone holds the invariant: the auth seam now refuses
  when a present session store cannot answer, and a new write-seam choke point refuses an
  unidentified caller — which also covers the nil-store route, a token with no `sid`, and a session
  row with an empty worker. `refuseFrozen` always names its actor now, so `worker.freeze_refused`
  can never read as a human refusal by silence. The comment that had justified the degradation was
  **stale**: it blamed the SQLite fallback, but the core MCP server is mounted only when Postgres
  is present, so that route cannot occur. Non-vacuity: 12 simultaneous failures on revert; the
  table runs every tool twice — once identified (must write) and once not (must refuse) — so a
  mis-seeded fake fails as proving nothing, which caught three vacuous cases during development.*
  `go/cmd/agentd/mcpserver.go:445-455` logs and continues when the session lookup fails, leaving
  `caller.Worker` empty; that empty string reaches `ConfigEvent.ActorWorker`
  (`mcp_management.go:690-696` → `config_events.go:375`), and an empty `actor_worker` is precisely
  how a **human** edit is recorded (`httpapi.go:175`) and rendered (`web/src/configLog.ts:28`). So
  a worker rewriting another worker's prompt during a database blip appears in the changelog as an
  operator hand-edit. `refuseFrozen` shares the hole, so `worker.freeze_refused` — the most
  interesting signal the system produces — can lose its actor. This undermines the attribution the
  acceptance loop and doctrine OM-10 both depend on.
  **Fix:** treat an unreadable session as a hard refusal at the MCP auth seam, or carry an explicit
  unknown-actor marker so `actor_worker == ""` keeps meaning "a human" and nothing else.
  *Validation:* go suite + live-Postgres; a test pinning that no MCP-originated config event can
  carry an empty actor.

### From the durability audit (2026-07-29)

Twelve findings; the four headline claims were verified by the orchestrator against the source
before filing. Ordered by *when* they must be fixed, not by cleverness.

**Blockers — fix before any person uses this for real work.**

- [ ] **RD5 — Deleting a session destroys the conversation, irreversibly, from an unguarded
  one-click button.** `agent_query_events.session_id REFERENCES agent_sessions(id) ON DELETE
  CASCADE` (`go/agentdb/migrations.go:139`; same for `agent_messages` at `:77` and
  `agent_artifacts` at `:50`), and `DELETE /agent/session/{id}` is a hard row delete
  (`go/httpapi/lifecycle.go:183` → `go/agentdb/sessions.go:187`). It is fired from an icon button
  with **no confirmation dialog** (`web/src/components/AgentSessionList.tsx:53-56`). There is no
  soft delete, no tombstone and no export. **This makes the calibration run's empty
  `agent_query_events` a product property, not a rig artefact** — the rig deleted sessions per
  hypothesis and the code did exactly what it does for any user tidying their session list.
  *Additional defect found while verifying:* the handler **discards the delete error** (`_ =`) and
  returns `204 No Content` regardless — a failed delete reports success. That is the silent-success
  class again, in the same three lines.
  **Fix:** `deleted_at` on `agent_sessions`, listings filtered on it, cascade retained for a
  separate operator purge; a confirmation step in the UI; and stop discarding the error.
  *Validation:* go suite + live-Postgres + web tests.
- [ ] **RD6 — A crash mid-turn loses the model's entire response.** *Amended 2026-07-29 after the
  sandbox-buffering question was settled by reading; the open question is now closed.* The user's
  **prompt survives** — `seedUserMessage` writes it to `agent_query_events` before the sandbox is
  called, under `context.WithoutCancel` (`go/runner.go:2338-2353`, called at `:892`). What is lost
  is everything the model said: the assistant's output becomes durable exactly once, at
  `query_complete` (`runner.go:943` → `events/pipeline.go:200-227`), and until then it lives in the
  `collected []Envelope` local inside `pipeline.Run`, whose **only caller in the repo** is that one
  line. `NewPipelineWithCadence`, written for exactly this case, has zero non-test callers.
  **The data survives agentd's death — in a place agentd has no code to read.** The sandbox buffers
  up to 2000 events in RAM (`sandbox/src/services/stream-service.ts:20,116-126`) and replays them
  to a newly attached stream (`:59-78`), so a reconnecting **browser renders the turn** while
  nothing writes it to Postgres: `Runner.Stream` serves both `/stream` and `/reconnect` and is a
  bare `io.Copy` (verified at `go/runner.go:975`) that never constructs a `QueryContext` and never
  touches the pipeline. The window then shuts by itself — `endQuery` deletes the buffer immediately
  after `query_complete` (`stream-service.ts:265`) — and the buffer silently drops its oldest events
  past 2000. This is a **wiring gap, not a limit**: cheap to fix, and worse to leave.
  Confirmed while settling it: the sandbox persists no conversation state at all — `StreamService`
  is in-memory maps, `conversationHistory` is a plain instance field, and `persistSession: false`
  is passed to the Agent SDK (verified at `sandbox/src/harness/claude-agent-sdk.ts:545`).
  **Fix — two changes, and (a) alone is not enough:** (a) pass a cadence to the default pipeline
  (`runner.go:167`), fixing the crash case; (b) route `Runner.Stream` through the pipeline so a
  reconnect drains the sandbox buffer into Postgres (`runner.go:975`). Doing only (a) leaves the
  reconnect path silently lossy.
  *One stack confirmation remains, now a check rather than an unknown:* start a long turn,
  `docker kill` the **agentd** container, restart, hit `/reconnect`, and observe that the events
  render while `agent_query_events` gains no row.
- [ ] **RD24 — After an agentd restart, the model remembers a turn the user cannot see.**
  Rehydration from the database runs **only** on the snapshot-restore path; the orphan-recover path
  deliberately skips it because it re-adopts a still-running container that already holds its
  in-memory history (the code says so at `go/runner.go:1396-1402`, verified). Combined with RD6,
  that means: agentd dies mid-turn, the container survives, `Recover` re-adopts it — and the
  harness keeps a turn in `conversationHistory` that Postgres never recorded. For the life of that
  container the model answers with reference to something the user cannot see and no operator can
  retrieve; then it is silently erased at the next archive/restore, when `loadConversation`
  overwrites the array from the database. A user asking "why did it say that?" has no way to find
  out, and the divergence heals invisibly.
  **Fix:** falls out of RD6(b) — if reconnect drains the buffer into Postgres, the two views
  reconverge. Worth an explicit test either way.
- [ ] **RD7 — A job can wedge in `running` forever, permanently consuming two capacity slots.**
  `settle` releases the session lease *before* stamping the delivery's terminal status
  (`go/cmd/agentd/dispatch.go:619` then `:621`). A crash in that window — or a failed status write,
  whose error is only logged — leaves `status='running'` with `lease_expires_at=0`, and
  `ListExpiredLeaseSessions` filters on `lease_expires_at > 0` (`go/agentdb/leases.go:90`), so the
  reaper can never see it. Nothing else sweeps deliveries by age. The row eats a `max_instances`
  and a `max_concurrent_jobs` slot for the life of the project. Note `dispatch.go:351` calls the
  lease reaper "the backstop" — by that line the lease is already released, so it cannot be.
  **Fix:** stamp terminal status first, release the lease second; add an age-based sweep over
  `running` deliveries.
- [ ] **RD8 — A container whose session row is gone leaks a host port permanently.** `Recover`
  re-adopts any container labelled with a session id (`go/execenv/docker/dind.go:484`), but with no
  session row `r.Snapshot` fails and the archive loop logs "snapshot failed, keeping the container"
  and `continue`s — every minute, forever (`go/runner.go:1101-1104`). One port from the pool of 100
  lost per occurrence, with no way back short of a restart.
  **Fix:** if the session row is absent, destroy rather than skip.

**Before sustained use — these degrade a working deployment over days.**

- [ ] **RD9 — A snapshot in daily use is reaped out from under the user.** `snapshotExpired` tests
  only `ci.ExpiresAt` (`go/cmd/agentd/snapshot_reaper.go:196`); `last_resumed_at` is stamped on
  every launch (`imageresolver.go:122-125`) but does not extend expiry, despite the reaper's own
  header implying it bears on it. A worker pinned to a named image gets a hard
  `ErrCustomImageUnmaterialisable` 30 days after burn regardless of use.
  **Fix:** extend expiry on resume, or warn before reaping anything recently resumed.
- [ ] **RD10 — Two concurrent drains can double-dispatch one delivery**, so a user's job runs
  twice. `UpdateDeliveryStatus` is a read-then-`Save` with no compare-and-set on
  `status='pending'` (`go/agentdb/events.go:733-757`); the only guard is against an in-memory
  snapshot (`dispatch.go:209`). `DrainPending` runs from two goroutines in one process — the router
  loop every 3s (`router.go:364`) and the scheduler loop (`scheduler.go:191`). The orphan session
  then emits a spurious `worker.failed{lost}` 15 minutes later.
  **Fix:** conditional update on the pending→running transition; act on `RowsAffected`.
- [ ] **RD11 — Missed schedule occurrences vanish without a trace.** `scheduler.go:156-161`
  evaluates only the current minute — no watermark, no catch-up. An hour of downtime means 60
  occurrences with no firing row, no event, no delivery and nothing recording that they were
  missed: indistinguishable from "never scheduled". Worse in a narrow window: a kill between
  `CreateProjectEvent` (`:227`) and `EnsureDelivery` (`:248`) leaves a `schedule.fired` event in
  the user's feed for a job that never ran, with the occurrence permanently consumed.
  **Fix:** per-schedule watermark; emit `schedule.missed` on restart for unevaluated occurrences.
- [ ] **RD12 — Archiving for idleness permanently mislabels artifacts as lost.**
  `teardownInstance` is shared by `Destroy` and the archive loop and unconditionally calls
  `Artifacts.MarkLost` (`go/runner.go:694`), stamping un-uploaded `live` artifacts as `lost`
  (`agentdb/artifacts.go:141-143`). For the archive path that is false — the snapshot is taken
  first, so the files return on restore — and nothing ever regresses the status. Contradicts
  `gc.go`'s own header claim that "nothing a user can see is lost".
  **Fix:** flag on `teardownInstance` so the archive path skips `MarkLost`.

**Integrity, cost and polish — real, not urgent.**

- [ ] **RD13 — The config log's append-only guarantee is enforced only in tests, and the fold has
  no production caller.** `InstallConfigEventGuard` is opt-in and every caller is a test
  (`agentdb/config_events.go:844`); `cmd/agentd/main.go:259-266` states agentd never arms it;
  `config_events` is not in the guarded-table set; migration 026 adds no trigger or `REVOKE`; and
  `Store.DB()` is exported. Separately `FoldTo` has no production callers and diverges from reality
  in three ways (project prompt folds as a second entity from the settings row it lives in and the
  two disagree after a mixed sequence; skills fold by name while `agent_skills` holds a row per
  revision; five wired paths mutate guarded tables with no config event at all).
  **Fix:** a DB-level `BEFORE UPDATE OR DELETE` trigger permitting only `emitted_at`; and either
  wire the fold to a real read path or mark it experimental. *This one matters more than its
  position suggests — the config log is what the doctrine work treats as the record of truth.*
- [ ] **RD14 — Every archive cycle orphans a full container-image archive.** `blobPathFor` keys on
  the committed image ref (`imageregistry/blobarchive/blobarchive.go:228`) and `ContainerCommit`
  returns a fresh digest each time (`execenv/docker/dind.go:354-358`), so each archive writes a new
  blob and `SetSnapshotHandle` overwrites the pointer to the previous one. `blobs.Delete`'s only
  production caller is the catalogue reaper, which never sees session snapshots. With
  `SupportsDiff=false` these are full archives. Unbounded storage growth; nothing user-visible.
  Same asymmetry on delete: the session cascade destroys the *index* to a user's artifacts while
  the *bytes* stay on the bill forever (`artifacts.ArtifactStore` has no Delete method at all).
  **Fix:** remove the previous blob after a successful `SetSnapshotHandle`, and the current one on
  session delete.
- [ ] **RD15 — "This ran" without "what it said".** `event_deliveries.session_id` is a plain
  VARCHAR with no foreign key (`migrations.go:352`), so job history outlives the session it points
  at: after a delete the user sees a green `ok` delivery with a dead transcript link. And a
  delivery that fails to start persists no reason — no reason column (admitted at
  `dispatch.go:432-435`), no `worker.failed` emitted.
  **Fix:** a `failure_reason` column; render a "transcript deleted" state rather than a broken link.
- [ ] **RD16 — Transcript ordering is a coin toss within a second.** `ListQueryEvents` orders by
  `created_at ASC` (`agentdb/messages.go:188`) where `created_at` is `time.Now().Unix()` —
  **seconds** (`:167`) — and the id is a random uuid. Two queries in the same second replay in
  arbitrary order. This is the identical hazard migration 028 added `revision` to fix for skills;
  the transcript never got the same treatment.
  **Fix:** milliseconds, or a per-session monotonic ordinal.

**Docs that contradict the code** (the code wins; fix the prose with the item): `gc.go`'s "nothing
a user can see is lost" (RD12); `dispatch.go:351`'s "the backstop" (RD7); `router.go:513-516`'s
at-most-once justification, which `ReleaseSessionLease` breaks by ignoring `RowsAffected` and
treating a missing row as success (`leases.go:66-79`) so two reapers both emit `worker.failed{lost}`;
and `snapshot_reaper.go:21` implying `last_resumed_at` bears on expiry (RD9).

**Discoveries from fixing RD1/RD2/RD4** (2026-07-29), each worth more than its item:

- **`SDKResultError` carries full usage *and* cost** — the error path was not missing data, it was
  throwing it away. Worth checking whether other "error → report nothing" paths discard available
  data rather than lacking it.
- **The e2e stack spec had the same arithmetic bug as the code it guards**
  (`config-and-workers.stack.spec.ts:336` summed the same two fields), agreeing with the broken
  reader instead of the writer. *A spec that mirrors the implementation's arithmetic cannot catch
  the implementation's arithmetic.* Same one-line defect existed in all three
  `e2e/experiments/*/runner.ts` token counters.
- **Four mock writers, one lie**: `mock-server/streaming.ts`, `go/mockmodel`, `go/modelproxy` and
  `go/modelproxy/script.go` all emitted the same invented two-key usage. The named fixture was a
  quarter of the surface — when a fixture shape is wrong, grep for every writer of that shape.
- **A fake store returned an invented error string instead of the package's sentinel**
  (`fakeDispatchStore.GetProjectEvent`), so no test could distinguish the classified path from the
  unclassified one. The same fake wrapped the sentinel correctly for `GetWorker`, which is why the
  missing-worker tests passed all along and the bug stayed invisible. **Fixtures must be captured
  from the writer — including error fixtures.**
- **`go test ./cmd/agentd/...` was green before any test was added**, meaning none of the three
  misclassification sites had a single test touching their error path: covered for success and for
  genuine absence, never for failure.
- **A comment outlived its premise and kept a hole open.** `mcpserver.go`'s auth comment justified
  degrading provenance by "the SQLite fallback, which knows nothing of workers" — but the core MCP
  server is mounted only when Postgres is present, so that route cannot occur. Worth grepping for
  other comments justifying a behaviour by a condition the wiring no longer permits.
- **The retry contract was documented in three places and both buggy sites ignored it** — a
  well-stated convention still shipped two deviations in one file.

**Further unclassified-error sites found and reported, not fixed** (queued behind the audits'
items): the inverse `err == nil`-as-proof-of-existence form at `dispatch.go:627` (a read failure
means the compensating write never happens, so a half-started session keeps a stale lease forever),
`dispatch.go:565`, and `httpapi/session.go:140/146` (a background goroutine that is the only writer
on that path, so a session stays `creating` permanently and the `create_error` diagnosis is
discarded — which is also why RD20's `create_error` may be empty when the UI finally reads it).
Transient-rendered-as-404: `attention.go:229` is the worst — the human-escalation path tells a
worker its own session does not exist on a database hiccup — plus the subscription trio in
`httpapi/events.go` and two sites in `lifecycle.go`.

**Meta-finding, promoted to §2's standing rule:** RD2's survival has the same root cause as TOK1's
— a fixture authored to match the reader rather than captured from the writer. Two of the four
findings are token-metering defects in a system whose only spend brake is token metering.

### From the first-run audit (2026-07-29)

**The finding that matters most in this whole document:** *the journey is polished right up to the
point that matters, and then stops.* Empty project → first-run card → topology interview → apply →
success receipt is genuinely good work. Then nothing can run, and the default configuration talks
to a mock model the UI never labels. Together those two mean the likeliest first hour ends with a
user who has built an org chart, seen no output, and cannot tell whether nothing fired or the model
was never real.

- [ ] **RD17 — There is no way to trigger the first job from the UI.** *The blocker.* Verified
  directly: **no `POST` to `/agent/events` exists anywhere in `web/src` or `examples/web/src`** —
  every POST is session create/message/cancel/restore, voice, attachments, topology preview/apply,
  or auth. 11 of the 14 built-in topologies are event-driven only; the three with a schedule fire
  hourly at :00 at the fastest (`go/topology/solo.go:26-30`). The topology interview *asks* which
  event type wakes the worker (`go/topology/supervisor.go:82`) and then offers no way to send one.
  No MCP tool emits events either. The code states the omission deliberately —
  `web/src/events.ts:33` ("this UI only ever GETs"), `EventReplayPanel.tsx:4-8` ("a 'test' button
  that quietly posted a real event into a project would be a footgun") — and the replay panel
  instead prints the curl command for the user to run by hand (`:91`). **It is already a known
  gap**: `docs/product/15-operator-console-design.md:258-266` says "after applying a topology whose
  only clock is cron, an operator has no way to make anything happen… **One button.**" Not built.
  A working side door exists (Workers → worker → Chat tab) but nothing points at it and it
  exercises the chat path, not the delivery pipeline just configured.
  **Fix:** build that one button — "Emit this event" on the replay panel, posting the draft it
  already composes, behind a confirm that says it writes a real event.
- [ ] **RD18 — Mock mode is invisible, and it is the default.** Both credential lines ship blank
  in `.env.example:9-10`, so a user following the README exactly gets the mock model. agentd logs
  it to stdout (`go/cmd/agentd/modelproxy.go:49`); the browser is told nothing — verified that
  `/auth/config` returns only `{modes, google_client_id}` (`googleauth.go:317-334`) and
  `examples/web/src/App.tsx:125` hardcodes an Opus label regardless of the actual proxy. The one
  mitigation is accidental (the unscripted mock's reply self-identifies) and it disappears under a
  mock script and never applies to a **worker job** — a scheduled job in mock mode writes plausible
  canned output into Desk, Events and Jobs with no marker anywhere. With RD17 this is a credibility
  failure, not a UX one: a user who does get output may conclude the product works when no model
  was ever called.
  **Fix:** add credential mode (`mock`|`api-key`|`subscription`) to `/auth/config` and render a
  persistent badge. One field, one component.
- [ ] **RD19 — An event matching no subscription vanishes silently.** `go/cmd/agentd/router.go:235-247`
  loops subscriptions, skips non-matches, and marks the event delivered — zero matches is
  byte-identical to a healthy no-op: no log, no row, no annotation. Write-time validation does not
  help: `go/agentdb/events.go:477-498` checks the event type's *shape* but never that the worker
  exists or the type is known, so `worker.faild` is accepted; envelope filter keys are unvalidated
  and any key the envelope lacks returns false (`router.go:417-420`). Same shape for briefing
  selectors: `go/compose.go:224-232` skips an empty selector silently, so a typo and "nothing
  written yet" are indistinguishable and the job runs with a quietly thinner prompt. "My worker
  didn't wake up" has no observable signal at all.
  **Fix:** one log line at `router.go:247` on zero matches (project, type, subscriptions
  considered) and at `compose.go:224`; validate worker existence and filter keys at write time.
- [ ] **RD20 — Failure reasons never reach the user.** Two audits found this independently, from
  opposite directions (see also RD15). `dispatch.go:437-438` logs the reason and
  `UpdateDeliveryStatus` writes status only; the code admits it at `:195-197` and the UI carries
  the limitation honestly (`web/src/desk.ts:277-279`). So the newcomer's answer to "why did my
  worker fail?" is `docker compose logs agentd`. Related and equally invisible: `create_error` is
  populated on provisioning failure and served at `httpapi/lifecycle.go:125` with **zero consumers
  in `web/src`** — the UI renders a bare `status: "error"`, and the good explanatory message only
  appears if the user sends a *second* message (`go/runner.go:1376`).
  **Fix:** a `reason` column on the delivery row, populated from `d.fail(...)` where the string
  already exists; and read `create_error` wherever `status === "error"` renders.
- [ ] **RD21 — Doctrine reaches no user today.** Confirmed by the audit and consistent with D5's
  design: the only caller performing the doctrine mutation is
  `e2e/experiments/calibration/doctrine.ts`. No topology sets a doctrine `SettingsPatch`; zero
  occurrences in `web/src` or `examples/web/src`. So a new user applying a topology gets none of
  it — not the injection-boundary rule, not the frozen-worker rule, not "no effect is a valid
  result". Defensible while every entry is `candidate`, but it means the doctrine work has **zero
  effect on the first production seeding** unless someone pastes the block by hand.
  **Fix:** an opt-in checkbox on topology apply ("seed the project prompt with operations doctrine
  v1"), or say in `docs/18` that operators should paste it and where it lives. Either way, stop it
  being invisible.
- [ ] **RD22 — Postgres credentials are undocumented and the volume initialises once.**
  `docker-compose.yml:69` builds `DATABASE_URL` from `POSTGRES_USER`/`PASSWORD`/`DB`, defaulting to
  the literal `agentorange`; `.env.example` has **zero** `POSTGRES` hits. The trap: `pg-data`
  initialises on first `up`, so setting a password afterwards re-renders `DATABASE_URL` but not the
  database, and agentd dies with raw gorm text naming neither the `postgres` service nor the stale
  volume.
  **Fix:** document the three in `.env.example`, noting a change needs `docker compose down -v`;
  wrap the connect error with what to check.
- [ ] **RD23 — Documentation that asserts missing capabilities that exist.** Cheap, and it misleads
  every newcomer *and* every agent. `docs/18` carries four stale claims, all in the direction of
  "this doesn't exist" when it does: `snapshot_ttl_days` called inert though `main.go:269,288,296`
  wires the reaper; "no `GET /agent/images` route" though `httpapi.go:305` registers it and the
  image picker uses it; "no other HTTP route takes a rationale" though six do; "no server-side
  worker filter" though `history.go:112-114` implements it and a test calls the doc's text the
  *old* behaviour. Plus a cross-link to a docs/15 section whose title now says the opposite, and
  the same reaper claim in `docs/01-architecture.md:280-281`. Also `.env.example:73-74` documents
  the pre-GC port lifecycle and contradicts itself twenty lines later, and
  `installations/README.md:75-76` claims `.claude/` is gitignored when `git check-ignore` says it
  is not.
  **Fix:** one commit deleting the false claims and repointing the link.

**Smaller code issues worth a sweep item** (from the same audit, not individually filed): nothing
polls, so the Desk shows nothing new after a job runs until a view switch or reload; a blank system
prompt saves silently; an invalid API key produces a raw SDK error with **nothing in agentd's log**,
so the operator has nowhere to look; `BASE_IMAGE` (documented) vs `AGENTKIT_IMAGE` (read) diverge
outside compose; the org-chart clock deep link drops its arguments; the topology done-screen
changelog link is dead code; "New session" gives no feedback; a zero-project account dead-ends with
no create form; and a typo'd project silently creates an empty one (projects are implicit from the
JWT claim with lazy creation).

**Verified correct, and worth knowing** (the audit's clearing list is unusually informative): cron
validation is the best diagnostics in the product — write-time 400 naming the field with a worked
example; **port-pool exhaustion is the best error message in the codebase**, naming the pool, its
size, what holds it and what to do, and correctly delivered inside the message stream; boot-time
validation is generally excellent (port range, GC durations, MCP allowlist, backend enums, login
secrets all fail loudly and actionably); `CLAUDE.md`'s `DATABASE_URL` claim holds and is in fact
*better* instrumented than stated, with four distinct boot lines announcing the degradation; all 26
core MCP tool names in `docs/18` match the code exactly; and every command, path, route and
relative link in the four newcomer docs resolves.

**Already fixed, doc is stale:** `CLAUDE.md`'s warning that CI's web job "breaks when this branch
reaches `main`" — verified fixed in `59e1ea8`; `.github/workflows/ci.yml:84` runs `npm ci` with a
comment explaining why. The lockfile inventory around it is still accurate. *Corrected in the same
commit as this entry, since every agent reads that file.*

**Local-machine hazard, not a repo issue:** `secrets/gcp-key.json` in this working tree is a
root-owned **directory**, not a file, and `docker-compose.override.yml:11` bind-mounts it at
`/gcp/key.json` on every bare `docker compose up`. ADC will fail in a way no doc explains. The
override is gitignored and untracked, so a fresh clone is unaffected — **Kai's tree only.**

### From the browser sweep (2026-07-29)

- [ ] **RD25 — Five UI source files are invisible to `grep`, and three of them are invisible to
  `git diff`.** *Filed first because it undermines the reliability of every other search anyone has
  run over `web/src`, including our own audits.* A NUL used as a composite-key separator was
  written as a **literal byte** rather than an escape, in `components/WorkerEditor.tsx` (offset
  4168), `useStagedFeed.ts` (5086), `useEvents.ts` (6244), `desk.ts` (22372) and `orgchart.ts`
  (32200) — **verified by byte-count comparison; exactly those five, offsets confirmed.** GNU grep
  classifies them as binary and suppresses all output, *reporting no matches* — which reads as
  "cleared". Demonstrated: `grep -rn "useSessionTokens" web/src` returns five hits, all importers;
  the **definition** at `useEvents.ts:216` does not appear, and `grep -a` finds it. The three
  offsets under git's 8000-byte threshold additionally render as `Binary files differ` with a stat
  line reading **0 insertions, 0 deletions** — so a PR touching the worker editor form, the events
  hook or the staged feed shows a reviewer nothing at all. One file was already known (the work
  plan's F2 entry, "normalise someday"); that entry understated it — the problem is not ugly diffs,
  it is that search silently lies.
  *Second-order risk:* any tool that round-trips these files as text (a formatter, `sed -i`, a
  paste through a terminal) will likely drop the NUL, silently turning `` `${type}\0${status}` ``
  into `` `${type}${status}` `` and making previously-distinct keys collide — with nothing failing
  at that moment either.
  **Fix:** replace the five literal bytes with `\0` escapes — behaviour-identical, and the files
  become text again. Add a CI guard failing the build if any tracked source file contains a raw NUL.
  *Caution while fixing: this is precisely the kind of edit whose diff is invisible — verify by
  byte count, not by eye.*
- [ ] **RD26 — A dropped SSE stream reports the turn as finished, so a truncated answer reads as a
  complete one.** `checkSessionStatus` returns `null` on *any* failure — network error, timeout,
  non-2xx — with an empty catch (`web/src/useAgentSession.ts:460-471`), so "the probe failed" and
  "there is no active query" are the same value. A `null` status falls through to a branch that
  only `console.log`s (`:534-547`), and the "Connection lost" error is gated behind
  `reconnectDepth >= MAX_RECONNECT_ATTEMPTS` (`:604-607`), never reached at depth 0. The UI then
  clears the spinner and calls `stopStuckDetection()` unconditionally (`:672-676`) — switching off
  the one remaining signal exactly when it would have fired. Meanwhile the agent is still running
  in its container producing output nobody will see.
  **Fix:** make `checkSessionStatus` distinguish unreachable from idle, and set the connection-lost
  error on any stream ending without `query_complete` that cannot be positively confirmed complete.
- [ ] **RD27 — When the attention route fails, the Desk says "Nothing is waiting on you" and the
  badge reads 0, with no error shown.** `useAttentionRequests.ts:81-85` sets an empty list on
  failure but leaves `available` true (only 404/501 count as "unwired"), and `attention.error` is
  **absent from the error chain** in `useDesk.ts:250` and `useAsksCount.ts:77`. Because `available`
  stayed true, the designed fallback — showing parked deliveries without their messages
  (`useDesk.ts:217-219`) — is skipped in exactly the case it was written for. An operator with
  three workers parked at `awaiting_human` sees a zero badge and a reassuring sentence, and closes
  the tab; the approvals sit until they time out.
  **Fix:** add `attention.error` to both chains, and drive the fallback off "did this load succeed"
  rather than off `available`.
- [ ] **RD28 — A failed load renders the first-run onboarding screen over an established project.**
  `useWorkers.ts:56-58` leaves the initial `[]` on failure, and `DeskPage.tsx:113` computes
  `firstRun = !loading && workerCount === 0`, replacing the entire Desk (`:137`). Same at
  `WorkersPage.tsx:123` and `WorkerList.tsx:59` (which has no error path at all). An operator with
  a dozen workers and live jobs is invited to "start from a topology". An error banner is present,
  but it carries raw server text while the panel carries a confident product sentence — users read
  the sentence. **The correct pattern is already in-repo**: `TopologyOnboarding.tsx:186` gates its
  empty state on `error === null`.
  **Fix:** gate the three first-run/empty states the same way.
- [ ] **RD29 — Wire-shape drift has no guard, and the risk runs engine→browser.** No current drift:
  `ProjectSettings` (11 fields), `Worker` (13), `Subscription` (9) and `Schedule` (10) all match
  their mirrors. But the mechanism is live: `coerceProjectSettings`/`coerceWorker` build a fresh
  object from an **explicit field list**, so an unknown key is dropped on read; the body helper
  then spreads that already-lossy object; and `PutProjectSettings`/`PutWorker` are whole-object
  writes that assign every column — so **the next time any human saves any setting in the console,
  a newly-added engine field is written back as its zero value.** No test on either side
  enumerates keys, so both suites stay green throughout. (Schedules are safe: `updateSchedule`
  patches named fields over a freshly-read row — the server-side shape the other two routes should
  follow.)
  **Fix, in the `token_usage.go` discipline — one captured artefact, two readers:** a committed
  `web/src/wire-shapes.json` of sorted key lists per struct; a Go test that marshals each
  zero-valued struct and fails if its key set differs; a vitest that fails if
  `Object.keys(coerceX(fullFixture))` differs. Adding a field on either side then fails the other
  side's test. The Go half alone catches the direction the risk actually runs.

**Folded into RD2:** the browser inherits the same token undercount — `readUsage`
(`web/src/events.ts:595-604`) reads only the two uncached fields and `sumTokens` (`:625-645`)
returns on the first usage-bearing object without descending, so cache fields would be ignored even
once the harness sends them. A job that burned ~400k tokens can display as ~12k, and an operator
sizing `daily_tokens_hard` from that number sets a ceiling an order of magnitude too low. **RD2's
fix must land in the browser in the same change.**

**Cleared by the browser sweep:** no optimistic writes anywhere — every save mutates local state
only after the await resolves and returns null/false on failure, leaving the editor dirty with a
banner; the `frozen` field is genuinely closed end to end; `configApi.request` surfaces the
server's own body text rather than "HTTP 500"; `useEventsOverview` uses `Promise.allSettled` so one
dead route cannot blank the others *and* reports the failure; `useConfigLog` correctly distinguishes
"not wired" from "failed" (the distinction RD27 and RD28 are missing, already implemented there);
topology apply is gated behind a preview with its error rendered; and polling is opt-in, single-
reload, and cannot restart its own timer.

### From the embeddable-singleton thread (2026-08-06)

*Filed here rather than in that thread's plan because it is a production-readiness defect that
predates embeddability and outlives it. Discovered by the parallel session while building the
project-API-key seam; owned by this workstream.*

- [ ] **RD30 — A container's per-session token is already a full-project credential.**
  `AGENTKIT_JWT_SECRET` and the "session secret" are the **same value in every real deployment**
  (`go/cmd/agentd/main.go:129-131`), despite the comment there claiming they are distinct. So the
  token handed to a session container — a token the model's own harness can read — verifies as a
  project-scoped JWT against every route the middleware protects. **This is the injection boundary's
  blast radius**: §6.2.4's boundary stops event text from redirecting a worker's *reasoning*, but a
  worker that is talked into reading its own environment holds a credential for the whole project's
  workers, settings, schedules and events. Nothing detects the use, because the token is valid.
  It becomes materially worse the moment a second credential kind exists (project API keys landed
  2026-08-06; embed tokens are pending), since the same secret then signs credentials with
  deliberately different reach.
  **Fix:** separate signing keys for the two credential classes, with the session key unable to
  mint or verify a project credential. **The fix changes deployment configuration** (a new secret
  to set and roll) — state the migration path and a compatible default; do not ship a breaking boot
  requirement without Kai's nod.
  *Validation:* go suite + live-Postgres, including a test pinning that a session token is
  **rejected** by the project routes — proven non-vacuous by reverting.

## 6. The durability table

*From the durability audit, 2026-07-29. What survives, what deletes it, and whether it comes back.
The repo had no such table anywhere; readiness bar #2 requires one.*

| Artifact | Where it lives | What deletes it | Reversible? |
|---|---|---|---|
| **Conversation / transcript** | `agent_query_events.events` (jsonb), one row per query — written once per turn, at `query_complete` | Session delete, via FK cascade (`migrations.go:139`). Nothing else. | **No** — no soft delete, no export, no tombstone (RD5) |
| **A turn in progress** | The sandbox's in-RAM replay buffer, and agentd's in-process `collected` slice | Process death, container archive, or `query_complete` itself (the buffer is dropped immediately after) | **No** — never written to Postgres before `query_complete`; a reconnecting browser can see it, but nothing persists it (RD6) |
| **The harness's own stdout log** | Docker's json-log for the session container | `ContainerRemove` — i.e. **ordinary idle archiving**, not just a crash | **No.** The calibration run's fallback record was thinner than it looked |
| **Messages** (legacy projection) | `agent_messages` | Same cascade (`migrations.go:77`) | **No** |
| **Job / delivery history** | `event_deliveries` | Nothing — no FK, no reaper, no retention | Survives forever, including after its session is gone (RD15) |
| **Project event log** | `project_events` | Nothing | Grows unbounded |
| **Worker config** | `workers` | `worker_delete`, which logs the full final row | **Yes** — replayable from the config log |
| **Memory** | `memories` | Nothing; store surface is Create/Get/Newest/Search only, pinned by `TestMemoriesStoreIsAppendOnly` | N/A; grows unbounded, with a `vector(1536)` column |
| **Config log** | `config_events` | Nothing through the store; only `emitted_at` is updated | Append-only **by convention only** (RD13) |
| **Session snapshot** | Blob store; pointer in `agent_sessions.snapshot_handle` | Nothing deletes the bytes; session delete drops the pointer only | Bytes orphaned and unreachable (RD14) |
| **Named catalogue image** | `agent_custom_images` + registry bytes | TTL reaper deletes bytes, tombstones the row | **No** — deliberate, but see RD9 |
| **Artifact metadata** | `agent_artifacts` | Session delete cascade (`migrations.go:50`) | **No** |
| **Artifact bytes** | Blob store | **Nothing** — `artifacts.ArtifactStore` has no Delete method | Orphaned when the row cascades away |

Two asymmetries worth stating plainly: deleting a session destroys the *index* to a user's files
while leaving the *bytes* on the bill forever; and job history outlives the transcript it links to,
so "this ran" survives without "what it said".

**What is safe** (established, not assumed): archive and idle-timeout do **not** lose the
transcript — it lives in Postgres, not the container. Port leases are re-adopted on restart
(`dind.go:432-484`), and pending deliveries are durable and re-drained (`router.go:357-368`).

**Cleared by the same audit** (recorded so the coverage is legible): whole-object PUT drift
between Go structs and their browser mirrors (none today; `workerBody` sends `frozen` explicitly);
MCP management tools' partial-update semantics; `backends.go` and `gc.go` (both refuse at boot
rather than falling back silently); `archiveIdleOnce` (a failed snapshot keeps the container);
attention request/resolution paths; and interactive chat sessions, which cannot write config
events at all because core MCP is wired only into the dispatcher. Two degradations judged
defensible but worth knowing: the budget gate's documented fail-open, and `BuildBriefingSections`,
where a worker can run with none of its memory context and still record `ok`.
