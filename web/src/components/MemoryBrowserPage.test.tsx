// @vitest-environment jsdom
// MB1: the memory browser — the selector bar as chips, the honesty notes that
// come with a text query, the `name=` fold, provenance, and the two failure
// modes told apart (a bad selector vs a route that is not served here).

import React from 'react'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import MemoryBrowserPage from './MemoryBrowserPage.js'

const MS = 1_700_000_000_000

let originalFetch: typeof globalThis.fetch
let memories: Record<string, unknown>[]
let status: number
let body: string
let urls: string[]

const memory = (over: Record<string, unknown> = {}) => ({
  id: 'm1',
  labels: { kind: 'lesson' },
  snippet: 'Quote the ticket reference in every reply.',
  score: 0.03,
  created_by_worker: 'email-reviewer',
  created_by_session: 'sess-7',
  created_at: MS,
  ...over,
})

beforeEach(() => {
  memories = [memory()]
  status = 200
  body = ''
  urls = []
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const u = String(url)
    urls.push(u)
    if (u.includes('/agent/memories')) {
      if (status !== 200) return new Response(body, { status })
      return new Response(JSON.stringify({ memories }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

function renderBrowser(props: Partial<React.ComponentProps<typeof MemoryBrowserPage>> = {}) {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <MemoryBrowserPage {...props} />
    </AgentChatProvider>,
  )
}

describe('the selector bar', () => {
  it('teaches the grammar: no OR, and clauses become chips', async () => {
    renderBrowser({ selector: 'kind=lesson,worker=email-answerer' })
    expect(await screen.findByText(/There is no OR/)).toBeInTheDocument()
    const chips = screen.getByTestId('selector-chips')
    expect(within(chips).getByText('kind=lesson')).toBeInTheDocument()
    expect(within(chips).getByText('worker=email-answerer')).toBeInTheDocument()
  })

  it('names an invalid clause the way the parser does, before the round trip', async () => {
    renderBrowser()
    const field = await screen.findByTestId('memory-selector')
    await userEvent.type(field, 'kind in (a')
    await waitFor(() =>
      expect(screen.getByText(`selector "kind in (a": unbalanced '('`)).toBeInTheDocument(),
    )
  })

  it('searches the route with the selector and the text', async () => {
    renderBrowser()
    await screen.findByText(/Quote the ticket reference/)
    await userEvent.type(await screen.findByTestId('memory-selector'), 'kind=lesson')
    await userEvent.type(await screen.findByTestId('memory-query'), 'reference')
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() =>
      expect(urls.some((u) => u.includes('selector=kind%3Dlesson') && u.includes('query=reference')))
        .toBe(true),
    )
  })

  it('deleting a chip re-runs the search without that clause', async () => {
    renderBrowser({ selector: 'kind=lesson,worker=w' })
    const chips = await screen.findByTestId('selector-chips')
    const chip = within(chips).getByText('kind=lesson').closest('.MuiChip-root') as HTMLElement
    await userEvent.click(within(chip).getByTestId('CancelIcon'))
    await waitFor(() =>
      expect(urls.some((u) => u.includes('selector=worker%3Dw') && !u.includes('kind'))).toBe(true),
    )
  })
})

describe('honesty about relevance', () => {
  it('says nothing about RRF when there is no text query', async () => {
    renderBrowser()
    await screen.findByText(/Quote the ticket reference/)
    expect(screen.queryByText(/Ranked by RRF/)).not.toBeInTheDocument()
  })

  it('with a query, states that a low score means nothing good matched', async () => {
    renderBrowser({ query: 'reference' })
    expect(await screen.findByText(/no relevance threshold/)).toBeInTheDocument()
  })

  it('flags a keyword-only-looking result set', async () => {
    renderBrowser({ query: 'invoices' })
    expect(await screen.findByText(/semantic leg is off/)).toBeInTheDocument()
  })
})

describe('rows', () => {
  it('folds the name= convention: current value first, superseded beneath', async () => {
    memories = [
      memory({ id: 'a', labels: { name: 'tone' }, snippet: 'Older tone.', created_at: MS - 5000 }),
      memory({ id: 'b', labels: { name: 'tone' }, snippet: 'Current tone.', created_at: MS }),
    ]
    renderBrowser()
    expect(await screen.findByText('name=tone')).toBeInTheDocument()
    expect(screen.getByText('Current tone.')).toBeInTheDocument()
    expect(screen.getByText(/1 superseded value/)).toBeInTheDocument()
    expect(screen.getByText(/nothing was deleted/)).toBeInTheDocument()
  })

  it('carries provenance: the worker, the time, and a link to the thread', async () => {
    const onOpenSession = vi.fn()
    renderBrowser({ onOpenSession })
    expect(await screen.findByText('email-reviewer')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'open the session' }))
    expect(onOpenSession).toHaveBeenCalledWith('sess-7')
  })

  it('renders labels as chips on an unnamed row', async () => {
    renderBrowser()
    const row = await screen.findByTestId('memory-row')
    expect(within(row).getByText('kind=lesson')).toBeInTheDocument()
  })
})

describe('failures are told apart', () => {
  it('a 400 is the operator’s selector, shown with the parser’s own words', async () => {
    status = 400
    body = 'selector term "no spaces": label key "no spaces" is invalid'
    renderBrowser({ selector: 'x=1' })
    expect(await screen.findByTestId('memory-selector-error')).toHaveTextContent(
      /label key "no spaces" is invalid/,
    )
  })

  it('a 501 says memory is not available here, not that something failed', async () => {
    status = 501
    body = 'the memory store is not configured on this host'
    renderBrowser()
    expect(await screen.findByText(/Memory is not available on this host/)).toBeInTheDocument()
  })

  it('an empty project says memories are written by workers, not here', async () => {
    memories = []
    renderBrowser()
    expect(await screen.findByText(/Nothing has been remembered in this project yet/)).toBeInTheDocument()
  })

  // RD27/RD28's class: the route IS served (500, not 501), so the empty list is
  // the failure's residue, not an answer about the project.
  it('a failed search never claims the project has remembered nothing', async () => {
    status = 500
    body = 'memory search: database is down'
    renderBrowser()
    expect(await screen.findByText(/database is down/)).toBeInTheDocument()
    expect(screen.queryByText(/Nothing has been remembered in this project yet/)).toBeNull()
    expect(screen.queryByText(/No memory matches/)).toBeNull()
  })
})
