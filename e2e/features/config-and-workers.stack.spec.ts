import { test, expect } from '@playwright/test'
import { newProjectClient, ProjectClient } from '../helpers/api'
import { configEvents, waitForConfigEvents } from '../helpers/configlog'

// Feature e2e: project settings (§5) + workers (§6) through the real HTTP API
// against the running stack, with the config log (§15) checked after every
// mutation.
//
// Three claims, in order of how much they'd hurt if they were false:
//
//  1. The CRUD round-trips: what you PUT is what you GET back, and PUT replaces
//     rather than patches.
//  2. A project is a hard namespace: a token for project A can neither read nor
//     write project B, and same-named rows in two projects are two rows.
//  3. Every configuration mutation left a config-log record with the action
//     §15.3 names — proved against the log itself, not against the store's own
//     unit tests.
//
// The CRUD describe below is deliberately free of the router, the scheduler and
// the model — it is about what settings and workers ARE. The second describe
// (added by TOK1) is about the one project setting whose meaning is a
// behaviour rather than a stored field: `daily_tokens_hard` only exists to stop
// jobs, so proving it round-trips proves nothing. That one needs a real job.

test.describe('project settings + workers + config log', () => {
  test.describe.configure({ mode: 'serial' })

  // Workers created per test, removed afterwards so a long-lived stack stays
  // tidy. Projects themselves are run-scoped and need no cleanup: a project is
  // just a name that exists because something carries it.
  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-cfg')
  })

  test('a fresh project reads back the spec defaults', async () => {
    const settings = await client.getSettings()

    // §5: an unwritten project is not an error and not a zero struct — the row
    // is created lazily and the defaults are what a reader sees.
    expect(settings.project).toBe(client.project)
    expect(settings.max_concurrent_jobs).toBe(4)
    expect(settings.briefing_max_bytes).toBe(2048)
    expect(settings.snapshot_ttl_days).toBe(30)
    expect(settings.system_prompt).toBe('')
    expect(settings.daily_tokens_soft).toBe(0)
    expect(settings.daily_tokens_hard).toBe(0)

    // Reading defaults is not a write: nothing was logged.
    expect(await configEvents(client)).toHaveLength(0)
    expect(await client.listWorkers()).toEqual([])
  })

  test('project settings PUT is whole-object and logs project_settings_put', async () => {
    const first = await client.putSettings({
      system_prompt: 'Answer customer email. Be brief.',
      base_image: 'acme/base:v1',
      daily_tokens_hard: 500,
      snapshot_ttl_days: 90,
    })
    expect(first.system_prompt).toBe('Answer customer email. Be brief.')
    expect(first.base_image).toBe('acme/base:v1')
    expect(first.daily_tokens_hard).toBe(500)
    // Unset numerics take the spec defaults, not zero.
    expect(first.max_concurrent_jobs).toBe(4)

    // A second PUT replaces the whole object: the fields it omits go back to
    // their defaults rather than surviving from the first write (§5).
    const second = await client.putSettings({ system_prompt: 'Be terse.' })
    expect(second.system_prompt).toBe('Be terse.')
    expect(second.base_image).toBe('')
    expect(second.daily_tokens_hard).toBe(0)

    // …and a re-read agrees with what the write echoed.
    expect(await client.getSettings()).toMatchObject({
      system_prompt: 'Be terse.',
      base_image: '',
      daily_tokens_hard: 0,
    })

    const log = await waitForConfigEvents(client, 2)
    expect(log.map((e) => e.action)).toEqual(['project_settings_put', 'project_settings_put'])
    // Newest first, and the payload is the full row after the write (§15.2).
    expect(log[0].payload).toMatchObject({ system_prompt: 'Be terse.', base_image: '' })
    // An edit over HTTP is a human/API edit: no actor (§15.2).
    expect(log[0].actor_worker).toBe('')
    expect(log[0].actor_session).toBe('')
  })

  test('worker CRUD round-trips and every mutation picks its §15.3 action', async () => {
    // ── create ──────────────────────────────────────────────────────────────
    const created = await client.putWorker('email-answerer', {
      description: 'answers inbound email',
      system_prompt: 'You answer customer email.',
      briefing: ['kind=house-style'],
      max_instances: 2,
    })
    expect(created).toMatchObject({
      project: client.project,
      name: 'email-answerer',
      description: 'answers inbound email',
      max_instances: 2,
      enabled: true, // the default a new worker gets (§6.1)
      briefing: ['kind=house-style'],
    })

    // ── read back ───────────────────────────────────────────────────────────
    expect(await client.getWorker('email-answerer')).toMatchObject({
      description: 'answers inbound email',
      system_prompt: 'You answer customer email.',
      max_instances: 2,
    })
    expect((await client.listWorkers()).map((w) => w.name)).toEqual(['email-answerer'])

    // ── update: a body change ───────────────────────────────────────────────
    const updated = await client.putWorker('email-answerer', {
      description: 'answers inbound customer email',
      system_prompt: 'You answer customer email. Be brief.',
      max_instances: 2,
    })
    expect(updated.description).toBe('answers inbound customer email')
    // PUT replaces: the briefing the create set is gone because this body omitted it.
    expect(updated.briefing ?? null).toBeNull()

    // ── disable: the same row with one field flipped ────────────────────────
    const disabled = await client.toggleWorkerEnabled('email-answerer', false)
    expect(disabled.enabled).toBe(false)
    expect(disabled.description).toBe('answers inbound customer email')

    // ── enable again ────────────────────────────────────────────────────────
    expect((await client.toggleWorkerEnabled('email-answerer', true)).enabled).toBe(true)

    // ── delete ──────────────────────────────────────────────────────────────
    await client.deleteWorker('email-answerer')
    expect(await client.listWorkers()).toEqual([])
    expect((await client.raw('GET', '/agent/workers/email-answerer')).status()).toBe(404)

    // ── the log tells the whole story, in order ─────────────────────────────
    const log = await waitForConfigEvents(client, 5)
    const actions = log.map((e) => e.action).reverse() // oldest first, as it happened
    expect(actions).toEqual([
      'worker_create',
      'worker_update',
      'worker_disable',
      'worker_enable',
      'worker_delete',
    ])

    // The delete carries the row as it last stood (§15.3 rule 2) — which is
    // what makes restoring a retired worker a lookup rather than archaeology.
    const deleted = log[0]
    expect(deleted.action).toBe('worker_delete')
    expect(deleted.payload).toMatchObject({
      name: 'email-answerer',
      description: 'answers inbound customer email',
      enabled: true,
      max_instances: 2,
    })
  })

  test('subscription CRUD logs create/update/delete, and posting an event logs nothing', async () => {
    await client.putWorker('email-answerer', { description: 'answers email' })

    const sub = await client.createSubscription({
      event_type: 'email.received',
      worker: 'email-answerer',
      filter: { interactive: false },
    })
    expect(sub).toMatchObject({
      project: client.project,
      event_type: 'email.received',
      worker: 'email-answerer',
      enabled: true, // live unless you say otherwise
      max_firings_per_hour: 0, // 0 = unlimited
    })

    await client.updateSubscription(sub.id, { max_firings_per_hour: 12 })
    expect((await client.listSubscriptions())[0].max_firings_per_hour).toBe(12)

    // An event is NOT configuration: §15.3 rule 3 keeps the event spine out of
    // the config log, because project_events is already its own append-only log.
    const event = await client.postEvent({ type: 'email.received', text: 'From: bob' })
    expect(event.envelope).toMatchObject({ source: 'external', depth: 0 })
    expect((await client.listEvents({ type: 'email.received' })).map((e) => e.id)).toContain(event.id)

    await client.deleteSubscription(sub.id)
    expect(await client.listSubscriptions()).toEqual([])

    const actions = (await waitForConfigEvents(client, 4)).map((e) => e.action).reverse()
    expect(actions).toEqual([
      'worker_create',
      'subscription_create',
      'subscription_update',
      'subscription_delete',
    ])
  })

  test('a project is a hard namespace: another project is unreachable, not merely empty', async ({
    request,
  }) => {
    const other = await newProjectClient(request, 'e2e-other')
    expect(other.project).not.toBe(client.project)

    await client.putWorker('shared-name', { description: 'mine' })
    await client.putSettings({ system_prompt: 'my prompt' })

    // Reads: the other project sees none of it. A row in another project looks
    // like a missing row — never like a 403, which would confirm it exists.
    expect(await other.listWorkers()).toEqual([])
    expect((await other.raw('GET', '/agent/workers/shared-name')).status()).toBe(404)
    expect((await other.getSettings()).system_prompt).toBe('')

    // Writes: the same name in the other project is a different row, and
    // writing it leaves mine untouched.
    await other.putWorker('shared-name', { description: 'theirs' })
    expect((await other.getWorker('shared-name')).description).toBe('theirs')
    expect((await client.getWorker('shared-name')).description).toBe('mine')

    // Deleting from the other project cannot reach mine.
    await other.deleteWorker('shared-name')
    expect((await client.getWorker('shared-name')).description).toBe('mine')

    // Events do not cross either. Note the other project's log is NOT empty:
    // every configuration mutation now emits its own `config.changed` (J3), so
    // the isolation claim is "none of mine are in there", not "nothing is".
    const mineEvent = await client.postEvent({ type: 'email.received', text: 'mine' })
    const theirEvents = await other.listEvents()
    expect(theirEvents.map((e) => e.id)).not.toContain(mineEvent.id)
    expect(theirEvents.every((e) => e.project === other.project)).toBe(true)

    // And neither does the config log: each project's history is its own.
    const mine = await configEvents(client)
    const theirs = await configEvents(other)
    expect(mine.every((e) => e.project === client.project)).toBe(true)
    expect(theirs.every((e) => e.project === other.project)).toBe(true)
    expect(mine.map((e) => e.action).reverse()).toEqual([
      'worker_create',
      'project_settings_put',
    ])
    expect(theirs.map((e) => e.action).reverse()).toEqual(['worker_create', 'worker_delete'])
  })

  test('an unauthenticated or malformed token reaches nothing', async ({ request }) => {
    await client.putWorker('email-answerer', { description: 'answers email' })

    // No Authorization header at all.
    expect((await request.get('/agent/workers')).status()).toBe(401)
    // A syntactically valid but unsigned/garbage bearer token.
    expect(
      (await request.get('/agent/workers', { headers: { Authorization: 'Bearer not-a-jwt' } })).status(),
    ).toBe(401)
    // …and the same for a write.
    expect(
      (
        await request.put('/agent/workers/sneaky', {
          headers: { Authorization: 'Bearer not-a-jwt' },
          data: { description: 'should not exist' },
        })
      ).status(),
    ).toBe(401)
    expect((await client.listWorkers()).map((w) => w.name)).toEqual(['email-answerer'])
  })
})

