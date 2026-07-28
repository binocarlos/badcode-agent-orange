// runner.ts — the half of the calibration rig that touches the stack.
//
// One job: turn a CalArm into an ArmOutcome. Apply hypothesis-lab@v1 to a fresh
// run-scoped project, set the token ceiling, wire the arm's one difference, then
// walk the hypotheses: dataset event in, verdict out, check event in, checker's
// call out. Then sweep.
//
// Shaped by the same scar tissue as ../runner.ts, plus three of its own:
//
//   * **Poll, never sleep.** Every wait is on a delivery row, an event row or a
//     config record.
//   * **A hypothesis is not over when the investigator stops.** In arm A the
//     critic's rewrite lands in a job that STARTS after the investigator
//     finishes. Reading the config log at investigator-finish races it, and the
//     next hypothesis would then run against a prompt that was still being
//     written. (C1's round-boundary lesson, docs/product/13's log.)
//   * **Sessions are swept per hypothesis, not per arm.** Thirty hypotheses ×
//     three sessions each × three arms is 270 containers, against a pool of
//     100. Sweeping at the end of the arm is the same as not sweeping.
//   * **Parse the deliverable, not the transcript.** The verdict comes from the
//     last assistant message of the investigator's session. `worker.finished`
//     text is the whole exchange and contains the harness's own instructions.
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
import { loadDoctrine } from './doctrine'
import type { ArmOutcome, HypothesisOutcome } from './metrics'
import type { CalArm, CalibrationSpec } from './spec'
import { checkEventText, datasetEventText } from './text'
import type { Hypothesis } from './truths'
import { parseCheckerCall, parseVerdict } from './verdict'

/** Generous: mock is fast, but every job is a real container. */
const DELIVERY_TIMEOUT_MS = 240_000
const EVENT_TIMEOUT_MS = 180_000

/** Runbook §4: stop after this many provision failures in a row. */
const MAX_CONSECUTIVE_FAILURES = 3

/** Delivery statuses that mean the router is done with a delivery. */
const TERMINAL = new Set(['ok', 'failed', 'awaiting_human', 'rate_limited'])

/** What one arm produced, plus the volatile facts kept OUT of the report. */
export interface ArmRun {
  outcome: ArmOutcome
  /** The throwaway project it ran in — metadata, never the deterministic artifact. */
  project: string
  /** The runbook §4 record: every project event and config event, as read back. */
  log: { events: ProjectEvent[]; configEvents: unknown[] }
}

/** Raised when an abort criterion fires; carries what had been recorded so far. */
class AbortRun extends Error {}

/**
 * Runs one arm end to end.
 *
 * The project is created, used and swept inside this function. An abort stops
 * the walk and returns what was recorded — a partial arm with its reason on the
 * record beats a thrown error that loses thirty hypotheses of evidence.
 */
