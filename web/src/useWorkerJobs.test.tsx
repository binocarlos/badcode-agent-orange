// @vitest-environment jsdom
// E5: per-worker job history is filtered by the server, not by the browser.
// The assertion that matters is the request itself — `?worker=` must be on the
// wire, because without it a busy project hides a quiet worker's older jobs
// behind the page limit.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useWorkerJobs } from './useWorkers.js'
import type { AgentSessionListItem } from './types.js'

const fetchedUrls: string[] = []
let originalFetch: typeof globalThis.fetch

function stubSessions(rows: AgentSessionListItem[]) {
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    fetchedUrls.push(String(url))
    return new Response(JSON.stringify(rows), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof globalThis.fetch
}

function session(id: string, worker: string, createdAt: number): AgentSessionListItem {
  return { id, worker, created_at: createdAt } as AgentSessionListItem
}

beforeEach(() => {
  fetchedUrls.length = 0
  originalFetch = globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
})

describe('useWorkerJobs', () => {
  it('asks the server for one worker and reports itself server-filtered', async () => {
    stubSessions([session('s2', 'triager', 200), session('s1', 'triager', 100)])
    const { result } = renderHook(() => useWorkerJobs('triager'))

    await waitFor(() => expect(result.current.jobs).toHaveLength(2))
    expect(result.current.serverFiltered).toBe(true)
    expect(result.current.truncated).toBe(false)
    // Newest first, whatever order the server sent.
    expect(result.current.jobs.map((j) => j.id)).toEqual(['s2', 's1'])

    expect(fetchedUrls).toHaveLength(1)
    const params = new URLSearchParams(fetchedUrls[0].split('?')[1])
    expect(fetchedUrls[0].split('?')[0]).toBe('/agent/sessions')
    expect(params.get('worker')).toBe('triager')
    expect(params.get('user_email')).toBe('*')
    expect(params.get('limit')).toBe('200')
  })

  it('does not fetch at all without a worker name', async () => {
    stubSessions([])
    const { result } = renderHook(() => useWorkerJobs(''))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(fetchedUrls).toEqual([])
    expect(result.current.jobs).toEqual([])
  })

  it('flags truncation when the worker fills the page', async () => {
    stubSessions([session('s1', 'triager', 100), session('s2', 'triager', 200)])
    const { result } = renderHook(() => useWorkerJobs('triager', { limit: 2 }))

    await waitFor(() => expect(result.current.truncated).toBe(true))
    expect(new URLSearchParams(fetchedUrls[0].split('?')[1]).get('limit')).toBe('2')
  })

  it('still drops a row for another worker, so an unfiltering host cannot lie', async () => {
    stubSessions([session('s1', 'triager', 100), session('s2', 'summariser', 200)])
    const { result } = renderHook(() => useWorkerJobs('triager'))

    await waitFor(() => expect(result.current.jobs).toHaveLength(1))
    expect(result.current.jobs[0].id).toBe('s1')
  })
})
