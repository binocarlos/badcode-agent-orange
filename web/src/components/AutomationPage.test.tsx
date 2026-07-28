// @vitest-environment jsdom
// F2: the subscription + schedule editors against a stubbed /agent/subscriptions
// + /agent/schedules + /agent/events.
//
// What is worth a browser test here (the pure grammar is covered in
// schedules.test.ts / nlAssist.test.ts):
//   - creating goes POST to the collection and editing goes PUT to the id;
//   - `enabled` is always in the body, so a disabled row cannot silently return;
//   - the NL assist PROPOSES and only writes the field when the human applies;
//   - the selection lives in the URL, router-free.

import React from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import AutomationPage from './AutomationPage.js'

let originalFetch: typeof globalThis.fetch
let requests: { url: string; method: string; body: any }[] = []
let subscriptions: Record<string, unknown>[]
let schedules: Record<string, unknown>[]

const subscription = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  project: 'acme',
  event_type: 'email.received',
  filter: {},
  worker: 'email-answerer',
  max_firings_per_hour: 0,
  enabled: true,
  created_at: 1,
  updated_at: 1,
  ...extra,
})

const schedule = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  project: 'acme',
  worker: 'tweet-author',
  cron: '0 9 * * 1-5',
  input: 'Write the morning tweet.',
  enabled: true,
  created_at: 1,
  updated_at: 1,
  ...extra,
})

beforeEach(() => {
  requests = []
  subscriptions = [subscription('sub-1')]
  schedules = [schedule('sch-1')]
  window.history.replaceState(null, '', '/')
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const u = String(url)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    requests.push({ url: u, method, body })
    const json = (v: unknown, status = 200) =>
      new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } })

    if (u.includes('/agent/subscriptions/')) {
      const id = decodeURIComponent(u.split('/agent/subscriptions/')[1]!)
      if (method === 'DELETE') {
        subscriptions = subscriptions.filter((s) => s.id !== id)
        return new Response(null, { status: 204 })
      }
      const stored = { ...subscription(id), ...(body as object), id, project: 'acme' }
      subscriptions = subscriptions.map((s) => (s.id === id ? stored : s))
      return json(stored)
    }
    if (u.includes('/agent/subscriptions')) {
      if (method === 'POST') {
        const stored = { ...subscription('sub-new'), ...(body as object), id: 'sub-new', project: 'acme' }
        subscriptions.push(stored)
        return json(stored, 201)
      }
      return json({ subscriptions })
    }
    if (u.includes('/agent/schedules/')) {
      const id = decodeURIComponent(u.split('/agent/schedules/')[1]!)
      const stored = { ...schedule(id), ...(body as object), id, project: 'acme' }
      schedules = schedules.map((s) => (s.id === id ? stored : s))
      return json(stored)
    }
    if (u.includes('/agent/schedules')) {
      if (method === 'POST') {
        const stored = { ...schedule('sch-new'), ...(body as object), id: 'sch-new', project: 'acme' }
        schedules.push(stored)
        return json(stored, 201)
      }
      return json({ schedules })
    }
    if (u.includes('/agent/events')) return json({ events: [] })
    if (u.includes('/agent/deliveries')) return json({ deliveries: [] })
    return json({})
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
  window.history.replaceState(null, '', '/')
})

const writes = () => requests.filter((r) => r.method === 'POST' || r.method === 'PUT')

/** The tabs are MUI Tabs, so they are role="tab" rather than buttons. */
const openSchedules = async () =>
  userEvent.click(await screen.findByRole('tab', { name: 'Schedules' }))

function renderPage(props: Partial<React.ComponentProps<typeof AutomationPage>> = {}) {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <AutomationPage projectId="acme" {...props} />
    </AgentChatProvider>,
  )
}

// K2: a subscription edit carries a reason, so the save button stays disabled
// until the "Why?" field has one. (The schedule editor's own reason field is
// older and still optional — it is labelled "Rationale".)
const explain = (why = 'the reviewer should see every answered mail') =>
  userEvent.type(screen.getByLabelText('Why?'), why)

