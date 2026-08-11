# 27 — Simplification inventory: what the KISS decision actually makes dead

*Written 2026-08-08, after the decision recorded in [`25-cooperative-patterns.md`](25-cooperative-patterns.md) §5
and the README's "three bots and one subscription". Nothing in this document has been deleted.
It exists so a removal is a decision with a measured blast radius rather than a sweep.*

The instruction was "get rid of all the stuff that's dead weight, this kind of dynamic event system —
we don't need it, we just need *this agent has just finished its job*."

The honest answer has three parts, and only one of them is a deletion.

---

## 1. The correction: there is no "dynamic event system" to delete

The event spine is what makes "this agent has just finished its job" reach the archivist. Router,
subscriptions and the depth floor are not the elaborate part — they *are* the mechanism the
KISS design runs on. It is 3,523 lines of engine plus 2,310 lines of test, and the archivist needs
essentially all of it: an event, a delivery, a dispatch gate.

*Amended 2026-08-08:* this originally listed **a filter** among the parts the archivist needs, and
cited the archivist's `interactive=true` filter as evidence. That filter is gone. It existed only to
stop the archivist waking on its own completion — subscription filters being equality-only, "every
`worker.finished` except my own" could not be written as one — and it cost every dispatched job's
completion as a side effect. The router now suppresses self-delivery for every subscription
(`subscriptionMatches`), so the archivist's subscription is **unfiltered** and it archives
conversations and job completions alike. The spine's requirement shrank by one part; filters remain
in the engine for subscriptions that genuinely want them.

What was elaborate was never built. The ambition — `event_emit`, `event_list`, `delivery_list`, a
worker able to name arbitrary signals and poll its own spine — was **recommended in
[`26-work-plan-cooperative-tests.md`](26-work-plan-cooperative-tests.md) G1/G2 and is now
withdrawn**. That recommendation was correct for the pattern space in doc 25 and wrong for this
product. Nothing to remove; something to *not add*.

**Recommendation: keep the spine exactly as it is, and hold the line at one subscription per
project.** If we ever find ourselves writing a cron job that polls memory to fake a signal, that is
the moment to reopen G1 — not before.

---

## 2. Genuinely dead, zero blast radius

| What | Size | Evidence it is dead |
| --- | --- | --- |
| `mock-server/` | 418 lines | The model server for the legacy Vite e2e rig, which was deleted 2026-08-08. Nothing in CI references it. The stack's mock mode is `go/mockmodel` + `go/modelproxy`. The only two references outside itself are **comments** in `e2e/playwright.stack.config.ts` and `docker-compose.stack-e2e.yml`. Already flagged Orphaned in `CLAUDE.md`. |
| the `mock-server:` service in `docker-compose.test.yml` | 15 lines | Same rig. **The file itself must stay** — a first pass through this inventory proposed deleting it and was wrong: `go/systemtest/` needs its `dind` + `registry` services and skips without them (`go/systemtest/system_test.go:475`). Only the third service block is dead. |

**Recommendation: delete the `mock-server/` package and that one service block.** This is the only
unambiguous removal in the repo, and it is small.

*(Applied 2026-08-08.)*

---

## 3. Seeds the KISS design makes redundant — the real decision

15 seeds ship in `go/topology/` (6,854 lines including tests). Nothing outside that package
references any seed by name, and the console lists them dynamically from `/agent/topologies`, so
removing one is a self-contained delete with no UI or e2e fallout.

They are not all the same kind of thing, and the KISS decision does not touch them equally.

### 3a. Keep — the KISS design uses these

| Seed | src+test | Why |
| --- | --- | --- |
| `architect-archivist` | 465 | The recommended shape itself. |
| `solo` | 300 | The control that answers "did any of this beat one good agent?" — the single most important comparison in the repo, and the one the research says usually wins. |
| `solo-memory` | 375 | Separates "the second agent helped" from "persistence helped". Under a memory-centric design this is now the *primary* baseline, not a footnote. |
| `escalation` | 246 | `request_human_attention` + `awaiting_human`. The practical shape for real work, and §8.8's marketing manager. |

