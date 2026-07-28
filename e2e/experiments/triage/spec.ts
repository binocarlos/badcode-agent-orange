// spec.ts — the shape of a triage run: which tickets, through which arms, under
// what ceiling.
//
// This is the SC-1 sibling of `../calibration/spec.ts`. The two differ in what a
// "round" is and therefore in what a config has to say. Calibration drives N
// datasets through one org and scores each CONCLUSION against a fact; triage
// drives N tickets through one org and scores each ROUTE against a fact. So
// every field below is data, and the only new one is the per-arm queue map:
// truth speaks in canonical queue ids, the org addresses workers by name, and
// something has to hold the correspondence.
//
// Arms are doc 19 §3 SC-1:
//
//   A  triage-lab@v1 as applied                the thing under test
//   B  same, critic's subscription deleted     no-learning baseline (MR-3 at scale)
//
// A and B are the whole run. There is deliberately no sham arm yet: SC-1's
// question is whether the critic learns routing RULES or routing CONFIDENCE,
// and churn-vs-learning (playbook C7) is already instrumented in the
// calibration rig. Adding a third arm here would triple the tokens to re-answer
// a question that has an instrument.

import type { QueueWorkers } from './route'

/** One arm: a wiring of triage-lab@v1, and what makes it differ. */
export interface TriageArm {
  /** Short label; it is the row key in the report, so keep it stable. */
  id: string
  /**
   * Two-or-three characters stamped into this arm's ticket markers.
   *
   * A mock rule keys on one substring, and the reply it serves has to name THIS
   * arm's queue worker — so unlike calibration (whose arms shared a VERDICT
   * token) the ticket marker has to say which arm is asking.
   */
  tag: string
  /** Worker names — one set per arm, because the mock script is agentd-wide. */
  dispatcher: string
  /** Canonical queue id → the worker that holds that queue in this arm. */
  queues: QueueWorkers
  critic: string
  auditor: string
  /**
   * Delete the critic's `worker.finished` subscription after the apply (arm B).
   *
   * Deleting the SUBSCRIPTION rather than the worker is deliberate: the org
   * chart stays identical row-for-row, so B differs from A in exactly one
   * edge — nothing wakes the critic. Anything stronger (deleting the critic, or
   * never applying it) would change the composed prompts too.
   */
  disableCritic?: boolean
  /** Skipped unless `--arms` names it explicitly. */
  optional?: boolean
  /** One line for the report's arm legend. */
  note: string
}

export interface TriageSpec {
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
  /** Repo-relative ticket manifest — triagelabgen's input. */
  manifest: string
  /** Repo-relative directory triagelabgen writes into (generated, gitignored). */
  datasetDir: string
  /**
   * The accuracy curve's window: first N vs last N tickets. Clamped to
   * floor(n/2) so a short run still reports two disjoint windows instead of
   * overlapping ones.
   */
  window: number
  /**
   * `daily_tokens_hard` for each arm's project — the rate-limit guard, set
   * before the first ticket. Crossing it makes the router refuse
   * non-interactive jobs, which the runner sees as a `rate_limited` delivery
   * and treats as an abort, not a failure to retry.
   */
  dailyTokensHard: number
  arms: TriageArm[]
}
