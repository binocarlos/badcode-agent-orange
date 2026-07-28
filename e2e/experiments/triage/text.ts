// text.ts — the two pieces of event text a triage run emits, and the one rule
// that governs both: ground truth appears in exactly one of them.
//
// The ticket event carries a generated ticket and an output contract, and
// nothing else. The audit event carries the stated route AND the held-out
// correct route, together, addressed to the frozen route-auditor. That
// asymmetry is the whole design (AGENTS_RESEARCH §4, doc 19 §2): truth lives
// harness-side, enters the project once per ticket, and terminates at an
// instrument nothing subscribes to.
//
// Both texts carry a bracketed marker. In a live run it is just an id in the
// record; in mock it is what lets a script key one rule per ticket without
// keying on a worker name (the naming trap in docs/product/13's standing
// traps).

import { auditMarker, ESCALATE, QUEUES, ROUTE_PREFIX, taskMarker, type QueueWorkers, type Route } from './route'

/** How much of a dispatcher's reply the audit event quotes back. */
export const REPLY_QUOTE_LIMIT = 4000

/**
 * The output contract, stated identically in the charter and in every ticket.
 *
 * It has to name the arm's own queue workers, because the ROUTE-TO line is
 * addressing and the only addressing the org has is a worker name (T4–T7).
 */
export function routeContract(workers: QueueWorkers): string {
  return [
    'End your reply with a final line reading exactly one of:',
    ...QUEUES.map((q) => `${ROUTE_PREFIX} ${workers[q]}`),
    `${ROUTE_PREFIX} ${ESCALATE}`,
    'Nothing may follow that line. Name exactly one destination.',
    `"${ESCALATE}" means no stated fact matches a rule and a person should decide — it is an answer, not a way of avoiding one.`,
  ].join('\n')
}

/**
 * The `routing-rules` answer, folded verbatim into the dispatcher's charter by
 * triage-lab@v1's renderer.
 *
 * It carries the output contract because that answer is the ONLY channel the
 * topology offers into that prompt, and without a machine-readable final line
 * there is nothing to score. The contract is the same string the ticket events
 * restate, built from one function so the two can never drift — a charter and a
 * ticket that disagree about the contract would produce unparseable routes and
 * look like a model failure.
 *
 * The rules are the CONTENT rules go/triagelab generates against, stated in the
 * prose a charter can carry. The last one is the whole scenario: vocabulary is
 * not a fact.
 */
export function routingRules(workers: QueueWorkers): string {
  return [
    `- A ticket that states a monetary amount which does not match an agreed figure goes to ${workers.billing}.`,
    `- A ticket that states an HTTP status in the 500s, or a request that timed out, goes to ${workers.outage}.`,
    `- A ticket that states that somebody cannot sign in, or was denied permission, goes to ${workers.access}.`,
    `- A ticket that states none of those goes to ${ESCALATE}.`,
    '',
    'Vocabulary is not a fact. A ticket written entirely in one queue\'s words still goes elsewhere, or to ' +
      `${ESCALATE}, unless it STATES that queue's fact. Say which rule you applied, and quote the sentence you applied it to.`,
    '',
    'OUTPUT CONTRACT (keep this rule whatever else changes):',
    routeContract(workers),
  ].join('\n')
}

/** What a ticket event says. */
export interface TicketTask {
  armTag: string
  ticketId: string
  /** The ticket bytes triagelabgen wrote, verbatim. */
  ticket: string
  workers: QueueWorkers
}

/**
 * The ticket event's text: framing, the ticket, the contract.
 *
 * The framing sentence is the same every time by design. Twenty-four different
 * framings would confound "did the org get better at routing" with "was this
 * one phrased more helpfully"; one framing over twenty-four tickets leaves the
 * ticket as the only thing that varies, which is what the accuracy curve needs
 * to mean anything.
 */
export function ticketEventText(task: TicketTask): string {
  return [
    `${taskMarker(task.armTag, task.ticketId)} Ticket ${task.ticketId} — decide where it belongs.`,
    '',
    'A customer has written in. Apply your routing rules to the facts this ticket states.',
    '',
    '--- ticket ---',
    task.ticket.trimEnd(),
    '--- end of ticket ---',
    '',
    routeContract(task.workers),
  ].join('\n')
}

/** What an audit event says. */
export interface AuditTask {
  ticketId: string
  /** The dispatcher's deliverable, as written. */
  reply: string
  /** What the harness parsed out of it, in canonical vocabulary. */
  route: Route
  /** The raw ROUTE-TO value, as the dispatcher wrote it. */
  statedRoute: string
  /** The held-out correct route for this ticket. */
  truthRoute: string
  /** triagelab's own sentence about this ticket — it already says why. */
  truthExplanation: string
}

/**
 * The audit event's text: the reply, the stated route, the held-out truth.
 *
 * The truth arrives as triagelab wrote it, not as a grade. The harness does not
 * tell the auditor whether the route was right — that is the auditor's one job,
 * and the agreement between its call and the harness's arithmetic is a reported
 * metric. Handing it the answer would turn that metric into a tautology.
 */
export function auditEventText(task: AuditTask): string {
  const quoted =
    task.reply.length > REPLY_QUOTE_LIMIT
      ? `${task.reply.slice(0, REPLY_QUOTE_LIMIT)}\n[…reply truncated at ${REPLY_QUOTE_LIMIT} characters]`
      : task.reply
  return [
    `${auditMarker(task.ticketId, task.route)} A routing decision is under review.`,
    '',
    `Decision under review (ticket ${task.ticketId}):`,
    quoted.trimEnd(),
    '',
    `Stated route: ${task.statedRoute === '' ? '(none stated)' : task.statedRoute}`,
    `Held-out correct route: ${task.truthRoute}. ${task.truthExplanation}`,
    '',
    'Judge whether the stated route matches the correct route.',
  ].join('\n')
}
