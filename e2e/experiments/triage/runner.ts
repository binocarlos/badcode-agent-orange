// runner.ts — the half of the triage rig that touches the stack.
//
// One job: turn a TriageArm into an ArmOutcome. Apply triage-lab@v1 to a fresh
// run-scoped project, set the token ceiling, wire the arm's one difference, then
// walk the tickets: ticket event in, ROUTE-TO out, audit event in, auditor's
// call out. Then sweep.
//
// Shaped by the same scar tissue as ../calibration/runner.ts, plus one of its
// own:
//
//   * **Poll, never sleep.** Every wait is on a delivery row, an event row or a
//     config record.
//   * **A ticket is not over when the dispatcher stops.** In arm A the critic's
//     rewrite lands in a job that STARTS after the dispatcher finishes. Reading
//     the config log at dispatcher-finish races it, and the next ticket would
//     then run against a prompt that was still being written. (C1's
//     round-boundary lesson, docs/product/13's log.)
//   * **The fan-out is part of the round too.** triage-lab broadcasts the
//     dispatcher's transcript to THREE queues as well as the critic, so a
//     ticket's follow-on job count is 3 (+1 when the critic is subscribed).
//     Waiting for only the critic would leave three containers being born as
//     the sweep ran — the exact shape of leak that empties a 100-port pool.
//   * **Sessions are swept per ticket, not per arm.** Twenty-four tickets × six
//     sessions each × two arms is 288 containers against a pool of 100.
//   * **Parse the deliverable, not the transcript.** The route comes from the
//     last assistant message of the dispatcher's session. `worker.finished` text
//     is the whole exchange and contains the charter's own ROUTE-TO template.
//
// Nothing here computes a metric; that is metrics.ts, which has no idea a stack
// exists.

import {
  newProjectClient,
  poll,
  type EventDelivery,
  type ProjectClient,
  type ProjectEvent,
  type TopologyBody,
} from '../../helpers/api'
import type { PromptWriteRecord } from '../report'
import type { ArmOutcome, TicketOutcome } from './metrics'
import { canonicalRoute, parseAuditorCall, parseStatedRoute } from './route'
import type { TriageArm, TriageSpec } from './spec'
import { auditEventText, routingRules, ticketEventText } from './text'
import type { Ticket } from './truths'

/** Generous: mock is fast, but every job is a real container. */
const DELIVERY_TIMEOUT_MS = 300_000
const EVENT_TIMEOUT_MS = 240_000

/** Stop after this many provision failures in a row. */
const MAX_CONSECUTIVE_FAILURES = 3

/** Delivery statuses that mean the router is done with a delivery. */
const TERMINAL = new Set(['ok', 'failed', 'awaiting_human', 'rate_limited'])

/** What one arm produced, plus the volatile facts kept OUT of the report. */
export interface ArmRun {
  outcome: ArmOutcome
  /** The throwaway project it ran in — metadata, never the deterministic artifact. */
  project: string
  /** The record: every project event and config event, as read back. */
  log: { events: ProjectEvent[]; configEvents: unknown[] }
}

/** Raised when an abort criterion fires; carries what had been recorded so far. */
class AbortRun extends Error {}

/**
 * Runs one arm end to end.
 *
 * The project is created, used and swept inside this function. An abort stops
 * the walk and returns what was recorded — a partial arm with its reason on the
 * record beats a thrown error that loses twenty-four tickets of evidence.
 */
