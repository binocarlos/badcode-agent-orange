// F1: the pure event/delivery/job logic — the status vocabulary, the job join,
// and the dry-run subscription matcher.

import { describe, it, expect } from 'vitest'
import {
  blankEnvelope,
  buildEventSearch,
  buildJobRows,
  coerceDelivery,
  coerceEnvelope,
  coerceProjectEvent,
  coerceSubscription,
  DELIVERY_STATUSES,
  deliveryDurationSeconds,
  deliveryStatusSeverity,
  describeDeliveryStatus,
  ENVELOPE_FILTER_KEYS,
  EVENT_SOURCES,
  envelopeFilterMatches,
  eventFromSearch,
  eventToDraftText,
  eventTypeMatches,
  formatDuration,
  formatTokens,
  isTerminalDeliveryStatus,
  matchSubscriptions,
  parseEventDraft,
  sumTokens,
  validateEventTypePattern,
  type EventDelivery,
  type ProjectEvent,
  type Subscription,
} from './events.js'

const event = (over: Partial<ProjectEvent> = {}): ProjectEvent =>
  coerceProjectEvent({
    id: 'e1',
    project: 'acme',
    type: 'email.received',
    text: 'hello',
    envelope: { ...blankEnvelope(), source: 'external' },
    occurred_at: 1000,
    created_at: 1000,
    delivered: true,
    ...over,
  })

const sub = (over: Partial<Subscription> = {}): Subscription =>
  coerceSubscription({
    id: 's1',
    project: 'acme',
    event_type: 'email.received',
    filter: {},
    worker: 'email-answerer',
    max_firings_per_hour: 0,
    enabled: true,
    created_at: 1,
    updated_at: 1,
    ...over,
  })

const delivery = (over: Partial<EventDelivery> = {}): EventDelivery =>
  coerceDelivery({
    id: 'd1',
    project: 'acme',
    event_id: 'e1',
    subscription_id: 's1',
    session_id: 'sess1',
    status: 'ok',
    started_at: 1000,
    ended_at: 1090,
    created_at: 1000,
    updated_at: 1090,
    ...over,
  })

describe('the wire vocabularies', () => {
  it('pins the six delivery statuses, in the engine’s order', () => {
    expect([...DELIVERY_STATUSES]).toEqual([
      'pending',
      'running',
      'ok',
      'failed',
      'awaiting_human',
      'rate_limited',
    ])
  })

  it('pins the four envelope sources', () => {
    expect([...EVENT_SOURCES]).toEqual(['worker', 'external', 'schedule', 'core'])
  })

  it('calls exactly ok/failed/rate_limited terminal — awaiting_human is a pause', () => {
    const terminal = DELIVERY_STATUSES.filter(isTerminalDeliveryStatus)
    expect(terminal).toEqual(['ok', 'failed', 'rate_limited'])
    expect(isTerminalDeliveryStatus('awaiting_human')).toBe(false)
  })

  it('describes every status, and says something honest about an unknown one', () => {
    for (const s of DELIVERY_STATUSES) {
      expect(describeDeliveryStatus(s)).not.toMatch(/^Unknown/)
    }
    expect(describeDeliveryStatus('exploded')).toMatch(/Unknown status/)
    // A pause is not an alarm (doc 21, X11): rose is painted by
    // DeliveryStatusChip, and the MUI bucket stays quiet rather than amber.
    expect(deliveryStatusSeverity('awaiting_human')).toBe('default')
    expect(deliveryStatusSeverity('ok')).toBe('success')
  })

  it('names the envelope filter keys the router can key on', () => {
    const envelopeKeys = Object.keys({ ...blankEnvelope(), reason: '' })
    for (const key of ENVELOPE_FILTER_KEYS) {
      expect(envelopeKeys).toContain(key)
    }
  })
})

describe('coercion', () => {
  it('fills every field a server omitted, so no renderer binds undefined', () => {
    const e = coerceProjectEvent({ id: 'x' })
    expect(e).toMatchObject({ type: '', text: '', delivered: false, occurred_at: 0 })
    expect(e.envelope).toMatchObject({ depth: 0, source: '', interactive: false })
  })

  it('drops an empty reason rather than rendering an empty chip', () => {
    expect(coerceEnvelope({ reason: '' }).reason).toBeUndefined()
    expect(coerceEnvelope({ reason: 'lost' }).reason).toBe('lost')
  })

  it('defaults a subscription with no `enabled` to enabled, like the HTTP layer', () => {
    expect(coerceSubscription({ id: 's' }).enabled).toBe(true)
    expect(coerceSubscription({ id: 's', enabled: false }).enabled).toBe(false)
  })
})

