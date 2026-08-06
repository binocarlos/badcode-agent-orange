// @vitest-environment jsdom
// RD15 (item I1): a job outlives the session it points at.
//
// `event_deliveries.session_id` is a plain VARCHAR with no foreign key, so
// deleting a session leaves its job history behind — a green `ok` row with an
// "open" link into a 404. These tests pin the two halves of the honest answer:
// the row still says the job ran (history is not rewritten), and the link is
// replaced by "transcript deleted" rather than left to fail on click.
//
// The distinction is drawn from the HTTP STATUS. A 404 means the session is
// gone; a 500 means we do not know, and a UI that reported "deleted" on a
// server error would be inventing a fact about the user's data.

import React from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { ThemeProvider, createTheme } from '@mui/material/styles'
import EventJobHistory from './EventJobHistory.js'
import { buildJobRows, type EventDelivery, type ProjectEvent, type Subscription } from '../events.js'

const NOW = 1_785_000_000

const delivery: EventDelivery = {
  id: 'd1',
  project: 'acme',
  event_id: 'e1',
  subscription_id: 's1',
  session_id: 'sess-gone',
  worker: 'answerer',
  status: 'ok',
  failure_reason: '',
  started_at: NOW - 300,
  ended_at: NOW - 200,
  created_at: NOW - 300,
  updated_at: NOW - 200,
}

const event: ProjectEvent = {
  id: 'e1',
  project: 'acme',
  type: 'email.received',
  text: 'a mail arrived',
  envelope: {
    depth: 0,
    source: 'external',
    worker: '',
    session_id: '',
    interactive: false,
    attention_requested: false,
  },
  occurred_at: NOW - 301,
  created_at: NOW - 301,
  delivered: true,
}

const subscription: Subscription = {
  id: 's1',
  project: 'acme',
  event_type: 'email.received',
  filter: {},
  worker: 'answerer',
  max_firings_per_hour: 0,
  enabled: true,
  created_at: NOW - 1000,
  updated_at: NOW - 1000,
}

const originalFetch = globalThis.fetch

function respondWith(status: number, body: string) {
  globalThis.fetch = vi.fn(async () => new Response(body, { status })) as unknown as typeof globalThis.fetch
}

beforeEach(() => {
  respondWith(404, 'not found')
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

function renderTable(props: Record<string, unknown> = {}) {
  const jobs = buildJobRows([delivery], [event], [subscription], NOW)
  return render(
    <ThemeProvider theme={createTheme({ palette: { mode: 'light' } })}>
      <EventJobHistory jobs={jobs} projectId="acme" nowSeconds={NOW} {...props} />
    </ThemeProvider>,
  )
}

describe('a job whose session has been deleted', () => {
  it('says the transcript is deleted instead of offering a link into nothing', async () => {
    renderTable()
    await screen.findByTestId('job-transcript-deleted')
    expect(screen.getByText('transcript deleted')).toBeTruthy()
    expect(screen.queryByText('open')).toBeNull()
  })

  it('still reports that the job ran — the history is not rewritten', async () => {
    renderTable()
    await screen.findByTestId('job-transcript-deleted')
    expect(screen.getByText('answerer')).toBeTruthy()
    expect(screen.getByText('email.received')).toBeTruthy()
    expect(screen.getByText('ok')).toBeTruthy()
  })

  it('does not shout "error" in the tokens cell for a session that is simply gone', async () => {
    renderTable()
    await screen.findByTestId('job-transcript-deleted')
    expect(screen.queryByText('error')).toBeNull()
  })

  it('keeps the link when the read fails for any other reason', async () => {
    // A 500 is "we do not know", not "it is deleted". Reporting deletion here
    // would be the silent-success shape one layer up: a confident sentence
    // about the user's data derived from a failed request.
    respondWith(500, 'boom')
    renderTable()
    await waitFor(() => expect(screen.getByText('error')).toBeTruthy())
    expect(screen.queryByTestId('job-transcript-deleted')).toBeNull()
    expect(screen.getByText('open')).toBeTruthy()
  })

  it('keeps the link on a row that was never read — it does not guess', async () => {
    // Past the token budget nothing is fetched, so nothing is known. The link
    // stays and the click finds out; a table that fetched every session to
    // grey out a link would cost a request per row.
    renderTable({ tokenAutoLoad: 0 })
    await screen.findByText('open')
    expect(screen.queryByTestId('job-transcript-deleted')).toBeNull()
  })
})
