import { test, expect } from '@playwright/test'
import { newProjectClient, ProjectClient } from '../helpers/api'
import { removeSandboxContainer, sandboxContainerExists } from '../helpers/occupancy'

// Feature e2e: a host that cannot start another session says so, in words that
// stop you re-creating the session (the port-pool half of `6dc27ac`).
//
// For most of this suite's life this path was untestable, and it showed. Every
// live session leases one host port and holds it until deleted, the pool was
// hardcoded 100 wide, so reaching exhaustion meant a hundred live containers —
// which nobody was going to do on purpose, and which therefore only ever
// happened by accident, in somebody else's run, an hour later. The error it
// produced then said "session has no running instance and no snapshot — session
// must be re-created": a description of a lost session, inviting the one action
// guaranteed to fail identically. It was misdiagnosed twice, confidently.
//
// `14ec184` made the range configuration (AGENTKIT_PORT_RANGE_START/END), which
// is what makes this test possible: `--port-pool 3` boots agentd with a pool of
// three and the real error arrives, at a real caller, in seconds. Nothing here
// simulates the failure — the pool really is full, and the sessions holding it
// are really running.
//
// Run it:
//
//   ./e2e/run-stack-e2e.sh test --port-pool 3 -- features/port-pool.stack.spec.ts
//
// It SKIPS on an ordinary run, because on a 100-port pool it would either take
// a hundred containers or prove nothing.

const POOL = Number(process.env.STACK_PORT_POOL) || 0
const RANGE = process.env.STACK_PORT_RANGE || ''

test.describe('a full host says it is full (§ port pool)', () => {
  test.describe.configure({ mode: 'serial' })

  test.skip(
    POOL === 0,
    'needs a deliberately narrow pool: ./e2e/run-stack-e2e.sh test --port-pool 3 -- features/port-pool.stack.spec.ts',
  )

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-pool')
  })

  test.afterEach(async () => {
    // Non-negotiable here of all places: this test's whole method is to fill the
    // host, and a run that left it full would hand the next person exactly the
    // incident the test is about.
    await client?.cleanup()
  })

  test('the session past the last port is told the HOST is full, not that it is lost', async () => {
    test.setTimeout(4 * 60_000)

    // Fill the pool. Every one of these must succeed — if the host cannot even
    // reach its own ceiling, the run is measuring something else.
    //
    // `createSession` resolving proves nothing: the route answers 200 with
    // status "creating" and provisions in the BACKGROUND. Waiting for each
    // session to leave "creating" is what actually takes the port, and without
    // it this test raced its own setup — the pool was not yet full when it
    // asked for one more, and the first version of it passed a run in which
    // nothing had been provisioned at all.
    for (let i = 0; i < POOL; i++) {
      const id = await client.createSession({})
      expect(id, `session ${i + 1} of ${POOL} should start on a ${POOL}-port pool`).toBeTruthy()
      await client.waitForCreatesToSettle()
      const row = (await client.listAllSessions()).find((s) => s.id === id)
      expect(row?.status, `session ${i + 1} of ${POOL} should be running, not ${row?.status}`).toBe('running')
    }

    // …and ask for one more than exists.
    //
    // Where the complaint arrives is not obvious, and the test must not assume:
    // `POST /agent/session` provisions in the BACKGROUND, so it answers with an
    // id before the failure exists, and the message route answers 200 and
    // streams the turn — so an error can arrive as a rejected promise OR as
    // text inside a stream that "succeeded". A test that watched only for a
    // thrown error would have called this silence a pass. It did, once.
    let failure = ''
    try {
      const extra = await client.createSession({})
      await client.waitForCreatesToSettle()
      failure = await client.sendMessage(extra, 'hello').then(
        (stream) => stream,
        (e: unknown) => String(e),
      )
    } catch (e) {
      failure = String(e)
    }

    // `failure` now holds whatever the caller was told, by either route. It
    // being non-empty is only the floor — silence would mean the caller learned
    // nothing at all, which is its own bug. The clauses below are the real
    // assertion.
    expect(failure, `the ${POOL + 1}th session's caller was told nothing at all`).not.toBe('')

    // The four clauses that make this message worth having, each of which was
    // missing from the one it replaced:
    //   WHICH resource, HOW big it is, WHAT holds it, and — the one that would
    //   have saved a day — that this is the host's state and not the session's.
    expect(failure).toContain('the host port pool is exhausted')
    expect(failure).toContain(`all ${POOL} ports in ${RANGE} are leased`)
    expect(failure).toContain('a session holds its port until it is deleted')
    expect(failure).toContain('a host capacity limit, not a lost or broken session')

    // And it must NOT be the old message, which sent two people to re-create a
    // session on a host where every create fails the same way.
    expect(failure).not.toContain('session must be re-created')

    // Capacity is asked of the host live and never written to the row — because
    // "the host is full" is true for one instant and stops being true the
    // moment somebody deletes a session, so storing it would plant a reason
    // guaranteed to go stale. An empty `create_error` here is that decision,
    // and it is the half a reader would otherwise never see: every other
    // failure DOES store its reason, so a future refactor that "helpfully"
    // stored this one too would look like an improvement.
    const failed = (await client.listAllSessions()).find((s) => s.status !== 'running')
    expect(failed?.id, 'the session that could not start should still be listed').toBeTruthy()
    expect((await client.getSession(failed!.id!)).create_error ?? '').toBe('')

    // Releasing one port makes the host usable again — which is what "capacity
    // limit" claims and what "lost session" would have denied. Without this the
    // test would prove only that a full pool produces nice prose.
    const [first] = await client.listAllSessions()
    expect(first?.id).toBeTruthy()
    await client.deleteSession(first!.id!)

    const recovered = await client.createSession({})
    expect(recovered, 'a freed port must let the next session start').toBeTruthy()
  })
})

