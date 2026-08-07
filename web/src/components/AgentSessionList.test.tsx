// @vitest-environment jsdom
import React from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { test, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import AgentSessionList from './AgentSessionList.js'

let originalFetch: typeof globalThis.fetch
const fetchedUrls: string[] = []
// Every DELETE the component issued, in order. Kept separate from fetchedUrls
// because the assertion that matters for RD5 is "no DELETE was sent", and
// /agent/session/s1 is also fetched by select() and by the metadata reads.
const deleted: string[] = []
// When set, the next DELETE answers with this status and body.
let deleteFailure: { status: number; body: string } | null = null

beforeEach(() => {
  fetchedUrls.length = 0
  deleted.length = 0
  deleteFailure = null
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const urlStr = String(url)
    fetchedUrls.push(urlStr)
    if (init?.method === 'DELETE') {
      deleted.push(urlStr)
      if (deleteFailure !== null) {
        return new Response(deleteFailure.body, { status: deleteFailure.status })
      }
      return new Response(null, { status: 204 })
    }
    // listSessions (refresh on mount)
    if (urlStr.includes('/agent/sessions')) {
      return new Response(
        JSON.stringify([{ id: 's1', title: 'First chat', artifact_count: 2 }]),
        { status: 200 },
      )
    }
    // queryEvents — empty so resumeSession falls back to persisted messages
    if (urlStr.includes('/query-events')) {
      return new Response(JSON.stringify({ events: [] }), { status: 200 })
    }
    // messages
    if (urlStr.includes('/messages')) {
      return new Response(JSON.stringify({ messages: [], total: 0 }), { status: 200 })
    }
    // getSession (the resumeSession metadata fetch — observable proof of select)
    return new Response(
      JSON.stringify({ id: 's1', status: 'active', workflowId: 'agent' }),
      { status: 200 },
    )
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

function renderList() {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentSessionList />
    </AgentChatProvider>
  )
}

test('renders sessions from the provider', async () => {
  renderList()
  expect(await screen.findByText('First chat')).toBeInTheDocument()
})

test('selects (resumes) the session on click', async () => {
  renderList()
  const item = await screen.findByText('First chat')

  // Clear fetches captured during mount so the assertion is unambiguous.
  fetchedUrls.length = 0
  await userEvent.click(item)

  // select(id) -> resumeSession(id) fetches the session metadata at getSession('s1').
  expect(fetchedUrls.some((u) => u.includes('/agent/session/s1'))).toBe(true)
})

// RD5: the delete button used to be wired straight to the DELETE. One click
// from a list destroyed the conversation. The click must now only ASK.
test('the delete button asks first and deletes nothing on its own', async () => {
  renderList()
  await screen.findByText('First chat')

  await userEvent.click(screen.getByRole('button', { name: /delete first chat/i }))

  expect(deleted).toEqual([])
  expect(screen.getByText('First chat')).toBeInTheDocument()
  // The question names the session and what is lost — not "are you sure?".
  expect(screen.getByRole('dialog')).toBeInTheDocument()
  expect(screen.getByText(/Delete “First chat”\?/)).toBeInTheDocument()
  expect(screen.getByText(/whole conversation/)).toBeInTheDocument()
  expect(screen.getByText(/its 2 files/)).toBeInTheDocument()
  expect(screen.getByText(/cannot undo this/i)).toBeInTheDocument()
})

test('cancelling the confirmation deletes nothing and keeps the session', async () => {
  renderList()
  await screen.findByText('First chat')

  await userEvent.click(screen.getByRole('button', { name: /delete first chat/i }))
  await userEvent.click(screen.getByRole('button', { name: /keep it/i }))

  expect(deleted).toEqual([])
  expect(screen.getByText('First chat')).toBeInTheDocument()
  // MUI unmounts the dialog after its close transition, so this is a waitFor.
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
})

test('confirming issues the DELETE and removes the session', async () => {
  renderList()
  await screen.findByText('First chat')

  await userEvent.click(screen.getByRole('button', { name: /delete first chat/i }))
  await userEvent.click(screen.getByRole('button', { name: /delete the conversation/i }))

  expect(deleted).toEqual(['/agent/session/s1'])
  expect(screen.queryByText('First chat')).not.toBeInTheDocument()
})

// The other half of RD5: a delete that FAILED used to be indistinguishable from
// one that worked — the row vanished from the list either way and reappeared on
// the next refresh.
test('a failed delete keeps the session and shows the server’s reason', async () => {
  deleteFailure = { status: 500, body: 'failed to delete agent session: connection refused' }
  renderList()
  await screen.findByText('First chat')

  await userEvent.click(screen.getByRole('button', { name: /delete first chat/i }))
  await userEvent.click(screen.getByRole('button', { name: /delete the conversation/i }))

  expect(deleted).toEqual(['/agent/session/s1'])
  expect(await screen.findByText(/connection refused/)).toBeInTheDocument()
  // Still there, and the dialog is still open, so the user knows it did not happen.
  expect(screen.getByRole('dialog')).toBeInTheDocument()
  expect(screen.getByText('First chat')).toBeInTheDocument()
})
