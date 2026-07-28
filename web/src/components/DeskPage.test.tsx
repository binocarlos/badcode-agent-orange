// @vitest-environment jsdom
// DK2: the Desk — the three stacks on the spine, the "since you last looked"
// mark in localStorage, the first-run state, and the degraded render when the
// attention route is not mounted.

import React from 'react'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import DeskPage from './DeskPage.js'
import { deskLastSeenKey, readDeskLastSeen, writeDeskLastSeen } from '../useDesk.js'

// A moment safely in the past: `markSeen` stamps the real wall clock, and a
// fixture newer than today would never fall behind the mark.
const NOW = 1_700_000_000
const NOW_MS = NOW * 1000

let originalFetch: typeof globalThis.fetch
let workers: Record<string, unknown>[]
let deliveries: Record<string, unknown>[]
let subscriptions: Record<string, unknown>[]
let events: Record<string, unknown>[]
let schedules: Record<string, unknown>[]
let configEvents: Record<string, unknown>[]
let attentionRequests: Record<string, unknown>[]
let attentionStatus: number

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
  window.localStorage.clear()
  workers = [{ name: 'email-answerer', project: 'acme', system_prompt: 'Answer.', enabled: true }]
  subscriptions = [
    {
      id: 's1',
      project: 'acme',
      event_type: 'email.received',
      filter: {},
      worker: 'email-answerer',
      max_firings_per_hour: 0,
      enabled: true,
    },
  ]
  deliveries = [
    {
      id: 'd1',
      project: 'acme',
      event_id: 'e1',
      subscription_id: 's1',
      session_id: 'sess-1',
      status: 'awaiting_human',
      started_at: NOW - 9600,
      ended_at: 0,
      created_at: NOW - 9600,
      updated_at: NOW - 9600,
    },
    {
      id: 'd2',
      project: 'acme',
      event_id: 'e2',
      subscription_id: 's1',
      session_id: 'sess-2',
      status: 'failed',
      started_at: NOW - 7200,
      ended_at: NOW - 7100,
      created_at: NOW - 7200,
      updated_at: NOW - 7100,
    },
  ]
  events = [
    {
      id: 'pe1',
      project: 'acme',
      type: 'worker.freeze_refused',
      text: 'Refused worker_prompt_write against frozen worker "fee-scorer". Attempted by worker "tuner".',
      envelope: envelope({ source: 'core', worker: 'tuner', session_id: 'sess-9' }),
      occurred_at: NOW - 3600,
      created_at: NOW - 3600,
      delivered: false,
    },
  ]
  schedules = [
    {
      id: 'sch-1',
      project: 'acme',
      worker: 'nightly-sweep',
      cron: '0 2 * * *',
      input: 'Sweep.',
      enabled: false,
      created_at: NOW - 100_000,
      updated_at: NOW - 1000,
      provision_failures: 5,
      last_provision_error: 'image "toolbox:9" names no image in the catalogue',
    },
  ]
  configEvents = [
    {
      id: 'c1',
      project: 'acme',
      actor_worker: 'email-reviewer',
      actor_session: 'sess-7',
      action: 'worker_prompt_write',
      payload: { name: 'email-answerer', system_prompt: 'Answer.\nQuote the reference.' },
      rationale: 'answers kept omitting the ticket reference',
      created_at: NOW_MS - 1000,
    },
  ]
  attentionRequests = [
    {
      id: 'a1',
      project: 'acme',
      session_id: 'sess-1',
      worker: 'email-answerer',
      message: 'Reply drafted for the Ridley invoice query — send as-is, or hold?',
      session_url: '/p/acme/s/sess-1',
      channel: 'webhook',
      delivered: true,
      expires_at: 0,
      created_at: NOW - 9600,
      answered_at: 0,
      timed_out_at: 0,
    },
  ]
  attentionStatus = 200

  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const u = String(url)
    const json = (v: unknown) =>
      new Response(JSON.stringify(v), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    if (u.includes('/agent/attention-requests')) {
      if (attentionStatus !== 200) {
        return new Response('attention requests are not configured on this host', {
          status: attentionStatus,
        })
      }
      return json({ attention_requests: attentionRequests })
    }
    if (u.includes('/agent/config-events')) return json({ config_events: configEvents })
    if (u.includes('/agent/deliveries')) return json({ deliveries })
    if (u.includes('/agent/subscriptions')) return json({ subscriptions })
    if (u.includes('/agent/schedules')) return json({ schedules })
    if (u.includes('/agent/workers')) return json({ workers })
    if (u.includes('/agent/events')) return json({ events })
    return json({})
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
  window.localStorage.clear()
})

