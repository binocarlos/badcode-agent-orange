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

- [ ] **RD1 — A schedule disables itself permanently on a transient database error, and records a
  false reason.** *Worst first-user failure found: silent, permanent, and actively
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
- [ ] **RD2 — The token ledger counts only uncached input tokens, so the spend brake still
  under-reads real spend.** TOK1 fixed the jsonb *path*; it did not fix *which fields* are
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
- [ ] **RD4 — A worker's config change can be recorded as a human edit.**
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

**Meta-finding, promoted to §2's standing rule:** RD2's survival has the same root cause as TOK1's
— a fixture authored to match the reader rather than captured from the writer. Two of the four
findings are token-metering defects in a system whose only spend brake is token metering.

**Cleared by the same audit** (recorded so the coverage is legible): whole-object PUT drift
between Go structs and their browser mirrors (none today; `workerBody` sends `frozen` explicitly);
MCP management tools' partial-update semantics; `backends.go` and `gc.go` (both refuse at boot
rather than falling back silently); `archiveIdleOnce` (a failed snapshot keeps the container);
attention request/resolution paths; and interactive chat sessions, which cannot write config
events at all because core MCP is wired only into the dispatcher. Two degradations judged
defensible but worth knowing: the budget gate's documented fail-open, and `BuildBriefingSections`,
where a worker can run with none of its memory context and still record `ok`.
