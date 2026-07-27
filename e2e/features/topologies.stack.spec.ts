import { test, expect } from '@playwright/test'
import {
  newProjectClient,
  poll,
  type ProjectClient,
  type TopologyBody,
  type TopologyPreview,
} from '../helpers/api'
import { waitForConfigAction } from '../helpers/configlog'

// Topology seeds T4–T7 and L2 (docs/product/13-work-plan-self-improvement.md):
// the org charts in the library — solo (control 1), actor-critic (4),
// supervisor (5), frozen-scorer (12), hypothesis-lab (13) — each proven end to
// end against the running stack:
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
// roster lists names but never that phrase; the hypothesis lab's fact-checker
// is keyed the same way, because the critic's tool call names it), and every
// name carries a per-seed prefix (tp4-/tp5-/tp6-/tp7-/tp13-, R1's
// tp8-/tp9-/tp10-/tp11-, and R2's tp14-/tp15-/tp16-) with no name a
// substring of another. (R2's fourth seed, self-organizing@v1, lives in its
// own spec — self-organizing.stack.spec.ts — because its e2e runs under
// --port-pool, which the runner refuses to combine with --mock-script.)
//
// R1 adds one rule with a subtler key, worth naming here because it is a new
// trick: memory-create SUCCESS is keyed on `\"embedded\":false` — the
// JSON-escaped, COMPACT form of the tool-result echo as it actually appears
// in the raw request body (the harness re-marshals the result compactly, so
// agentd's own pretty-printed `"embedded": false` never reaches the wire;
// established empirically against the running stack). The obvious key,
// `created_by_worker`, rides in EVERY request via the image_list tool
// description and so discriminates nothing; `"embedded"` is a JSON key only
// memory_create's result carries, and the escaped-quote form can only come
// from a tool_result body. That rule sits ABOVE the note-marker rules, so a
// round-1 after-write turn (echo present) and a round-2 briefed turn (note
// marker present, no echo) are told apart by which rule catches them first.
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

// ── L2 — hypothesis-lab@v1 (entry 13) ───────────────────────────────────────

// The calibration org (AGENTS_RESEARCH §6): an investigator analyses a dataset
// whose true answer the HARNESS holds, a methodology-critic rewrites HOW it
// investigates, and a FROZEN fact-checker judges the conclusion against truth
// the harness sends it — never truth it generates. The round tells the whole
// §8.7 calibration story against the real machinery:
//
//   round 1: the dataset event lands and the investigator confirms NAIVELY;
//   the critic fires, first tries to tune the frozen checker (refused —
//   worker.freeze_refused), then rewrites the investigator's METHODOLOGY with
//   a rationale (config-logged);
//   round 2, SAME dataset: the rewritten prompt's marker switches the mock
//   script and the investigator reports the controlled null — behaviour, not
//   just storage, proves the rewrite was delivered;
//   check: the harness emits conclusion + held-out truth to the fact-checker,
//   whose verdict lands, while its row stays frozen and byte-identical.
//
// The dataset below is REAL generator output — hypolab.Generate(13,
// {ConfoundTrap, N:120}) — pinned byte-for-byte by go/hypolab/fixture_test.go
// (TestE2EFixtureBytes), which also proves these very bytes carry the trap:
// the naive estimator confirms the false effect on them and the stratified
// one refuses to. Regenerate both copies together or not at all. Ground truth
// (effect=false: age drives both jumper colour and lateness) appears NOWHERE
// in the project until the harness mails it to the checker.
//
// Standing traps honoured: the critic's subscription is retired once its
// round settles (a subscribed critic re-fires its script on round 2), and the
// checker's mock rule is keyed on its identity phrase because the critic's
// scripted tool call names it.

const INSPECTOR = 'tp13-inspector'
const METHODIST = 'tp13-methodist'
const VERIFIER = 'tp13-verifier'
const TP13_EVENT = `${INSPECTOR}.task` // derived by the renderer from the investigator's name
const TP13_CHECK_EVENT = `${VERIFIER}.task` // the harness-side truth channel
const TP13_HINT = 'age_group — it may drive both sides of a correlation'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP13_NAIVE_MARK = 'TP13-NAIVE-CONFIRM'
const TP13_RULE_MARKER = 'TP13-CONTROL-RULE'
const TP13_NULL_MARK = 'TP13-NULL-VERDICT'
const TP13_MATCH_MARK = 'TP13-TRUTH-MATCH'
const TP13_RATIONALE = 'the conclusion never controlled for the stated covariates'

// hypolab.Generate(13, {Kind: ConfoundTrap, N: 120}).CSV() — see the block
// comment above for the pin.
const TP13_DATASET = `jumper,age_group,late
other,old,no
other,young,yes
other,old,no
other,old,no
other,young,no
other,old,yes
other,young,yes
other,old,no
red,young,yes
red,young,no
red,young,yes
other,young,yes
other,old,no
red,young,yes
red,old,no
other,old,no
red,young,no
other,old,yes
red,young,yes
red,young,yes
red,old,no
red,young,no
red,young,yes
other,old,no
other,young,yes
other,old,no
red,young,yes
red,old,no
other,old,no
red,young,yes
red,young,no
other,young,no
other,old,yes
other,old,yes
other,old,no
red,old,no
other,old,no
other,old,yes
other,young,yes
other,young,yes
red,young,yes
red,young,yes
other,old,no
other,old,no
red,old,no
red,young,no
red,young,yes
red,young,no
red,young,yes
red,old,no
red,young,yes
red,young,no
red,old,no
other,old,no
red,young,no
red,young,yes
other,old,no
other,old,no
other,old,no
other,old,no
red,old,yes
red,old,no
red,young,yes
other,old,no
red,young,yes
red,young,yes
red,young,yes
other,old,no
other,old,no
other,old,no
other,old,yes
other,old,yes
other,young,yes
red,young,yes
other,old,no
red,young,no
other,old,no
other,young,yes
red,young,yes
red,old,no
other,old,no
other,old,no
red,young,yes
other,old,no
red,old,no
red,young,yes
other,old,no
other,old,no
other,old,yes
other,old,no
red,young,yes
red,young,yes
other,old,no
other,old,yes
other,old,no
other,young,yes
other,old,no
other,old,no
other,young,no
red,young,yes
red,young,yes
red,old,no
other,old,no
red,young,yes
other,young,yes
other,old,no
other,old,no
red,young,yes
other,old,no
other,old,no
other,old,no
other,old,no
other,old,no
other,old,no
other,old,no
red,young,yes
other,old,no
other,young,no
red,young,yes
other,old,no
`

