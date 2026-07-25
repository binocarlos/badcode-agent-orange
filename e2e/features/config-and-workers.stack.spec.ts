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
// Deliberately NOT here: anything that needs the router, the scheduler, or a
// model. Those are unbuilt; see e2e/README.md for the queue.

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

    // Events do not cross either.
    await client.postEvent({ type: 'email.received', text: 'mine' })
    expect(await other.listEvents()).toEqual([])

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
