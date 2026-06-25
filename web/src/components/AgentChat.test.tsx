// @vitest-environment jsdom
import React from 'react'
import { render, screen } from '@testing-library/react'
import { test, expect } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import AgentChat from './AgentChat.js'
import type { AgentMessage } from '../types.js'

test('AgentChat renders from provider context', () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat />
    </AgentChatProvider>
  )
  // Matches the real placeholder text in AgentChat.tsx line ~679
  expect(screen.getByPlaceholderText(/type a message/i)).toBeInTheDocument()
})

// ---------------------------------------------------------------------------
// messages render
// ---------------------------------------------------------------------------

test('AgentChat renders user and assistant messages', () => {
  const messages: AgentMessage[] = [
    { id: 'm1', role: 'user', content: 'Hello agent!', timestamp: '2024-01-01T00:00:00Z' },
    { id: 'm2', role: 'assistant', content: 'Hello! How can I help?', timestamp: '2024-01-01T00:00:01Z' },
  ]
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat messages={messages} />
    </AgentChatProvider>
  )
  expect(screen.getByText('Hello agent!')).toBeInTheDocument()
  expect(screen.getByText('Hello! How can I help?')).toBeInTheDocument()
})

test('AgentChat renders multiple messages in order', () => {
  const messages: AgentMessage[] = [
    { id: 'm1', role: 'user', content: 'First message', timestamp: '2024-01-01T00:00:00Z' },
    { id: 'm2', role: 'assistant', content: 'Second message', timestamp: '2024-01-01T00:00:01Z' },
    { id: 'm3', role: 'user', content: 'Third message', timestamp: '2024-01-01T00:00:02Z' },
  ]
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat messages={messages} />
    </AgentChatProvider>
  )
  expect(screen.getByText('First message')).toBeInTheDocument()
  expect(screen.getByText('Third message')).toBeInTheDocument()
})

test('AgentChat assigns data-role to message boxes', () => {
  const messages: AgentMessage[] = [
    { id: 'u1', role: 'user', content: 'My question', timestamp: '2024-01-01T00:00:00Z' },
    { id: 'a1', role: 'assistant', content: 'My answer', timestamp: '2024-01-01T00:00:01Z' },
  ]
  const { container } = render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat messages={messages} />
    </AgentChatProvider>
  )
  expect(container.querySelector('[data-role="user"]')).not.toBeNull()
  expect(container.querySelector('[data-role="assistant"]')).not.toBeNull()
})

// ---------------------------------------------------------------------------
// error state displays
// ---------------------------------------------------------------------------

test('AgentChat displays error alert when error prop is set', () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat error="Connection failed" />
    </AgentChatProvider>
  )
  expect(screen.getByText('Connection failed')).toBeInTheDocument()
  // MUI Alert renders with role="alert" when severity is set
  expect(screen.getByRole('alert')).toBeInTheDocument()
})

test('AgentChat does not show error alert when error is null', () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat error={null} />
    </AgentChatProvider>
  )
  expect(screen.queryByRole('alert')).toBeNull()
})

test('AgentChat shows error alongside messages', () => {
  const messages: AgentMessage[] = [
    { id: 'm1', role: 'user', content: 'A message', timestamp: '2024-01-01T00:00:00Z' },
  ]
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat messages={messages} error="Something went wrong" />
    </AgentChatProvider>
  )
  expect(screen.getByText('A message')).toBeInTheDocument()
  expect(screen.getByText('Something went wrong')).toBeInTheDocument()
})

// ---------------------------------------------------------------------------
// empty session placeholder
// ---------------------------------------------------------------------------

test('AgentChat shows input placeholder when no messages', () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat messages={[]} />
    </AgentChatProvider>
  )
  expect(screen.getByPlaceholderText(/type a message/i)).toBeInTheDocument()
})

test('AgentChat renders no message bubbles when messages is empty', () => {
  const { container } = render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat messages={[]} />
    </AgentChatProvider>
  )
  // No data-role elements should be present when there are no messages
  expect(container.querySelector('[data-role]')).toBeNull()
})

test('AgentChat shows Send button in empty state', () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat messages={[]} />
    </AgentChatProvider>
  )
  expect(screen.getByRole('button', { name: /send/i })).toBeInTheDocument()
})

// ---------------------------------------------------------------------------
// readOnly mode
// ---------------------------------------------------------------------------

test('AgentChat hides input when readOnly is true', () => {
  render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AgentChat readOnly={true} />
    </AgentChatProvider>
  )
  expect(screen.queryByPlaceholderText(/type a message/i)).toBeNull()
  expect(screen.queryByRole('button', { name: /send/i })).toBeNull()
})
