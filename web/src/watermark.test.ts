// @vitest-environment jsdom
// W4 (doc 21 §4.2): the one integer, and the four things it derives.
//
// The arithmetic is pinned rather than the rendering, because the arithmetic is
// where "since you last looked" goes quietly wrong: an off-by-one at the mark
// shows the operator a change they already read as new, forever.

import { describe, it, expect, afterEach } from 'vitest'
import {
  countNewSince,
  newItemsSummary,
  partitionByWatermark,
  readWatermark,
  waterlineIndex,
  waterlineLabel,
  watermarkKey,
  writeWatermark,
} from './watermark.js'
import { deskLastSeenKey } from './useDesk.js'

const at = (ms: number) => ({ ms })
const stamp = (item: { ms: number }) => item.ms

afterEach(() => {
  globalThis.localStorage?.clear()
})

describe('the key', () => {
  it('is byte-identical to the key the Desk was already writing', () => {
    // The whole point of generalising rather than replacing: an operator's
    // existing mark must survive the change.
    expect(watermarkKey('desk', 'acme')).toBe('agentkit.desk.lastSeen.acme')
    expect(deskLastSeenKey('acme')).toBe(watermarkKey('desk', 'acme'))
  })

  it('gives each surface its own mark, and each project its own within it', () => {
    expect(watermarkKey('events', 'acme')).not.toBe(watermarkKey('desk', 'acme'))
    expect(watermarkKey('desk', 'other')).not.toBe(watermarkKey('desk', 'acme'))
  })

  it('reads rubbish, zero and the unwritable as "never looked"', () => {
    expect(readWatermark('events', 'acme')).toBe(0)
    writeWatermark('events', 'acme', 0)
    expect(readWatermark('events', 'acme')).toBe(0)
    globalThis.localStorage.setItem(watermarkKey('events', 'acme'), 'yesterday')
    expect(readWatermark('events', 'acme')).toBe(0)
    writeWatermark('events', 'acme', 1_700_000_000_000)
    expect(readWatermark('events', 'acme')).toBe(1_700_000_000_000)
  })
})

describe('the arithmetic', () => {
  const items = [at(500), at(400), at(300), at(200), at(100)]

  it('counts strictly after the mark — an item stamped AT it was on screen', () => {
    expect(countNewSince(items, stamp, 300)).toBe(2)
    expect(countNewSince(items, stamp, 500)).toBe(0)
    expect(countNewSince(items, stamp, 50)).toBe(5)
  })

  it('counts nothing when the operator has never looked', () => {
    // Everything is new to a first visit, and "5 new" above a list of exactly
    // five items says nothing at all.
    expect(countNewSince(items, stamp, 0)).toBe(0)
  })

  it('partitions into fresh and already-read, at the same boundary', () => {
    const { fresh, seen } = partitionByWatermark(items, stamp, 300)
    expect(fresh.map(stamp)).toEqual([500, 400])
    expect(seen.map(stamp)).toEqual([300, 200, 100])
    expect(partitionByWatermark(items, stamp, 0).fresh).toEqual([])
  })

  it('puts the divider between the new and the read, in a newest-first list', () => {
    expect(waterlineIndex(items, stamp, 300)).toBe(2)
    expect(waterlineIndex(items, stamp, 450)).toBe(1)
  })

  it('draws no divider when everything, or nothing, is new', () => {
    // A line at the very top or the very bottom of a list separates nothing.
    expect(waterlineIndex(items, stamp, 50)).toBe(-1)
    expect(waterlineIndex(items, stamp, 500)).toBe(-1)
    expect(waterlineIndex(items, stamp, 0)).toBe(-1)
    expect(waterlineIndex([], stamp, 300)).toBe(-1)
  })
})

describe('the label', () => {
  const now = new Date(2026, 6, 28, 16, 0, 0).getTime()

  it('names the time the operator last looked, in the console clock', () => {
    const mark = new Date(2026, 6, 28, 9, 12, 0).getTime()
    expect(waterlineLabel(mark, now)).toBe('New since 09:12')
  })

  it('says the day too when the mark is from another one', () => {
    const mark = new Date(2026, 6, 27, 9, 12, 0).getTime()
    expect(waterlineLabel(mark, now)).toBe('New since 27/7 09:12')
  })

  it('is empty when there is no mark — there is nothing to be "since"', () => {
    expect(waterlineLabel(0, now)).toBe('')
  })
})

describe('the one debounced summary the pill announces', () => {
  it('is one sentence, pluralised, with the qualifiers that matter', () => {
    expect(newItemsSummary(3, 'delivery', 'deliveries', [{ count: 1, label: 'failed' }])).toBe(
      '3 new deliveries, 1 failed',
    )
    expect(newItemsSummary(1, 'ask')).toBe('1 new ask')
    expect(newItemsSummary(2, 'change')).toBe('2 new changes')
  })

  it('drops qualifiers that are zero, and says nothing at all for zero', () => {
    expect(newItemsSummary(2, 'event', 'events', [{ count: 0, label: 'failed' }])).toBe(
      '2 new events',
    )
    expect(newItemsSummary(0, 'event')).toBe('')
  })
})
