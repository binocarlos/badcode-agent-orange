import { test, expect } from '@playwright/test'
import {
  newProjectClient,
  poll,
  type ProjectClient,
  type TopologyBody,
  type TopologyPreview,
} from '../helpers/api'
import { waitForConfigAction } from '../helpers/configlog'

// Topology seeds T4–T7 (docs/product/13-work-plan-self-improvement.md): the
// first four org charts in the library — solo (control 1), actor-critic (4),
// supervisor (5), frozen-scorer (12) — each proven end to end against the
// running stack:
//
//   (a) preview with fixed answers → the diff is exactly the org chart the
//       seed promises, and it is applicable on a fresh project;
//   (b) apply → the read-back org chart matches the preview, and the config
//       log holds the `topology_apply` bracket naming topology@version;
//   (c) one round runs in mock: the seed's inbound event is emitted, the
//       deliveries settle, and the scripted behaviour proves the wiring —
//       not just the rows — landed.
//
// The mock script is e2e/mock-scripts/topologies.json and the constants below
// must agree with it byte for byte. All the standing traps apply: rules are
// partitioned by worker name with critics ABOVE actors (a critic's request
// contains the actor's transcript), the supervisor's specialists are keyed on
// their unique identity phrase ("You are tp6-hand-N" — the dispatcher's
// roster lists names but never that phrase), and every name carries a
// per-seed prefix (tp4-/tp5-/tp6-/tp7-) with no name a substring of another.
//
// Run with the script loaded (the tests skip without it):
//   ./e2e/run-stack-e2e.sh test mock --mock-script e2e/mock-scripts/topologies.json -- e2e/features/topologies.stack.spec.ts

const NEEDS_SCRIPT =
  'needs the scripted model: ./e2e/run-stack-e2e.sh test mock --mock-script e2e/mock-scripts/topologies.json -- e2e/features/topologies.stack.spec.ts'

// ── Shared helpers ──────────────────────────────────────────────────────────

/**
 * Applies a previewed topology and asserts the project's READ-BACK org chart
 * — workers, subscriptions, schedules, through the ordinary list routes, not
 * the apply response — matches the preview, and that the `topology_apply`
 * bracket landed naming topology@version. Returns the bracket record.
 */
async function applyAndVerify(client: ProjectClient, body: TopologyBody, preview: TopologyPreview) {
  const result = await client.applyTopology(body)
  expect(result.event.action).toBe('topology_apply')

  // Read back through the same routes an operator's UI uses: what the project
  // now CONTAINS is what the preview promised.
  const workers = await client.listWorkers()
  expect(workers.map((w) => w.name).sort()).toEqual([...preview.diff.new_workers].sort())
  for (const bw of preview.bundle.workers) {
    const stored = workers.find((w) => w.name === bw.name)!
    expect(stored, `worker ${bw.name} must exist after apply`).toBeTruthy()
    expect(stored.system_prompt).toBe(bw.system_prompt)
    expect(stored.enabled).toBe(bw.enabled)
    expect(stored.frozen).toBe(bw.frozen)
    expect(stored.project).toBe(client.project)
  }

  const subs = await client.listSubscriptions()
  const subShape = (s: { event_type: string; worker: string; filter?: Record<string, unknown> | null }) =>
    `${s.event_type}|${s.worker}|${JSON.stringify(s.filter ?? {})}`
  expect(subs.map(subShape).sort()).toEqual(preview.bundle.subscriptions.map(subShape).sort())
  for (const s of subs) expect(s.enabled).toBe(true)

  const schedules = await client.listSchedules()
  const schedShape = (s: { cron: string; worker: string; input: string }) => `${s.cron}|${s.worker}|${s.input}`
  expect(schedules.map(schedShape).sort()).toEqual(preview.bundle.schedules.map(schedShape).sort())

  // The bracket is in the log, naming what was applied and what was answered.
  const bracket = await waitForConfigAction(client, 'topology_apply', 30_000)
  expect(bracket.id).toBe(result.event.id)
  expect(bracket.payload.topology).toBe(`${body.name}@${body.version}`)
  // A human applied this over plain JWT — no worker actor on the record.
  expect(bracket.actor_worker).toBe('')
  return bracket
}