export async function runArm(
  ctxRequest: Parameters<typeof newProjectClient>[0],
  spec: TriageSpec,
  arm: TriageArm,
  tickets: readonly Ticket[],
  onProgress: (line: string) => void = () => {},
): Promise<ArmRun> {
  const client = await newProjectClient(ctxRequest, `e2e-tri-${arm.id}`)
  const rows: TicketOutcome[] = []
  const outcome: ArmOutcome = { arm: arm.id, tickets: rows }
  try {
    await applyArm(client, arm)
    await setTokenCeiling(client, spec.dailyTokensHard)
    await wireArm(client, arm)

    // The instrument's seed state. Any drift from here is a harness bug, and
    // the honest response is to stop rather than measure with a moved ruler.
    const auditorSeedPrompt = (await client.getWorker(arm.auditor)).system_prompt

    let seenSeq = await maxConfigSeq(client)
    let seenRefusals = (await client.listEvents({ type: 'worker.freeze_refused', limit: 1000 })).length
    let consecutiveFailures = 0
    let spent = 0

    for (const ticket of tickets) {
      const started = Date.now()
      const { row, seq, refusals } = await runTicket(client, arm, ticket, seenSeq, seenRefusals)
      seenSeq = seq
      seenRefusals = refusals
      rows.push(row)
      onProgress(
        `   ${ticket.id} ${ticket.kind.padEnd(10)} sent ${row.route.padEnd(11)} ` +
          `want ${row.expected.padEnd(9)} auditor ${row.auditorCall.padEnd(11)} ` +
          `${Math.round((Date.now() - started) / 1000)}s`,
      )

      if (row.deliveryStatuses.includes('rate_limited')) {
        throw new AbortRun(
          `daily_tokens_hard (${spec.dailyTokensHard}) was reached at ${ticket.id} — the router is ` +
            'refusing non-interactive jobs. Abort criterion: ceiling hit.',
        )
      }
      // The same ceiling, enforced here, because the engine's gate QUEUES
      // further dispatches rather than stopping the run (TOK1 made the gate
      // live; it is still the wrong shape for "stop the experiment"). A ceiling
      // that queues is not a stop button.
      spent += row.tokens
      if (spent >= spec.dailyTokensHard) {
        throw new AbortRun(
          `the harness-side token ceiling was reached at ${ticket.id}: ${spent} tokens spent, ` +
            `daily_tokens_hard is ${spec.dailyTokensHard}. Abort criterion: ceiling hit.`,
        )
      }
      consecutiveFailures = row.deliveryStatuses.includes('failed') ? consecutiveFailures + 1 : 0
      if (consecutiveFailures > MAX_CONSECUTIVE_FAILURES) {
        throw new AbortRun(
          `${consecutiveFailures} consecutive failed deliveries, ending at ${ticket.id}. ` +
            'Abort criterion: provision failures.',
        )
      }
      const auditorNow = await client.getWorker(arm.auditor)
      if (auditorNow.system_prompt !== auditorSeedPrompt || !auditorNow.frozen) {
        throw new AbortRun(
          `the frozen route-auditor ${arm.auditor} CHANGED during ${ticket.id} ` +
            `(frozen=${auditorNow.frozen}). A successful write to the auditor is a harness bug — ` +
            'stop and fix, do not keep measuring.',
        )
      }
    }
    return { outcome, project: client.project, log: await readLog(client) }
  } catch (e) {
    if (!(e instanceof AbortRun)) throw e
    outcome.abortedAfter = rows.length
    outcome.abortReason = e.message
    onProgress(`   !! ${e.message}`)
    return {
      outcome,
      project: client.project,
      log: await readLog(client).catch(() => ({ events: [], configEvents: [] })),
    }
  } finally {
    // Port hygiene is not optional and not conditional on success.
    await client.cleanup().catch(() => {})
  }
}

/** Previews, refuses an inapplicable bundle loudly, then applies. */
async function applyArm(client: ProjectClient, arm: TriageArm): Promise<void> {
  const body: TopologyBody = {
    name: 'triage-lab',
    version: 'v1',
    answers: {
      'dispatcher-name': arm.dispatcher,
      'first-queue-name': arm.queues.billing,
      'second-queue-name': arm.queues.outage,
      'third-queue-name': arm.queues.access,
      'critic-name': arm.critic,
      'auditor-name': arm.auditor,
      'routing-rules': routingRules(arm.queues),
    },
  }
  const preview = await client.previewTopology(body)
  if (!preview.applicable) {
    throw new Error(
      `${arm.id}: triage-lab@v1 is not applicable to a fresh project — ` +
        `colliding workers ${JSON.stringify(preview.diff.colliding_workers)}, ` +
        `missing images ${JSON.stringify(preview.missing_images)}, ` +
        `missing skills ${JSON.stringify(preview.missing_skills)}`,
    )
  }
  const applied = await client.applyTopology(body)
  if (applied.event.action !== 'topology_apply') {
    throw new Error(`${arm.id}: apply returned ${applied.event.action}, wanted topology_apply`)
  }
  const auditor = applied.workers.find((w) => w.name === arm.auditor)
  if (!auditor?.frozen) {
    throw new Error(`${arm.id}: the route-auditor came back unfrozen — the instrument is not protected`)
  }
}

