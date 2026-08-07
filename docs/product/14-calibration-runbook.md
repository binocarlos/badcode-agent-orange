# Calibration runbook — the first real-model run of the hypothesis lab

*Written 2026-07-27 (L3 of [`13-work-plan-self-improvement.md`](./13-work-plan-self-improvement.md)).
Status (2026-07-29): **executed once, partially — see §5a and
[`runs/2026-07-28-calibration-aborted/`](./runs/2026-07-28-calibration-aborted/README.md).** The
protocol below stands; a second run is gated on work-plan items L3H and L3M, not on approval.
Protocol:
[`AGENTS_RESEARCH.md`](../AGENTS_RESEARCH.md) §6–§7. Everything below runs against machinery that
is already merged and mock-proven (hypolab generator, hypothesis-lab@v1 seed, frozen workers).*

## 1. Question under test

Does the §8.7 loop — a methodology-critic rewriting an investigator's prompt between hypotheses —
**improve investigation accuracy on hypotheses with known answers?** This is the calibration run:
its scorer is a fact, not a judge, so a flat line means "no improvement", not "blind instrument".

## 2. Design

**Task stream.** 30 hypotheses over `hypolab`-generated datasets, fixed seeds recorded up front,
shuffled kind order, roughly: 10 real-effect, 8 planted-null, 8 confound-trap, 4 underpowered.
Ground truth (the `Verdict` returns) lives in the harness runner only — never in project memory,
never in event text except the fact-checker's own check events.

**Arms** (same 30 hypotheses, same order, separate projects):

| Arm | Topology | Isolates |
| --- | --- | --- |
| A | hypothesis-lab@v1 (critic live) | the thing under test |
| B | same, critic's subscription disabled after apply | scorer variance / no-learning baseline (MR-3 at scale) |
| C | same, critic replaced by sham (rewrites are arbitrary shuffles of instruction order) | churn vs learning — the C7 control |

Minimum honest run: arms A and B. C if budget allows. ≥2 seeds per arm if budget allows;
otherwise say "one seed" loudly in the results.

**Per-hypothesis loop** (all driven by emitted events, per C6): emit dataset event → investigator
concludes → fact-checker event (conclusion + truth) → record verdict → critic round (arm A/C
only) → next hypothesis.

## 3. Metrics (all computed from the event/config logs after the run)

1. **Accuracy curve**: correct-verdict rate, first 10 vs last 10 hypotheses, per arm. The
   headline: does A's late-minus-early beat B's?
2. **Planted-null false-confirmation rate** — the single most important number. An org that
   confirms everything is the failure this exists to catch.
3. **Confound-trap escape rate** (naive-answer vs controlled-answer conclusions).
4. **Prompt lineage**: every `worker_prompt_write` diff + rationale (free from the config log).
   Read what methodology the critic invented — this is R4's derived-rubric artifact.
5. `worker.freeze_refused` count (any attempt on the fact-checker is reward-hacking observed).
6. Token spend per arm (the usage logs), for the cost row in the results table.

## 4. Mechanics

- Stack in **api-key mode** (recommended: bounded, budgetable, terms-clean) or subscription mode
  (Kai's call; AGENTS_RESEARCH §1 caveat applies to unattended loops).
  `./e2e/run-stack-e2e.sh up api-key` or the ordinary compose stack with `.env`.
- The runner is a script (node, e2e-helper-based) that plays the §2 loop; it is *watching* the
  run, so this is attended, not headless automation.
- Record everything: project names, seeds, per-hypothesis event ids, and a dump of
  `GET /agent/config-events` + `GET /agent/events` per project, committed under
  `docs/product/runs/<date>-calibration/` (the repo's dated-record convention).
- **Cost ceiling**: set `daily_tokens_hard` on each arm's project before starting; pick the
  number when the run is approved. Abort criteria: ceiling hit, or >3 consecutive provision
  failures, or any write to the fact-checker succeeding (that is a harness bug, stop and fix).

## 5. What results mean

- **A(late) > A(early) and A > B** on accuracy, with null-rate not worsening → the loop improved
  methodology on this task. First positive calibration; poetry (Tier B proper) becomes worth
  running.
- **A ≈ B** → transmission works (the offline suite proves it) but the critic's edits didn't
  help; read the lineage to see what it tried. Not a measurement failure — that is the point of
  calibrating on facts.
- **A ≈ C** → edits help no more than churn; the loop is motion, not learning (C7).
- **Null false-confirmation rises in A** → the critic is optimising for "found something";
  reward-hacking on a factual task — the most instructive possible outcome.

## 5a. What actually happened — run 1, 2026-07-28 (partial)

Record: [`runs/2026-07-28-calibration-aborted/`](./runs/2026-07-28-calibration-aborted/README.md).
Attended, subscription mode, Kai present. Probe 3/3; arm A reached h08 (8/8; **11/11** including
the probe, across all four kinds); arm B never started; the runner crashed at h09 and wrote no
report.

**Result: §5's "A ≈ B", diagnosed before arm B ran.** Zero prompt rewrites in 11 rounds — the
critic ran each round and declined on the record ("No methodological amendment required") because
the investigator was already stratifying and declining to overclaim. Nothing was wrong, so nothing
improved. At this model strength these datasets do not discriminate between org charts; the next
run needs a harder manifest (work-plan **L3M**), and the runner needs to survive an upstream
failure (**L3H**) — a delivery marked `ok` with an empty session defeated §4's `rate_limited`
abort and an unhandled poll timeout destroyed eight successful hypotheses' worth of results.

The instrument is not implicated: a fact scorer reported an honest null rather than a flattering
curve, which is the whole reason §1 calibrates on facts first.

## 6. Checklist for the go decision

- [ ] Kai approves the run and picks credential mode (api-key recommended) and token ceiling.
- [ ] Arms: A+B minimum; C and second seeds if budget allows.
- [ ] Runner script reviewed (it is harness code, not engine code — lives in e2e/experiments/).
- [ ] Results land as a dated record + a summary appended to this file.
