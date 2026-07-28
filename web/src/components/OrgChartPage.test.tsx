// @vitest-environment jsdom
// OC2: the org chart — the schematic renders from the read routes, the frozen
// worker is a sealed instrument, the state line is live, and the propagation
// panel chains hop by hop down the depth ruler with its caveat attached.

import React from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import OrgChartPage, {
  stateLine,
  toggleSentence,
  toggleTitle,
  wireFilter,
  wireSentence,
  WIRE_EVENT_TYPE,
} from './OrgChartPage.js'
import { PROPAGATION_CAVEAT, PROPAGATION_NOTHING_SUBSCRIBES } from '../orgchart.js'
import { FROZEN_SENTENCE, newWorkerDraft } from '../workers.js'

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
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const u = String(url)
    const json = (v: unknown) =>
      new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
    // Writes echo the stored row back, the way the routes do — so a saved
    // worker does not come back nameless and land on the chart as a ghost.
    const method = (init?.method ?? 'GET').toUpperCase()
    if (method !== 'GET') {
      const body = init?.body === undefined ? {} : JSON.parse(String(init.body))
      if (u.includes('/agent/subscriptions')) return json({ id: 'new-sub', project: 'acme', ...body })
      if (u.includes('/agent/workers')) {
        const name = decodeURIComponent(u.split('/agent/workers/')[1] ?? '')
        return json({ project: 'acme', name, ...body })
      }
      return json({})
    }
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

// ---------------------------------------------------------------------------
// OC4 — direct manipulation (§6.4, K3)
// ---------------------------------------------------------------------------

/** The requests that changed something, in order. */
function writes() {
  const calls = (globalThis.fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls
  return calls
    .map(([url, init]) => ({ url: String(url), init: (init ?? {}) as RequestInit }))
    .filter((c) => c.init.method !== undefined && c.init.method !== 'GET')
}

describe('drag to wire (OC4)', () => {
  it('proposes — it does not write — and shows the exact fields, filter and all', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-email-answerer')).toBeTruthy())

    fireEvent.pointerDown(screen.getByTestId('node-email-answerer'))
    fireEvent.pointerEnter(screen.getByTestId('node-fee-scorer'))
    expect(screen.getByTestId('ghost-wire')).toBeTruthy()
    fireEvent.pointerUp(screen.getByTestId('node-fee-scorer'))

    const card = await screen.findByTestId('wire-proposal')
    expect(within(card).getByText('When email-answerer finishes, wake fee-scorer')).toBeTruthy()
    expect(within(card).getByText('worker.finished')).toBeTruthy()
    // OC1's finding: an UNFILTERED worker.finished wire self-edges its
    // subscriber. The proposal names the source, so it cannot.
    expect(within(card).getByTestId('proposal-filter').textContent).toContain(
      '{"worker":"email-answerer"}',
    )
    expect(writes()).toHaveLength(0)
  })

  it('refuses to write until a reason is written, then POSTs it with the rationale', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-email-answerer')).toBeTruthy())

    fireEvent.pointerDown(screen.getByTestId('node-email-answerer'))
    fireEvent.pointerUp(screen.getByTestId('node-email-reviewer'))
    const card = await screen.findByTestId('wire-proposal')

    const button = within(card).getByRole('button', { name: 'Wire it up' })
    expect(button.hasAttribute('disabled')).toBe(true)
    await user.type(within(card).getByLabelText(/why are you wiring this/i), 'review every answer')
    expect(button.hasAttribute('disabled')).toBe(false)
    await user.click(button)

    await waitFor(() => expect(writes()).toHaveLength(1))
    const [write] = writes()
    expect(write.url).toContain('/agent/subscriptions')
    expect(write.init.method).toBe('POST')
    expect(JSON.parse(String(write.init.body))).toMatchObject({
      event_type: 'worker.finished',
      filter: { worker: 'email-answerer' },
      worker: 'email-reviewer',
      rationale: 'review every answer',
    })
  })

  it('can be done entirely from the keyboard, through the node menu', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-email-answerer')).toBeTruthy())

    screen.getByTestId('node-email-answerer').focus()
    await user.keyboard('{Enter}')
    await user.click(await screen.findByText('Wire email-answerer to another worker…'))

    const card = await screen.findByTestId('wire-proposal')
    await user.click(within(card).getByLabelText(/wake which worker/i))
    await user.click(await screen.findByRole('option', { name: 'fee-scorer' }))
    await user.type(within(card).getByLabelText(/why are you wiring this/i), 'score every answer')
    await user.click(within(card).getByRole('button', { name: 'Wire it up' }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String(writes()[0].init.body))).toMatchObject({ worker: 'fee-scorer' })
  })
})

