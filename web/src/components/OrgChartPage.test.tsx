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
  HALTED_CLOCK_SENTENCE,
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
let workersStatus: number

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
  workersStatus = 200
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
    if (u.includes('/agent/workers')) {
      if (workersStatus !== 200) {
        return new Response('workers: connection refused', { status: workersStatus })
      }
      return json({ workers })
    }
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

  // -------------------------------------------------------------------------
  // doc 21 §2 — the defects the populated walkthrough found (W1)
  // -------------------------------------------------------------------------

  it('draws the lock in SVG user units, never as a nested <svg> (X1)', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-fee-scorer')).toBeTruthy())
    const plate = screen.getByTestId('node-fee-scorer')
    // The bug: `SpineGlyph` is a `Box component="svg"` sized by CSS, and CSS
    // sizing does not apply to an <svg> nested inside an <svg> — it rendered
    // at the 300×150 default, a giant clipped disc in the canvas corner.
    expect(plate.querySelectorAll('svg')).toHaveLength(0)
    const lock = within(plate).getByLabelText('frozen — only a human may change it')
    expect(lock.tagName.toLowerCase()).toBe('g')
    expect(lock.getAttribute('transform')).toMatch(/scale\(/)
    expect(lock.querySelector('path')).toBeTruthy()
  })

  it('drops a wired pip caption — the wire already carries the same text (X4)', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('pip-email.received')).toBeTruthy())
    expect(screen.queryByTestId('pip-caption-email.received')).toBeNull()
    // The wire leaving it names the event, once.
    expect(screen.getByTestId('label-s1#@email.received').textContent).toBe('email.received')
  })

  it('keeps the caption on a pip nothing subscribes to (X4)', async () => {
    subscriptions = []
    renderChart()
    await waitFor(() => expect(screen.getByTestId('pip-email.received')).toBeTruthy())
    expect(screen.getByTestId('pip-caption-email.received').textContent).toBe('email.received')
  })

  it('draws every riding label in one layer above the plates (X2)', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-labels')).toBeTruthy())
    const labels = screen.getByTestId('org-chart-labels')
    // Last child of the transform group: wires run UNDER the plates, so a
    // label painted with its wire disappears beneath the plate it points at.
    expect(labels.parentElement?.lastElementChild).toBe(labels)
    // One riding label per wire. (W5 puts its `↳ ×n` traffic counts in this
    // same layer, for the same reason the labels are here — so the pin is on
    // the labels themselves rather than on every text node in the group.)
    expect(labels.querySelectorAll('[data-testid^="label-"]')).toHaveLength(2)
    // The label layer is not a second wire — the wire count is unchanged.
    expect(document.querySelectorAll('[data-testid^="wire-"]')).toHaveLength(2)
  })

  it('marks a plate holding an unanswered ask, in rose, on the state line (X9)', async () => {
    deliveries.push({
      id: 'd2',
      project: 'acme',
      event_id: 'e1',
      subscription_id: 's2',
      session_id: 'sess-2',
      status: 'awaiting_human',
      started_at: NOW - 600,
      ended_at: 0,
      created_at: NOW - 600,
      updated_at: NOW - 600,
    })
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-email-reviewer')).toBeTruthy())
    const plate = screen.getByTestId('node-email-reviewer')
    // It read `idle 0/2` before: a parked ask was invisible on the chart.
    expect(within(plate).getByText('◆ awaiting human 1')).toBeTruthy()
    expect(within(plate).getByLabelText('email-reviewer is waiting for a human')).toBeTruthy()
    expect(within(screen.getByTestId('node-email-answerer')).queryByLabelText(
      'email-answerer is waiting for a human',
    )).toBeNull()
  })

  it('crosses out a clock the five-strike rule killed (X10)', async () => {
    schedules[0].enabled = false
    schedules[0].provision_failures = 5
    renderChart()
    await waitFor(() => expect(screen.getByTestId('clock-sch-1')).toBeTruthy())
    const clock = screen.getByTestId('clock-sch-1')
    expect(within(clock).getByLabelText("fee-scorer's clock is dead")).toBeTruthy()
    expect(clock.querySelector('title')?.textContent).toContain(HALTED_CLOCK_SENTENCE)
  })

  it('leaves a merely-disabled clock a clock, not a corpse (X10)', async () => {
    schedules[0].enabled = false
    schedules[0].provision_failures = 2
    renderChart()
    await waitFor(() => expect(screen.getByTestId('clock-sch-1')).toBeTruthy())
    const clock = screen.getByTestId('clock-sch-1')
    expect(within(clock).queryByLabelText("fee-scorer's clock is dead")).toBeNull()
    expect(clock.querySelector('title')?.textContent).not.toContain(HALTED_CLOCK_SENTENCE)
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

  // RD28's shape, one screen over: a failed list leaves the initial [], and
  // "this project has no workers yet" over a project that has three is the same
  // comforting falsehood.
  it('does not claim the project is empty when the worker list failed', async () => {
    workersStatus = 500
    renderChart()
    expect(await screen.findByText(/workers: connection refused/)).toBeInTheDocument()
    expect(screen.queryByText(/no workers yet/i)).toBeNull()
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

  it('captions the dashed edge in its own lane, above the plates (X2)', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await user.click(screen.getByLabelText('Show conventions'))

    const label = await screen.findByTestId('label-convention-email-reviewer→fee-scorer')
    expect(label.textContent).toBe('ROUTE-TO ⇢')
    // In the label layer, not in the edge's own group: drawn with the edge it
    // slid under the plate it points at.
    expect(label.parentElement?.getAttribute('data-testid')).toBe('org-chart-labels')
    expect(
      screen.getByTestId('convention-email-reviewer→fee-scorer').querySelector('text'),
    ).toBeNull()
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
  type Node = { enabled: boolean; frozen: boolean; maxInstances: number }
  const cases: [string, Node, number, number, string][] = [
    ['running', { enabled: true, frozen: false, maxInstances: 2 }, 1, 0, '● running 1/2'],
    ['idle', { enabled: true, frozen: false, maxInstances: 2 }, 0, 0, 'idle 0/2'],
    ['frozen', { enabled: true, frozen: true, maxInstances: 1 }, 0, 0, 'frozen 0/1'],
    ['disabled', { enabled: false, frozen: false, maxInstances: 1 }, 0, 0, 'disabled'],
    ['disabled beats everything', { enabled: false, frozen: true, maxInstances: 1 }, 3, 2, 'disabled'],
    // X9: an unanswered ask outranks idle and frozen, and is ADDITIVE with
    // running — both are true, and the operator needs both.
    ['awaiting', { enabled: true, frozen: false, maxInstances: 1 }, 0, 1, '◆ awaiting human 1'],
    ['awaiting beats frozen', { enabled: true, frozen: true, maxInstances: 1 }, 0, 2, '◆ awaiting human 2'],
    [
      'running and awaiting at once',
      { enabled: true, frozen: false, maxInstances: 2 },
      1,
      1,
      '● running 1/2 · ◆ awaiting human 1',
    ],
  ]
  for (const [label, node, running, awaiting, want] of cases) {
    it(label, () => expect(stateLine(node, running, awaiting)).toBe(want))
  }

  it('still reads as it did when nothing is parked', () => {
    expect(stateLine({ enabled: true, frozen: false, maxInstances: 2 }, 0)).toBe('idle 0/2')
  })
})

// ---------------------------------------------------------------------------
// W5 — chart motion (doc 21 §4.1, §5 M0–M3)
//
// What is asserted here is WHAT ANIMATES WHEN, in both reduced-motion
// branches, and — more importantly — what is drawn when nothing animates at
// all. The feel is a screenshot, and no jsdom test can hold it: jsdom has no
// SMIL clock, so `beginElement()` is a no-op here by design, and the dot's
// presence is what is under test rather than its travel.
// ---------------------------------------------------------------------------

/** Mock the one media query the gate reads (the pattern `useReducedMotion`'s
 *  own callers established — jsdom has no reduced-motion support of its own). */
function setReducedMotion(reduced: boolean) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: reduced && query.includes('reduce'),
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}

const testIds = (prefix: string): string[] =>
  Array.from(document.querySelectorAll(`[data-testid^="${prefix}"]`)).map(
    (el) => el.getAttribute('data-testid') ?? '',
  )

let originalMatchMedia: typeof window.matchMedia
beforeEach(() => {
  originalMatchMedia = window.matchMedia
})
afterEach(() => {
  window.matchMedia = originalMatchMedia
})

describe('M0 — the still-screenshot floor (chevrons and counts)', () => {
  it('draws a direction chevron on every wire, always', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    expect(testIds('chevron-')).toHaveLength(2)
  })

  it('draws the chevrons under reduced motion too — direction is not motion', async () => {
    setReducedMotion(true)
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    expect(testIds('chevron-')).toHaveLength(2)
  })

  it('never rotates a chevron off a right angle', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    const chevrons = document.querySelectorAll('[data-testid^="chevron-"]')
    expect(chevrons.length).toBeGreaterThan(0)
    for (const chevron of Array.from(chevrons)) {
      const transform = chevron.getAttribute('transform') ?? ''
      const angle = Number(/rotate\((-?\d+(?:\.\d+)?)\)/.exec(transform)?.[1] ?? 'NaN')
      expect(Number.isFinite(angle)).toBe(true)
      expect(angle % 90).toBe(0)
    }
  })

  it('rides the `↳ ×n` traffic count on the wire that carried it', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    // One delivery, on s1 (the pip → email-answerer wire). The other wire
    // carried nothing and says nothing.
    expect(screen.getByTestId('traffic-s1#@email.received').textContent).toBe('↳ ×1')
    expect(testIds('traffic-')).toHaveLength(1)
  })

  it('counts the traffic under reduced motion — this IS the reduced rendering', async () => {
    setReducedMotion(true)
    deliveries.push({ ...deliveries[0], id: 'd2', status: 'ok' })
    renderChart()
    await waitFor(() => expect(screen.getByTestId('traffic-s1#@email.received')).toBeTruthy())
    expect(screen.getByTestId('traffic-s1#@email.received').textContent).toBe('↳ ×2')
  })

  it('keeps the riding label a single text node, count and all', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    // The count is its OWN element: appending to the label would silently
    // change what every whole-string match on it sees.
    const label = screen.getByTestId('label-s1#@email.received')
    expect(label.childNodes).toHaveLength(1)
    expect(label.textContent).toBe('email.received')
  })
})

