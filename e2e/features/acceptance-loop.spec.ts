import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type ProjectClient } from '../helpers/api'
import { sessionMCP } from '../helpers/mcp'
import { configEvents, waitForConfigAction, waitForConfigEvents } from '../helpers/configlog'

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

  // ── The rewrite: a worker editing a worker ────────────────────────────────

  // §8.7's definition of done for the entire spec. Everything else in this file
  // exists so that this can happen: the answerer's prompt changes because the
  // reviewer decided it should, with no human and no deploy.
  test('the reviewer rewrites the answerer prompt, with a rationale, through a tool', async () => {
    await seedAcceptanceOrg(client)
    const before = (await client.getWorker(ANSWERER)).system_prompt
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    // Wait for the reviewer job the fan-out started, and act as it. The model
    // does not choose to call the tool — the mock model cannot without a script
    // — but the credential, the tool and the row written are the real ones.
    const fanout = await client.waitForDeliveries(
      (rows) => rows.length >= 3 && rows.every((d) => d.session_id !== ''),
      { timeoutMs: 120_000 },
    )
    let reviewerSession = ''
    for (const d of fanout) {
      if ((await client.getSession(d.session_id)).worker === REVIEWER) reviewerSession = d.session_id
    }
    expect(reviewerSession, 'the fan-out must have started a reviewer job').not.toBe('')

    const rewritten = `${before}\nAcknowledge the customer's frustration before answering.`
    const why = 'three customers in a row said the answers read as curt'
    const result = await sessionMCP(client.project, reviewerSession).callOK('worker_prompt_write', {
      name: ANSWERER,
      system_prompt: rewritten,
      rationale: why,
    })
    // The tool tells the model the change lands on the NEXT job.
    expect(String(result.note)).toContain('NEXT job')
    // It also reports whether the prompt-revision memory was written — and that
    // is REPORTED, not required. If the memory write fails the tool still
    // succeeds, because the prompt is already live and config-evented, and
    // telling the model otherwise would be telling it the opposite of the
    // truth. So assert the shape, never `stored: true`.
    expect(typeof result.prompt_revision.stored).toBe('boolean')

    // The config log carries the decision.
    const rewrite = await waitForConfigAction(client, 'worker_prompt_write')
    // §15.5: the *why* is the one thing not recoverable from the text, so it is
    // mandatory on this action and on no other.
    expect(rewrite.rationale).toBe(why)
    // Written BY a worker, FROM a session — not a human edit, which logs no actor.
    expect(rewrite.actor_worker).toBe(REVIEWER)
    expect(rewrite.actor_session).toBe(reviewerSession)
    // Payload is the full worker row after the write (§15.2).
    expect(rewrite.payload).toMatchObject({ name: ANSWERER, system_prompt: rewritten })

    // …and the stored worker agrees with the log.
    expect((await client.getWorker(ANSWERER)).system_prompt).toBe(rewritten)
  })

  // A rewrite with no reason is refused: §15.5 makes the rationale mandatory on
  // this action precisely because the text of a prompt never explains itself.
  test('a prompt rewrite without a rationale is refused', async () => {
    await seedAcceptanceOrg(client)
    const session = await client.createSession({ job: 'reviewer-stand-in' })
    await client.sendMessage(session, 'hello')

    const out = await sessionMCP(client.project, session).call('worker_prompt_write', {
      name: ANSWERER,
      system_prompt: 'Be warmer.',
      rationale: '   ',
    })
    expect(out.isError).toBe(true)
    expect(out.text).toContain('rationale is required on a prompt write')
    expect(out.text).toContain('§15.5')
    // Refused means refused: the prompt is untouched and nothing was logged.
    expect((await client.getWorker(ANSWERER)).system_prompt).toBe(ANSWERER_PROMPT)
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toEqual([])
  })

  // The superseded prompt survives as a memory, which is what makes "put it
  // back to the version that worked" a lookup rather than an archaeology
  // project (§15.7) — and what lets a later reviewer see its own history.
  //
  // The memory is best-effort BY DESIGN: the tool reports whether it stored one
  // and succeeds either way. So this asserts the tool's own report and the
  // memory agree — not that a memory must exist. The guarantee lives in the
  // config event, which the test above asserts.
  test('the rewrite reports its prompt-revision memory, and the report is true', async () => {
    await seedAcceptanceOrg(client)
    const session = await client.createSession({ job: 'reviewer-stand-in' })
    await client.sendMessage(session, 'hello')
    const mcp = sessionMCP(client.project, session)

    const result = await mcp.callOK('worker_prompt_write', {
      name: ANSWERER,
      system_prompt: 'You answer customer email. Be warm.',
      rationale: 'the answers read as curt',
    })

    const found = await mcp.callOK('memory_search', { label_selector: 'kind=prompt-revision' })
    if (result.prompt_revision.stored === true) {
      expect(found.count).toBeGreaterThan(0)
      expect(found.results[0].labels).toMatchObject({ kind: 'prompt-revision', worker: ANSWERER })
    } else {
      // The documented degraded path: the prompt still changed, and the tool
      // said so rather than pretending the write failed.
      expect(String(result.warning ?? '')).not.toBe('')
      expect((await client.getWorker(ANSWERER)).system_prompt).toContain('Be warm.')
    }
  })

  // The boundary that keeps every prompt rewrite auditable: worker_update can
  // change anything about a worker EXCEPT its prompt, so there is no way to
  // move a prompt without a rationale (§15.5). A regression here would not
  // break anything visibly — it would just quietly make the changelog lie.
  test('worker_update refuses to touch a system prompt, and names the tool that can', async () => {
    await seedAcceptanceOrg(client)
    const session = await client.createSession({ job: 'reviewer-stand-in' })
    await client.sendMessage(session, 'hello')
    const mcp = sessionMCP(client.project, session)

    const out = await mcp.call('worker_update', {
      name: ANSWERER,
      fields: { system_prompt: 'sneaking a rewrite past the audit trail' },
    })
    expect(out.isError).toBe(true)
    expect(out.text).toContain('worker_prompt_write')
    // Nothing moved, and nothing was logged as if it had.
    expect((await client.getWorker(ANSWERER)).system_prompt).toBe(ANSWERER_PROMPT)
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toEqual([])
  })

  // ── The change becomes an event other workers can react to ────────────────

  // §15.4: the config log records what changed, and `config.changed` is how the
  // rest of the project finds out. Emitted AFTER commit, never inside the
  // transaction — a routed event must not exist for a change that rolled back.
  test('a prompt rewrite emits a routable config.changed naming its record', async () => {
    await seedAcceptanceOrg(client)
    await client.postEvent({ type: 'email.received', text: 'From: bob\n\nstill broken' })

    // Made from the reviewer's real job, because the envelope depends on it:
    // a change made BY a job is stamped `worker` and carries that job's depth,
    // which is what keeps §8.4's loop floor working when something subscribes
    // to config.changed. A change made from a plain session is stamped
    // `external` at depth 0 — correctly, since that is a human-shaped edit.
    const fanout = await client.waitForDeliveries(
      (rows) => rows.length >= 3 && rows.every((d) => d.session_id !== ''),
      { timeoutMs: 120_000 },
    )
    let reviewerSession = ''
    for (const d of fanout) {
      if ((await client.getSession(d.session_id)).worker === REVIEWER) reviewerSession = d.session_id
    }
    expect(reviewerSession).not.toBe('')

    const why = 'the answers read as curt'
    await sessionMCP(client.project, reviewerSession).callOK('worker_prompt_write', {
      name: ANSWERER,
      system_prompt: 'You answer customer email. Be warm.',
      rationale: why,
    })

    const rewrite = await waitForConfigAction(client, 'worker_prompt_write')
    const changed = await client.waitForEvents((rows) => rows.length > 0, {
      type: 'config.changed',
      timeoutMs: 60_000,
    })

    // The event names the record, so a subscriber fetches the full before/after
    // rather than the event trying to carry it.
    const announcement = changed[0]
    expect(announcement.text).toContain(rewrite.id)
    expect(announcement.text).toContain('worker_prompt_write')
    // The rationale travels with it: a worker reacting to the change gets the
    // why without a second lookup.
    expect(announcement.text).toContain(why)
    // Stamped as coming from a worker, with a depth that keeps the loop floor
    // meaningful for anything subscribed to it.
    expect(announcement.envelope.source).toBe('worker')
    expect(announcement.envelope.depth).toBeGreaterThan(0)
  })

})

