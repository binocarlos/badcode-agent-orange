// @vitest-environment jsdom
// OC2: the org chart — the schematic renders from the read routes, the frozen
// worker is a sealed instrument, the state line is live, and the propagation
// panel chains hop by hop down the depth ruler with its caveat attached.

import React from 'react'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import OrgChartPage, { stateLine } from './OrgChartPage.js'
import { PROPAGATION_CAVEAT, PROPAGATION_NOTHING_SUBSCRIBES } from '../orgchart.js'

const NOW = 1_700_000_000

let originalFetch: typeof globalThis.fetch
let workers: Record<string, unknown>[]
let subscriptions: Record<string, unknown>[]
let schedules: Record<string, unknown>[]
let deliveries: Record<string, unknown>[]
let events: Record<string, unknown>[]

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
  workers = [
    { name: 'email-answerer', project: 'acme', description: 'answers inbound', enabled: true, max_instances: 1 },
    { name: 'email-reviewer', project: 'acme', description: 'reviews answers', enabled: true, max_instances: 2 },
    { name: 'fee-scorer', project: 'acme', description: 'scores fees', enabled: true, frozen: true, max_instances: 1 },
  ]
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
    {
      id: 's2',
      project: 'acme',
      event_type: 'worker.finished',
      filter: { worker: 'email-answerer' },
      worker: 'email-reviewer',
      max_firings_per_hour: 0,
      enabled: true,
    },
  ]
  schedules = [
    {
      id: 'sch-1',
      project: 'acme',
      worker: 'fee-scorer',
      cron: '0 9,17 * * *',
      input: 'Score.',
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
      status: 'running',
      started_at: NOW - 60,
      ended_at: 0,
      created_at: NOW - 60,
      updated_at: NOW - 60,
    },
  ]
  events = [
    {
      id: 'e1',
      project: 'acme',
      type: 'email.received',
      text: 'a question',
      envelope: envelope(),
      occurred_at: NOW - 60,
      created_at: NOW - 60,
      delivered: true,
    },
  ]

  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const u = String(url)
    const json = (v: unknown) =>
      new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
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
})

function renderChart(props: Partial<React.ComponentProps<typeof OrgChartPage>> = {}) {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <OrgChartPage projectId="acme" nowSeconds={NOW} {...props} />
    </AgentChatProvider>,
  )
}

describe('the schematic', () => {
  it('draws a plate per worker, a wire per subscription and a dial per schedule', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-email-answerer')).toBeTruthy())
    expect(screen.getByTestId('node-email-reviewer')).toBeTruthy()
    expect(screen.getByTestId('node-fee-scorer')).toBeTruthy()
    expect(screen.getByTestId('clock-sch-1')).toBeTruthy()
    expect(screen.getByTestId('pip-email.received')).toBeTruthy()
    expect(document.querySelectorAll('[data-testid^="wire-"]')).toHaveLength(2)
  })

  it('rides the event type on the wire, filter and all', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-email-reviewer')).toBeTruthy())
    expect(screen.getByText('worker.finished {worker: email-answerer}')).toBeTruthy()
  })

  it('shows the live state line, running against max', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByText('● running 1/1')).toBeTruthy())
    expect(screen.getByText('idle 0/2')).toBeTruthy()
  })

  it('draws a frozen worker as a sealed instrument — double rule plus the lock', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-fee-scorer')).toBeTruthy())
    const plate = screen.getByTestId('node-fee-scorer')
    // Two rules on the plate itself — the lock glyph's own shapes are nested
    // in their own <svg>, so `:scope >` counts the double rule and not them.
    expect(plate.querySelectorAll(':scope > rect')).toHaveLength(2)
    expect(within(plate).getByLabelText('frozen — only a human may change it')).toBeTruthy()
  })

  it('says positions are derived, never stored (§6.3)', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    expect(screen.getByText(/never stored/i)).toBeTruthy()
  })

  it('offers pan and zoom', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    expect(screen.getByLabelText('zoom in')).toBeTruthy()
    expect(screen.getByLabelText('zoom out')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Reset view' })).toBeTruthy()
  })

  it('invites rather than shrugs when the project has no workers', async () => {
    workers = []
    renderChart()
    await waitFor(() => expect(screen.getByText(/no workers yet/i)).toBeTruthy())
    expect(screen.getByText(/Nothing will run until something wakes them/i)).toBeTruthy()
    expect(screen.queryByTestId('org-chart-canvas')).toBeNull()
  })
})