const TP13_TASK_TEXT =
  'Hypothesis: people wearing red jumpers miss the train more often than others.\n' +
  'Investigate against this dataset (CSV):\n' +
  TP13_DATASET +
  'Report your conclusion.'

// The harness-side check event: conclusion + held-out truth, TOGETHER, and
// only here. No worker name and no script marker appears in this text — it is
// event text, and event text is matched against by every mock rule.
const TP13_CHECK_TEXT =
  'Conclusion under review: once age_group is controlled there is no significant association between red jumpers and lateness — a null result.\n' +
  'Held-out ground truth: effect=false; age_group drives both jumper colour and lateness, so the naive correlation was spurious.\n' +
  'Judge whether the conclusion matches the truth.'

test.describe('L2 — hypothesis-lab@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp13')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('naive confirm, methodology rewrite, controlled null, frozen check', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'hypothesis-lab',
      version: 'v1',
      answers: {
        'investigator-name': INSPECTOR,
        'critic-name': METHODIST,
        'checker-name': VERIFIER,
        'covariates-hint': TP13_HINT,
      },
    }

    // (a) Preview: three workers, the checker already frozen in the bundle,
    // and exactly three edges — dataset in, finishes to the critic, the
    // harness-side check channel in. Nothing routes the checker's own events.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([INSPECTOR, METHODIST, VERIFIER])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: TP13_EVENT, worker: INSPECTOR },
      { event_type: 'worker.finished', worker: METHODIST },
      { event_type: TP13_CHECK_EVENT, worker: VERIFIER },
    ])
    expect(preview.diff.new_schedules).toEqual([])
    const verifierRow = preview.bundle.workers.find((w) => w.name === VERIFIER)!
    expect(verifierRow.frozen, 'the bundle must carry the fact-checker frozen').toBe(true)
    expect(preview.bundle.workers.filter((w) => w.frozen)).toHaveLength(1)
    // The covariates hint is configuration: it landed in the method charter.
    const inspectorRow = preview.bundle.workers.find((w) => w.name === INSPECTOR)!
    expect(inspectorRow.system_prompt).toContain(TP13_HINT)
    // Ground truth cannot ride the bundle: no memory seeds, no preconditions.
    expect(preview.bundle.memory_seeds ?? []).toEqual([])
    expect(preview.bundle.preconditions.images ?? []).toEqual([])
    expect(preview.bundle.preconditions.skills ?? []).toEqual([])

    // (b) Apply. The checker comes back frozen from the apply itself.
    await applyAndVerify(client, body, preview)
    const storedVerifier = await client.getWorker(VERIFIER)
    expect(storedVerifier.frozen, 'apply must carry Frozen:true through UpsertWorker').toBe(true)
    const verifierSeedPrompt = storedVerifier.system_prompt

    // Causal isolation in the stored wiring: the critic observes only the
    // investigator, and nothing subscribes to the checker's events.
    const subs = await client.listSubscriptions()
    for (const sub of subs.filter((s) => s.worker === METHODIST)) {
      expect(sub.filter).toMatchObject({ worker: INSPECTOR })
    }
    expect(subs.filter((s) => (s.filter as { worker?: string })?.worker === VERIFIER)).toHaveLength(0)

    // (c) Round 1: the dataset lands and the investigator confirms naively.
    expect((await client.getWorker(INSPECTOR)).system_prompt).not.toContain(TP13_RULE_MARKER)
    const round1 = await runRound(client, TP13_EVENT, TP13_TASK_TEXT)
    const reply1 = await assistantReply(client, round1.deliveries[0].session_id)
    expect(reply1, 'round 1 must be the naive confirmation').toContain(TP13_NAIVE_MARK)

    // The critic fires — only the critic; the checker holds no worker.finished
    // subscription, truth being none of the loop's business.
    const followOn = await settleFollowOn(client, round1.deliveries[0].session_id, 1)
    const criticSessionId = followOn.deliveries[0].session_id

    // Its first move — tuning the frozen checker — was REFUSED at the MCP
    // boundary and recorded as the C8 signal.
    const refusals = await client.waitForEvents((rows) => rows.length > 0, {
      type: 'worker.freeze_refused',
      timeoutMs: 180_000,
    })
    expect(refusals).toHaveLength(1)
    expect(refusals[0].text).toContain('worker_prompt_write')
    expect(refusals[0].text).toContain(VERIFIER)
    expect(refusals[0].envelope.worker).toBe(METHODIST)

    // …and the SAME critic session's methodology rewrite landed, rationale
    // and all: judge method, never truth.
    const rewrite = await waitForConfigAction(client, 'worker_prompt_write', 180_000)
    expect(rewrite.rationale).toBe(TP13_RATIONALE)
    expect(rewrite.actor_worker).toBe(METHODIST)
    expect(rewrite.actor_session).not.toBe('')
    expect(rewrite.payload).toMatchObject({ name: INSPECTOR })
    expect(String(rewrite.payload.system_prompt)).toContain(TP13_RULE_MARKER)
    expect((await client.getWorker(INSPECTOR)).system_prompt).toContain(TP13_RULE_MARKER)

    // Retire the critic's round: wait for its session to finish, then remove
    // its subscription — a critic left subscribed re-fires its script on
    // round 2's finish and double-writes everything (standing trap).
    await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === criticSessionId),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    const criticSub = subs.find((s) => s.worker === METHODIST)!
    await client.deleteSubscription(criticSub.id)

    // (d) Round 2, the SAME dataset: the rewritten prompt's marker switches
    // the script — the controlled null is a DELIVERY assertion, proving the
    // rewrite reached the composed prompt of the next job (where-vs-when).
    const round2 = await runRound(client, TP13_EVENT, TP13_TASK_TEXT)
    const reply2 = await assistantReply(client, round2.deliveries[0].session_id)
    expect(reply2, 'round 2 must report the controlled null').toContain(TP13_NULL_MARK)
    expect(reply2).not.toContain(TP13_NAIVE_MARK)
    // Let the finish settle (it routes nowhere now) before the check round.
    await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === round2.deliveries[0].session_id),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )

    // (e) The harness mails conclusion + held-out truth to the fact-checker —
    // the FIRST moment truth exists inside the project — and the frozen
    // instrument does its one job.
    const check = await runRound(client, TP13_CHECK_EVENT, TP13_CHECK_TEXT)
    const verdict = await assistantReply(client, check.deliveries[0].session_id)
    expect(verdict).toContain('Verdict: match')
    expect(verdict).toContain(TP13_MATCH_MARK)

    // (f) The instrument is untouched: still frozen, prompt byte-identical.
    const after = await client.getWorker(VERIFIER)
    expect(after.frozen).toBe(true)
    expect(after.system_prompt).toBe(verifierSeedPrompt)

    // The ledger balances: one refusal (the checker), one rewrite (the
    // investigator), and nothing else touched a prompt.
    expect(await client.listEvents({ type: 'worker.freeze_refused' })).toHaveLength(1)
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toHaveLength(1)
  })
})