function renderDesk(props: Partial<React.ComponentProps<typeof DeskPage>> = {}) {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <DeskPage projectId="acme" nowSeconds={NOW} {...props} />
    </AgentChatProvider>,
  )
}

describe('the three stacks', () => {
  it('asks first, with the sentence the worker wrote and how long it has waited', async () => {
    renderDesk()
    const asks = await screen.findByRole('region', { name: 'Asks' })
    expect(within(asks).getByText('email-answerer · awaiting_human · 2h 40m')).toBeInTheDocument()
    expect(within(asks).getByText(/Ridley invoice query/)).toBeInTheDocument()
    expect(within(asks).getByText(/stays parked at awaiting_human/)).toBeInTheDocument()
  })

  it('reads the changes as a changelog: who, what, and why', async () => {
    renderDesk()
    const changes = await screen.findByRole('region', { name: 'Changes' })
    expect(
      await within(changes).findByText('email-reviewer rewrote email-answerer'),
    ).toBeInTheDocument()
    expect(
      within(changes).getByText('answers kept omitting the ticket reference'),
    ).toBeInTheDocument()
  })

  it('collects the three failure shapes, each with the sentence the docs wrote', async () => {
    renderDesk()
    const trouble = await screen.findByRole('region', { name: 'Trouble' })
    expect(
      await within(trouble).findByText('1 delivery failed · worker email-answerer'),
    ).toBeInTheDocument()
    expect(within(trouble).getByText(/No reason is recorded on a delivery row/)).toBeInTheDocument()
    expect(
      within(trouble).getByText('schedule nightly-sweep (0 2 * * *) disabled after 5 failed starts'),
    ).toBeInTheDocument()
    expect(within(trouble).getByText(/toolbox:9/)).toBeInTheDocument()
    expect(within(trouble).getByText('fee-scorer refused 1 rewrite from tuner')).toBeInTheDocument()
  })

  it('never writes: everything it does is a GET', async () => {
    renderDesk()
    await screen.findByText(/Ridley invoice query/)
    const calls = (globalThis.fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls
    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) {
      const init = call[1] as RequestInit | undefined
      expect(init?.method ?? 'GET').toBe('GET')
    }
  })

  it('opens the thread through the host, rather than navigating itself', async () => {
    const onOpenSession = vi.fn()
    renderDesk({ onOpenSession })
    const asks = await screen.findByRole('region', { name: 'Asks' })
    await userEvent.click(within(asks).getByRole('button', { name: 'open thread' }))
    expect(onOpenSession).toHaveBeenCalledWith('sess-1')
  })
})

describe('since you last looked', () => {
  it('hides changes older than the stored mark', async () => {
    window.localStorage.setItem(deskLastSeenKey('acme'), String(NOW_MS))
    renderDesk()
    const changes = await screen.findByRole('region', { name: 'Changes' })
    await waitFor(() =>
      expect(
        within(changes).getByText('Nothing has changed since you last looked.'),
      ).toBeInTheDocument(),
    )
  })

  it('stores the mark per project when the operator marks them seen', async () => {
    renderDesk()
    await screen.findByText('email-reviewer rewrote email-answerer')
    await userEvent.click(screen.getByRole('button', { name: 'Mark these changes as seen' }))
    await waitFor(() => expect(readDeskLastSeen('acme')).toBeGreaterThan(0))
    // Another project's Desk is untouched — the mark is keyed, not global.
    expect(readDeskLastSeen('other')).toBe(0)
    expect(screen.getByText('Nothing has changed since you last looked.')).toBeInTheDocument()
  })

  it('treats unreadable or rubbish storage as "never looked"', () => {
    window.localStorage.setItem(deskLastSeenKey('acme'), 'yesterday')
    expect(readDeskLastSeen('acme')).toBe(0)
    writeDeskLastSeen('acme', 42)
    expect(readDeskLastSeen('acme')).toBe(42)
  })
})

