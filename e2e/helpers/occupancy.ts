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
//     from 30001–30100 (`go/cmd/agentd/main.go`), so the 101st live session
//     fails with `dind provision: port pool exhausted` — surfaced to a caller
//     as "session has no running instance and no snapshot", which describes a
//     lost session and invites re-creation when the truth is a saturated host.
//   * **Schedules outlive their test.** A `* * * * *` row keeps firing for as
//     long as it exists, whatever happened to the test that made it. Fifty-three
//     of them, left by earlier runs, consumed the whole pool every minute and
//     poisoned an unrelated verification run for an hour.
//
// `afterEach` cleanup cannot help with either, because the runs that leak are
// exactly the runs that died before `afterEach`. Hence a check outside the
// tests: measure before, measure after, and make the difference a failure.

const COMPOSE_PROJECT = process.env.STACK_COMPOSE_PROJECT || 'agent-orange-stack-e2e'

/** The size of agentd's sandbox port pool (PortRangeStart..PortRangeEnd). */
export const PORT_POOL_SIZE = 100

/**
 * Projects this suite creates. Every fixture mints ids with these prefixes, so
 * anything matching is ours and anything else is a human's and must be left
 * alone — the stack is shared.
 */
const E2E_PROJECT_PATTERNS = ["e2e-%", "%probe%", "mcpprobe%", "mcpseed%", "diag%", "g1probe%"]

const projectFilter = E2E_PROJECT_PATTERNS.map((p) => `project LIKE '${p}'`).join(' OR ')

export interface Occupancy {
  /** Live session containers — one port each, out of PORT_POOL_SIZE. */
  containers: number
  /** Enabled schedules belonging to e2e projects: each one fires forever. */
  e2eSchedules: number
  /** Enabled schedules in total, including any a human created. */
  allSchedules: number
}

/** Counts the running sandbox containers, i.e. ports taken out of the pool. */
export async function runningContainers(): Promise<number> {
  try {
    const { stdout } = await exec('docker', [
      'compose',
      '-p',
      COMPOSE_PROJECT,
      'exec',
      '-T',
      'dind',
      'sh',
      '-c',
      'docker ps -q --filter name=sandbox- | wc -l',
    ])
    return Number(stdout.trim()) || 0
  } catch {
    return 0
  }
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
