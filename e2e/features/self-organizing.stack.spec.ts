import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type ProjectClient, type TopologyBody } from '../helpers/api'
import { waitForConfigAction } from '../helpers/configlog'

// R2 — self-organizing@v1 (topology library entry 9, decided UNCAPPED by D3).
//
// This seed lives in its OWN spec file, apart from topologies.stack.spec.ts,
// because of a runner constraint: its e2e must run against a deliberately
// NARROWED session port pool (D3 accepted the runaway risk of an uncapped
// pool; in mock mode a runaway costs containers and ports, and a narrow pool
// is the cheap blast door), and `run-stack-e2e.sh test` refuses to combine
// `--port-pool` with `--mock-script` — each agentd reload carries only its own
// override. So this spec runs against the PLAIN canned mock model, which can
// never emit a tool_use, and the assertions are chosen for exactly that world:
//
//   (a) apply proves the wiring: one founder, one inbound subscription,
//       nothing else — and NO settings patch (the uncapped decision, visible
//       as an absence);
//   (b) one round proves the founder runs and its charter (the management
//       surface, honestly described) rode into the composed prompt;
//   (c) the org does NOT grow: the canned model calls no tools, so zero
//       worker_create / subscription_create beyond the apply's own rows —
//       uncapped autonomy without a model behind it is inert. Growth under a
//       scripted or real model is a different experiment (the C1 rig / L3
//       runbook); this offline e2e pins apply + wiring + inertness.
//
// Run it:
//   ./e2e/run-stack-e2e.sh test mock --port-pool 8 -- e2e/features/self-organizing.stack.spec.ts
//
// It SKIPS on an ordinary run (no STACK_PORT_POOL): the narrowed pool is the
// point, and running the same assertions on a 100-port stack would silently
// drop the one guardrail D3 asked this e2e to keep.

const POOL = Number(process.env.STACK_PORT_POOL) || 0

const FOUNDER = 'so-founder'
const SO_EVENT = 'so.mission'
const SO_MISSION = 'You run the paperwork pool for the orchard.'

test.describe('R2 — self-organizing@v1 (uncapped, narrowed pool)', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  test.skip(
    POOL === 0,
    'needs a narrowed pool: ./e2e/run-stack-e2e.sh test mock --port-pool 8 -- e2e/features/self-organizing.stack.spec.ts',
  )

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-so')
  })

  test.afterEach(async () => {
    // Non-negotiable on a pool this narrow: leaked sessions here wedge every
    // later run on the stack, which is the exact incident D3's blast door
    // exists to contain.
    await client?.cleanup()
  })

  test('the founder applies, runs, and — with no model behind it — the org does not grow', async () => {
    const body: TopologyBody = {
      name: 'self-organizing',
      version: 'v1',
      answers: { 'founder-name': FOUNDER, 'inbound-event-type': SO_EVENT, mission: SO_MISSION },
    }

    // (a) Preview: the smallest bundle in the library — the org chart is not
    // the seed's to draw — and the D3 absence: no settings patch, so the
    // apply changes no caps and the project's own brakes are the only brakes.
    const preview = await client.previewTopology(body)
    expect(preview.applicable).toBe(true)
    expect(preview.diff.new_workers).toEqual([FOUNDER])
    expect(preview.diff.new_subscriptions).toEqual([{ event_type: SO_EVENT, worker: FOUNDER }])
    expect(preview.diff.new_schedules).toEqual([])
    expect(preview.bundle.settings_patch ?? null, 'D3: uncapped — the bundle must patch no settings').toBeNull()
    expect(preview.missing_images).toEqual([])
    expect(preview.missing_skills).toEqual([])
    // The charter describes the REAL management surface, and never the one
    // verb that does not exist.
    const founderRow = preview.bundle.workers[0]!
    for (const tool of ['worker_create', 'worker_update', 'subscription_create', 'schedule_create']) {
      expect(founderRow.system_prompt, `the charter must name ${tool}`).toContain(tool)
    }
    expect(founderRow.system_prompt).toContain('There is no worker_delete tool')
    expect(founderRow.system_prompt).toContain('uncapped by design')

    // (b) Apply, and read back through the ordinary routes.
    const result = await client.applyTopology(body)
    expect(result.event.action).toBe('topology_apply')
    const workers = await client.listWorkers()
    expect(workers.map((w) => w.name)).toEqual([FOUNDER])
    expect(workers[0]!.system_prompt).toBe(founderRow.system_prompt)
    const subs = await client.listSubscriptions()
    expect(subs).toHaveLength(1)
    expect(subs[0]).toMatchObject({ event_type: SO_EVENT, worker: FOUNDER })
    expect(await client.listSchedules()).toEqual([])
    const bracket = await waitForConfigAction(client, 'topology_apply', 30_000)
    expect(bracket.payload.topology).toBe('self-organizing@v1')

    // The apply's own config-log rows are the baseline the growth assertion
    // measures against: exactly one worker_create (the founder), one
    // subscription_create (its wake-up).
    expect(await client.configEvents({ action: 'worker_create' })).toHaveLength(1)
    expect(await client.configEvents({ action: 'subscription_create' })).toHaveLength(1)

    // (c) One round. The canned mock model (no script under --port-pool)
    // answers with plain text and can never emit a tool_use.
    const event = await client.postEvent({ type: SO_EVENT, text: 'Build whatever team the paperwork needs.' })
    const deliveries = await client.waitForDeliveries(
      (rows) => rows.length >= 1 && rows.every((d) => d.status === 'ok' && d.session_id !== ''),
      { event_id: event.id, timeoutMs: 180_000 },
    )
    const sessionId = deliveries[0]!.session_id

    // The founder really ran, and its charter rode into the job: the composed
    // prompt carries the management-surface description the seed exists to
    // deliver.
    const messages = await poll(
      () => client.listMessages(sessionId),
      (rows) => rows.some((m) => m.role === 'assistant' && m.content.trim() !== ''),
      60_000,
      `an assistant reply in session ${sessionId}`,
    )
    const reply = messages
      .filter((m) => m.role === 'assistant')
      .map((m) => m.content)
      .join('\n')
    expect(reply.trim()).not.toBe('')
    const session = await client.getSession(sessionId)
    expect(session.composed_prompt ?? '').toContain('worker_create')
    expect(session.composed_prompt ?? '').toContain('There is no worker_delete tool')

    // Let the finish settle (it routes nowhere) before counting anything.
    await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === sessionId),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )

    // The inertness assertion: autonomy without a model is inert. Nothing
    // grew — the config log still holds exactly the apply's rows, the org
    // chart is still one worker and one edge, and no prompt was rewritten.
    expect(await client.configEvents({ action: 'worker_create' })).toHaveLength(1)
    expect(await client.configEvents({ action: 'subscription_create' })).toHaveLength(1)
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toHaveLength(0)
    expect(await client.configEvents({ action: 'schedule_create' })).toHaveLength(0)
    expect((await client.listWorkers()).map((w) => w.name)).toEqual([FOUNDER])
    expect(await client.listSubscriptions()).toHaveLength(1)
  })
})