describe('the conventions overlay (OC3)', () => {
  beforeEach(() => {
    workers[1].system_prompt =
      'You review answers.\nROUTE-TO: fee-scorer when the answer quotes a fee.'
  })

  it('is off by default — a heuristic never draws itself uninvited', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    expect(screen.getByLabelText('Show conventions')).toBeTruthy()
    expect(document.querySelectorAll('[data-testid^="convention-"]')).toHaveLength(0)
    expect(screen.queryByTestId('conventions-caveat')).toBeNull()
  })

  it('draws a dashed edge that quotes its prompt line and says it is not enforced', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await user.click(screen.getByLabelText('Show conventions'))

    const edge = await screen.findByTestId('convention-email-reviewer→fee-scorer')
    expect(edge.querySelector('title')?.textContent).toBe(
      '"ROUTE-TO: fee-scorer when the answer quotes a fee." — convention — written in a prompt, not enforced by the engine',
    )
    expect(edge.querySelector('line')?.getAttribute('stroke-dasharray')).toBe('4 4')
    expect(screen.getByTestId('conventions-caveat').textContent).toMatch(
      /convention — written in a prompt, not enforced by the engine/,
    )
  })

  it('says so plainly when no prompt names another worker', async () => {
    workers[1].system_prompt = 'You review answers.'
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await user.click(screen.getByLabelText('Show conventions'))
    expect(screen.getByTestId('conventions-caveat').textContent).toMatch(
      /No prompt in this project names another worker/,
    )
  })
})

describe('the propagation panel', () => {
  it('traces an entry pip hop by hop down the ruler', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())

    // The pip on the canvas, not the Chip below it — both trace, and the
    // gesture the design describes is "pick an entry pip".
    await user.click(screen.getByTestId('pip-email.received'))

    const ruler = await screen.findByTestId('depth-ruler')
    expect(within(ruler).getByText('email.received ▸ email-answerer')).toBeTruthy()
    expect(within(ruler).getByText('worker.finished ▸ email-reviewer')).toBeTruthy()
    expect(within(ruler).getByText(PROPAGATION_NOTHING_SUBSCRIBES)).toBeTruthy()
  })

  it('carries the not-modelled caveat, verbatim and exactly once', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    expect(screen.getAllByText(PROPAGATION_CAVEAT)).toHaveLength(1)
  })

  it('runs the ruler to the stop line at 8', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    const chip = screen.getAllByText('email.received')
    await userEvent.setup().click(chip[chip.length - 1])
    const ruler = await screen.findByTestId('depth-ruler')
    expect(within(ruler).getByText('8')).toBeTruthy()
    expect(within(ruler).getByText(/refuses deeper/)).toBeTruthy()
  })

  it('refuses an unparseable pasted event, in the parser own words', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await user.type(screen.getByLabelText(/paste an event/i), '{{ not json')
    await user.click(screen.getByRole('button', { name: /trace this event/i }))
    expect(await screen.findByTestId('depth-ruler')).toBeTruthy()
    expect(screen.getByTestId('paste-error').textContent).toMatch(/JSON/i)
  })

  it('traces a pasted event', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await user.click(screen.getByLabelText(/paste an event/i))
    await user.paste('{"type":"email.received"}')
    await user.click(screen.getByRole('button', { name: /trace this event/i }))
    const ruler = await screen.findByTestId('depth-ruler')
    expect(within(ruler).getByText('email.received ▸ email-answerer')).toBeTruthy()
  })
})

describe('stateLine', () => {
  const cases: [string, { enabled: boolean; frozen: boolean; maxInstances: number }, number, string][] = [
    ['running', { enabled: true, frozen: false, maxInstances: 2 }, 1, '● running 1/2'],
    ['idle', { enabled: true, frozen: false, maxInstances: 2 }, 0, 'idle 0/2'],
    ['frozen', { enabled: true, frozen: true, maxInstances: 1 }, 0, 'frozen 0/1'],
    ['disabled', { enabled: false, frozen: false, maxInstances: 1 }, 0, 'disabled'],
    ['disabled beats everything', { enabled: false, frozen: true, maxInstances: 1 }, 3, 'disabled'],
  ]
  for (const [label, node, running, want] of cases) {
    it(label, () => expect(stateLine(node, running)).toBe(want))
  }
})
