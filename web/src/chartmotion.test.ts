// W5: the chart's motion, as arithmetic (doc 21 §4.1, §5 M0–M3).
//
// What is pinned here is every rule the research fixed as a NUMBER — the ≤3
// cap, the 600px/s normalisation, the flash lifetimes, the coarsening
// thresholds, the never-`rotate=auto` snap. What is deliberately NOT pinned is
// how any of it feels; that is a screenshot, and no unit test can hold it.

import { describe, it, expect } from 'vitest'
import type { JobRow } from './events.js'
import type { OrgChartPoint, OrgChartWire, Propagation } from './orgchart.js'
import { tickIntervalMs, TICK_COARSE_AFTER_SECONDS } from './useElapsedTicker.js'
import {
  BREATHE_MIN_OPACITY,
  ELAPSED_COARSEN_AFTER_SECONDS,
  FLASH_IN_MS,
  FLASH_MIN_GAP_MS,
  FLASH_OUT_MS,
  MAX_CONCURRENT_PULSES,
  PULSE_MAX_SECONDS,
  PULSE_MIN_SECONDS,
  PULSE_SPEED_PX_PER_SEC,
  REPLAY_STAGGER_MS,
  TRACE_DIM_OPACITY,
  TRACE_HOP_STAGGER_MS,
  chevronFor,
  coalesceFlashStart,
  coarseElapsed,
  flashKindFor,
  flashLifetimeMs,
  formatElapsed,
  hopGlyph,
  offsetPathSupported,
  planArrivalPulses,
  planReplayPulses,
  pointAlong,
  polylineLength,
  polylinePath,
  pulseDurationSeconds,
  pulseEndMs,
  runningSince,
  traceDrawDelayMs,
  traceHopDepths,
  trafficLabel,
  wireForJob,
  wireTraffic,
} from './chartmotion.js'

const NOW = 1_700_000_000

function wire(over: Partial<OrgChartWire> = {}): OrgChartWire {
  return {
    id: 'w1',
    subscriptionId: 's1',
    from: null,
    fromPip: 'email.received',
    to: 'answerer',
    eventType: 'email.received',
    filter: {},
    enabled: true,
    back: false,
    points: [
      { x: 0, y: 0 },
      { x: 0, y: 100 },
    ],
    label: 'email.received',
    labelX: 0,
    labelY: 50,
    labelLane: 0,
    ...over,
  }
}

function job(over: {
  id?: string
  subscription?: string
  status?: string
  worker?: string
  eventType?: string
  producer?: string
  startedAt?: number
  createdAt?: number
} = {}): JobRow {
  const {
    id = 'd1',
    subscription = 's1',
    status = 'ok',
    worker = 'answerer',
    eventType = 'email.received',
    producer = '',
    startedAt = NOW - 60,
    createdAt = NOW - 61,
  } = over
  return {
    delivery: {
      id,
      project: 'acme',
      event_id: `e-${id}`,
      subscription_id: subscription,
      session_id: '',
      status,
      started_at: startedAt,
      ended_at: 0,
      created_at: createdAt,
      updated_at: createdAt,
    },
    event: {
      id: `e-${id}`,
      project: 'acme',
      type: eventType,
      text: '',
      envelope: {
        depth: 0,
        source: producer === '' ? 'external' : 'worker',
        worker: producer,
        session_id: '',
        interactive: false,
        attention_requested: false,
      },
      occurred_at: createdAt,
      created_at: createdAt,
      delivered: true,
    },
    subscription: null,
    worker,
    eventType,
    status,
    durationSeconds: null,
    sessionId: '',
  }
}

// ---------------------------------------------------------------------------

describe('geometry', () => {
  it('measures an orthogonal route by its segments', () => {
    const points: OrgChartPoint[] = [
      { x: 0, y: 0 },
      { x: 30, y: 0 },
      { x: 30, y: 40 },
    ]
    expect(polylineLength(points)).toBe(70)
    expect(polylineLength([{ x: 5, y: 5 }])).toBe(0)
    expect(polylineLength([])).toBe(0)
  })

  it('writes the route as an SVG path both mechanisms can read', () => {
    expect(
      polylinePath([
        { x: 0, y: 0 },
        { x: 10, y: 0 },
        { x: 10, y: 20 },
      ]),
    ).toBe('M0 0 L10 0 L10 20')
    expect(polylinePath([])).toBe('')
  })
})

