// runner.ts — the half of the rig that touches the stack.
//
// One job: turn an ArmSpec into a RunOutcome. Apply the topology to a fresh
// run-scoped project, drive the task's rounds by emitting events, wait for the
// happens-after records the router writes, and read the outcome out of the
// event and config logs. Then delete everything.
//
// Standing traps this file is shaped by (docs/product/13-work-plan-self-
// improvement.md):
//
//   * **Poll, never sleep.** Every wait below is a poll on a delivery row, an
//     event row or a config record. There is not one timer in here.
//   * **A round is not over when the actor stops.** In a critic topology the
//     rewrite lands in a job that starts *after* the actor finishes. Reading
//     the config log before that job settles would score the arm on a rewrite
//     that had not happened yet, and would do so intermittently — the worst
//     kind of wrong. `followOnDeliveries` is what makes the round boundary
//     real.
//   * **Sessions hold a host port.** `client.cleanup()` runs in a `finally`,
//     so an arm that throws still releases its containers. The rig also
//     measures the pool at the end (compare.ts) and says what it found.
//
// Nothing here computes a metric or knows what "better" means; that is
// report.ts, which has no idea a stack exists.

import { newProjectClient, poll, type EventDelivery, type ProjectClient, type TopologyBody } from '../helpers/api'
import type { ArmSpec, RoundObservation, TaskSpec } from './spec'
import type { PromptWriteRecord, RoundOutcome, RunOutcome } from './report'

/** How long a single delivery is given to settle. Generous: mock, but real containers. */
const DELIVERY_TIMEOUT_MS = 240_000
const EVENT_TIMEOUT_MS = 180_000

/** What one arm run produced, plus the volatile facts kept OUT of the report. */
export interface ArmRun {
  outcome: RunOutcome
  /** The throwaway project it ran in — metadata, never the deterministic artifact. */
  project: string
}

/**
 * Runs one repetition of one arm end to end.
 *
 * The project is created, used and swept inside this function; nothing about it
 * outlives the call except the outcome table and the project's name (for the
 * metadata file, so a failed run can be inspected before the sweep).
 */
export async function runArm(
  ctxRequest: Parameters<typeof newProjectClient>[0],
  spec: TaskSpec,
  arm: ArmSpec,
  repetition: number,
): Promise<ArmRun> {
  const client = await newProjectClient(ctxRequest, `e2e-xp-${arm.id}`)
  try {
    await applyArm(client, arm)
    if (arm.afterApply) await arm.afterApply(client)

    const observations: RoundObservation[] = []
    const rounds: RoundOutcome[] = []
    let seenSeq = await maxConfigSeq(client)

    for (let i = 0; i < spec.rounds.length; i++) {
      const { observation, seq } = await runRound(client, arm, spec.rounds[i], i + 1, seenSeq)
      seenSeq = seq
      const previous = observations[observations.length - 1]
      const properties: Record<string, boolean> = {}
      for (const p of spec.properties) properties[p.id] = p.holds(observation, previous)
      observations.push(observation)
      rounds.push({
        round: observation.round,
        taskText: observation.taskText,
        deliveryStatuses: observation.deliveryStatuses,
        promptWrites: observation.promptWrites,
        output: observation.output,
        workerPromptAfter: observation.workerPromptAfter,
        properties,
      })
    }
    return { outcome: { arm: arm.id, repetition, rounds }, project: client.project }
  } finally {
    // Port hygiene is not optional and not conditional on success.
    await client.cleanup().catch(() => {})
  }
}

/** Previews, refuses an inapplicable bundle loudly, then applies. */
async function applyArm(client: ProjectClient, arm: ArmSpec): Promise<void> {
  const body: TopologyBody = { name: arm.topology, version: arm.version, answers: arm.answers }
  const preview = await client.previewTopology(body)
  if (!preview.applicable) {
    throw new Error(
      `${arm.id}: ${arm.topology}@${arm.version} is not applicable to a fresh project — ` +
        `colliding workers ${JSON.stringify(preview.diff.colliding_workers)}, ` +
        `missing images ${JSON.stringify(preview.missing_images)}, ` +
        `missing skills ${JSON.stringify(preview.missing_skills)}`,
    )
  }
  const applied = await client.applyTopology(body)
  if (applied.event.action !== 'topology_apply') {
    throw new Error(`${arm.id}: apply returned ${applied.event.action}, wanted topology_apply`)
  }
}

/**
 * Drives one round and reads what it produced.
 *
 * Returns the highest config-log sequence number it observed, which becomes the
 * next round's floor: rewrites are attributed to the round they landed in, and
 * a round can never re-count its predecessor's.
 */
