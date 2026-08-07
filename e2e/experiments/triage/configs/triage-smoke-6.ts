// triage-smoke-6.ts — the mock smoke. Six tickets, arms A and B, offline and free.
//
// It exists so that the live run's failures are MODEL failures rather than
// harness failures. Every mechanism the real run depends on is exercised here
// against the scripted model:
//
//   * triage-lab@v1 applies and the route-auditor comes back frozen
//   * `daily_tokens_hard` is set through the whole-object settings PUT
//   * arm B's critic subscription is deleted and the critic never wakes
//   * a ticket event carries generated text to the dispatcher, whose deliverable
//     parses into a ROUTE-TO — and on the traps it parses into the WRONG one, so
//     the headline misroute rate is observed non-zero rather than assumed
//   * the dispatcher's finish fans out to all THREE queues as well as the
//     critic, and every one of those sessions is settled and swept
//   * arm A's critic fires, is refused at the frozen auditor, rewrites the
//     dispatcher with a rationale, and the rewrite CHANGES the next ticket's
//     route — so the accuracy curve registers a difference that is a DELIVERY
//     assertion, not a storage one
//   * decision + truth reach the frozen auditor, whose call is parsed
//   * `escalate` is reachable, parseable, and scored as an answer
//
// **The numbers it produces are meaningless as a result.** Arm A scores 6/6 from
// ticket 2 onward and arm B 2/6 because the mock script says so, not because an
// org learned anything. AGENTS_RESEARCH §7, Tier A: mock proves transmission,
// never discovery. The report markdown says the same thing on its own face.

import { ARM_A, ARM_B } from '../arms'
import type { TriageSpec } from '../spec'

export const spec: TriageSpec = {
  id: 'triage-smoke-6',
  description:
    'Mock smoke for the triage runner: six tickets (two per trap kind) through arms A and B, ' +
    'scripted end to end. Proves the machinery, measures nothing.',
  mode: 'mock',
  mockScript: 'e2e/mock-scripts/triage-smoke.json',
  manifest: 'e2e/experiments/triage/manifest-smoke-6.json',
  datasetDir: 'e2e/experiments/triage/datasets/triage-smoke-6',
  // Six tickets clamp this to 3 — see metrics.windowFor. The report prints the
  // window it used, not the one it was asked for. Three is exactly the point
  // where the rewrite (which lands after ticket 1) splits the two windows.
  window: 10,
  // High enough that the mock (which reports a fixed 10 in / 10 out per turn)
  // can never reach it: the smoke proves the ceiling is SET, not that it bites.
  dailyTokensHard: 5_000_000,
  arms: [ARM_A, ARM_B],
}

export default spec
