import { E2E_PROJECT_PREFIX } from './api'
import { psql } from './stackdb'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const exec = promisify(execFile)

// What the stack is currently carrying, and what this suite left behind.
//
// Two resources leak, and both were rediscovered the hard way as failures
// somewhere else entirely:
//
//   * **Sessions hold a port.** agentd allocates each session container a port
//     from a pool 100 wide by default, so the 101st live session cannot start.
//     It now says so — "the host port pool is exhausted … a host capacity
//     limit, not a lost or broken session" — but for most of this suite's life
//     it surfaced as "session has no running instance and no snapshot", which
//     describes a lost session and invites re-creation when the truth is a
//     saturated host. A clear error makes the leak diagnosable, not harmless.
//   * **Schedules outlive their test.** A `* * * * *` row keeps firing for as
//     long as it exists, whatever happened to the test that made it. Fifty-three
//     of them, left by earlier runs, consumed the whole pool every minute and
//     poisoned an unrelated verification run for an hour. §8.6 now retires a
//     schedule after five firings that start NO job — which does not cover the
//     leak that matters here, because a schedule filling the pool is one that
//     provisions successfully every time.
//
// `afterEach` cleanup cannot help with either, because the runs that leak are
// exactly the runs that died before `afterEach`. Hence a check outside the
// tests: measure before, measure after, and make the difference a failure.

const COMPOSE_PROJECT = process.env.STACK_COMPOSE_PROJECT || 'agent-orange-stack-e2e'

/**
 * The size of agentd's sandbox port pool (AGENTKIT_PORT_RANGE_START..END).
 *
 * 100 is agentd's default and what every ordinary run sees. A run started with
 * `--port-pool N` deliberately narrows it so the exhaustion path is reachable,
 * and sets this — without it the guard and the leak check would both do their
 * arithmetic against a pool the stack does not have.
 */
export const PORT_POOL_SIZE = Number(process.env.E2E_PORT_POOL_SIZE) || 100

/**
 * Projects this suite creates, derived from the constant the fixtures mint with
 * rather than restated here. That is deliberate: a list maintained in two
 * places drifts, and a fixture inventing its own prefix would escape the check
 * without anyone noticing. `uniqueProject()` guarantees the prefix, so the
 * only way to escape is to bypass the fixture entirely.
 *
 * Anything else on the stack is treated as a human's and left alone.
 */
const projectFilter = `project LIKE '${E2E_PROJECT_PREFIX}%'`

export interface Occupancy {
  /** Live session containers — one port each, out of PORT_POOL_SIZE. */
  containers: number
  /** Enabled schedules belonging to e2e projects: each one fires forever. */
  e2eSchedules: number
  /** Enabled schedules in total, including any a human created. */
  allSchedules: number
}

/**
 * Counts the running sandbox containers, i.e. ports taken out of the pool.
 *
 * Throws if it cannot count. It used to answer 0 on any failure, which is the
 * worst possible answer: a docker hiccup, a stopped dind, a renamed compose
 * project all reported "the stack is empty", so the pre-run guard waved the run
 * through and the post-run check certified a leaking run as clean. A number
 * this one is allowed to invent is not a measurement.
 */
export async function runningContainers(): Promise<number> {
  let stdout: string
  try {
    ;({ stdout } = await exec('docker', [
      'compose',
      '-p',
      COMPOSE_PROJECT,
      'exec',
      '-T',
      'dind',
      'sh',
      '-c',
      'docker ps -q --filter name=sandbox- | wc -l',
    ]))
  } catch (e) {
    throw new Error(
      `could not count session containers in the '${COMPOSE_PROJECT}' stack, so this run cannot ` +
        `tell whether the host is full or whether it leaked: ${e instanceof Error ? e.message : String(e)}`,
    )
  }
  const n = Number(stdout.trim())
  if (!Number.isFinite(n)) {
    throw new Error(`could not read a container count from docker; it said: ${JSON.stringify(stdout)}`)
  }
  return n
}

/** A snapshot of what the stack is carrying right now. */
export async function measure(): Promise<Occupancy> {
  const [containers, e2e, all] = await Promise.all([
    runningContainers(),
    psql(`SELECT count(*) FROM schedules WHERE enabled AND (${projectFilter});`).catch(() => '0'),
    psql('SELECT count(*) FROM schedules WHERE enabled;').catch(() => '0'),
  ])
  return {
    containers,
    e2eSchedules: Number(String(e2e).trim()) || 0,
    allSchedules: Number(String(all).trim()) || 0,
  }
}

/**
 * Is there a running container for this session id?
 *
 * Answers the question a count cannot: whether a specific session's container
 * exists, which is what distinguishes an ORPHAN (container, no session row)
 * from an ordinary live session.
 */
export async function sandboxContainerExists(sessionId: string): Promise<boolean> {
  const { stdout } = await exec('docker', [
    'compose',
    '-p',
    COMPOSE_PROJECT,
    'exec',
    '-T',
    'dind',
    'sh',
    '-c',
    `docker ps -q --filter name=sandbox-${sessionId}`,
  ])
  return stdout.trim() !== ''
}

/**
 * Force-removes a session's container.
 *
 * Reaching past the API like this is normally a smell, and here it is the
 * point: an ORPHAN has no session row, so there is no API call that could
 * remove it. A test that demonstrates the orphan must still not leave one, or
 * every run afterwards fails the leak check for a mess the test made.
 */
export async function removeSandboxContainer(sessionId: string): Promise<void> {
  await exec('docker', [
    'compose',
    '-p',
    COMPOSE_PROJECT,
    'exec',
    '-T',
    'dind',
    'sh',
    '-c',
    `docker rm -f sandbox-${sessionId} 2>/dev/null || true`,
  ]).catch(() => {})
}

/**
 * Measures, then keeps measuring until the count stops moving.
 *
 * A leak can arrive AFTER the run appears to be over, and measuring once misses
 * it. `POST /agent/session` provisions in the background, so a session deleted
 * while its create is still in flight is removed from the database and *then*
 * gets its container — an orphan holding a port that nothing will ever reap
 * (reproduced on 2026-07-26: create, delete 0ms later, container up 14s after
 * that, no session row, still running minutes later). Teardown measured zero
 * and certified the run clean; the ports were gone all the same.
 *
 * So: wait for two consecutive identical readings before believing either.
 * Cheap when nothing is in flight — one extra reading — and the only way a
 * late-arriving container is counted against the run that caused it.
 */
export async function measureSettled(
  quietMs = 6_000,
  timeoutMs = 60_000,
): Promise<Occupancy> {
  const deadline = Date.now() + timeoutMs
  let last = await measure()
  for (;;) {
    await new Promise((r) => setTimeout(r, quietMs))
    const next = await measure()
    if (next.containers === last.containers) return next
    last = next
    if (Date.now() >= deadline) return next
  }
}

/** Deletes the e2e schedules — used to stop a leak poisoning the next run. */
export async function deleteE2ESchedules(): Promise<number> {
  const before = (await measure()).e2eSchedules
  if (before === 0) return 0
  await psql(`DELETE FROM schedules WHERE ${projectFilter};`).catch(() => '')
  return before
}

/** One line an operator can act on. */
export function describe(o: Occupancy): string {
  return (
    `${o.containers}/${PORT_POOL_SIZE} session ports in use, ` +
    `${o.allSchedules} enabled schedule(s) (${o.e2eSchedules} from e2e projects)`
  )
}