// ── What actually drains the pool ───────────────────────────────────────────
//
// Needs no narrow pool: it is about one session, and it runs on any stack.
test.describe('a deleted session leaves nothing behind', () => {
  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-orphan')
  })

  test.afterEach(async () => {
    await client?.cleanup()
  })

  test('deleting a session mid-create does not leave an orphaned container (KNOWN GAP: it does)', async () => {
    test.setTimeout(2 * 60_000)

    // `POST /agent/session` answers 200 with status "creating" and provisions
    // in the background. Delete before that finishes and the row goes; the
    // container arrives afterwards, belonging to nothing.
    const id = await client.createSession({})
    await client.deleteSession(id)

    // Long enough for the background create to land — measured at ~14s.
    await new Promise((r) => setTimeout(r, 45_000))

    // The session is gone from the API's point of view…
    const rows = await client.listAllSessions()
    expect(rows.find((s) => s.id === id), 'the session row should be gone').toBeUndefined()

    // …and its container should be gone with it. It is not: it comes up healthy
    // after the delete, holds one of the host's session ports, and nothing ever
    // reaps it — not the archive loop, which only knows about sessions, and not
    // `clean`, until a human runs it. Every such orphan permanently costs the
    // host one concurrent session, which is a far better explanation of how
    // this stack kept filling up than anything previously written down.
    //
    // Leave this red until a delete cancels or joins its in-flight create.
    //
    // The `finally` is not tidiness — it is what stops this test being the very
    // thing it documents. An orphan has no session row, so `cleanup()` cannot
    // reach it and it would survive every run, and the leak check would then
    // fail every later run for a container this test left. Removing it needs
    // the reach past the API that `removeSandboxContainer` exists for.
    try {
      expect(
        await sandboxContainerExists(id),
        `container sandbox-${id} outlived the session it belonged to`,
      ).toBe(false)
    } finally {
      await removeSandboxContainer(id)
    }
  })
})
