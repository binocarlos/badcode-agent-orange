import { test, expect } from '@playwright/test'
import { newProjectClient, ProjectClient, Schedule } from '../helpers/api'
import { configEvents } from '../helpers/configlog'

// Feature e2e: a schedule that can never start a job retires itself (§8.6 rule 5).
//
// This test exists because of an incident, and it is the only test in the suite
// whose subject is the suite's own safety. On 2026-07-26 fifty-three abandoned
// `* * * * *` schedules — left behind by earlier runs of these very specs —
// fired every minute, held every host port between them, and made the whole
// stack unable to provision anything. It presented an hour later, in somebody
// else's work, as "the product will not start sessions", and it was
// misdiagnosed twice as a product bug before anyone counted the schedules.
//
// The engine fix has two halves. The first (a capacity error that says it is a
// host limit) cannot be exercised here: reaching it needs 100 live sessions and
// the pool bounds are hardcoded in agentd, so this suite verifies it by reading
// the binary, not by filling the host — see e2e/README.md. The second half is
// this: a schedule whose firings repeatedly start nothing is switched off, so
// the storm is self-limiting whatever a test forgets to clean up.
//
// The trigger is a worker that EXISTS but is disabled. That distinction is the
// whole reason this test is cheap and safe:
//
//   - a MISSING worker takes §8.6 rule 4's path and disables the schedule on
//     the very first firing, which would prove nothing about the streak;
//   - a disabled worker passes the scheduler's GetWorker check, reaches the
//     dispatch gate, and is refused there — `dispatchFailed`, which is what the
//     streak counts;
//   - and nothing is ever provisioned, so the test that proves the anti-storm
//     mechanism cannot itself start a storm. It leases no port and launches no
//     container.
//
// It is by a wide margin the slowest test in the suite: the streak needs five
// firings, a firing is one wall-clock minute, and there is no catch-up, so five
// minutes is a floor set by the product and not by the polling.

/** How many consecutive failures retire a schedule (agentdb.ScheduleMaxProvisionFailures). */
const MAX_PROVISION_FAILURES = 5

/**
 * Waits for a schedule to satisfy a predicate, reading every `intervalMs`.
 *
 * Not `poll` from helpers/api: that reads every 250ms, which is right for a
 * backend catching up in under a second and wrong for a six-minute wait, where
 * it would put some fifteen hundred pointless queries through Postgres. The
 * progress log is here because a silent six-minute test is indistinguishable
 * from a hung one.
 */
async function waitForSchedule(
  client: ProjectClient,
  id: string,
  predicate: (s: Schedule) => boolean,
  what: string,
  timeoutMs: number,
  /** Every distinct state seen on the way, in order — asserted on afterwards. */
  seen: Schedule[] = [],
  intervalMs = 5_000,
): Promise<Schedule> {
  const deadline = Date.now() + timeoutMs
  let last: Schedule | undefined
  for (;;) {
    const found = (await client.listSchedules()).find((s) => s.id === id)
    if (found) {
      if (last?.provision_failures !== found.provision_failures || last?.enabled !== found.enabled) {
        seen.push(found)
        console.log(
          `[schedule-resilience] ${found.provision_failures}/${MAX_PROVISION_FAILURES} failures,` +
            ` enabled=${found.enabled}`,
        )
      }
      last = found
      if (predicate(found)) return found
    }
    if (Date.now() >= deadline) {
      throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}; last: ${JSON.stringify(last)}`)
    }
    await new Promise((r) => setTimeout(r, intervalMs))
  }
}

test.describe('a schedule that can never start a job retires itself (§8.6)', () => {
  test.describe.configure({ mode: 'serial' })

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-sched')
  })

  test.afterEach(async () => {
    // Belt and braces. The schedule under test is expected to disable itself,
    // but a test that fails early leaves an ENABLED `* * * * *` row behind, and
    // that row is the exact hazard this file is about.
    await client?.cleanup()
  })

  test('five firings that start nothing disable the schedule, with the reason in the config log', async () => {
    // Eight minutes: five firings at one per wall-clock minute, plus a margin
    // for whichever fraction of the first minute we arrive in and for a tick
    // that lands just after a boundary.
    test.setTimeout(8 * 60_000)

    await client.putWorker('stalled-worker', {
      description: 'exists, but is switched off — every firing will be refused at the gate',
      system_prompt: 'never runs',
      enabled: false,
    })

    const created = await client.createSchedule({
      worker: 'stalled-worker',
      cron: '* * * * *',
      input: 'this instruction can never be delivered',
      rationale: 'e2e: prove a schedule that cannot provision is retired',
    })
    expect(created.enabled).toBe(true)
    // A fresh schedule starts with a clean streak — the counter is not nullable
    // and not absent from the API, which is what lets an operator see a streak
    // building rather than only its result.
    expect(created.provision_failures).toBe(0)
    expect(created.last_provision_error).toBe('')

    const seen: Schedule[] = []
    const retired = await waitForSchedule(
      client,
      created.id,
      (s) => !s.enabled,
      `schedule ${created.id} to be disabled after ${MAX_PROVISION_FAILURES} failed firings`,
      7 * 60_000,
      seen,
    )

    // The streak is visible WHILE it builds, not only in its result. That is the
    // whole point of the column being on the API: an operator watching a
    // schedule can see it going wrong and fix the worker before anything is
    // switched off. A mechanism that only reported after the fact would leave
    // them with the same "why did this stop?" the incident began with.
    const building = seen.filter((s) => s.enabled && s.provision_failures > 0)
    expect(building.length).toBeGreaterThan(0)
    expect(building[0].last_provision_error).toBe('worker "stalled-worker" is disabled')

    // The disable spends the streak: a human who re-enables this schedule after
    // fixing the worker gets a full budget, not a row that retires again on its
    // next firing.
    expect(retired.provision_failures).toBe(0)
    expect(retired.last_provision_error).toBe('')

    // The decision is in the config log with its reason. The COUNTING is not,
    // deliberately — a config event every minute for a schedule failing every
    // minute would bury the log it exists to make readable — so the log should
    // hold exactly one record for this schedule, not five.
    const log = await configEvents(client)
    const disables = log.filter(
      (e) => e.action === 'schedule_update' && e.rationale.includes('disabled by the scheduler'),
    )
    expect(disables).toHaveLength(1)
    expect(disables[0].rationale).toContain(`${MAX_PROVISION_FAILURES} consecutive firings could not start a job`)
    // The reason travels with the decision, and it is the DISPATCH gate's own
    // words rather than a generic "could not start" — so the changelog answers
    // "why is my schedule off?" without a log-dive.
    expect(disables[0].rationale).toContain('last reason: worker "stalled-worker" is disabled')

    // Nothing was provisioned on the way here. This is the claim that makes the
    // mechanism worth having: five refused firings cost the host no ports and
    // no containers, which is precisely what the fifty-three abandoned
    // schedules did not manage.
    expect(await client.listAllSessions()).toHaveLength(0)
  })
})