**The memory schema is three legs, not one prompt.** This document and the README both said "the
archivist's prompt **is** the memory schema". That is too narrow, and the narrow version hides the
leg that does most of the work. The three:

1. **the label registry** — a seeded memory, `name=label-registry`, briefed to both workers: the
   shared vocabulary;
2. **the architect's prompt** — which labels each role it creates reads, and which it writes. *"You
   are a news journalist; before you do anything, read from this memory and write to that one"* is an
   **architect** instruction issued when a role is designed, not an archivist one;
3. **the archivist's prompt** — what a finished piece of work becomes.

`architect-archivist@v1` already implements all three (both workers are briefed on
`name=label-registry`, and the architect prompt says to design the labels a role reads and writes).
Only the description was wrong — but that sentence is how a reader forms their model of the system,
so it is worth correcting. Leg 2 is the one that is easy to miss, because it lives inside the roles
the architect writes rather than in any single editable field.

### 3b. Keep — the measurement apparatus

`frozen-scorer` (369), `hypothesis-lab` (464), `triage-lab` (670), `sham-critic` (294) — **1,797 lines.**

These look like the most deletable things here and are the least. KISS is a claim about the
*product*, not a licence to stop measuring: without a frozen scorer and a placebo arm we cannot tell
whether the archivist policy is helping or whether we simply like it. The 2026-07-28 run is the
argument — the placebo tied the real critic exactly on rewrite count, and only a control could show
that.

**Recommendation: keep all four.** Cost is 1,797 lines that never run in production.

### 3c. Candidates for retirement — measured worse than the cheap alternative

| Seed | src+test | The case for removing it |
| --- | --- | --- |
| `debate` | 421 | Doc 25 rejected multi-agent debate outright: isolated self-correction with the same revision budget measured *better*, and debate collapses into rubber-stamping with weak critics. We would be shipping a shape the evidence says not to use. |
| `self-organizing` | 328 | Needs `worker_create` in agent hands with no human gate; the effect **reverses below a model-capability threshold**. The architect covers the useful half of this (roles created deliberately, after a human says yes) with none of the runaway risk. |

**Recommendation: retire both, or mark them `Deprecated` in the registry with the evidence in the
description.** Marking is cheaper and keeps the negative result visible, which is worth something.
749 lines either way.

### 3d. Keep, but demote in the docs

`supervisor` (431), `assembly-line` (406), `blackboard` (356), `temporal-hierarchy` (490),
`actor-critic` (390) — **2,073 lines.**

All five are shapes people genuinely ask for, and assembly-line won a real coordination bake-off.
They stay available; they stop being the *first* thing the topology library shows. The library's
opening line should be "start with architect-archivist; reach for these when you know why".

---

## 4. Documents to mark superseded

Neither should be deleted — they are the reasoning behind the decision, and doc 25 §6's audit of its
own sourcing is the most useful page in either.

- **`25-cooperative-patterns.md`** — add a banner: the catalogue stands; §5's "the one change that
  unlocks the most patterns is `mcp_events.go`" is superseded by §1 of this document.
- **`26-work-plan-cooperative-tests.md`** — most of its 24 tickets test patterns the KISS design does
  not use. The ones that survive are the ones already built (G3, G4, G7 — done 2026-08-08) plus
  T06–T11 and T14–T15, which pin *current* behaviour. Mark the rest **Not scheduled**, with the
  reason, rather than deleting them: the gap list is still the honest record of what this substrate
  cannot do.

---

## 5. Totals, if everything recommended is done

| Action | Lines |
| --- | --- |
| Delete `mock-server/` + `docker-compose.test.yml` | ~430 |
| Retire `debate` + `self-organizing` (or mark deprecated: 0) | 749 |
| Not-built and now not-building (`event_emit`/`event_list`/`delivery_list` + their tests) | ~0 removed, ~600 avoided |
| **Kept deliberately** — the event spine, the measurement seeds, the work topologies | ~7,400 |

The headline: **the simplification is a decision about what we build next, not a large deletion.**
Roughly 1,200 lines are genuinely removable. The reason the codebase does not shrink much is that
the KISS design runs on the same primitives the elaborate one would have — it just declines to add
the next ten.
