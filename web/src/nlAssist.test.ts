// nlAssist — the config-time NL→cron/filter compiler (F2, L28).
//
// The tests that matter here are the REFUSALS. A compiler that guesses is worse
// than no compiler, because a wrong cron parses and then runs wrong every day
// until somebody notices; and the whole design (propose → human confirms →
// apply) only pays off if "I don't know" is a real answer.

import { describe, it, expect } from 'vitest'
import { compileCron, compileEnvelopeFilter } from './nlAssist.js'
import { validateCron } from './schedules.js'

const cron = (text: string): string => {
  const r = compileCron(text)
  if (!r.ok) throw new Error(`expected a cron for ${text}: ${r.error}`)
  return r.value.cron
}

describe('compileCron', () => {
  it('compiles the phrasings people actually type', () => {
    expect(cron('every day at 09:00')).toBe('0 9 * * *')
    expect(cron('daily at 9am')).toBe('0 9 * * *')
    expect(cron('every day at midnight')).toBe('0 0 * * *')
    expect(cron('every weekday at 17:30')).toBe('30 17 * * 1,2,3,4,5')
    expect(cron('at noon on weekends')).toBe('0 12 * * 0,6')
    expect(cron('every Monday and Thursday at 08:00')).toBe('0 8 * * 1,4')
    expect(cron('every 15 minutes')).toBe('*/15 * * * *')
    expect(cron('every minute')).toBe('* * * * *')
    expect(cron('every 2 hours')).toBe('0 */2 * * *')
    expect(cron('on the 1st of the month at 09:00')).toBe('0 9 1 * *')
    expect(cron('every friday at 6pm')).toBe('0 18 * * 5')
  })

  it('always emits five fields the engine will accept — nicknames are impossible by construction', () => {
    for (const text of [
      'every day at 09:00',
      'every weekday at 17:30',
      'every 15 minutes',
      'every 2 hours',
      'on Sundays at 23:00',
    ]) {
      const expr = cron(text)
      expect(expr.split(' ')).toHaveLength(5)
      expect(validateCron(expr), `${text} → ${expr}`).toBeNull()
    }
  })

  it('echoes back what it compiled, in words', () => {
    const r = compileCron('every weekday at 09:00')
    expect(r.ok).toBe(true)
    if (r.ok) expect(r.explanation).toBe('At 09:00, on weekdays.')
  })

  it('passes a hand-written cron expression straight through', () => {
    const r = compileCron('0 9 * * 1-5')
    expect(r.ok).toBe(true)
    if (r.ok) {
      expect(r.value.cron).toBe('0 9 * * 1-5')
      expect(r.explanation).toBe('At 09:00, on weekdays.')
    }
  })

  it('refuses rather than guesses', () => {
    const refusals: Array<[string, RegExp]> = [
      ['', /Describe when/],
      ['when it feels right', /could not find a time/],
      ['whenever the mood takes the team', /could not find a time/],
      ['every 30 seconds', /sub-minute/],
      ['@daily', /nicknames/],
      ['every 90 minutes', /does not divide an hour/],
      ['every 36 hours', /does not divide a day/],
      ['on the 1st and every Monday at 9am', /almost never what people mean/],
    ]
    for (const [text, want] of refusals) {
      const r = compileCron(text)
      expect(r.ok, `"${text}" was accepted as ${r.ok ? r.value.cron : ''}`).toBe(false)
      if (!r.ok) expect(r.error, text).toMatch(want)
    }
  })
})

describe('compileEnvelopeFilter', () => {
  it('names the event type and the envelope fields', () => {
    const r = compileEnvelopeFilter('when the email-answerer worker finishes')
    expect(r.ok).toBe(true)
    if (r.ok) {
      expect(r.value.event_type).toBe('worker.finished')
      expect(r.value.filter).toEqual({ worker: 'email-answerer' })
    }
  })

  it('takes an explicit type verbatim, wildcard and all', () => {
    const r = compileEnvelopeFilter('any email.* event')
    expect(r.ok && r.value.event_type).toBe('email.*')
    const q = compileEnvelopeFilter('`config.changed` from the marketing-manager worker')
    expect(q.ok && q.value.event_type).toBe('config.changed')
    expect(q.ok && q.value.filter).toEqual({ worker: 'marketing-manager' })
  })

  it('understands the three envelope predicates that matter', () => {
    const fails = compileEnvelopeFilter('only failures from the tweet-author worker')
    expect(fails.ok && fails.value.filter.reason).toBe('error')
    const chats = compileEnvelopeFilter('worker.finished but ignore chats')
    expect(chats.ok && chats.value.filter.interactive).toBe(false)
    const attn = compileEnvelopeFilter('worker.finished where attention was requested')
    expect(attn.ok && attn.value.filter.attention_requested).toBe(true)
  })

  it('refuses predicates the router cannot express, instead of quietly widening them', () => {
    const r = compileEnvelopeFilter('when a job takes more than five minutes')
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.error).toMatch(/equality and a trailing \* only/)

    const s = compileEnvelopeFilter('when the subject contains invoice')
    expect(s.ok).toBe(false)
  })

  it('refuses when it recognised nothing at all', () => {
    const r = compileEnvelopeFilter('something interesting happens')
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.error).toMatch(/could not find an event type/)
  })

  it('explains the proposal in words, for the confirmation step', () => {
    const r = compileEnvelopeFilter('when the email-answerer worker finishes')
    expect(r.ok && r.explanation).toBe(
      'Match an `worker.finished` event whose envelope has worker=email-answerer.',
    )
  })
})