// ── R1 / tp8 — solo-memory@v1 (control 2) ───────────────────────────────────

// Solo plus the memory channel: the worker writes a kind=<label> note after
// each task and carries a briefing selector for the same label, so the newest
// note rides into the next job's composed prompt. The e2e proves the CHANNEL,
// end to end and as behaviour: round 1's scripted memory_create stores a note
// (the reply's MEM-WRITE-CONFIRMED line is keyed on the tool result's
// `\"embedded\":false` echo — it cannot appear unless the create succeeded),
// and round 2's reply switches on the note content arriving via the briefing,
// with the composed_prompt as the where-it-came-from witness. Like solo, the
// seed's only clock is cron, so the round is driven through an operator-added
// poke subscription (the T4 pattern).

const KEEPER = 'tp8-keeper'
const TP8_SEED = 'You tend the orchard and keep its running log.'
const TP8_POKE = 'tp8.poke'
const TP8_LABEL = 'tp8-notes'
const TP8_SELECTOR = `kind=${TP8_LABEL}`
const TP8_HEADING = `Your memory briefing: ${TP8_SELECTOR}`
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP8_NOTE_MARK = 'TP8-ORCHARD-NOTE'
const TP8_CONFIRM = 'MEM-WRITE-CONFIRMED'
const TP8_RECALL = 'TP8-RECALLED'

test.describe('R1 — solo-memory@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp8')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('a round-1 note rides the briefing into round 2', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'solo-memory',
      version: 'v1',
      answers: { 'worker-name': KEEPER, 'prompt-seed': TP8_SEED, 'memory-label': TP8_LABEL },
    }

    // (a) Preview: solo's shape — one worker, one daily schedule, no edges —
    // plus the memory channel visible in the bundle row: the briefing
    // selector, and a prompt naming the real tool and the real label.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([KEEPER])
    expect(preview.diff.new_subscriptions).toEqual([])
    expect(preview.diff.new_schedules).toHaveLength(1)
    expect(preview.diff.new_schedules[0]).toMatchObject({ cron: '0 9 * * *', worker: KEEPER })
    const bundled = preview.bundle.workers[0]
    expect(bundled.briefing).toEqual([TP8_SELECTOR])
    expect(bundled.system_prompt).toContain('memory_create')
    expect(bundled.system_prompt).toContain(TP8_SELECTOR)

    // (b) Apply: the briefing selector survives the write path (applyAndVerify
    // does not compare briefing, so restate it against the read-back row).
    const bracket = await applyAndVerify(client, body, preview)
    expect(bracket.payload.answers).toMatchObject({ cadence: 'daily', 'memory-label': TP8_LABEL })
    expect((await client.getWorker(KEEPER)).briefing).toEqual([TP8_SELECTOR])

    // (c) Round 1, via the poke pattern: the worker replies, writes the note,
    // and confirms — the confirm line is keyed on the memory_create SUCCESS
    // echo, so its presence proves the store accepted the labelled note.
    await client.createSubscription({ event_type: TP8_POKE, worker: KEEPER })
    const round1 = await runRound(client, TP8_POKE, 'Keep the log for today, please.')
    const reply1 = await assistantReply(client, round1.deliveries[0].session_id)
    expect(reply1, 'round 1 must confirm the stored note').toContain(TP8_CONFIRM)
    expect(reply1).not.toContain(TP8_RECALL)

    // Round 1 ran BEFORE any note existed: no note content and no briefing
    // SECTION in its composed prompt — the channel's before/after contrast.
    // Careful assertion: the worker prompt itself QUOTES the heading (it tells
    // the model where to look), so the heading string appears once in every
    // composed prompt; the real briefing section is a SECOND occurrence.
    const headingCount = (s: string) => s.split(TP8_HEADING).length - 1
    const session1 = await client.getSession(round1.deliveries[0].session_id)
    expect(session1.composed_prompt ?? '').not.toContain(TP8_NOTE_MARK)
    expect(headingCount(session1.composed_prompt ?? ''), 'round 1: prompt quotation only').toBe(1)

    // (d) Round 2: the note content switches the script — a delivery
    // assertion (the marker can only reach this request via the briefing).
    const round2 = await runRound(client, TP8_POKE, 'Keep the log again, please.')
    const reply2 = await assistantReply(client, round2.deliveries[0].session_id)
    expect(reply2, 'round 2 must act on the briefed note').toContain(TP8_RECALL)
    expect(reply2).not.toContain(TP8_CONFIRM)

    // And the where: round 2's composed prompt carries the briefing section —
    // the heading a SECOND time (beyond the prompt's own quotation) and the
    // note content — §7.4 made visible from outside.
    const session2 = await client.getSession(round2.deliveries[0].session_id)
    expect(headingCount(session2.composed_prompt ?? ''), 'round 2: quotation + real section').toBe(2)
    expect(session2.composed_prompt ?? '').toContain(TP8_NOTE_MARK)
  })
})