/**
 * Emits one event and waits for its deliveries to settle `ok`, returning them.
 * Assert-on-polls, never sleeps: the delivery rows are the happens-after
 * record the router writes.
 */
async function runRound(client: ProjectClient, eventType: string, text: string, expectDeliveries = 1) {
  const event = await client.postEvent({ type: eventType, text })
  const deliveries = await client.waitForDeliveries(
    (rows) =>
      rows.length >= expectDeliveries && rows.every((d) => d.status === 'ok' && d.session_id !== ''),
    { event_id: event.id, timeoutMs: 180_000 },
  )
  return { event, deliveries }
}

/** The concatenated assistant reply of a job session, from its transcript. */
async function assistantReply(client: ProjectClient, sessionId: string): Promise<string> {
  const messages = await poll(
    () => client.listMessages(sessionId),
    (rows) => rows.some((m) => m.role === 'assistant' && m.content.trim() !== ''),
    60_000,
    `an assistant reply in session ${sessionId}`,
  )
  return messages
    .filter((m) => m.role === 'assistant')
    .map((m) => m.content)
    .join('\n')
}

/**
 * The `worker.finished` event for one finished session, then its follow-on
 * deliveries settled `ok`. The session id is the join key — "the first
 * worker.finished" goes ambiguous the moment two workers run.
 */
async function settleFollowOn(client: ProjectClient, sessionId: string, expectDeliveries: number) {
  const finished = await client.waitForEvents(
    (rows) => rows.some((e) => e.envelope.session_id === sessionId),
    { type: 'worker.finished', timeoutMs: 120_000 },
  )
  const trigger = finished.find((e) => e.envelope.session_id === sessionId)!
  const deliveries = await client.waitForDeliveries(
    (rows) => rows.length >= expectDeliveries && rows.every((d) => d.status === 'ok'),
    { event_id: trigger.id, timeoutMs: 180_000 },
  )
  return { trigger, deliveries }
}

// ── T4 — solo@v1 (control 1) ────────────────────────────────────────────────

// One worker, one schedule, nothing else. The seed's own clock is cron —
// unwaitable in a test — so the round is driven by wiring an ordinary
// subscription to the applied worker afterwards: an operator mutation, and
// exactly how a human would poke a scheduled worker on demand.

const SOLOIST = 'tp4-soloist'
const TP4_SEED = 'You keep a tiny daily log of the orchard.'
const TP4_POKE = 'tp4.poke'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP4_REPLY_MARK = 'The orchard log is kept'

const soloBody = (worker: string): TopologyBody => ({
  name: 'solo',
  version: 'v1',
  answers: { 'worker-name': worker, 'prompt-seed': TP4_SEED },
})

test.describe('T4 — solo@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp4')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('preview, apply, and one poked round', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    // (a) Preview: exactly one worker, one daily schedule, no edges.
    const preview = await client.previewTopology(soloBody(SOLOIST))
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([SOLOIST])
    expect(preview.diff.colliding_workers).toEqual([])
    expect(preview.diff.new_subscriptions).toEqual([])
    expect(preview.diff.new_schedules).toHaveLength(1)
    expect(preview.diff.new_schedules[0]).toMatchObject({ cron: '0 9 * * *', worker: SOLOIST })
    expect(preview.missing_images).toEqual([])
    expect(preview.missing_skills).toEqual([])

    // (b) Apply: read-back matches, bracket records the RESOLVED answers —
    // the cadence default was applied even though never typed.
    const bracket = await applyAndVerify(client, soloBody(SOLOIST), preview)
    expect(bracket.payload.answers).toMatchObject({ cadence: 'daily', 'worker-name': SOLOIST })

    // (c) One round. The applied worker answers a poke through an
    // operator-added subscription (see the block comment above).
    await client.createSubscription({ event_type: TP4_POKE, worker: SOLOIST })
    const round = await runRound(client, TP4_POKE, 'Keep the log for today, please.')
    const reply = await assistantReply(client, round.deliveries[0].session_id)
    expect(reply, 'the applied worker must answer with its scripted line').toContain(TP4_REPLY_MARK)
  })
})