describe('duration', () => {
  it('is null before a job starts', () => {
    expect(deliveryDurationSeconds({ started_at: 0, ended_at: 0 }, 5000)).toBeNull()
  })

  it('measures a finished job between its own stamps', () => {
    expect(deliveryDurationSeconds({ started_at: 1000, ended_at: 1090 }, 9999)).toBe(90)
  })

  it('keeps counting for awaiting_human, whose ended_at the engine leaves unset', () => {
    expect(deliveryDurationSeconds({ started_at: 1000, ended_at: 0 }, 1300)).toBe(300)
  })

  it('formats seconds, minutes and hours', () => {
    expect(formatDuration(12)).toBe('12s')
    expect(formatDuration(200)).toBe('3m 20s')
    expect(formatDuration(3900)).toBe('1h 5m')
    expect(formatDuration(null)).toBe('—')
  })
})

describe('buildJobRows', () => {
  it('joins a delivery to its event and subscription, newest first', () => {
    const rows = buildJobRows(
      [delivery({ id: 'd1', created_at: 10 }), delivery({ id: 'd2', created_at: 20 })],
      [event()],
      [sub()],
      2000,
    )
    expect(rows.map((r) => r.delivery.id)).toEqual(['d2', 'd1'])
    expect(rows[0]!.worker).toBe('email-answerer')
    expect(rows[0]!.eventType).toBe('email.received')
    expect(rows[0]!.durationSeconds).toBe(90)
  })

  it('keeps a job whose subscription has since been deleted', () => {
    // A pre-migration-024 row (no `worker` of its own) is the only case with
    // nothing left to say: the join is the sole source and the join is gone.
    const rows = buildJobRows([delivery()], [event()], [], 2000)
    expect(rows).toHaveLength(1)
    expect(rows[0]!.subscription).toBeNull()
    expect(rows[0]!.worker).toBe('')
  })

  // RD15/I1: the delivery row has named its own worker since migration 024.
  // The browser used to derive it by joining through the subscription, which
  // reported NO worker for a job whose subscription had been deleted and for
  // every schedule firing (which never had one).
  it('names the worker from the delivery row when the subscription is gone', () => {
    const rows = buildJobRows(
      [delivery({ worker: 'nightly-summariser' })],
      [event()],
      [],
      2000,
    )
    expect(rows[0]!.subscription).toBeNull()
    expect(rows[0]!.worker).toBe('nightly-summariser')
  })

  it('prefers the delivery row over the join — it is what actually ran', () => {
    // The subscription has since been repointed at another worker; the job
    // that ran, ran the one on its own row.
    const rows = buildJobRows(
      [delivery({ worker: 'ran-this-one' })],
      [event()],
      [sub({ worker: 'repointed-since' })],
      2000,
    )
    expect(rows[0]!.worker).toBe('ran-this-one')
  })

  it('falls back to the join for a row written before migration 024', () => {
    const rows = buildJobRows([delivery({ worker: '' })], [event()], [sub()], 2000)
    expect(rows[0]!.worker).toBe('email-answerer')
  })

  it('keeps a job whose event fell outside the fetched page', () => {
    const rows = buildJobRows([delivery({ event_id: 'ancient' })], [event()], [sub()], 2000)
    expect(rows).toHaveLength(1)
    expect(rows[0]!.event).toBeNull()
    expect(rows[0]!.eventType).toBe('')
  })

  // The two properties doc 21 §4.2 names as PRECONDITIONS for animating a
  // status at all. Both already held; both are pinned now, because both are
  // one careless sort away from breaking and the symptom (the row an operator
  // is watching teleports as it finishes) is blamed on the animation.
  it('is a projection: one row per delivery id, whatever the status history', () => {
    const rows = buildJobRows(
      [
        delivery({ id: 'd1', status: 'running' }),
        delivery({ id: 'd2', status: 'ok' }),
        delivery({ id: 'd3', status: 'failed' }),
      ],
      [event()],
      [sub()],
      2000,
    )
    expect(rows).toHaveLength(3)
    expect(new Set(rows.map((r) => r.delivery.id)).size).toBe(3)
  })

  it('sorts by CREATED, never by updated — a row never moves because it changed', () => {
    const rows = buildJobRows(
      [
        delivery({ id: 'old-but-just-finished', created_at: 10, started_at: 900, ended_at: 1999 }),
        delivery({ id: 'newer', created_at: 20, started_at: 15, ended_at: 30 }),
      ],
      [event()],
      [sub()],
      2000,
    )
    // Sorting by recent activity would put the just-finished row on top and
    // teleport the exact row the operator was watching.
    expect(rows.map((r) => r.delivery.id)).toEqual(['newer', 'old-but-just-finished'])
  })
})

