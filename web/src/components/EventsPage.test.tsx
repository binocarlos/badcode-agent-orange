// @vitest-environment jsdom
// F1: the events/deliveries/jobs view, the dry-run subscription test, and the
// changelog — against stubbed /agent/events, /agent/deliveries,
// /agent/subscriptions and an injected config-log fetcher.

import React from 'react'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import EventsPage from './EventsPage.js'
import ChangelogView from './ChangelogView.js'
import { coerceConfigEvent, type ConfigEvent } from '../configLog.js'

let originalFetch: typeof globalThis.fetch
let requests: { url: string; method: string }[] = []
let events: Record<string, unknown>[]
let deliveries: Record<string, unknown>[]
let subscriptions: Record<string, unknown>[]
let queryEvents: Record<string, unknown>

const envelope = (over: Record<string, unknown> = {}) => ({
  depth: 0,
  source: 'external',
  worker: '',
  session_id: '',
  interactive: false,
  attention_requested: false,
  ...over,
})

beforeEach(() => {
  requests = []
  events = [
    {
      id: 'e1',
      project: 'acme',
      type: 'email.received',
      text: 'From: a@b.c\nSubject: help',
      envelope: envelope(),
      occurred_at: 1_700_000_000,
      created_at: 1_700_000_000,
      delivered: true,
    },
    {
      id: 'e2',
      project: 'acme',
      type: 'worker.finished',
      text: 'transcript…',
      envelope: envelope({ source: 'worker', worker: 'email-answerer', depth: 1 }),
      occurred_at: 1_700_000_100,
      created_at: 1_700_000_100,
      delivered: true,
    },
  ]
  deliveries = [
    {
      id: 'd1',
      project: 'acme',
      event_id: 'e1',
      subscription_id: 's1',
      session_id: 'sess-1',
      status: 'ok',
      started_at: 1_700_000_010,
      ended_at: 1_700_000_070,
      created_at: 1_700_000_010,
      updated_at: 1_700_000_070,
    },
    {
      id: 'd2',
      project: 'acme',
      event_id: 'e2',
      subscription_id: 's2',
      session_id: 'sess-2',
      status: 'awaiting_human',
      started_at: 1_700_000_110,
      ended_at: 0,
      created_at: 1_700_000_110,
      updated_at: 1_700_000_110,
    },
  ]
  subscriptions = [
    {
      id: 's1',
      project: 'acme',
      event_type: 'email.*',
      filter: {},
      worker: 'email-answerer',
      max_firings_per_hour: 0,
      enabled: true,
    },
    {
      id: 's2',
      project: 'acme',
      event_type: 'worker.finished',
      filter: { worker: 'email-answerer' },
      worker: 'review-consultant',
      max_firings_per_hour: 0,
      enabled: true,
    },
  ]
  // The real `GET /agent/session/{id}/query-events` shape: usage is nested
  // under `data.usage`, camelCase, on the LAST envelope of the row. Captured
  // 2026-07-28 off the e2e stack's Postgres — see events.test.ts for the full
  // fixture and why an invented shape here used to make every total read 0.
  queryEvents = {
    events: [
      {
        events: [
          { type: 'user_message', data: { content: 'go' } },
          {
            type: 'query_complete',
            data: { status: 'completed', model: 'claude-opus-4-5', usage: { inputTokens: 1200, outputTokens: 340 } },
          },
        ],
      },
    ],
  }

  window.history.replaceState(null, '', '/')
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const u = String(url)
    requests.push({ url: u, method: init?.method ?? 'GET' })
    const json = (v: unknown) =>
      new Response(JSON.stringify(v), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    if (u.includes('/query-events')) return json(queryEvents)
    if (u.includes('/agent/deliveries')) return json({ deliveries })
    if (u.includes('/agent/subscriptions')) return json({ subscriptions })
    if (u.includes('/agent/events')) return json({ events })
    return json({})
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
  window.history.replaceState(null, '', '/')
})

function renderPage(props: Partial<React.ComponentProps<typeof EventsPage>> = {}) {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <EventsPage projectId="acme" nowSeconds={1_700_000_410} {...props} />
    </AgentChatProvider>,
  )
}