// ── The collision refusal: applying over an existing org changes nothing ────

test.describe('topology apply refuses a collision', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp4-again')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('the second apply of solo is a 409 and nothing changed', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body = soloBody('tp4-encore')
    const preview = await client.previewTopology(body)
    await applyAndVerify(client, body, preview)

    // The second preview already says no — same worker name, now taken.
    const second = await client.previewTopology(body)
    expect(second.applicable).toBe(false)
    expect(second.diff.colliding_workers).toEqual(['tp4-encore'])
    expect(second.diff.new_workers).toEqual([])

    // And the apply refuses with a 409 that names the collision.
    const refused = await client.raw('POST', '/agent/topologies/apply', body)
    expect(refused.status()).toBe(409)
    expect(await refused.text()).toContain('already exists')

    // Nothing changed: one worker, one schedule, exactly one bracket.
    expect(await client.listWorkers()).toHaveLength(1)
    expect(await client.listSchedules()).toHaveLength(1)
    expect(await client.configEvents({ action: 'topology_apply' })).toHaveLength(1)
  })
})

// ── T5 — actor-critic@v1 (entry 4) ──────────────────────────────────────────

const WRITER = 'tp5-writer'
const REVIEWER = 'tp5-reviewer'
const TP5_EVENT = `${WRITER}.task` // derived by the renderer from the actor's name
const TP5_SEED = 'You write product blurbs for the fruit catalogue.'
const TP5_CRITERION = 'every blurb opens with a headline line'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP5_MARKER = 'TP5-HEADLINE-RULE'
const TP5_RATIONALE = 'the blurb shipped without a headline line'

test.describe('T5 — actor-critic@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp5')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('preview, apply, and one improvement round', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'actor-critic',
      version: 'v1',
      answers: {
        'actor-name': WRITER,
        'critic-name': REVIEWER,
        'actor-prompt-seed': TP5_SEED,
        criterion: TP5_CRITERION,
      },
    }

    // (a) Preview: the pair, the inbound route, the filtered review edge.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([WRITER, REVIEWER])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: TP5_EVENT, worker: WRITER },
      { event_type: 'worker.finished', worker: REVIEWER },
    ])
    expect(preview.diff.new_schedules).toEqual([])
    const reviewEdge = preview.bundle.subscriptions.find((s) => s.worker === REVIEWER)!
    expect(reviewEdge.filter).toMatchObject({ worker: WRITER })
    // The criterion is configuration: it landed in the critic's prompt.
    const criticRow = preview.bundle.workers.find((w) => w.name === REVIEWER)!
    expect(criticRow.system_prompt).toContain(TP5_CRITERION)

    // (b) Apply.
    await applyAndVerify(client, body, preview)

    // (c) One round: the actor works, the critic fires and rewrites it
    // through the real worker_prompt_write, with a rationale.
    expect((await client.getWorker(WRITER)).system_prompt).not.toContain(TP5_MARKER)
    const round = await runRound(client, TP5_EVENT, 'Write a blurb about the new apples.')
    const reply = await assistantReply(client, round.deliveries[0].session_id)
    expect(reply).toContain('blurb')

    const rewrite = await waitForConfigAction(client, 'worker_prompt_write', 180_000)
    expect(rewrite.rationale).toBe(TP5_RATIONALE)
    expect(rewrite.actor_worker).toBe(REVIEWER)
    expect(rewrite.actor_session).not.toBe('')
    expect(rewrite.payload).toMatchObject({ name: WRITER })
    expect(String(rewrite.payload.system_prompt)).toContain(TP5_MARKER)
    expect((await client.getWorker(WRITER)).system_prompt).toContain(TP5_MARKER)

    // Let the critic's job settle so teardown is not racing a container.
    await settleFollowOn(client, round.deliveries[0].session_id, 1)
  })
})

// ── T6 — supervisor@v1 (entry 5) ────────────────────────────────────────────

