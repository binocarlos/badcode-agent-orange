import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { poll } from './api'

const exec = promisify(execFile)

// Reading the config log (§15) from an e2e test.
//
// The log has no HTTP route yet — J1 built the table and the store seam,
// `GET /agent/config-events` is still unbuilt (see e2e/README.md). So the one
// way to assert "that mutation was recorded" end-to-end is to read the stack's
// Postgres directly, through the compose project the runner script starts.
//
// This is deliberately the ONLY place in the suite that reaches past the HTTP
// API. When the read route lands, replace the body of `configEvents` with a
// client call and every feature spec keeps working unchanged.

const COMPOSE_PROJECT = process.env.STACK_COMPOSE_PROJECT || 'agent-orange-stack-e2e'
const PG_USER = process.env.POSTGRES_USER || 'agentorange'
const PG_DB = process.env.POSTGRES_DB || 'agentorange'

/** One row of `config_events` (go/agentdb/config_events.go). */
export interface ConfigEvent {
  id: string
  project: string
  actor_worker: string
  actor_session: string
  action: string
  payload: Record<string, unknown>
  rationale: string
  created_at: number
}

/** Runs one SQL statement in the stack's postgres and returns raw stdout. */
async function psql(sql: string): Promise<string> {
  const { stdout } = await exec('docker', [
    'compose',
    '-p',
    COMPOSE_PROJECT,
    'exec',
    '-T',
    'postgres',
    'psql',
    '-U',
    PG_USER,
    '-d',
    PG_DB,
    '-At',
    '-c',
    sql,
  ])
  return stdout
}

/** Escapes a string for a single-quoted SQL literal. */
function lit(v: string): string {
  return `'${v.replace(/'/g, "''")}'`
}

/**
 * Returns a project's config-log records, newest first — the same order and
 * scoping ListConfigEvents gives (§15.9). Project is mandatory here for the
 * same reason it is in the store: there is no cross-project read.
 */
export async function configEvents(project: string): Promise<ConfigEvent[]> {
  const out = await psql(
    `select coalesce(json_agg(row_to_json(t) order by t.created_at desc, t.id desc), '[]')
     from (select id, project, actor_worker, actor_session, action, payload, rationale, created_at
           from config_events where project = ${lit(project)}) t;`,
  )
  return JSON.parse(out.trim() || '[]') as ConfigEvent[]
}

/** Just the action verbs, newest first — what most assertions actually compare. */
export async function configActions(project: string): Promise<string[]> {
  return (await configEvents(project)).map((e) => e.action)
}

/**
 * Waits until a project's log holds at least `count` records and returns them.
 * The dual write commits inside the mutation's transaction (§15.4), so this is
 * normally satisfied on the first read — the poll exists so a slow machine
 * fails with a useful message instead of a bare length mismatch.
 */
export function waitForConfigEvents(
  project: string,
  count: number,
  timeoutMs = 10_000,
): Promise<ConfigEvent[]> {
  return poll(
    () => configEvents(project),
    (rows) => rows.length >= count,
    timeoutMs,
    `${count} config event(s) in ${project}`,
  )
}

/** True when the stack's postgres is reachable — lets a spec skip rather than error. */
export async function configLogReadable(): Promise<boolean> {
  try {
    await psql('select 1;')
    return true
  } catch {
    return false
  }
}