describe('events view', () => {
  it('lists the project’s events with their source and depth', async () => {
    renderPage()
    expect(await screen.findByText('email.received')).toBeInTheDocument()
    expect(screen.getByText('worker.finished')).toBeInTheDocument()
    expect(screen.getByText(/depth 1 · from email-answerer/)).toBeInTheDocument()
  })

  it('never writes: every request it makes is a GET', async () => {
    renderPage()
    await screen.findByText('email.received')
    await waitFor(() => expect(requests.length).toBeGreaterThanOrEqual(3))
    expect(requests.every((r) => r.method === 'GET')).toBe(true)
  })

  it('puts the selected event in the URL, router-free', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email.received'))
    await waitFor(() => expect(window.location.search).toBe('?event=e1'))
  })

  it('opens the event named by the URL on mount and shows its envelope', async () => {
    window.history.replaceState(null, '', '/?event=e2')
    renderPage()
    expect(await screen.findByText('source: worker')).toBeInTheDocument()
    expect(screen.getByText('depth: 1')).toBeInTheDocument()
    expect(screen.getByText('interactive: false')).toBeInTheDocument()
  })

  it('leaves the URL alone when the host controls selection', async () => {
    const onSelect = vi.fn()
    renderPage({ selected: null, onSelect })
    await userEvent.click(await screen.findByText('email.received'))
    expect(onSelect).toHaveBeenCalledWith('e1')
    expect(window.location.search).toBe('')
  })

  it('shows the jobs one event produced under that event', async () => {
    window.history.replaceState(null, '', '/?event=e1')
    renderPage()
    const table = await screen.findByRole('table', { name: /job history/i })
    expect(within(table).getByText('email-answerer')).toBeInTheDocument()
    expect(within(table).queryByText('review-consultant')).not.toBeInTheDocument()
  })
})

describe('job history (L29)', () => {
  it('shows event, worker, duration, status and a permalink per job', async () => {
    renderPage()
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /jobs/i }))

    const table = await screen.findByRole('table', { name: /job history/i })
    const rows = within(table).getAllByRole('row')
    // Newest delivery first: d2 (awaiting_human) then d1 (ok).
    expect(within(rows[1]!).getByText('worker.finished')).toBeInTheDocument()
    expect(within(rows[1]!).getByText('review-consultant')).toBeInTheDocument()
    expect(within(rows[1]!).getByText('awaiting_human')).toBeInTheDocument()
    expect(within(rows[2]!).getByText('ok')).toBeInTheDocument()
    expect(within(rows[2]!).getByText('1m 0s')).toBeInTheDocument()

    const links = within(table).getAllByRole('link', { name: 'open' })
    expect(links[0]).toHaveAttribute('href', '/p/acme/s/sess-2')
    expect(links[1]).toHaveAttribute('href', '/p/acme/s/sess-1')
  })

  it('keeps counting an awaiting_human job, whose ended_at the engine leaves unset', async () => {
    renderPage()
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /jobs/i }))
    const table = await screen.findByRole('table', { name: /job history/i })
    const rows = within(table).getAllByRole('row')
    // now (1_700_000_410) − started_at (1_700_000_110) = 300s.
    expect(within(rows[1]!).getByText('5m 0s')).toBeInTheDocument()
    expect(within(rows[1]!).getByText('(so far)')).toBeInTheDocument()
  })

  it('loads token totals for the first rows unprompted', async () => {
    renderPage()
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /jobs/i }))
    // tokenAutoLoad defaults to 10, so both rows fetch: 1200 + 340 each.
    expect(await screen.findAllByText('1,540')).toHaveLength(2)
  })

  it('leaves the rest behind a button rather than firing a request per row', async () => {
    renderPage({ tokenAutoLoad: 0 })
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /jobs/i }))

    const table = await screen.findByRole('table', { name: /job history/i })
    const loadButtons = within(table).getAllByRole('button', { name: 'load' })
    expect(loadButtons).toHaveLength(2)
    expect(requests.some((r) => r.url.includes('/query-events'))).toBe(false)

    await userEvent.click(loadButtons[0]!)
    expect(await within(table).findByText('1,540')).toBeInTheDocument()
    expect(requests.filter((r) => r.url.includes('/query-events'))).toHaveLength(1)
  })

  it('hands the session click to the host when onOpenSession is supplied', async () => {
    const onOpenSession = vi.fn()
    renderPage({ onOpenSession })
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /jobs/i }))
    const table = await screen.findByRole('table', { name: /job history/i })
    await userEvent.click(within(table).getAllByRole('button', { name: 'open' })[0]!)
    expect(onOpenSession).toHaveBeenCalledWith('sess-2')
  })
})