describe('speed normalisation (§4.1: a constant ~600px/s)', () => {
  it('is length/600 seconds in the middle of the band', () => {
    expect(pulseDurationSeconds(300)).toBeCloseTo(0.5, 6)
    expect(pulseDurationSeconds(PULSE_SPEED_PX_PER_SEC * 0.7)).toBeCloseTo(0.7, 6)
  })

  it('clamps, so a stub wire is never a blink and a long one never a crawl', () => {
    expect(pulseDurationSeconds(10)).toBe(PULSE_MIN_SECONDS)
    expect(pulseDurationSeconds(100_000)).toBe(PULSE_MAX_SECONDS)
    expect(pulseDurationSeconds(0)).toBe(PULSE_MIN_SECONDS)
    expect(pulseDurationSeconds(Number.NaN)).toBe(PULSE_MIN_SECONDS)
  })

  it('never exceeds the research 1s ceiling', () => {
    expect(PULSE_MAX_SECONDS).toBeLessThan(1)
  })
})

describe('the chevron (§5 M0 — direction is permanent, never auto-rotated)', () => {
  it('lands on the route and snaps its angle to a right angle', () => {
    const down = chevronFor([
      { x: 40, y: 0 },
      { x: 40, y: 200 },
    ])
    expect(down).toEqual({ x: 40, y: 70, angle: 90 })

    const right = chevronFor([
      { x: 0, y: 8 },
      { x: 200, y: 8 },
    ])
    expect(right?.angle).toBe(0)

    const left = chevronFor([
      { x: 200, y: 8 },
      { x: 0, y: 8 },
    ])
    expect(left?.angle).toBe(180)
  })

  it('snaps a diagonal too — a chevron must never sit at 43°', () => {
    const diagonal = pointAlong(
      [
        { x: 0, y: 0 },
        { x: 100, y: 93 },
      ],
      0.5,
    )
    // 42.9° rounds to the nearest right angle, which is 0 — not to 43.
    expect(diagonal?.angle).toBe(0)
    expect((diagonal?.angle ?? 0) % 90).toBe(0)
  })

  it('has nothing to draw on a degenerate route', () => {
    expect(chevronFor([{ x: 1, y: 1 }])).toBeNull()
    expect(
      chevronFor([
        { x: 1, y: 1 },
        { x: 1, y: 1 },
      ]),
    ).toBeNull()
  })
})

describe('attributing a delivery to a wire', () => {
  it('takes the only candidate when a subscription draws one wire', () => {
    expect(wireForJob(job(), [wire()])?.id).toBe('w1')
  })

  it('is null when nothing on the chart carries that subscription', () => {
    expect(wireForJob(job({ subscription: 'gone' }), [wire()])).toBeNull()
  })

  it('uses the event envelope to pick between a fan-in subscription’s wires', () => {
    const wires = [
      wire({ id: 'w-a', subscriptionId: 's2', from: 'alpha', fromPip: null }),
      wire({ id: 'w-b', subscriptionId: 's2', from: 'beta', fromPip: null }),
    ]
    const picked = wireForJob(
      job({ subscription: 's2', producer: 'beta', eventType: 'worker.finished' }),
      wires,
    )
    expect(picked?.id).toBe('w-b')
  })

  it('refuses to guess when the event fell outside the page (rule 1)', () => {
    const wires = [
      wire({ id: 'w-a', subscriptionId: 's2', from: 'alpha', fromPip: null }),
      wire({ id: 'w-b', subscriptionId: 's2', from: 'beta', fromPip: null }),
    ]
    const orphan = { ...job({ subscription: 's2' }), event: null, eventType: '' }
    // Drawing a dot down an arbitrary one of the two would be motion with no
    // cause — exactly the failure §4.1 rule 1 names.
    expect(wireForJob(orphan, wires)).toBeNull()
  })
})

