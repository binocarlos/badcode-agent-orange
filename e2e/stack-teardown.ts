import { readFileSync, rmSync } from 'node:fs'
import { deleteE2ESchedules, describe, measureSettled, type Occupancy } from './helpers/occupancy'
import { BASELINE_FILE } from './stack-setup'

// Post-run leak check.
//
// **A run that leaks does not get to report success.** Both times this suite
// leaked, the damage showed up somewhere else entirely — once as "images are
// broken", once as "the product will not provision", an hour later, in someone
// else's work. The leak is the failure, even when every assertion passed.
//
// Schedules are deleted here as well as reported, because leaving them would
// hand the next person the same afternoon. Sessions are only reported: deleting
// them could destroy a container a concurrent run is using, and this is a
// shared stack.

function baseline(): Occupancy | null {
  try {
    return JSON.parse(readFileSync(BASELINE_FILE, 'utf8')) as Occupancy
  } catch {
    return null
  }
}

export default async function globalTeardown(): Promise<void> {
  const before = baseline()
  rmSync(BASELINE_FILE, { force: true })

  // Settled, not instantaneous: a container can still be on its way up when the
  // last test ends, and counting before it lands is how this check certified a
  // leaking run as clean (see measureSettled).
  const after = await measureSettled()
  console.log(`[stack] after the run: ${describe(after)}`)

  const problems: string[] = []

  // Schedules: any left by an e2e project is this suite's, whoever created it.
  if (after.e2eSchedules > 0) {
    const removed = await deleteE2ESchedules()
    problems.push(
      `${after.e2eSchedules} enabled schedule(s) from e2e projects survived the run.\n` +
        `  A schedule fires for as long as its row exists — one abandoned '* * * * *' row ` +
        `provisions a session every minute for ever and will exhaust the port pool on its own.\n` +
        `  This happens when a test dies before its afterEach, so the fix is a finally, not a ` +
        `better afterEach.\n` +
        `  I have deleted ${removed} of them so the next run is not poisoned — but the leak is ` +
        `still a failure, which is why you are reading this.`,
    )
  }

  // Sessions: report the delta, not the total. Inheriting a full host is the
  // previous run's fault, and blaming this one for it would train people to
  // ignore the message.
  const leakedPorts = before ? after.containers - before.containers : 0
  if (leakedPorts > 0) {
    problems.push(
      `${leakedPorts} session container(s) more than when the run started — each holds one of ` +
        `agentd's session ports until deleted.\n` +
        `  Specs that create sessions must release them: client.cleanup() sweeps every session in ` +
        `the project (the router creates its own, which your test never sees), and browser specs ` +
        `use cleanupOpenedProjects().\n` +
        `  If the count is right but the containers are ORPHANS — running with no session row — ` +
        `suspect the create/delete race instead: a session deleted while its background create is ` +
        `still provisioning loses its row and gets its container anyway, and nothing reaps that. ` +
        `Check with: docker compose -p agent-orange-stack-e2e exec -T dind docker ps --filter name=sandbox-\n` +
        `  Not deleted automatically: on a shared stack a container may belong to someone else's run.`,
    )
  }

  if (problems.length > 0) {
    throw new Error(
      `the e2e run leaked stack state:\n\n` +
        problems.map((p) => `- ${p}`).join('\n\n') +
        `\n\nTo clear everything: ./e2e/run-stack-e2e.sh clean`,
    )
  }
}
