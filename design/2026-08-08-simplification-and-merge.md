# Simplification, self-delivery, and merging the two threads

*Written 2026-08-08. Hand-off document: everything a session needs to finish this work without
having read either originating thread.*

Two sessions have been working on this repo in parallel.

- **Thread A (CI/merge)** got the suite genuinely green: merged `readiness` into `product-layer`,
  landed PR #1, fixed three CI-only web flakes, built `./stack`, added OpenAI embeddings, unified
  chat-session capability, made CI actually run the live-Postgres and stack-e2e suites that had been
  silently skipping, and cut e2e from 20.2 → 7.9 min. Its last commit is **`3ed3096`**. **PR #2 is
  open from `product-layer` and was green on `3ed3096`.**
- **Thread B (research/product)** ran a 26-agent research swarm over cooperative agent patterns,
  judged them against this code, and turned the result into a much simpler product direction plus
  four engine changes. Its five commits, `9d29a3d`…`13c5686`, now sit **on top of `3ed3096` on the
  same branch**.

This document is the merge point. §1 is what landed; §2 is what CI needs to know; §3–§7 are the
five remaining work items, specified to be executable cold.

---

## 1. What Thread B landed (already committed, tests green)

| Commit | What |
| --- | --- |
| `9d29a3d` | `worker.finished` carries `[tool] name(args) → outcome` lines, and a chat emits one when it goes **idle** instead of once per turn. |
| `c609277` | `retracts=<id>` is honoured at memory read time. Migration **043**. |
| `f5d25f9` | `architect-archivist@v1` — the 15th topology seed, and the recommended starting shape. |
| `c5c1569` | `mock-server/` deleted, plus its `docker-compose.test.yml` service block. |
| `13c5686` | `docs/product/25`, `26`, `27` (research catalogue, test plan, simplification decision) and a rewritten `README.md`. |

Verified before commit: `go build`, `go vet`, `go test ./...` (31 packages); memory changes against a
**live pgvector Postgres** (the skipping suite proves nothing there); `web/` typecheck + 1288 tests;
`sandbox/` typecheck + 162 tests. **Not run: the stack e2e** — that is §6.

### The product decision, in one paragraph

Agent Orange is not a framework for splitting one task across agents — a single good session with
subagents beats that, and the harness is already the Claude Agent SDK, so we get it free. It is a
runtime for an **organisation that outlives any conversation**. Coordination therefore happens
through **labelled shared memory and a clock**, not through a general event mesh. The recommended
project shape is two workers and one subscription: an **architect** you talk to that designs and
builds the rest of the org with the management tools it already holds, and an **archivist** woken by
finished work whose prompt is the memory policy. The reasoning, with 242 sources and an audit of its
own weak citations, is `docs/product/25-cooperative-patterns.md`; the decision is
`docs/product/27-simplification-inventory.md`.

---

## 2. Two behaviour changes CI must be told about

**2a. An interactive session no longer emits `worker.finished` per turn.** It emits once, from the
archive sweep, when the session has been idle past `Policy.ArchiveTimeout`. Dispatched jobs are
unchanged (a job *is* one turn), and `TestDispatchedJobDoesNotEmitTwice` pins that.

*The coupling to watch:* **with `ArchiveTimeout` at 0 the sweep never runs and an interactive session
never emits at all.** `agentd` sets it from `AGENTKIT_SESSION_IDLE_TIMEOUT` (default 30m), so the
compose stack is fine — but any harness that builds a Runner directly with a zero Policy will see a
chat produce no event. `go/examples/standalone/main.go:149` is exactly that configuration.

*Grep already done:* no e2e spec creates a worker-attached human chat and then waits for a downstream
worker to wake. The only `interactive` references in `e2e/` are
`config-and-workers.stack.spec.ts:168` and `acceptance-loop.spec.ts:160`, both asserting
`interactive: false` on dispatched/external events. **The stack run in §6 is still the check** —
treat this paragraph as "expected clean", not "proven clean".

**2b. Transcripts are longer.** Every tool call adds one line (≤ ~420 chars). Anything asserting a
transcript byte-for-byte, or asserting a token count, may move. Nothing in the Go suite did.

---

## 3. Item 1 — Suppress self-delivery in the router *(the one that changes the product)*

