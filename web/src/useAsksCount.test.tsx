// @vitest-environment jsdom
// W2 / doc 21 X7: the chrome's badge counts asks — parked deliveries joined to
// open attention requests — and asks only the parked page for them.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import useAsksCount from './useAsksCount.js'

const fetchedUrls: string[] = []
let originalFetch: typeof globalThis.fetch

const NOW = 1_789_000_000

const parked = (id: string, session: string) => ({
  id,
  project: 'acme',
  event_id: 'e1',
  subscription_id: 's1',
  session_id: session,
  status: 'awaiting_human',
  started_at: NOW - 600,
  ended_at: 0,
  created_at: NOW - 600,
  updated_at: NOW - 600,
})

const openRequest = (id: string, session: string, over: Record<string, unknown> = {}) => ({
  id,
  project: 'acme',
  session_id: session,
  worker: 'email-answerer',
  message: 'Does this reply look right?',
  session_url: '',
  channel: 'webhook',
  delivered: true,
  expires_at: 0,
  created_at: NOW - 600,
  answered_at: 0,
  timed_out_at: 0,
  ...over,
})

/** Serve each route from `bodies`, or 404 to model an unmounted route. */
function stubRoutes(bodies: Record<string, unknown | null>) {
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const href = String(url)
    fetchedUrls.push(href)
    const path = href.split('?')[0]!
    const body = bodies[path]
    if (body === undefined || body === null) {
      return new Response('not found', { status: 404 })
    }
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof globalThis.fetch
}

beforeEach(() => {
  fetchedUrls.length = 0
  originalFetch = globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
})

describe('useAsksCount', () => {
  it('counts the join, not the open requests', async () => {
    stubRoutes({
      '/agent/events': { events: [] },
      '/agent/deliveries': { deliveries: [parked('d1', 'sess-1')] },
      '/agent/subscriptions': { subscriptions: [] },
      '/agent/attention-requests': {
        attention_requests: [openRequest('a1', 'sess-1'), openRequest('a2', 'sess-2')],
      },
    })

    const { result } = renderHook(() => useAsksCount())
    await waitFor(() => expect(result.current.loading).toBe(false))
    // Two open requests; one of them has a parked delivery. The badge says one.
    expect(result.current.count).toBe(1)
    expect(result.current.asksHaveMessages).toBe(true)
  })

  it('asks the deliveries route for the parked page only', async () => {
    stubRoutes({
      '/agent/events': { events: [] },
      '/agent/deliveries': { deliveries: [] },
      '/agent/subscriptions': { subscriptions: [] },
      '/agent/attention-requests': { attention_requests: [] },
    })

    const { result } = renderHook(() => useAsksCount())
    await waitFor(() => expect(result.current.loading).toBe(false))

    const deliveries = fetchedUrls.find((u) => u.startsWith('/agent/deliveries'))!
    expect(new URLSearchParams(deliveries.split('?')[1]).get('status')).toBe('awaiting_human')
  })

  it('falls back to parked deliveries where the attention route is not mounted', async () => {
    stubRoutes({
      '/agent/events': { events: [] },
      '/agent/deliveries': { deliveries: [parked('d1', 'sess-1'), parked('d2', 'sess-2')] },
      '/agent/subscriptions': { subscriptions: [] },
      '/agent/attention-requests': null, // 404 — a host that never mounted it
    })

    const { result } = renderHook(() => useAsksCount())
    await waitFor(() => expect(result.current.asksHaveMessages).toBe(false))
    await waitFor(() => expect(result.current.count).toBe(2))
  })

  it('does not count a delivery whose request has been answered', async () => {
    stubRoutes({
      '/agent/events': { events: [] },
      '/agent/deliveries': { deliveries: [parked('d1', 'sess-1')] },
      '/agent/subscriptions': { subscriptions: [] },
      '/agent/attention-requests': {
        attention_requests: [openRequest('a1', 'sess-1', { answered_at: NOW })],
      },
    })

    const { result } = renderHook(() => useAsksCount())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.count).toBe(0)
  })
})
