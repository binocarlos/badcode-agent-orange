// Subscriptions — the editing half of §8.3 (F2).

import { describe, it, expect } from 'vitest'
import { coerceSubscription, type Subscription } from './events.js'
import {
  buildSubscriptionSearch,
  describeSubscription,
  newSubscriptionDraft,
  subscriptionBody,
  subscriptionFromSearch,
  SUBSCRIPTION_ENDPOINTS,
  validateFilterEntry,
  validateSubscription,
} from './subscriptions.js'

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
    updated_at: 2,
    ...over,
  })

describe('the wire shape', () => {
  it('defaults to unlimited and enabled', () => {
    const draft = newSubscriptionDraft('acme')
    expect(draft.max_firings_per_hour).toBe(0)
    expect(draft.enabled).toBe(true)
    expect(draft.id).toBe('')
  })

  it('always sends max_firings_per_hour and enabled, because absent means "unchanged" on the wire', () => {
    const body = subscriptionBody(sub({ enabled: false, max_firings_per_hour: 4 }))
    expect(body).toEqual({
      event_type: 'email.received',
      filter: {},
      worker: 'email-answerer',
      max_firings_per_hour: 4,
      enabled: false,
    })
    // Omitting `enabled` to mean false is exactly how a disabled subscription
    // silently comes back on.
    expect(Object.keys(body)).toContain('enabled')
  })

  it('never sends server-owned fields', () => {
    const body = subscriptionBody(sub()) as Record<string, unknown>
    for (const key of ['id', 'project', 'created_at', 'updated_at']) {
      expect(body[key]).toBeUndefined()
    }
  })

  it('pins the endpoints', () => {
    expect(SUBSCRIPTION_ENDPOINTS.list).toBe('/agent/subscriptions')
    expect(SUBSCRIPTION_ENDPOINTS.one('a/b')).toBe('/agent/subscriptions/a%2Fb')
  })
})

describe('validateSubscription', () => {
  it('accepts what the engine accepts', () => {
    expect(validateSubscription(sub())).toEqual({})
    expect(validateSubscription(sub({ event_type: 'email.*' }))).toEqual({})
    expect(validateSubscription(sub({ filter: { worker: 'x', interactive: false } }))).toEqual({})
  })

  it('refuses what the engine refuses', () => {
    expect(validateSubscription(sub({ event_type: '' })).event_type).toBe('event_type is required')
    expect(validateSubscription(sub({ event_type: '*' })).event_type).toMatch(/not a supported pattern/)
    expect(validateSubscription(sub({ event_type: 'a*b' })).event_type).toMatch(/trailing wildcard/)
    expect(validateSubscription(sub({ worker: ' ' })).worker).toBe('worker is required')
    expect(validateSubscription(sub({ max_firings_per_hour: -1 })).max_firings_per_hour).toMatch(/0 \(unlimited\)/)
    expect(validateSubscription(sub({ max_firings_per_hour: 1.5 })).max_firings_per_hour).toBe('must be a whole number')
  })

  it('rejects a filter on a field the envelope does not have — it would match nothing for ever', () => {
    const errors = validateSubscription(sub({ filter: { subject: 'invoice' } }))
    expect(errors['filter.subject']).toMatch(/not an envelope field/)
    expect(validateFilterEntry('worker', 'x')).toBeNull()
    expect(validateFilterEntry('depth', 2)).toBeNull()
    expect(validateFilterEntry('interactive', false)).toBeNull()
    expect(validateFilterEntry('worker', { name: 'x' })).toMatch(/plain value/)
    expect(validateFilterEntry('worker', null)).toMatch(/needs a value/)
  })
})

describe('describeSubscription', () => {
  it('says what the row will do, in one sentence', () => {
    expect(describeSubscription(sub())).toBe(
      'When an `email.received` event arrives, start a job for email-answerer.',
    )
    expect(
      describeSubscription(sub({ filter: { worker: 'reviewer' }, max_firings_per_hour: 4 })),
    ).toBe(
      'When an `email.received` event arrives whose envelope has worker=reviewer, ' +
        'start a job for email-answerer. At most 4 firing(s) per hour.',
    )
  })

  it('says so when it is disabled, because a disabled row that looks armed is the trap', () => {
    expect(describeSubscription(sub({ enabled: false }))).toMatch(/disabled — it will not fire/)
  })
})

describe('router-free URL helpers', () => {
  it('round-trips the selection', () => {
    const search = buildSubscriptionSearch('', 's1')
    expect(search).toBe('?subscription=s1')
    expect(subscriptionFromSearch(search)).toBe('s1')
    expect(subscriptionFromSearch(buildSubscriptionSearch(search, null))).toBeNull()
  })
})
