// Schedules — the pure module behind the schedule editor (F2).
//
// The cron parser here is a second implementation of a rule the engine owns, so
// most of these tests are really about ONE property: it accepts exactly what
// go/agentdb/schedules.go:ParseCron accepts, and refuses exactly what it
// refuses. The rest is the description — the thing that turns "0 9 * * 1-5"
// into something a human can check.

import { describe, it, expect } from 'vitest'
import {
  buildScheduleSearch,
  coerceSchedule,
  describeCron,
  newScheduleDraft,
  scheduleBody,
  scheduleFromSearch,
  SCHEDULE_ENDPOINTS,
  validateCron,
  validateSchedule,
  type Schedule,
} from './schedules.js'

const schedule = (over: Partial<Schedule> = {}): Schedule =>
  coerceSchedule({
    id: 'sch-1',
    project: 'acme',
    worker: 'tweet-author',
    cron: '0 9 * * 1-5',
    input: 'Write the morning tweet.',
    enabled: true,
    created_at: 1,
    updated_at: 2,
    ...over,
  })

describe('the wire shape', () => {
  it('defaults enabled to true, because a zero-valued schedule would never fire', () => {
    expect(newScheduleDraft('acme').enabled).toBe(true)
    expect(coerceSchedule({ id: 'x' }).enabled).toBe(true)
    expect(coerceSchedule({ id: 'x', enabled: false }).enabled).toBe(false)
  })

  it('never binds undefined to a controlled input', () => {
    const s = coerceSchedule(null, 'acme')
    expect(s).toEqual(newScheduleDraft('acme'))
    expect(coerceSchedule({ cron: 42 }).cron).toBe('')
  })

  it('sends only the fields the server owns, and omits an empty rationale', () => {
    expect(scheduleBody(schedule())).toEqual({
      worker: 'tweet-author',
      cron: '0 9 * * 1-5',
      input: 'Write the morning tweet.',
      enabled: true,
    })
    expect(scheduleBody(schedule(), '  moved an hour later  ')).toMatchObject({
      rationale: 'moved an hour later',
    })
    expect(scheduleBody(schedule(), '   ')).not.toHaveProperty('rationale')
  })

  it('pins the endpoints', () => {
    expect(SCHEDULE_ENDPOINTS.list).toBe('/agent/schedules')
    expect(SCHEDULE_ENDPOINTS.one('a b')).toBe('/agent/schedules/a%20b')
  })
})

describe('validateCron — the engine grammar, mirrored', () => {
  it('accepts the five-field forms the engine parses', () => {
    for (const expr of [
      '* * * * *',
      '0 9 * * 1-5',
      '*/15 * * * *',
      '30 8,17 * * *',
      '0 0 1 * *',
      '0 12 * JAN-MAR MON',
      '0 0 * * 0',
      '0 0 * * 7',
      '5/10 * * * *',
    ]) {
      expect(validateCron(expr), expr).toBeNull()
    }
  })

  it('refuses nicknames rather than expanding them, exactly as the engine does', () => {
    expect(validateCron('@daily')).toMatch(/nicknames like @daily are not supported/)
    expect(validateCron('@every 5m')).toMatch(/nicknames/)
  })

  it('refuses the wrong number of fields', () => {
    expect(validateCron('')).toMatch(/empty/)
    expect(validateCron('0 9 * *')).toMatch(/exactly 5 fields.*got 4/)
    expect(validateCron('0 9 * * * *')).toMatch(/got 6/)
  })

  it('names the field and the problem', () => {
    expect(validateCron('99 * * * *')).toMatch(/minute field: 99 is out of range \(0-59\)/)
    expect(validateCron('* 25 * * *')).toMatch(/hour field: 25 is out of range \(0-23\)/)
    expect(validateCron('* * 0 * *')).toMatch(/day-of-month field: 0 is out of range \(1-31\)/)
    expect(validateCron('* * * FOO *')).toMatch(/month field: .*month name/)
    expect(validateCron('* * * * FOO')).toMatch(/day-of-week field: .*day name/)
    expect(validateCron('9-5 * * * *')).toMatch(/inverted/)
    expect(validateCron('*/0 * * * *')).toMatch(/positive integer/)
    expect(validateCron('1,,2 * * * *')).toMatch(/empty list element/)
  })
})

describe('describeCron — the confirmation a human actually reads', () => {
  it('turns expressions into sentences', () => {
    expect(describeCron('0 9 * * *')).toBe('At 09:00, every day.')
    expect(describeCron('0 9 * * 1-5')).toBe('At 09:00, on weekdays.')
    expect(describeCron('30 17 * * 5')).toBe('At 17:30, on Friday.')
    expect(describeCron('*/15 * * * *')).toBe('Every 15 minutes.')
    expect(describeCron('* * * * *')).toBe('Every minute.')
    expect(describeCron('0 0 1 * *')).toBe('At 00:00, on day 1 of the month.')
    expect(describeCron('0 12 * * 0,6')).toBe('At 12:00, at weekends.')
  })

  it('says OR when both day fields are restricted, because cron means OR', () => {
    expect(describeCron('0 9 1 * 1')).toMatch(/ OR /)
  })

  it('returns null for something that does not parse, so the caller shows the error instead', () => {
    expect(describeCron('@daily')).toBeNull()
    expect(describeCron('nonsense')).toBeNull()
  })
})

describe('validateSchedule', () => {
  it('requires a worker and a valid cron, and nothing else', () => {
    expect(validateSchedule(schedule())).toEqual({})
    expect(validateSchedule(schedule({ input: '' }))).toEqual({})
    expect(validateSchedule(schedule({ worker: '  ' })).worker).toBe('worker is required')
    expect(validateSchedule(schedule({ cron: '@daily' })).cron).toMatch(/nicknames/)
  })
})

describe('router-free URL helpers', () => {
  it('round-trips the selection', () => {
    const search = buildScheduleSearch('', 'sch-9')
    expect(search).toBe('?schedule=sch-9')
    expect(scheduleFromSearch(search)).toBe('sch-9')
    expect(scheduleFromSearch(buildScheduleSearch(search, null))).toBeNull()
  })

  it('leaves other parameters alone', () => {
    expect(buildScheduleSearch('?tab=x', 'sch-1')).toBe('?tab=x&schedule=sch-1')
  })
})
