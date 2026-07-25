import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type EventDelivery, type ProjectClient } from '../helpers/api'
import { sessionMCP, type MCPClient } from '../helpers/mcp'
import { psql, lit } from '../helpers/stackdb'

const exec = promisify(execFile)

// I4 — the worker image pointer, end to end (§13.3, §13.5, §13.6).
//
// Everything §13 needs already existed separately: the engine could snapshot a
// container (layer 0), `image_create` could name and version the result (I2),
// the catalogue could resolve `name` → latest and `name:version` → pinned (I1),
// and the worker editor could store a pointer (C3). What was missing was the
// wire between them — so a worker with `image` set FAILED every job with "no
// ImageResolver was supplied". This file is the proof that the wire is there.
//
// # What it asserts, and how
//
// The only honest proof that a job launched from a particular image is what is
// inside the container the job ran in. So each burned version carries a marker
// file written by a skill's install script, and every assertion below reads
// that file out of the job's own container through DinD:
//
//	vanilla session ──skill_install──▶ /workspace/.toolbox-marker = "v1"
//	                ──image_create───▶ toolbox:1
//	worker.image = "toolbox" ──event──▶ job container has marker "v1"
//
// A test that only checked the catalogue would pass on a build that resolved
// the pointer perfectly and then launched the base image anyway — which is
// precisely the silent substitution §13.3 exists to forbid.
//
// # Driving the tools without the model
//
// `skill_install` and `image_create` are called with the curating session's own
// credential (helpers/mcp.ts). The mock model serves a canned script and cannot
// be made to invoke a tool, so this is the only way to run the §13.8 workflow
// offline: same tool, same auth, same provenance — only the decision to call is
// the test's rather than the model's.

const COMPOSE_PROJECT = process.env.STACK_COMPOSE_PROJECT || 'agent-orange-stack-e2e'

/** The file each burned version carries, so a container can say what it is. */
const MARKER = '/workspace/.toolbox-marker'

/** The curated image name every worker in this file points at. */
const IMAGE = 'toolbox'

/** The event type that wakes a worker here. */
const TRIGGER = 'toolbox.check'

// ── Reading a session's container ───────────────────────────────────────────

/**
 * Runs a command inside a session's container, through DinD.
 *
 * The container name is `sandbox-<session id>` (go/execenv/docker/client.go),
 * and agentd shares DinD's network namespace, so `docker compose exec dind
 * docker exec …` is the only route a test has into a running session's
 * filesystem. Returns stdout trimmed, or '' when the command fails — a missing
 * marker file is an expected answer here, not an error.
 */
async function inContainer(sessionId: string, cmd: string[]): Promise<string> {
  const { stdout } = await exec('docker', [
    'compose',
    '-p',
    COMPOSE_PROJECT,
    'exec',
    '-T',
    'dind',
    'docker',
    'exec',
    `sandbox-${sessionId}`,
    ...cmd,
  ]).catch(() => ({ stdout: '' }))
  return stdout.trim()
}

/** The marker a session's image carries, or '' on a vanilla image. */
function marker(sessionId: string): Promise<string> {
  return inContainer(sessionId, ['cat', MARKER])
}

/**
 * `last_resumed_at` for one catalogue version (§5's snapshot metadata tuple).
 *
 * Read straight from Postgres because no route exposes it — it is operator
 * telemetry, not product surface. B4 shipped the column and the store method
 * and nothing called it; a non-zero value here is the proof that binding the
 * pointer also stamps it.
 */
async function lastResumedAt(project: string, name: string, version: number): Promise<number> {
  const out = await psql(
    `SELECT last_resumed_at FROM agent_custom_images
      WHERE customer = ${lit(project)} AND name = ${lit(name)} AND version = ${version};`,
  )
  return Number(out.trim() || '0')
}

// ── Curation ────────────────────────────────────────────────────────────────

/** A session on the vanilla image, with a live container and its MCP client. */
async function vanillaSession(client: ProjectClient): Promise<{ id: string; mcp: MCPClient }> {
  const id = await client.createSession({ job: 'curate' })
  // A session has no container until its first turn, and every tool below acts
  // on that container.
  await client.sendMessage(id, 'hello')
  return { id, mcp: sessionMCP(client.project, id) }
}

