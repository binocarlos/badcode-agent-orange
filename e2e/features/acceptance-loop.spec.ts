import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type ProjectClient } from '../helpers/api'
import { sessionMCP } from '../helpers/mcp'
import { configEvents, waitForConfigEvents } from '../helpers/configlog'

// G1 — the acceptance loop. This is the bar the whole spec is measured against
// (§8.7): a hundred emails flow through an answerer; a reviewer reads the
// transcripts and rewrites the answerer's system prompt *through a tool*; the
// next email is answered better. Behaviour changed by a worker editing a
// worker, with no human and no deploy.
//
// # State of the loop
//
// The router (E3) now runs it, so most of this file asserts real behaviour: an
// email starts an answerer job, the answerer finishing fans out to the reviewer
// and the archivist, and — the part the whole design rests on — what one job
// writes down is in front of the next job.
//
// What remains pending is the rewrite itself: `worker_prompt_write` is E4, in
// flight. Those tests are written out in full against E4's contract and marked
// `test.fixme()` with the item that blocks them, so they flip the moment it
// merges. Do not delete a pending test to make the file green.
//
// # On driving tools without the model
//
// The mock model serves a canned script and only calls a tool when agentd is
// given one (AGENTKIT_MOCK_MODEL_SCRIPT). Where a test needs a job to have used
// a tool, it calls that tool with the job's OWN session credential: same tool,
// same auth, same provenance row — only the decision to call it is the test's
// rather than the model's. That is the honest limit of a mock-mode acceptance
// test, and it is stated at each such call.

/** The §8.7 cast: who reacts to what. */
const ANSWERER = 'email-answerer'
const REVIEWER = 'email-reviewer'
const ARCHIVIST = 'archivist'

const ANSWERER_PROMPT = [
  'You answer inbound customer email.',
  'Read the thread, write a reply, and send it.',
].join('\n')

// The reviewer's prompt is the loop: it is the sentence that makes a worker
// rewrite another worker's prompt. Nothing in the engine knows about reviewing.
const REVIEWER_PROMPT = [
  'You review answered email threads for tone.',
  'If you find a systemic problem — not a one-off — use worker_prompt_read and',
  `worker_prompt_write to amend ${ANSWERER}'s system prompt with concrete guidance,`,
  'and record a kind=lesson memory saying what you changed and why.',
].join('\n')

const ARCHIVIST_PROMPT = [
  'You keep the project\'s rolling summary up to date (§7.4).',
  'Read what happened, then write a kind=rolling-summary memory with name=email.',
].join('\n')

/**
 * Seeds the §8.7 organisation: three workers and the three subscriptions that
 * wire them together. Returns nothing — the assertions live in the tests, so a
 * later test can reuse the seeding without inheriting someone else's checks.
 */
async function seedAcceptanceOrg(client: ProjectClient): Promise<void> {
  await client.putWorker(ANSWERER, {
    description: 'answers inbound customer email',
    system_prompt: ANSWERER_PROMPT,
    // The answerer is the worker being improved, so it is the one that reads
    // the rolling summary the archivist writes (§7.4). This selector is the
    // seam the whole loop closes through.
    briefing: ['kind=rolling-summary'],
  })
  await client.putWorker(REVIEWER, {
    description: 'reviews answered threads and retunes the answerer',
    system_prompt: REVIEWER_PROMPT,
  })
  await client.putWorker(ARCHIVIST, {
    description: 'keeps the rolling summary',
    system_prompt: ARCHIVIST_PROMPT,
  })

  // An inbound email starts an answerer job.
  await client.createSubscription({ event_type: 'email.received', worker: ANSWERER })
  // The answerer finishing starts a reviewer job *and* an archivist job. The
  // envelope filter is what keeps the reviewer from reacting to its own work —
  // without it, a reviewer job would finish, match its own subscription, and
  // loop for ever (§8.4's loop floor is the backstop, this is the intent).
  await client.createSubscription({
    event_type: 'worker.finished',
    worker: REVIEWER,
    filter: { worker: ANSWERER },
  })
  await client.createSubscription({
    event_type: 'worker.finished',
    worker: ARCHIVIST,
    filter: { worker: ANSWERER },
  })
}