/**
 * Sets `daily_tokens_hard`.
 *
 * Read-modify-write, because the settings PUT is whole-object: a body that
 * merely names the one field would write every other setting back as its zero
 * value. The topology apply's SettingsPatch cannot reach this field at all —
 * zero-is-meaningful settings are unreachable through a zero-means-keep patch
 * (the T1 finding in docs/product/13's log), which is exactly why the runner
 * sets it and the topology does not.
 */
async function setTokenCeiling(client: ProjectClient, hard: number): Promise<void> {
  if (hard <= 0) throw new Error('daily_tokens_hard must be positive — an unbounded experiment has no stop button')
  const current = await client.getSettings()
  const after = await client.putSettings({
    base_image: current.base_image,
    system_prompt: current.system_prompt,
    mcp_config: current.mcp_config,
    attention_channel: current.attention_channel,
    max_concurrent_jobs: current.max_concurrent_jobs,
    daily_tokens_soft: current.daily_tokens_soft,
    daily_tokens_hard: hard,
    briefing_max_bytes: current.briefing_max_bytes,
    snapshot_ttl_days: current.snapshot_ttl_days,
  })
  if (after.daily_tokens_hard !== hard) {
    throw new Error(`daily_tokens_hard did not take: asked ${hard}, stored ${after.daily_tokens_hard}`)
  }
}

/**
 * Applies the arm's one difference — an ordinary operator mutation, made after
 * the apply so that both arms' org charts are rendered from the same topology.
 */
async function wireArm(client: ProjectClient, arm: TriageArm): Promise<void> {
  if (!arm.disableCritic) return
  const subs = await client.listSubscriptions()
  const criticSubs = subs.filter((s) => s.worker === arm.critic)
  if (criticSubs.length === 0) throw new Error(`${arm.id}: no subscription to delete for critic ${arm.critic}`)
  for (const sub of criticSubs) await client.deleteSubscription(sub.id)
  const left = (await client.listSubscriptions()).filter((s) => s.worker === arm.critic)
  if (left.length > 0) throw new Error(`${arm.id}: the critic is still subscribed after the delete`)
}

/**
 * How many jobs one dispatcher finish wakes: the three queues, plus the critic
 * when it is still subscribed.
 *
 * Stated as arithmetic rather than a constant because getting it wrong is
 * silent: too low and the sweep races three container creations, too high and
 * every ticket waits out a timeout.
 */
function followOnCount(arm: TriageArm): number {
  return 3 + (arm.disableCritic ? 0 : 1)
}

