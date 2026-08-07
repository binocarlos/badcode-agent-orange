import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import {
  test,
  expect,
  request as playwrightRequest,
  type APIRequestContext,
  type APIResponse,
} from '@playwright/test'
import { mappedProjectClient } from '../helpers/api'
import { lit, psql, stackDbReadable } from '../helpers/stackdb'

const exec = promisify(execFile)

// T17 of design/2026-08-06-embeddable-agent-orange.md — the whole "embeddable
// Orange" feature, end to end, against the compose stack.
//
// This is the only test that holds the wiring T1–T16/T18 added, and most of that
// wiring is the kind unit tests cannot reach: an argument passed to a
// constructor in main.go, a route registered on the right mux, a header emitted
// by nginx, a token minted with the secret the middleware verifies with. The
// Discovered Issues Log calls that pattern out four separate times ("pass the
// wrong secret or a nil store and every unit test still passes while the route
// is dead in production"). So the assertions below are deliberately made from
// OUTSIDE the process — over HTTP on the web origin, in a browser, and against
// Postgres — and never against a Go fake.
//
// # The shape of the story
//
// An embedding application (Agent Wolf) holds a project API key and nothing
// else. With it, it:
//
//   1. creates a session under a NAME it chose             (T6/T7)
//   2. attaches a session-mode schedule to that name       (T9)
//   3. lets two firings run, with a snapshot in between,
//      and finds the second one working on the first one's
//      workspace                                           (§4.5 restore)
//   4. fetches the file the agent wrote as an artifact, by
//      session name and path — never holding a uuid        (T8)
//   5. mints a short-lived embed token and drops it in an
//      iframe URL fragment                                 (T10/T12/T13)
//   6. reads the memory the scheduled run wrote, in FULL,
//      over HTTP                                            (T15/T18)
//
// # Why the project is `apples-oranges` and not a fresh one
//
// Every other feature spec mints a run-scoped `e2e-…` project. This one cannot:
// an API key is boot configuration (`AGENTKIT_PROJECT_MAP` names the env var
// holding it), so the only project in this stack that HAS a key is the one the
// overlay declares. That is also the reason the suite's own leak checks do not
// cover this file — `deleteE2ESchedules` finds `e2e-` projects only — and the
// reason cleanup here is a `finally`-shaped afterAll rather than a courtesy.
//
// # Why a mock script
//
// The stack's mock model serves fixed canned text and cannot be made to call a
// tool, and three of the six assertions above are about what a tool DID. So the
// interesting test gates on `STACK_MOCK_SCRIPT` and is driven by
// `e2e/mock-scripts/embedding.json`:
//
//     ./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/embedding.json \
//         -- features/embedding.stack.spec.ts
//
// The script's two rules are partitioned with `absent`, not by turn index, and
// that is a fact about this harness worth writing down: the in-image agent calls
// the SDK with `persistSession: false` and NO resume, folding prior turns into
// the system prompt as text (`sandbox/src/harness/claude-agent-sdk.ts:186-199`).
// The assistant-message count therefore RESETS on every user message, so turn
// indices are per-firing and the two firings are told apart by a marker the
// first one leaves in its final reply.

// ── Fixed stack facts (docker-compose.stack-e2e.yml) ────────────────────────

/** The one project in this stack with an API key, and the key itself. */
const PROJECT = 'apples-oranges'
const API_KEY = process.env.STACK_E2E_API_KEY || 'stack-e2e-apples-api-key-0123456789'

/** The origins `AGENTKIT_PROJECT_MAP` lets frame the embed page (T1/T13). */
const ALLOWED_ORIGINS = ['http://localhost:5173', 'https://wolf.example.test']

// ── Markers the mock script plants (e2e/mock-scripts/embedding.json) ────────