describe('event-type patterns (§8.3)', () => {
  it('matches exactly, or on a trailing wildcard', () => {
    expect(eventTypeMatches('email.received', 'email.received')).toBe(true)
    expect(eventTypeMatches('email.received', 'email.sent')).toBe(false)
    expect(eventTypeMatches('email.*', 'email.received')).toBe(true)
    expect(eventTypeMatches('email.*', 'chat.started')).toBe(false)
  })

  it('refuses the patterns the engine refuses', () => {
    expect(validateEventTypePattern('email.received')).toBeNull()
    expect(validateEventTypePattern('email.*')).toBeNull()
    expect(validateEventTypePattern('')).toMatch(/required/)
    expect(validateEventTypePattern('*')).toMatch(/not a supported pattern/)
    expect(validateEventTypePattern('em*ail')).toMatch(/trailing wildcard/)
    expect(validateEventTypePattern(' email ')).toMatch(/whitespace/)
  })
})

describe('envelope filters', () => {
  const env = { ...blankEnvelope(), source: 'worker', worker: 'email-answerer', depth: 2 }

  it('matches on equality, comparing as text like the jsonb ->> operator does', () => {
    expect(envelopeFilterMatches({ worker: 'email-answerer' }, env)).toBe(true)
    expect(envelopeFilterMatches({ depth: 2 }, env)).toBe(true)
    expect(envelopeFilterMatches({ depth: '2' }, env)).toBe(true)
    expect(envelopeFilterMatches({ interactive: false }, env)).toBe(true)
    expect(envelopeFilterMatches({ interactive: 'false' }, env)).toBe(true)
  })

  it('fails on a mismatch and on a key the envelope does not carry', () => {
    expect(envelopeFilterMatches({ worker: 'someone-else' }, env)).toBe(false)
    expect(envelopeFilterMatches({ reason: 'lost' }, env)).toBe(false)
  })

  it('an empty filter matches everything', () => {
    expect(envelopeFilterMatches({}, env)).toBe(true)
  })
})

describe('matchSubscriptions — the dry run', () => {
  it('matches on type, naming the worker that would be started', () => {
    const [m] = matchSubscriptions(event(), [sub()])
    expect(m!.matched).toBe(true)
    expect(m!.reason).toMatch(/email-answerer/)
  })

  it('explains a type miss', () => {
    const [m] = matchSubscriptions(event({ type: 'chat.started' }), [sub()])
    expect(m!.matched).toBe(false)
    expect(m!.reason).toMatch(/does not match "email.received"/)
  })

  it('explains a filter miss by naming the field and both values', () => {
    const e = event({ envelope: { ...blankEnvelope(), source: 'worker', worker: 'archivist' } })
    const [m] = matchSubscriptions(e, [sub({ filter: { worker: 'email-answerer' } })])
    expect(m!.matched).toBe(false)
    expect(m!.reason).toContain('worker=email-answerer')
    expect(m!.reason).toContain('archivist')
  })

  // The mirror of the router's self-delivery guard. This file exists because
  // web/src/events.ts is a SECOND implementation of the router's matcher, and a
  // dry run that disagrees with the router is worse than none: it sends someone
  // debugging a subscription that is behaving exactly as designed.
  it('suppresses self-delivery, and says whose completion it was', () => {
    const e = event({
      type: 'worker.finished',
      envelope: { ...blankEnvelope(), source: 'worker', worker: 'archivist' },
    })
    const [m] = matchSubscriptions(e, [sub({ event_type: 'worker.finished', worker: 'archivist' })])
    expect(m!.matched).toBe(false)
    expect(m!.reason).toContain('archivist')
    expect(m!.reason).toMatch(/its own completion/)
  })

  it('another worker\'s completion still matches', () => {
    const e = event({
      type: 'worker.finished',
      envelope: { ...blankEnvelope(), source: 'worker', worker: 'poster' },
    })
    const [m] = matchSubscriptions(e, [sub({ event_type: 'worker.finished', worker: 'archivist' })])
    expect(m!.matched).toBe(true)
  })

  // External events carry no worker. A guard without the emptiness check would
  // compare '' against every subscriber and match nothing — the spine's main
  // path, silently dead in the dry run.
  it('an external event with no worker still matches every subscriber', () => {
    const [m] = matchSubscriptions(event(), [sub()])
    expect(m!.matched).toBe(true)
  })

  it('never matches a disabled subscription, however well it fits', () => {
    const [m] = matchSubscriptions(event(), [sub({ enabled: false })])
    expect(m!.matched).toBe(false)
    expect(m!.reason).toBe('Disabled.')
  })

  it('honours a trailing wildcard and puts matches first', () => {
    const results = matchSubscriptions(event(), [
      sub({ id: 'a', event_type: 'chat.*', worker: 'chatter' }),
      sub({ id: 'b', event_type: 'email.*', worker: 'email-answerer' }),
    ])
    expect(results[0]!.matched).toBe(true)
    expect(results[0]!.subscription.id).toBe('b')
    expect(results[1]!.matched).toBe(false)
  })
})