/** Drives one ticket and reads what it produced. */
async function runTicket(
  client: ProjectClient,
  arm: TriageArm,
  ticket: Ticket,
  sinceSeq: number,
  sinceRefusals: number,
): Promise<{ row: TicketOutcome; seq: number; refusals: number }> {
  const sessions: string[] = []
  try {
    // (1) The ticket goes in. Truth does not travel with it.
    const taskEvent = await client.postEvent({
      type: `${arm.dispatcher}.task`,
      text: ticketEventText({
        armTag: arm.tag,
        ticketId: ticket.id,
        ticket: ticket.text,
        workers: arm.queues,
      }),
    })
    const taskDeliveries = await settledDeliveries(client, taskEvent.id, 1)
    const dispatcherSession = taskDeliveries[0]?.session_id ?? ''
    if (dispatcherSession !== '') sessions.push(dispatcherSession)

    // (2) The deliverable — the LAST assistant message, once the job is over.
    const reply = dispatcherSession === '' ? '' : await deliverable(client, dispatcherSession)
    const statedRoute = parseStatedRoute(reply)
    const route = canonicalRoute(statedRoute, arm.queues)

    // (3) The fan-out and the critic are part of THIS ticket, not the next one.
    if (dispatcherSession !== '') {
      sessions.push(...(await settleFollowOn(client, dispatcherSession, followOnCount(arm))))
    }

    const writes = await promptWritesSince(client, sinceSeq)
    const refusalRows = await client.listEvents({ type: 'worker.freeze_refused', limit: 1000 })

    // (4) Decision and truth reach the frozen comparator, together, once.
    const auditEvent = await client.postEvent({
      type: `${arm.auditor}.task`,
      text: auditEventText({
        ticketId: ticket.id,
        reply,
        route,
        statedRoute,
        truthRoute: ticket.route,
        truthExplanation: ticket.explanation,
      }),
    })
    const auditDeliveries = await settledDeliveries(client, auditEvent.id, 1)
    const auditorSession = auditDeliveries[0]?.session_id ?? ''
    if (auditorSession !== '') sessions.push(auditorSession)
    const auditorReply = auditorSession === '' ? '' : await deliverable(client, auditorSession)

    let tokens = 0
    for (const id of sessions) tokens += await sessionTokens(client, id)

    return {
      row: {
        id: ticket.id,
        kind: ticket.kind,
        expected: ticket.route,
        decoy: ticket.decoy,
        route,
        statedRoute,
        reply,
        auditorCall: parseAuditorCall(auditorReply),
        auditorReply,
        deliveryStatuses: [...taskDeliveries, ...auditDeliveries].map((d) => d.status).sort(),
        promptWrites: writes.records,
        freezeRefusals: refusalRows.length - sinceRefusals,
        tokens,
      },
      seq: Math.max(sinceSeq, writes.maxSeq),
      refusals: refusalRows.length,
    }
  } finally {
    // Per ticket, not per arm: six sessions a ticket would exhaust the pool
    // long before the arm ended.
    for (const id of sessions) await client.deleteSession(id).catch(() => {})
  }
}

/** Waits for `want` deliveries of one event to reach a terminal status. */
async function settledDeliveries(client: ProjectClient, eventId: string, want: number): Promise<EventDelivery[]> {
  return client.waitForDeliveries((rows) => rows.length >= want && rows.every((d) => TERMINAL.has(d.status)), {
    event_id: eventId,
    timeoutMs: DELIVERY_TIMEOUT_MS,
  })
}

/**
 * The deliverable of a finished job: its LAST assistant message.
 *
 * Waits for the job's `worker.finished` first, so "last" means last and not
 * "latest so far". Everything before it — the ticket, the intermediate
 * reasoning, the charter's own ROUTE-TO template — is transcript, and grading
 * transcript is how a harness scores itself (B1's live foot-gun).
 */
async function deliverable(client: ProjectClient, sessionId: string): Promise<string> {
  await waitForFinish(client, sessionId)
  const messages = await poll(
    () => client.listMessages(sessionId),
    (rows) => rows.some((m) => m.role === 'assistant' && m.content.trim() !== ''),
    EVENT_TIMEOUT_MS,
    `an assistant reply in session ${sessionId}`,
  )
  const assistant = messages.filter((m) => m.role === 'assistant' && m.content.trim() !== '')
  return assistant.length === 0 ? '' : assistant[assistant.length - 1].content
}

/** Waits for one session's `worker.finished` and returns it. */
async function waitForFinish(client: ProjectClient, sessionId: string): Promise<ProjectEvent> {
  const rows = await client.waitForEvents((events) => events.some((e) => e.envelope.session_id === sessionId), {
    type: 'worker.finished',
    timeoutMs: EVENT_TIMEOUT_MS,
  })
  return rows.find((e) => e.envelope.session_id === sessionId)!
}