/** In the schedule input; selects the script's rules. */
const HYP = '[T17-HYP]'
/** The first firing's final reply — also the `absent` key that retires rule 1. */
const RUN1_DONE = '[T17-RUN1-DONE]'
const RUN2_DONE = '[T17-RUN2-DONE]'
/** In the workspace file, one per firing. */
const ROUND_1 = '[T17-ROUND-1]'
const ROUND_2 = '[T17-ROUND-2]'
/** Head and tail of the >500-byte memory body, so truncation is detectable. */
const MEMORY_HEAD = '[T17-MEMORY]'
const MEMORY_TAIL = '[T17-MEMORY-TAIL]'
/** The `name=` label the script writes the memory under. */
const MEMORY_NAME = 't17-hypothesis'
/** The path the agent writes, relative to /workspace. */
const ARTIFACT_PATH = 'hypothesis.md'

const NEEDS_SCRIPT =
  'needs the scripted mock model: ./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/embedding.json'

// ── Small helpers ───────────────────────────────────────────────────────────

/**
 * A run-scoped session name.
 *
 * Names are project-unique for ever (there is no rename and no by-name delete),
 * and this project is shared with whatever a previous run or a human left
 * behind — `hypothesis-a` is already taken. Kebab-case only: lowercase letters,
 * digits and single hyphens (`agentdb.ValidateSessionName`), which base36 of the
 * clock and of a random satisfy.
 */
function uniqueSessionName(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`
}

/** Everything this run created, so the afterAll can release it whatever happens. */
const created = { sessions: [] as string[], schedules: [] as string[] }

class KeyClient {
  constructor(
    private readonly request: APIRequestContext,
    private readonly key = API_KEY,
  ) {}

  raw(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, data?: unknown): Promise<APIResponse> {
    const opts = { headers: { 'X-API-Key': this.key }, ...(data === undefined ? {} : { data }) }
    switch (method) {
      case 'GET':
        return this.request.get(path, opts)
      case 'POST':
        return this.request.post(path, opts)
      case 'PUT':
        return this.request.put(path, opts)
      case 'DELETE':
        return this.request.delete(path, opts)
    }
  }

  async json<T>(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, data?: unknown): Promise<T> {
    const resp = await this.raw(method, path, data)
    if (!resp.ok()) throw new Error(`${method} ${path} → ${resp.status()}: ${await resp.text()}`)
    return (await resp.json()) as T
  }

  /** Creates a NAMED session and records it for cleanup. */
  async createNamedSession(name: string, job: string): Promise<string> {
    const { id } = await this.json<{ id: string }>('POST', '/agent/session', { name, job })
    created.sessions.push(id)
    return id
  }

  /** Every stored SSE event for a session, oldest first (see ProjectClient.queryEvents). */
  async events(sessionId: string): Promise<Array<{ type: string; data: Record<string, unknown> }>> {
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
}

/** One completed tool call: what the model asked for, and what came back. */
interface ToolCall {
  name: string
  output: string
  isError: boolean
}

/**
 * Pairs `tool_use_start` with its `tool_use_end` by call id.
 *
 * The OUTPUT is the point of this whole helper. A scripted model emits the same
 * text whatever happens, so its prose proves nothing; the tool result is the one
 * thing in the stream that the container — not the script — produced.
 */
function toolCalls(events: Array<{ type: string; data: Record<string, unknown> }>): ToolCall[] {
  const names = new Map<string, string>()
  const out: ToolCall[] = []
  for (const ev of events) {
    const id = String(ev.data.toolCallId ?? '')
    if (ev.type === 'tool_use_start' && id) names.set(id, String(ev.data.toolName ?? ''))
    if (ev.type === 'tool_use_end' && id) {
      out.push({
        name: names.get(id) ?? '',
        output: String(ev.data.output ?? ''),
        isError: ev.data.isError === true,
      })
    }
  }
  return out
}

/** Everything the model said, concatenated — the text half of the stream. */
function assistantText(events: Array<{ type: string; data: Record<string, unknown> }>): string {
  return events
    .filter((e) => e.type === 'content_delta')
    .map((e) => String(e.data.delta ?? ''))
    .join('')
}

/**
 * Polls at a human interval with a progress line, for the minute-scale waits.
 *
 * `poll` from helpers/api reads every 250ms, which is right for a backend
 * catching up in under a second and wrong here: a schedule fires on a wall-clock
 * minute boundary and there is no catch-up, so these waits are minutes long by
 * construction and a silent one is indistinguishable from a hang.
 */
async function waitMinutes<T>(
  read: () => Promise<T>,
  predicate: (v: T) => boolean,
  what: string,
  timeoutMs: number,
  describe: (v: T) => string = () => '',
): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let last = ''
  for (;;) {
    const value = await read()
    if (predicate(value)) return value
    const now = describe(value)
    if (now !== last) {
      last = now
      console.log(`[embedding] waiting for ${what}${now ? ` — ${now}` : ''}`)
    }
    if (Date.now() >= deadline) {
      throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}; last: ${now}`)
    }
    await new Promise((r) => setTimeout(r, 3_000))
  }
}