/**
 * The §13.8/§14.4 workflow, once: open a vanilla session, install a skill whose
 * script marks the filesystem, verify the mark, then burn the result.
 *
 * Returns the burned version. Each call marks with a different `tag`, which is
 * what makes "which version did this job launch from?" answerable by `cat`.
 */
async function curateAndBurn(client: ProjectClient, tag: string): Promise<number> {
  const { id, mcp } = await vanillaSession(client)

  // The vanilla image carries no marker — the baseline that makes every later
  // assertion mean something.
  expect(await marker(id), 'the vanilla image must not already carry a marker').toBe('')

  await mcp.callOK('skill_create', {
    name: 'toolbox-probe',
    markdown: `# Toolbox probe\n\nWrites ${MARKER} so a container can name its own image.`,
    install_sh: `printf '%s' '${tag}' > ${MARKER}`,
    labels: { kind: 'probe' },
  })
  const installed = await mcp.callOK('skill_install', { name: 'toolbox-probe' })
  // §14.2: the script's exit status is reported, never assumed — a burn on top
  // of a failed install would be an image that lies about what is in it.
  expect(installed.script).toMatchObject({ ran: true, exit_code: 0 })
  expect(await marker(id)).toBe(tag)

  const burned = await mcp.callOK('image_create', {
    name: IMAGE,
    labels: { purpose: 'e2e-toolbox', marker: tag },
  })
  expect(burned.name).toBe(IMAGE)
  return burned.version as number
}

// ── Waking a worker ─────────────────────────────────────────────────────────

/**
 * Fires `TRIGGER` and waits for the resulting delivery to have started (or
 * failed). Returns the delivery row.
 *
 * A delivery row exists before its session does, so the wait is for a terminal
 * enough state to assert on: a session id, or a failure.
 */
async function wakeWorker(client: ProjectClient): Promise<EventDelivery> {
  const event = await client.postEvent({ type: TRIGGER, text: 'check the toolbox' })
  const rows = await poll(
    () => client.listDeliveries({ event_id: event.id }),
    (ds) => ds.length > 0 && ds.every((d) => d.session_id !== '' || d.status === 'failed'),
    120_000,
    `a delivery for ${event.id}`,
  )
  expect(rows).toHaveLength(1)
  return rows[0]
}

