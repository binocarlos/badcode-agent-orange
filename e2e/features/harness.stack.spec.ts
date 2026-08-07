import { test, expect } from '@playwright/test'
import {
  MAPPED_PROJECTS,
  login,
  mappedProjectClient,
  newProjectClient,
  permalinkBase,
  sessionPermalink,
} from '../helpers/api'

// Harness self-check: proves the fixtures in helpers/api.ts do what they claim,
// so a feature spec that uses one and fails is failing about its feature.
//
// Every fixture the suite exports is exercised somewhere — the ones the first
// feature spec does not need are exercised here rather than left to rot until
// the track that needs them discovers they were wrong.

test.describe('harness fixtures', () => {
  test('login yields the mapped projects and a wildcard token for new ones', async ({ request }) => {
    const auth = await login(request)
    expect(auth.email).toBe('test@example.com')
    expect(auth.projects.map((p) => p.id).sort()).toEqual([...MAPPED_PROJECTS].sort())
    // The test account is an implicit wildcard: it can mint tokens for project
    // ids that do not exist yet, which is how a test gets a private project.
    expect(auth.wildcard).toBe(true)
    expect(auth.login_token).toBeTruthy()

    const mapped = await mappedProjectClient(request, MAPPED_PROJECTS[0])
    expect(mapped.project).toBe(MAPPED_PROJECTS[0])
    // A mapped project is shared with other specs and other runs, so assert
    // only that it answers — never that it is empty.
    expect(Array.isArray(await mapped.listWorkers())).toBe(true)
  })

  test('newProjectClient hands back a private, empty project each time', async ({ request }) => {
    const a = await newProjectClient(request, 'e2e-harness')
    const b = await newProjectClient(request, 'e2e-harness')
    expect(a.project).not.toBe(b.project)
    for (const c of [a, b]) {
      expect(await c.listWorkers()).toEqual([])
      expect(await c.listSubscriptions()).toEqual([])
      expect(await c.listEvents()).toEqual([])
      expect(await c.listDeliveries()).toEqual([])
    }
  })

  test('the polling fixtures resolve as soon as the predicate holds', async ({ request }) => {
    const c = await newProjectClient(request, 'e2e-harness')

    // Satisfied immediately — this asserts the poll returns rather than sleeps.
    const started = Date.now()
    expect(await c.waitForDeliveries((rows) => rows.length === 0, { timeoutMs: 5_000 })).toEqual([])
    expect(Date.now() - started).toBeLessThan(5_000)

    // And a real wait: the event the fixture posts must become visible in the log.
    const posted = await c.postEvent({ type: 'harness.ping', text: 'ping' })
    const seen = await c.waitForEvents((rows) => rows.some((e) => e.id === posted.id), {
      type: 'harness.ping',
      timeoutMs: 10_000,
    })
    expect(seen.map((e) => e.id)).toContain(posted.id)

    // The failure path reports what it last saw instead of a bare timeout.
    await expect(
      c.waitForDeliveries((rows) => rows.length > 0, { timeoutMs: 1_000 }),
    ).rejects.toThrow(/timed out after 1000ms waiting for deliveries/)
  })

  test('the session permalink has the documented shape and the web app serves it', async ({
    request,
  }) => {
    // The format is the contract between cmd/agentd/permalink.go and
    // web/src/permalink.ts: <public base>/p/<project>/s/<session>.
    expect(sessionPermalink('apples-oranges', 'abc-123')).toBe(
      `${permalinkBase()}/p/apples-oranges/s/abc-123`,
    )
    // Ids are path-escaped so the link round-trips through the UI's parser.
    expect(sessionPermalink('a/b', 'c d')).toBe(`${permalinkBase()}/p/a%2Fb/s/c%20d`)

    // The route resolves in the running stack (the SPA fallback serves it).
    // This is a routing check, not a rendering one: asserting the session view
    // renders needs a real session, which belongs to the session-lifecycle spec.
    const resp = await request.get('/p/apples-oranges/s/does-not-exist')
    expect(resp.status()).toBe(200)
    expect(resp.headers()['content-type']).toContain('text/html')
  })
})
