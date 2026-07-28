// route.ts — turning what the dispatcher SAID into something scoreable, and
// refusing to invent an answer when it did not say one.
//
// Two rules govern this file, both scar tissue:
//
//   1. **Parse the deliverable, not the transcript.** `worker.finished` text is
//      the whole exchange — the ticket, the charter's own example line, every
//      intermediate thought. A regex run over that would happily find the
//      `ROUTE-TO: <queue-name>` line the TOPOLOGY wrote into the charter and
//      score the dispatcher on its own prompt. (B1's live foot-gun,
//      docs/product/13's log.) The runner hands this file the last assistant
//      message and nothing else.
//
//   2. **Unparseable is a result.** A reply with no ROUTE-TO line is recorded as
//      `unparseable` and counted. It is never guessed at from prose, never
//      defaulted to `escalate`, and never dropped from the denominator — a
//      scorer that quietly repairs its inputs measures the repair. If the critic
//      ever rewrites the output contract out of the dispatcher's prompt, the
//      unparseable count is how the run says so.
//
// The vocabulary is deliberately TWO-LAYERED. The ROUTE-TO line names a WORKER
// (that is the only addressing the org has — T4–T7), while truth and metrics
// speak in canonical QUEUE IDs (`billing`, `outage`, `access`, `escalate`),
// which is what go/triagelab generates and what makes two arms with different
// worker names comparable at all. `canonicalRoute` is the single place the two
// meet.

/** The canonical queue ids go/triagelab's truth speaks in. */
export type Queue = 'billing' | 'outage' | 'access'

/** A scoreable answer: a queue, the reserved escalation, or nothing readable. */
export type Route = Queue | 'escalate' | 'unparseable'

/** What the frozen route-auditor called it. */
export type AuditorCall = 'match' | 'mismatch' | 'unparseable'

/** The reserved ROUTE-TO value. Must match go/topology's TriageEscalateRoute. */
export const ESCALATE = 'escalate'

/** The literal contract line the dispatcher is told to end with. */
export const ROUTE_PREFIX = 'ROUTE-TO:'

/** One arm's mapping from canonical queue id to the worker that holds it. */
export interface QueueWorkers {
  billing: string
  outage: string
  access: string
}

/** The canonical queue ids, in the fixed order reports print them. */
export const QUEUES: readonly Queue[] = ['billing', 'outage', 'access']

/**
 * Strips the decoration a model puts around a line it means emphatically:
 * markdown bold/italic, backticks, bullet markers, trailing punctuation.
 *
 * Deliberately narrow. It removes ornament, never content — `**ROUTE-TO:
 * billing-desk**` and `ROUTE-TO: billing-desk` are the same claim, but "I think
 * this is probably billing" is not a route line and no amount of stripping
 * makes it one.
 */
function undecorate(line: string): string {
  return line
    .replace(/^[\s>*_`~-]+/, '')
    .replace(/[\s*_`~.]+$/, '')
    .trim()
}

/**
 * Reads the raw ROUTE-TO value out of a dispatcher's deliverable, exactly as
 * written. Returns '' when there is no contract line.
 *
 * Scans from the END: the contract says the route is the final line, and a
 * reply that states a route twice (a summary at the top, the contract line at
 * the bottom) must be scored on the one the contract names.
 */
export function parseStatedRoute(deliverable: string): string {
  const lines = (deliverable ?? '').split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = undecorate(lines[i])
    if (line === '') continue
    const m = /^ROUTE-TO\s*:\s*(\S.*)$/i.exec(line)
    if (!m) continue
    const value = m[1].trim()
    // The charter's own worked example. A dispatcher that echoed the template
    // instead of filling it in has not routed anything, and scoring the
    // placeholder as a route would credit it for the prompt it was given.
    if (value.startsWith('<') && value.endsWith('>')) return ''
    return value
  }
  return ''
}

/**
 * Maps a stated route onto the canonical vocabulary.
 *
 * A worker name this arm does not hold is `unparseable`, not a wrong queue: the
 * dispatcher addressed something that does not exist, which is a different
 * failure from misrouting, and folding the two together would inflate the
 * headline misroute rate with output-contract breakage.
 */
export function canonicalRoute(stated: string, workers: QueueWorkers): Route {
  const value = stated.trim()
  if (value === '') return 'unparseable'
  if (value.toLowerCase() === ESCALATE) return ESCALATE
  for (const queue of QUEUES) {
    if (value === workers[queue]) return queue
  }
  return 'unparseable'
}

/** Deliverable → canonical route, the two steps above in one call. */
export function parseRoute(deliverable: string, workers: QueueWorkers): Route {
  return canonicalRoute(parseStatedRoute(deliverable), workers)
}

/**
 * Reads the route-auditor's call. Its charter (go/topology/triagelab.go) fixes
 * the wording: `Verdict: match` or `Verdict: mismatch`.
 *
 * This is the IN-PROJECT instrument's opinion, kept beside the harness's own
 * arithmetic rather than instead of it. The harness scores against the truth it
 * is holding; the auditor's call is reported as an agreement rate, so a
 * disagreeing scoreboard shows up as a number instead of silently becoming the
 * result.
 */
export function parseAuditorCall(reply: string): AuditorCall {
  const lines = (reply ?? '').split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = undecorate(lines[i])
    const m = /^verdict\s*:\s*(match|mismatch)\b/i.exec(line)
    if (m) return m[1].toLowerCase() as AuditorCall
  }
  return 'unparseable'
}

/**
 * The marker on a ticket event: which arm, and which ticket.
 *
 * The ARM is in there because a mock rule keys on one substring and the reply it
 * serves has to name that arm's own queue worker — two arms cannot share a
 * ROUTE-TO line the way calibration's two arms shared a VERDICT token. In a live
 * run it is just an id in the record.
 */
export function taskMarker(armTag: string, ticketId: string): string {
  return `[TRI-${armTag.toUpperCase()}-${ticketId.toUpperCase()}]`
}

/**
 * The marker the harness stamps on an audit event so a mock script can key a
 * rule on (ticket, stated route) without the runner leaking its own grade into
 * the project.
 *
 * Both halves are facts the auditor legitimately receives anyway — which ticket,
 * and where the dispatcher sent it. What deliberately does NOT appear is whether
 * the harness thinks that was right: handing the scoreboard its answer would
 * make the agreement rate meaningless. It carries no arm tag either, because the
 * right call depends only on the ticket and the stated route, so one rule serves
 * every arm — and an arm-shaped scoreboard would be a scoreboard per arm.
 */
export function auditMarker(ticketId: string, route: Route): string {
  return `[TRI-AUDIT-${ticketId.toUpperCase()}-${route.toUpperCase()}]`
}