describe('traffic counts (§5 M0 — the still screenshot)', () => {
  it('counts per wire and keeps the NEWEST status, jobs being newest-first', () => {
    const traffic = wireTraffic(
      [wire()],
      [
        job({ id: 'd3', status: 'failed' }),
        job({ id: 'd2', status: 'ok' }),
        job({ id: 'd1', status: 'ok' }),
      ],
    )
    expect(traffic.get('w1')).toEqual({ count: 3, lastStatus: 'failed', lastDeliveryId: 'd3' })
  })

  it('says nothing about a wire nothing travelled', () => {
    expect(wireTraffic([wire()], []).size).toBe(0)
    expect(trafficLabel(0)).toBeNull()
  })

  it('renders as `↳ ×n`, for ANY count — not only past the pulse cap', () => {
    expect(trafficLabel(1)).toBe('↳ ×1')
    expect(trafficLabel(14)).toBe('↳ ×14')
  })
})

describe('flashes', () => {
  it('is fault only for a failure — a pause and a rate limit are not errors', () => {
    expect(flashKindFor('failed')).toBe('fault')
    expect(flashKindFor('ok')).toBe('ember')
    expect(flashKindFor('awaiting_human')).toBe('ember')
    expect(flashKindFor('rate_limited')).toBe('ember')
    expect(flashKindFor('running')).toBe('ember')
  })

  it('decays in 60ms in + 450ms out — and a fault NEVER decays', () => {
    expect(flashLifetimeMs('ember')).toBe(FLASH_IN_MS + FLASH_OUT_MS)
    expect(flashLifetimeMs('ember')).toBe(510)
    // §4.1: a failure is a state, not an event. Null is "forever".
    expect(flashLifetimeMs('fault')).toBeNull()
  })

  it('coalesces a burst onto a ≤3-per-second grid (WCAG 2.3.1)', () => {
    expect(coalesceFlashStart(null, 1000)).toBe(1000)
    // Three arrivals in the same millisecond: pushed onto the grid, not strobed.
    let last: number | null = null
    const starts: number[] = []
    for (let i = 0; i < 3; i += 1) {
      const at = coalesceFlashStart(last, 1000)
      starts.push(at)
      last = at
    }
    expect(starts).toEqual([1000, 1000 + FLASH_MIN_GAP_MS, 1000 + 2 * FLASH_MIN_GAP_MS])
    expect(FLASH_MIN_GAP_MS * 3).toBeGreaterThanOrEqual(1000)
  })

  it('does not delay a flash on a wire that has been quiet', () => {
    expect(coalesceFlashStart(1000, 9000)).toBe(9000)
  })
})