const DESK = 'tp6-desk'
const HANDS = ['tp6-hand-1', 'tp6-hand-2']
const TP6_EVENT = 'tp6.question'
const TP6_MISSION = 'You triage questions about the fruit catalogue.'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP6_ROUTED = 'ROUTE-TO: tp6-hand-1'
const TP6_TAKES = 'TP6-ONE-TAKES-IT'
const TP6_DECLINES = 'TP6-TWO-DECLINES'

test.describe('T6 — supervisor@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp6')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('preview, apply, and one dispatched round', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'supervisor',
      version: 'v1',
      answers: {
        'dispatcher-name': DESK,
        'specialist-prefix': 'tp6-hand',
        'specialist-count': '2',
        'inbound-event-type': TP6_EVENT,
        mission: TP6_MISSION,
      },
    }

    // (a) Preview: the star — dispatcher on the inbound type, both
    // specialists on the dispatcher's broadcast.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([DESK, ...HANDS])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: TP6_EVENT, worker: DESK },
      { event_type: 'worker.finished', worker: HANDS[0] },
      { event_type: 'worker.finished', worker: HANDS[1] },
    ])
    expect(preview.diff.new_schedules).toEqual([])
    for (const edge of preview.bundle.subscriptions.filter((s) => s.worker !== DESK)) {
      expect(edge.filter).toMatchObject({ worker: DESK })
    }

    // (b) Apply — and the convention the seed promises is really in the
    // stored prompt: the dispatcher's standing orders document ROUTE-TO and
    // are honest that worker.finished broadcast is all the emission there is.
    await applyAndVerify(client, body, preview)
    const deskPrompt = (await client.getWorker(DESK)).system_prompt
    expect(deskPrompt).toContain('ROUTE-TO:')
    expect(deskPrompt).toContain('you cannot post events yourself')

    // (c) One round: the question wakes the desk; the desk's finish wakes
    // BOTH hands; the routed one takes it and the other declines.
    const round = await runRound(client, TP6_EVENT, 'How should apples be stored?')
    const deskReply = await assistantReply(client, round.deliveries[0].session_id)
    expect(deskReply).toContain(TP6_ROUTED)

    const followOn = await settleFollowOn(client, round.deliveries[0].session_id, HANDS.length)
    expect(followOn.deliveries).toHaveLength(HANDS.length)

    // Which delivery is which specialist: join through the subscription rows.
    const subs = await client.listSubscriptions()
    const byWorker = new Map(subs.map((s) => [s.worker, s.id]))
    const replyOf = async (worker: string) => {
      const delivery = followOn.deliveries.find((d) => d.subscription_id === byWorker.get(worker))!
      expect(delivery, `a delivery for ${worker}`).toBeTruthy()
      return assistantReply(client, delivery.session_id)
    }
    expect(await replyOf(HANDS[0]), 'the routed specialist must take the item').toContain(TP6_TAKES)
    expect(await replyOf(HANDS[1]), 'the unrouted specialist must decline').toContain(TP6_DECLINES)
  })
})

// ── T7 — frozen-scorer@v1 (entry 12) ────────────────────────────────────────

// The proof that a bundle can SHIP a frozen instrument: the scorer row comes
// back frozen from the apply itself — no post-apply freeze step — and from
// the first round the MCP boundary refuses the critic's scripted attempt to
// rewrite it (one worker.freeze_refused event) while the same critic's
// rewrite of the actor lands. The wiring keeps the critic and scorer causally
// disconnected: both observe the actor, neither observes the other.

const AUTHOR = 'tp7-author'
const TUNER = 'tp7-tuner'
const JUDGE = 'tp7-judge'
const TP7_EVENT = `${AUTHOR}.task` // derived by the renderer from the actor's name
const TP7_SEED = 'You draft release notes for the team.'
const TP7_CRITERION = 'every draft ends with a summary line'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP7_MARKER = 'TP7-SUMMARY-RULE'
const TP7_RATIONALE = 'the draft shipped without a summary line'

