import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const exec = promisify(execFile)

// Direct access to the running stack's Postgres.
//
// Used only where the HTTP API cannot express what a test needs to assert or
// arrange. Every use is a small admission that a route is missing, so each one
// carries a comment saying which. Today there are two:
//
//   - reading `config_events` (no read route yet — helpers/configlog.ts)
//   - seeding a session's `mcp_servers` (no write path at all — see below)

const COMPOSE_PROJECT = process.env.STACK_COMPOSE_PROJECT || 'agent-orange-stack-e2e'
const PG_USER = process.env.POSTGRES_USER || 'agentorange'
const PG_DB = process.env.POSTGRES_DB || 'agentorange'

/** Runs one SQL statement in the stack's postgres and returns raw stdout. */
export async function psql(sql: string): Promise<string> {
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
export function lit(v: string): string {
  return `'${v.replace(/'/g, "''")}'`
}

/** True when the stack's postgres is reachable — lets a spec skip rather than error. */
export async function stackDbReadable(): Promise<boolean> {
  try {
    await psql('select 1;')
    return true
  } catch {
    return false
  }
}

/**
 * Writes a session's MCP configuration straight onto its row.
 *
 * This exists because **no API path can put MCP config on a session** today:
 * `POST /agent/session` has no `mcp_servers` field, and the project/worker
 * `mcp_config` that `SessionContextProvider` resolves is dropped before it
 * reaches the runner (see the red test in features/session-mcp.stack.spec.ts).
 *
 * Seeding the row is therefore the only way to exercise everything downstream
 * of it — the wire protocol, `${VAR}` resolution, and the resume path §4.5
 * requires. Delete this helper the day a route can do it.
 */
export async function seedSessionMCPServers(
  sessionId: string,
  servers: Record<string, unknown>,
): Promise<void> {
  const json = JSON.stringify(servers)
  const out = await psql(
    `UPDATE agent_sessions SET mcp_servers = ${lit(json)}::jsonb WHERE id = ${lit(sessionId)};`,
  )
  if (!out.includes('UPDATE 1')) {
    throw new Error(`seeding mcp_servers for ${sessionId} matched no row: ${out.trim()}`)
  }
}

/** Reads a session's stored MCP config back — the API does not expose it. */
export async function storedSessionMCPServers(sessionId: string): Promise<Record<string, unknown> | null> {
  const out = (
    await psql(`SELECT coalesce(mcp_servers::text, 'null') FROM agent_sessions WHERE id = ${lit(sessionId)};`)
  ).trim()
  return out === '' || out === 'null' ? null : (JSON.parse(out) as Record<string, unknown>)
}