describe('M1 — the pulse', () => {
  it('replays the history on chart open, keyed as a replay', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await waitFor(() => expect(testIds('pulse-')).toHaveLength(1))
    expect(testIds('pulse-')[0]).toBe('pulse-replay:d1@s1#@email.received')
  })

  it('pulses ONCE for a delivery that arrives, and never for one already seen', async () => {
    const { rerender } = render(
      <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
        <OrgChartPage projectId="acme" nowSeconds={NOW} limit={50} />
      </AgentChatProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await waitFor(() => expect(testIds('pulse-')).toHaveLength(1))

    // A refetch that brings back ONE new delivery. `limit` is the events
    // hook's own refetch key, so this is a real reload through the real hook.
    deliveries.unshift({
      id: 'd2',
      project: 'acme',
      event_id: 'e1',
      subscription_id: 's2',
      session_id: 'sess-2',
      status: 'ok',
      started_at: NOW - 5,
      ended_at: NOW - 2,
      created_at: NOW - 5,
      updated_at: NOW - 2,
    })
    rerender(
      <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
        <OrgChartPage projectId="acme" nowSeconds={NOW} limit={51} />
      </AgentChatProvider>,
    )

    // Exactly one ARRIVAL pulse — for d2, down d2's wire. d1 was seen on the
    // first hydrated pass and does not pulse again just because the component
    // rendered (the arrival-not-render rule).
    await waitFor(() =>
      expect(testIds('pulse-').filter((id) => !id.includes('replay:'))).toEqual([
        'pulse-d2@s2#email-answerer',
      ]),
    )
  })

  it('under reduced motion there is no dot at all — the flash and the count carry it', async () => {
    setReducedMotion(true)
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await waitFor(() => expect(screen.getByTestId('traffic-s1#@email.received')).toBeTruthy())
    expect(testIds('pulse-')).toHaveLength(0)
    expect(screen.getByTestId('org-chart-pulses').childNodes).toHaveLength(0)
  })

  it('falls back to SMIL when offset-path is not available on an SVG child', async () => {
    // jsdom is exactly that browser, which is why the fallback is the branch
    // the suite exercises: it is the one that has to work everywhere.
    renderChart()
    await waitFor(() => expect(testIds('pulse-')).toHaveLength(1))
    const dot = screen.getByTestId('pulse-replay:d1@s1#@email.received')
    expect(dot.getAttribute('data-motion')).toBe('smil')
    const motion = dot.querySelector('animateMotion')
    expect(motion?.getAttribute('begin')).toBe('indefinite')
    // Never `rotate="auto"`: on an orthogonal patchbay it snaps 90° at every
    // corner and reads as a glitch.
    expect(motion?.getAttribute('rotate')).toBe('0')
    expect(dot.getAttribute('aria-hidden')).toBe('true')
  })
})

