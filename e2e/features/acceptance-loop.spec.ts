import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type ProjectClient } from '../helpers/api'
import { configEvents, waitForConfigEvents } from '../helpers/configlog'

// G1 — the acceptance loop. This is the bar the whole spec is measured against
// (§8.7): a hundred emails flow through an answerer; a reviewer reads the
// transcripts and rewrites the answerer's system prompt *through a tool*; the
// next email is answered better. Behaviour changed by a worker editing a
// worker, with no human and no deploy.
//
// # Why this file is half-written on purpose
//
// The router (E3) is what turns an event into a job, and the management tools
// (E4) are what let a worker rewrite another worker's prompt. Neither exists
// yet. Everything that does not depend on them is written and runs; everything
// that does is written out in full and marked `test.fixme()` with the item that
// blocks it.
//
// So this file is the *shape* of the finished acceptance test. When E3 lands,
// deleting one `test.fixme()` line should be most of the work — and if it is
// not, that gap is the thing worth knowing early. Do not delete a pending test
// to make the file green.
//
// What each pending test needs is stated at its `fixme`, so nobody has to
// reverse-engineer the intent from an empty body.

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
  })
  await client.putWorker(REVIEWER, {
    description: 'reviews answered threads and retunes the answerer',
    system_prompt: REVIEWER_PROMPT,
  })
  await client.putWorker(ARCHIVIST, {
    description: 'keeps the rolling summary',
    system_prompt: ARCHIVIST_PROMPT,
    briefing: ['kind=rolling-summary'],
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

  // ── What is provable today ────────────────────────────────────────────────

  test('the §8.7 organisation can be seeded, and the config log records every hire', async () => {
    await seedAcceptanceOrg(client)

    const workers = await client.listWorkers()
    expect(workers.map((w) => w.name).sort()).toEqual([ANSWERER, ARCHIVIST, REVIEWER].sort())
    // Every worker starts enabled — a disabled one ignores its subscriptions,
    // which would make the loop silently never start.
    expect(workers.every((w) => w.enabled)).toBe(true)
    expect((await client.getWorker(REVIEWER)).system_prompt).toContain('worker_prompt_write')
    // The archivist's briefing selector is what pulls the rolling summary into
    // its job (§7.4).
    expect((await client.getWorker(ARCHIVIST)).briefing).toEqual(['kind=rolling-summary'])

    const subs = await client.listSubscriptions()
    expect(subs).toHaveLength(3)
    const byWorker = Object.fromEntries(subs.map((s) => [s.worker, s]))
    expect(byWorker[ANSWERER].event_type).toBe('email.received')
    expect(byWorker[ANSWERER].filter).toEqual({})
    // Both reactions to the answerer are filtered to the answerer.
    expect(byWorker[REVIEWER]).toMatchObject({ event_type: 'worker.finished', filter: { worker: ANSWERER } })
    expect(byWorker[ARCHIVIST]).toMatchObject({ event_type: 'worker.finished', filter: { worker: ANSWERER } })

    // Seeding an org is configuration, so all six writes are in the log (§15.3).
    const actions = (await waitForConfigEvents(client.project, 6)).map((e) => e.action).reverse()
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
    expect(await configEvents(client.project)).toHaveLength(6)
  })

  // ── Blocked on the router (E3) ────────────────────────────────────────────

  test('the router starts an answerer job for the inbound email', async () => {
    test.fixme(
      true,
      'E3 (router) is unbuilt: nothing polls undelivered events, so no delivery row is ever ' +
        'written and no session is created. Needs: a delivery per (event, matching subscription), ' +
        'status pending → running → ok, and a session whose `worker` is the subscription target.',
    )
    await seedAcceptanceOrg(client)
    const event = await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    const deliveries = await client.waitForDeliveries((rows) => rows.length > 0, { event_id: event.id })
    expect(deliveries).toHaveLength(1)
    expect(deliveries[0].subscription_id).toBeTruthy()
    await client.waitForDeliveries((rows) => rows.every((d) => d.status === 'ok'), { event_id: event.id })

    // The job ran as the answerer, with the event as its first message (§6.2).
    const session = await client.getSession(deliveries[0].session_id)
    expect(session.worker).toBe(ANSWERER)
  })

  test('the answerer finishing fans out to the reviewer and the archivist', async () => {
    test.fixme(
      true,
      'E3 (router). E2 already emits worker.finished with {worker} on the envelope; what is ' +
        'missing is the router matching it against the two filtered subscriptions. Needs: two ' +
        'delivery rows for the one worker.finished event, and the reviewer NOT reacting to its ' +
        'own finish (the filter is what prevents an infinite loop).',
    )
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    const finished = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.worker === ANSWERER),
      { type: 'worker.finished' },
    )
    const trigger = finished.find((e) => e.envelope.worker === ANSWERER)!
    // Its depth is one more than the job that produced it — the loop floor.
    expect(trigger.envelope.depth).toBe(1)

    const fanout = await client.waitForDeliveries((rows) => rows.length >= 2, { event_id: trigger.id })
    const workers = await Promise.all(
      fanout.map(async (d) => (await client.getSession(d.session_id)).worker),
    )
    expect(workers.sort()).toEqual([ARCHIVIST, REVIEWER].sort())
  })

  // ── Blocked on the management tools (E4) ──────────────────────────────────

  test('the reviewer rewrites the answerer prompt, with a rationale, through a tool', async () => {
    test.fixme(
      true,
      'E4 (core MCP management tools) is unbuilt: `worker_prompt_write` does not exist, so no ' +
        'worker can edit another worker. This is the assertion the whole spec exists for. ' +
        'E4 must expose worker_prompt_write(name, system_prompt, rationale) with rationale ' +
        'REQUIRED and non-empty (§15.5), writing through the J1 config-event seam so the record ' +
        'below appears, plus a kind=prompt-revision memory. It also needs a deterministic way to ' +
        'make the mock model call it — see the (G1) findings in the work plan.',
    )
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    // The loop's whole point: the answerer's prompt changed, and a worker did
    // it. The reviewer only rewrites once it has seen enough, so this is a wait,
    // not a read.
    const log = await poll(
      () => configEvents(client.project),
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
      'J3 is unbuilt: nothing emits `config.changed` after a config-event commit. Needs an ' +
        'emission AFTER the transaction commits (never inside), at-least-once with an ' +
        'idempotency guard on the config-event id (§15.4), carrying that id so a subscriber can ' +
        'look the change up.',
    )
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    const changed = await client.waitForEvents((rows) => rows.length > 0, { type: 'config.changed' })
    const log = await configEvents(client.project)
    const rewrite = log.find((e) => e.action === 'worker_prompt_write')!
    // The event names the record, so a reader can fetch the full before/after.
    expect(JSON.stringify(changed[0])).toContain(rewrite.id)
  })

  test("the archivist's rolling summary reaches the next job's composed prompt", async () => {
    test.fixme(
      true,
      'Blocked twice over. (1) E3, for the archivist job to run at all. (2) `composed_prompt` ' +
        'is written on the session row by C2 but is not exposed by any HTTP route, so a test ' +
        'cannot read the prompt a job actually ran with. Needs: composed_prompt on the session ' +
        'read path (or an equivalent), otherwise the single most valuable assertion in G1 — that ' +
        'a memory written by one job shows up in the next job\'s prompt — is unobservable.',
    )
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nfirst' })
    await client.waitForDeliveries((rows) => rows.length >= 3, {})

    // A second email, after the archivist has summarised the first round.
    const second = await client.postEvent({ type: 'email.received', text: 'From: carol\n\nsecond' })
    const [delivery] = await client.waitForDeliveries((rows) => rows.length > 0, { event_id: second.id })
    const session = await client.getSession(delivery.session_id)

    // The briefing section the archivist wrote is in the prompt this job ran
    // with — the proof that memory feeds the next job (§7.4).
    expect((session as unknown as { composed_prompt?: string }).composed_prompt ?? '').toContain(
      'rolling-summary',
    )
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
    const log = await waitForConfigEvents(client.project, 3)
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
      'Blocked on E3 (a due schedule must dispatch a job) and E4 (`worker_create` as a tool). ' +
        'The point of the assertion is that NO bootstrap code path exists: the org appears ' +
        'because a worker read its own prompt and called tools.',
    )
    // …after the daily schedule fires, workers the human never created exist.
    const workers = await client.listWorkers()
    expect(workers.map((w) => w.name)).toContain('tweet-author')
    const log = await configEvents(client.project)
    const created = log.filter((e) => e.action === 'worker_create' && e.actor_worker === MANAGER)
    expect(created.length).toBeGreaterThan(0)
  })

  test('a content worker pauses cleanly for human sign-off', async () => {
    test.fixme(
      true,
      'Blocked on E3. H2 built request_human_attention and the attention sweep, but nothing ' +
        'starts the job that would call it. Needs: the delivery parked at `awaiting_human` with ' +
        'no ended_at, an envelope carrying attention_requested, and a POST to the project ' +
        'attention channel of {message, session_url}.',
    )
    await client.putSettings({
      attention_channel: { kind: 'webhook', url: 'http://127.0.0.1:9/never' },
    })
    const parked = await client.waitForDeliveries((rows) => rows.some((d) => d.status === 'awaiting_human'), {})
    expect(parked.find((d) => d.status === 'awaiting_human')!.ended_at).toBe(0)
  })
})
