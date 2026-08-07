// @vitest-environment jsdom
// MB1: the briefing preview — the sections core would inject right now, at the
// project's real byte cap, with the truncation marker where it would fall.

import React from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import BriefingPreview from './BriefingPreview.js'

const MS = 1_700_000_000_000

let originalFetch: typeof globalThis.fetch
let bySelector: Record<string, Record<string, unknown>[]>
let briefingMaxBytes: number
let selectorsAsked: string[]

const memory = (snippet: string) => ({
  id: `m-${snippet.length}`,
  labels: { kind: 'rolling-summary' },
  snippet,
  score: 0,
  created_by_worker: 'archivist',
  created_by_session: 'sess-3',
  created_at: MS,
})

beforeEach(() => {
  briefingMaxBytes = 32
  selectorsAsked = []
  bySelector = {
    'kind=rolling-summary,worker=answerer': [memory('40 emails answered this week; three needed a human.')],
    'kind=lesson': [],
  }
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const u = String(url)
    const json = (v: unknown) =>
      new Response(JSON.stringify(v), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    if (u.includes('/agent/memories')) {
      const selector = new URL(u, 'http://x').searchParams.get('selector') ?? ''
      selectorsAsked.push(selector)
      return json({ memories: bySelector[selector] ?? [] })
    }
    if (u.includes('/agent/project-settings')) {
      return json({ project: 'acme', briefing_max_bytes: briefingMaxBytes })
    }
    return json({})
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

function renderPreview(briefing: string[] | null) {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <BriefingPreview worker={{ name: 'answerer', briefing }} />
    </AgentChatProvider>,
  )
}

it('asks for the built-in selector first, then the worker’s own', async () => {
  renderPreview(['kind=lesson'])
  await waitFor(() =>
    expect(selectorsAsked).toEqual(
      expect.arrayContaining(['kind=rolling-summary,worker=answerer', 'kind=lesson']),
    ),
  )
  expect(await screen.findByText(/\(built in\)/)).toBeInTheDocument()
})

it('renders the section at the cap, with the marker where it falls', async () => {
  renderPreview(null)
  await waitFor(() =>
    expect(screen.getByText(/Your memory briefing/).textContent).toContain(
      '[… briefing section truncated at 32 bytes]',
    ),
  )
  expect(screen.getByText(/Your memory briefing/).textContent).toContain('40 emails answered')
})

it('a selector that matches nothing says the section is simply not injected', async () => {
  renderPreview(['kind=lesson'])
  expect(
    await screen.findByText(/Nothing matches — this section is not injected at all/),
  ).toBeInTheDocument()
})

it('a zero cap is "unset", so the project default applies and nothing is cut', async () => {
  briefingMaxBytes = 0
  renderPreview(null)
  await screen.findByText(/2048 bytes/)
  expect(screen.getByText(/Your memory briefing/).textContent).not.toContain('truncated at')
})