> **DONE 2026-08-08.** Built as specified, plus three things this section did not anticipate:
>
> - **`web/src/events.ts` needed the same rule.** `matchSubscriptions` is a second implementation of
>   the router's matcher — the F1 note in `router.go` warns about exactly this — and without the
>   guard the console's dry run would have told an operator "this matches" where the router refuses,
>   sending them to debug a subscription behaving correctly. Mirrored, with three tests.
> - **Two existing Go tests collided**, both because a fixture reused one worker name for emitter and
>   subscriber: `TestRouterSubscriptionMatching/reason_matches_on_worker.failed` and
>   `TestRouterRefusesEventsPastTheDepthFloor`. Neither was testing self-delivery; both now use
>   distinct names so each isolates the rule it is about. No seed relied on self-delivery — every
>   other topology filters to another worker (audited).
> - **The claim that this generalises `supervisor.go`'s guard is not quite right.** That validator
>   refuses a seed *inbound* type in the `worker.*` namespace, which prevents a dispatcher being woken
>   by its SPECIALISTS — other workers. Self-delivery suppression does not cover it. Both are needed;
>   the comment in `router.go` says so.
>
> Also note the guard is on the envelope's worker, not the event type, so it covers `worker.failed`
> as well as `worker.finished`. That is wanted — self-delivery of a failure is a retry spin — and is
> pinned by test.

**Why.** A subscription filter is equality-only, so "every `worker.finished` **except** my own"
cannot be written. The archivist therefore ships with `filter: {"interactive": "true"}` — which
dodges the self-loop as a side effect, at the cost of never archiving dispatched job completions.
That is a workaround standing in for a rule.

The rule the codebase already wants: **a worker is never woken by its own completion.** Evidence it
is already wanted — `go/topology/supervisor.go:213` hand-rolls a validator refusing an event type in
the `worker.*` namespace, with the comment *"a dispatcher woken by `worker.finished` would react to
its own specialists"*. One router rule generalises that guard to every subscription anyone ever
writes.

Being woken by your own completion is a spin with no use case: schedules already express "keep
going", and the depth-8 floor is the only thing currently stopping it.

**The change.** `go/cmd/agentd/router.go`, in `subscriptionMatches` (~line 413) — the single gate
every delivery passes through:

```go
// A worker is never woken by its own completion. Filters are equality-only, so
// "everything except me" cannot be expressed as a filter; it is a property of
// the spine instead. Generalises the guard supervisor.go:213 hand-rolls.
if ev.Envelope.Worker != "" && ev.Envelope.Worker == sub.Worker {
    return false
}
```

**Then simplify the seed.** In `go/topology/architectarchivist.go`, drop
`Filter: agentdb.JSONMap{"interactive": "true"}` so the subscription is unfiltered, and rewrite the
two comment blocks (the header's "TWO HONEST LIMITS" item 1, and the inline note at the
`Subscription` literal) — the loop is now prevented by the router, and the archivist archives
**conversations and job completions alike**.

**Tests.**
- New, in `go/cmd/agentd/router_test.go`: an event whose `envelope.worker` is `X` produces **no**
  delivery for a subscription whose `worker` is `X`, and still produces one for a subscription whose
  worker is `Y`. Table-driven, beside the existing `subscriptionMatches` cases.
- New: an event with an empty `envelope.worker` (external, `source: "external"`) still delivers to
  every matching subscription — the guard must not swallow external events.
- Rewrite `TestArchivistSubscriptionCannotWakeItself` in
  `go/topology/architectarchivist_test.go`: it currently asserts the filter exists and is the string
  `"true"`. It should now assert the subscription is **unfiltered**, and the loop-safety assertion
  moves to the router test above. Keep the `MaxFiringsPerHour == 0` assertion and its reason.
- `TestArchitectArchivistRenderDefaults` asserts `len(b.Subscriptions) == 1`; unchanged.

**Docs to follow.** `README.md` (the ASCII diagram's caption says "when a conversation goes quiet" —
it now also covers jobs), and `docs/product/27-simplification-inventory.md` §1, which currently cites
the filter as the reason the archivist looked like a special case.

**Size.** ~3 lines of engine, ~80 of test, ~20 of prose.

---

## 4. Item 2 — Correct the memory-schema framing *(three legs, not one)*

