// text.ts — the two pieces of event text a calibration run emits, and the one
// rule that governs both: ground truth appears in exactly one of them.
//
// The dataset event carries a hypothesis, a CSV and an output contract, and
// nothing else. The check event carries a conclusion AND the held-out verdict,
// together, addressed to the frozen fact-checker. That asymmetry is the whole
// design (AGENTS_RESEARCH §4, runbook §2): truth lives harness-side, enters the
// project once per hypothesis, and terminates at an instrument nothing
// subscribes to.
//
// Both texts carry a bracketed marker. In a live run it is just an id in the
// record; in mock it is what lets a script key one rule per hypothesis without
// keying on a worker name (the naming trap in docs/product/13's standing
// traps).

import { checkMarker, taskMarker, VERDICT_PREFIX, type Verdict } from './verdict'

/** How much of a conclusion the check event quotes back. */
export const CONCLUSION_QUOTE_LIMIT = 4000

/** The output contract, stated identically in the prompt and in every task. */
export const OUTPUT_CONTRACT = [
  'End your reply with a final line reading exactly one of:',
  `${VERDICT_PREFIX} effect`,
  `${VERDICT_PREFIX} no-effect`,
  'Nothing may follow that line. "effect" means the treatment column causally raises the outcome rate;',
  '"no-effect" means it does not, or that this sample cannot honestly support the claim.',
].join('\n')

/** What a dataset event says. */
export interface DatasetTask {
  hypothesisId: string
  /** The CSV bytes hypolabgen wrote, verbatim. */
  csv: string
  rows: number
}

/**
 * The dataset event's text: framing, hypothesis, CSV, contract.
 *
 * The hypothesis is the same sentence every time by design. Thirty different
 * questions would confound "did the org get better at investigating" with "was
 * this one easier to phrase"; one question over thirty datasets leaves the data
 * as the only thing that varies, which is what the accuracy curve needs to
 * mean anything.
 */
export function datasetEventText(task: DatasetTask): string {
  return [
    `${taskMarker(task.hypothesisId)} Hypothesis ${task.hypothesisId} — investigate and conclude.`,
    '',
    'Hypothesis: wearing a red jumper (jumper=red) causes a person to be late for the train',
    '(late=yes) more often than not wearing one.',
    '',
    `Dataset (CSV, ${task.rows} rows, one observation per line):`,
    task.csv.trimEnd(),
    '',
    'Analyse the data and report your conclusion.',
    OUTPUT_CONTRACT,
  ].join('\n')
}

/** What a check event says. */
export interface CheckTask {
  hypothesisId: string
  /** The investigator's deliverable, as written. */
  conclusion: string
  /** What the harness parsed out of it. */
  verdict: Verdict
  /** The held-out truth: does the effect exist in the generating process? */
  truthEffect: boolean
  /** hypolab's own sentence about this scenario — it already states the
   * honest report, including the underpowered case where the effect is real
   * and a null is still the right answer. */
  truthExplanation: string
}

/**
 * The check event's text: conclusion, stated verdict, held-out truth.
 *
 * The truth arrives as hypolab wrote it, not as a grade. The harness does not
 * tell the fact-checker whether the conclusion is right — that is the
 * checker's one job, and the agreement between its call and the harness's
 * arithmetic is a reported metric. Handing it the answer would turn that metric
 * into a tautology.
 */
export function checkEventText(task: CheckTask): string {
  const quoted = task.conclusion.length > CONCLUSION_QUOTE_LIMIT
    ? `${task.conclusion.slice(0, CONCLUSION_QUOTE_LIMIT)}\n[…conclusion truncated at ${CONCLUSION_QUOTE_LIMIT} characters]`
    : task.conclusion
  return [
    `${checkMarker(task.hypothesisId, task.verdict)} A conclusion is under review.`,
    '',
    `Conclusion under review (investigation ${task.hypothesisId}):`,
    quoted.trimEnd(),
    '',
    `Stated verdict: ${task.verdict}`,
    `Held-out ground truth: effect=${task.truthEffect}. ${task.truthExplanation}`,
    '',
    'Judge whether the stated verdict matches the ground truth.',
  ].join('\n')
}