describe('parseEventDraft', () => {
  it('accepts the minimal shape and defaults the envelope to external/depth 0', () => {
    const parsed = parseEventDraft('{"type":"email.received","text":"hi"}')
    expect(parsed.ok).toBe(true)
    if (!parsed.ok) return
    expect(parsed.event.envelope.source).toBe('external')
    expect(parsed.event.envelope.depth).toBe(0)
  })

  it('accepts a whole stored event, ignoring the fields it does not need', () => {
    const parsed = parseEventDraft(eventToDraftText(event({ type: 'worker.finished' })))
    expect(parsed.ok).toBe(true)
    if (!parsed.ok) return
    expect(parsed.event.type).toBe('worker.finished')
  })

  it('reports unparsable JSON, a non-object, and a missing type', () => {
    expect(parseEventDraft('{oops').ok).toBe(false)
    expect(parseEventDraft('[1,2]').ok).toBe(false)
    const noType = parseEventDraft('{"text":"hi"}')
    expect(noType.ok).toBe(false)
    if (noType.ok) return
    expect(noType.error).toMatch(/"type" is required/)
  })
})

describe('URL selection', () => {
  it('round-trips the selected event and preserves other parameters', () => {
    expect(eventFromSearch('?event=e1')).toBe('e1')
    expect(eventFromSearch('')).toBeNull()
    expect(buildEventSearch('?tab=jobs', 'e1')).toBe('?tab=jobs&event=e1')
    expect(buildEventSearch('?tab=jobs&event=e1', null)).toBe('?tab=jobs')
    expect(buildEventSearch('?event=e1', null)).toBe('')
  })
})

