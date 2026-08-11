import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type ProjectClient, type TopologyBody } from '../helpers/api'
import { waitForConfigAction } from '../helpers/configlog'

// architect-archivist@v1 end to end — the recommended shape, and until this
// file existed the ONLY shape with no e2e at all.
//
// Why that mattered more than a coverage gap: the loop it describes could not
// run. Two independent defects, each invisible, each fixed on 2026-08-08:
//
//   1. `POST /agent/session` dropped `worker`. The console had been sending it
//      since WorkerChatPanel was written, encoding/json is not strict, and the
//      field simply vanished — so "Chat with <worker>" produced a bare session
//      with none of the worker's prompt. Nothing failed.
//   2. Because of (1) Session.Worker stayed empty, and emitIdleFinish refuses to
//      emit `worker.finished` for a session with no worker. So a human
//      conversation never emitted anything, and the archivist — whose entire job
//      is to be woken when a conversation goes quiet — could not be woken by
//      one. Ever.
//
// Both are load-bearing for this spec, and both would pass silently again if
// this file did not exist. That is the argument for it.
//
// ── The two mechanics you must know before editing ──────────────────────────
//
// A CONVERSATION ENDS BY GOING QUIET. There is no "end chat" call. An
// interactive session emits its worker.finished from the ARCHIVE SWEEP, once it
// has been idle past AGENTKIT_SESSION_IDLE_TIMEOUT. Hence --idle-timeout below:
// 30 minutes of waiting becomes one. agentd refuses anything under a minute
// (the sweep only runs once a minute), so ~1–2 minutes per quiescence is the
// floor and the reason this spec is slow.
//
// Do NOT "optimise" that by calling `POST /agent/session/{id}/archive`. That
// route is Snapshot+Destroy — the manual snapshot half — and does not run
// emitIdleFinish, so it produces no event and this spec would pass while
// testing nothing. (It also uses Destroy where the sweep uses
// teardownInstance(keepArtifactStatus), which is the RD12 distinction.)
//
// THE TURN INDEX RESETS ON EVERY USER MESSAGE. The in-image agent calls the SDK
// with persistSession:false and folds prior turns into the system prompt as
// prose (sandbox/src/harness/claude-agent-sdk.ts), so a chat's second message
// starts at turn 0 again. Multi-message chats are therefore sequenced by
// MARKERS the previous reply left behind, never by turn index — [AA-PROPOSED]
// then [AA-BUILT]. Only within a single dispatched job (one user message) does
// the turn index sequence anything, which is why the archivist's three turns
// work the ordinary way.
//
// ── Rule ordering in architect-archivist.json ───────────────────────────────
//
// The archivist's rule is FIRST and keyed on its identity phrase
// ("You are aa-scribe,", which only its own composed prompt holds). It has to be
// first: a worker.finished event's text is the finishing conversation's whole
// transcript, so the archivist's request contains every marker the designer
// ever emitted and would otherwise be answered by the designer's rules.
//
// Then [AA-BUILT] above [AA-PROPOSED] above the designer's identity, because
// each later state's request contains all the earlier markers.
//
// Names are mutually non-substring: aa-designer / aa-scribe / aa-baker /
// aa-runner.
//
// Run it:
//   ./e2e/run-stack-e2e.sh test mock \
//     --mock-script e2e/mock-scripts/architect-archivist.json --idle-timeout 1m \
//     -- e2e/features/architect-archivist.stack.spec.ts

const NEEDS_RIG =
  'needs the scripted model AND a short idle timeout: ./e2e/run-stack-e2e.sh test mock ' +
  '--mock-script e2e/mock-scripts/architect-archivist.json --idle-timeout 1m ' +
  '-- e2e/features/architect-archivist.stack.spec.ts'

const DESIGNER = 'aa-designer'
const SCRIBE = 'aa-scribe'
const BAKER = 'aa-baker'
const RUNNER = 'aa-runner'
const TASK_EVENT = 'aa.task'