/** Decodes a JWT payload without verifying it — a test reading its own claims. */
function claimsOf(token: string): Record<string, unknown> {
  const [, payload] = token.split('.')
  return JSON.parse(Buffer.from(payload, 'base64url').toString('utf8')) as Record<string, unknown>
}

/**
 * Whether a session container is live inside DinD.
 *
 * Asked of Docker rather than of the API, because the claim it supports is
 * exactly that the API's view and the host's agree: an archive that left the
 * container running would still report a snapshot handle.
 */
async function containerRunning(sessionId: string): Promise<boolean> {
  try {
    const { stdout } = await exec('docker', [
      'compose',
      '-p',
      process.env.STACK_COMPOSE_PROJECT || 'agent-orange-stack-e2e',
      'exec',
      '-T',
      'dind',
      'docker',
      'ps',
      '-q',
      '--filter',
      `name=sandbox-${sessionId}`,
    ])
    return stdout.trim() !== ''
  } catch {
    return false
  }
}

// ── The always-on check: the framing header ─────────────────────────────────

test.describe('embedding: the framing contract (T13)', () => {
  // Cheap, sessionless, and it holds the one part of the feature that is pure
  // nginx configuration. It needs no model at all, so it runs on every stack
  // pass rather than only the scripted ones.
  test('the embed page declares frame-ancestors from the project map; the console page does not', async ({
    request,
  }) => {
    const embed = await request.get('/embed/session/does-not-matter')
    expect(embed.status()).toBe(200)
    const csp = embed.headers()['content-security-policy'] ?? ''
    expect(csp).toContain('frame-ancestors')
    for (const origin of ALLOWED_ORIGINS) {
      expect(csp).toContain(origin)
    }
    // `frame-ancestors` restricts framing, never a top-level load, which is why
    // the browser step below can navigate to this page directly.
    expect(csp).not.toContain("'none'")

    // The console is NOT under the /embed/ prefix and must carry no such header
    // — the header is bound to the path prefix, not to the document (see the
    // Discovered Issues Log's T13 entry).
    const consolePage = await request.get('/')
    expect(consolePage.status()).toBe(200)
    expect(consolePage.headers()['content-security-policy']).toBeUndefined()
  })
})

// ── The headline: the whole loop ────────────────────────────────────────────