// §8.8 — the shape the first real deployment takes: one human-seeded manager
// that builds the rest of the workforce from its own prompt. G1 must cover it
// because it is what "how do you go from no workers to a workforce" answers.
test.describe('G1 §8.8 — the marketing-manager shape', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(240_000)

  const MANAGER = 'marketing-manager'
  // The marker is what the mock script matches on. It has to be a token unique
  // to THIS worker's prompt: matching on 'tweet-author' would also fire inside
  // the manager's own job, because the manager's prompt names the workers it is
  // supposed to create.
  const MANAGER_PROMPT = [
    'You own BadCode marketing.',
    'The workforce that should exist: a tweet-author posting daily,',
    'an instagram-image-maker, and a secretary handling inbound mail.',
  ].join('\n')
  // The marker goes in the schedule INPUT, not the worker's prompt: the input
  // becomes the event text and therefore the job's first user message, which is
  // what the mock proxy matches on. A marker in the system prompt does not
  // match — see the (G1) finding about composed prompts and the model request.
  const RECONCILE =
    '[G1-MARKER-RECONCILE] Reconcile the workforce: ensure every worker, schedule, and ' +
    'subscription described in your system prompt exists and matches; create or update via ' +
    'your tools. Report what you changed.'
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
      system_prompt: MANAGER_PROMPT,
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

  // The §8.8 claim in one test: no bootstrap code path exists. The org appears
  // because a worker read its own prompt, on a schedule, and called a tool.
  //
  // Needs a scripted model, since the mock model does not choose tool calls:
  //   ./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/g1-acceptance.json
  //
  // STATUS 2026-07-26: runs only with --mock-script, and FAILS there. The
  // scripted tool call demonstrably reaches the model (a marker in the trigger
  // text produces tool_use_start/tool_use_end and the turn-1 reply), but no
  // worker appears and the tool result carries no error I could capture. Two
  // things are known and worth starting from:
  //   * a marker in the worker's SYSTEM PROMPT never matches, though the
  //     composed prompt provably contains it and dispatch.go hands it to the
  //     session as SystemPrompt — so markers must go in the trigger text;
  //   * with the marker in the trigger text the model does call
  //     mcp__core__worker_create, and the worker still is not created.
  // Left runnable rather than deleted: it is one `--mock-script` away from
  // being the §8.8 proof, and the gap it names is real.
  test('the daily reconcile hires the worker its prompt describes', async () => {
    test.skip(
      !process.env.STACK_MOCK_SCRIPT,
      'needs a scripted model: ./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/g1-acceptance.json',
    )
    await client.putWorker(MANAGER, {
      description: 'owns marketing strategy and the workforce that delivers it',
      system_prompt: MANAGER_PROMPT,
    })
    // Every minute, so the scheduler's next sweep picks it up. The daily cron of
    // the test above is the realistic one; this is the same mechanism on a
    // timescale a test can wait for.
    await client.createSchedule({
      worker: MANAGER,
      cron: '* * * * *',
      input: RECONCILE,
      rationale: 'reconcile the workforce (§8.8)',
    })

    // The schedule fires, the router dispatches a manager job, and the manager
    // calls worker_create — nothing here creates the worker but the worker.
    const hired = await poll(
      () => client.listWorkers(),
      (rows) => rows.some((w) => w.name === 'tweet-author'),
      180_000,
      'the manager to hire the worker its prompt describes',
    )
    const author = hired.find((w) => w.name === 'tweet-author')!
    expect(author.enabled).toBe(true)
    expect(author.system_prompt).not.toBe('')

    // The config log says a WORKER did the hiring, from a job — not a human.
    const record = await waitForConfigAction(client, 'worker_create')
    const byManager = (await client.configEvents({ action: 'worker_create' })).find(
      (e) => String(e.payload.name) === 'tweet-author',
    )!
    expect(byManager.actor_worker).toBe(MANAGER)
    expect(byManager.actor_session).not.toBe('')
    expect(record).toBeTruthy()

    // …and the job that did it was started by the schedule, not by a human.
    const session = await client.getSession(byManager.actor_session)
    expect(session.worker).toBe(MANAGER)
  })

  // Staged autonomy (§8.8 step 3): a content worker drafts, then asks a human
  // before acting. The pause is a real state the delivery sits in, not a
  // convention — which is what lets a human answer hours later.
  // Staged autonomy (§8.8 step 3): a content worker drafts, then asks a human
  // before acting. What works today is the asking.
  test('a content worker asks a human for sign-off, and gets a link back to itself', async () => {
    test.skip(
      !process.env.STACK_MOCK_SCRIPT,
      'needs a scripted model: ./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/g1-acceptance.json',
    )
    await client.putWorker('tweet-author', {
      description: 'writes the daily tweet',
      system_prompt:
        'You write BadCode tweets. Draft it, then call request_human_attention to get ' +
        'sign-off before posting.',
    })
    await client.createSubscription({ event_type: 'content.requested', worker: 'tweet-author' })
    const event = await client.postEvent({
      type: 'content.requested',
      text: '[G1-MARKER-SIGNOFF] today: ship something small',
    })

    const [delivery] = await client.waitForDeliveries(
      (rows) => rows.length > 0 && rows.every((d) => d.session_id !== ''),
      { event_id: event.id, timeoutMs: 180_000 },
    )
    const calls = await poll(
      async () => (await client.queryEvents(delivery.session_id)).filter((e) => e.type === 'tool_use_end'),
      (rows) => rows.length > 0,
      120_000,
      'the content worker to ask for attention',
    )
    // The MODEL chose to call it — this is the autonomous half of §8.8.
    const result = JSON.parse(String(calls[0].data.output))
    expect(calls[0].data.isError).toBe(false)
    // The human is handed a link back to the conversation, so answering means
    // reading the draft in context rather than in a notification.
    expect(result.session_url).toBe(client.permalink(delivery.session_id))
    expect(String(result.message)).toContain('sign-off')
    // With no attention channel configured the tool still succeeds and says so,
    // rather than failing a job over a missing webhook (§9).
    expect(result.channel).toBe('none')
    expect(result.delivered).toBe(false)
    expect(result.request_id).toBeTruthy()
  })

  // KNOWN GAP — left failing deliberately, because the product is incomplete.
  //
  // Asking for attention does not pause the job. The tool records the request
  // and returns cleanly (the test above), but the delivery runs to `ok` with an
  // `ended_at`, and the `worker.finished` envelope carries
  // `attention_requested: false`. §8.4 wants the delivery parked at
  // `awaiting_human` with no `ended_at` — a pause, not a finish — so the UI can
  // show an open-ended duration and a human can answer hours later.
  //
  // This is the gap E2 flagged when it wrote `attention_requested` as a
  // parameter the Runner passes `false`: "H2 must add a session-level flag and
  // one line in emitJobOutcome". The flag still is not there, so from the
  // outside a job that asked for sign-off is indistinguishable from one that
  // finished its work.
  test('asking for attention pauses the job (KNOWN GAP: the delivery still completes)', async () => {
    test.skip(
      !process.env.STACK_MOCK_SCRIPT,
      'needs a scripted model: ./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/g1-acceptance.json',
    )
    await client.putWorker('tweet-author', {
      description: 'writes the daily tweet',
      system_prompt: 'You write BadCode tweets. Ask for sign-off before posting.',
    })
    await client.createSubscription({ event_type: 'content.requested', worker: 'tweet-author' })
    const event = await client.postEvent({
      type: 'content.requested',
      text: '[G1-MARKER-SIGNOFF] today: ship something small',
    })

    const parked = await client.waitForDeliveries(
      (rows) => rows.some((d) => d.status === 'awaiting_human'),
      { event_id: event.id, timeoutMs: 120_000 },
    )
    const waiting = parked.find((d) => d.status === 'awaiting_human')!
    // Awaiting a human is a pause, not an end.
    expect(waiting.ended_at).toBe(0)

    // …and the envelope says so, so a reviewer can skip work that is knowingly
    // half-done rather than judging it as finished.
    const finished = await client.waitForEvents((rows) => rows.length > 0, { type: 'worker.finished' })
    expect(finished[0].envelope.attention_requested).toBe(true)
  })
})