describe('event replay / subscription test', () => {
  it('says which subscriptions would match a pasted event, and why not', async () => {
    renderPage()
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /replay/i }))

    const editor = await screen.findByLabelText('Event JSON')
    await userEvent.clear(editor)
    await userEvent.type(
      editor,
      '{{"type":"email.received","text":"hi","envelope":{{"source":"external"}}',
    )

    expect(await screen.findByText(/1 of 2 subscriptions would match/)).toBeInTheDocument()
    const results = screen.getByLabelText('Subscription match results')
    expect(within(results).getByText('match')).toBeInTheDocument()
    expect(within(results).getByText(/does not match "worker.finished"/)).toBeInTheDocument()
  })

  it('is a dry run: pressing nothing and typing anything issues no POST', async () => {
    renderPage()
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /replay/i }))
    const editor = await screen.findByLabelText('Event JSON')
    await userEvent.clear(editor)
    await userEvent.type(editor, '{{"type":"email.received"}')
    await screen.findByLabelText('Subscription match results')
    expect(requests.every((r) => r.method === 'GET')).toBe(true)
  })

  it('reports unparsable JSON instead of pretending nothing matched', async () => {
    renderPage()
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /replay/i }))
    const editor = await screen.findByLabelText('Event JSON')
    await userEvent.clear(editor)
    await userEvent.type(editor, '{{oops')
    await waitFor(() =>
      expect(screen.queryByLabelText('Subscription match results')).not.toBeInTheDocument(),
    )
  })

  it('loads the selected event into the editor', async () => {
    window.history.replaceState(null, '', '/?event=e2')
    renderPage()
    await screen.findByText('source: worker')
    await userEvent.click(screen.getByRole('tab', { name: /replay/i }))
    await userEvent.click(await screen.findByRole('button', { name: /load “worker.finished”/i }))
    const editor = (await screen.findByLabelText('Event JSON')) as HTMLTextAreaElement
    expect(editor.value).toContain('"type": "worker.finished"')
    expect(editor.value).toContain('"worker": "email-answerer"')
  })
})

// ---------------------------------------------------------------------------
// Changelog (§15.10) — driven through the injected fetcher, because
// GET /agent/config-events does not exist yet.
// ---------------------------------------------------------------------------

const configEvent = (over: Partial<ConfigEvent>): ConfigEvent =>
  coerceConfigEvent({
    id: 'c1',
    project: 'acme',
    actor_worker: '',
    actor_session: '',
    action: 'worker_update',
    payload: { name: 'email-answerer' },
    rationale: '',
    created_at: 1_789_000_000_000,
    ...over,
  })

