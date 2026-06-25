// @vitest-environment jsdom
// Task 5.1: Custom endpoint override tests.
// Verifies DEFAULT_ENDPOINTS URL patterns AND that overriding an endpoint
// causes the hook to actually fetch the custom URL.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import useAgentSession from './useAgentSession.js'
import { DEFAULT_ENDPOINTS } from './plugins.js'

describe('DEFAULT_ENDPOINTS', () => {
  it('status uses the expected path', () => {
    expect(DEFAULT_ENDPOINTS.status('abc')).toBe('/agent/session/abc/status')
  })

  it('cancel uses the expected path', () => {
    expect(DEFAULT_ENDPOINTS.cancel('abc')).toBe('/agent/session/abc/cancel')
  })

  it('getSession uses the expected path', () => {
    expect(DEFAULT_ENDPOINTS.getSession('abc')).toBe('/agent/session/abc')
  })

  it('deleteSession uses the expected path', () => {
    expect(DEFAULT_ENDPOINTS.deleteSession('abc')).toBe('/agent/session/abc')
  })

  it('restore uses the expected path', () => {
    expect(DEFAULT_ENDPOINTS.restore('abc')).toBe('/agent/session/abc/restore')
  })

  it('messages uses the expected path', () => {
    expect(DEFAULT_ENDPOINTS.messages('abc')).toBe('/agent/session/abc/messages')
  })

  it('queryEvents uses the expected path', () => {
    expect(DEFAULT_ENDPOINTS.queryEvents('abc')).toBe('/agent/session/abc/query-events')
  })

  it('listSessions is the expected static path', () => {
    expect(DEFAULT_ENDPOINTS.listSessions).toBe('/agent/sessions')
  })

  it('searchMessages is the expected static path', () => {
    expect(DEFAULT_ENDPOINTS.searchMessages).toBe('/agent/messages/search')
  })
})

describe('endpoint overrides reach fetch', () => {
  const fetchedUrls: string[] = []
  let originalFetch: typeof globalThis.fetch

  beforeEach(() => {
    fetchedUrls.length = 0
    originalFetch = globalThis.fetch
    globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
      const urlStr = String(url)
      fetchedUrls.push(urlStr)
      // createSession response
      if (urlStr.endsWith('/agent/session')) {
        return new Response(
          JSON.stringify({ id: 'sess-1', status: 'active', workflowId: 'agent' }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      // status poll response — a STABLE state so the poll stops itself.
      return new Response(
        JSON.stringify({ sandboxState: 'running', activeQuery: null }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    }) as typeof globalThis.fetch
  })

  afterEach(() => {
    globalThis.fetch = originalFetch
    vi.restoreAllMocks()
  })

  it('uses the custom status endpoint override when polling', async () => {
    const { result } = renderHook(() =>
      useAgentSession({
        apiBaseUrl: '',
        endpoints: { ...DEFAULT_ENDPOINTS, status: (id) => `/custom/${id}/st` },
      }),
    )

    await act(async () => {
      await result.current.createSession({ customer: 'c', job: 'j', workflow_id: 'agent' })
    })

    // The container-state poll fires immediately after createSession and hits status().
    await waitFor(() => {
      expect(fetchedUrls.some((u) => u.includes('/custom/sess-1/st'))).toBe(true)
    })

    // Stop the module-level interval so it doesn't leak into other tests.
    await act(async () => {
      result.current.clearSession()
    })
  })

  it('uses the custom listSessions endpoint override', async () => {
    const { result } = renderHook(() =>
      useAgentSession({
        apiBaseUrl: '',
        endpoints: { ...DEFAULT_ENDPOINTS, listSessions: '/my-custom/sessions' },
      }),
    )

    await act(async () => {
      await result.current.listSessions()
    })

    expect(fetchedUrls.some((u) => u.includes('/my-custom/sessions'))).toBe(true)
  })

  it('stores snapshot_progress from the status poll onto the session row', async () => {
    const progressPayload = {
      op: 'snapshot',
      phase: 'uploading',
      bytesDone: 10,
      bytesTotal: 100,
      startedAt: new Date().toISOString(),
    }

    globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
      const urlStr = String(url)
      fetchedUrls.push(urlStr)
      // createSession response
      if (urlStr.endsWith('/agent/session')) {
        return new Response(
          JSON.stringify({ id: 'sess-snap', status: 'active', workflowId: 'agent' }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      // listSessions response — pre-seeds the session row so the poll can update it
      if (urlStr.includes('/agent/sessions')) {
        return new Response(
          JSON.stringify({ sessions: [{ id: 'sess-snap', title: 'Test', status: 'active', customer: 'c', job: 'j', workflow_id: 'agent', user_email: '', persona: '', current_node: '', artifact_count: 0 }] }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      // status poll response — includes progress
      return new Response(
        JSON.stringify({ sandboxState: 'running', activeQuery: null, progress: progressPayload }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    }) as typeof globalThis.fetch

    const { result } = renderHook(() =>
      useAgentSession({ apiBaseUrl: '' }),
    )

    // Seed sessionList via listSessions so the poll can find the row
    await act(async () => {
      await result.current.listSessions()
    })

    // Now start the poll by triggering createSession
    await act(async () => {
      await result.current.createSession({ customer: 'c', job: 'j', workflow_id: 'agent' })
    })

    await waitFor(() => {
      const row = result.current.sessionList.find(s => s.id === 'sess-snap')
      expect(row?.snapshot_progress?.phase).toBe('uploading')
    })

    await act(async () => {
      result.current.clearSession()
    })
  })
})