describe('a failure is a state, not an event', () => {
  it('paints the wire fault and leaves it there', async () => {
    deliveries.push({
      id: 'd-fail',
      project: 'acme',
      event_id: 'e1',
      subscription_id: 's2',
      session_id: '',
      status: 'failed',
      started_at: NOW - 300,
      ended_at: NOW - 299,
      created_at: NOW - 300,
      updated_at: NOW - 299,
    })
    renderChart()
    await waitFor(() => expect(screen.getByTestId('wire-s2#email-answerer')).toBeTruthy())
    const stroke = () =>
      screen
        .getByTestId('wire-s2#email-answerer')
        .querySelector('polyline')
        ?.getAttribute('stroke')
    expect(stroke()).toBe('#8F2B2B')
    // Long past any flash lifetime (60ms in + 450ms out): still fault. The
    // colour comes from the DATA, not from a timer, so it survives a reload
    // and a still screenshot.
    await new Promise((resolve) => setTimeout(resolve, 600))
    expect(stroke()).toBe('#8F2B2B')
  })

  it('leaves a wire whose newest delivery succeeded alone', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('wire-s2#email-answerer')).toBeTruthy())
    expect(
      screen
        .getByTestId('wire-s2#email-answerer')
        .querySelector('polyline')
        ?.getAttribute('stroke'),
    ).not.toBe('#8F2B2B')
  })
})