export async function runArm(
  ctxRequest: Parameters<typeof newProjectClient>[0],
  spec: CalibrationSpec,
  arm: CalArm,
  hypotheses: readonly Hypothesis[],
  onProgress: (line: string) => void = () => {},
): Promise<ArmRun> {
  const client = await newProjectClient(ctxRequest, `e2e-cal-${arm.id}`)
  const rows: HypothesisOutcome[] = []
  const outcome: ArmOutcome = { arm: arm.id, hypotheses: rows }
  try {
    await applyArm(client, arm, spec)
    await setTokenCeiling(client, spec.dailyTokensHard)
    await wireArm(client, arm)

    // The instrument's seed state. Any drift from here is a harness bug, and
    // runbook §4 says stop rather than measure with a moved ruler.
    const checkerSeedPrompt = (await client.getWorker(arm.checker)).system_prompt

    let seenSeq = await maxConfigSeq(client)
    let seenRefusals = (await client.listEvents({ type: 'worker.freeze_refused', limit: 1000 })).length
    let consecutiveFailures = 0
    let spent = 0

    for (const hypothesis of hypotheses) {
      const started = Date.now()
      const { row, seq, refusals } = await runHypothesis(client, arm, hypothesis, seenSeq, seenRefusals)
      seenSeq = seq
      seenRefusals = refusals
      rows.push(row)
      onProgress(
        `   ${hypothesis.id} ${hypothesis.kind.padEnd(14)} said ${row.verdict.padEnd(11)} ` +
          `want ${row.expected.padEnd(9)} checker ${row.checkerCall.padEnd(11)} ` +
          `${Math.round((Date.now() - started) / 1000)}s`,
      )

      if (row.deliveryStatuses.includes('rate_limited')) {
        throw new AbortRun(
          `daily_tokens_hard (${spec.dailyTokensHard}) was reached at ${hypothesis.id} — the router is ` +
            'refusing non-interactive jobs. Runbook §4 abort criterion: ceiling hit.',
        )
      }
      // The same ceiling, enforced here, because the project-level one cannot
      // fire: the router reads CountProjectTokensSince, whose SQL does not match
      // the shape the harness actually stores (see sessionTokens). The engine
      // gate is live since TOK1, but it QUEUES further dispatches rather than
      // stopping the run — so this arithmetic stays as the abort mechanism,
      // checked every hypothesis.
      spent += row.tokens
      if (spent >= spec.dailyTokensHard) {
        throw new AbortRun(
          `the harness-side token ceiling was reached at ${hypothesis.id}: ${spent} tokens spent, ` +
            `daily_tokens_hard is ${spec.dailyTokensHard}. Runbook §4 abort criterion: ceiling hit. ` +
            '(The engine-side ceiling queues rather than aborts; this harness-side one stops the run.)',
        )
      }
      consecutiveFailures = row.deliveryStatuses.includes('failed') ? consecutiveFailures + 1 : 0
      if (consecutiveFailures > MAX_CONSECUTIVE_FAILURES) {
        throw new AbortRun(
          `${consecutiveFailures} consecutive failed deliveries, ending at ${hypothesis.id}. ` +
            'Runbook §4 abort criterion: provision failures.',
        )
      }
      const checkerNow = await client.getWorker(arm.checker)
      if (checkerNow.system_prompt !== checkerSeedPrompt || !checkerNow.frozen) {
        throw new AbortRun(
          `the frozen fact-checker ${arm.checker} CHANGED during ${hypothesis.id} ` +
            `(frozen=${checkerNow.frozen}). Runbook §4: a successful write to the fact-checker is a ` +
            'harness bug — stop and fix, do not keep measuring.',
        )
      }
    }
    return { outcome, project: client.project, log: await readLog(client) }
  } catch (e) {
    if (!(e instanceof AbortRun)) throw e
    outcome.abortedAfter = rows.length
    outcome.abortReason = e.message
    onProgress(`   !! ${e.message}`)
    return { outcome, project: client.project, log: await readLog(client).catch(() => ({ events: [], configEvents: [] })) }
  } finally {
    // Port hygiene is not optional and not conditional on success.
    await client.cleanup().catch(() => {})
  }
}

/** Previews, refuses an inapplicable bundle loudly, then applies. */
async function applyArm(client: ProjectClient, arm: CalArm, spec: CalibrationSpec): Promise<void> {
  const body: TopologyBody = {
    name: 'hypothesis-lab',
    version: 'v1',
    answers: {
      'investigator-name': arm.investigator,
      'critic-name': arm.critic,
      'checker-name': arm.checker,
      'covariates-hint': spec.covariatesHint,
    },
  }
  const preview = await client.previewTopology(body)
  if (!preview.applicable) {
    throw new Error(
      `${arm.id}: hypothesis-lab@v1 is not applicable to a fresh project — ` +
        `colliding workers ${JSON.stringify(preview.diff.colliding_workers)}, ` +
        `missing images ${JSON.stringify(preview.missing_images)}, ` +
        `missing skills ${JSON.stringify(preview.missing_skills)}`,
    )
  }
  const applied = await client.applyTopology(body)
  if (applied.event.action !== 'topology_apply') {
    throw new Error(`${arm.id}: apply returned ${applied.event.action}, wanted topology_apply`)
  }
  const checker = applied.workers.find((w) => w.name === arm.checker)
  if (!checker?.frozen) {
    throw new Error(`${arm.id}: the fact-checker came back unfrozen — the instrument is not protected`)
  }
}

/**
 * Sets `daily_tokens_hard` (runbook §4's rate-limit guard).
 *
 * Read-modify-write, because the settings PUT is whole-object: a body that
 * merely names the one field would write every other setting back as its zero
 * value. The topology apply's SettingsPatch cannot reach this field at all —
 * zero-is-meaningful settings are unreachable through a zero-means-keep patch
 * (the T1 finding in docs/product/13's log), which is exactly why the runner
 * sets it and the topology does not.
 */
