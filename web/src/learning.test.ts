// BA1 — the before/after join (design §7.2).

import { describe, it, expect } from 'vitest'
import { beforeAfter, eventMs, NO_JOB_YET, BEFORE_AFTER_CAVEAT } from './learning.js'
import { coerceConfigEvent } from './configLog.js'
import { coerceProjectEvent } from './events.js'

const rewrite = (over: Record<string, unknown> = {}) =>
  coerceConfigEvent({
    id: 'c1',
    project: 'acme',
    actor_worker: 'email-reviewer',
    actor_session: 'sess-r',
    action: 'worker_prompt_write',
    payload: { name: 'email-answerer', system_prompt: 'new prompt' },
    rationale: 'the rule is now first',
    created_at: 1_700_000_100_000,
    ...over,
  })

const finished = (worker: string, seconds: number, text: string, id = `e${seconds}`) =>
  coerceProjectEvent({
    id,
    project: 'acme',
    type: 'worker.finished',
    text,
    envelope: {
      depth: 1,
      source: 'worker',
      worker,
      session_id: `sess-${id}`,
      interactive: false,
      attention_requested: false,
    },
    occurred_at: seconds,
    created_at: seconds,
    delivered: true,
  })

describe('beforeAfter', () => {
  it('picks the neighbouring jobs of the SUBJECT worker, not the actor', () => {
    const ba = beforeAfter(rewrite(), [
      finished('email-answerer', 1_700_000_000, 'Hi Jane,'),
      finished('email-reviewer', 1_700_000_050, 'reviewer chatter'),
      finished('email-answerer', 1_700_000_200, 'Ticket #4471\nHi Jane,'),
      finished('email-answerer', 1_700_000_300, 'later still'),
    ])
    expect(ba.workerName).toBe('email-answerer')
    expect(ba.applicable).toBe(true)
    expect(ba.before.text).toBe('Hi Jane,')
    expect(ba.after.text).toBe('Ticket #4471\nHi Jane,')
    expect(ba.actorWorker).toBe('email-reviewer')
    expect(ba.rationale).toBe('the rule is now first')
  })

  it('compares milliseconds against seconds — every job is not "after"', () => {
    // occurred_at is unix SECONDS; created_at on a config event is unix MS.
    const ba = beforeAfter(rewrite(), [finished('email-answerer', 1_700_000_000, 'said')])
    expect(eventMs(finished('email-answerer', 1_700_000_000, 'said'))).toBe(1_700_000_000_000)
    expect(ba.before.event?.id).toBe('e1700000000')
    expect(ba.after.event).toBeNull()
  })

  it('returns nulls when a side has not happened yet', () => {
    const none = beforeAfter(rewrite(), [])
    expect(none.before.event).toBeNull()
    expect(none.after.event).toBeNull()
    expect(none.before.atMs).toBeNull()
    const onlyAfter = beforeAfter(rewrite(), [
      finished('email-answerer', 1_700_000_400, 'first ever'),
    ])
    expect(onlyAfter.before.event).toBeNull()
    expect(onlyAfter.after.text).toBe('first ever')
    expect(NO_JOB_YET).toBe('no job has run since')
  })

  it('sorts an unordered list and counts a same-millisecond job as before', () => {
    const ba = beforeAfter(rewrite(), [
      finished('email-answerer', 1_700_000_200, 'after'),
      finished('email-answerer', 1_700_000_100, 'exactly at the write'),
      finished('email-answerer', 1_700_000_000, 'older'),
    ])
    expect(ba.before.text).toBe('exactly at the write')
    expect(ba.after.text).toBe('after')
  })

  it('is not applicable to a config event that wrote no prompt', () => {
    const ba = beforeAfter(
      rewrite({ action: 'worker_freeze', payload: { name: 'email-answerer' } }),
      [],
    )
    expect(ba.applicable).toBe(false)
    expect(ba.workerName).toBe('email-answerer')
  })

  it('renders the caveat verbatim', () => {
    expect(BEFORE_AFTER_CAVEAT).toBe(
      'Tool calls are absent from these transcripts — this shows what the worker said, never what it did.',
    )
  })
})
