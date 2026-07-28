// smoke-4.ts — the mock smoke. Four hypotheses, arms A and B, offline and free.
//
// It exists so that the live run's failures are MODEL failures rather than
// harness failures. Every mechanism the real run depends on is exercised here
// against the scripted model:
//
//   * the topology applies and the fact-checker comes back frozen
//   * `daily_tokens_hard` is set through the whole-object settings PUT
//   * arm B's critic subscription is deleted and the critic never wakes
//   * a dataset event carries a CSV to the investigator, whose deliverable
//     parses into a VERDICT — and on the planted null it parses into the WRONG
//     one, so the false-confirmation rate is observed non-zero rather than
//     assumed to work
//   * arm A's critic fires, is refused at the frozen checker, rewrites the
//     investigator with a rationale, and the rewrite CHANGES the next
//     hypothesis's answer — so the accuracy curve registers a difference
//   * conclusion + truth reach the frozen checker, whose call is parsed
//   * sessions are swept per hypothesis and the port pool comes back clean
//
// **The numbers it produces are meaningless as a result.** Arm A scores 4/4 and
// arm B 1/4 because the mock script says so, not because an org learned
// anything. AGENTS_RESEARCH §7, Tier A: mock proves transmission, never
// discovery. The report markdown says the same thing on its own face.

import { ARM_A, ARM_B, COVARIATES_HINT } from '../arms'
import type { CalibrationSpec } from '../spec'

export const spec: CalibrationSpec = {
  id: 'smoke-4',
  description:
    'Mock smoke for the calibration runner: four hypotheses (one per trap kind) through arms A and B, ' +
    'scripted end to end. Proves the machinery, measures nothing.',
  mode: 'mock',
  mockScript: 'e2e/mock-scripts/calibration-smoke.json',
  manifest: 'e2e/experiments/calibration/manifest-smoke-4.json',
  datasetDir: 'e2e/experiments/calibration/datasets/smoke-4',
  covariatesHint: COVARIATES_HINT,
  // Four hypotheses clamp this to 2 — see metrics.windowFor. The report prints
  // the window it used, not the one it was asked for.
  window: 10,
  // High enough that the mock (which reports a fixed 10 in / 10 out per turn)
  // can never reach it: the smoke proves the ceiling is SET, not that it bites.
  dailyTokensHard: 5_000_000,
  arms: [ARM_A, ARM_B],
}

export default spec