test.describe('G1 §8.7 — the acceptance loop', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(240_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-g1')
  })

  // The router creates a session per delivery, and each holds a running
  // container until it is deleted. This loop makes three or four per test.
  test.afterEach(async () => {
    await client.cleanup()
  })

  // ── What is provable today ────────────────────────────────────────────────

  test('the §8.7 organisation can be seeded, and the config log records every hire', async () => {
    await seedAcceptanceOrg(client)

    const workers = await client.listWorkers()
    expect(workers.map((w) => w.name).sort()).toEqual([ANSWERER, ARCHIVIST, REVIEWER].sort())
    // Every worker starts enabled — a disabled one ignores its subscriptions,
    // which would make the loop silently never start.
    expect(workers.every((w) => w.enabled)).toBe(true)
    expect((await client.getWorker(REVIEWER)).system_prompt).toContain('worker_prompt_write')
    // The answerer's briefing selector is what pulls the archivist's rolling
    // summary into the next answerer job (§7.4) — the loop's substrate.
    expect((await client.getWorker(ANSWERER)).briefing).toEqual(['kind=rolling-summary'])

    const subs = await client.listSubscriptions()
    expect(subs).toHaveLength(3)
    const byWorker = Object.fromEntries(subs.map((s) => [s.worker, s]))
    expect(byWorker[ANSWERER].event_type).toBe('email.received')
    expect(byWorker[ANSWERER].filter).toEqual({})
    // Both reactions to the answerer are filtered to the answerer.
    expect(byWorker[REVIEWER]).toMatchObject({ event_type: 'worker.finished', filter: { worker: ANSWERER } })
    expect(byWorker[ARCHIVIST]).toMatchObject({ event_type: 'worker.finished', filter: { worker: ANSWERER } })

    // Seeding an org is configuration, so all six writes are in the log (§15.3).
    const actions = (await waitForConfigEvents(client, 6)).map((e) => e.action).reverse()
    expect(actions).toEqual([
      'worker_create',
      'worker_create',
      'worker_create',
      'subscription_create',
      'subscription_create',
      'subscription_create',
    ])
  })

  test('an inbound email enters the project as an external event, awaiting routing', async () => {
    await seedAcceptanceOrg(client)

    const event = await client.postEvent({
      type: 'email.received',
      text: 'From: bob@example.com\nSubject: still broken\n\nThis is the third time I have written.',
    })

    // Core stamps the envelope; a sender cannot claim to be a worker or set a
    // depth, which is what keeps the loop floor and every filter honest (§8.1).
    expect(event.envelope).toMatchObject({ source: 'external', depth: 0, worker: '', interactive: false })
    expect(event.delivered).toBe(false)

    const logged = await client.listEvents({ type: 'email.received' })
    expect(logged.map((e) => e.id)).toContain(event.id)

    // Ingesting an event is not configuration — §15.3 rule 3 keeps the event
    // spine out of the config log — so the only records are the six from seeding.
    expect(await configEvents(client)).toHaveLength(6)
  })

  // ── The loop, now that the router runs it ─────────────────────────────────

  test('the router starts an answerer job for the inbound email', async () => {
    await seedAcceptanceOrg(client)
    const event = await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    // One matching subscription, so exactly one delivery — the (event,
    // subscription) pair is the idempotency key, so a retrying router cannot
    // double-deliver (§8.4).
    const deliveries = await client.waitForDeliveries((rows) => rows.length > 0, { event_id: event.id })
    expect(deliveries).toHaveLength(1)
    await client.waitForDeliveries((rows) => rows.every((d) => d.status === 'ok'), {
      event_id: event.id,
      timeoutMs: 120_000,
    })

    // The job ran as the answerer, and the prompt it ran with is on the record.
    const [delivery] = await client.listDeliveries({ event_id: event.id })
    const session = await client.getSession(delivery.session_id)
    expect(session.worker).toBe(ANSWERER)
    expect(session.composed_prompt).toContain(ANSWERER_PROMPT)
    // Composition puts the worker's own prompt after the core preamble, and the
    // preamble is what tells a worker how to treat event text (§6.2/§6.3).
    expect(session.composed_prompt).toContain('--- worker prompt ---')
  })

  test('the answerer finishing fans out to the reviewer and the archivist', async () => {
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    const finished = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.worker === ANSWERER),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    const trigger = finished.find((e) => e.envelope.worker === ANSWERER)!
    // Depth is one more than the job that produced it. This is the loop floor,
    // and it only works because the router stamps depth BEFORE the turn runs.
    expect(trigger.envelope.depth).toBe(1)
    expect(trigger.envelope.source).toBe('worker')

    // Two subscriptions match that one event, so two jobs start. A delivery is
    // created before its session exists, so wait for both to be dispatched
    // rather than for the rows to appear.
    const fanout = await client.waitForDeliveries(
      (rows) => rows.length >= 2 && rows.every((d) => d.session_id !== ''),
      { event_id: trigger.id, timeoutMs: 120_000 },
    )
    expect(fanout).toHaveLength(2)
    const workers = await Promise.all(
      fanout.map(async (d) => (await client.getSession(d.session_id)).worker),
    )
    expect(workers.sort()).toEqual([ARCHIVIST, REVIEWER].sort())

    // …and the reviewer does NOT react to its own finish. The envelope filter is
    // what stops that; without it this project would run for ever.
    const reviewerFinished = (await client.listEvents({ type: 'worker.finished' })).filter(
      (e) => e.envelope.worker === REVIEWER,
    )
    for (const e of reviewerFinished) {
      expect(await client.listDeliveries({ event_id: e.id })).toEqual([])
    }
  })

  // The assertion the loop actually rests on: what one job writes down, the
  // next job reads. Until `composed_prompt` was exposed there was no way to
  // observe it, and it is the difference between "the archivist ran" and "the
  // archivist changed what the answerer knows".
  test("a memory written by one job reaches the next job's composed prompt", async () => {
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    // Wait for the archivist job the fan-out started, and act as it.
    const fanout = await client.waitForDeliveries(
      (rows) => rows.length >= 3 && rows.every((d) => d.session_id !== ''),
      { timeoutMs: 120_000 },
    )
    let archivistSession = ''
    for (const d of fanout) {
      if ((await client.getSession(d.session_id)).worker === ARCHIVIST) archivistSession = d.session_id
    }
    expect(archivistSession, 'the fan-out must have started an archivist job').not.toBe('')

    // The archivist writes the rolling summary with its OWN session credential,
    // so the memory carries that job's provenance. The model does not choose to
    // call the tool — the mock model cannot — but everything else is the real
    // path: same tool, same auth, same row.
    const summary = 'ROLLING SUMMARY: three customers said the answers were curt.'
    await sessionMCP(client.project, archivistSession).callOK('memory_create', {
      content: summary,
      labels: { kind: 'rolling-summary', name: 'email' },
    })

    // A second email starts a fresh answerer job…
    const second = await client.postEvent({ type: 'email.received', text: 'From: carol\n\nanother one' })
    const [delivery] = await client.waitForDeliveries(
      (rows) => rows.length > 0 && rows.every((d) => d.session_id !== ''),
      { event_id: second.id, timeoutMs: 120_000 },
    )
    const next = await client.getSession(delivery.session_id)
    expect(next.worker).toBe(ANSWERER)

    // …and that job's prompt carries the archivist's summary, under the heading
    // its briefing selector asked for. This is §7.4 closing: a lesson learned in
    // one job is in front of the next one, with no human in between.
    expect(next.composed_prompt).toContain('--- Your memory briefing: kind=rolling-summary ---')
    expect(next.composed_prompt).toContain(summary)
  })

  // ── Blocked on the management tools (E4) ──────────────────────────────────

  test('the reviewer rewrites the answerer prompt, with a rationale, through a tool', async () => {
    test.fixme(
      true,
      'E4 is in flight: `worker_prompt_write` does not exist yet, so no worker can edit another ' +
        'worker. This is the assertion the whole spec exists for. E4 must expose ' +
        'worker_prompt_write(name, system_prompt, rationale) with rationale REQUIRED and ' +
        'non-empty (§15.5), writing through the J1 config-event seam so the record below appears, ' +
        'plus a kind=prompt-revision memory. The way to make it happen from a job is now settled: ' +
        'script the reviewer with AGENTKIT_MOCK_MODEL_SCRIPT (match on the worker name, turn 0 ' +
        'the tool call, turn 1 the reply) — see the unblock bullets in the work plan.',
    )
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    // The loop's whole point: the answerer's prompt changed, and a worker did
    // it. The reviewer only rewrites once it has seen enough, so this is a wait,
    // not a read.
    const log = await poll(
      () => configEvents(client),
      (rows) => rows.some((e) => e.action === 'worker_prompt_write'),
      120_000,
      'the reviewer to rewrite the answerer prompt',
    )
    const rewrite = log.find((e) => e.action === 'worker_prompt_write')!
    // §15.5: the *why* is the one thing not recoverable from the text, so it is
    // mandatory on this action and on no other.
    expect(rewrite.rationale.trim()).not.toBe('')
    // Written BY a worker, FROM a session — not a human edit, which logs no actor.
    expect(rewrite.actor_worker).toBe(REVIEWER)
    expect(rewrite.actor_session).not.toBe('')
    // Payload is the full worker row after the write (§15.2).
    expect(rewrite.payload).toMatchObject({ name: ANSWERER })
    expect(String(rewrite.payload.system_prompt)).not.toBe(ANSWERER_PROMPT)

    // …and the stored worker agrees with the log.
    expect((await client.getWorker(ANSWERER)).system_prompt).toBe(rewrite.payload.system_prompt)
  })

  test('the prompt rewrite emits a routable config.changed event', async () => {
    test.fixme(
      true,
      'J3 is in flight: nothing emits `config.changed` yet (no emitter exists in the tree). ' +
        'Needs an emission AFTER the transaction commits, never inside it, at-least-once with an ' +
        'idempotency guard on the config-event id (§15.4), carrying that id so a subscriber can ' +
        'look the change up. Depends on E4 too, since the rewrite is what triggers it here.',
    )
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    const changed = await client.waitForEvents((rows) => rows.length > 0, { type: 'config.changed' })
    const log = await configEvents(client)
    const rewrite = log.find((e) => e.action === 'worker_prompt_write')!
    // The event names the record, so a reader can fetch the full before/after.
    expect(JSON.stringify(changed[0])).toContain(rewrite.id)
  })

})