// ── R1 / tp9 — sham-critic@v1 (control 3) ───────────────────────────────────

// The placebo arm: actor-critic's exact wiring, but the critic's rewrite is an
// honest arbitrary reshuffle. One improvement round, sham edition: the shuffle
// lands in the config log with a rationale that says it found nothing wrong,
// and round 2 proves the actor runs under the REORDERED prompt — the round-2
// rule is keyed on the shuffled adjacency ("Close ... Open ...") that exists
// in no other prompt state.

const CLERK = 'tp9-clerk'
const SHUFFLER = 'tp9-shuffler'
const TP9_EVENT = `${CLERK}.task` // derived by the renderer from the actor's name
const TP9_SEED = 'You keep the till ledger. Open with the date line. Close with a totals line.'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP9_SHUFFLED = 'You keep the till ledger. Close with a totals line. Open with the date line.'
const TP9_RATIONALE =
  'an arbitrary reshuffle of the same instructions: order changed, meaning untouched, nothing was found wrong'
const TP9_SWITCH = 'TP9-SHUFFLE-FOLLOWED'

test.describe('R1 — sham-critic@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp9')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('the sham shuffle lands, says it is arbitrary, and the actor follows it', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'sham-critic',
      version: 'v1',
      answers: { 'actor-name': CLERK, 'critic-name': SHUFFLER, 'actor-prompt-seed': TP9_SEED },
    }

    // (a) Preview: the same wiring shape as actor-critic@v1 — same two
    // workers, same inbound route, same filtered review edge (the Go suite
    // pins this against actor-critic's renderer; here it is asserted against
    // the running preview).
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([CLERK, SHUFFLER])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: TP9_EVENT, worker: CLERK },
      { event_type: 'worker.finished', worker: SHUFFLER },
    ])
    expect(preview.diff.new_schedules).toEqual([])
    const shuffleEdge = preview.bundle.subscriptions.find((s) => s.worker === SHUFFLER)!
    expect(shuffleEdge.filter).toMatchObject({ worker: CLERK })
    // The charter is the honest one: reorder-only, arbitrary, never a claim
    // of a defect.
    const shamRow = preview.bundle.workers.find((w) => w.name === SHUFFLER)!
    expect(shamRow.system_prompt).toContain('REORDER')
    expect(shamRow.system_prompt).toContain('arbitrary reshuffle')

    // (b) Apply.
    await applyAndVerify(client, body, preview)

    // (c) Round 1: the actor works under the original order, the sham fires
    // and reorders — and the config log records exactly what the control arm
    // must record: a rewrite whose rationale ADMITS it diagnosed nothing.
    expect((await client.getWorker(CLERK)).system_prompt).toBe(TP9_SEED)
    const round1 = await runRound(client, TP9_EVENT, 'Write up today.')
    const reply1 = await assistantReply(client, round1.deliveries[0].session_id)
    expect(reply1).toContain('ledger')
    expect(reply1).not.toContain(TP9_SWITCH)

    const followOn = await settleFollowOn(client, round1.deliveries[0].session_id, 1)
    const rewrite = await waitForConfigAction(client, 'worker_prompt_write', 180_000)
    expect(rewrite.rationale).toBe(TP9_RATIONALE)
    expect(rewrite.actor_worker).toBe(SHUFFLER)
    expect(rewrite.payload).toMatchObject({ name: CLERK })
    // The sham property, byte-for-byte: the stored prompt is exactly the
    // reordered seed — same sentences, new order, nothing added or removed.
    expect(rewrite.payload.system_prompt).toBe(TP9_SHUFFLED)
    expect((await client.getWorker(CLERK)).system_prompt).toBe(TP9_SHUFFLED)

    // Retire the sham's round before driving another (standing trap: a critic
    // left subscribed re-fires its script and double-writes).
    await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === followOn.deliveries[0].session_id),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    const subs = await client.listSubscriptions()
    await client.deleteSubscription(subs.find((s) => s.worker === SHUFFLER)!.id)

    // (d) Round 2: the actor's reply switches on the SHUFFLED ORDER being in
    // its composed prompt — churn delivered, no diagnosis anywhere.
    const round2 = await runRound(client, TP9_EVENT, 'Write up today.')
    const reply2 = await assistantReply(client, round2.deliveries[0].session_id)
    expect(reply2, 'round 2 must run under the reordered prompt').toContain(TP9_SWITCH)

    // The ledger balances: exactly one rewrite, the honest one.
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toHaveLength(1)
  })
})

// ── R1 / tp10 — assembly-line@v1 (entry 6) ──────────────────────────────────

// The chain: the inbound event feeds stage 1; stage 2 subscribes to stage 1's
// worker.finished, so stage 1's ENTIRE transcript is stage 2's first message —
// the transcript IS the hand-off (the T6 discovery, honestly worded in the
// prompts). The relay proof is the mock keying itself: stage 2's reply rule
// matches the BATON MARKER from stage 1's reply, a string that can only reach
// stage 2's request through the relayed transcript.

const BELT1 = 'tp10-belt-1'
const BELT2 = 'tp10-belt-2'
const TP10_EVENT = 'tp10.item'
const TP10_MISSION = 'You turn rough furniture into painted pieces.'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP10_BATON = 'TP10-STAGE1-BATON'
const TP10_DONE = 'TP10-STAGE2-DONE'

