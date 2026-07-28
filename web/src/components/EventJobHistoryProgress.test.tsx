// @vitest-environment jsdom
// W6 (doc 21 §4.2 "Long agent jobs", §5-M6): the long-wait affordance in the
// job table.
//
// Three claims are pinned, because each is a way this feature could quietly
// become dishonest: a long-running row says how many STEPS it has taken (never
// a percentage), the step line comes from the query-events response the table
// already fetched for tokens (never a second request or a backend field), and a
// finished row reports when it stopped and what it produced rather than "Done".

import React from 'react'
import { render, screen, waitFor, within } from '@testing-library/react'
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { ThemeProvider, createTheme } from '@mui/material/styles'
import EventJobHistory from './EventJobHistory.js'
import { buildJobRows, type EventDelivery, type ProjectEvent, type Subscription } from '../events.js'

const NOW = 1_785_000_000

function delivery(over: Partial<EventDelivery>): EventDelivery {
  return {
    id: 'd1',
    project: 'acme',
    event_id: 'e1',
    subscription_id: 's1',
    session_id: 'sess-1',
    status: 'running',
    started_at: NOW - 300,
    ended_at: 0,
    created_at: NOW - 300,
    updated_at: NOW - 300,
    ...over,
  }
}

const event: ProjectEvent = {
  id: 'e1',
  project: 'acme',
  type: 'email.received',
  payload: {},
  labels: {},
  source: 'test',
  created_at: NOW - 300,
} as unknown as ProjectEvent

const subscription: Subscription = {
  id: 's1',
  project: 'acme',
  event_type: 'email.received',
  filter: {},
  worker: 'email-answerer',
  max_firings_per_hour: 0,
  enabled: true,
  created_at: 1,
  updated_at: 1,
}

const toolStart = (id: string, toolName: string, input: Record<string, unknown> = {}) => ({
  type: 'tool_use_start',
  timestamp: '2026-07-25T23:29:03.604Z',
  data: { toolCallId: id, toolName, input },
})

/** The route's real envelope, minimal: three tool calls and one artifact. */
const queryEventsPayload = {
  events: [
    {
      id: 'row-1',
      session_id: 'sess-1',
      query_id: 'q-1',
      created_at: NOW,
      search_text: '',
      events: [
        toolStart('t1', 'read', { file_path: '/workspace/inbox.md' }),
        { type: 'tool_use_end', data: { toolCallId: 't1' } },
        toolStart('t2', 'bash', { command: 'ls' }),
        { type: 'tool_use_end', data: { toolCallId: 't2' } },
        toolStart('t3', 'text_editor', { command: 'create' }),
        { type: 'artifact_registered', data: { filePath: '/out/reply.md', label: 'Draft reply' } },
        { type: 'query_complete', data: { usage: { inputTokens: 10, outputTokens: 5 } } },
      ],
    },
  ],
}

let requests: string[] = []
let originalFetch: typeof globalThis.fetch

beforeEach(() => {
  requests = []
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input))
    return new Response(JSON.stringify(queryEventsPayload), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as unknown as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

function renderTable(d: EventDelivery, props: Record<string, unknown> = {}) {
  const jobs = buildJobRows([d], [event], [subscription], NOW)
  return render(
    <ThemeProvider theme={createTheme({ palette: { mode: 'light' } })}>
      <EventJobHistory jobs={jobs} projectId="acme" nowSeconds={NOW} {...props} />
    </ThemeProvider>,
  )
}

describe('the long-job step affordance', () => {
  it('shows step count and last-step label on a job that has been running past the threshold', async () => {
    renderTable(delivery({ status: 'running' }))
    const line = await screen.findByTestId('job-step-line')
    await waitFor(() => expect(line.textContent).toBe('step 3 · Write File'))
    // The honest affordance, and nothing more: no percentage, no fake total.
    expect(line.textContent).not.toMatch(/%|of \d/)
    // A screen reader gets the sentence; the compact line is hidden from it.
    expect(line.getAttribute('aria-label')).toBe('3 steps so far, most recently Write File')
  })

  it('shows it on a parked ask too — the longest wait on the console', async () => {
    renderTable(delivery({ status: 'awaiting_human' }))
    await waitFor(() =>
      expect(screen.getByTestId('job-step-line').textContent).toBe('step 3 · Write File'),
    )
  })

  it('costs no extra request: the step line and the token total are one response', async () => {
    renderTable(delivery({ status: 'running' }))
    await screen.findByTestId('job-step-line')
    await waitFor(() => expect(requests.length).toBe(1))
    expect(requests[0]).toContain('/agent/session/sess-1/query-events')
    // Same fetch, both readings.
    await waitFor(() => expect(screen.getByText('15')).toBeTruthy())
  })

  it('stays inside the token budget — a row past it fetches nothing', async () => {
    // The step line rides on the token request; it does not buy itself a new
    // one. `tokenAutoLoad: 0` means no request, which means no step line.
    renderTable(delivery({ status: 'running' }), { tokenAutoLoad: 0 })
    await screen.findByText('load')
    expect(screen.queryByTestId('job-step-line')).toBeNull()
    expect(requests.filter((r) => r.includes('/query-events'))).toHaveLength(0)
  })

  it('stays off a short job — under ten seconds the elapsed is the whole truth', async () => {
    renderTable(delivery({ status: 'running', started_at: NOW - 3 }))
    await screen.findByText('3s')
    expect(screen.queryByTestId('job-step-line')).toBeNull()
  })
})

describe('a finished job', () => {
  it('reports when it stopped and what it produced, never a generic "Done"', async () => {
    renderTable(delivery({ status: 'ok', ended_at: NOW - 60, started_at: NOW - 300 }))
    // Start is its own column, elapsed is the duration cell; the stop time and
    // the product are what were missing.
    const span = await screen.findByTestId('job-span')
    expect(span.textContent).toMatch(/^ended /)
    await waitFor(() =>
      expect(screen.getByTestId('job-produced').textContent).toBe('made Draft reply'),
    )
    expect(screen.queryByText('Done')).toBeNull()
    // And it does not tick: a finished row has no step line either.
    expect(screen.queryByTestId('job-step-line')).toBeNull()
    expect(within(screen.getByTestId('job-produced').closest('td')!).getByText('open')).toBeTruthy()
  })
})