async function setTokenCeiling(client: ProjectClient, hard: number): Promise<void> {
  if (hard <= 0) throw new Error('daily_tokens_hard must be positive — runbook §4 requires a ceiling')
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
 * the apply so that every arm's org chart is rendered from the same topology.
 */
async function wireArm(client: ProjectClient, arm: CalArm): Promise<void> {
  if (arm.doctrine) await applyDoctrine(client, arm)
  if (arm.disableCritic) {
    const subs = await client.listSubscriptions()
    const criticSubs = subs.filter((s) => s.worker === arm.critic)
    if (criticSubs.length === 0) throw new Error(`${arm.id}: no subscription to delete for critic ${arm.critic}`)
    for (const sub of criticSubs) await client.deleteSubscription(sub.id)
    const left = (await client.listSubscriptions()).filter((s) => s.worker === arm.critic)
    if (left.length > 0) throw new Error(`${arm.id}: the critic is still subscribed after the delete`)
  }
  if (arm.criticPromptOverride) {
    const stored = await client.getWorker(arm.critic)
    await client.putWorker(arm.critic, {
      description: stored.description,
      system_prompt: arm.criticPromptOverride,
      mcp_config: stored.mcp_config,
      image: stored.image,
      max_instances: stored.max_instances,
      enabled: stored.enabled,
      frozen: stored.frozen,
      ...(stored.briefing != null ? { briefing: stored.briefing } : {}),
    })
    const after = await client.getWorker(arm.critic)
    if (after.system_prompt !== arm.criticPromptOverride) {
      throw new Error(`${arm.id}: the sham critic prompt did not take`)
    }
  }
}

/**
 * Writes the arm's doctrine block into the project prompt (DR1, doc 20 §3).
 *
 * The same whole-object read-modify-write `setTokenCeiling` uses, and for the
 * same reason: `PutProjectSettings` replaces the row, zero values included, so
 * a body naming only `system_prompt` would blank the ceiling that was just set.
 * Every other field is carried through explicitly — including
 * `daily_tokens_hard`, which is why this runs AFTER setTokenCeiling and reads
 * fresh settings rather than reusing an earlier snapshot.
 *
 * Nothing else is needed to make this an auditable mutation: the settings PUT
 * writes a `project_settings_put` config event in the same transaction, so the
 * arm's difference is on the project's own record without the rig logging
 * anything itself.
 *
 * The read-back is a storage check and is treated as one — it catches a
 * refused or silently-truncated write early, but it is NOT the delivery
 * assertion. Delivery (the block reaching a COMPOSED prompt) is proved by the
 * mock script's rule on `DOCTRINE_DELIVERY_PHRASE`, because reading back a
 * stored value proves storage and nothing more.
 */
async function applyDoctrine(client: ProjectClient, arm: CalArm): Promise<void> {
  const version = arm.doctrine!
  const block = loadDoctrine(version)
  const current = await client.getSettings()
  if (current.system_prompt.trim() !== '') {
    throw new Error(
      `${arm.id}: the project prompt is not empty before the doctrine write (${current.system_prompt.length} bytes). ` +
        'Overwriting real content would make the A/B compare doctrine against something else — ' +
        'doc 20 §3 says such a project needs the block APPENDED, deliberately, not written over.',
    )
  }
  const after = await client.putSettings({
    base_image: current.base_image,
    system_prompt: block,
    mcp_config: current.mcp_config,
    attention_channel: current.attention_channel,
    max_concurrent_jobs: current.max_concurrent_jobs,
    daily_tokens_soft: current.daily_tokens_soft,
    daily_tokens_hard: current.daily_tokens_hard,
    briefing_max_bytes: current.briefing_max_bytes,
    snapshot_ttl_days: current.snapshot_ttl_days,
  })
  if (after.system_prompt !== block) {
    throw new Error(
      `${arm.id}: the doctrine-${version} block did not take — stored ${after.system_prompt.length} bytes, ` +
        `wrote ${block.length}`,
    )
  }
  if (after.daily_tokens_hard !== current.daily_tokens_hard) {
    throw new Error(
      `${arm.id}: the doctrine write moved daily_tokens_hard from ${current.daily_tokens_hard} to ` +
        `${after.daily_tokens_hard} — the settings PUT is whole-object and a field was dropped`,
    )
  }
}

/** Drives one hypothesis and reads what it produced. */
async function runHypothesis(
  client: ProjectClient,
  arm: CalArm,
  hypothesis: Hypothesis,
  sinceSeq: number,
  sinceRefusals: number,
): Promise<{ row: HypothesisOutcome; seq: number; refusals: number }> {
  const sessions: string[] = []
  try {
    // (1) The dataset goes in. Truth does not travel with it.
    const taskEvent = await client.postEvent({
      type: `${arm.investigator}.task`,
      text: datasetEventText({ hypothesisId: hypothesis.id, csv: hypothesis.csv, rows: hypothesis.n }),
    })
    const taskDeliveries = await settledDeliveries(client, taskEvent.id, 1)
    const investigatorSession = taskDeliveries[0]?.session_id ?? ''
    if (investigatorSession !== '') sessions.push(investigatorSession)

    // (2) The deliverable — the LAST assistant message, once the job is over.
    const conclusion = investigatorSession === '' ? '' : await deliverable(client, investigatorSession)
    const verdict = parseVerdict(conclusion)

    // (3) The critic's job is part of the hypothesis, not of the next one.
    if (!arm.disableCritic && investigatorSession !== '') {
      const criticSessions = await settleFollowOn(client, investigatorSession, 1)
      sessions.push(...criticSessions)
    }

    const writes = await promptWritesSince(client, sinceSeq)
    const refusalRows = await client.listEvents({ type: 'worker.freeze_refused', limit: 1000 })

    // (4) Conclusion and truth reach the frozen comparator, together, once.
    const checkEvent = await client.postEvent({
      type: `${arm.checker}.task`,
      text: checkEventText({
        hypothesisId: hypothesis.id,
        conclusion,
        verdict,
        truthEffect: hypothesis.truthEffect,
        truthExplanation: hypothesis.truthExplanation,
      }),
    })
    const checkDeliveries = await settledDeliveries(client, checkEvent.id, 1)
    const checkerSession = checkDeliveries[0]?.session_id ?? ''
    if (checkerSession !== '') sessions.push(checkerSession)
    const checkerReply = checkerSession === '' ? '' : await deliverable(client, checkerSession)

    let tokens = 0
    for (const id of sessions) tokens += await sessionTokens(client, id)

    return {
      row: {
        id: hypothesis.id,
        kind: hypothesis.kind,
        expected: hypothesis.expected,
        truthEffect: hypothesis.truthEffect,
        verdict,
        conclusion,
        checkerCall: parseCheckerCall(checkerReply),
        checkerReply,
        deliveryStatuses: [...taskDeliveries, ...checkDeliveries].map((d) => d.status).sort(),
        promptWrites: writes.records,
        freezeRefusals: refusalRows.length - sinceRefusals,
        tokens,
      },
      seq: Math.max(sinceSeq, writes.maxSeq),
      refusals: refusalRows.length,
    }
  } finally {
    // Per hypothesis, not per arm: 30 × 3 sessions would exhaust the pool
    // long before the arm ended.
    for (const id of sessions) await client.deleteSession(id).catch(() => {})
  }
}

/** Waits for `want` deliveries of one event to reach a terminal status. */
async function settledDeliveries(client: ProjectClient, eventId: string, want: number): Promise<EventDelivery[]> {
  return client.waitForDeliveries(
    (rows) => rows.length >= want && rows.every((d) => TERMINAL.has(d.status)),
    { event_id: eventId, timeoutMs: DELIVERY_TIMEOUT_MS },
  )
}

/**
 * The deliverable of a finished job: its LAST assistant message.
 *
 * Waits for the job's `worker.finished` first, so "last" means last and not
 * "latest so far". Everything before it — the CSV, the intermediate reasoning,
 * the harness's own instructions — is transcript, and grading transcript is how
 * a harness scores itself (B1's live foot-gun).
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
 * This is what makes the hypothesis boundary real: without it the next dataset
 * event races the critic's rewrite, and the race is intermittent, which is the
 * worst kind of wrong.
 */
async function settleFollowOn(client: ProjectClient, sessionId: string, want: number): Promise<string[]> {
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
 * of WHY: `rationale` is runbook §3.4's derived-rubric artifact and exists
 * nowhere else.
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
 * **This runner counts tokens itself because nothing else in the stack does.**
 * The in-image harness reports usage on its `query_complete` envelope, nested
 * and camelCase:
 *
 *   events[i].events[j] = {"type":"query_complete","data":{"usage":{"inputTokens":10,"outputTokens":6}}}
 *
 * History: this rig found the engine's three token readers querying a shape
 * (`events->0->>'input_tokens'`) that no stored row carries, so the
 * project-level ceiling could not fire — TOK1 fixed the readers against a
 * captured envelope (`go/agentdb/token_usage.go`), and the `daily_tokens_hard`
 * gate is now live and e2e-tested. The runner STILL keeps its own running
 * total and aborts on it: a belt-and-braces ceiling costs one addition per
 * hypothesis, and the engine gate queues rather than aborts, which is the
 * wrong shape for runbook §4's "stop the experiment" criterion.
 *
 * Both spellings are accepted so that a fix upstream does not silently zero
 * this column. The walk stops descending at the first object that carries
 * usage, so a future envelope repeating it nested cannot double-count.
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
    const counted = ['inputTokens', 'outputTokens', 'input_tokens', 'output_tokens'].filter(
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

/** The runbook §4 record: everything the project logged, as read back. */
async function readLog(client: ProjectClient): Promise<ArmRun['log']> {
  return {
    events: await client.listEvents({ limit: 1000 }),
    configEvents: await client.configEvents({ limit: 1000 }),
  }
}
