// spec.ts — the shape of a gauntlet run: which tickets, through which arms,
// under what ceiling, against which baseline.
//
// It is `../triage/spec.ts` with two additions and one subtraction.
//
//   + `doctrine` on an arm: the operator mutation DR1 built (doc 20 §3, D5) —
//     the doctrine block written into the project prompt after the apply. This
//     is the first use of that lever as an experimental AXIS rather than as a
//     delivery demo.
//   + `baseline` on the spec: the SC-1 accuracy this stream scored WITHOUT
//     attacks, quoted from a committed triage report. The robustness tax is
//     read against it, and a unit test pins the number to that report so the
//     two cannot drift.
//   − `disableCritic`: both gauntlet arms run the critic live. SC-3's arms
//     differ in doctrine and in nothing else (doc 20 §2's promotion protocol:
//     one nameable mutation per arm pair), and a critic-off arm here would be
//     answering SC-1's question with SC-3's tokens.

import type { QueueWorkers } from '../triage/route'
import type { DoctrineVersion } from './doctrine'

/** One arm: a wiring of triage-lab@v1, and what makes it differ. */
export interface GauntletArm {
  /** Short label; it is the row key in the report, so keep it stable. */
  id: string
  /**
   * One or two characters stamped into this arm's ticket markers.
   *
   * It carries the arm because a mock rule keys on one substring and the reply
   * it serves has to name THIS arm's queue worker — and because a marker that
   * already implies the arm leaves a rule's second predicate free for the
   * doctrine tripwire.
   */
  tag: string
  dispatcher: string
  /** Canonical queue id → the worker that holds that queue in this arm. */
  queues: QueueWorkers
  critic: string
  auditor: string
  /**
   * Write this doctrine version into the project prompt after the apply.
   *
   * The whole axis, and one ordinary operator mutation: the settings PUT is
   * already config-logged, so the arm's difference lands on the project's own
   * record without the rig logging anything itself (doc 20 §3).
   */
  doctrine?: DoctrineVersion
  /** Skipped unless `--arms` names it explicitly. */
  optional?: boolean
  /** One line for the report's arm legend. */
  note: string
}

/** The SC-1 result this run's robustness tax is measured against. */
export interface Baseline {
  /** The arm id in the SC-1 report the number was read from. */
  arm: string
  /** That arm's `accuracy`, verbatim. */
  accuracy: number
  /** Repo-relative path to the report — the provenance, pinned by unit test. */
  report: string
}

export interface GauntletSpec {
  /** Filename-safe id; the report artifacts are named after it. */
  id: string
  description: string
  /**
   * `mock` refuses to run against a real-credential stack and vice versa. Not a
   * convenience: a mock run's numbers are authored into the script and mean
   * nothing, and a live run costs tokens.
   */
  mode: 'mock' | 'live'
  /** Repo-relative mock script path; mock-mode configs only. */
  mockScript?: string
  /** Repo-relative adversarial manifest — gauntletgen's input. */
  manifest: string
  /** Repo-relative directory gauntletgen writes into (generated, gitignored). */
  datasetDir: string
  /** The accuracy curve's window: first N vs last N tickets, clamped to floor(n/2). */
  window: number
  /** `daily_tokens_hard` for each arm's project — the rate-limit guard. */
  dailyTokensHard: number
  /** The SC-1 baseline this run's robustness tax is read against. */
  baseline: Baseline
  arms: GauntletArm[]
}
