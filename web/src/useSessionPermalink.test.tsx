// @vitest-environment jsdom
// F3: the canonical session permalink route, bound to the chat context.
// Proves both directions — a permalink URL resumes (replays) that session, and
// an active session stamps its canonical URL into the address bar.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { AgentChatProvider } from './AgentChatProvider.js'
import useSessionPermalink, { projectIdFromLocation } from './useSessionPermalink.js'

const fetchedUrls: string[] = []
let originalFetch: typeof globalThis.fetch

function stubApi() {
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const urlStr = String(url)
    fetchedUrls.push(urlStr)
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })

    if (urlStr.includes('/query-events')) return json({ events: [] })
    if (urlStr.includes('/messages')) return json({ messages: [], total: 0 })
    if (urlStr.includes('/status')) return json({ sandboxState: 'running', activeQuery: null })
    // getSession
    const m = /\/agent\/session\/([^/?]+)/.exec(urlStr)
    if (m) return json({ id: m[1], status: 'active', workflowId: 'agent', customer: 'c', job: 'j' })
    return json({})
  }) as typeof globalThis.fetch
}

function wrapper({ children }: { children: ReactNode }) {
  return (
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      {children}
    </AgentChatProvider>
  )
}

beforeEach(() => {
  fetchedUrls.length = 0
  originalFetch = globalThis.fetch
  stubApi()
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
  window.history.replaceState(null, '', '/')
})

describe('URL → session', () => {
  it('resumes the session named by the permalink', async () => {
    window.history.replaceState(null, '', '/p/acme/s/sess-42')

    const { result } = renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })

    await waitFor(() => {
      expect(fetchedUrls.some((u) => u.includes('/agent/session/sess-42'))).toBe(true)
    })
    expect(result.current.routeSessionId).toBe('sess-42')
    expect(result.current.foreignProject).toBe(false)
  })

  it('ignores a permalink for a different project', async () => {
    window.history.replaceState(null, '', '/p/other/s/sess-42')

    const { result } = renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })

    await waitFor(() => expect(result.current.foreignProject).toBe(true))
    expect(result.current.routeSessionId).toBeNull()
    expect(fetchedUrls.some((u) => u.includes('/agent/session/sess-42'))).toBe(false)
  })

  it('does nothing on a non-session URL', async () => {
    window.history.replaceState(null, '', '/settings')

    const { result } = renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })

    expect(result.current.routeSessionId).toBeNull()
    expect(result.current.foreignProject).toBe(false)
    expect(fetchedUrls.some((u) => u.includes('/agent/session/'))).toBe(false)
  })

  it('resumes again on browser back/forward (popstate)', async () => {
    window.history.replaceState(null, '', '/p/acme/s/sess-a')
    renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })
    await waitFor(() => {
      expect(fetchedUrls.some((u) => u.includes('/agent/session/sess-a'))).toBe(true)
    })

    await act(async () => {
      window.history.pushState(null, '', '/p/acme/s/sess-b')
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    await waitFor(() => {
      expect(fetchedUrls.some((u) => u.includes('/agent/session/sess-b'))).toBe(true)
    })
  })

  it('decodes percent-escaped ids before resuming', async () => {
    window.history.replaceState(null, '', '/p/acme/s/sess%20one')

    const { result } = renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })

    await waitFor(() => expect(result.current.routeSessionId).toBe('sess one'))
  })
})

describe('session → URL', () => {
  it('openSession pushes the canonical path and resumes', async () => {
    const { result } = renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })

    await act(async () => {
      result.current.openSession('sess-99')
    })

    expect(window.location.pathname).toBe('/p/acme/s/sess-99')
    await waitFor(() => {
      expect(fetchedUrls.some((u) => u.includes('/agent/session/sess-99'))).toBe(true)
    })
    expect(result.current.routeSessionId).toBe('sess-99')
  })

  it('openSession preserves query and hash', async () => {
    window.history.replaceState(null, '', '/?debug=1#top')

    const { result } = renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })
    await act(async () => {
      result.current.openSession('sess-7')
    })

    expect(window.location.pathname).toBe('/p/acme/s/sess-7')
    expect(window.location.search).toBe('?debug=1')
    expect(window.location.hash).toBe('#top')
  })

  it('a session resumed from the URL is not pushed again', async () => {
    window.history.replaceState(null, '', '/p/acme/s/sess-42')
    const before = window.history.length

    renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })
    await waitFor(() => {
      expect(fetchedUrls.some((u) => u.includes('/agent/session/sess-42'))).toBe(true)
    })

    expect(window.location.pathname).toBe('/p/acme/s/sess-42')
    expect(window.history.length).toBe(before)
  })
})

describe('permalink minting', () => {
  it('permalinkFor is relative without a base URL', () => {
    const { result } = renderHook(() => useSessionPermalink({ projectId: 'acme' }), { wrapper })
    expect(result.current.permalinkFor('s1')).toBe('/p/acme/s/s1')
    expect(result.current.permalink).toBeNull()
  })

  it('permalinkFor is absolute with a base URL', () => {
    const { result } = renderHook(
      () => useSessionPermalink({ projectId: 'acme', baseUrl: 'https://o.example.com/' }),
      { wrapper },
    )
    expect(result.current.permalinkFor('s1')).toBe('https://o.example.com/p/acme/s/s1')
  })

  it('permalink names the active session once resumed', async () => {
    window.history.replaceState(null, '', '/p/acme/s/sess-42')
    const { result } = renderHook(
      () => useSessionPermalink({ projectId: 'acme', baseUrl: 'https://o.example.com' }),
      { wrapper },
    )
    await waitFor(() => {
      expect(result.current.permalink).toBe('https://o.example.com/p/acme/s/sess-42')
    })
  })
})

describe('disabled / non-browser', () => {
  it('enabled:false neither reads nor writes the URL', async () => {
    window.history.replaceState(null, '', '/p/acme/s/sess-42')

    const { result } = renderHook(
      () => useSessionPermalink({ projectId: 'acme', enabled: false }),
      { wrapper },
    )

    expect(result.current.routeSessionId).toBeNull()
    expect(fetchedUrls.some((u) => u.includes('/agent/session/sess-42'))).toBe(false)

    // permalinkFor still works — hosts with their own router use it for sharing.
    expect(result.current.permalinkFor('s1')).toBe('/p/acme/s/s1')
  })
})

describe('projectIdFromLocation', () => {
  const cases: Array<{ path: string; want: string | null }> = [
    { path: '/p/acme/s/s1', want: 'acme' },
    { path: '/p/acme', want: 'acme' },
    { path: '/p/a%2Fb/s/s1', want: 'a/b' },
    { path: '/settings', want: null },
    { path: '/', want: null },
  ]
  for (const c of cases) {
    it(`${c.path} → ${String(c.want)}`, () => {
      expect(projectIdFromLocation(c.path)).toBe(c.want)
    })
  }

  it('reads window.location when no argument is given', () => {
    window.history.replaceState(null, '', '/p/acme/s/s1')
    expect(projectIdFromLocation()).toBe('acme')
  })
})