describe('token totals', () => {
  // CAPTURED, not invented. The envelope below was read out of the running e2e
  // stack's Postgres on 2026-07-28 (`agent-orange-stack-e2e-postgres-1`) and the
  // wrapping row is the real `GET /agent/session/{id}/query-events` shape
  // (httpapi/history.go writes `{"events": [<agent_query_events rows>]}`); the
  // ids and created_at are a real row's. Only the elided middle envelopes and
  // the token numbers are ours.
  //
  // The predecessor of this fixture INVENTED a flat `{input_tokens}` on the
  // envelope root. That invention is the whole of bug TOK1: sumTokens matched
  // the fixture, the fixture matched nothing, and every token total the UI ever
  // showed was 0. If this fixture ever stops looking like a real response, the
  // test is worthless again — re-capture it, don't hand-edit it.
  //
  // RD2 (2026-07-29) found the same fixture lying a second way: it wrote a
  // TWO-KEY usage object, and no real response has ever had one. The provider
  // bills three input components — `input_tokens`, `cache_creation_input_tokens`
  // and `cache_read_input_tokens` — none of which contains the others. The
  // fixture now carries all four fields, and callers state each component.
  const capturedRow = (
    queryId: string,
    inputTokens: number,
    outputTokens: number,
    cacheCreationInputTokens = 0,
    cacheReadInputTokens = 0,
  ) => ({
    id: '000622c6-f65a-445e-a9b8-45b0270e26f6',
    session_id: '1a4f6f27-4a6e-4f27-a27d-1811fe93c078',
    query_id: queryId,
    created_at: 1785022143,
    search_text: '',
    events: [
      { type: 'user_message', timestamp: '2026-07-25T23:29:03Z', data: { content: 'Event: schedule.fired' } },
      {
        type: 'message_start',
        timestamp: '2026-07-25T23:29:03.604Z',
        data: { role: 'assistant', messageId: 'e322c9b0-6ccd-4170-aa14-a82faf70fc4f' },
      },
      {
        type: 'query_complete',
        timestamp: '2026-07-25T23:29:06.243Z',
        data: {
          model: 'claude-opus-4-5',
          usage: { inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens },
          result: 'Hello from the agentd mock model proxy. Set ANTHROPIC_API_KEY for a real agent.',
          status: 'completed',
          queryId,
          totalCostUsd: 0.0004,
        },
      },
    ],
  })

  it('sums the nested query-events shape the route actually serves', () => {
    // Input is the sum of all three billed components: (100+400+0) + (7+0+1500).
    const payload = {
      events: [capturedRow('q-34', 100, 30, 400, 0), capturedRow('q-35', 7, 3, 0, 1500)],
    }
    expect(sumTokens(payload)).toEqual({ input: 2007, output: 33, total: 2040 })
  })

  it('counts cache reads — the component that carries most of a cached bill', () => {
    // RD2. With a large composed prompt the cache read dominates; a reader
    // that summed only `inputTokens` reported 12 of 8,466 real input tokens
    // (0.14%) and the UI showed a plausible number that was almost entirely
    // fiction. This must stay in lockstep with go/agentdb/token_usage.go — the
    // number an operator reads and the number that stops their jobs are
    // supposed to be the same number.
    const payload = [
      {
        type: 'query_complete',
        data: {
          usage: {
            inputTokens: 12,
            outputTokens: 213,
            cacheCreationInputTokens: 1704,
            cacheReadInputTokens: 6750,
          },
        },
      },
    ]
    expect(sumTokens(payload)).toEqual({ input: 8466, output: 213, total: 8679 })
  })

  it('counts a usage object that is ONLY cache reads', () => {
    // A fully-cached turn can bill zero uncached input. Returning null there
    // would drop the whole envelope and re-zero the total.
    expect(sumTokens([{ data: { usage: { cacheReadInputTokens: 900 } } }])).toEqual({
      input: 900,
      output: 0,
      total: 900,
    })
  })

  it('reads historical two-key envelopes unchanged', () => {
    // Every envelope stored before 2026-07-29 has only these two keys. Widening
    // the reader must not make them unreadable.
    expect(sumTokens([{ data: { usage: { inputTokens: 40, outputTokens: 12 } } }])).toEqual({
      input: 40,
      output: 12,
      total: 52,
    })
  })

  it('sums a flat envelope list just as well', () => {
    // The legacy route (httpapi with no AgentDB) serves the envelopes bare.
    expect(sumTokens(capturedRow('q-34', 5, 5).events)).toEqual({
      input: 5,
      output: 5,
      total: 10,
    })
  })

  it("reads the provider's snake_case usage spelling too", () => {
    // camelCase is one line of one pluggable harness converting the Anthropic
    // wire format; a harness forwarding usage verbatim must not read as zero.
    const payload = [
      {
        type: 'query_complete',
        data: {
          usage: {
            input_tokens: 12,
            output_tokens: 4,
            cache_creation_input_tokens: 30,
            cache_read_input_tokens: 900,
          },
        },
      },
    ]
    expect(sumTokens(payload)).toEqual({ input: 942, output: 4, total: 946 })
  })

  it('ignores the flat shape nothing has ever written', () => {
    // Guards against someone "restoring" the invented shape and re-breaking it
    // in the direction that reads plausible but counts fiction.
    expect(sumTokens([{ type: 'query_complete', input_tokens: 999, output_tokens: 999 }]).total).toBe(0)
  })

  it('does not double-count by descending into an object it already counted', () => {
    const nested = { usage: { inputTokens: 10, outputTokens: 0 }, data: { usage: { inputTokens: 10 } } }
    expect(sumTokens(nested).total).toBe(10)
  })

  it('is zero when nothing carries tokens', () => {
    expect(sumTokens({ events: [] })).toEqual({ input: 0, output: 0, total: 0 })
    expect(formatTokens(0)).toBe('0')
  })
})
