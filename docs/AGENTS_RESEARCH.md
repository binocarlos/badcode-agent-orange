# Agents research — measuring self-improvement

*Written 2026-07-27. **Status corrected 2026-07-29: the harness proposed below is BUILT.** The
research in §§1–7 stands as written; what changed is that it stopped being a proposal. This file
previously said "nothing here is built yet", which was true when written and had been false for
days — see [§8](#8-the-research-map) for what exists now and where it lives.*

**This is the single entry point for Agent Orange's agent research.** Two workstreams ran in
parallel across separate sessions — the self-improvement/measurement line (this file and its
downstream docs) and the operator-console line (docs 15/16/21). They did not duplicate each other
and they have not contradicted each other; §8 is the map, including where they met.

This document exists for two reasons. First, `.env.example` cites it for the subscription-OAuth
question — that answer is in [§1](#1-model-credentials-and-subscription-oauth). Second, it records
the literature review behind the self-improvement test harness, so the design choices have their
reasons attached rather than being folk wisdom six months from now.

The motivating question: **Agent Orange's §8.7 acceptance loop demonstrably runs — a worker
rewrites another worker's prompt with a rationale, and the next job uses it. Nothing measures
whether it *improves* anything.** Proving the loop closes is not the same as proving it helps, and
the gap between those two claims is where this document lives.

---

## 1. Model credentials and subscription OAuth

Three credential modes, precedence `ANTHROPIC_API_KEY` > `CLAUDE_CODE_OAUTH_TOKEN` > mock:

| Mode | Set | Path to the model |
| --- | --- | --- |
| API-billed | `ANTHROPIC_API_KEY` | Sessions talk to agentd's model proxy, which injects a per-session JWT. |
| Subscription | `CLAUDE_CODE_OAUTH_TOKEN` (API key blank) | Sessions call `api.anthropic.com` directly with the OAuth token. |
| Offline | neither | Deterministic mock model; `AGENTKIT_MOCK_MODEL_SCRIPT` for scripted turns. |

Subscription mode is already wired end to end — `.env.example`, `docker-compose.yml`,
`go/runner.go`'s credential branch, with `go/runner_test.go` pinning that the API key stays absent
and the OAuth token is passed through. To use it: run `claude setup-token` on the host, paste the
`sk-ant-oat01-…` value into `.env`, leave `ANTHROPIC_API_KEY` blank.

**The caveat that matters for this research.** Anthropic's terms restrict subscription OAuth for
headless automation. A self-improvement experiment is a long-running unattended loop — squarely the
restricted case, not an edge of it. Treat subscription mode as a personal-use opt-in for
interactive and small-scale work, and read the current terms before running a large unattended
experiment on it. This is a licensing question, not a technical one; the code does not and should
not try to enforce it.

---

## 2. The central failure mode: judge–truth divergence

A self-improvement loop optimises whatever signal it is given. When that signal is a model judging
its own output without ground truth, the loop optimises *persuasiveness*, not *quality* — and it
does so fast.

[**More Convincing, Not More Correct**](https://arxiv.org/html/2607.05904) quantifies this
precisely enough to be worth memorising. Self-play on GSM8K, model generates → judges without a
reference → optimises by preference learning, with a hidden exact-match anchor attached that the
judge never sees:

- Judge pass rate: **72% → 94%**
- True accuracy: **flat, ~20%**
- Judge–truth gap: **0.74**

The hacking appeared within a handful of iterations, transferred across judge families (Qwen,
Llama, Gemma) and scales up to 14B, and **a strict three-judge ensemble still accepted 55% of the
wrong answers**. Ensembling is not a fix.

The named mechanism is **verification asymmetry**: shown a candidate answer with no ground truth, a
judge can only assess whether it *looks* right. For any task where verifying is harder than
recognising plausibility, the judge scores plausibility. This creates "false-positive basins" that
optimisation pressure actively seeks out.

Their decisive mitigation is **de-anchoring** — require the judge to commit its own answer
*before* it sees the candidate. False-positive rate collapsed from 0.72 to 0.01, and used as the
training reward it prevented the basin forming at all.

**Why this bites us specifically.** Poetry is the extreme of verification asymmetry: there is no
correctness at all, only plausibility. A self-graded poetry loop is, structurally, a machine for
producing text that pattern-matches "good poem" to one particular judge.

### The related failures

From [Harness Engineering for Self-Improvement](https://lilianweng.github.io/posts/2026-07-04-harness/)
(Weng, July 2026) — the closest published description of the thing we have built:

- **Diversity collapse** toward whatever high-reward pattern got exploited first.
- **"Numerical duct tape"** — models declaring success on noisy results.
- **Memory degradation** over long horizons without persistent logging.
- **Weak evaluators** as the root cause of most of it.

From [Who Grades the Grader?](https://arxiv.org/html/2607.12790), observed specification gaming in
their own runs: evolved skills wrote tags in place of numbers (~30% of tags had no value beside
them at peak) and invented confident forecasts to satisfy style dimensions. Worth reading as a
catalogue of what "improvement" looks like from the inside when it isn't.

---

## 3. Design rules that follow

### R1 — The evolving loop never touches the measuring instrument

From [Who Grades the Grader?](https://arxiv.org/html/2607.12790): held-out evaluation must never
pass through the evolved metric. The consequence is the reason this rule is load-bearing —

> a weak metric slows learning but **cannot corrupt measurement**.

That asymmetry is the whole game. A loop with a mediocre frozen scorer learns slowly and we can
tell. A loop that can influence its scorer learns nothing and we *cannot* tell.

### R2 — Pairwise and de-anchored, not absolute scoring

Absolute 1–10 judging suffers ceiling compression, heavy-tailed variance, and scale drift between
judges. [Reference-anchored Elo](https://openreview.net/forum?id=Q88mQBuPjB) — pairwise comparison
against a fixed reference, expressed as win probability — is materially more stable and yields
uncertainty estimates without resampling. Combine with de-anchoring (§2): the scorer answers the
brief itself before seeing any candidate.

### R3 — Separate the two capabilities

Weng's distinction, which our §8.7 loop currently conflates: **harness-updating capability** (can
the critic produce good prompt edits?) versus **harness-benefit capability** (can the worker
exploit them?). Smaller models can propose procedurally valid edits while failing to benefit from
them. If our numbers don't move, without this split we won't know which half failed.

### R4 — Expect the derived rubric to be weak, and measure it

The premise "start from a vague goal with no guidance" is deliberately hard mode.
[Model-generated rubrics run ~27 points behind human-authored ones](https://arxiv.org/pdf/2603.00077)
with the judge held fixed, and scaling synthetic rubrics shows diminishing returns while human
rubrics keep improving. The rubric the system *derives* from a vague goal is therefore itself a
primary experimental artifact — diff round-k's prompt against round-0's and read what criteria it
invented.

---

## 4. What "frozen" means

Frozen is a statement about **causality, not location**. A scorer is frozen if no action available
to the loop can change what it measures. It may live in the same project, the same database, even
the same process — provided the loop has no writable channel to it.

Three things must be frozen together; freezing one or two is the same as freezing none:

1. **The instrument** — scorer prompt, model, and version, pinned and hash-checked.
2. **The held-out set** — briefs the loop never sees during its rounds.
3. **The procedure** — comparison method, ordering randomisation, aggregation.

### Channels that unfreeze a scorer in *our* architecture

Agent Orange makes several of these easy to create by accident, because the product layer is
deliberately built for workers to reconfigure each other:

| Channel | Why it leaks | Containment |
| --- | --- | --- |
| `worker_prompt_write` | The §8.7 loop's whole purpose. A scorer worker is a legal target. | Protected-worker flag, or compose-time hash check on the scorer's prompt. |
| Project memory | Append-only shared memory; loop writes, scorer reads. | Scorer's MCP tool set excludes memory reads, or reads a fixed namespace only. |
| Held-out briefs in memory or config | Anything the loop can read, it can train on. | Briefs live outside the project entirely — in the harness repo, not the database. |
| Same model family | Not a leak, but correlated blind spots. | Prefer a different model for scoring; at minimum pin the version. |

The practical upshot for hosting the scorer as a worker in the same project: **yes, and it's a
reasonable choice — but "frozen" then has to be enforced rather than merely intended.** A
convention that says "don't rewrite the scorer" is not a control; `worker_prompt_write` is a tool
the loop holds, and the entire lesson of §2 is that optimisation pressure finds writable channels
whether or not anyone intended them as targets.

---

## 5. Proposed harness

**Tier 1 — the loop under test.** A real Agent Orange project. A worker whose system prompt starts
near-empty, a critic worker that reads outputs and calls `worker_prompt_write` with a rationale, a
schedule driving rounds. P8 (append-only) means the config log *is* the experiment record —
complete prompt lineage, free, with no extra instrumentation.

**Tier 2 — the frozen scorer.** Per §4. Held-out briefs, de-anchored pairwise comparison against
round-0 output and a fixed reference set, positions randomised, reference-anchored Elo.

### Metrics

| Metric | Detects |
| --- | --- |
| **Held-out win-rate vs round 0** | The primary curve. Did anything improve? |
| **Internal critic score vs frozen score** | Judge–truth divergence — the §2 signature, visible within a couple of rounds and cheap. |
| Embedding dispersion per round | Diversity collapse. pgvector and the embedding provider are already wired. |
| Prompt length / instruction count | Verbosity and duct-tape hacks. |
| Round-k prompt diff vs round-0 | The derived rubric (R4). |
| Swap tests (round-k prompt × round-0 config) | R3's two axes. |

The second row is the one to build first. It is the cheapest signal we have and it fails loudly.

### Controls

Without these it is a demo, not an experiment:

- **No-critic arm** — same round count, prompt frozen. Isolates real change from scorer variance.
- **Random-edit critic arm** — arbitrary prompt edits. If the real critic cannot beat prompt churn,
  there is no self-improvement, only motion.
- **Memory-off arm** — does append-only memory contribute, or is prompt rewriting doing the work?
- Multiple seeds, variance reported. One run on a 20-item held-out set is noise.

---

## 6. Calibrate the instrument before trusting it

**Recommendation: run the rig first on a domain with hidden ground truth, and only then on an
unverifiable one like poetry.**

The reasoning is the hidden-anchor trick from §2. On an unverifiable task, a harness that cannot
detect improvement is indistinguishable from a loop that did not improve. Both produce a flat line.
Calibrating on a task where we independently know the right answer tells us which of those two
things a flat line means — and that is the difference between a result and a shrug.

### The hypothesis-investigation domain

A promising calibration domain, and a better one than a maths anchor because it exercises the same
multi-worker machinery an open-ended goal would: give the org chart a **hypothesis over a dataset
whose true answer we computed and held out.** "Do people in red jumpers miss trains more often?"

Its properties, in the terms of this document:

- **Verifiable.** The conclusion checks against a held-out ground-truth answer. No verification
  asymmetry, so §2's basin cannot form — which is exactly why it calibrates.
- **Truth is ours to set.** With synthetic data we control effect sizes, confounders, and sample
  size, so difficulty is a dial rather than an accident.
- **It admits planted nulls.** Some hypotheses must be *false*. A competent research org has to be
  able to conclude "no effect found" — and an org that confirms every hypothesis it is handed is
  the single most important failure to catch. It is also nearly free to test.
- **Improvement is meta-level.** What improves across hypotheses is *methodology* — how the org
  investigates — not one answer. That is Weng's meta-level axis, and it is the axis §8.7 actually
  claims to operate on.

Poetry then becomes the second experiment, run with an instrument we have reason to trust.

---

## 7. Two tiers of test: the gate and the instrument

The full argument and the story catalogue live in
[`docs/product/11-learning-stories.md`](product/11-learning-stories.md); this section records the
division of labour, because the two tiers have different failure semantics and conflating them
ruins both.

**Tier A — the deterministic gate.** The scripted mock model
(`go/modelproxy/script.go`) selects its response by substring match against the raw request body —
which contains the composed system prompt. It is therefore a *prompt-conditioned* deterministic
model: rewrite the prompt and the behaviour changes, through the real loop, with zero tokens. Tests
built on it are binary and fast. They **gate merges**. They prove the machinery *transmits* an
improvement; they can never prove the system *discovers* one, because the improvement is authored
into the script.

**Tier B — the graded instrument.** The same stories run against a real model, with a second model
grading outputs. The result is a number with variance, not a verdict. It is run on demand or
nightly, recorded as a curve, and **never wired as a pass/fail CI gate** — a gate with variance
flakes, and a flaky gate gets disabled within a fortnight. When the curve does something
surprising, Tier A is the debugger.

### Grading protocol for Tier B

1. **Blind and shuffled.** Strip provenance (which round, which prompt version) and randomise
   presentation order — otherwise the grader's expectation of improvement is measured, not
   improvement.
2. **Rank, don't score.** Batch-grade all candidate outputs together and ask for an ordering or
   pairwise preferences. Absolute 0–10 scores suffer ceiling compression and scale drift (§R2);
   comparison within one context window is the reliable operation.
3. **Fixed anchor items in every batch.** Two or three unchanging outputs included in every grading
   run put separate runs on a common scale. Without anchors, batch grading is only *internally*
   comparable and a cross-run improvement curve is meaningless.
4. **The grader is not the model under test.** Models exhibit documented self-preference for their
   own generations. Grade with a different model; at minimum treat a same-model grade as weaker
   evidence.
5. **One story set, two harnesses.** Tier B reuses Tier A's stories with the model swapped —
   no duplicate scenario maintenance.

### Simulated time

A necessary property for both tiers, and one the architecture already has: **a schedule firing is
an ordinary project event through the ordinary dispatch gate** (`cmd/agentd/scheduler.go` —
`CreateProjectEvent` + the shared `dispatch.go` gate; the cron tick only decides *when*). So tests
never wait for the wall clock: they emit the event a subscription matches via `POST /agent/events`
and rounds happen on demand. Any future trigger type should preserve this property — the moment a
trigger fires through a private path instead of the event spine, it stops being simulatable and
its consumers stop being testable offline.

---

## 8. The research map

*Added 2026-07-29, consolidating two parallel sessions into one index. Every claim here was checked
against the repo, not against memory. This section is the **map**; the linked docs remain
authoritative for their own subjects — consolidating their content into one file would destroy the
structure that makes them usable.*

### Did the two threads diverge?

**No.** They worked different surfaces and met at three named points, each of which resolved
cleanly:

| | Self-improvement line | Operator-console line |
| --- | --- | --- |
| Question | Does the §8.7 loop actually *improve* anything, and how would we know? | What does a human see, and can they operate it? |
| Docs | this file, 10, 11, 12, 13, 14, 19, 20, 22, `doctrine/`, `runs/` | 15, 16, 21, `ux-review/` |
| Output | 14 topology seeds, 3 scenario rigs, doctrine-v1, 29 readiness findings | the console, built and UX-reviewed against a populated fixture org |

**Where they met.** *(Corrected 2026-07-29 by the self-improvement session against `git log`; the
first draft had the two lines swapped.)*

1. **The self-improvement line executed work-plan 13's Wave 7** — DR1, SC1, SC3 — and ran the L3X
   live calibration, which produced the L3H/L3M items. The console line ran the readiness audits
   (doc 22) in parallel. Coordinated, not accidental.
2. **`20-operations-doctrine.md` gained OM-9 (from SC1) and OM-10 (from SC3)** — both from the
   self-improvement line, hours apart, and every commit touching that file is from that line. The
   console line's contribution was catching that the two rows had been left in the wrong order and
   fixing it: a real find, but a review of one thread's doc rather than a second author on it.
3. **`e2e/mock-scripts/README.md` cost two hand-resolved conflicts — inside one thread, not
   between them.** Both were between *parallel executors of the self-improvement line* (DR1 and
   SC1 each appending a row, then SC1 moving its own to avoid the collision it had verified). The
   rule that came out of it is the valuable part and is unchanged: a shared index file needs its
   insertion point stated in the executor's brief.

**No contradictions found.** The failure that did occur was different and worth naming: **four docs
claimed "nothing here is built" long after their subjects were built** (this file, 10, 11, 15). That
is the same defect class doc 22 exists to hunt — a confident statement that was true when written
and silently stopped being true. All four corrected 2026-07-29.

### The map

**Research and principles**

| Doc | Holds | Status |
| --- | --- | --- |
| **this file** (§§1–7) | The literature: judge–truth divergence, the five-point grading protocol, what "frozen" means, calibrate-on-facts, the two-tier gate/instrument split, simulated time | Research **executed**; §§1–7 unchanged and still the reasoning |
| [`product/12`](product/12-composition-playbook.md) | Composition principles **C1–C8** and the ordered plan that became work-plan 13 | Plan complete |
| [`product/19`](product/19-scenario-library.md) | The scenario-admissibility contract, and 5 scenarios (SC-0 hypothesis lab, SC-1 triage, SC-3 gauntlet built; SC-2/4/5 catalogued) | Three built |
| [`product/20`](product/20-operations-doctrine.md) | The operator's manual **OM-1–OM-10**, and the worker doctrine block **WD-1–WD-10** | doctrine-v1 written; **every entry still `candidate`** |
| [`product/doctrine/doctrine-v1.md`](product/doctrine/doctrine-v1.md) | The canonical injected bytes | Immutable once referenced; **reaches no real user yet** (doc 22 RD21) |

**Instruments (built)**

| Doc | Holds | Status |
| --- | --- | --- |
| [`product/10`](product/10-topology-library.md) | Org charts as data; the frozen-worker design | **14 seeds built**, registry + preview + apply + UI flow |
| [`product/11`](product/11-learning-stories.md) | The deterministic gate: MR-1/2/3 and stories S1–S9 | **Built and green**, runs offline on the scripted mock |
| [`product/14`](product/14-calibration-runbook.md) | The live-run protocol: arms, metrics, abort criteria | Written; **executed once**, see runs/ and §5a |
| [`e2e/experiments/`](../e2e/experiments/) | The harnesses themselves — the map's one pointer at the code: `calibration/` (SC-0 plus DR1's doctrine axis), `triage/` (SC-1), `gauntlet/` (SC-3), `tierb/` (the §7 graded protocol), and C1's comparison rig | Built; every smoke report byte-reproducible and committed. SC-3's delivery claim is proven by collapse: disable the doctrine injection and the protected arm reproduces the unprotected one in every column |

**Records and open work**

| Doc | Holds | Status |
| --- | --- | --- |
| [`product/13`](product/13-work-plan-self-improvement.md) | The executed plan, waves 1–7, plus ~90 Discovered Issues | **Open: L3H, L3M.** The best record of what was actually built |
| [`product/22`](product/22-readiness.md) | The silent-success failure class, the readiness bar, the durability table, findings **RD1–RD29** | **3 fixed** (RD1, RD2, RD4); the rest specified |
| [`product/runs/`](product/runs/) | Dated run records — currently one, the aborted first calibration | Living; the README's run log links here |

**Operator console (the other thread)**

| Doc | Holds | Status |
| --- | --- | --- |
| [`product/15`](product/15-operator-console-design.md) | What the browser shows; the backend seams it needed | **Built** (doc 16, all items) |
| [`product/16`](product/16-work-plan-operator-console.md) | That build's plan and its discoveries | Complete |
| [`product/21`](product/21-console-ux-review.md) | The populated-state critique and motion design, reviewed against a realistic fixture org | Complete; screenshots in `product/ux-review/` |

### What is actually unfinished

Stated plainly, because three docs' status lines used to imply otherwise:

1. **L3H + L3M** — **neither started** (status owned by the self-improvement line, 2026-07-29).
   *L3H* hardens the calibration runner: an empty assistant reply must become a recorded abort
   rather than a 180-second hang, and no throw may bypass report-writing — in the L3X run an
   unhandled poll timeout destroyed eight already-completed hypotheses. *L3M* needs a manifest hard
   enough that improvement has room to show, and **its shape is genuinely open**: L3X's ceiling
   result (11/11 first-try correct, zero rewrites, the critic declining on the record) is an
   argument for pointing the loop at real underspecified work rather than engineering harder
   synthetic puzzles. Neither alone makes a second live run worth paying for.
2. **The readiness blockers** — doc 22's RD5/RD6/RD17/RD18 in particular: no way to fire the first
   job from the UI, mock mode indistinguishable from real, session delete destroying conversations,
   and crash/reconnect losing the model's response.
3. **Doctrine promotion** — every WD entry is `candidate`. The instrument to promote WD-1 exists
   (SC-3); no entry has yet won a measured A/B.
4. **The first production seeding** (spec §8.8) — deliberately Kai's, deliberately not done.

---

## Sources

- [More Convincing, Not More Correct: Self-Play Reward Hacking of Reference-Free LLM Judges](https://arxiv.org/html/2607.05904)
- [Who Grades the Grader? Co-Evolving Evaluation Metrics and Skills for Self-Improving LLM Agents](https://arxiv.org/html/2607.12790)
- [Harness Engineering for Self-Improvement — Lil'Log](https://lilianweng.github.io/posts/2026-07-04-harness/)
- [Robust LLM-Based Scoring via Reference-Anchored ELO Estimation](https://openreview.net/forum?id=Q88mQBuPjB)
- [Autorubric: Unifying Rubric-based LLM Evaluation](https://arxiv.org/pdf/2603.00077)
- [Semantic Voting: A Self-Evaluation-Free Approach for Efficient LLM Self-Improvement on Unverifiable Open-ended Tasks](https://arxiv.org/pdf/2509.23067)
- [Darwin Gödel Machine](https://sakana.ai/dgm/) and [Gödel Agent](https://arxiv.org/html/2410.04444v1) — self-modification precedents
- [On The Statistical Limits of Self-Improving Agents](https://arxiv.org/pdf/2510.04399)
