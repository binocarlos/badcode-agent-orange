// calibration-30.ts — the real thing. docs/product/14-calibration-runbook.md,
// executed.
//
// 30 hypotheses with known answers, through hypothesis-lab@v1 wired three ways,
// against a REAL model. Arms A and B by default (the runbook's minimum honest
// run); C with `--arms A-critic-live,B-critic-off,C-critic-sham`.
//
// This config is `mode: 'live'` and run.sh will refuse to run it against a mock
// stack — and refuse to run the smoke against a live one. Running it costs
// tokens and is ATTENDED: work plan 13's L3 says subscription OAuth, attended
// runs only, and L3X says the orchestrator and Kai run it, not an executor.
//
// The seeds are pinned in ../manifest-30.json, which is the record runbook §2
// asks for. hypolabgen re-derives the datasets from it and re-proves every trap
// before a token is spent.

import { ARM_A, ARM_B, ARM_C, COVARIATES_HINT } from '../arms'
import type { CalibrationSpec } from '../spec'

export const spec: CalibrationSpec = {
  id: 'calibration-30',
  description:
    'Runbook §2: 30 hypolab hypotheses (10 real-effect, 8 planted-null, 8 confound-trap, 4 underpowered, ' +
    'shuffled) through hypothesis-lab@v1 with the critic live (A), with the critic unsubscribed (B), and ' +
    'optionally with a sham critic (C). Ground truth lives in the harness and reaches the project only ' +
    'inside the frozen fact-checker\'s own check events.',
  mode: 'live',
  manifest: 'e2e/experiments/calibration/manifest-30.json',
  datasetDir: 'e2e/experiments/calibration/datasets/calibration-30',
  covariatesHint: COVARIATES_HINT,
  window: 10,
  /**
   * The runbook §4 ceiling, per arm project.
   *
   * A rate-limit guard, not a billing control (work plan 13, L3): it stops a
   * runaway inside one project on one day. 30 hypotheses × ~3 jobs, each
   * carrying a few thousand tokens of CSV, lands well under this; crossing it
   * means something is looping, and the runner aborts on the first
   * `rate_limited` delivery rather than grinding on.
   *
   * The orchestrator may raise or lower it with the config, not with a flag —
   * a ceiling that is easy to wave away is not a ceiling.
   */
  dailyTokensHard: 3_000_000,
  arms: [ARM_A, ARM_B, ARM_C],
}

export default spec