// Byte-for-byte with e2e/mock-scripts/architect-archivist.json.
const PROPOSED = '[AA-PROPOSED]'
const BUILT = '[AA-BUILT]'
const ARCHIVED = '[AA-ARCHIVED]'
const REFUSAL = '[AA-FROZEN-REFUSAL]'
const RAN = '[AA-RAN]'

const GOAL = 'run a neighbourhood bakery'

function seedBody(): TopologyBody {
  return {
    name: 'architect-archivist',
    version: 'v1',
    answers: {
      goal: GOAL,
      'architect-name': DESIGNER,
      'archivist-name': SCRIBE,
    },
  }
}

/**
 * Waits for the conversation to go quiet and be archived, and returns the
 * worker.finished it emitted.
 *
 * The generous timeout is not padding. The sweep runs once a minute and
 * archives sessions idle for over a minute, so the worst case is two full
 * sweeps plus a snapshot of a real container.
 */
async function waitForConversationToEnd(client: ProjectClient, sessionId: string) {
  const events = await client.waitForEvents(
    (rows) => rows.some((e) => e.envelope.session_id === sessionId),
    { type: 'worker.finished', timeoutMs: 240_000 },
  )
  const ev = events.find((e) => e.envelope.session_id === sessionId)!
  expect(ev.envelope.interactive, 'a chat that goes quiet emits an INTERACTIVE worker.finished').toBe(true)
  return ev
}