test.describe('R1 — assembly-line@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp10')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('one item relays down the chain, transcript as baton', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'assembly-line',
      version: 'v1',
      answers: {
        'stage-prefix': 'tp10-belt',
        'stage-count': '2',
        'inbound-event-type': TP10_EVENT,
        mission: TP10_MISSION,
      },
    }

    // (a) Preview: the chain — inbound to stage 1, stage 1's finishes to
    // stage 2 and nothing else; no schedules.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([BELT1, BELT2])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: TP10_EVENT, worker: BELT1 },
      { event_type: 'worker.finished', worker: BELT2 },
    ])
    expect(preview.diff.new_schedules).toEqual([])
    const relayEdge = preview.bundle.subscriptions.find((s) => s.worker === BELT2)!
    expect(relayEdge.filter).toMatchObject({ worker: BELT1 })

    // (b) Apply — and the honesty is really in the stored prompts: stage 1
    // documents the transcript hand-off, stage 2 knows its input is stage 1's
    // transcript and that it is last.
    await applyAndVerify(client, body, preview)
    const firstPrompt = (await client.getWorker(BELT1)).system_prompt
    expect(firstPrompt).toContain('cannot post events yourself')
    expect(firstPrompt).toContain(BELT2)
    const lastPrompt = (await client.getWorker(BELT2)).system_prompt
    expect(lastPrompt).toContain(`${BELT1}'s full finished transcript`)
    expect(lastPrompt).toContain('last stage')

    // (c) One item down the line. Stage 1 replies with the baton...
    const round = await runRound(client, TP10_EVENT, 'A rough chair arrives at the line.')
    const reply1 = await assistantReply(client, round.deliveries[0].session_id)
    expect(reply1).toContain(TP10_BATON)

    // ...stage 2 fires off stage 1's finish, its first user message IS the
    // transcript bearing the baton, and its scripted reply — keyed on that
    // very marker — proves the relay delivered.
    const followOn = await settleFollowOn(client, round.deliveries[0].session_id, 1)
    const stage2Session = followOn.deliveries[0].session_id
    const stage2Messages = await client.listMessages(stage2Session)
    const firstUser = stage2Messages.find((m) => m.role === 'user')!
    expect(firstUser, 'stage 2 must have a transcript-bearing first message').toBeTruthy()
    expect(firstUser.content).toContain(TP10_BATON)
    const reply2 = await assistantReply(client, stage2Session)
    expect(reply2, "stage 2's reply is scripted off the baton in its transcript").toContain(TP10_DONE)

    // The chain terminates: stage 2's finish routes nowhere. Let it settle so
    // teardown is not racing a container.
    await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === stage2Session),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
  })
})

// ── R1 / tp11 — blackboard@v1 (entry 8) ─────────────────────────────────────

// N peers, one event, no addressing: both contributors wake on the same
// inbound event, each appends a labelled note (memory_create success proven
// per worker by the `\"embedded\":false` echo rule), and the follow-up round
// proves the notes exist under the label the only way the surface allows —
// there is no memories HTTP API, so the briefing itself is the witness: round
// 2's composed prompts carry the kind=tp11-chalk section with a round-1 note
// in it. Round-1 rules carry `absent: TP11-TASK-TWO` rather than keying
// round 2 on the note marker, because round-1 jobs run CONCURRENTLY: the
// slower job may legitimately compose after the faster one's note landed and
// so carry a briefing already — the round marker in the event text is the
// only round discriminator that cannot race.

const BOARD1 = 'tp11-board-1'
const BOARD2 = 'tp11-board-2'
const TP11_EVENT = 'tp11.chore'
const TP11_LABEL = 'tp11-chalk'
const TP11_SELECTOR = `kind=${TP11_LABEL}`
const TP11_HEADING = `Your memory briefing: ${TP11_SELECTOR}`
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP11_TASK1 = 'TP11-TASK-ONE: survey the orchard wall and leave a note on the board.'
const TP11_TASK2 = 'TP11-TASK-TWO: read the board and report.'
const TP11_NOTE_RE = /TP11-NOTE-(ONE|TWO)/
const TP11_CONFIRM = 'MEM-WRITE-CONFIRMED'
const TP11_RECALLS: Record<string, string> = {
  [BOARD1]: 'TP11-RECALL-ONE',
  [BOARD2]: 'TP11-RECALL-TWO',
}