describe('M2 — running and waiting', () => {
  it('breathes the running plate’s status line, slowly and opacity-only', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByText('● running 1/1')).toBeTruthy())
    const line = screen.getByTestId('state-email-answerer')
    expect(line.getAttribute('style')).toContain('org-chart-breathe')
    expect(line.getAttribute('style')).toContain('2000ms')
    // Opacity only: no scale, no glow, no filter (§4.1 — filters kill the
    // schematic and cost frames).
    const keyframes = document.querySelector('svg style')?.textContent ?? ''
    expect(keyframes).toContain('opacity')
    expect(keyframes).not.toContain('scale')
    expect(keyframes).not.toContain('filter')
  })

  it('does not breathe an idle plate — motion must be caused, and must stop', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByText('idle 0/2')).toBeTruthy())
    expect(screen.getByTestId('state-email-reviewer').getAttribute('style')).toBeNull()
  })

  it('does not breathe under reduced motion — the text carries it', async () => {
    setReducedMotion(true)
    renderChart()
    await waitFor(() => expect(screen.getByText('● running 1/1')).toBeTruthy())
    expect(screen.getByTestId('state-email-answerer').getAttribute('style')).toBeNull()
  })

  it('ticks the elapsed beside the state line, with a coarse label for a reader', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('elapsed-email-answerer')).toBeTruthy())
    const elapsed = screen.getByTestId('elapsed-email-answerer')
    // d1 started 60s before NOW.
    expect(elapsed.textContent).toBe('1m 00s')
    expect(elapsed.getAttribute('aria-label')).toBe(
      'email-answerer has been running for 1 minute',
    )
  })

  it('says nothing about elapsed on a plate with nothing running', async () => {
    renderChart()
    await waitFor(() => expect(screen.getByTestId('node-email-reviewer')).toBeTruthy())
    expect(screen.queryByTestId('elapsed-email-reviewer')).toBeNull()
  })

  it('renders the elapsed under reduced motion too — it is text, not motion', async () => {
    setReducedMotion(true)
    renderChart()
    await waitFor(() => expect(screen.getByTestId('elapsed-email-answerer')).toBeTruthy())
    expect(screen.getByTestId('elapsed-email-answerer').textContent).toBe('1m 00s')
  })
})