describe('planning arrivals (the arrival-not-render rule)', () => {
  const wires = [wire({ id: 'w1', subscriptionId: 's1' })]

  it('plans nothing for deliveries the caller has already seen', () => {
    const jobs = [job({ id: 'd2' }), job({ id: 'd1' })]
    const plan = planArrivalPulses(jobs, wires, new Set(['d1', 'd2']))
    expect(plan.pulses).toEqual([])
    expect(plan.seen).toEqual(['d2', 'd1'])
  })

  it('plans exactly one pulse for one new delivery', () => {
    const jobs = [job({ id: 'd2' }), job({ id: 'd1' })]
    const plan = planArrivalPulses(jobs, wires, new Set(['d1']))
    expect(plan.pulses).toHaveLength(1)
    expect(plan.pulses[0]).toMatchObject({ deliveryId: 'd2', wireId: 'w1', kind: 'ember' })
  })

  it('plays a burst oldest-first, so it reads in the order it happened', () => {
    const jobs = [job({ id: 'd3' }), job({ id: 'd2' }), job({ id: 'd1' })]
    const plan = planArrivalPulses(jobs, wires, new Set())
    expect(plan.pulses.map((p) => p.deliveryId)).toEqual(['d1', 'd2', 'd3'])
  })

  it('caps at three dots per wire — past that the count is the rendering', () => {
    const jobs = ['d5', 'd4', 'd3', 'd2', 'd1'].map((id) => job({ id }))
    const plan = planArrivalPulses(jobs, wires, new Set())
    expect(plan.pulses).toHaveLength(MAX_CONCURRENT_PULSES)
    // Every id is still marked seen: a delivery the cap dropped must not pulse
    // later just because another render happened.
    expect(plan.seen).toHaveLength(5)
  })

  it('counts dots already in flight against the same cap', () => {
    const jobs = [job({ id: 'd2' }), job({ id: 'd1' })]
    const plan = planArrivalPulses(jobs, wires, new Set(), new Map([['w1', 2]]))
    expect(plan.pulses).toHaveLength(1)
  })

  it('caps per wire, not per chart', () => {
    const wires2 = [
      wire({ id: 'w1', subscriptionId: 's1' }),
      wire({ id: 'w2', subscriptionId: 's2' }),
    ]
    const jobs = [
      ...['a3', 'a2', 'a1'].map((id) => job({ id, subscription: 's1' })),
      ...['b3', 'b2', 'b1'].map((id) => job({ id, subscription: 's2' })),
    ]
    expect(planArrivalPulses(jobs, wires2, new Set()).pulses).toHaveLength(6)
  })

  it('carries the fault colour for a failed delivery', () => {
    const plan = planArrivalPulses([job({ id: 'd9', status: 'failed' })], wires, new Set())
    expect(plan.pulses[0]?.kind).toBe('fault')
  })

  it('plans nothing for a delivery no wire can be named for', () => {
    const plan = planArrivalPulses([job({ id: 'd9', subscription: 'gone' })], wires, new Set())
    expect(plan.pulses).toEqual([])
    // …but it is still seen, so it cannot surface as an "arrival" later.
    expect(plan.seen).toEqual(['d9'])
  })
})

describe('the chart-open replay (§5 M1)', () => {
  const wires = [
    wire({ id: 'w1', subscriptionId: 's1' }),
    wire({ id: 'w2', subscriptionId: 's2' }),
  ]

  it('takes the last N and staggers them, oldest first', () => {
    const jobs = ['d4', 'd3', 'd2', 'd1'].map((id, i) =>
      job({ id, subscription: i % 2 === 0 ? 's1' : 's2' }),
    )
    const pulses = planReplayPulses(jobs, wires, 4)
    expect(pulses.map((p) => p.deliveryId)).toEqual(['d1', 'd2', 'd3', 'd4'])
    expect(pulses.map((p) => p.delayMs)).toEqual([0, 80, 160, 240])
    expect(REPLAY_STAGGER_MS).toBe(80)
  })

  it('honours the limit, and keys replay pulses apart from arrivals', () => {
    const jobs = ['d3', 'd2', 'd1'].map((id) => job({ id, subscription: 's2' }))
    const pulses = planReplayPulses(jobs, wires, 2)
    expect(pulses).toHaveLength(2)
    expect(pulses[0]?.key.startsWith('replay:')).toBe(true)
  })

  it('still caps at three per wire — a busy wire replays 3 and counts the rest', () => {
    const jobs = ['d9', 'd8', 'd7', 'd6', 'd5'].map((id) => job({ id, subscription: 's1' }))
    expect(planReplayPulses(jobs, wires)).toHaveLength(MAX_CONCURRENT_PULSES)
  })

  it('replays nothing from an empty history', () => {
    expect(planReplayPulses([], wires)).toEqual([])
    expect(planReplayPulses([job()], wires, 0)).toEqual([])
  })
})

describe('when a pulse is over (motion must terminate)', () => {
  it('is its stagger plus its normalised traversal', () => {
    const pulses = planReplayPulses(
      [job({ id: 'd2' }), job({ id: 'd1' })],
      [wire()],
    )
    expect(pulseEndMs(pulses[0]!, 300)).toBeCloseTo(500, 6)
    expect(pulseEndMs(pulses[1]!, 300)).toBeCloseTo(580, 6)
  })
})

