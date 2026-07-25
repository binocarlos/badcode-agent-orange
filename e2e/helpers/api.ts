import type { APIRequestContext, APIResponse } from '@playwright/test'

// Product-layer API fixtures for the standalone-stack e2e.
//
// Every feature test needs the same four things: a login, a token scoped to a
// throwaway project, a typed client over the /agent/* routes, and a way to wait
// for something the backend does asynchronously. They live here so a feature
// spec is a story about the feature rather than a pile of fetch calls.
//
// Wire shapes below are transcribed from the handlers in go/httpapi (workers.go,
// project_settings.go, events.go) and the row structs in go/agentdb — when a
// handler changes, this file is what must change with it.
//
// Everything is driven through the web origin (http://localhost:8080), not
// agentd directly: nginx proxies /auth/ and /agent/ to agentd (deploy/
// web.nginx.conf), so the tests exercise the same path a browser does.

export const TEST_EMAIL = 'test@example.com'
export const TEST_PASSWORD = 'orange-e2e'

/** Projects the stack-e2e overlay maps to the test account (AGENTKIT_PROJECT_MAP). */
export const MAPPED_PROJECTS = ['apples-oranges', 'pears-plums'] as const

/** Where a permalink points: the web UI's externally reachable base URL. */
export function permalinkBase(): string {
  return (
    process.env.AGENTKIT_PUBLIC_BASE_URL ||
    process.env.STACK_BASE_URL ||
    'http://localhost:8080'
  ).replace(/\/$/, '')
}

/** The canonical session permalink (cmd/agentd/permalink.go, web/src/permalink.ts). */
export function sessionPermalink(project: string, session: string): string {
  return `${permalinkBase()}/p/${encodeURIComponent(project)}/s/${encodeURIComponent(session)}`
}

// ── Row shapes (go/agentdb) ─────────────────────────────────────────────────

export interface ProjectSettings {
  project: string
  base_image: string
  system_prompt: string
  mcp_config: Record<string, unknown>
  attention_channel: Record<string, unknown>
  max_concurrent_jobs: number
  daily_tokens_soft: number
  daily_tokens_hard: number
  briefing_max_bytes: number
  snapshot_ttl_days: number
  updated_at: number
}

export interface Worker {
  project: string
  name: string
  description: string
  system_prompt: string
  mcp_config: Record<string, unknown>
  image: string
  briefing?: string[] | null
  max_instances: number
  enabled: boolean
  created_at: number
  updated_at: number
}

/** The PUT body for a worker. Absent fields take their default — PUT replaces. */
export interface WorkerBody {
  description?: string
  system_prompt?: string
  mcp_config?: Record<string, unknown>
  image?: string
  max_instances?: number
  briefing?: string[] | null
  enabled?: boolean
}

export interface EventEnvelope {
  depth: number
  source: 'worker' | 'external' | 'schedule' | 'core'
  worker: string
  session_id: string
  interactive: boolean
  attention_requested: boolean
  reason?: string
}

export interface ProjectEvent {
  id: string
  project: string
  type: string
  text: string
  envelope: EventEnvelope
  occurred_at: number
  created_at: number
  delivered: boolean
}

export interface Subscription {
  id: string
  project: string
  event_type: string
  filter: Record<string, unknown>
  worker: string
  max_firings_per_hour: number
  enabled: boolean
  created_at: number
  updated_at: number
}

export interface EventDelivery {
  id: string
  project: string
  event_id: string
  subscription_id: string
  session_id: string
  status: 'pending' | 'running' | 'ok' | 'failed' | 'awaiting_human' | 'rate_limited'
  started_at: number
  ended_at: number
  created_at: number
  updated_at: number
}

/** One persisted message of a session transcript. */
export interface SessionMessage {
  id: string
  session_id: string
  role: string
  content: string
  sequence_num: number
  created_at: number
}

