// @vitest-environment jsdom
// BA1 — the three-column before/after (design §7.2), against a stubbed
// /agent/events.

import React from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import BeforeAfterView from './BeforeAfterView.js'
import { coerceConfigEvent } from '../configLog.js'

let originalFetch: typeof globalThis.fetch
let events: Record<string, unknown>[]

const envelope = (worker: string, sessionId: string) => ({
  depth: 1,
  source: 'worker',
  worker,
  session_id: sessionId,
  interactive: false,
  attention_requested: false,
})

const rewrite = coerceConfigEvent({
  id: 'c1',
  project: 'acme',
  actor_worker: 'email-reviewer',
  actor_session: 'sess-r',
  action: 'worker_prompt_write',
  payload: { name: 'email-answerer', system_prompt: 'new prompt' },
  rationale: 'the rule is now first',
  created_at: 1_700_000_100_000,
})

beforeEach(() => {
  events = [
    {
      id: 'e1',
      project: 'acme',
      type: 'worker.finished',
      text: 'Hi Jane, thanks for getting in touch',
      envelope: envelope('email-answerer', 'sess-1'),
      occurred_at: 1_700_000_000,
      created_at: 1_700_000_000,
      delivered: true,
    },
    {
      id: 'e2',
      project: 'acme',
      type: 'worker.finished',
      text: 'Ticket #4471 — Hi Jane',
      envelope: envelope('email-answerer', 'sess-2'),
      occurred_at: 1_700_000_200,
      created_at: 1_700_000_200,
      delivered: true,
    },
  ]
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    const body = url.includes('/agent/events')
      ? { events }
      : url.includes('/agent/deliveries')
        ? { deliveries: [] }
        : { subscriptions: [] }
    return new Response(JSON.stringify(body), { status: 200 })
  }) as unknown as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

describe('BeforeAfterView', () => {
  it('renders both transcripts, the rationale and the caveat', async () => {
    render(<BeforeAfterView configEvent={rewrite} />)
    await waitFor(() =>
      expect(screen.getByText(/Hi Jane, thanks for getting in touch/)).toBeInTheDocument(),
    )
    expect(screen.getByText(/Ticket #4471/)).toBeInTheDocument()
    expect(screen.getByText(/the rule is now first/)).toBeInTheDocument()
    expect(screen.getByText(/email-reviewer said:/)).toBeInTheDocument()
    expect(
      screen.getByText(/this shows what the worker said, never what it did/),
    ).toBeInTheDocument()
  })

  it('says no job has run since when a side is missing', async () => {
    events = events.filter((e) => e.id === 'e1')
    render(<BeforeAfterView configEvent={rewrite} />)
    await waitFor(() => expect(screen.getByText('no job has run since')).toBeInTheDocument())
  })
})