describe('AutomationPage — subscriptions', () => {
  it('lists them and opens one into the editor', async () => {
    renderPage()
    const row = await screen.findByText('email.received')
    await userEvent.click(row)
    expect(await screen.findByLabelText('Event type')).toHaveValue('email.received')
    expect(screen.getByLabelText('Worker')).toHaveValue('email-answerer')
  })

  it('PUTs an edit to the id, always carrying enabled', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email.received'))
    const type = await screen.findByLabelText('Event type')
    await userEvent.clear(type)
    await userEvent.type(type, 'email.*')
    await explain()
    await userEvent.click(screen.getByRole('button', { name: /save subscription/i }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    const [write] = writes()
    expect(write.method).toBe('PUT')
    expect(write.url).toContain('/agent/subscriptions/sub-1')
    expect(write.body).toMatchObject({
      event_type: 'email.*',
      enabled: true,
      rationale: 'the reviewer should see every answered mail',
    })
    // Absent means "unchanged" on the wire, so both must always be present.
    expect(Object.keys(write.body)).toEqual(
      expect.arrayContaining(['event_type', 'filter', 'worker', 'max_firings_per_hour', 'enabled']),
    )
  })

  it('POSTs a new one to the collection', async () => {
    renderPage()
    await userEvent.click(await screen.findByRole('button', { name: /new subscription/i }))
    await userEvent.type(await screen.findByLabelText('Event type'), 'invoice.received')
    await userEvent.type(screen.getByLabelText('Worker'), 'book-keeper')
    // Complete in every other respect, and still refused without a reason (K2).
    expect(screen.getByRole('button', { name: /create subscription/i })).toBeDisabled()
    await explain('invoices were piling up unread')
    await userEvent.click(screen.getByRole('button', { name: /create subscription/i }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0].method).toBe('POST')
    expect(writes()[0].url).toMatch(/\/agent\/subscriptions$/)
    expect(writes()[0].body).toMatchObject({
      event_type: 'invoice.received',
      worker: 'book-keeper',
      rationale: 'invoices were piling up unread',
    })
  })

  it('will not save a pattern the router cannot express', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email.received'))
    const type = await screen.findByLabelText('Event type')
    await userEvent.clear(type)
    await userEvent.type(type, 'e*mail')
    await explain()
    expect(await screen.findByText(/trailing wildcard/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save subscription/i })).toBeDisabled()
  })
})

describe('AutomationPage — the NL assist', () => {
  it('proposes, and writes nothing until the human applies', async () => {
    renderPage()
    await openSchedules()
    await userEvent.click(await screen.findByText('tweet-author'))

    const cron = await screen.findByLabelText('Cron')
    expect(cron).toHaveValue('0 9 * * 1-5')

    await userEvent.type(screen.getByLabelText('Describe the timing'), 'every day at 6pm')
    await userEvent.click(screen.getByRole('button', { name: /^preview$/i }))

    // The proposal is shown, in words and as the expression…
    expect(await screen.findByText('At 18:00, every day.')).toBeInTheDocument()
    expect(screen.getByText('0 18 * * *')).toBeInTheDocument()
    // …and the field is untouched until Apply, and nothing has been sent.
    expect(cron).toHaveValue('0 9 * * 1-5')
    expect(writes()).toHaveLength(0)

    await userEvent.click(screen.getByRole('button', { name: /use this cron/i }))
    expect(cron).toHaveValue('0 18 * * *')
    expect(writes()).toHaveLength(0)
  })

  it('refuses a description it cannot compile, instead of guessing', async () => {
    renderPage()
    await openSchedules()
    await userEvent.click(await screen.findByText('tweet-author'))

    await userEvent.type(screen.getByLabelText('Describe the timing'), 'whenever it feels right')
    await userEvent.click(screen.getByRole('button', { name: /^preview$/i }))

    expect(await screen.findByText(/could not find a time/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /use this cron/i })).toBeNull()
    expect(await screen.findByLabelText('Cron')).toHaveValue('0 9 * * 1-5')
  })
})

describe('AutomationPage — schedules', () => {
  it('reads the cron back in words, so a human can check it', async () => {
    renderPage()
    await openSchedules()
    expect(await screen.findByText('At 09:00, on weekdays.')).toBeInTheDocument()
  })

  it('sends the rationale with the change', async () => {
    renderPage()
    await openSchedules()
    await userEvent.click(await screen.findByText('tweet-author'))

    const cron = await screen.findByLabelText('Cron')
    await userEvent.clear(cron)
    await userEvent.type(cron, '0 10 * * 1-5')
    await userEvent.type(screen.getByLabelText('Rationale'), 'engagement peaks at 10')
    await userEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0].body).toMatchObject({
      cron: '0 10 * * 1-5',
      rationale: 'engagement peaks at 10',
    })
  })

  it('refuses a cron nickname, exactly as the engine does', async () => {
    renderPage()
    await openSchedules()
    await userEvent.click(await screen.findByText('tweet-author'))

    const cron = await screen.findByLabelText('Cron')
    await userEvent.clear(cron)
    await userEvent.type(cron, '@daily')
    expect(await screen.findByText(/nicknames like @daily are not supported/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save schedule/i })).toBeDisabled()
  })
})

describe('AutomationPage — routing without a router', () => {
  it('puts the selection in the URL and reads it back', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email.received'))
    await waitFor(() => expect(window.location.search).toBe('?subscription=sub-1'))

    window.history.replaceState(null, '', '/?schedule=sch-1')
    renderPage()
    expect(await screen.findAllByLabelText('Cron')).not.toHaveLength(0)
  })

  it('leaves the URL alone when the host controls the selection', async () => {
    renderPage({ selected: 'sub-1', onSelect: () => {} })
    expect(await screen.findByLabelText('Event type')).toHaveValue('email.received')
    expect(window.location.search).toBe('')
  })
})