describe('degraded and empty', () => {
  it('still shows the asks when the attention route is not mounted, and says why they are wordless', async () => {
    attentionStatus = 501
    renderDesk()
    const asks = await screen.findByRole('region', { name: 'Asks' })
    expect(
      await within(asks).findByText('email-answerer · awaiting_human · 2h 40m'),
    ).toBeInTheDocument()
    expect(within(asks).getByText(/does not serve/)).toBeInTheDocument()
    expect(screen.queryByText(/Ridley invoice query/)).toBeNull()
  })

  it('shows the first-run state — the two doors in — when the project has no workers', async () => {
    workers = []
    deliveries = []
    events = []
    schedules = []
    configEvents = []
    attentionRequests = []
    const onStartFromTopology = vi.fn()
    const onOpenChat = vi.fn()
    renderDesk({ onStartFromTopology, onOpenChat })
    expect(await screen.findByText('This project has no workers yet')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Start from an org chart' }))
    expect(onStartFromTopology).toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Open chat' }))
    expect(onOpenChat).toHaveBeenCalled()
    // Never a bare "nothing to show".
    expect(screen.queryByText(/nothing to show/i)).toBeNull()
  })

  it('a quiet Desk in a working project says the fleet ran and nobody needed you', async () => {
    deliveries = []
    events = []
    schedules = []
    configEvents = []
    attentionRequests = []
    renderDesk()
    expect(await screen.findByText(/nobody needed you/)).toBeInTheDocument()
    expect(screen.getByText('Nothing is waiting on you.')).toBeInTheDocument()
    expect(screen.getByText('Nothing has failed.')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// W4 — feed liveness (doc 21 §4.2)
// ---------------------------------------------------------------------------

describe('liveness', () => {
  it('hangs each stack off a role="log", not a role="feed"', async () => {
    renderDesk()
    await screen.findByText('email-reviewer rewrote email-answerer')
    const logs = screen.getAllByRole('log')
    expect(logs.map((l) => l.getAttribute('aria-label'))).toEqual(
      expect.arrayContaining(['Asks', 'Changes', 'Trouble']),
    )
    // `feed` drags in an article-navigation keyboard contract we do not
    // implement; `log` is chronological and implicitly polite.
    expect(screen.queryAllByRole('feed')).toHaveLength(0)
  })

  it('draws a labelled waterline with the already-read changes beneath it', async () => {
    // A mark AFTER the fixture's config events: everything is "already read",
    // so the line has something to divide.
    window.localStorage.setItem(deskLastSeenKey('acme'), String(NOW_MS))
    renderDesk()
    const changes = await screen.findByRole('region', { name: 'Changes' })
    // The window is still the window — the empty line stays honest…
    expect(
      within(changes).getByText('Nothing has changed since you last looked.'),
    ).toBeInTheDocument()
    // …and the divider now says WHEN, with the change underneath it.
    const rule = await within(changes).findByRole('separator')
    expect(rule.getAttribute('aria-label')).toMatch(/^New since /)
    expect(within(changes).getByText('email-reviewer rewrote email-answerer')).toBeInTheDocument()
  })

  it('carries no waterline on a first visit — a line above everything says nothing', async () => {
    renderDesk()
    const changes = await screen.findByRole('region', { name: 'Changes' })
    expect(within(changes).queryByRole('separator')).toBeNull()
  })

  it('offers the pause toggle exactly where something is actually polling', async () => {
    renderDesk()
    await screen.findByText('email-reviewer rewrote email-answerer')
    // Nothing polls by default, so there is nothing to pause and no switch.
    expect(screen.queryByRole('checkbox', { name: 'Pause live updates' })).toBeNull()
  })

  it('shows the toggle when the host asked for a live Desk', async () => {
    renderDesk({ refreshMs: 60_000 })
    await screen.findByText('email-reviewer rewrote email-answerer')
    expect(screen.getByRole('checkbox', { name: 'Pause live updates' })).toBeInTheDocument()
  })

  it('reports its ask count up, so the shell badge need not fetch again (X7)', async () => {
    const onAsksCount = vi.fn()
    renderDesk({ onAsksCount })
    await screen.findByText('email-answerer · awaiting_human · 2h 40m')
    await waitFor(() => expect(onAsksCount).toHaveBeenCalledWith(1))
  })

  it('announces an ask coarsely while its ticking headline stays aria-hidden', async () => {
    renderDesk()
    const headline = await screen.findByText('email-answerer · awaiting_human · 2h 40m')
    // The digits change every second; a screen reader must not read them out.
    expect(headline).toHaveAttribute('aria-hidden')
    expect(
      screen.getByText('email-answerer · awaiting_human · waiting about 2 hours'),
    ).toBeInTheDocument()
    // And past an hour it says so in words, not only in colour.
    expect(screen.getByText('waiting over 1h')).toBeInTheDocument()
  })
})