/** What the in-image agent reports about a turn (sandbox `session_info`). */
export interface SessionInfo {
  tools: string[]
  model: string
  mcpServers: Array<{ name: string; status: string }>
}

/** The session row, trimmed to what the e2e suite asserts on. */
export interface SessionRow {
  id: string
  customer: string
  status: string
  title: string
  worker?: string
}

// ── Login ───────────────────────────────────────────────────────────────────

export interface Login {
  email: string
  projects: Array<{ id: string; token: string }>
  wildcard?: boolean
  login_token?: string
}

/**
 * Logs in with the fixed stack-e2e password account and returns its project
 * tokens plus the wildcard login token (cmd/agentd/googleauth.go). The account
 * is an implicit wildcard, so it can mint tokens for project ids that do not
 * exist yet — which is how a test gets an empty project of its own.
 */
export async function login(request: APIRequestContext): Promise<Login> {
  const resp = await request.post('/auth/password', {
    data: { email: TEST_EMAIL, password: TEST_PASSWORD },
  })
  if (!resp.ok()) {
    throw new Error(`login failed: ${resp.status()} ${await resp.text()}`)
  }
  return (await resp.json()) as Login
}

/** Exchanges a wildcard login token for a token scoped to `project`. */
export async function mintProjectToken(
  request: APIRequestContext,
  loginToken: string,
  project: string,
): Promise<string> {
  const resp = await request.post('/auth/project-token', {
    data: { token: loginToken, project },
  })
  if (!resp.ok()) {
    throw new Error(`project-token for ${project} failed: ${resp.status()} ${await resp.text()}`)
  }
  return ((await resp.json()) as { id: string; token: string }).token
}

/**
 * A run-scoped project id. Project ids must be kebab-case and <= 64 chars
 * (authProjectTokenHandler validates), and a unique one per test keeps repeated
 * runs against one long-lived stack from colliding.
 */