test.describe('R1 — blackboard@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp11')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('one event wakes everyone; the board is the only channel', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'blackboard',
      version: 'v1',
      answers: {
        'worker-prefix': 'tp11-board',
        'worker-count': '2',
        'inbound-event-type': TP11_EVENT,
        'memory-label': TP11_LABEL,
        mission: 'You maintain the orchard wall.',
      },
    }

    // (a) Preview: two peers on the SAME unfiltered inbound type, each with
    // the shared briefing selector; no schedules, and no addressing — no
    // worker.finished edges, no prompt naming another contributor.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([BOARD1, BOARD2])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: TP11_EVENT, worker: BOARD1 },
      { event_type: TP11_EVENT, worker: BOARD2 },
    ])
    expect(preview.diff.new_schedules).toEqual([])
    for (const w of preview.bundle.workers) {
      expect(w.briefing).toEqual([TP11_SELECTOR])
      const other = w.name === BOARD1 ? BOARD2 : BOARD1
      expect(w.system_prompt, 'no addressing: prompts never name a peer').not.toContain(other)
    }
    for (const s of preview.bundle.subscriptions) {
      expect(s.filter ?? {}).toEqual({})
    }

    // (b) Apply.
    await applyAndVerify(client, body, preview)

    // Join deliveries to workers through the subscription rows (T6 pattern).
    const subs = await client.listSubscriptions()
    const workerOf = new Map(subs.map((s) => [s.id, s.worker]))

    // (c) Round 1: ONE event, BOTH fire — no addressing, so N deliveries for
    // N contributors — and each writes its note; each confirm line is keyed
    // on that worker's own memory_create success echo.
    const round1 = await runRound(client, TP11_EVENT, TP11_TASK1, 2)
    expect(round1.deliveries).toHaveLength(2)
    expect(round1.deliveries.map((d) => workerOf.get(d.subscription_id)).sort()).toEqual([BOARD1, BOARD2])
    for (const d of round1.deliveries) {
      const reply = await assistantReply(client, d.session_id)
      expect(reply, `${workerOf.get(d.subscription_id)} must confirm its stored note`).toContain(TP11_CONFIRM)
    }

    // (d) Round 2: the notes exist under the label — proven through the only
    // read surface the org has, the shared briefing. Every contributor's
    // composed prompt now carries the kind=tp11-chalk section with the newest
    // round-1 note in it, and every reply is the round-2 recall behaviour.
    const round2 = await runRound(client, TP11_EVENT, TP11_TASK2, 2)
    expect(round2.deliveries).toHaveLength(2)
    for (const d of round2.deliveries) {
      const worker = workerOf.get(d.subscription_id)!
      const reply = await assistantReply(client, d.session_id)
      expect(reply, `${worker}'s round 2 must be the recall behaviour`).toContain(TP11_RECALLS[worker])
      expect(reply).not.toContain(TP11_CONFIRM)
      const session = await client.getSession(d.session_id)
      // The contributor prompt QUOTES the heading once; a real briefing
      // section is a second occurrence (the tp8 lesson, same footgun).
      const headings = (session.composed_prompt ?? '').split(TP11_HEADING).length - 1
      expect(headings, `${worker}'s briefing must carry the board section`).toBe(2)
      expect(session.composed_prompt ?? '').toMatch(TP11_NOTE_RE)
    }
  })
})

// ── R2 / tp14 — debate@v1 (entry 7) ─────────────────────────────────────────

// The committee: one question event wakes BOTH debaters at once (unfiltered
// shared subscription — independence is structural, and neither prompt names
// the other); the aggregator holds one equality-filtered worker.finished edge
// PER debater. What is asserted, and the honest shape of it: the aggregator
// fires N times per question — once per debater's finish — and each of its
// jobs receives a SINGLE debater's transcript, because worker.finished is the
// only routable worker output and nothing hands the judge the whole panel at
// once. The relay proof is the mock keying itself: each aggregator reply rule
// matches the ARGUMENT MARKER from one debater's reply, a string that can
// only reach the aggregator's request through that debater's relayed
// transcript. Asserted per edge, for both edges.

const DEBATERS = ['tp14-debater-1', 'tp14-debater-2']
const CHAIR = 'tp14-chair'
const TP14_EVENT = 'tp14.question'
const TP14_MISSION = 'You settle questions about orchard storage.'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP14_ARGS: Record<string, string> = {
  [DEBATERS[0]!]: 'TP14-ARG-ONE',
  [DEBATERS[1]!]: 'TP14-ARG-TWO',
}
const TP14_VERDICTS: Record<string, string> = {
  [DEBATERS[0]!]: 'TP14-VERDICT-ONE',
  [DEBATERS[1]!]: 'TP14-VERDICT-TWO',
}

test.describe('R2 — debate@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp14')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('one question, two independent arguments, one judgment per finish', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'debate',
      version: 'v1',
      answers: {
        'debater-prefix': 'tp14-debater',
        'debater-count': '2',
        'aggregator-name': CHAIR,
        'inbound-event-type': TP14_EVENT,
        mission: TP14_MISSION,
      },
    }

    // (a) Preview: both debaters unfiltered on the shared question type, and
    // one filtered review edge per debater — 2N subscriptions for N debaters.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([...DEBATERS, CHAIR])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: TP14_EVENT, worker: DEBATERS[0] },
      { event_type: TP14_EVENT, worker: DEBATERS[1] },
      { event_type: 'worker.finished', worker: CHAIR },
      { event_type: 'worker.finished', worker: CHAIR },
    ])
    expect(preview.diff.new_schedules).toEqual([])
    // Independence in the bundle: debater edges unfiltered, chair edges cover
    // each debater exactly once, and no debater prompt names the other.
    for (const s of preview.bundle.subscriptions.filter((s) => s.worker !== CHAIR)) {
      expect(s.filter ?? {}).toEqual({})
    }
    const chairEdges = preview.bundle.subscriptions.filter((s) => s.worker === CHAIR)
    expect(chairEdges.map((s) => (s.filter as { worker?: string }).worker).sort()).toEqual([...DEBATERS].sort())
    for (const d of DEBATERS) {
      const row = preview.bundle.workers.find((w) => w.name === d)!
      const other = DEBATERS.find((x) => x !== d)!
      expect(row.system_prompt, 'independence: debaters never name each other').not.toContain(other)
    }
    // The N-firings honesty is really in the stored charter.
    const chairRow = preview.bundle.workers.find((w) => w.name === CHAIR)!
    expect(chairRow.system_prompt).toContain('once per debater')
    expect(chairRow.system_prompt).toContain("a SINGLE debater's full finished transcript")

    // (b) Apply.
    await applyAndVerify(client, body, preview)

    // Join deliveries to workers through the subscription rows (T6 pattern).
    const subs = await client.listSubscriptions()
    const workerOf = new Map(subs.map((s) => [s.id, s.worker]))
    const chairSubOf = new Map(
      subs
        .filter((s) => s.worker === CHAIR)
        .map((s) => [(s.filter as { worker?: string }).worker!, s.id]),
    )

    // (c) One question: BOTH debaters fire — one event, two deliveries — and
    // each argues with its scripted marker.
    const round = await runRound(
      client,
      TP14_EVENT,
      'A question for the committee: how should the orchard store its apples?',
      2,
    )
    expect(round.deliveries.map((d) => workerOf.get(d.subscription_id)).sort()).toEqual([...DEBATERS].sort())
    const debaterSession: Record<string, string> = {}
    for (const d of round.deliveries) {
      const worker = workerOf.get(d.subscription_id)!
      debaterSession[worker] = d.session_id
      const reply = await assistantReply(client, d.session_id)
      expect(reply, `${worker} must argue with its scripted marker`).toContain(TP14_ARGS[worker])
    }

    // (d) The judge fires ONCE PER FINISH — the honest N-firings shape. For
    // each debater: its finish triggers exactly one delivery, to the chair's
    // edge filtered to that debater, and the chair job's scripted reply is
    // keyed off THAT debater's argument marker — the transcript relay, proven
    // per edge.
    const chairSessions: string[] = []
    for (const worker of DEBATERS) {
      const followOn = await settleFollowOn(client, debaterSession[worker]!, 1)
      expect(followOn.deliveries).toHaveLength(1)
      const delivery = followOn.deliveries[0]!
      expect(delivery.subscription_id).toBe(chairSubOf.get(worker))
      const verdict = await assistantReply(client, delivery.session_id)
      expect(verdict, `the chair's judgment of ${worker} keys off that debater's marker`).toContain(
        TP14_VERDICTS[worker],
      )
      chairSessions.push(delivery.session_id)
    }
    // Two chair firings for two debaters — N firings, not one grand synthesis.
    expect(new Set(chairSessions).size).toBe(2)

    // The chair's own finishes route nowhere; let them settle so teardown is
    // not racing containers.
    for (const sessionId of chairSessions) {
      await client.waitForEvents(
        (rows) => rows.some((e) => e.envelope.session_id === sessionId),
        { type: 'worker.finished', timeoutMs: 120_000 },
      )
    }
  })
})