test.describe('architect-archivist@v1 — the recommended shape', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(600_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-aa')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('the seed lays down two workers, one UNFILTERED edge, and a label registry', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_RIG)

    const body = seedBody()
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([DESIGNER, SCRIBE])
    expect(preview.diff.new_subscriptions).toEqual([
      { event_type: 'worker.finished', worker: SCRIBE },
    ])
    expect(preview.diff.new_schedules).toEqual([])

    await client.applyTopology(body)

    // THE assertion of this test. The subscription is unfiltered, and that is
    // only safe because the router suppresses self-delivery. It carried
    // `filter: {interactive: "true"}` until 2026-08-08 purely to dodge the
    // self-loop, and paid for it by never archiving a dispatched job. Re-adding
    // a filter here would silently halve the project's history.
    const subs = await client.listSubscriptions()
    expect(subs).toHaveLength(1)
    expect(subs[0]!.filter ?? {}, 'the one standing edge must be unfiltered').toEqual({})
    expect(subs[0]!.worker).toBe(SCRIBE)

    // Frozen: the humans own the memory schema.
    expect((await client.getWorker(SCRIBE)).frozen).toBe(true)

    // Leg 1 of the memory schema — the shared vocabulary, as data both workers
    // are briefed on rather than prose copied into two prompts.
    const registry = await client.raw('GET', '/agent/memories?selector=name%3Dlabel-registry')
    expect(registry.ok(), 'the seed plants a label registry').toBeTruthy()
    const body2 = (await registry.json()) as { memories?: Array<{ content?: string }> }
    expect(body2.memories?.length ?? 0).toBeGreaterThan(0)
    for (const w of [DESIGNER, SCRIBE]) {
      expect((await client.getWorker(w)).briefing).toContain('name=label-registry')
    }
  })

  test('you can talk to the architect, and it is the architect you are talking to', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_RIG)
    await client.applyTopology(seedBody())

    const sid = await client.createSession({ worker: DESIGNER })
    await client.waitForCreatesToSettle()

    // The identity half. Empty here means emitIdleFinish will never emit and
    // the whole downstream loop is dead — the defect this route carried in
    // silence until 2026-08-08.
    const row = await client.getSession(sid)
    expect(row.worker, 'the chat must BE the worker, not merely resemble one').toBe(DESIGNER)

    // The prompt half, proven by behaviour rather than by reading a field: the
    // scripted reply is keyed on "You are aa-designer," which appears only in
    // the architect's composed prompt. Getting it back means the prompt reached
    // the model.
    const reply = await client.sendMessage(sid, 'Design me an org for the bakery.')
    expect(reply).toContain(PROPOSED)
    expect(reply, 'the architect proposes before it acts').toContain('before I build anything')
  })

  test('a typo does not silently become a bare session', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_RIG)
    await client.applyTopology(seedBody())

    // The old behaviour — accept anything, hand back a session with none of the
    // worker's prompt — is exactly how this stayed hidden. Refusing is the fix.
    const resp = await client.raw('POST', '/agent/session', { worker: 'aa-desinger' })
    expect(resp.status()).toBe(404)
    expect(await resp.text()).toContain('aa-desinger')
  })

  test('the architect builds the roster it proposed, and the log names it as the actor', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_RIG)
    await client.applyTopology(seedBody())

    const sid = await client.createSession({ worker: DESIGNER })
    await client.waitForCreatesToSettle()
    await client.sendMessage(sid, 'Design me an org for the bakery.')

    // Second message: sequenced by the [AA-PROPOSED] marker its own first reply
    // left behind, NOT by turn index — see the header.
    const built = await client.sendMessage(sid, 'Go ahead.')
    expect(built).toContain(BUILT)

    // It used the ordinary management tools — no new machinery, which is the
    // seed's whole claim about the architect.
    const created = await waitForConfigAction(client, 'worker_create', 120_000)
    expect(created.actor_worker, 'a chat session acts as its worker').toBe(DESIGNER)
    expect(created.actor_session).not.toBe('')
    expect(created.payload).toMatchObject({ name: BAKER })
    expect(created.rationale).not.toBe('')

    const scheduled = await waitForConfigAction(client, 'schedule_create', 120_000)
    expect(scheduled.actor_worker).toBe(DESIGNER)

    // Leg 2 of the memory schema: the architect decided what the new role reads
    // and writes. That instruction lives in the role it wrote, which is why it
    // is the leg people miss.
    const baker = await client.getWorker(BAKER)
    expect(baker.system_prompt).toContain('kind=order')
    expect(baker.system_prompt).toContain('kind=decision')

    // It preferred a clock to a chain, as its prompt tells it to.
    expect(await client.listSchedules()).toHaveLength(1)
    expect(await client.listSubscriptions()).toHaveLength(1)
  })

  test('the frozen archivist refuses the architect, and the refusal is recorded', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_RIG)
    await client.applyTopology(seedBody())

    const sid = await client.createSession({ worker: DESIGNER })
    await client.waitForCreatesToSettle()
    await client.sendMessage(sid, 'Design me an org for the bakery.')
    await client.sendMessage(sid, 'Go ahead.')

    // "Only the architect may create workers" is a prompt statement, not a
    // permission — every worker holds every core tool. Frozen is the one real
    // boundary, and this is it being enforced against the most privileged
    // worker in the project.
    const refused = await client.sendMessage(sid, 'Now make the archivist keep more.')
    expect(refused).toContain(REFUSAL)

    const scribe = await client.getWorker(SCRIBE)
    expect(scribe.system_prompt, 'a frozen prompt must be unchanged').not.toContain('absolutely everything')

    // A refusal is a project EVENT, not a config-log entry — and the
    // distinction is the design, not a detail. The config log records what
    // CHANGED; a refused write changed nothing, so it has no entry there. The
    // attempt is still worth knowing about, so it goes on the event spine where
    // another worker can subscribe to it (frozen-scorer@v1 and the injection
    // gauntlet both do exactly that).
    const refusals = await client.waitForEvents((rows) => rows.length > 0, {
      type: 'worker.freeze_refused',
      timeoutMs: 60_000,
    })
    expect(refusals.length, 'the attempt on a frozen worker must be visible to the project').toBeGreaterThan(0)
    expect(refusals[0]!.envelope.worker, 'the event names who tried').toBe(DESIGNER)

    // And nothing was written: a refusal that quietly logged a config change
    // would be worse than no record at all.
    const actions = (await client.configEvents()).map((e) => e.action)
    expect(actions, 'a refused write must not appear as a change').not.toContain('worker_prompt_write')
  })

  test('a conversation that goes quiet wakes the archivist — and the archivist does not wake itself', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_RIG)
    test.skip(!process.env.STACK_IDLE_TIMEOUT, NEEDS_RIG)
    await client.applyTopology(seedBody())

    const sid = await client.createSession({ worker: DESIGNER })
    await client.waitForCreatesToSettle()
    await client.sendMessage(sid, 'Design me an org for the bakery.')

    // (a) The conversation ends by going quiet, and that is what emits.
    const finished = await waitForConversationToEnd(client, sid)
    expect(finished.envelope.worker).toBe(DESIGNER)
    expect(finished.text, 'the event carries the conversation itself, not a summary').toContain(PROPOSED)

    // (b) The archivist wakes and writes, per its policy.
    const deliveries = await client.waitForDeliveries(
      (rows) => rows.some((d) => d.worker === SCRIBE && d.status === 'ok'),
      { timeoutMs: 180_000 },
    )
    expect(deliveries.filter((d) => d.worker === SCRIBE)).toHaveLength(1)

    const memories = await poll(
      () => client.raw('GET', '/agent/memories?selector=kind%3Dsummary').then((r) => r.json()),
      (b: { memories?: unknown[] }) => (b.memories?.length ?? 0) > 0,
      120_000,
      'the archivist to write its summary',
    )
    expect(JSON.stringify(memories)).toContain(ARCHIVED)

    // (c) THE ONE THAT WOULD HAVE SPUN. The archivist's own completion is a
    // worker.finished matching its own UNFILTERED subscription. Before the
    // router suppressed self-delivery this re-woke it every time, all the way
    // to the depth-8 floor, and the seed's filter existed only to dodge it.
    //
    // Assert on the archivist's own event specifically: waiting for "no second
    // delivery" in the abstract would pass on a stack where nothing happened at
    // all, so first prove the archivist DID emit, then prove nobody woke on it.
    const own = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.worker === SCRIBE),
      { type: 'worker.finished', timeoutMs: 180_000 },
    )
    expect(own.some((e) => e.envelope.worker === SCRIBE)).toBeTruthy()

    const settled = await client.listDeliveries()
    expect(
      settled.filter((d) => d.worker === SCRIBE),
      'the archivist must be woken by the conversation and by nothing else — a second delivery here is the self-loop',
    ).toHaveLength(1)
  })

  test('a dispatched job also wakes the archivist — what dropping the filter bought', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_RIG)
    await client.applyTopology(seedBody())

    // A third worker, woken by an event rather than by a human. Its completion
    // is NOT interactive, so the seed's old `interactive=true` filter excluded
    // it: under that filter the project's history was conversations only, and
    // everything the workforce actually did went unrecorded.
    await client.putWorker(RUNNER, {
      description: 'counts the morning trays',
      system_prompt: 'You are aa-runner, and you count trays. Read kind=order.',
    })
    await client.createSubscription({ event_type: TASK_EVENT, worker: RUNNER })

    await client.postEvent({ type: TASK_EVENT, text: 'Count this morning.' })

    const deliveries = await client.waitForDeliveries(
      (rows) => rows.some((d) => d.worker === RUNNER && d.status === 'ok'),
      { timeoutMs: 180_000 },
    )
    expect(deliveries.some((d) => d.worker === RUNNER)).toBeTruthy()

    // The runner's completion reaches the archivist — no idle wait needed, a
    // dispatched job emits when its turn settles.
    const woken = await client.waitForDeliveries(
      (rows) => rows.some((d) => d.worker === SCRIBE && d.status === 'ok'),
      { timeoutMs: 180_000 },
    )
    expect(
      woken.some((d) => d.worker === SCRIBE),
      'an unfiltered archivist archives job completions too — this is what the filter used to cost',
    ).toBeTruthy()

    const ran = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.worker === RUNNER),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    expect(ran.find((e) => e.envelope.worker === RUNNER)!.text).toContain(RAN)
  })
})
