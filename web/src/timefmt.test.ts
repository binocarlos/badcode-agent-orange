// W2 / doc 21 X8: the one compact time vocabulary.
//
// Every case builds its instants with the local-time Date constructor, so the
// expectations hold in any timezone the suite happens to run in.

import { describe, it, expect } from 'vitest'
import { agoShort, calendarDaysAgo, formatClock, formatCompactTime, formatDay } from './timefmt.js'

const at = (y: number, m: number, d: number, h = 0, min = 0): number =>
  new Date(y, m - 1, d, h, min).getTime()

describe('formatCompactTime', () => {
  const now = at(2026, 7, 28, 9, 15)

  it('renders today as a bare 24-hour clock', () => {
    expect(formatCompactTime(at(2026, 7, 28, 6, 43), now)).toBe('06:43')
    expect(formatCompactTime(at(2026, 7, 28, 14, 32), now)).toBe('14:32')
  })

  it('renders this week as weekday plus clock', () => {
    // 27 Jul 2026 is a Monday.
    expect(formatCompactTime(at(2026, 7, 27, 14, 32), now)).toBe('Mon 14:32')
    expect(formatCompactTime(at(2026, 7, 22, 8, 5), now)).toBe('Wed 08:05')
  })

  it('renders anything older than a week as a date', () => {
    expect(formatCompactTime(at(2026, 7, 21, 14, 32), now)).toBe('21 Jul 2026')
    expect(formatCompactTime(at(2025, 12, 1, 0, 0), now)).toBe('1 Dec 2025')
  })

  it('counts calendar days, not 24-hour blocks', () => {
    const justAfterMidnight = at(2026, 7, 28, 0, 2)
    const justBefore = at(2026, 7, 27, 23, 58)
    expect(formatCompactTime(justAfterMidnight, justAfterMidnight)).toBe('00:02')
    expect(formatCompactTime(justBefore, justAfterMidnight)).toBe('Mon 23:58')
    expect(calendarDaysAgo(justBefore, justAfterMidnight)).toBe(1)
  })

  it('gives a future stamp its unambiguous date, never a weekday', () => {
    expect(formatCompactTime(at(2026, 9, 9, 12, 0), now)).toBe('9 Sep 2026')
    // Later the same day is still today.
    expect(formatCompactTime(at(2026, 7, 28, 23, 0), now)).toBe('23:00')
  })

  it('renders an unset timestamp as nothing at all', () => {
    expect(formatCompactTime(0, now)).toBe('')
    expect(formatCompactTime(null, now)).toBe('')
    expect(formatCompactTime(undefined, now)).toBe('')
  })

  it('exposes its parts', () => {
    expect(formatClock(at(2026, 7, 28, 6, 3))).toBe('06:03')
    expect(formatDay(at(2026, 1, 5, 6, 3))).toBe('5 Jan 2026')
  })
})

describe('agoShort', () => {
  const now = at(2026, 7, 28, 12, 0)
  const minutes = (n: number) => now - n * 60_000

  it('drops the word "ago" and coarsens as it goes', () => {
    expect(agoShort(minutes(0.5), now)).toBe('now')
    expect(agoShort(minutes(12), now)).toBe('12m')
    expect(agoShort(minutes(60 * 3), now)).toBe('3h')
    expect(agoShort(minutes(60 * 24 * 4), now)).toBe('4d')
    expect(agoShort(minutes(60 * 24 * 15), now)).toBe('2w')
  })

  it('reads a future stamp as now rather than a negative age', () => {
    expect(agoShort(now + 5_000, now)).toBe('now')
  })

  it('renders an unset timestamp as nothing at all', () => {
    expect(agoShort(0, now)).toBe('')
  })
})