describe('the offset-path feature gate', () => {
  it('is false when the browser has no CSS.supports at all', () => {
    const original = (globalThis as { CSS?: unknown }).CSS
    delete (globalThis as { CSS?: unknown }).CSS
    try {
      expect(offsetPathSupported()).toBe(false)
    } finally {
      if (original !== undefined) (globalThis as { CSS?: unknown }).CSS = original
    }
  })

  it('is false when the property parses but no document can be asked', () => {
    const original = (globalThis as { CSS?: unknown }).CSS
    ;(globalThis as { CSS?: unknown }).CSS = { supports: () => true }
    try {
      expect(offsetPathSupported(undefined)).toBe(false)
    } finally {
      if (original === undefined) delete (globalThis as { CSS?: unknown }).CSS
      else (globalThis as { CSS?: unknown }).CSS = original
    }
  })

  it('is true only when an SVG child’s own style keeps the value', () => {
    const original = (globalThis as { CSS?: unknown }).CSS
    ;(globalThis as { CSS?: unknown }).CSS = { supports: () => true }
    const fakeDoc = {
      createElementNS: () => ({ style: {} as Record<string, string> }),
    } as unknown as Document
    try {
      // The style object keeps whatever is assigned: the round trip holds.
      expect(offsetPathSupported(fakeDoc)).toBe(true)
      // A style that silently drops the property (the real failure mode on an
      // engine that supports offset-path on HTML but not on SVG children).
      const dropping = {
        createElementNS: () => ({
          style: Object.defineProperty({}, 'offsetPath', {
            get: () => '',
            set: () => {},
          }),
        }),
      } as unknown as Document
      expect(offsetPathSupported(dropping)).toBe(false)
    } finally {
      if (original === undefined) delete (globalThis as { CSS?: unknown }).CSS
      else (globalThis as { CSS?: unknown }).CSS = original
    }
  })
})

describe('the ticking status line (§5 M2)', () => {
  it('ticks per second, then coarsens to a minute', () => {
    // The cadence policy lives in useElapsedTicker (one authority); this pins
    // that the chart's FORMATTING threshold still agrees with it.
    expect(tickIntervalMs('running', 0)).toBe(1000)
    expect(tickIntervalMs('running', ELAPSED_COARSEN_AFTER_SECONDS - 1)).toBe(1000)
    expect(tickIntervalMs('running', ELAPSED_COARSEN_AFTER_SECONDS)).toBe(60_000)
    expect(tickIntervalMs('running', 9999)).toBe(60_000)
    expect(ELAPSED_COARSEN_AFTER_SECONDS).toBe(TICK_COARSE_AFTER_SECONDS)
  })

  it('shows seconds while they are still a decision, minutes after', () => {
    expect(formatElapsed(0)).toBe('0s')
    expect(formatElapsed(42)).toBe('42s')
    expect(formatElapsed(65)).toBe('1m 05s')
    expect(formatElapsed(119)).toBe('1m 59s')
    expect(formatElapsed(120)).toBe('2m')
    expect(formatElapsed(252)).toBe('4m')
    expect(formatElapsed(3600)).toBe('1h 00m')
    expect(formatElapsed(4320)).toBe('1h 12m')
  })

  it('reads a clock skew as 0s, never -1s', () => {
    expect(formatElapsed(-30)).toBe('0s')
    expect(coarseElapsed(-30)).toBe('under a minute')
  })

  it('gives a screen reader minutes, never a new number every second', () => {
    expect(coarseElapsed(5)).toBe('under a minute')
    expect(coarseElapsed(60)).toBe('1 minute')
    expect(coarseElapsed(150)).toBe('2 minutes')
    expect(coarseElapsed(3600)).toBe('1 hour')
    expect(coarseElapsed(7300)).toBe('2 hours')
    // The whole point: a second passing does not change what is announced.
    expect(coarseElapsed(200)).toBe(coarseElapsed(201))
  })
})

