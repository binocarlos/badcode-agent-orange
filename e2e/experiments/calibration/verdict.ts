// verdict.ts — turning what a worker SAID into something scoreable, and
// refusing to invent one when it did not say it.
//
// Two rules govern this file, both scar tissue:
//
//   1. **Parse the deliverable, not the transcript.** `worker.finished` text is
//      the whole exchange — the task event, the CSV, every intermediate
//      thought. A regex run over that would happily find the VERDICT line the
//      HARNESS wrote into the instructions and score the investigator on its
//      own prompt. (B1's live foot-gun, docs/product/13's log.) The runner
//      hands this file the last assistant message and nothing else.
//
//   2. **Unparseable is a result.** A conclusion with no verdict line is
//      recorded as `unparseable` and counted. It is never guessed at from
//      prose, never defaulted to `no-effect`, and never dropped from the
//      denominator — a scorer that quietly repairs its inputs measures the
//      repair. If the critic ever rewrites the output contract out of the
//      investigator's prompt, the unparseable count is how the run says so.

/** What an investigator concluded, as far as it can be read. */
export type Verdict = 'effect' | 'no-effect' | 'unparseable'

/** What the frozen fact-checker called it. */
export type CheckerCall = 'match' | 'mismatch' | 'unparseable'

/** The literal contract line the investigator is told to end with. */
export const VERDICT_PREFIX = 'VERDICT:'

/**
 * Strips the decoration a model puts around a line it means emphatically:
 * markdown bold/italic, backticks, bullet markers, trailing punctuation.
 *
 * Deliberately narrow. It removes ornament, never content — `**VERDICT:
 * effect**` and `VERDICT: effect` are the same claim, but "probably no effect"
 * is not a verdict line and no amount of stripping makes it one.
 */
function undecorate(line: string): string {
  return line
    .replace(/^[\s>*_`~-]+/, '')
    .replace(/[\s*_`~.]+$/, '')
    .trim()
}

/**
 * Reads the verdict out of an investigator's deliverable.
 *
 * Scans from the END: the contract says the verdict is the final line, and a
 * reply that states its verdict twice (a summary at the top, the contract line
 * at the bottom) must be scored on the one the contract names. The first
 * matching line found walking backwards wins.
 */
export function parseVerdict(deliverable: string): Verdict {
  const lines = (deliverable ?? '').split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = undecorate(lines[i])
    if (line === '') continue
    const m = /^VERDICT\s*:\s*(no[-\s]?effect|effect)$/i.exec(line)
    if (!m) continue
    return /^effect$/i.test(m[1]) ? 'effect' : 'no-effect'
  }
  return 'unparseable'
}

/**
 * Reads the fact-checker's call. Its charter (go/topology/hypothesislab.go)
 * fixes the wording: `Verdict: match` or `Verdict: mismatch`.
 *
 * This is the IN-PROJECT instrument's opinion, kept beside the harness's own
 * arithmetic rather than instead of it. The harness scores against the truth it
 * is holding; the checker's call is reported as an agreement rate, so a
 * disagreeing scoreboard shows up as a number instead of silently becoming the
 * result.
 */
export function parseCheckerCall(reply: string): CheckerCall {
  const lines = (reply ?? '').split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = undecorate(lines[i])
    const m = /^verdict\s*:\s*(match|mismatch)\b/i.exec(line)
    if (m) return m[1].toLowerCase() as CheckerCall
  }
  return 'unparseable'
}

/**
 * The marker the harness stamps on a check event so a mock script can key a
 * rule on (hypothesis, stated verdict) without the runner leaking its own
 * grade into the project.
 *
 * Both halves are facts the fact-checker legitimately receives anyway — which
 * hypothesis, and what the investigator concluded. What deliberately does NOT
 * appear is whether the harness thinks the conclusion is right: handing the
 * scoreboard its answer would make the agreement rate meaningless.
 */
export function checkMarker(hypothesisId: string, verdict: Verdict): string {
  const suffix = verdict === 'effect' ? 'YES' : verdict === 'no-effect' ? 'NO' : 'UNPARSED'
  return `[CAL-CHECK-${hypothesisId.toUpperCase()}-${suffix}]`
}

/** The marker on a dataset event: which hypothesis this is. */
export function taskMarker(hypothesisId: string): string {
  return `[CAL-${hypothesisId.toUpperCase()}]`
}
