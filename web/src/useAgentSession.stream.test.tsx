// @vitest-environment jsdom
//
// B1 / doc 22 RD26 — a dropped SSE stream must not report the turn as finished.
//
// `checkSessionStatus` used to return `null` for BOTH "the probe failed" and
// "there is no active query", so a stream that stopped short of
// `query_complete` was treated as a clean finish: spinner cleared, no error,
// stuck-detection switched off — while the agent kept producing output in its
// container. These tests pin the three-way distinction.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import useAgentSession from './useAgentSession.js'

const CONNECTION_LOST = 'Connection lost — loaded conversation from history'

/** An SSE body that emits one assistant chunk and then ends — no query_complete. */
function truncatedStream(): ReadableStream<Uint8Array> {
  const enc = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      controller.enqueue(enc.encode(
        'data: ' + JSON.stringify({ type: 'assistant_text', content: 'half an ans' }) + '\n\n',
      ))
      controller.close()
    },
  })
}

type StatusBehaviour = 'reject' | 'http-500' | 'idle'

/**
 * Installs a fetch that streams a truncated turn from sendMessage and answers
 * the status probe according to `behaviour`. Every other route answers empty so
 * the persisted-history fallback is a no-op.
 */
function installFetch(behaviour: StatusBehaviour) {
  const calls: string[] = []
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const u = String(url)
    calls.push(u)

    if (u.includes('/status')) {
      if (behaviour === 'reject') throw new TypeError('Failed to fetch')
      if (behaviour === 'http-500') return new Response('boom', { status: 500 })
      return new Response(
        JSON.stringify({ sandboxState: 'running', activeQuery: null }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    }
    if (u.includes('/query-events')) {
      return new Response(JSON.stringify({ events: [] }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    }
    // `/messages` must be matched BEFORE `/message` — the send endpoint is a
    // prefix of the history one.
    if (u.includes('/messages')) {
      return new Response(JSON.stringify({ messages: [], total: 0 }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    }
    if (u.endsWith('/message')) {
      return new Response(truncatedStream(), {
        status: 200,
        headers: { 'Content-Type': 'text/event-stream' },
      })
    }
    if (u.endsWith('/agent/session')) {
      return new Response(
        JSON.stringify({ id: 'sess-b1', status: 'active', workflowId: 'agent' }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    }
    return new Response(JSON.stringify({}), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof globalThis.fetch
  return calls
}

describe('a stream that ends without query_complete', () => {
  let originalFetch: typeof globalThis.fetch

  beforeEach(() => {
    originalFetch = globalThis.fetch
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    vi.spyOn(console, 'log').mockImplementation(() => {})
    // Installed before the hook renders so the stuck-detection interval, which
    // is created during sendMessage, is a fake one we can advance.
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    globalThis.fetch = originalFetch
    vi.restoreAllMocks()
  })

  /** Drives create + sendMessage and returns the hook result. */
  async function runTurn() {
    const { result } = renderHook(() => useAgentSession({ apiBaseUrl: '' }))
    await act(async () => {
      await result.current.createSession({ customer: 'c', job: 'j', workflow_id: 'agent' })
    })
    await act(async () => {
      await result.current.sendMessage('hello')
    })
    return result
  }

  async function cleanup(result: { current: { clearSession: () => void } }) {
    await act(async () => { result.current.clearSession() })
  }

  it('surfaces connection-lost when the status probe rejects', async () => {
    installFetch('reject')
    const result = await runTurn()

    expect(result.current.error).toBe(CONNECTION_LOST)
    expect(result.current.isStreaming).toBe(false)
    await cleanup(result)
  })

  it('surfaces connection-lost when the status probe answers 500', async () => {
    installFetch('http-500')
    const result = await runTurn()

    expect(result.current.error).toBe(CONNECTION_LOST)
    await cleanup(result)
  })

  it('keeps stuck-detection armed when the end could not be confirmed', async () => {
    installFetch('reject')
    const result = await runTurn()

    // The detector is a 5s interval; 50s without a heartbeat is 'likely_stuck'.
    // If it had been switched off (the RD26 bug) this stays 'ok' forever.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000)
    })
    expect(result.current.stuckStatus).toBe('likely_stuck')
    await cleanup(result)
  })

  it('settles quietly when the server confirms there is no active query', async () => {
    installFetch('idle')
    const result = await runTurn()

    expect(result.current.error).toBeNull()
    expect(result.current.isStreaming).toBe(false)

    // …and the detector is disarmed: nothing to be stuck about.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000)
    })
    expect(result.current.stuckStatus).toBe('ok')
    await cleanup(result)
  })

  it('says so on resume when the status probe cannot be reached', async () => {
    installFetch('reject')
    const { result } = renderHook(() => useAgentSession({ apiBaseUrl: '' }))
    await act(async () => {
      await result.current.resumeSession('sess-b1')
    })

    // Previously this branch was `status?.activeQuery` — an unreachable probe
    // looked exactly like "nothing is running", and resume said nothing at all.
    expect(result.current.error).toBe(
      'Could not check whether this session is still running — showing saved history',
    )
    await cleanup(result)
  })
})
