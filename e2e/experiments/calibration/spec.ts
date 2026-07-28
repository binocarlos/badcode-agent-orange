// spec.ts — the shape of a calibration run: which hypotheses, through which
// arms, under what ceiling.
//
// This is the calibration sibling of `../spec.ts` (the C1 comparison rig). The
// two differ in what a "round" is and therefore in what a config has to say.
// C1 drives ONE task repeatedly through N topologies and scores output
// PREDICATES; calibration drives N different datasets through ONE topology in
// three wirings and scores each answer against a FACT the harness is holding.
// So there are no predicates here — the scorer is arithmetic over ground truth,
// and every field below is data. Which is why this config, unlike C1's, could
// have been JSON; it is TypeScript only so the two rigs read the same way.
//
// Arms are docs/product/14-calibration-runbook.md §2:
//
//   A  hypothesis-lab@v1 as applied              the thing under test
//   B  same, critic's subscription deleted       no-learning baseline (MR-3 at scale)
//   C  same, critic's prompt replaced by a sham  churn vs learning (the C7 control)
//
// A and B are the minimum honest run. C is `optional: true` and is skipped
// unless `--arms` names it.
//
// Wave 7 adds a fourth kind of difference on the same principle — the doctrine
// axis (DR1, doc 20 §3): the same arm with an operations-doctrine block written
// into its project prompt, versus withheld. It is still exactly one operator
// mutation after the apply; it just lands on the settings row rather than on a
// subscription or a worker.

import type { DoctrineVersion } from './doctrine'

/** One arm: a wiring of hypothesis-lab@v1, and what makes it differ. */
export interface CalArm {
  /** Short label; it is the row key in the report, so keep it stable. */
  id: string
  /** Worker names — one set per arm, because the mock script is agentd-wide. */
  investigator: string
  critic: string
  checker: string
  /**
   * Delete the critic's `worker.finished` subscription after the apply (arm B).
   *
   * Deleting the SUBSCRIPTION rather than the worker is deliberate: the org
   * chart stays identical row-for-row, so B differs from A in exactly one
   * edge — nothing wakes the critic. Anything stronger (deleting the critic,
   * or never applying it) would change the composed prompts too.
   */
  disableCritic?: boolean
  /**
   * Replace the critic's rendered system prompt after the apply (arm C).
   *
   * The sham critic of sham-critic@v1, transplanted: same wiring, same rewrite
   * budget, rewrites that only reorder. An ordinary operator mutation through
   * the worker PUT — the topology is untouched.
   */
  criticPromptOverride?: string
  /**
   * Inject an operations-doctrine block as this arm's project prompt (DR1).
   *
   * The canonical bytes are `docs/product/doctrine/doctrine-<version>.md` below
   * its marker line; the runner writes them through the whole-object settings
   * PUT after the apply, exactly like `daily_tokens_hard`, which config-logs it
   * for free (doc 20 §3, decision D5). `ProjectSettings.SystemPrompt` is
   * composed into EVERY worker's job as the `project prompt` section, so the
   * block rides the investigator, the critic and the frozen checker alike — an
   * arm pair that differs only in this field is a clean common-sense ablation
   * and nothing else.
   *
   * Undefined means withheld, which is the control side of that pair.
   */
  doctrine?: DoctrineVersion
  /** Skipped unless `--arms` names it explicitly. */
  optional?: boolean
  /** One line for the report's arm legend. */
  note: string
}

export interface CalibrationSpec {
  /** Filename-safe id; the report artifacts are named after it. */
  id: string
  description: string
  /**
   * `mock` refuses to run against a real-credential stack and vice versa.
   *
   * Not a convenience. A mock run's numbers are authored into the script and
   * mean nothing; a live run costs tokens. Getting the two the wrong way round
   * is the one mistake this rig can make that is expensive in both directions,
   * so the config states which it is and run.sh enforces it against the mode
   * the stack recorded.
   */
  mode: 'mock' | 'live'
  /** Repo-relative mock script path; mock-mode configs only. */
  mockScript?: string
  /** Repo-relative scenario manifest — hypolabgen's input. */
  manifest: string
  /** Repo-relative directory hypolabgen writes into (generated, gitignored). */
  datasetDir: string
  /**
   * Folded verbatim into the investigator's method charter through the
   * topology's `covariates-hint` answer — which is the only channel
   * hypothesis-lab@v1 offers into that prompt, and therefore where the OUTPUT
   * CONTRACT has to ride. Without a machine-readable final line there is no
   * verdict to score, and a scorer that guessed from prose would be measuring
   * its own regexes.
   */
  covariatesHint: string
  /**
   * The accuracy curve's window: first N vs last N hypotheses (runbook §3.1
   * says 10). Clamped to floor(n/2) so a short run still reports two disjoint
   * windows instead of overlapping ones.
   */
  window: number
  /**
   * `daily_tokens_hard` for each arm's project — the rate-limit guard of
   * runbook §4, set before the first hypothesis. Crossing it makes the router
   * refuse non-interactive jobs, which the runner sees as a `rate_limited`
   * delivery and treats as an abort, not a failure to retry.
   */
  dailyTokensHard: number
  arms: CalArm[]
}