describe('M3 — the trace', () => {
  it('numbers the hops and dims everything the event does not reach', async () => {
    const user = userEvent.setup()
    // A worker nothing in this trace wakes, so there is something to dim.
    subscriptions.push({
      id: 's3',
      project: 'acme',
      event_type: 'invoice.received',
      filter: {},
      worker: 'fee-scorer',
      max_firings_per_hour: 0,
      enabled: true,
    })
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await user.click(screen.getByTestId('pip-email.received'))

    // Hop numbers make the trace reconstructible from a screenshot, which
    // motion never allows. Depth 0 is the event that arrived, so ① is the
    // first wake.
    await waitFor(() => expect(screen.getByTestId('hop-s1#@email.received')).toBeTruthy())
    expect(screen.getByTestId('hop-s1#@email.received').textContent).toBe('①')
    expect(screen.getByTestId('hop-s2#email-answerer').textContent).toBe('②')
    expect(screen.queryByTestId('hop-s3#@invoice.received')).toBeNull()

    expect(screen.getByTestId('wire-s3#@invoice.received').getAttribute('opacity')).toBe('0.22')
    expect(screen.getByTestId('node-fee-scorer').getAttribute('opacity')).toBe('0.22')
    expect(screen.getByTestId('node-email-answerer').getAttribute('opacity')).toBe('1')
  })

  it('staggers the draw-in by hop', async () => {
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await user.click(screen.getByTestId('pip-email.received'))

    await waitFor(() => expect(screen.getByTestId('hop-s2#email-answerer')).toBeTruthy())
    const hop2 = screen
      .getByTestId('wire-s2#email-answerer')
      .querySelector('polyline')
      ?.getAttribute('style')
    expect(hop2).toContain('org-chart-draw')
    expect(hop2).toContain('120ms')
  })

  it('drops ONLY the draw-in under reduced motion, losing no information', async () => {
    setReducedMotion(true)
    const user = userEvent.setup()
    renderChart()
    await waitFor(() => expect(screen.getByTestId('org-chart-canvas')).toBeTruthy())
    await user.click(screen.getByTestId('pip-email.received'))

    await waitFor(() => expect(screen.getByTestId('hop-s1#@email.received')).toBeTruthy())
    // The hop numbers and the dim — the whole answer — are still here.
    expect(screen.getByTestId('hop-s2#email-answerer').textContent).toBe('②')
    const style =
      screen
        .getByTestId('wire-s2#email-answerer')
        .querySelector('polyline')
        ?.getAttribute('style') ?? ''
    expect(style).not.toContain('org-chart-draw')
  })
})
