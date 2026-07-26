import { writeFileSync } from 'node:fs'
import path from 'node:path'
import { describe, measure, PORT_POOL_SIZE, type Occupancy } from './helpers/occupancy'

// Pre-run guard for the stack suite.
//
// The failure this prevents: a run starts on a host that is already full, every
// session fails to provision, and the output blames whatever spec happened to
// run last. That has cost this project several debugging rounds — the error a
// saturated host produces ("session has no running instance and no snapshot")
// reads exactly like a product bug.
//
// So: say what the host is carrying before a single test runs, and refuse to
// start when there is not enough room to be worth trying.

/**
 * Below this many free ports, a full suite cannot finish; refuse to start.
 *
 * Deliberately a guess, not a measurement: the cost of being wrong is a false
 * refusal — loud, and a one-line fix — whereas a number derived from one
 * observed peak would invite people to trust it as derived.
 *
 * Data, not the source of the number: sampling every 10s through a full
 * 11.5-minute run on 2026-07-26 saw a peak of **7** concurrent sessions. So 25
 * is roughly three times the observed high-water mark, which is the kind of
 * margin a guess should have.
 */
const MINIMUM_FREE_PORTS = 25

export const BASELINE_FILE = path.join(import.meta.dirname, '.stack-baseline.json')

export default async function globalSetup(): Promise<void> {
  const before = await measure()
  // Recorded so teardown can report what THIS run leaked, rather than blaming a
  // run for a mess it inherited.
  writeFileSync(BASELINE_FILE, JSON.stringify(before), 'utf8')

  const free = PORT_POOL_SIZE - before.containers
  console.log(`[stack] ${describe(before)} — ${free} port(s) free`)

  if (before.e2eSchedules > 0) {
    // Not fatal on its own: a schedule fires once a minute and may be someone
    // else's run in progress. But it is the single best predictor of a pool
    // that is about to fill, so it must not be quiet.
    console.warn(
      `[stack] WARNING: ${before.e2eSchedules} enabled schedule(s) from e2e projects are still ` +
        `firing. Each one provisions a session every minute, for ever. If this run starts ` +
        `failing to provision, that is why — clear them with:\n` +
        `        ./e2e/run-stack-e2e.sh clean`,
    )
  }

  if (free < MINIMUM_FREE_PORTS) {
    throw new Error(
      `the stack has only ${free} of ${PORT_POOL_SIZE} session ports free, which is not enough to ` +
        `run this suite.\n\n` +
        `Every session takes one port from agentd's pool (30001–30100) and holds it until the ` +
        `session is deleted; nothing reaps them. A run started now would fail with ` +
        `"session has no running instance and no snapshot", which looks like a product bug and ` +
        `is not one.\n\n` +
        `    ./e2e/run-stack-e2e.sh clean\n\n` +
        `Currently: ${describe(before)}`,
    )
  }
}

export type { Occupancy }
