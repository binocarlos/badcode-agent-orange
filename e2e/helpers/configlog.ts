import { poll, type ConfigEvent, type ProjectClient } from './api'

// Reading the config log (§15) from an e2e test.
//
// This used to go through psql, because J1 built the table and the store seam
// but no read route. `GET /agent/config-events` (F1) is now live, so these read
// the log the way the changelog UI does — over the API.
//
// They take a ProjectClient rather than a project name precisely so scoping is
// the token's business: a test cannot ask for another project's history even by
// accident, because it has no credential that would answer.

export type { ConfigEvent } from './api'

/** A project's config-log records, newest first (§15.9). */
export async function configEvents(client: ProjectClient): Promise<ConfigEvent[]> {
  return client.configEvents()
}

/** Just the action verbs, newest first — what most assertions actually compare. */
export async function configActions(client: ProjectClient): Promise<string[]> {
  return (await configEvents(client)).map((e) => e.action)
}

/**
 * Waits until the log holds at least `count` records and returns them.
 *
 * For a mutation the test made itself this is satisfied on the first read — the
 * dual write commits inside the mutation's transaction (§15.4). It is a poll
 * because a record written by a *job* lands whenever that job gets to it.
 */
export function waitForConfigEvents(
  client: ProjectClient,
  count: number,
  timeoutMs = 10_000,
): Promise<ConfigEvent[]> {
  return poll(
    () => configEvents(client),
    (rows) => rows.length >= count,
    timeoutMs,
    `${count} config event(s) in ${client.project}`,
  )
}

/** Waits for a record with the given action and returns the newest such record. */
export async function waitForConfigAction(
  client: ProjectClient,
  action: string,
  timeoutMs = 120_000,
): Promise<ConfigEvent> {
  const rows = await poll(
    () => client.configEvents({ action }),
    (found) => found.length > 0,
    timeoutMs,
    `a ${action} record in ${client.project}`,
  )
  return rows[0]
}
