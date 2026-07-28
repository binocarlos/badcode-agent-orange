// arms.ts — the three arms of runbook §2, and the two strings both configs
// share.
//
// The arms differ in exactly one operator mutation each, made AFTER the apply,
// so every arm's org chart is rendered from the identical topology and the
// difference between them is nameable in one sentence:
//
//   A  nothing                                  the loop as designed
//   B  the critic's subscription is deleted     nothing wakes the critic
//   C  the critic's prompt is replaced          the critic reshuffles instead of diagnosing
//
// Worker names carry a per-arm prefix and no name is a substring of another —
// the mock script is agentd-wide, and a rule keyed on a name that is also a
// substring of another arm's name fires in the wrong project (the naming trap
// in docs/product/13's standing traps).

import { OUTPUT_CONTRACT } from './text'
import type { CalArm } from './spec'

/**
 * The `covariates-hint` answer, folded verbatim into the investigator's method
 * charter by hypothesis-lab@v1's renderer.
 *
 * It carries the output contract because that answer is the ONLY channel the
 * topology offers into that prompt, and without a machine-readable final line
 * there is nothing to score. The contract is the same string the task events
 * restate, imported from one place so the two can never drift — a prompt and a
 * task that disagree about the contract would produce unparseable verdicts and
 * look like a model failure.
 */
export const COVARIATES_HINT = [
  'age_group — it may drive both jumper colour and lateness, so compare within age groups',
  'before concluding, and say which comparison your verdict rests on.',
  '',
  'OUTPUT CONTRACT (keep this rule whatever else changes):',
  OUTPUT_CONTRACT,
].join('\n')

/**
 * Arm C's critic prompt: sham-critic@v1's charter, transplanted verbatim except
 * for the worker name.
 *
 * Byte-similarity to the real seed is the point — the control has the identical
 * intervention channel (same tools, same wiring, same rewrite budget) and
 * differs only in what it is told to write. A minus C is therefore diagnosis
 * minus churn, playbook C7, by construction.
 */
export function shamCriticPrompt(investigator: string): string {
  return [
    `You review ${investigator}'s finished work. Each delivery you receive is ${investigator}'s full transcript.`,
    'You are a control, not a judge: you never evaluate quality and you hold no standard of good.',
    `After each delivery, use worker_prompt_read and worker_prompt_write to REORDER the instructions in ${investigator}'s system prompt:`,
    'change the order of its sentences or rules and nothing else — no additions, no removals, no rewording beyond what the reordering itself requires.',
    'Be honest in the rationale, every time: say plainly that this is an arbitrary reshuffle with no diagnostic content. Never claim something was wrong.',
  ].join('\n')
}

/** Arm A — hypothesis-lab@v1 exactly as applied. The thing under test. */
export const ARM_A: CalArm = {
  id: 'A-critic-live',
  investigator: 'cala-invest',
  critic: 'cala-critic',
  checker: 'cala-judge',
  note: 'hypothesis-lab@v1 as applied: the methodology critic reads every finish and may rewrite the investigator between hypotheses.',
}

/** Arm B — the no-learning baseline. MR-3 at scale. */
export const ARM_B: CalArm = {
  id: 'B-critic-off',
  investigator: 'calb-invest',
  critic: 'calb-critic',
  checker: 'calb-judge',
  disableCritic: true,
  note: "the same org with the critic's subscription deleted after apply: nothing wakes it, so nothing rewrites. Scorer variance and the no-learning baseline.",
}

/** Arm C — the sham. Optional: run it when the budget allows. */
export const ARM_C: CalArm = {
  id: 'C-critic-sham',
  investigator: 'calc-invest',
  critic: 'calc-critic',
  checker: 'calc-judge',
  criticPromptOverride: shamCriticPrompt('calc-invest'),
  optional: true,
  note: "the same org with the critic's prompt replaced by sham-critic@v1's: it reshuffles the investigator's instructions and says so. Churn without diagnosis.",
}