// ── R2 / tp15 — temporal-hierarchy@v1 (entry 10) ────────────────────────────

// Operators on fast events, a strategist on a slow schedule. The review
// channel is memory, not events: schedules deliver only their own Input text
// and wiring the strategist to worker.finished would put it on the work's
// timescale — so the operator files a kind=tp15-reports note after each task
// and the strategist's briefing selector carries the newest one. The
// strategist's only clock is cron (unwaitable), so its round is driven by an
// operator-added poke subscription — the T4 pattern for schedule-only seeds.
// The rewrite is proven twice over: as a config-log record (with rationale,
// naming the strategist as actor) and as BEHAVIOUR — round 2's operator reply
// switches on the [TP15-TIMEBOX-RULE] marker reaching its composed prompt.

const OPERATOR = 'tp15-op-1'
const STRATEGIST = 'tp15-strategist'
const TP15_EVENT = 'tp15.work'
const TP15_POKE = 'tp15.poke'
const TP15_LABEL = 'tp15-reports'
const TP15_SELECTOR = `kind=${TP15_LABEL}`
const TP15_HEADING = `Your memory briefing: ${TP15_SELECTOR}`
const TP15_MISSION = 'You keep the orchard intake moving.'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP15_REPORT_MARK = 'TP15-REPORT'
const TP15_CONFIRM = 'MEM-WRITE-CONFIRMED'
const TP15_MARKER = 'TP15-TIMEBOX-RULE'
const TP15_SWITCH = 'TP15-SWITCH'
const TP15_RATIONALE = "the operator's reports show it never timeboxes its work"

test.describe('R2 — temporal-hierarchy@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp15')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('the slow layer reads the reports and rewrites the fast layer', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'temporal-hierarchy',
      version: 'v1',
      answers: {
        'operator-prefix': 'tp15-op',
        'operator-count': '1',
        'strategist-name': STRATEGIST,
        'inbound-event-type': TP15_EVENT,
        'memory-label': TP15_LABEL,
        mission: TP15_MISSION,
      },
    }

    // (a) Preview: the timescale separation is visible in the wiring — the
    // operator holds the only subscription, the strategist the only schedule
    // (weekly by default: the hierarchy is temporal), and the memory channel's
    // two ends are in the rows.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([OPERATOR, STRATEGIST])
    expect(preview.diff.new_subscriptions).toEqual([{ event_type: TP15_EVENT, worker: OPERATOR }])
    expect(preview.diff.new_schedules).toHaveLength(1)
    expect(preview.diff.new_schedules[0]).toMatchObject({ cron: '0 9 * * 1', worker: STRATEGIST })
    const opRow = preview.bundle.workers.find((w) => w.name === OPERATOR)!
    expect(opRow.system_prompt).toContain('memory_create')
    expect(opRow.system_prompt).toContain(TP15_SELECTOR)
    expect(opRow.system_prompt, 'reports go to a label, not an address').not.toContain(STRATEGIST)
    expect(opRow.briefing ?? []).toEqual([])
    const stratRow = preview.bundle.workers.find((w) => w.name === STRATEGIST)!
    expect(stratRow.briefing).toEqual([TP15_SELECTOR])
    expect(stratRow.system_prompt).toContain('worker_prompt_write')
    expect(stratRow.system_prompt).toContain(OPERATOR)

    // (b) Apply — and the briefing selector survives the write path.
    const bracket = await applyAndVerify(client, body, preview)
    expect(bracket.payload.answers).toMatchObject({ 'strategist-cadence': 'weekly', 'memory-label': TP15_LABEL })
    expect((await client.getWorker(STRATEGIST)).briefing).toEqual([TP15_SELECTOR])

    // (c) Round 1 — the fast layer: the operator handles the task and files
    // its report; the confirm line is keyed on memory_create's success echo,
    // so its presence proves the labelled note landed in the store.
    const round1 = await runRound(client, TP15_EVENT, 'Sort the morning intake, please.')
    const reply1 = await assistantReply(client, round1.deliveries[0].session_id)
    expect(reply1, 'round 1 must confirm the filed report').toContain(TP15_CONFIRM)
    expect(reply1).not.toContain(TP15_SWITCH)
    // Its finish routes nowhere (the strategist holds no subscription — that
    // is the point); let it settle before the review round.
    await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === round1.deliveries[0].session_id),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )

    // (d) The review — the slow layer, poked on demand: the strategist's own
    // clock is cron, so drive it through an operator-added subscription. Its
    // composed prompt must carry the briefing section with the operator's
    // report — the upward channel, witnessed end to end. (The prompt itself
    // QUOTES the heading once; the real section is the second occurrence —
    // the tp8 lesson.)
    await client.createSubscription({ event_type: TP15_POKE, worker: STRATEGIST })
    const review = await runRound(client, TP15_POKE, 'Weekly review: read the newest report and retune.')
    const reviewSession = await client.getSession(review.deliveries[0].session_id)
    const headingCount = (reviewSession.composed_prompt ?? '').split(TP15_HEADING).length - 1
    expect(headingCount, "the strategist's briefing must carry the report section").toBe(2)
    expect(reviewSession.composed_prompt ?? '').toContain(TP15_REPORT_MARK)

    // The rewrite landed, with its rationale, from the strategist's session.
    const rewrite = await waitForConfigAction(client, 'worker_prompt_write', 180_000)
    expect(rewrite.rationale).toBe(TP15_RATIONALE)
    expect(rewrite.actor_worker).toBe(STRATEGIST)
    expect(rewrite.actor_session).not.toBe('')
    expect(rewrite.payload).toMatchObject({ name: OPERATOR })
    expect(String(rewrite.payload.system_prompt)).toContain(TP15_MARKER)
    expect((await client.getWorker(OPERATOR)).system_prompt).toContain(TP15_MARKER)
    // Let the strategist settle (its finish routes nowhere).
    await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === review.deliveries[0].session_id),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )

    // (e) Round 2 — the fast layer, improved: the operator's reply switches
    // on the rewritten prompt's marker being in its composed prompt. Where-vs-
    // when: behaviour proves delivery, not just storage.
    const round2 = await runRound(client, TP15_EVENT, 'Sort the afternoon intake, please.')
    const reply2 = await assistantReply(client, round2.deliveries[0].session_id)
    expect(reply2, 'round 2 must run under the strategist-amended prompt').toContain(TP15_SWITCH)
    expect(reply2).not.toContain(TP15_CONFIRM)

    // The ledger balances: exactly one rewrite, and it names the operator.
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toHaveLength(1)
  })
})