test.describe('I4 §13 — a worker launches from the image it was pointed at', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(600_000)

  let client: ProjectClient
  /** Sessions the ROUTER created (client.cleanup only knows its own). */
  const jobSessions: string[] = []

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-i4')
  })

  // Sessions hold a running container inside DinD until they are deleted and
  // nothing reaps them on a timer; an accumulating stack stops provisioning
  // anything at all. Job sessions are created by the router, so they have to be
  // collected by hand — cleanup() only tracks what this client created.
  test.afterEach(async () => {
    for (const id of jobSessions.splice(0)) {
      await client.raw('DELETE', `/agent/session/${encodeURIComponent(id)}`).catch(() => {})
    }
    await client.cleanup()
  })

  /** Creates a worker woken by TRIGGER, and adopts an image through §9's tool. */
  async function workerOn(name: string, mcp: MCPClient, image: string): Promise<void> {
    await client.putWorker(name, {
      description: 'checks the toolbox',
      system_prompt: 'You verify that the curated toolbox is present.',
    })
    // §13.5: adoption is a VISIBLE act, and it is this call. Burning a version
    // never repoints a worker by itself; a human or a worker decides.
    await mcp.callOK('worker_update', {
      name,
      fields: { image },
      rationale: `run ${name} on the curated ${image}`,
    })
    expect((await client.getWorker(name)).image).toBe(image)
    await client.createSubscription({ event_type: TRIGGER, worker: name })
  }

  test('curate, burn, adopt — and the next job runs on the burned image', async () => {
    // §13.8 workflow 2: vanilla → skill_install → verify → burn.
    const version = await curateAndBurn(client, 'v1')
    expect(version, 'the first burn of a name is version 1').toBe(1)

    const { mcp } = await vanillaSession(client)
    await workerOn('checker', mcp, IMAGE)

    // The NEXT job — nothing about the running curation session changes.
    const delivery = await wakeWorker(client)
    jobSessions.push(delivery.session_id)
    expect(delivery.status, 'a worker with an image pointer must no longer fail its job').not.toBe('failed')

    const session = await client.getSession(delivery.session_id)
    expect(session.worker).toBe('checker')
    // The proof: the job ran inside the curated environment. Before I4 this
    // job did not run at all; a regression that resolved the pointer and then
    // launched the base image would leave this empty.
    expect(await marker(delivery.session_id)).toBe('v1')

    // …and the launch stamped §5's `last_resumed_at`, which nothing called
    // before I4 (B4's finding), so an operator can see a soon-to-expire image
    // is still in daily use.
    expect(await lastResumedAt(client.project, IMAGE, 1)).toBeGreaterThan(0)
  })

  test('a floating pointer follows a new burn; a pinned one does not', async () => {
    expect(await curateAndBurn(client, 'v1')).toBe(1)
    // Publishing an improvement is a new version under the same name (§13.2).
    expect(await curateAndBurn(client, 'v2')).toBe(2)

    const { mcp } = await vanillaSession(client)
    // A bare name is a floating pointer: "give me the current toolbox".
    await workerOn('floating', mcp, IMAGE)
    // `name:version` pins: stability over freshness (§13.3).
    await workerOn('pinned', mcp, `${IMAGE}:1`)

    // One event, two subscriptions, two jobs — same trigger, different images.
    const event = await client.postEvent({ type: TRIGGER, text: 'check the toolbox' })
    const rows = await poll(
      () => client.listDeliveries({ event_id: event.id }),
      (ds) => ds.length >= 2 && ds.every((d) => d.session_id !== '' || d.status === 'failed'),
      180_000,
      `two deliveries for ${event.id}`,
    )
    expect(rows).toHaveLength(2)
    for (const d of rows) jobSessions.push(d.session_id)

    const byWorker: Record<string, string> = {}
    for (const d of rows) {
      expect(d.status, `delivery for ${d.id} failed`).not.toBe('failed')
      byWorker[(await client.getSession(d.session_id)).worker!] = await marker(d.session_id)
    }
    // The floating worker moved when curation published; the pinned one did
    // not. Both are one text field's worth of expressiveness (§13.3).
    expect(byWorker.floating).toBe('v2')
    expect(byWorker.pinned).toBe('v1')

    // Both versions are live and both were launched from, so both are stamped.
    expect(await lastResumedAt(client.project, IMAGE, 1)).toBeGreaterThan(0)
    expect(await lastResumedAt(client.project, IMAGE, 2)).toBeGreaterThan(0)
  })

  test('an unburned pointer fails the job loudly instead of launching something else', async () => {
    const { mcp } = await vanillaSession(client)
    // Syntactically valid, never burned: the pointer is accepted at write time
    // (a worker may be pointed at an image curation is about to publish) and
    // refused at launch.
    await workerOn('lost', mcp, 'never-burned')

    const delivery = await wakeWorker(client)
    // §13.3, the whole reason this item was worth doing: resolution failure
    // fails the job rather than silently falling back to the project default.
    expect(delivery.status).toBe('failed')
    // Nothing was launched at all — a session id here would mean SOMETHING ran,
    // and whatever it ran on would not be what the worker was pointed at.
    expect(delivery.session_id).toBe('')
  })

  test('a worker with no pointer still launches on the project default', async () => {
    // The other half of §13.5's chain: `worker.image > project base_image >
    // global`. Binding the pointer must not disturb the workers that have none.
    await client.putWorker('plain', {
      description: 'no image pointer',
      system_prompt: 'You run wherever the project runs.',
    })
    await client.createSubscription({ event_type: TRIGGER, worker: 'plain' })

    const delivery = await wakeWorker(client)
    jobSessions.push(delivery.session_id)
    expect(delivery.status).not.toBe('failed')
    // On the vanilla image, so no marker — and no §13 resolution happened.
    expect(await marker(delivery.session_id)).toBe('')
  })
})
