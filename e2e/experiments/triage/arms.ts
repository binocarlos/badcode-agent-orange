// arms.ts — the two arms of SC-1.
//
// They differ in exactly one operator mutation, made AFTER the apply, so both
// arms' org charts are rendered from the identical topology and the difference
// between them is nameable in one sentence:
//
//   A  nothing                                the loop as designed
//   B  the critic's subscription is deleted   nothing wakes the critic
//
// Worker names carry a per-arm prefix and no name is a substring of another —
// the mock script is agentd-wide, and a rule keyed on a name that is also a
// substring of another arm's name fires in the wrong project (the naming trap
// in docs/product/13's standing traps). Six workers per arm and two arms is
// twelve names in one namespace, which is the most this discipline has had to
// carry; triage-lab@v1's renderer refuses a violating set at render time.

import type { TriageArm } from './spec'

/** Arm A — triage-lab@v1 exactly as applied. The thing under test. */
export const ARM_A: TriageArm = {
  id: 'A-critic-live',
  tag: 'a',
  dispatcher: 'tra-dispatch',
  queues: { billing: 'tra-money', outage: 'tra-uptime', access: 'tra-signin' },
  critic: 'tra-critic',
  auditor: 'tra-audit',
  note: 'triage-lab@v1 as applied: the methodology critic reads every finish and may rewrite the dispatcher between tickets.',
}

/** Arm B — the no-learning baseline. MR-3 at scale. */
export const ARM_B: TriageArm = {
  id: 'B-critic-off',
  tag: 'b',
  dispatcher: 'trb-dispatch',
  queues: { billing: 'trb-money', outage: 'trb-uptime', access: 'trb-signin' },
  critic: 'trb-critic',
  auditor: 'trb-audit',
  disableCritic: true,
  note: "the same org with the critic's subscription deleted after apply: nothing wakes it, so nothing rewrites. Scorer variance and the no-learning baseline.",
}

/** Every worker name an arm renders, for the naming-discipline unit test. */
export function armWorkerNames(arm: TriageArm): string[] {
  return [arm.dispatcher, arm.queues.billing, arm.queues.outage, arm.queues.access, arm.critic, arm.auditor]
}
