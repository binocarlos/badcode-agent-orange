import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type ProjectClient, type SessionInfo } from '../helpers/api'
import { seedSessionMCPServers, stackDbReadable, storedSessionMCPServers } from '../helpers/stackdb'

// A4: a session-supplied MCP server is callable in-session, and survives
// snapshot → resume (§4.1, §4.4, §4.5).
//
// # What "callable" can mean in mock mode
//
// The stack's mock model is a fixed canned SSE script (go/modelproxy's
// MockHandler) — it emits the same text for every prompt and can never be made
// to invoke a tool. So no test in mock mode can make the *model* call an MCP
// tool, and one that claimed to would be lying.
//
// What is provable is everything up to that point, and it is nearly all of it.
// The in-image agent reports a `session_info` event per turn carrying the model,
// the tool names it was given, and every MCP server with a connection status.
// `status: "connected"` is not a guess: the harness completed a JSON-RPC
// handshake with that server and got its tool list back, which is why the tools
// then appear as `mcp__<server>__<tool>`. A tool the model could not call would
// not be in that list. That is the assertion these tests make.
//
// # The MCP server under test
//
// agentd serves its own core tools over HTTP MCP at `/mcp` (D3), authenticating
// by the session token the Runner injects as SESSION_TOKEN. Pointing a
// *session-supplied* server at that endpoint under a different name gives a real
// server to connect to, with no fixture process to build, and proves the
// `${VAR}` header resolution of §4.4 at the same time — an unresolved reference
// fails the spawn loudly rather than connecting anonymously.

const MCP_URL = 'http://172.17.0.1:8099/mcp' // AGENTKIT_SELF_URL from inside DinD
const PROBE_SERVER = {
  probe: { url: MCP_URL, headers: { Authorization: '${SESSION_TOKEN}' } },
}

/** The names of every MCP server the container reported, newest turn first. */
function serversIn(infos: SessionInfo[]): Array<{ name: string; status: string }> {
  return infos.length === 0 ? [] : infos[infos.length - 1].mcpServers
}

test.describe('session MCP servers', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(240_000)

  let client: ProjectClient

  test.beforeAll(async () => {
    expect(
      await stackDbReadable(),
      'these tests seed and read the stack postgres directly — see helpers/stackdb.ts',
    ).toBe(true)
  })

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-mcp')
  })

  // The control: proves the observable itself works, so the red test below is
  // red for the right reason. The in-image `ui` server is always present.
  test('the in-image MCP server connects and its tools reach the model', async () => {
    const session = await client.createSession({ job: 'mcp-control' })
    await client.sendMessage(session, 'hello')

    const infos = await client.waitForSessionInfo(session, (i) => i.length > 0)
    const servers = serversIn(infos)
    expect(servers.map((s) => s.name)).toContain('ui')
    expect(servers.find((s) => s.name === 'ui')?.status).toBe('connected')
    // Tool names are namespaced per server, which is how the model addresses them.
    expect(infos[infos.length - 1].tools.some((t) => t.startsWith('mcp__ui__'))).toBe(true)
  })

  // The A4 headline, on the only path that can deliver config today: the
  // session row. Everything downstream of the row is exercised — the wire
  // protocol (A2), `${VAR}` resolution (A3), and the resume re-supply (§4.5).
  test('a session-supplied MCP server connects, exposes its tools, and survives snapshot→resume', async () => {
    const session = await client.createSession({ job: 'mcp-session' })
    // A session has no container until its first turn, and there is nothing to
    // snapshot before that.
    await client.sendMessage(session, 'hello')

    // No API can put MCP config on a session (see the red test below), so the
    // row is seeded directly. This is the only unrealistic step in the test.
    await seedSessionMCPServers(session, PROBE_SERVER)
    expect(await storedSessionMCPServers(session)).toMatchObject({ probe: { url: MCP_URL } })

    // Snapshot the container and throw it away, then bring it back. This is the
    // path A2 fixed: a restored session with no prior turns used to skip its
    // session-create and come back with no MCP config at all.
    await client.archiveSession(session)
    const restored = await client.restoreSession(session)
    expect(restored.State).toBe('running')

    await client.sendMessage(session, 'hello after resume')
    const infos = await client.waitForSessionInfo(session, (i) =>
      i.some((info) => info.mcpServers.some((s) => s.name === 'probe')),
    )
    const latest = infos[infos.length - 1]

    // Connected means the handshake succeeded — the config survived the round
    // trip, the ${SESSION_TOKEN} header resolved, and agentd accepted the token.
    expect(latest.mcpServers).toContainEqual({ name: 'probe', status: 'connected' })
    // …and the server's tools are in the list the model was given, namespaced
    // under the name the session config chose.
    const probeTools = latest.tools.filter((t) => t.startsWith('mcp__probe__'))
    expect(probeTools.length).toBeGreaterThan(0)
    // The in-image server is still there too: session config extends, never replaces.
    expect(latest.mcpServers.map((s) => s.name)).toContain('ui')
  })

  // A bad reference must fail loudly rather than connect without the credential
  // (§4.4, and A3's "an empty variable is treated as unset"). The turn refuses
  // with an AGENT_ERROR naming the variable and telling the operator how to fix
  // it — an unusually good error, worth pinning so it stays that way.
  test('an unresolvable ${VAR} reference fails the turn loudly instead of connecting anonymously', async () => {
    const session = await client.createSession({ job: 'mcp-badvar' })
    await client.sendMessage(session, 'hello') // provision the container first
    await seedSessionMCPServers(session, {
      probe: { url: MCP_URL, headers: { Authorization: '${NO_SUCH_VARIABLE_ANYWHERE}' } },
    })
    await client.archiveSession(session)
    await client.restoreSession(session)
    await client.sendMessage(session, 'hello again').catch(() => {
      /* the turn may fail outright — that is an acceptable way to be loud */
    })

    const errors = await poll(
      () => client.errorEvents(session),
      (rows) => rows.length > 0,
      60_000,
      'an error event naming the unresolved variable',
    )
    const message = errors.map((e) => e.message ?? '').join('\n')
    expect(errors.some((e) => e.code === 'AGENT_ERROR')).toBe(true)
    expect(message).toContain('NO_SUCH_VARIABLE_ANYWHERE')
    expect(message).toContain('refusing to spawn the MCP server')
    // …and it never connected anyway.
    const probe = serversIn(await client.sessionInfoEvents(session)).find((s) => s.name === 'probe')
    expect(probe?.status ?? 'absent').not.toBe('connected')
  })
})