// ── R2 / tp16 — escalation@v1 (entry 11) ────────────────────────────────────

// One worker, two cases, two outcomes. The routine case settles `ok` with no
// attention requested; the risky case calls request_human_attention and the
// delivery PARKS at `awaiting_human` with no ended_at — a pause, not an end
// (S4's assertion pattern: delivery status + the §9 envelope stamp, never the
// reply text alone). Round discrimination is event text (TP16-CASE-ROUTINE /
// TP16-CASE-RISKY): the two jobs are separate sessions of the same worker,
// and the case text is the only thing that differs.

const WARDEN = 'tp16-warden'
const TP16_EVENT = 'tp16.case'
const TP16_MISSION = 'You run the records desk for the orchard.'
// Byte-for-byte with e2e/mock-scripts/topologies.json:
const TP16_ROUTINE_TASK = 'TP16-CASE-ROUTINE: file the daily stock count.'
const TP16_RISKY_TASK = 'TP16-CASE-RISKY: purge the entire archive.'
const TP16_HANDLED = 'TP16-HANDLED'

test.describe('R2 — escalation@v1', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-tp16')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('routine settles ok; the risky case parks for a human', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    const body: TopologyBody = {
      name: 'escalation',
      version: 'v1',
      answers: { 'worker-name': WARDEN, 'inbound-event-type': TP16_EVENT, mission: TP16_MISSION },
    }

    // (a) Preview: the smallest working shape — one worker, one inbound edge,
    // no schedules — and the valve is really in the charter.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([WARDEN])
    expect(preview.diff.new_subscriptions).toEqual([{ event_type: TP16_EVENT, worker: WARDEN }])
    expect(preview.diff.new_schedules).toEqual([])
    const row = preview.bundle.workers[0]!
    expect(row.system_prompt).toContain('request_human_attention')
    expect(row.system_prompt).toContain('the correct outcome, not a failure')

    // (b) Apply.
    await applyAndVerify(client, body, preview)

    // (c) The routine case: handled to completion — delivery ok, scripted
    // handled marker, and the envelope says no attention was requested.
    const routine = await runRound(client, TP16_EVENT, TP16_ROUTINE_TASK)
    const routineReply = await assistantReply(client, routine.deliveries[0].session_id)
    expect(routineReply).toContain(TP16_HANDLED)
    const finishedRoutine = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === routine.deliveries[0].session_id),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    expect(
      finishedRoutine.find((e) => e.envelope.session_id === routine.deliveries[0].session_id)!.envelope
        .attention_requested,
    ).toBe(false)

    // (d) The risky case: no runRound here — that helper waits for `ok`, and
    // the whole point is that this delivery must NOT reach `ok`. It parks.
    const event = await client.postEvent({ type: TP16_EVENT, text: TP16_RISKY_TASK })
    const parked = await client.waitForDeliveries(
      (rows) => rows.some((d) => d.status === 'awaiting_human'),
      { event_id: event.id, timeoutMs: 180_000 },
    )
    const waiting = parked.find((d) => d.status === 'awaiting_human')!
    // A pause, not an end (§8.4): parked open-ended, by a fresh job.
    expect(waiting.ended_at).toBe(0)
    expect(waiting.session_id).not.toBe(routine.deliveries[0].session_id)

    // …and the §9 stamp marks the work knowingly half-done.
    const finishedRisky = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === waiting.session_id),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    expect(
      finishedRisky.find((e) => e.envelope.session_id === waiting.session_id)!.envelope.attention_requested,
    ).toBe(true)

    // The ledger balances: one ok delivery, one parked, nothing else.
    expect((await client.listDeliveries({ status: 'ok' })).length).toBe(1)
    expect((await client.listDeliveries({ status: 'awaiting_human' })).length).toBe(1)
  })
})
