import { test, expect } from '@playwright/test'
import { newProjectClient, type ProjectClient } from '../helpers/api'
import { MCPClient, sessionMCP } from '../helpers/mcp'

// The memory size ceiling and the two windowing arguments, against the running
// stack (design/2026-08-10-memory-query-and-injection-hardening.md, T15).
//
// The Go suite proves all of this against a live Postgres already. What only a
// stack run can prove is that the arguments survive the wire: MCP schema →
// JSON decode → store, and the full-content HTTP read back out. A 30KB body is
// exactly the case where a shape that unit-tests fine can still be truncated by
// something in between.
//
// Like images-and-skills.stack.spec.ts, these drive agentd's core MCP server
// with a minted session token rather than through the model — in mock mode the
// proxy serves a fixed script and can never emit a tool call.

/** Over MaxEmbeddedMemoryBytes (24576), comfortably. */
const BIG_BYTES = 30_000
const HEAD = 'HEAD-MARKER-e08a'
const TAIL = 'TAIL-MARKER-e08a'

/** A body of exactly BIG_BYTES with a distinct first and last line. */
function bigDocument(): string {
  const filler = 'x'.repeat(BIG_BYTES - HEAD.length - TAIL.length - 2)
  return `${HEAD}\n${filler}\n${TAIL}`
}

test.describe('memory: size ceiling and windowed queries', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient
  let mcp: MCPClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-memq')
    // No message is sent: the memory tools run on the host and never touch the
    // container, so this session costs no host port.
    const session = await client.createSession({ job: 'memq' })
    mcp = sessionMCP(client.project, session)
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('a 30KB document stores whole with embed:false and reads back byte-for-byte', async () => {
    const content = bigDocument()
    const created = await mcp.callOK('memory_create', {
      content,
      labels: { kind: 'doc', name: 'big' },
      embed: false,
    })
    // `embedded` reports what the STORE did, not what the embedder was asked
    // for, so this is the honest witness that no vector was written.
    expect(created.embedded).toBe(false)

    // Through the tool the model uses…
    const viaTool = await mcp.callOK('memory_get', { id: created.id })
    expect(viaTool.content.length).toBe(content.length)
    expect(viaTool.content).toBe(content)

    // …and through the HTTP route an embedding application uses. Both matter:
    // memory_search would have handed back a 500-byte snippet.
    const resp = await client.raw('GET', `/agent/memories/${created.id}`)
    expect(resp.ok()).toBe(true)
    const viaHTTP = await resp.json()
    expect(viaHTTP.content.length).toBe(content.length)
    expect(viaHTTP.content.startsWith(HEAD)).toBe(true)
    expect(viaHTTP.content.endsWith(TAIL)).toBe(true)
  })

  test('the same document without embed:false is refused, and the refusal names the flag', async () => {
    const out = await mcp.call('memory_create', { content: bigDocument(), labels: { kind: 'doc' } })
    expect(out.isError).toBe(true)
    const text = JSON.stringify(out)
    expect(text).toContain('embed:false')
    // The point of refusing before the provider is called: the instruction that
    // fixes it arrives instead of a wrapped provider error.
    expect(text).toContain(String(BIG_BYTES))
  })

  test('since and until cut the window at a bound the caller was given', async () => {
    const kind = 'window'
    const older = await mcp.callOK('memory_create', {
      content: 'OLDER-ROW written first',
      labels: { kind, name: 'older' },
    })
    // A full second, so the two rows cannot share a millisecond and make the
    // bound ambiguous — created_at is unix MILLISECONDS.
    await new Promise((r) => setTimeout(r, 1100))
    const newer = await mcp.callOK('memory_create', {
      content: 'NEWER-ROW written second',
      labels: { kind, name: 'newer' },
    })
    expect(newer.created_at).toBeGreaterThan(older.created_at)

    // `since` is inclusive of its bound, so the newer row's own timestamp keeps
    // it and drops the older one.
    const since = await mcp.callOK('memory_search', {
      label_selector: `kind=${kind}`,
      since: newer.created_at,
    })
    expect(since.results.map((m: { id: string }) => m.id)).toEqual([newer.id])

    const until = await mcp.callOK('memory_search', {
      label_selector: `kind=${kind}`,
      until: older.created_at,
    })
    expect(until.results.map((m: { id: string }) => m.id)).toEqual([older.id])

    // The relative form goes over the same wire as the numeric one.
    const recent = await mcp.callOK('memory_search', { label_selector: `kind=${kind}`, since: '1h' })
    expect(recent.results.map((m: { id: string }) => m.id).sort()).toEqual([newer.id, older.id].sort())
  })

  test('latest_per returns the newest row per label value, and omits rows lacking the key', async () => {
    const kind = 'perkey'
    await mcp.callOK('memory_create', { content: 'alpha v1', labels: { kind, topic: 'alpha' } })
    await mcp.callOK('memory_create', { content: 'beta only', labels: { kind, topic: 'beta' } })
    const alphaV2 = await mcp.callOK('memory_create', {
      content: 'alpha v2 SUPERSEDES',
      labels: { kind, topic: 'alpha' },
    })
    // No `topic` at all. NULL does not group with NULL here — this row must be
    // dropped, not collapsed into a phantom bucket.
    await mcp.callOK('memory_create', { content: 'no topic label', labels: { kind } })

    const out = await mcp.callOK('memory_search', {
      label_selector: `kind=${kind}`,
      latest_per: 'topic',
    })
    const topics = out.results.map((m: { labels: Record<string, string> }) => m.labels.topic)
    expect(topics.sort()).toEqual(['alpha', 'beta'])

    const alpha = out.results.find((m: { labels: Record<string, string> }) => m.labels.topic === 'alpha')
    expect(alpha.id).toBe(alphaV2.id)
    expect(alpha.snippet).toContain('SUPERSEDES')
  })
})