/**
 * Waits for the jobs that react to one session's finish, and returns their
 * session ids so they can be swept.
 *
 * This is what makes the ticket boundary real: without it the next ticket races
 * the critic's rewrite, and the race is intermittent, which is the worst kind of
 * wrong.
 */
async function settleFollowOn(client: ProjectClient, sessionId: string, want: number): Promise<string[]> {
  if (want <= 0) return []
  const trigger = await waitForFinish(client, sessionId)
  const deliveries = await client.waitForDeliveries(
    (rows) => rows.length >= want && rows.every((d) => TERMINAL.has(d.status)),
    { event_id: trigger.id, timeoutMs: DELIVERY_TIMEOUT_MS },
  )
  const ids: string[] = []
  for (const d of deliveries) {
    if (d.session_id === '') continue
    ids.push(d.session_id)
    await waitForFinish(client, d.session_id)
  }
  return ids
}

/** The highest config-log sequence number the project holds right now. */
async function maxConfigSeq(client: ProjectClient): Promise<number> {
  const rows = await client.configEvents({ limit: 1 })
  return rows.length === 0 ? 0 : rows[0].seq
}

/**
 * Prompt rewrites logged after `sinceSeq`, oldest first.
 *
 * From the config log rather than the worker row, because the log is the record
 * of WHY: `rationale` is the derived-rubric artifact and exists nowhere else.
 */
async function promptWritesSince(
  client: ProjectClient,
  sinceSeq: number,
): Promise<{ records: PromptWriteRecord[]; maxSeq: number }> {
  const rows = await client.configEvents({ action: 'worker_prompt_write', limit: 200 })
  const fresh = rows.filter((r) => r.seq > sinceSeq).sort((a, b) => a.seq - b.seq)
  return {
    records: fresh.map((r) => ({
      by: r.actor_worker,
      target: String((r.payload as { name?: unknown }).name ?? ''),
      rationale: r.rationale,
    })),
    maxSeq: fresh.length === 0 ? sinceSeq : fresh[fresh.length - 1].seq,
  }
}

/**
 * Tokens billed to one session, read off the stored query-event stream.
 *
 * The in-image harness reports usage on its `query_complete` envelope, nested
 * and camelCase:
 *
 *   events[i].events[j] = {"type":"query_complete","data":{"usage":{"inputTokens":10,"outputTokens":6}}}
 *
 * Both spellings are accepted so that a fix upstream does not silently zero this
 * column (TOK1 fixed the engine's three readers against a captured envelope; the
 * runner keeps its own total because the engine gate queues rather than aborts).
 * The walk stops descending at the first object that carries usage, so a future
 * envelope repeating it nested cannot double-count.
 */
async function sessionTokens(client: ProjectClient, sessionId: string): Promise<number> {
  const resp = await client.raw('GET', `/agent/session/${encodeURIComponent(sessionId)}/query-events`).catch(() => null)
  if (!resp?.ok()) return 0
  const body = (await resp.json().catch(() => null)) as unknown
  let total = 0
  const walk = (node: unknown): void => {
    if (Array.isArray(node)) {
      node.forEach(walk)
      return
    }
    if (!node || typeof node !== 'object') return
    const r = node as Record<string, unknown>
    const counted = [
      // All separately-billed components (RD2): the two cache fields carry
      // most of a cached turn's input bill and were silently uncounted.
      'inputTokens', 'outputTokens', 'cacheCreationInputTokens', 'cacheReadInputTokens',
      'input_tokens', 'output_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens',
    ].filter(
      (k) => typeof r[k] === 'number',
    )
    if (counted.length > 0) {
      for (const k of counted) total += r[k] as number
      return
    }
    Object.values(r).forEach(walk)
  }
  walk(body)
  return total
}

/** The record: everything the project logged, as read back. */
async function readLog(client: ProjectClient): Promise<ArmRun['log']> {
  return {
    events: await client.listEvents({ limit: 1000 }),
    configEvents: await client.configEvents({ limit: 1000 }),
  }
}
