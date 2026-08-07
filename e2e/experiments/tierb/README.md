# Tier B — the graded instrument

*Built 2026-07-28 (B1 of [`docs/product/13-work-plan-self-improvement.md`](../../../docs/product/13-work-plan-self-improvement.md)).
Protocol: [`docs/AGENTS_RESEARCH.md`](../../../docs/AGENTS_RESEARCH.md) §7.
Status: **harness built and offline-proven. Execution against a real model is GATED.***

## What this is

Agent Orange has two tiers of test, and they have different failure semantics
(AGENTS_RESEARCH §7):

- **Tier A — the deterministic gate.** `e2e/features/learning-stories.stack.spec.ts`
  against the scripted mock model. Binary, fast, gates merges. It proves the
  machinery *transmits* an improvement; it can never prove the system
  *discovers* one, because the improvement is authored into the script.
- **Tier B — the graded instrument. This directory.** The same stories run
  against a real model, with a *second* model grading the outputs. The result
  is a number with variance: a curve, not a verdict.

Tier B measures one thing: **do a worker's outputs get better across rounds,
as judged blind by a model that is not the one under test?**

## The rule that outranks every other rule here

> **Never a CI gate.**

A gate with variance flakes, and a flaky gate gets disabled within a fortnight
(§7). Tier B is run on demand or nightly and recorded as a curve. When the
curve does something surprising, Tier A is the debugger — not the other way
round. Nothing in this directory emits a pass/fail; `score.test.ts` asserts
that the report carries no verdict field, so nobody can add one by accident.

## The §7 rules, and where each one lives

| §7 rule | Implementation |
| --- | --- |
| **1. Blind and shuffled** — strip provenance, randomise order | `GradingBatch` in `grade.ts` contains only `{batchId, task, criterion, items:[{label, text}]}`. Round, prompt version, worker, arm and candidate id live in `Candidate.meta` and in the harness-side `PreparedBatch.key`, never in the payload. Order is a seeded Fisher-Yates. `grade.test.ts` greps every serialised batch — and the real grader's rendered prompt — for every provenance string in the fixture. |
| **2. Rank, don't score** | The grader seam's return type is `{batchId, order: string[]}` — an ordering of presented labels. There is nowhere to put a 0–10 score. `resolveRanking` refuses anything that is not a strict permutation of the presented labels. |
| **3. Fixed anchors in every batch** | `buildBatches` appends *all* configured anchors to *every* batch, and `score.ts` counts only comparisons against anchors. That is what puts separate batches — and separate runs — on one scale. At least two anchors are required (one anchor has nothing to be measured against, so the scale would have no reference of its own). |
| **4. The grader is not the model under test** | `anthropicGrader({model})` is configurable and defaults to `claude-opus-5`. The code cannot tell which model produced the candidates, so this is an operator obligation: **the run record must state both model ids.** A same-model grade is weaker evidence (documented self-preference) — say so in the record rather than quietly running it. |
| **5. One story set, two harnesses** | Tier B grades outputs collected from the same seeded projects Tier A drives. `collect.ts`'s `roundOutputsFromEvents` reads ordinary `worker.finished` project events, so no scenario is maintained twice. |

## Files

| File | What |
| --- | --- |
| `rng.ts` | splitmix64 + FNV-1a seeding + Fisher-Yates. Own implementation, golden-value tested — determinism given a seed is an asserted property here, and a built-in generator can change between runtimes and silently turn those assertions into noise. Same decision, same reason, as `hypolab` on the Go side (13-work-plan §L1). |
| `collect.ts` | Round outputs → a candidates file `[{id, text, meta}]`. `meta` (round, prompt version, worker, project, arm, source event) is the provenance channel and is separated from `text` by construction. |
| `grade.ts` | Batching, blinding, shuffling; the `GraderSeam` type; `scriptedGrader` (table-driven, offline) and `anthropicGrader` (real API via `fetch`, defensively parsed). |
| `score.ts` | Rankings → anchor-relative win rates → per-round curve `{round → score, spread}` as JSON plus `formatCurve`'s table. The math is documented in full at the top of the file. |
| `run.ts` | Thin CLI: candidates file + config file → curve JSON + printed table. |
| `fixtures.ts` | The offline fixture, hand-computable end to end. |
| `*.test.ts` | Pure offline tests. No stack, no network, no credentials. |

## Running the offline tests

```sh
node --experimental-strip-types --test e2e/experiments/tierb/*.test.ts
```