describe('how long a worker has been busy', () => {
  it('takes the OLDEST running delivery, not the newest', () => {
    const since = runningSince([
      job({ id: 'd2', status: 'running', worker: 'answerer', startedAt: NOW - 30 }),
      job({ id: 'd1', status: 'running', worker: 'answerer', startedAt: NOW - 600 }),
    ])
    expect(since.get('answerer')).toBe(NOW - 600)
  })

  it('ignores anything that is not running, and anything with no clock', () => {
    const since = runningSince([
      job({ id: 'd1', status: 'ok', worker: 'answerer' }),
      job({ id: 'd2', status: 'awaiting_human', worker: 'reviewer' }),
      job({ id: 'd3', status: 'running', worker: '', startedAt: NOW - 5 }),
      job({ id: 'd4', status: 'running', worker: 'scorer', startedAt: 0, createdAt: 0 }),
    ])
    expect(since.size).toBe(0)
  })

  it('falls back to created_at when a delivery never started', () => {
    const since = runningSince([
      job({ id: 'd1', status: 'running', worker: 'scorer', startedAt: 0, createdAt: NOW - 90 }),
    ])
    expect(since.get('scorer')).toBe(NOW - 90)
  })
})

describe('the trace (§5 M3)', () => {
  const propagation: Propagation = {
    hops: [
      {
        depth: 0,
        wakes: [
          { depth: 0, eventType: 'email.received', from: '', worker: 'answerer', subscriptionId: 's1' },
        ],
      },
      {
        depth: 1,
        wakes: [
          { depth: 1, eventType: 'worker.finished', from: 'answerer', worker: 'reviewer', subscriptionId: 's2' },
          // A second wake on a subscription already lit at depth 1.
          { depth: 1, eventType: 'worker.finished', from: 'answerer', worker: 'reviewer', subscriptionId: 's2' },
        ],
      },
      {
        depth: 2,
        wakes: [
          { depth: 2, eventType: 'worker.finished', from: 'reviewer', worker: 'answerer', subscriptionId: 's1' },
        ],
      },
    ],
    stopped: false,
    maxDepth: 8,
  }

  it('keeps each subscription’s FIRST depth — a loop wears its earliest number', () => {
    const depths = traceHopDepths(propagation)
    expect(depths.get('s1')).toBe(0)
    expect(depths.get('s2')).toBe(1)
    expect(depths.size).toBe(2)
  })

  it('has nothing to say without a trace', () => {
    expect(traceHopDepths(null).size).toBe(0)
  })

  it('numbers hops from ① — depth 0 is the event that arrived', () => {
    expect(hopGlyph(1)).toBe('①')
    expect(hopGlyph(2)).toBe('②')
    expect(hopGlyph(9)).toBe('⑨')
    // Past the closed glyph set, a number rather than a wrong circle.
    expect(hopGlyph(10)).toBe('(10)')
  })

  it('staggers the draw-in by ~120ms per hop, and never backwards', () => {
    expect(traceDrawDelayMs(0)).toBe(0)
    expect(traceDrawDelayMs(1)).toBe(TRACE_HOP_STAGGER_MS)
    expect(traceDrawDelayMs(3)).toBe(3 * TRACE_HOP_STAGGER_MS)
    expect(traceDrawDelayMs(-1)).toBe(0)
    expect(TRACE_HOP_STAGGER_MS).toBe(120)
  })
})

describe('the constants the research fixed', () => {
  it('are the numbers doc 21 §4.1 names', () => {
    expect(MAX_CONCURRENT_PULSES).toBe(3)
    expect(PULSE_SPEED_PX_PER_SEC).toBe(600)
    expect(FLASH_IN_MS).toBe(60)
    expect(FLASH_OUT_MS).toBe(450)
    expect(TRACE_DIM_OPACITY).toBe(0.22)
  })

  it('keep the breathe readable — it carries text', () => {
    expect(BREATHE_MIN_OPACITY).toBeGreaterThanOrEqual(0.5)
    expect(BREATHE_MIN_OPACITY).toBeLessThan(1)
  })
})