// ---------------------------------------------------------------------------
// The daily token budget (§5 / §8.4 step 6) — added by TOK1.
//
// This is the only test in the suite that watches a project setting DO
// something, and it exists because the setting did nothing at all. The router's
// budget gate measures spend with `agentdb.CountProjectTokensSince`, which
// summed a jsonb path (`events->0->>'input_tokens'`) that no stored row has
// ever had: usage is nested under `data.usage`, camelCase, on the LAST envelope.
// Measured on this very stack's Postgres: 942 stored query rows, the production
// SQL summed 0, the real usage summed 20,980. A ceiling measured against a
// constant zero cannot fire, so `daily_tokens_soft`/`hard` were inert
// product-wide — and every unit test of the gate's POLICY was green, because
// they all fed it a number from a fake store.
//
// So the two claims here are the two halves that were never joined:
//
//  1. a finished mock job leaves non-zero, readable token usage behind
//     (the mock model bills 10 in / 6 out per turn);
//  2. with a hard budget below that spend, the next job does NOT start — and
//     lifting the budget lets the same queued delivery through. The lift is
//     what makes it a proof rather than a coincidence: a delivery that stays
//     pending for other reasons would stay pending after the lift too.
// ---------------------------------------------------------------------------

test.describe('the daily token budget stops jobs once real spend crosses it', () => {
  test.describe.configure({ mode: 'serial' })

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-budget')
  })

  test.afterEach(async () => {
    // Subscriptions first, then sessions — a job that finishes while sessions
    // are being swept starts another one. cleanup() does both in that order.
    await client.cleanup()
  })

  test('a finished job is counted, and daily_tokens_hard then refuses the next', async () => {
    // Two container-backed jobs, each provisioned from scratch.
    test.setTimeout(420_000)

    await client.putWorker('token-spender', {
      description: 'runs so the ledger has something to count',
      system_prompt: 'Answer in one short sentence.',
    })
    await client.createSubscription({ event_type: 'budget.probe', worker: 'token-spender' })

    // ── 1. One job runs, and its tokens are readable ────────────────────────
    const first = await client.postEvent({ type: 'budget.probe', text: 'first probe' })
    const firstRows = await client.waitForDeliveries((rows) => rows.some((r) => r.status === 'ok'), {
      event_id: first.id,
      timeoutMs: 300_000,
    })
    const firstDelivery = firstRows.find((r) => r.status === 'ok')!
    expect(firstDelivery.session_id).not.toBe('')

    // The surface the browser reads (web/src/events.ts sumTokens reduces this
    // exact body). Asserting the PATH, not just the total: a sum that happens
    // to be non-zero for some other reason would not prove the readers agree
    // with the writer.
    const envelopes = await client.queryEvents(firstDelivery.session_id)
    const completions = envelopes.filter((e) => e.type === 'query_complete')
    expect(completions.length).toBeGreaterThan(0)

    let spent = 0
    for (const e of completions) {
      const usage = e.data.usage as Record<string, unknown> | undefined
      expect(usage, 'query_complete must carry data.usage — the whole ledger hangs off it').toBeTruthy()
      // All three billed input components, matching go/agentdb/token_usage.go.
      // RD2: summing only `inputTokens` here would agree with the old broken
      // reader instead of with the writer — the mistake this spec exists to
      // catch. The mock bills a cache write and a cache read like a real turn.
      spent +=
        Number(usage!.inputTokens ?? 0) +
        Number(usage!.cacheCreationInputTokens ?? 0) +
        Number(usage!.cacheReadInputTokens ?? 0) +
        Number(usage!.outputTokens ?? 0)
    }
    expect(spent, 'a finished mock job must bill more than zero tokens').toBeGreaterThan(0)

    // ── 2. A ceiling below that spend stops the next job ────────────────────
    await client.putSettings({ daily_tokens_hard: 1 })

    const second = await client.postEvent({ type: 'budget.probe', text: 'second probe' })
    // The delivery row is written by the router whatever the gate decides, so
    // wait for it to exist rather than sleeping.
    await client.waitForDeliveries((rows) => rows.length > 0, { event_id: second.id, timeoutMs: 60_000 })

    // Then hold it under observation for well over the time the first job took
    // to reach `running`. Proving a job did NOT start is the one assertion that
    // cannot be a happens-after signal; it is bounded by the first job's own
    // measured latency instead of by a guess.
    const held = Date.now() + 30_000
    while (Date.now() < held) {
      const rows = await client.listDeliveries({ event_id: second.id })
      expect(
        rows.map((r) => r.status),
        'daily_tokens_hard=1 with tokens already spent must keep the delivery pending',
      ).toEqual(['pending'])
      await new Promise((r) => setTimeout(r, 2_000))
    }

    // ── 3. Lifting the ceiling releases the same delivery ───────────────────
    // Without this the test would also pass if deliveries never ran at all.
    await client.putSettings({ daily_tokens_hard: 0 })
    const released = await client.waitForDeliveries((rows) => rows.some((r) => r.status === 'ok'), {
      event_id: second.id,
      timeoutMs: 300_000,
    })
    expect(released.map((r) => r.id)).toContain(
      (await client.listDeliveries({ event_id: second.id }))[0].id,
    )
  })
})