test.describe('embedding: an API key drives a named session end to end', () => {
  test.describe.configure({ mode: 'serial' })

  let key: KeyClient
  const sessionName = uniqueSessionName('hypothesis-t17')
  const siblingName = uniqueSessionName('sibling-t17')
  let sessionId = ''
  let siblingId = ''
  let scheduleId = ''

  test.beforeAll(async () => {
    expect(
      await stackDbReadable(),
      'this spec reads agent_sessions directly to hold T15’s wiring — see helpers/stackdb.ts',
    ).toBe(true)
  })

  // Not optional housekeeping. Each session holds one of agentd's 100 host ports
  // until it is deleted, and a `* * * * *` schedule left behind wakes a session
  // every minute for ever — the exact pair that took this stack down on
  // 2026-07-26. This project is not `e2e-` prefixed, so the suite's own teardown
  // sweep does NOT cover it: this block is the only thing that does.
  test.afterAll(async () => {
    // `request` is a test-scoped fixture and unreachable from afterAll, so this
    // builds its own context — which is the right shape anyway: cleanup must run
    // when the test died before it ever touched a fixture.
    const ctx = await playwrightRequest.newContext({
      baseURL: process.env.STACK_BASE_URL || 'http://localhost:8080',
    })
    const cleaner = new KeyClient(ctx)
    // Schedules first. Deleting the sessions while a `* * * * *` row still names
    // one just races the scheduler, which would wake it again a minute later.
    for (const id of created.schedules.splice(0)) {
      await cleaner.raw('DELETE', `/agent/schedules/${encodeURIComponent(id)}`).catch(() => {})
    }
    for (const id of created.sessions.splice(0)) {
      await cleaner.raw('DELETE', `/agent/session/${encodeURIComponent(id)}`).catch(() => {})
    }
    await ctx.dispose()
  })

  test('a named session, a session schedule that restores it, an artifact by name, a memory in full, and an embed page with no login', async ({
    request,
    page,
  }) => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)
    // Two firings on wall-clock minute boundaries, with a snapshot + restore
    // between them, and no catch-up if one is missed. The floor is set by the
    // product, not by the polling.
    test.setTimeout(15 * 60_000)

    key = new KeyClient(request)

    // ── 1. A session, created with an API key, under a name the caller chose ──
    await test.step('the API key creates a named session and can find it again by name', async () => {
      sessionId = await key.createNamedSession(sessionName, 't17-embedding')
      expect(sessionId).not.toBe('')

      const byName = await key.json<{ id: string; name: string; customer: string }>(
        'GET',
        `/agent/sessions/by-name/${encodeURIComponent(sessionName)}`,
      )
      expect(byName).toMatchObject({ id: sessionId, name: sessionName, customer: PROJECT })

      // The two negatives that make the positive mean something: a wrong key is
      // refused, and an unknown name is indistinguishable from absent.
      const badKey = new KeyClient(request, 'not-the-key-0123456789012345')
      expect((await badKey.raw('GET', `/agent/sessions/by-name/${encodeURIComponent(sessionName)}`)).status()).toBe(401)
      expect((await key.raw('GET', '/agent/sessions/by-name/no-such-session-anywhere')).status()).toBe(404)
    })

    // ── T15's stack check: the core tools reach an HTTP-created session ──
    await test.step('the session row carries the core MCP server, and no worker or composed prompt', async () => {
      const row = (
        await psql(
          `SELECT coalesce(mcp_servers::text,'null') || E'\\x1e' || coalesce(worker,'') || E'\\x1e' ` +
            `|| coalesce(composed_prompt,'') FROM agent_sessions WHERE id = ${lit(sessionId)};`,
        )
      ).trim()
      const [mcpJSON, worker, composed] = row.split('\x1e')
      // T15's whole subject: `POST /agent/session` merges the host's core MCP
      // servers into the create request, so a plain chat session gets the tools.
      expect(Object.keys(JSON.parse(mcpJSON) as Record<string, unknown>)).toContain('core')
      // …and T15's explicit non-goals: the create path does NOT run ComposeJob,
      // so a console chat still emits no worker.finished and stores no prompt.
      expect(worker).toBe('')
      expect(composed).toBe('')
    })

    // ── 2a. A session-mode schedule, and its first firing ──
    await test.step('a session-mode schedule fires into the existing session', async () => {
      const sched = await key.json<{ id: string; enabled: boolean; target_session: string; worker?: string }>(
        'POST',
        '/agent/schedules',
        {
          target_session: sessionName,
          cron: '* * * * *',
          input: `${HYP} Take another pass over the hypothesis.`,
          rationale: 'e2e T17: prove a session schedule wakes a long-lived session',
        },
      )
      scheduleId = sched.id
      created.schedules.push(scheduleId)
      expect(sched.enabled).toBe(true)
      expect(sched.target_session).toBe(sessionName)
      // The two modes are exclusive: a session schedule names no worker, which
      // is what sends the firing down scheduler.fireSession rather than through
      // the dispatch gate.
      expect(sched.worker ?? '').toBe('')

      // The observable is the session's own event stream. A session-mode firing
      // writes no project event and no delivery row — deliberately (§8.6) — so
      // "did it fire?" can only be answered by what the session did.
      const events = await waitMinutes(
        () => key.events(sessionId),
        (evs) => assistantText(evs).includes(RUN1_DONE),
        `the first firing of schedule ${scheduleId} to complete`,
        7 * 60_000,
        (evs) => `${evs.length} events, ${toolCalls(evs).length} tool call(s)`,
      )

      const calls = toolCalls(events)
      // This is the T15 claim no unit test can reach: a CHAT session (no worker
      // on its row) resolved ${SESSION_TOKEN}, handshook with agentd's /mcp, and
      // got a real answer from a core tool.
      const wrote = calls.find((c) => c.name === 'mcp__core__memory_create')
      expect(wrote, `core memory tool was never called; calls: ${calls.map((c) => c.name).join(', ')}`).toBeDefined()
      expect(wrote?.isError).toBe(false)
      expect(wrote?.output).toContain(MEMORY_HEAD)
    })

    // ── 2b. Snapshot the container away, so the next firing has to restore ──
    await test.step('the session is snapshotted and its container destroyed', async () => {
      // Disabled first, and only for the length of the archive. The schedule row
      // is the same one throughout — this is two firings of ONE schedule — but a
      // firing landing mid-snapshot would race the archive for the container.
      await key.json('PUT', `/agent/schedules/${encodeURIComponent(scheduleId)}`, { enabled: false })

      const archived = await key.json<{ kind: string; ref: string }>(
        'POST',
        `/agent/session/${encodeURIComponent(sessionId)}/archive`,
      )
      expect(archived.ref).not.toBe('')

      // The claim being set up is "restore, not a fresh container", and it is
      // only worth anything if the container is genuinely gone first. Asked of
      // Docker, not of the API.
      expect(
        await waitMinutes(
          () => containerRunning(sessionId),
          (live) => !live,
          'the session container to be released by the archive',
          120_000,
          (live) => (live ? 'still running' : 'gone'),
        ),
      ).toBe(false)

      await key.json('PUT', `/agent/schedules/${encodeURIComponent(scheduleId)}`, { enabled: true })
    })

    // ── 3. The second firing, on the restored container ──
    await test.step('the second firing restores the snapshot and reads the first run’s file', async () => {
      const events = await waitMinutes(
        () => key.events(sessionId),
        (evs) => assistantText(evs).includes(RUN2_DONE),
        `the second firing of schedule ${scheduleId} to complete`,
        8 * 60_000,
        (evs) => `${evs.length} events, ${toolCalls(evs).length} tool call(s)`,
      )
      const calls = toolCalls(events)

      // THE headline assertion. The file was written before the snapshot, the
      // container was destroyed, and this `cat` ran after the restore — so the
      // bytes came out of the snapshot, not out of a fresh image.
      const cat = calls.find((c) => c.name === 'Bash' && c.output.includes(ROUND_1))
      expect(
        cat,
        `no Bash result carried ${ROUND_1}; outputs: ${calls.map((c) => `${c.name}:${c.output.slice(0, 80)}`).join(' | ')}`,
      ).toBeDefined()
      expect(cat?.isError).toBe(false)

      // …and the Wolf loop in miniature: the memory a scheduled run wrote is read
      // back through a core tool, IN FULL, inside the long-lived session.
      const read = calls.find((c) => c.name === 'mcp__core__memory_current')
      expect(read, 'memory_current was never called by the second firing').toBeDefined()
      expect(read?.isError).toBe(false)
      expect(read?.output).toContain(MEMORY_HEAD)
      expect(read?.output).toContain(MEMORY_TAIL)

      // Stop the clock before anything else: every further minute is another
      // container-waking firing this test has no use for.
      await key.raw('DELETE', `/agent/schedules/${encodeURIComponent(scheduleId)}?rationale=e2e+T17+finished`)
      created.schedules = created.schedules.filter((id) => id !== scheduleId)
      expect((await key.raw('GET', `/agent/schedules/${encodeURIComponent(scheduleId)}`)).status()).toBe(404)
    })

    // ── 4. The artifact, addressed the way an integrator addresses it ──
    await test.step('the artifact is fetched by session NAME and path, and carries the second run’s content', async () => {
      const listed = await waitMinutes(
        () =>
          key.json<Array<{ file_path?: string; filePath?: string; status?: string }>>(
            'GET',
            `/agent/sessions/by-name/${encodeURIComponent(sessionName)}/artifacts`,
          ),
        (rows) => rows.some((r) => (r.file_path ?? r.filePath ?? '').replace(/^\//, '') === ARTIFACT_PATH),
        `the ${ARTIFACT_PATH} artifact to be registered`,
        120_000,
        (rows) => `${rows.length} artifact(s)`,
      )
      expect(listed.length).toBeGreaterThan(0)

      // The bytes, by name and path — no uuid anywhere in the request. The wait
      // is for extraction: registration and the cat-out-of-the-container that
      // stores the blob are two different steps (runner.onArtifactRegistered).
      const body = await waitMinutes(
        async () => {
          const resp = await key.raw(
            'GET',
            `/agent/sessions/by-name/${encodeURIComponent(sessionName)}/artifacts/file?path=${ARTIFACT_PATH}`,
          )
          return { status: resp.status(), text: resp.ok() ? await resp.text() : '' }
        },
        (r) => r.status === 200 && r.text.includes(ROUND_2),
        `${ARTIFACT_PATH} to carry the second run’s content`,
        180_000,
        (r) => `status ${r.status}`,
      )
      // Upsert, not accumulate: artifacts dedup on (session, path), so the
      // second write replaced the first rather than adding a second row.
      expect(body.text).toContain(ROUND_2)
      expect(body.text).not.toContain(ROUND_1)
    })

    // ── 5/6. The memory over HTTP, in full ──
    let memoryId = ''
    let fullContent = ''
    await test.step('the memory the scheduled run wrote is readable over HTTP, untruncated', async () => {
      const current = await key.json<{
        id: string
        content: string
        labels: Record<string, string>
        created_by_worker: string
        created_by_session: string
      }>('GET', `/agent/memories/current?name=${encodeURIComponent(MEMORY_NAME)}`)

      memoryId = current.id
      fullContent = current.content
      expect(current.labels).toMatchObject({ name: MEMORY_NAME })
      // Written from inside THIS session's container, by a session with no
      // worker — the provenance that makes it T15's evidence and not some other
      // run's memory.
      expect(current.created_by_session).toBe(sessionId)
      expect(current.created_by_worker).toBe('')

      // T18's whole point. `GET /agent/memories` returns a MemorySearchResult
      // whose Snippet is cut at 500 bytes and which has no Content field at all;
      // these routes return the body.
      expect(fullContent.length).toBeGreaterThan(500)
      expect(fullContent).toContain(MEMORY_HEAD)
      expect(fullContent).toContain(MEMORY_TAIL)

      // The by-id route agrees with the by-name one, byte for byte.
      const byId = await key.json<{ id: string; content: string }>(
        'GET',
        `/agent/memories/${encodeURIComponent(memoryId)}`,
      )
      expect(byId.id).toBe(memoryId)
      expect(byId.content).toBe(fullContent)

      // And the contrast that shows the truncation is real rather than absent:
      // the SEARCH route, on the same memory, hands back a snippet capped at
      // 500 bytes and no content at all.
      const { memories } = await key.json<{
        memories: Array<{ id: string; snippet?: string; content?: string }>
      }>('GET', `/agent/memories?selector=${encodeURIComponent(`name=${MEMORY_NAME}`)}&limit=5`)
      const hit = memories.find((m) => m.id === memoryId)
      expect(hit, 'the search route did not return the memory the read routes did').toBeDefined()
      expect(hit?.content ?? '').toBe('')
      expect((hit?.snippet ?? '').length).toBeLessThanOrEqual(500)
      expect((hit?.snippet ?? '').length).toBeLessThan(fullContent.length)
    })

    // ── An embed token: minted with the key, confined to one session ──
    let embedToken = ''
    await test.step('an embed token is minted with the API key, and only with the API key', async () => {
      const minted = await key.json<{ token: string; expires_at: number }>('POST', '/agent/embed-token', {
        session: sessionName,
      })
      embedToken = minted.token
      expect(embedToken).not.toBe('')
      expect(minted.expires_at).toBeGreaterThan(Math.floor(Date.now() / 1000))

      const claims = claimsOf(embedToken)
      // The scope is a session ID even though the request named the session —
      // T3 refused to overload `sid`, so this token is not a container's token.
      expect(claims.scope).toBe(`session:${sessionId}`)
      expect(claims.sid ?? '').toBe('')
      expect(claims.customer).toBe(PROJECT)

      // A console JWT must NOT be able to mint one: a browser holds exactly that
      // credential, and without this check an embed token could mint itself a
      // fresh one for any session in the project.
      const consoleJWT = await mappedProjectClient(request, PROJECT)
      expect((await consoleJWT.raw('POST', '/agent/embed-token', { session: sessionName })).status()).toBe(403)
    })

    await test.step('the embed token reaches its own session and no other', async () => {
      siblingId = await key.createNamedSession(siblingName, 't17-sibling')

      const bearer = (path: string) =>
        request.get(path, { headers: { Authorization: `Bearer ${embedToken}` } })

      expect((await bearer(`/agent/session/${encodeURIComponent(sessionId)}/status`)).status()).toBe(200)
      // A sibling in the SAME project is 404, not 403: the scope is enforced in
      // ownsSession, and a distinguishable answer would be an existence oracle.
      expect((await bearer(`/agent/session/${encodeURIComponent(siblingId)}/status`)).status()).toBe(404)
      // …and by name, which is the route the embed page itself calls.
      expect((await bearer(`/agent/sessions/by-name/${encodeURIComponent(siblingName)}`)).status()).toBe(404)
      expect((await bearer(`/agent/sessions/by-name/${encodeURIComponent(sessionName)}`)).status()).toBe(200)

      // Release the sibling's port immediately; it has done its one job.
      await key.raw('DELETE', `/agent/session/${encodeURIComponent(siblingId)}`)
      created.sessions = created.sessions.filter((id) => id !== siblingId)
    })

    // ── The embed page itself, in a browser ──
    await test.step('the embed page renders the conversation and never shows a login screen', async () => {
      // The credential travels in the FRAGMENT. Playwright's goto sends
      // everything before the `#` to the server, so this is also the assertion
      // that the token never reaches an access log.
      await page.goto(`/embed/session/${encodeURIComponent(sessionName)}#token=${embedToken}`)

      // The failure card and the console login are the two wrong answers, and
      // they are wrong in different ways: the card means the token or the name
      // did not resolve, the login means the embed bundle fell back to the
      // console shell inside somebody else's iframe.
      await expect(page.getByTestId('login-screen')).toHaveCount(0)
      await expect(page.getByTestId('embed-unavailable')).toHaveCount(0)

      // The chat is really mounted: its composer is there…
      await expect(page.locator('textarea').first()).toBeVisible({ timeout: 60_000 })
      // …and it resumed THIS session — the second firing's reply is on screen,
      // which no other session in the project could produce.
      await expect(page.getByText(RUN2_DONE, { exact: false }).first()).toBeVisible({ timeout: 60_000 })

      // T12's security criterion, guarded by nothing else in CI: the token is
      // erased from the address bar before the first paint, and never written to
      // storage where the parent page or an extension could read it.
      expect(page.url()).not.toContain('token=')
      expect(page.url()).not.toContain('#')
      const stored = await page.evaluate(() => ({
        local: JSON.stringify(Object.entries(localStorage)),
        session: JSON.stringify(Object.entries(sessionStorage)),
      }))
      expect(stored.local).not.toContain(embedToken)
      expect(stored.session).not.toContain(embedToken)
    })
  })
})
