// @vitest-environment jsdom
import React from 'react'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { vi, test, expect } from 'vitest'
import { AgentChatProvider, useAgentChat, useAgentSessions } from './AgentChatProvider.js'

function Probe() {
  const { isStreaming } = useAgentChat()
  return <div>streaming:{String(isStreaming)}</div>
}

test('provider exposes chat context', () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <Probe />
    </AgentChatProvider>
  )
  expect(screen.getByText('streaming:false')).toBeInTheDocument()
})

test('useAgentChat throws outside provider', () => {
  const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
  expect(() => render(<Probe />)).toThrow(/AgentChatProvider/)
  spy.mockRestore()
})

// Task 5.3: useAgentSessions
test('useAgentSessions lists sessions', async () => {
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    if (String(url).includes('/agent/sessions')) {
      return new Response(JSON.stringify([{ id: 's1', title: 'First' }]), { status: 200 })
    }
    return new Response('{}', { status: 200 })
  }) as typeof globalThis.fetch

  function List() {
    const { sessions, refresh } = useAgentSessions()
    return <button onClick={refresh}>{sessions.length} sessions</button>
  }
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <List />
    </AgentChatProvider>
  )
  await userEvent.click(screen.getByRole('button'))
  expect(await screen.findByText('1 sessions')).toBeInTheDocument()
})

// refresh({userEmail}) drives the ?user_email= filter ('*' = all users).
test('refresh passes the user_email filter through', async () => {
  const fetched: string[] = []
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    fetched.push(String(url))
    return new Response(JSON.stringify([]), { status: 200 })
  }) as typeof globalThis.fetch

  function List() {
    const { refresh } = useAgentSessions()
    return (
      <>
        <button onClick={() => refresh({ userEmail: '*' })}>all</button>
        <button onClick={() => refresh({ userEmail: 'kai@example.com' })}>one</button>
        <button onClick={() => refresh()}>mine</button>
      </>
    )
  }
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <List />
    </AgentChatProvider>
  )
  await userEvent.click(screen.getByText('all'))
  await userEvent.click(screen.getByText('one'))
  await userEvent.click(screen.getByText('mine'))

  const sessionCalls = fetched.filter((u) => u.includes('/agent/sessions'))
  expect(sessionCalls.some((u) => u.endsWith('?user_email=*'))).toBe(true)
  expect(sessionCalls.some((u) => u.endsWith('?user_email=kai%40example.com'))).toBe(true)
  expect(sessionCalls.some((u) => !u.includes('user_email'))).toBe(true)
})