describe('cutting a wire (OC4)', () => {
  it('never says undo — it says what it stops, and writes forward', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())

    await user.click(screen.getByTestId('wire-s2#email-answerer'))
    const dialog = await screen.findByTestId('cut-wire-dialog')
    expect(
      within(dialog).getByText('Stop waking email-reviewer when email-answerer finishes'),
    ).toBeTruthy()
    expect(dialog.textContent).not.toMatch(/undo/i)

    await user.type(within(dialog).getByLabelText(/why are you stopping this/i), 'reviews are noise')
    await user.click(within(dialog).getByRole('button', { name: /^Stop waking email-reviewer$/ }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0].init.method).toBe('DELETE')
    expect(writes()[0].url).toContain('/agent/subscriptions/s2')
    expect(writes()[0].url).toContain('rationale=reviews')
  })
})

describe('the node toggles (OC4)', () => {
  it('freezes through the workers save path, carrying every field', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-email-reviewer')).toBeTruthy())

    screen.getByTestId('node-email-reviewer').focus()
    await user.keyboard('{Enter}')
    await user.click(await screen.findByText('Freeze email-reviewer'))

    const dialog = await screen.findByTestId('worker-toggle-dialog')
    await user.type(within(dialog).getByLabelText(/^why\?/i), 'measurement instrument')
    await user.click(within(dialog).getByRole('button', { name: 'Freeze email-reviewer' }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    const write = writes()[0]
    expect(write.init.method).toBe('PUT')
    expect(write.url).toContain('/agent/workers/email-reviewer')
    expect(JSON.parse(String(write.init.body))).toEqual({
      description: 'reviews answers',
      system_prompt: '',
      mcp_config: {},
      image: '',
      max_instances: 2,
      briefing: null,
      enabled: true,
      frozen: true,
      rationale: 'measurement instrument',
    })
  })

  it('disables without thawing a frozen worker — PUT is create-or-replace', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-fee-scorer')).toBeTruthy())

    screen.getByTestId('node-fee-scorer').focus()
    await user.keyboard('{Enter}')
    await user.click(await screen.findByText('Disable fee-scorer'))
    const dialog = await screen.findByTestId('worker-toggle-dialog')
    await user.type(within(dialog).getByLabelText(/^why\?/i), 'paused for the quarter')
    await user.click(within(dialog).getByRole('button', { name: 'Disable fee-scorer' }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    const body = JSON.parse(String(writes()[0].init.body))
    expect(body.enabled).toBe(false)
    expect(body.frozen).toBe(true)
  })

  it('offers Unfreeze on a frozen worker, and Enable on a disabled one', async () => {
    workers[0].enabled = false
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-fee-scorer')).toBeTruthy())

    screen.getByTestId('node-fee-scorer').focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByText('Unfreeze fee-scorer')).toBeTruthy()
    await user.keyboard('{Escape}')

    screen.getByTestId('node-email-answerer').focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByText('Enable email-answerer')).toBeTruthy()
  })
})

describe('the clocks (OC4, K3)', () => {
  it('deep-link to Automation rather than editing a schedule here', async () => {
    const onOpenAutomation = vi.fn()
    const user = userEvent.setup()
    renderChart({ onOpenAutomation })
    await waitFor(() => expect(screen.getByTestId('clock-sch-1')).toBeTruthy())

    await user.click(screen.getByTestId('clock-sch-1'))
    expect(onOpenAutomation).toHaveBeenCalledWith('sch-1', 'fee-scorer')
    expect(writes()).toHaveLength(0)
  })

  it('opens from the keyboard too', async () => {
    const onOpenAutomation = vi.fn()
    const user = userEvent.setup()
    renderChart({ onOpenAutomation })
    await waitFor(() => expect(screen.getByTestId('clock-sch-1')).toBeTruthy())

    screen.getByTestId('clock-sch-1').focus()
    await user.keyboard('{Enter}')
    expect(onOpenAutomation).toHaveBeenCalledWith('sch-1', 'fee-scorer')
  })

  it('stays a dial, and says where its row lives, when the host offers no link', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('clock-sch-1')).toBeTruthy())
    const clock = screen.getByTestId('clock-sch-1')
    expect(clock.getAttribute('role')).toBeNull()
    expect(clock.querySelector('title')?.textContent).toMatch(
      /schedules are edited on the Automation page/,
    )
  })
})

describe('the canvas copy (OC4)', () => {
  it('says what a freeze means, in the same sentence the rest of the UI uses', () => {
    const worker = { ...newWorkerDraft('acme'), name: 'fee-scorer' }
    expect(toggleTitle(worker, 'frozen')).toBe('Freeze fee-scorer')
    expect(toggleSentence(worker, 'frozen')).toContain(FROZEN_SENTENCE)
    expect(toggleTitle({ ...worker, frozen: true }, 'frozen')).toBe('Unfreeze fee-scorer')
    expect(toggleTitle(worker, 'enabled')).toBe('Disable fee-scorer')
    expect(toggleTitle({ ...worker, enabled: false }, 'enabled')).toBe('Enable fee-scorer')
  })

  it('always names the source in a canvas wire filter', () => {
    expect(wireFilter('a')).toEqual({ worker: 'a' })
    expect(WIRE_EVENT_TYPE).toBe('worker.finished')
    expect(wireSentence('a', 'b')).toBe('When a finishes, wake b')
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
