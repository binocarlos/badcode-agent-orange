// W4 (doc 21 §4.2): the staging buffer — what counts as an arrival.
//
// This is the state machine behind "arrivals never insert themselves under a
// reading eye", and it is pinned here rather than through a component because
// three of its four rules are invisible when broken: a backfill that animates,
// a reconnect that re-fires every highlight, and a burst that lights up twenty
// rows all look like "it works" in a screenshot.

import { describe, it, expect } from 'vitest'
import { emptyStagedFeed, flushStagedFeed, stageFeed } from './useStagedFeed.js'
import { HIGHLIGHT_CAP } from './feedhighlight.js'

const head = { autoFlush: true, paused: false }
const away = { autoFlush: false, paused: false }

describe('the backfill', () => {
  it('shows everything and animates nothing — the first batch is not news', () => {
    const state = stageFeed(emptyStagedFeed(), ['a', 'b', 'c'], head)
    expect([...state.shown].sort()).toEqual(['a', 'b', 'c'])
    expect(state.arrivals.size).toBe(0)
    expect(state.staged).toEqual([])
    expect(state.hydrated).toBe(true)
  })
})

describe('arrivals', () => {
  const hydrated = stageFeed(emptyStagedFeed(), ['a', 'b'], head)

  it('inserts and highlights only what is genuinely new, while at the head', () => {
    const next = stageFeed(hydrated, ['c', 'a', 'b'], head)
    expect([...next.arrivals]).toEqual(['c'])
    expect(next.shown.has('c')).toBe(true)
    expect(next.staged).toEqual([])
  })

  it('fires nothing when a refetch re-delivers the same page', () => {
    // The most common live-feed bug: an SSE reconnect replays the backlog and
    // every row highlights again. Ids, not positions, are why this is a no-op —
    // and the state comes back BY IDENTITY, so React does not even re-render.
    expect(stageFeed(hydrated, ['b', 'a'], head)).toBe(hydrated)
  })

  it('stages instead of inserting when the operator is not at the head', () => {
    const next = stageFeed(hydrated, ['c', 'd', 'a', 'b'], away)
    expect(next.staged).toEqual(['c', 'd'])
    expect(next.shown.has('c')).toBe(false)
    expect(next.arrivals.size).toBe(0)
  })

  it('does not re-stage what is already staged', () => {
    const once = stageFeed(hydrated, ['c', 'a', 'b'], away)
    const twice = stageFeed(once, ['c', 'a', 'b'], away)
    expect(twice).toBe(once)
  })

  it('stages and never highlights while paused, even at the head', () => {
    const next = stageFeed(hydrated, ['c', 'a', 'b'], { autoFlush: true, paused: true })
    expect(next.staged).toEqual(['c'])
    expect(next.arrivals.size).toBe(0)
  })
})

describe('the concurrency cap', () => {
  const hydrated = stageFeed(emptyStagedFeed(), ['a'], head)
  const burst = Array.from({ length: HIGHLIGHT_CAP + 1 }, (_, i) => `n${i}`)

  it('shows a burst but highlights none of it — the boundary carries the news', () => {
    const next = stageFeed(hydrated, [...burst, 'a'], head)
    expect(next.capped).toBe(true)
    expect(next.arrivals.size).toBe(0)
    for (const id of burst) expect(next.shown.has(id)).toBe(true)
  })

  it('highlights a batch exactly at the cap', () => {
    const atCap = burst.slice(0, HIGHLIGHT_CAP)
    const next = stageFeed(hydrated, [...atCap, 'a'], head)
    expect(next.capped).toBe(false)
    expect(next.arrivals.size).toBe(HIGHLIGHT_CAP)
  })
})

describe('the flush (what the pill does)', () => {
  it('shows the staged rows and highlights them, once', () => {
    const hydrated = stageFeed(emptyStagedFeed(), ['a'], head)
    const staged = stageFeed(hydrated, ['b', 'c', 'a'], away)
    const flushed = flushStagedFeed(staged)
    expect(flushed.staged).toEqual([])
    expect([...flushed.arrivals].sort()).toEqual(['b', 'c'])
    expect(flushed.shown.has('b')).toBe(true)
    // Nothing staged ⇒ nothing to do, and the same object back.
    expect(flushStagedFeed(flushed)).toBe(flushed)
  })

  it('respects the cap on a big staged batch too', () => {
    const hydrated = stageFeed(emptyStagedFeed(), ['a'], head)
    const many = Array.from({ length: HIGHLIGHT_CAP + 3 }, (_, i) => `n${i}`)
    const flushed = flushStagedFeed(stageFeed(hydrated, [...many, 'a'], away))
    expect(flushed.capped).toBe(true)
    expect(flushed.arrivals.size).toBe(0)
  })
})
