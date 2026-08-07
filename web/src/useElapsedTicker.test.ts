// W4 (doc 21 §4.2): which durations are allowed to tick, and how fast.
//
// The rule the research left standing is a policy, not a rendering detail —
// "tick only when knowing the elapsed IS the operator's decision input" — so it
// is a pure function and it is pinned. The specific thing this protects against
// is the bug §3 filed on this console: a ticking clock on a finished job, and a
// still one on a running job, both of which read as "nothing is happening".

import { describe, it, expect } from 'vitest'
import {
  ageEscalation,
  coarseAgeLabel,
  ESCALATE_AMBER_SECONDS,
  ESCALATE_FAULT_SECONDS,
  TICK_COARSE_AFTER_SECONDS,
  TICK_COARSE_MS,
  TICK_FAST_MS,
  tickIntervalForRows,
  tickIntervalMs,
} from './useElapsedTicker.js'

describe('what ticks', () => {
  it('ticks a young running job per second, then coarsens to a minute', () => {
    expect(tickIntervalMs('running', 5)).toBe(TICK_FAST_MS)
    expect(tickIntervalMs('running', TICK_COARSE_AFTER_SECONDS - 1)).toBe(TICK_FAST_MS)
    // Past two minutes nobody is reading the seconds.
    expect(tickIntervalMs('running', TICK_COARSE_AFTER_SECONDS)).toBe(TICK_COARSE_MS)
    expect(tickIntervalMs('running', 60 * 60)).toBe(TICK_COARSE_MS)
  })

  it('ticks an ask prominently, and forever — the number is the call to action', () => {
    expect(tickIntervalMs('awaiting_human', 5)).toBe(TICK_FAST_MS)
    expect(tickIntervalMs('awaiting_human', 10 * 60 * 60)).toBe(TICK_FAST_MS)
  })

  it('never ticks a finished job, or one that has not started', () => {
    // A ticking clock on a finished thing is a bug, not a flourish.
    expect(tickIntervalMs('ok', 10)).toBe(0)
    expect(tickIntervalMs('failed', 10)).toBe(0)
    expect(tickIntervalMs('rate_limited', 10)).toBe(0)
    expect(tickIntervalMs('pending', 10)).toBe(0)
    expect(tickIntervalMs('something-else', 10)).toBe(0)
  })
})

describe('one interval per surface', () => {
  it('takes the fastest cadence any row asks for', () => {
    expect(
      tickIntervalForRows([
        { status: 'ok', elapsedSeconds: 4 },
        { status: 'running', elapsedSeconds: 900 },
        { status: 'awaiting_human', elapsedSeconds: 30 },
      ]),
    ).toBe(TICK_FAST_MS)
  })

  it('coarsens when every live row has coarsened', () => {
    expect(
      tickIntervalForRows([
        { status: 'running', elapsedSeconds: 900 },
        { status: 'ok', elapsedSeconds: 4 },
      ]),
    ).toBe(TICK_COARSE_MS)
  })

  it('holds NO timer at all when nothing on the page is moving', () => {
    // The property that makes "one shared ticker" cheap: a finished page is a
    // page with no interval, not a page with a quiet one.
    expect(tickIntervalForRows([{ status: 'ok', elapsedSeconds: 4 }])).toBe(0)
    expect(tickIntervalForRows([])).toBe(0)
  })
})

describe('escalation', () => {
  it('goes amber at an hour and fault at four — an SLA on the operator', () => {
    expect(ageEscalation(0)).toBe('none')
    expect(ageEscalation(ESCALATE_AMBER_SECONDS - 1)).toBe('none')
    expect(ageEscalation(ESCALATE_AMBER_SECONDS)).toBe('amber')
    expect(ageEscalation(ESCALATE_FAULT_SECONDS - 1)).toBe('amber')
    expect(ageEscalation(ESCALATE_FAULT_SECONDS)).toBe('fault')
  })
})

describe('the coarse label a screen reader hears', () => {
  it('is vague enough to stay true for the minutes between refreshes', () => {
    expect(coarseAgeLabel(30)).toBe('less than a minute')
    expect(coarseAgeLabel(60)).toBe('about 1 minute')
    expect(coarseAgeLabel(4 * 60 + 30)).toBe('about 4 minutes')
    expect(coarseAgeLabel(60 * 60)).toBe('about 1 hour')
    expect(coarseAgeLabel(5 * 60 * 60)).toBe('about 5 hours')
    expect(coarseAgeLabel(50 * 60 * 60)).toBe('about 2 days')
  })
})