Node ≥ 22.6 (checked on 22.14). No install step, no `node_modules`, no stack —
the whole point is that the instrument can be exercised without the thing it
measures. Node strips types but does not check them; `e2e/` has no `typecheck`
script today, so `tsc --noEmit` here would need `npm install` in `e2e/` and a
tsconfig with `allowImportingTsExtensions` (the `.ts` import extensions are
required by node's type stripping).

## The scoring, in one paragraph

For each batch, expand the ranking into pairwise outcomes and keep only the
comparisons **against anchors**. A candidate's score is `wins / comparisons`
against the fixed anchors; an anchor's score is the same against the *other*
anchors. Per round, pool the members' wins and comparisons and report the
population standard deviation of member scores as `spread`. An Elo-style
restatement `400·log10(p/(1−p))` (with a Laplace-corrected `p` so a clean
sweep stays finite) is reported alongside. **Read `spread` before reading the
trend** — a rising line with overlapping spreads is not a result.

**Anchor invariance is the pinned property.** With a fixed anchor set and an
honest grader, anchor *scores* are constants: they do not depend on the
candidates, the seed, or the run. Before comparing two runs' curves, compare
their anchor scores — if those moved, the instrument moved, not the loop. Note
the caveat pinned by test: `elo` shrinks with comparison count, so it is only
comparable between runs with equal comparison counts. `score` is the scale
check.

**What is seeded and what is not.** Batch *membership* is a deterministic
round-robin partition of the candidates in file order (which interleaves
rounds without consulting the rng). Only *presentation order within a batch*
is seeded. That split is what makes an order-independent grader's scores
seed-invariant — and therefore what makes a score that *does* move between
seeds a positive detection of order bias, rather than partitioning noise.
`score.test.ts` runs both halves of that: same fixture + two seeds + an honest
grader → different presented orders, identical scores; same fixture + two
seeds + a rigged order-biased grader → the scores disagree with themselves.

## How a real run will work (GATED — do not run without Kai's go)

The gate is L3's (13-work-plan §Wave 4): **no real-model run without Kai's
explicit go, and the credential mode is his call** — api-key spend versus the
subscription-OAuth terms caveat in AGENTS_RESEARCH §1, which restricts
subscription OAuth for unattended automation. Everything below is the
procedure, not permission.

1. **Stack up in a real-model mode.** `./e2e/run-stack-e2e.sh up api-key`
   (recommended: bounded, budgetable, terms-clean), or the ordinary compose
   stack with `ANTHROPIC_API_KEY` set. Set `daily_tokens_hard` on the project
   before starting.
2. **Drive the rounds.** Reuse the Tier A stories, driven by emitted events via
   `POST /agent/events` — never the wall clock (AGENTS_RESEARCH §7, Simulated
   time).
3. **Collect.** Pull the project events with `e2e/helpers/api.ts`, then
   `roundOutputsFromEvents(events, {worker, textOf})` → `collectCandidates` →
   `writeCandidatesFile`. **Supply a `textOf`**: `worker.finished` event text
   is the actor's *entire transcript*, which carries worker names and tool
   calls straight past the blinding. `textOf` is where the deliverable gets
   sliced out; a run that skips it is grading transcripts, not outputs.
4. **Freeze the anchors.** Two or three unchanging outputs, committed to the
   run record and reused verbatim across every run in the series. Changing an
   anchor starts a new scale — old and new curves are not comparable.
   Per AGENTS_RESEARCH §4, they live in the harness/repo, **not** in project
   memory or config, where the loop under test could read or rewrite them.
5. **Grade.** A config file naming the seed, the anchors, and
   `{"type": "anthropic", "model": "<a different model than the actor>"}`:

   ```sh
   node --experimental-strip-types e2e/experiments/tierb/run.ts \
     --candidates run/candidates.json \
     --config     run/config.json \
     --out        run/curve.json
   ```

   `ANTHROPIC_API_KEY` is read from the environment by `anthropicGrader`.
6. **Record.** Commit the candidates file, the config (seed + anchors + grader
   model), the curve JSON and the printed table under
   `docs/product/runs/<date>-tierb/`, following the repo's dated-record
   convention. State the actor model and the grader model explicitly, and
   state the seed count — one seed is one seed, and saying so is the
   difference between a result and a shrug.

## What this harness cannot tell you

- It cannot detect judge–truth divergence on its own. Poetry is the extreme of
  verification asymmetry (§2) — a flat line means "no improvement" *or* "blind
  instrument", and Tier B cannot distinguish them. That is what
  [`14-calibration-runbook.md`](../../../docs/product/14-calibration-runbook.md)
  is for: calibrate on the hypothesis lab, where the scorer is a fact, before
  trusting a curve on an unverifiable task.
- It cannot enforce §7.4. Nothing here knows which model produced the
  candidates; only the run record does.
- It measures the actor's outputs, not the critic's edits. Weng's two axes
  (AGENTS_RESEARCH §R3 — can the critic *propose* good edits vs. can the actor
  *benefit* from them) need the swap tests in §5's metrics table, which this
  harness does not run.