> **DONE 2026-08-08.** `README.md` (the archivist paragraph, the ASCII caption, the "Shared state"
> row, and the diagram's "writes what each conversation was worth" → "each finished piece of work"),
> `go/topology/architectarchivist.go` (header comment), `docs/product/27-simplification-inventory.md`
> §3a. No code or test changes, as predicted.

**Why.** The README and the seed say "the archivist's prompt **is** the memory schema". That is too
narrow, and the correction is Kai's: the architect also decides, for every worker it creates, which
labels that worker reads and which it writes — *"you are a news journalist; before you do anything,
read from this memory and write to that memory"* is an **architect** instruction, not an archivist
one. The memory system is **three legs**:

1. **the label registry** — a seeded memory (`name=label-registry`), the shared vocabulary;
2. **the architect prompt** — who reads what, who writes what, per role it creates;
3. **the archivist prompt** — what a finished conversation *becomes*.

The seed already implements all three (both workers are briefed on `name=label-registry`, and the
architect prompt says "when you design a role, design the labels it reads and the labels it
writes"). **Only the wording is wrong**, and that sentence is how a reader will understand the
system.

**Files.** `README.md` (the paragraph beginning "**The archivist** is woken every time…", and the
"Shared state" table row); `go/topology/architectarchivist.go` (the header comment's ARCHIVIST
paragraph); `docs/product/27-simplification-inventory.md` §3a. No code, no test changes.

---

## 5. Item 3 — Push, and decide what PR #2 is

**The situation.** PR #2 is open from `product-layer` and was green on `3ed3096`. Thread B's five
commits are now on that branch, so **PR #2's green run is stale**: pushing re-runs all seven jobs,
including the ~25-minute stack e2e.

Two options, and it is Kai's call:

- **(a) Merge PR #2 at `3ed3096` first, then push Thread B's commits as PR #3.** Un-reds `main`
  immediately with a run that is already green; Thread B's behaviour changes get their own review
  and their own CI run. **Recommended.**
- **(b) Push onto `product-layer` and let PR #2 grow.** One PR, one merge, but the green run is
  discarded and `main` stays red until the new run passes.

Under (a): `git push origin product-layer` **must not happen before** PR #2 merges, or option (a)
disappears.

---

## 6. Item 4 — Run the stack e2e

The only gate Thread B did not run, and the only one that proves a composed prompt reaches a model.

```sh
./e2e/run-stack-e2e.sh clean     # clears leftover sessions and restarts agentd
./e2e/run-stack-e2e.sh up mock
./e2e/run-stack-e2e.sh test
```

Watch specifically for §2a — anything that drives a worker-attached chat and expects a follow-on.
`e2e/features/learning-stories.stack.spec.ts` and `e2e/features/acceptance-loop.spec.ts` are the two
most likely to notice, because they settle rounds by waiting for follow-on deliveries.

The stack serves a **built** image of `examples/web`, so any UI change needs
`docker compose up -d --build web` before browser tests mean anything.

---

## 7. Item 5 — Seed the real project

`docs/product/17-product-spec.md` §8.8's BadCode marketing manager. Deliberately undone since July,
because it is a production act rather than a verification — and it is now the cheapest it will ever
be to attempt, because `architect-archivist@v1` exists and the archivist gives the project a memory
without anyone having to design one first.

Shape: instantiate `architect-archivist@v1` with the goal, talk to the architect, let it propose a
roster, approve it, and let it build. Then watch the config log. **This is Kai's to run, not an
agent's** — it spends real tokens against a real account and creates real outward-facing work.

---

## 8. Verification gates

Nothing here is done until all of these are green:

```sh
cd go && go build ./... && go vet ./... && go test ./...          # 31 packages
AGENTKIT_TEST_POSTGRES_URL=postgres://… go test ./agentdb/        # throwaway DB, not a shared one
cd web && npm ci && npx tsc --noEmit && npx vitest run            # 1288 tests
cd sandbox && npm ci && npx vitest run                            # 162 tests; then: git checkout sandbox/yarn.lock
./e2e/run-stack-e2e.sh test                                       # ~8 min on 4 cores
```

One pre-existing failure is **not** Thread B's and should not be chased as a regression:
`TestLivePG_QueryEventsMixedPreAndPostMigrationRows` in `go/agentdb` fails identically with Thread
B's changes stashed (verified 2026-08-08), and passed on a freshly-migrated database — it looks
state-dependent.

---

## 9. What is deliberately NOT being done

- **`event_emit` / `event_list` / `delivery_list`** (docs 26 G1/G2). Withdrawn. They unlock patterns
  the simplified design does not use. If we ever find ourselves writing a cron job that polls memory
  to fake a signal, that is the moment to reopen it — not before.
- **Deleting the event spine.** There is nothing there to delete; it is what the simple design runs
  on. See doc 27 §1.
- **Retiring `debate` and `self-organizing`** (749 lines, both measured worse than the cheap
  alternative). Still open; marking them deprecated in the registry is cheaper than deleting and
  keeps the negative result visible.
