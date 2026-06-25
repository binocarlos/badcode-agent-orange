// @vitest-environment jsdom
import React from 'react'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { test, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import AgentSessionList from './AgentSessionList.js'

let originalFetch: typeof globalThis.fetch
const fetchedUrls: string[] = []

beforeEach(() => {
  fetchedUrls.length = 0
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const urlStr = String(url)
    fetchedUrls.push(urlStr)
    // listSessions (refresh on mount)
    if (urlStr.includes('/agent/sessions')) {
      return new Response(JSON.stringify([{ id: 's1', title: 'First chat' }]), { status: 200 })
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

test('renders sessions from the provider', async () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentSessionList />
    </AgentChatProvider>
  )
  expect(await screen.findByText('First chat')).toBeInTheDocument()
})

test('selects (resumes) the session on click', async () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentSessionList />
    </AgentChatProvider>
  )
  const item = await screen.findByText('First chat')

  // Clear fetches captured during mount so the assertion is unambiguous.
  fetchedUrls.length = 0
  await userEvent.click(item)

  // select(id) -> resumeSession(id) fetches the session metadata at getSession('s1').
  expect(fetchedUrls.some((u) => u.includes('/agent/session/s1'))).toBe(true)
})

test('deletes the session on delete-button click', async () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentSessionList />
    </AgentChatProvider>
  )
  await screen.findByText('First chat')

  fetchedUrls.length = 0
  const deleteBtn = screen.getByRole('button', { name: /delete first chat/i })
  await userEvent.click(deleteBtn)

  // delete(id) issues a DELETE to deleteSession('s1') = /agent/session/s1, and the
  // item is removed from the list.
  expect(fetchedUrls.some((u) => u.includes('/agent/session/s1'))).toBe(true)
  expect(screen.queryByText('First chat')).not.toBeInTheDocument()
})
