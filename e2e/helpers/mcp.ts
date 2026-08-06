import { execFile } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { promisify } from 'node:util'

const exec = promisify(execFile)

// Driving agentd's core MCP server (`POST /mcp`) from a test.
//
// # Why this is not an ordinary HTTP call
//
// Two things stand between a Playwright test and that endpoint, and both are
// facts about the deployment rather than problems to route around:
//
//  1. **It is not reachable from the host.** nginx proxies /auth/ and /agent/
//     to agentd; /mcp is deliberately not among them, and agentd's port is not
//     published — it shares DinD's network namespace so only containers can
//     reach it. So requests go through `docker compose exec dind wget`, which
//     is exactly the path a session container takes.
//  2. **It authenticates by session token, not project token.** The tools scope
//     themselves to the caller's session: `image_create` snapshots *this*
//     session's filesystem, `skill_install` writes into *this* session's
//     container. A token with no `sid` is refused, by design (§13.2, §14.2).
//
// The token is minted here rather than scraped out of a container: it is an
// HS256 JWT over the same claims the Runner issues (`customer`, `sid`), signed
// with the stack's SESSION key — derived from its JWT secret, see
// SESSION_SECRET below. The secret is a known constant of the test overlay
// (docker-compose.stack-e2e.yml), so this mints exactly the credential a real
// job carries — including, when asked, a deliberately broken one.

const COMPOSE_PROJECT = process.env.STACK_COMPOSE_PROJECT || 'agent-orange-stack-e2e'
const MCP_URL = process.env.STACK_MCP_URL || 'http://localhost:8099/mcp'
/** Matches AGENTKIT_JWT_SECRET in docker-compose.stack-e2e.yml. */
const JWT_SECRET = process.env.AGENTKIT_JWT_SECRET || 'stack-e2e-secret'

// Session tokens are NOT signed with the API secret. They are a separate
// credential class with its own key, derived from the API secret by HMAC-SHA256
// under a fixed label, so that a container's token cannot authenticate as its
// project on the ordinary API routes (doc 22, RD30). This must stay in step
// with `resolveSessionSecret` in go/cmd/agentd/sessionsecret.go — same label,
// same construction — and with the AGENTKIT_SESSION_JWT_SECRET override, which
// wins there and here.
const SESSION_SECRET_LABEL = 'agent-orange/session-token/v1'
const SESSION_SECRET: Buffer | string =
  process.env.AGENTKIT_SESSION_JWT_SECRET?.trim() ||
  createHmac('sha256', JWT_SECRET).update(SESSION_SECRET_LABEL).digest()

function b64url(input: Buffer | string): string {
  return Buffer.from(input).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * Mints a session token: the credential the Runner injects as `SESSION_TOKEN`.
 *
 * `sid` is what makes a caller a *session*; omit it to build the token the
 * session-scoped tools must refuse.
 */
export function mintSessionToken(opts: {
  project: string
  sessionId?: string
  expiresInSeconds?: number
}): string {
  const now = Math.floor(Date.now() / 1000)
  const claims: Record<string, unknown> = {
    customer: opts.project,
    email: 'e2e@example.com',
    iat: now,
    exp: now + (opts.expiresInSeconds ?? 3600),
  }
  if (opts.sessionId !== undefined) claims.sid = opts.sessionId

  const header = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }))
  const payload = b64url(JSON.stringify(claims))
  const signature = b64url(createHmac('sha256', SESSION_SECRET).update(`${header}.${payload}`).digest())
  return `${header}.${payload}.${signature}`
}

/** One tool result: MCP reports tool failure in-band, as `isError` (§ transport). */
export interface ToolResult {
  /** Parsed `structuredContent` when the tool returned one. */
  structured: any
  /** The text block — for a failure, the whole report. */
  text: string
  /** True when the tool failed. NOT a transport error: the call itself succeeded. */
  isError: boolean
}

/** A JSON-RPC transport error (bad token, unknown method) — distinct from isError. */
export class MCPTransportError extends Error {
  constructor(
    readonly code: number,
    message: string,
  ) {
    super(`MCP JSON-RPC error ${code}: ${message}`)
  }
}

let nextId = 1

/** Sends one JSON-RPC request to agentd's /mcp, through DinD. */
async function rpc(token: string, method: string, params?: unknown): Promise<any> {
  const body = JSON.stringify({ jsonrpc: '2.0', id: nextId++, method, ...(params ? { params } : {}) })
  const { stdout } = await exec('docker', [
    'compose',
    '-p',
    COMPOSE_PROJECT,
    'exec',
    '-T',
    'dind',
    'wget',
    '-qO-',
    '--header=Content-Type: application/json',
    `--header=Authorization: Bearer ${token}`,
    `--post-data=${body}`,
    MCP_URL,
  ]).catch((err: { stderr?: string; stdout?: string; message: string }) => {
    // wget exits non-zero on 4xx/5xx and prints the status to stderr, which is
    // the only place an auth refusal shows up.
    throw new Error(`MCP request failed (${method}): ${err.stderr?.trim() || err.message}`)
  })

  const parsed = JSON.parse(stdout) as { result?: any; error?: { code: number; message: string } }
  if (parsed.error) throw new MCPTransportError(parsed.error.code, parsed.error.message)
  return parsed.result
}

/** An MCP client bound to one session's credential. */
export class MCPClient {
  constructor(readonly token: string) {}

  /** The tool names this server exposes, in registration order. */
  async listTools(): Promise<string[]> {
    const result = await rpc(this.token, 'tools/list')
    return (result.tools as Array<{ name: string }>).map((t) => t.name)
  }

  /**
   * Calls a tool. A tool that fails comes back with `isError: true` and its
   * report in `text` — the call itself succeeded, so this does not throw.
   * Only a transport-level refusal (a bad token) throws.
   */
  async call(name: string, args: Record<string, unknown> = {}): Promise<ToolResult> {
    const result = await rpc(this.token, 'tools/call', { name, arguments: args })
    const text = (result.content as Array<{ type: string; text?: string }> | undefined)
      ?.filter((c) => c.type === 'text')
      .map((c) => c.text ?? '')
      .join('\n')
    return {
      structured: result.structuredContent,
      text: text ?? '',
      isError: result.isError === true,
    }
  }

  /** Calls a tool and fails the caller if the tool reported an error. */
  async callOK(name: string, args: Record<string, unknown> = {}): Promise<any> {
    const out = await this.call(name, args)
    if (out.isError) throw new Error(`${name} failed: ${out.text}`)
    return out.structured
  }
}

/** A client for a session, with the credential that session would carry. */
export function sessionMCP(project: string, sessionId: string): MCPClient {
  return new MCPClient(mintSessionToken({ project, sessionId }))
}

/** A client whose token names a project but no session — must be refused. */
export function projectOnlyMCP(project: string): MCPClient {
  return new MCPClient(mintSessionToken({ project }))
}
