// Subscriptions — the EDITING half of `/agent/subscriptions` (spec
// docs/product/04-events-and-schedules.md §8.3, engine: go/agentdb/events.go,
// go/httpapi/events.go). Work-plan item F2.
//
// Pure: no React, no window, no fetch. The hook lives in useSubscriptions.ts
// and the components in components/Subscription*.tsx.
//
// F1 already owns the READING half in events.ts — the `Subscription` type,
// `coerceSubscription`, `validateEventTypePattern`, the envelope-filter keys
// and `matchSubscriptions` (the dry-run "what would this match?" preview). This
// module adds only what editing needs and re-exports nothing: one type, one
// matcher, one validator, in one place.
//
// Validation policy: mirror the engine. The server enforces an event-type
// pattern (exact or trailing-`*`), a worker, a non-negative rate limit, and
// that the filter is a flat equality map over ENVELOPE fields — so this
// enforces exactly those.

import {
  ENVELOPE_FILTER_KEYS,
  validateEventTypePattern,
  type Subscription,
} from './events.js'

/** Endpoint paths for the subscription routes. `list` is also the POST target
 *  (create returns 201); `one` takes PUT and DELETE. */
export const SUBSCRIPTION_ENDPOINTS = {
  list: '/agent/subscriptions',
  one: (id: string) => `/agent/subscriptions/${encodeURIComponent(id)}`,
}

/** A subscription being edited. An empty `id` means "not created yet". */
export type SubscriptionDraft = Subscription

/** A blank subscription: unlimited firings, enabled — the engine's defaults. */
export function newSubscriptionDraft(project = ''): SubscriptionDraft {
  return {
    id: '',
    project,
    event_type: '',
    filter: {},
    worker: '',
    max_firings_per_hour: 0,
    enabled: true,
    created_at: 0,
    updated_at: 0,
  }
}

/** The write body. `project`, `id` and the timestamps are the server's.
 *
 *  `max_firings_per_hour` and `enabled` are pointers on the wire, where an
 *  absent field means "unchanged/default" — so both are ALWAYS sent. Omitting
 *  `enabled` to mean false is exactly how a disabled subscription silently
 *  comes back on. */
export function subscriptionBody(s: SubscriptionDraft): {
  event_type: string
  filter: Record<string, unknown>
  worker: string
  max_firings_per_hour: number
  enabled: boolean
} {
  return {
    event_type: s.event_type.trim(),
    filter: s.filter,
    worker: s.worker.trim(),
    max_firings_per_hour: s.max_firings_per_hour,
    enabled: s.enabled,
  }
}

/** Field name → human-readable problem. Filter entries key as `filter.<key>`.
 *  Empty object means "safe to save". */
export type SubscriptionFieldErrors = Record<string, string>

export function validateSubscription(s: SubscriptionDraft): SubscriptionFieldErrors {
  const errors: SubscriptionFieldErrors = {}

  const pattern = validateEventTypePattern(s.event_type)
  if (pattern !== null) errors.event_type = pattern
  if (s.worker.trim() === '') errors.worker = 'worker is required'
  if (!Number.isInteger(s.max_firings_per_hour)) {
    errors.max_firings_per_hour = 'must be a whole number'
  } else if (s.max_firings_per_hour < 0) {
    errors.max_firings_per_hour = 'must be 0 (unlimited) or more'
  }

  for (const [key, value] of Object.entries(s.filter)) {
    const problem = validateFilterEntry(key, value)
    if (problem !== null) errors[`filter.${key}`] = problem
  }
  return errors
}

/**
 * Validate one envelope-filter entry.
 *
 * The filter is equality on ENVELOPE fields only (§8.3) — core stamps the
 * envelope and a sender never controls it, which is the whole reason filtering
 * on it is safe. A key outside that set matches nothing for ever, so it is
 * rejected here rather than saved as a subscription that silently never fires.
 */
export function validateFilterEntry(key: string, value: unknown): string | null {
  if (!(ENVELOPE_FILTER_KEYS as readonly string[]).includes(key)) {
    return `"${key}" is not an envelope field — filters match ${ENVELOPE_FILTER_KEYS.join(', ')} only`
  }
  if (value === null || value === undefined) return 'needs a value to match'
  if (typeof value === 'object') return 'must be a plain value (string, number or boolean), not an object'
  return null
}

/**
 * Describe a subscription in words, for the confirmation line under the
 * editor: "When an `email.*` event arrives from worker email-answerer, start a
 * job for reviewer (at most 4 per hour)."
 */
export function describeSubscription(s: SubscriptionDraft): string {
  const type = s.event_type.trim() === '' ? 'any event' : `an \`${s.event_type.trim()}\` event`
  const worker = s.worker.trim() === '' ? 'a worker' : s.worker.trim()
  const filters = Object.entries(s.filter)
  const where =
    filters.length === 0
      ? ''
      : ` whose envelope has ${filters.map(([k, v]) => `${k}=${String(v)}`).join(' and ')}`
  const rate =
    s.max_firings_per_hour > 0 ? ` At most ${s.max_firings_per_hour} firing(s) per hour.` : ''
  const disabled = s.enabled ? '' : ' (disabled — it will not fire until you enable it)'
  return `When ${type} arrives${where}, start a job for ${worker}.${rate}${disabled}`
}

// ---------------------------------------------------------------------------
// Router-free URL helpers (the WorkersPage/EventsPage convention)
// ---------------------------------------------------------------------------

export const SUBSCRIPTION_QUERY_PARAM = 'subscription'

export function subscriptionFromSearch(search: string): string | null {
  return new URLSearchParams(search).get(SUBSCRIPTION_QUERY_PARAM)
}

export function buildSubscriptionSearch(search: string, id: string | null): string {
  const params = new URLSearchParams(search)
  if (id === null || id === '') params.delete(SUBSCRIPTION_QUERY_PARAM)
  else params.set(SUBSCRIPTION_QUERY_PARAM, id)
  const qs = params.toString()
  return qs === '' ? '' : `?${qs}`
}