test.describe('T7 — frozen-scorer@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp7')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('the bundle ships a frozen scorer; the boundary holds from round one', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'frozen-scorer',
      version: 'v1',
      answers: {
        'actor-name': AUTHOR,
        'critic-name': TUNER,
        'scorer-name': JUDGE,
        'actor-prompt-seed': TP7_SEED,
        criterion: TP7_CRITERION,
      },
    }

    // (a) Preview: three workers — and the bundle row for the scorer is
    // already frozen, before anything is written.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([AUTHOR, TUNER, JUDGE])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: TP7_EVENT, worker: AUTHOR },
      { event_type: 'worker.finished', worker: TUNER },
      { event_type: 'worker.finished', worker: JUDGE },
    ])
    expect(preview.diff.new_schedules).toEqual([])
    const judgeRow = preview.bundle.workers.find((w) => w.name === JUDGE)!
    expect(judgeRow.frozen, 'the bundle must carry the scorer frozen').toBe(true)
    expect(preview.bundle.workers.filter((w) => w.frozen)).toHaveLength(1)

    // (b) Apply — applyAndVerify compares `frozen` per worker, so the scorer
    // coming back frozen IS asserted there; restate the load-bearing bit.
    await applyAndVerify(client, body, preview)
    const storedJudge = await client.getWorker(JUDGE)
    expect(storedJudge.frozen, 'apply must carry Frozen:true through UpsertWorker').toBe(true)
    expect(storedJudge.enabled).toBe(true)
    const judgeSeedPrompt = storedJudge.system_prompt

    // Causal isolation, in the stored wiring: the critic observes only the
    // actor, and NOTHING subscribes to the scorer's events.
    const subs = await client.listSubscriptions()
    for (const sub of subs.filter((s) => s.worker === TUNER)) {
      expect(sub.filter).toMatchObject({ worker: AUTHOR })
    }
    expect(subs.filter((s) => (s.filter as { worker?: string })?.worker === JUDGE)).toHaveLength(0)

    // (c) One round. The author's finish wakes BOTH the tuner and the judge.
    const round = await runRound(client, TP7_EVENT, 'Draft the release notes for 2.4.')
    const followOn = await settleFollowOn(client, round.deliveries[0].session_id, 2)

    // The critic's first move — rewriting the judge — was REFUSED at the MCP
    // boundary and recorded as the C8 signal, naming tool, target, attacker.
    const refusals = await client.waitForEvents((rows) => rows.length > 0, {
      type: 'worker.freeze_refused',
      timeoutMs: 180_000,
    })
    expect(refusals).toHaveLength(1)
    expect(refusals[0].text).toContain('worker_prompt_write')
    expect(refusals[0].text).toContain(JUDGE)
    expect(refusals[0].envelope.worker).toBe(TUNER)

    // …while the SAME critic session's rewrite of the actor landed: the
    // boundary is the frozen flag, not the critic's plumbing.
    const rewrite = await waitForConfigAction(client, 'worker_prompt_write', 180_000)
    expect(rewrite.rationale).toBe(TP7_RATIONALE)
    expect(rewrite.actor_worker).toBe(TUNER)
    expect(rewrite.payload).toMatchObject({ name: AUTHOR })
    expect(String(rewrite.payload.system_prompt)).toContain(TP7_MARKER)
    expect((await client.getWorker(AUTHOR)).system_prompt).toContain(TP7_MARKER)

    // The instrument is untouched: still frozen, prompt byte-identical, and
    // it DID its job this round — the scorer's delivery settled ok and its
    // reply is a score.
    const afterRound = await client.getWorker(JUDGE)
    expect(afterRound.frozen).toBe(true)
    expect(afterRound.system_prompt).toBe(judgeSeedPrompt)
    const judgeSub = subs.find((s) => s.worker === JUDGE)!
    const judgeDelivery = followOn.deliveries.find((d) => d.subscription_id === judgeSub.id)!
    expect(judgeDelivery, 'the frozen scorer must still run jobs').toBeTruthy()
    expect(await assistantReply(client, judgeDelivery.session_id)).toContain('Score:')

    // The ledger balances: one refusal (the judge), one rewrite (the author).
    expect(await client.listEvents({ type: 'worker.freeze_refused' })).toHaveLength(1)
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toHaveLength(1)
  })
})