export function uniqueProject(prefix = 'e2e'): string {
  const stamp = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`
  return `${prefix}-${stamp}`.toLowerCase()
}

// ── The project client ──────────────────────────────────────────────────────

/**
 * A typed client over the /agent/* routes, bound to one project's token.
 *
 * Two levels on purpose: the named methods throw on a non-2xx (a test asserting
 * a feature should not have to check status codes), while `raw` returns the
 * APIResponse untouched so isolation tests can assert 404/403 without a throw.
 */
export class ProjectClient {
  constructor(
    private readonly request: APIRequestContext,
    readonly project: string,
    readonly token: string,
  ) {}

  private headers(): Record<string, string> {
    return { Authorization: `Bearer ${this.token}` }
  }

  /** The unchecked escape hatch: returns the response whatever its status. */
  raw(method: 'GET' | 'PUT' | 'POST' | 'DELETE', path: string, data?: unknown): Promise<APIResponse> {
    const opts = { headers: this.headers(), ...(data === undefined ? {} : { data }) }
    switch (method) {
      case 'GET':
        return this.request.get(path, opts)
      case 'PUT':
        return this.request.put(path, opts)
      case 'POST':
        return this.request.post(path, opts)
      case 'DELETE':
        return this.request.delete(path, opts)
    }
  }

  private async json<T>(method: 'GET' | 'PUT' | 'POST' | 'DELETE', path: string, data?: unknown): Promise<T> {
    const resp = await this.raw(method, path, data)
    if (!resp.ok()) {
      throw new Error(`${method} ${path} → ${resp.status()}: ${await resp.text()}`)
    }
    return (await resp.json()) as T
  }

  // ── Project settings (§5) ─────────────────────────────────────────────────

  getSettings(): Promise<ProjectSettings> {
    return this.json<ProjectSettings>('GET', '/agent/project-settings')
  }

  /** PUT is whole-object: an absent field is written as its zero value. */
  putSettings(body: Partial<ProjectSettings>): Promise<ProjectSettings> {
    return this.json<ProjectSettings>('PUT', '/agent/project-settings', body)
  }

  // ── Workers (§6) ──────────────────────────────────────────────────────────

  async listWorkers(): Promise<Worker[]> {
    const { workers } = await this.json<{ workers: Worker[] }>('GET', '/agent/workers')
    return workers ?? []
  }

  getWorker(name: string): Promise<Worker> {
    return this.json<Worker>('GET', `/agent/workers/${encodeURIComponent(name)}`)
  }

  putWorker(name: string, body: WorkerBody = {}): Promise<Worker> {
    return this.json<Worker>('PUT', `/agent/workers/${encodeURIComponent(name)}`, body)
  }

  async deleteWorker(name: string): Promise<void> {
    const resp = await this.raw('DELETE', `/agent/workers/${encodeURIComponent(name)}`)
    if (resp.status() !== 204) {
      throw new Error(`DELETE worker ${name} → ${resp.status()}: ${await resp.text()}`)
    }
  }

  /**
   * Flips `enabled` and changes nothing else, by reading the stored row and
   * writing it back with one field different.
   *
   * The read-modify-write is the point, not ceremony. PUT is whole-object, and
   * the config log picks `worker_enable`/`worker_disable` only when every other
   * field is byte-identical (§15.3) — so a body that merely omits `mcp_config`
   * writes null over the stored `{}` and the change is logged as a
   * `worker_update` instead. Sending the row back entire is what makes a toggle
   * a toggle.
   */
  async toggleWorkerEnabled(name: string, enabled: boolean): Promise<Worker> {
    const stored = await this.getWorker(name)
    const body: WorkerBody = {
      description: stored.description,
      system_prompt: stored.system_prompt,
      mcp_config: stored.mcp_config,
      image: stored.image,
      max_instances: stored.max_instances,
      enabled,
    }
    if (stored.briefing != null) body.briefing = stored.briefing
    return this.putWorker(name, body)
  }

  // ── Events (§8) ───────────────────────────────────────────────────────────

  /** Posts an external trigger. Core stamps the envelope; a sender cannot. */
  postEvent(body: { type: string; text?: string }): Promise<ProjectEvent> {
    return this.json<ProjectEvent>('POST', '/agent/events', body)
  }

  async listEvents(opts: { type?: string; limit?: number; offset?: number } = {}): Promise<ProjectEvent[]> {
    const { events } = await this.json<{ events: ProjectEvent[] }>('GET', `/agent/events${query(opts)}`)
    return events ?? []
  }

  createSubscription(body: {
    event_type: string
    worker: string
    filter?: Record<string, unknown>
    max_firings_per_hour?: number
    enabled?: boolean
  }): Promise<Subscription> {
    return this.json<Subscription>('POST', '/agent/subscriptions', body)
  }

  async listSubscriptions(): Promise<Subscription[]> {
    const { subscriptions } = await this.json<{ subscriptions: Subscription[] }>('GET', '/agent/subscriptions')
    return subscriptions ?? []
  }

  updateSubscription(id: string, body: Record<string, unknown>): Promise<Subscription> {
    return this.json<Subscription>('PUT', `/agent/subscriptions/${encodeURIComponent(id)}`, body)
  }

  async deleteSubscription(id: string): Promise<void> {
    await this.json<{ deleted: boolean }>('DELETE', `/agent/subscriptions/${encodeURIComponent(id)}`)
  }

  async listDeliveries(
    opts: { event_id?: string; subscription_id?: string; status?: string; limit?: number } = {},
  ): Promise<EventDelivery[]> {
    const { deliveries } = await this.json<{ deliveries: EventDelivery[] }>('GET', `/agent/deliveries${query(opts)}`)
    return deliveries ?? []
  }

  // ── Waiting for the backend to catch up ───────────────────────────────────

  /**
   * Polls deliveries until `predicate` accepts the list, or the timeout expires.
   * The router (E3) writes these rows asynchronously, so any assertion about a
   * job having started is a wait, never a single read.
   */
  waitForDeliveries(
    predicate: (rows: EventDelivery[]) => boolean,
    opts: { event_id?: string; subscription_id?: string; timeoutMs?: number } = {},
  ): Promise<EventDelivery[]> {
    const { timeoutMs = 30_000, ...filter } = opts
    return poll(() => this.listDeliveries(filter), predicate, timeoutMs, 'deliveries')
  }

  /** Polls the event log until `predicate` accepts it. */
  waitForEvents(
    predicate: (rows: ProjectEvent[]) => boolean,
    opts: { type?: string; timeoutMs?: number } = {},
  ): Promise<ProjectEvent[]> {
    const { timeoutMs = 30_000, ...filter } = opts
    return poll(() => this.listEvents(filter), predicate, timeoutMs, 'events')
  }

  // ── Sessions ──────────────────────────────────────────────────────────────

  /** Creates a session and returns its id. */
  async createSession(body: { job?: string; persona?: string; systemPrompt?: string } = {}): Promise<string> {
    const { id } = await this.json<{ id: string }>('POST', '/agent/session', body)
    return id
  }

  /**
   * Sends a message and waits for the turn to finish.
   *
   * The route answers with the turn's SSE stream, so the response body arriving
   * complete *is* the end-of-turn signal — no polling needed.
   */
  async sendMessage(sessionId: string, content: string, timeoutMs = 120_000): Promise<string> {
    const resp = await this.request.post(`/agent/session/${encodeURIComponent(sessionId)}/message`, {
      headers: this.headers(),
      data: { content },
      timeout: timeoutMs,
    })
    if (!resp.ok()) throw new Error(`send message → ${resp.status()}: ${await resp.text()}`)
    return resp.text()
  }

  /** Snapshots the session and destroys its container (§4.5's snapshot half). */
  async archiveSession(sessionId: string): Promise<{ kind: string; ref: string }> {
    return this.json('POST', `/agent/session/${encodeURIComponent(sessionId)}/archive`)
  }

  /** Re-provisions an archived session — the resume path that must re-supply MCP config. */
  async restoreSession(sessionId: string): Promise<{ SessionID: string; State: string }> {
    return this.json('POST', `/agent/session/${encodeURIComponent(sessionId)}/restore`)
  }

  /**
   * The `session_info` payloads the in-image agent emitted for this session.
   *
   * This is the one window onto what the *container* actually received: the
   * harness reports the model, the tool names it was given, and every MCP
   * server with its connection status. `status: "connected"` means a real
   * JSON-RPC handshake with that server succeeded and its tool list came back —
   * which is as close to "callable" as mock mode can get, since the mock model
   * emits a fixed script and can never be made to invoke a tool.
   */
  async sessionInfoEvents(sessionId: string): Promise<SessionInfo[]> {
    const rows = await this.queryEvents(sessionId)
    return rows.filter((e) => e.type === 'session_info').map((e) => e.data as unknown as SessionInfo)
  }

  /** Every stored SSE event for a session, oldest first. */
  async queryEvents(sessionId: string): Promise<Array<{ type: string; data: Record<string, unknown> }>> {
    const body = await this.json<{ events?: unknown[] }>(
      'GET',
      `/agent/session/${encodeURIComponent(sessionId)}/query-events`,
    )
    const out: Array<{ type: string; data: Record<string, unknown> }> = []
    for (const row of body.events ?? []) {
      const raw = (row as { events?: unknown }).events
      const parsed = typeof raw === 'string' ? (JSON.parse(raw) as unknown[]) : ((raw ?? []) as unknown[])
      for (const ev of parsed) {
        const e = ev as { type?: string; data?: Record<string, unknown> }
        if (e.type) out.push({ type: e.type, data: e.data ?? {} })
      }
    }
    return out
  }

  /** The `error` events a session emitted — how the in-image agent fails loudly. */
  async errorEvents(sessionId: string): Promise<Array<{ code?: string; message?: string }>> {
    return (await this.queryEvents(sessionId))
      .filter((e) => e.type === 'error')
      .map((e) => e.data as { code?: string; message?: string })
  }

  /** Waits for the in-image agent to report a session_info matching `predicate`. */
  waitForSessionInfo(
    sessionId: string,
    predicate: (info: SessionInfo[]) => boolean,
    timeoutMs = 60_000,
  ): Promise<SessionInfo[]> {
    return poll(
      () => this.sessionInfoEvents(sessionId),
      predicate,
      timeoutMs,
      `session_info for ${sessionId}`,
    )
  }

  /** The persisted transcript of a session — what a replay has to work from. */
  async listMessages(sessionId: string): Promise<SessionMessage[]> {
    const { messages } = await this.json<{ count: number; messages: SessionMessage[]; total: number }>(
      'GET',
      `/agent/session/${encodeURIComponent(sessionId)}/messages`,
    )
    return messages ?? []
  }

  /** The session row, including its status. */
  getSession(sessionId: string): Promise<SessionRow> {
    return this.json<SessionRow>('GET', `/agent/session/${encodeURIComponent(sessionId)}`)
  }

  /** The canonical permalink for a session in this project. */
  permalink(sessionId: string): string {
    return sessionPermalink(this.project, sessionId)
  }
}

/**
 * Logs in and returns a client for a brand-new, run-scoped project. This is the
 * fixture nearly every feature test starts with: an empty project nobody else
 * is writing to.
 */
export async function newProjectClient(
  request: APIRequestContext,
  prefix = 'e2e',
): Promise<ProjectClient> {
  const auth = await login(request)
  if (!auth.login_token) {
    throw new Error('login returned no wildcard login_token — is AGENTKIT_TEST_LOGIN set on agentd?')
  }
  const project = uniqueProject(prefix)
  const token = await mintProjectToken(request, auth.login_token, project)
  return new ProjectClient(request, project, token)
}

/**
 * A client for a project that already exists — typically one a browser test
 * created through the UI, so an assertion can be made against the API for the
 * same project. The wildcard test account can mint a token for any id.
 */
export async function projectClient(
  request: APIRequestContext,
  project: string,
): Promise<ProjectClient> {
  const auth = await login(request)
  const existing = auth.projects.find((p) => p.id === project)
  if (existing) return new ProjectClient(request, project, existing.token)
  if (!auth.login_token) throw new Error('login returned no wildcard login_token')
  return new ProjectClient(request, project, await mintProjectToken(request, auth.login_token, project))
}

/** A client for one of the pre-mapped projects (apples-oranges, pears-plums). */
export async function mappedProjectClient(
  request: APIRequestContext,
  project: string,
): Promise<ProjectClient> {
  const auth = await login(request)
  const found = auth.projects.find((p) => p.id === project)
  if (!found) {
    throw new Error(`login granted no token for ${project} (got ${auth.projects.map((p) => p.id).join(', ')})`)
  }
  return new ProjectClient(request, project, found.token)
}

// ── Small utilities ─────────────────────────────────────────────────────────

function query(params: Record<string, string | number | undefined>): string {
  const parts = Object.entries(params)
    .filter(([, v]) => v !== undefined && v !== '')
    .map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`)
  return parts.length ? `?${parts.join('&')}` : ''
}

/** Polls `read` until `predicate` accepts the value; throws with the last value. */
export async function poll<T>(
  read: () => Promise<T>,
  predicate: (value: T) => boolean,
  timeoutMs: number,
  what: string,
): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let last: T | undefined
  for (;;) {
    last = await read()
    if (predicate(last)) return last
    if (Date.now() >= deadline) {
      throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}; last value: ${JSON.stringify(last)}`)
    }
    await new Promise((r) => setTimeout(r, 250))
  }
}
