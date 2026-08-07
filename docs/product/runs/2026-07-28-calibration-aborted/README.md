# 2026-07-28 — first live calibration (L3X): probe clean, full run aborted at h09

*Attended run, Kai at the keyboard, subscription OAuth mode. Protocol:
[`14-calibration-runbook.md`](../../14-calibration-runbook.md). Rig: `e2e/experiments/calibration/`.
**This is a partial record of an incomplete run.** Arm A reached h08 of 30; arm B never started;
no report artifact exists because the runner crashed before writing one. Everything below is
reconstructed from the console log and the project/config event dumps beside this file.*

## What ran

| | |
| --- | --- |
| Probe | 3 hypotheses (h01–h03), arm A only, **3/3 correct**, 35,132 tokens, 572s. Artifacts in `probe/`. |
| Full run | 30 hypotheses × arms A+B intended. Arm A completed **h01–h08, 8/8 correct**, then died at h09. |
| Combined | **11/11 correct verdicts** across all four hypothesis kinds, on real model calls. |
| Rewrites | **0.** Not one `worker_prompt_write` in 11 rounds. |
| `freeze_refused` | **0.** No attempt on the frozen fact-checker. |
| Checker agreement | **1.0** — the in-project scoreboard never disagreed with the harness. |

Per-kind: real-effect confirmed correctly (h01, h04, h08); confound traps escaped (h02, h06);
planted nulls rejected (h03, h07); the underpowered sample answered `no-effect` (h05) — restraint,
the behaviour `expected_verdict` scoring exists to reward.

## The headline finding: the task has a ceiling, and the loop is inert on it

The critic ran all nine rounds and **declined to intervene every time**. Verbatim from its last
complete round:

> The methodology is sound. cala-invest: 1. Stated method: Yes — "Stratified Analysis controlling
> for age_group" 2. Controlled for stated covariates: Yes 3. Reported effect or null with
> confidence: Yes … **No methodological amendment required.**

That is the critic working correctly. The investigator was already stratifying, testing, and
declining to overclaim on the first hypothesis it ever saw — so there was nothing to improve, and
the §8.7 loop had no purchase. Arm A would have equalled arm B not because transmission failed
(the offline suite proves transmission) but because **A never diverged from B**.

This is runbook §5's "A ≈ B" outcome, arriving with an unusually readable cause. Its consequence
for the experiment design is concrete: at this model strength, n=400 datasets with clean planted
effects **do not discriminate between org charts**. Re-running the same 30 would buy an expensive
null. A discriminating calibration needs harder instruments — smaller effect sizes, noisier data,
lower n, multi-way confounds — chosen so a competent first attempt is *not* already correct.

The instrument itself is vindicated: fact-scored, no judge to hack, and it reported a true "no
improvement to see here" rather than a flattering curve.

## Why it stopped (three defects, none of them the model's)

At 17:32 UTC, after ~40 minutes of continuous real-model work:

1. **h09's investigator job ended mid-turn** — 76s against a 160–240s norm, its deliverable the
   single line `Let me analyze the data directly using bash.` Parsed, correctly, as `unparseable`.
2. **The frozen checker's job then finished in 6 seconds having produced no assistant message at
   all** — yet its delivery was recorded `status = ok` (`event_deliveries`, session
   `228114c6-…`). The session holds the user message and nothing else.
3. **The runner polled that session for 180s and threw**, killing the whole process: arm A's eight
   completed hypotheses were lost (no report written) and arm B never started.

The signature of (1)+(2) inside one 90-second window — a truncated turn followed by an empty one —
is what an upstream API failure (subscription usage limit, overload) looks like from outside the
container. The harness swallowed it and reported success both times, which is why the runbook's
`rate_limited` abort criterion never fired.

### Defects to fix before the next live run

- **A delivery can be `ok` with an empty session.** Outcome status does not reflect whether the
  model said anything, so "success" was reported for a turn that produced nothing. This defeated
  the designed abort. Same family as TOK1: a reader trusting a status that was never true.
- **A poll timeout is fatal to every arm.** It should be a recorded per-hypothesis failure or a
  clean arm abort — the rig already knows how to abort an arm, write the report, and continue with
  the remaining arms. Roughly 135k tokens of real spend produced zero artifacts because the throw
  bypassed all of it.
- **Swept sessions leave no transcript in the database.** `agent_query_events` holds zero rows for
  every session in this run, including the successful ones — sessions are deleted per hypothesis
  before anything archives them. The harness's own run-log is therefore the *only* transcript
  record, and it is exactly what a crash destroys. Runbook §4 says "record everything"; today that
  requirement was met only by luck and a console log.

## Files here

| Path | What |
| --- | --- |
| `run-console.log` | The full console output, including the crash and stack trace. |
| `arm-a-project-events.json` | Every project event of arm A's project (the transcripts, as `worker.finished` text). |
| `arm-a-config-events.json` | The complete config log: 11 events, all setup and teardown, **no rewrites**. |
| `probe/` | The clean 3-hypothesis probe: report JSON + markdown + run-log + metadata. |

Project: `e2e-cal-a-critic-live-ms4wt5v7-y9jka`. Manifest seeds are pinned in
`e2e/experiments/calibration/manifest-30.json`; datasets regenerate byte-identically from it.
