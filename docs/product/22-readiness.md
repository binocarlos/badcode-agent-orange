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
rule against pre-ticked, pre-invented items applies to this file too.*
