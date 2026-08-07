// doctrine-smoke-4.ts — the doctrine axis (work plan 13, DR1; doc 20 §3).
//
// The same four hypotheses as `smoke-4`, through the same org chart, twice: once
// as applied, and once with `docs/product/doctrine/doctrine-v1.md` written into
// the project prompt after the apply. Exactly one operator mutation apart, which
// is decision D5's whole claim — doctrine needs no engine code, because the
// project prompt already reaches every composed prompt and already has a
// config-logged write path.
//
// **What this config proves, precisely.** That the doctrine block reaches the
// COMPOSED PROMPT of the worker it is meant to govern — delivery, not storage.
// The mock script partitions `cald-invest`'s rules on its identity phrase and
// splits that partition with `absent:` on a doctrine-v1 sentence:
//
//   * doctrine present → the rule is skipped and the request falls through to
//     the shared per-hypothesis rules, so the doctrine arm answers exactly as
//     arm A does, hypothesis for hypothesis;
//   * doctrine ABSENT → the tripwire fires and the investigator returns prose
//     with no contract line, which the rig records as `unparseable` on all four
//     hypotheses (doctrine-v1's WD-2 is the entry about output contracts, so the
//     scripted failure is the one that entry exists to prevent).
//
// So a green run is the assertion: accuracy 4/4 with `unparseable` 0 in the
// doctrine arm is only reachable if the block was in the prompt. A broken
// injection does not merely fail to help — it collapses the arm to 0/4 and
// changes the committed report, which is how this rig makes anything loud.
//
// The identity phrase has to be the partition and the doctrine phrase the split,
// never the other way round: the block rides EVERY worker in the arm — critic
// and frozen checker included — so a rule keyed on a doctrine phrase alone would
// answer the checker's requests too (docs/product/13's standing traps: partition
// by worker name first).
//
// **Its numbers are meaningless as a result**, exactly as `smoke-4`'s are. Both
// arms score 4/4 because the script says so, and the equality is the point: an
// authored delta between doctrine and no-doctrine would be a number about the
// script. Whether doctrine changes how an org performs is a LIVE question, and
// doc 20 §2 keeps every entry CANDIDATE until a real run answers it.

import { ARM_A, ARM_A_DOCTRINE_V1, COVARIATES_HINT } from '../arms'
import type { CalibrationSpec } from '../spec'

export const spec: CalibrationSpec = {
  id: 'doctrine-smoke-4',
  description:
    'Mock smoke for the doctrine axis: the four smoke hypotheses through arm A and through arm A with ' +
    'doctrine-v1 written into the project prompt. Proves the block reaches composed prompts; measures nothing.',
  mode: 'mock',
  // A superset of calibration-smoke.json: every rule that file has, plus the
  // doctrine arm's critic and the doctrine tripwire. A separate file rather
  // than an extension of that one, so `smoke-4` runs against bytes this item
  // never touched.
  mockScript: 'e2e/mock-scripts/calibration-doctrine-smoke.json',
  // The same manifest as smoke-4, deliberately: the pair must differ in the
  // doctrine mutation and in nothing else, datasets included.
  manifest: 'e2e/experiments/calibration/manifest-smoke-4.json',
  // Its own generated directory even so — two configs writing one dataset
  // directory would make a stale-dataset failure look like a doctrine failure.
  datasetDir: 'e2e/experiments/calibration/datasets/doctrine-smoke-4',
  covariatesHint: COVARIATES_HINT,
  window: 10,
  dailyTokensHard: 5_000_000,
  arms: [ARM_A, ARM_A_DOCTRINE_V1],
}

export default spec