// §8.8 — the shape the first real deployment takes: one human-seeded manager
// that builds the rest of the workforce from its own prompt. G1 must cover it
// because it is what "how do you go from no workers to a workforce" answers.
test.describe('G1 §8.8 — the marketing-manager shape', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(240_000)

  const MANAGER = 'marketing-manager'
  const RECONCILE =
    'Reconcile the workforce: ensure every worker, schedule, and subscription described in ' +
    'your system prompt exists and matches; create or update via your tools. Report what you changed.'
  const CRITIQUE =
    'Critique your own system prompt: search memory for prompt revisions, published content ' +
    'and lessons; judge the strategy, then rewrite your prompt to be the most effective version of itself.'

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-g1-mgr')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('a single seeded manager plus its two schedules is the whole bootstrap', async () => {
    // Everything a human does, done: one worker whose prompt describes the org.
    await client.putWorker(MANAGER, {
      description: 'owns marketing strategy and the workforce that delivers it',
      system_prompt: [
        'You own BadCode marketing.',
        'The workforce that should exist: a tweet-author posting daily,',
        'an instagram-image-maker, and a secretary handling inbound mail.',
      ].join('\n'),
    })
    await client.createSchedule({
      worker: MANAGER,
      cron: '0 9 * * *',
      input: RECONCILE,
      rationale: 'daily workforce reconciliation (§8.8)',
    })
    await client.createSchedule({
      worker: MANAGER,
      cron: '0 10 * * 1',
      input: CRITIQUE,
      rationale: 'weekly self-critique of the strategy prompt (§8.8)',
    })

    const schedules = await client.listSchedules()
    expect(schedules).toHaveLength(2)
    expect(schedules.every((s) => s.worker === MANAGER && s.enabled)).toBe(true)
    expect(schedules.map((s) => s.cron).sort()).toEqual(['0 10 * * 1', '0 9 * * *'])

    // One worker and two schedules — and the log says a human did it (no actor).
    const log = await waitForConfigEvents(client, 3)
    expect(log.map((e) => e.action).reverse()).toEqual([
      'worker_create',
      'schedule_create',
      'schedule_create',
    ])
    expect(log.every((e) => e.actor_worker === '')).toBe(true)
    // The schedule writes carried the rationale the API accepted.
    expect(log.filter((e) => e.action === 'schedule_create').every((e) => e.rationale !== '')).toBe(true)
  })

  test('the daily reconcile builds the workforce described in the prompt', async () => {
    test.fixme(
      true,
      'E3 has landed, so a due schedule now dispatches a job; what is missing is E4\'s ' +
        '`worker_create` tool for the manager to call, and a mock script driving it. The point ' +
        'of the assertion is that NO bootstrap code path exists: the org appears because a ' +
        'worker read its own prompt and called tools.',
    )
    // …after the daily schedule fires, workers the human never created exist.
    const workers = await client.listWorkers()
    expect(workers.map((w) => w.name)).toContain('tweet-author')
    const log = await configEvents(client)
    const created = log.filter((e) => e.action === 'worker_create' && e.actor_worker === MANAGER)
    expect(created.length).toBeGreaterThan(0)
  })

  test('a content worker pauses cleanly for human sign-off', async () => {
    test.fixme(
      true,
      'H2 built request_human_attention and the attention sweep, and E3 now starts the jobs — ' +
        'what is missing is a job that CALLS the tool, which needs a mock script ' +
        '(AGENTKIT_MOCK_MODEL_SCRIPT) driving the content worker. Needs: the delivery parked at ' +
        '`awaiting_human` with no ended_at, an envelope carrying attention_requested, and a POST ' +
        'to the project attention channel of {message, session_url}.',
    )
    await client.putSettings({
      attention_channel: { kind: 'webhook', url: 'http://127.0.0.1:9/never' },
    })
    const parked = await client.waitForDeliveries((rows) => rows.some((d) => d.status === 'awaiting_human'), {})
    expect(parked.find((d) => d.status === 'awaiting_human')!.ended_at).toBe(0)
  })
})