// ── KNOWN FAILURE ───────────────────────────────────────────────────────────
//
// Left red deliberately: the product is wrong, not the test.
//
// There is **no way to configure an MCP server for a session through any API**:
//
//   1. `POST /agent/session` has no `mcp_servers` field (go/httpapi/session.go's
//      createSessionBody), so A1's per-request config is unreachable over HTTP.
//   2. Project and worker `mcp_config` — which IS settable, via
//      `PUT /agent/project-settings` — is resolved correctly by agentd's
//      sessionContextProvider and then **dropped**: its `Resolve` returns
//      `&extension.SessionContext{SystemPrompt, BaseImage}` and never sets the
//      `MCPServers` field that A2 added to that struct and that the runner
//      merges in `sessionMCPServers`. Nothing in the repo populates it.
//
// So the tools a project configures never reach any container. B2 predicted
// exactly this ("without that wiring, project/worker MCP defaults resolve but
// never reach a container") and assigned it to A2; A2 added the field and the
// runner merge but not the one line in agentd that fills it in.
//
// This test asserts the product-level claim of §5: a server configured for the
// project is granted to every session in it.
test.describe('session MCP servers — known failures', () => {
  test.setTimeout(240_000)

  test("a project's mcp_config reaches its sessions (KNOWN FAILURE: SessionContext.MCPServers is never populated)", async ({
    request,
  }) => {
    const client = await newProjectClient(request, 'e2e-mcp-proj')
    await client.putSettings({ mcp_config: PROBE_SERVER })
    expect(await client.getSettings()).toMatchObject({ mcp_config: { probe: { url: MCP_URL } } })

    const session = await client.createSession({ job: 'mcp-project' })

    // The row is the first place the project's config should appear: the runner
    // resolves project ∪ worker defaults and persists them on create.
    expect(
      await storedSessionMCPServers(session),
      "the project's mcp_config should have been resolved onto the new session's row",
    ).toMatchObject({ probe: { url: MCP_URL } })

    // …and then reach the container.
    await client.sendMessage(session, 'hello')
    const infos = await client.waitForSessionInfo(session, (i) => i.length > 0)
    expect(serversIn(infos).map((s) => s.name)).toContain('probe')
  })
})