describe('changelog view', () => {
  const log: ConfigEvent[] = [
    configEvent({
      id: 'c1',
      created_at: 1_789_000_000_000,
      action: 'worker_create',
      payload: { name: 'email-answerer', system_prompt: 'answer email thoroughly' },
    }),
    configEvent({
      id: 'c2',
      created_at: 1_789_000_100_000,
      action: 'worker_prompt_write',
      actor_worker: 'review-consultant',
      actor_session: 'sess-9',
      payload: { name: 'email-answerer', system_prompt: 'answer email briefly' },
      rationale: 'customers said the replies were too long',
    }),
    configEvent({
      id: 'c3',
      created_at: 1_789_000_200_000,
      action: 'subscription_create',
      payload: { id: 'sub-1', event_type: 'email.*', worker: 'email-answerer' },
    }),
  ]

  const renderChangelog = (props: Partial<React.ComponentProps<typeof ChangelogView>> = {}) =>
    render(
      <ChangelogView
        projectId="acme"
        fetchConfigEvents={async () => log}
        {...props}
      />,
    )

  it('renders the log newest first, with the rationale as the commit message', async () => {
    renderChangelog()
    const items = await screen.findAllByRole('listitem')
    expect(within(items[0]!).getByText('Created subscription “sub-1”')).toBeInTheDocument()
    expect(
      within(items[1]!).getByText('Rewrote the prompt of worker “email-answerer”'),
    ).toBeInTheDocument()
    expect(
      within(items[1]!).getByText('customers said the replies were too long'),
    ).toBeInTheDocument()
  })

  it('computes the prompt diff at read time between consecutive events for the key', async () => {
    renderChangelog()
    const diff = await screen.findByLabelText('Prompt diff')
    expect(within(diff).getByText('-answer email thoroughly')).toBeInTheDocument()
    expect(within(diff).getByText('+answer email briefly')).toBeInTheDocument()
    expect(screen.getByText(/\+1 −1 against the previous version/)).toBeInTheDocument()
  })

  it('deep-links each entry to the session that decided it', async () => {
    renderChangelog()
    const link = await screen.findByRole('link', { name: /the session that decided it/i })
    expect(link).toHaveAttribute('href', '/p/acme/s/sess-9')
  })

  it('marks human/API edits as having no acting session', async () => {
    renderChangelog()
    const items = await screen.findAllByRole('listitem')
    expect(within(items[0]!).getByText(/by a human \(UI or API\)/)).toBeInTheDocument()
  })

  it('filters by action group', async () => {
    renderChangelog()
    await screen.findAllByRole('listitem')
    await userEvent.click(screen.getByRole('combobox', { name: /show/i }))
    await userEvent.click(await screen.findByRole('option', { name: 'Subscriptions' }))
    await waitFor(() => expect(screen.getAllByRole('listitem')).toHaveLength(1))
    expect(screen.getByText('Created subscription “sub-1”')).toBeInTheDocument()
  })

  it('shows the full-state payload on demand — §15.2’s payload is never a diff', async () => {
    renderChangelog()
    const items = await screen.findAllByRole('listitem')
    await userEvent.click(within(items[0]!).getByRole('button', { name: /show full state/i }))
    expect(await within(items[0]!).findByText(/"event_type": "email\.\*"/)).toBeInTheDocument()
  })

  it('says the route is not mounted rather than showing an empty history', async () => {
    // What a deployment without J2/J3's handler actually answers.
    renderChangelog({
      fetchConfigEvents: async () => {
        throw new Error('404 page not found')
      },
    })
    expect(await screen.findByText(/does not serve it yet/i)).toBeInTheDocument()
    expect(screen.getByText('GET /agent/config-events')).toBeInTheDocument()
  })

  it('is reachable as a tab of the events page', async () => {
    renderPage({ fetchConfigEvents: async () => log })
    await screen.findByText('email.received')
    await userEvent.click(screen.getByRole('tab', { name: /changelog/i }))
    expect(await screen.findByText('Created subscription “sub-1”')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// W4 — feed liveness on the events surface (doc 21 §4.2)
// ---------------------------------------------------------------------------

describe('liveness (W4)', () => {
  it('makes the event list a role="log"', async () => {
    renderPage()
    await screen.findByText('email.received')
    expect(screen.getByRole('log', { name: 'Events' })).toBeInTheDocument()
    expect(screen.queryAllByRole('feed')).toHaveLength(0)
  })

  it('keeps one always-mounted status region for the pill to announce into', async () => {
    renderPage()
    await screen.findByText('email.received')
    // Mounted while empty on purpose: a live region inserted at the moment it
    // gains text is a live region that may never be announced.
    expect(screen.getAllByRole('status').length).toBeGreaterThan(0)
    expect(screen.queryByTestId('new-items-pill')).toBeNull()
  })

  it('shows no pause toggle until something actually polls', async () => {
    renderPage()
    await screen.findByText('email.received')
    expect(screen.queryByRole('checkbox', { name: 'Pause live updates' })).toBeNull()
  })
})