async function runRound(
  client: ProjectClient,
  arm: ArmSpec,
  text: string,
  round: number,
  sinceSeq: number,
): Promise<{ observation: RoundObservation; seq: number }> {
  const want = arm.deliveriesPerRound ?? 1
  const event = await client.postEvent({ type: arm.eventType, text })
  const deliveries = await client.waitForDeliveries(
    (rows) => rows.length >= want && rows.every((d) => d.status === 'ok' && d.session_id !== ''),
    { event_id: event.id, timeoutMs: DELIVERY_TIMEOUT_MS },
  )

  const primary = await primaryDelivery(client, arm, deliveries)
  const output = await assistantReply(client, primary.session_id)

  // The round is over only once whatever reacts to the primary worker has also
  // finished — see the header note on round boundaries.
  const followOn = arm.followOnDeliveries ?? 0
  if (followOn > 0) await settleFollowOn(client, primary.session_id, followOn)

  const writes = await promptWritesSince(client, sinceSeq)
  const workerPromptAfter = (await client.getWorker(arm.primaryWorker)).system_prompt

  return {
    observation: {
      round,
      taskText: text,
      deliveryStatuses: deliveries.map((d) => d.status).sort(),
      output,
      promptWrites: writes.records,
      workerPromptAfter,
    },
    seq: Math.max(sinceSeq, writes.maxSeq),
  }
}

/**
 * The delivery belonging to the arm's primary worker.
 *
 * Joined through the subscription rows rather than assumed to be first: a
 * broadcast topology settles several deliveries for one event and the order the
 * router writes them in is not a contract.
 */
async function primaryDelivery(
  client: ProjectClient,
  arm: ArmSpec,
  deliveries: EventDelivery[],
): Promise<EventDelivery> {
  const subs = await client.listSubscriptions()
  const ids = new Set(subs.filter((s) => s.worker === arm.primaryWorker).map((s) => s.id))
  const found = deliveries.find((d) => ids.has(d.subscription_id))
  if (found) return found
  if (deliveries.length === 1) return deliveries[0]
  throw new Error(
    `${arm.id}: no delivery for primary worker ${arm.primaryWorker} among ` +
      `${deliveries.map((d) => d.subscription_id).join(', ')}`,
  )
}

/** The concatenated assistant reply of a job session, from its stored transcript. */
async function assistantReply(client: ProjectClient, sessionId: string): Promise<string> {
  const messages = await poll(
    () => client.listMessages(sessionId),
    (rows) => rows.some((m) => m.role === 'assistant' && m.content.trim() !== ''),
    EVENT_TIMEOUT_MS,
    `an assistant reply in session ${sessionId}`,
  )
  return messages
    .filter((m) => m.role === 'assistant')
    .map((m) => m.content)
    .join('\n')
}

/**
 * Waits for the `worker.finished` of one session, its follow-on deliveries to
 * settle `ok`, and then for each of THOSE sessions to finish too.
 *
 * The last step is what stops the next round racing an in-flight critic. The
 * session id is the join key throughout — "the first worker.finished" is
 * ambiguous the moment two workers run.
 */
async function settleFollowOn(client: ProjectClient, sessionId: string, want: number): Promise<void> {
  const finished = await client.waitForEvents(
    (rows) => rows.some((e) => e.envelope.session_id === sessionId),
    { type: 'worker.finished', timeoutMs: EVENT_TIMEOUT_MS },
  )
  const trigger = finished.find((e) => e.envelope.session_id === sessionId)!
  const deliveries = await client.waitForDeliveries(
    (rows) => rows.length >= want && rows.every((d) => d.status === 'ok'),
    { event_id: trigger.id, timeoutMs: DELIVERY_TIMEOUT_MS },
  )
  for (const d of deliveries) {
    await client.waitForEvents((rows) => rows.some((e) => e.envelope.session_id === d.session_id), {
      type: 'worker.finished',
      timeoutMs: EVENT_TIMEOUT_MS,
    })
  }
}

/** The highest config-log sequence number the project holds right now. */
async function maxConfigSeq(client: ProjectClient): Promise<number> {
  const rows = await client.configEvents({ limit: 1 })
  return rows.length === 0 ? 0 : rows[0].seq
}

/**
 * Prompt rewrites logged after `sinceSeq`, oldest first.
 *
 * Read from the config log rather than from the worker row because the log is
 * the record of WHY: `rationale` is what distinguishes a genuine critic from
 * the sham control, and it exists nowhere else.
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
