// useEventsOverview and friends — the data hooks behind the events view.
//
// Read-only, all of them: this surface replaces the deleted watchapi cockpit
// and its whole job is to show what the router already did. Nothing in THIS
// file POSTs, and the subscription test in EventReplayPanel is a pure function
// over data these hooks already have — a dry run must never touch the firing
// path. The panel's separate, confirmed "Emit this event" button (F1/RD17) is
// the one write on the surface, and it calls `reload` here afterwards.
//
// Init is a render-phase ref-guard, not a useEffect, matching
// useProjectSettings/useWorkers: a host that inlines `getAuthToken` in its
// config changes `request`'s identity every render, and a `[request]` effect
// would turn that into an unbounded GET loop.

import { useCallback, useMemo, useRef, useState } from 'react'
import { configApiStatus, useConfigApi, type ConfigApiOptions } from './configApi.js'
import { summariseJobProgress, type JobProgress } from './jobprogress.js'
import {
  buildJobRows,
  coerceDelivery,
  coerceProjectEvent,
  coerceSubscription,
  EVENT_ENDPOINTS,
  sumTokens,
  type EventDelivery,
  type JobRow,
  type ProjectEvent,
  type Subscription,
  type TokenTotals,
} from './events.js'

/** Rows fetched per list when the caller does not choose. Matches the engine's
 *  defaultEventLimit, so "one page" means the same thing on both sides. */
export const DEFAULT_EVENT_PAGE = 100

export interface UseEventsOverviewOptions extends ConfigApiOptions {
  /** Override the list endpoints (defaults in EVENT_ENDPOINTS). */
  eventsEndpoint?: string
  deliveriesEndpoint?: string
  subscriptionsEndpoint?: string
  /** Rows per list. Default DEFAULT_EVENT_PAGE. */
  limit?: number
  /** Only fetch events of this exact type (the route's `?type=`). */
  type?: string
  /** Only fetch deliveries with this status (the route's `?status=`). */
  status?: string
  /**
   * Clock for open-ended durations, in unix seconds. Injectable so a table of
   * durations is testable; defaults to the wall clock.
   */
  nowSeconds?: number
  /**
   * Fetch at all. Default true. `false` mounts the hook without issuing a
   * request — which is how two components that want the same page of data
   * avoid fetching it twice (doc 21, X7): whichever one is on screen reads,
   * the other stands down. It still reloads on demand through `reload`.
   */
  enabled?: boolean
}

export interface EventsOverviewApi {
  events: ProjectEvent[]
  deliveries: EventDelivery[]
  subscriptions: Subscription[]
  /** Deliveries joined to their events and subscriptions, newest first. */
  jobs: JobRow[]
  loading: boolean
  /** The first failure across the three loads, as the server phrased it. */
  error: string | null
  reload: () => Promise<void>
  /** True when a list came back full, so older rows may not be shown. */
  truncated: boolean
}

/**
 * The whole events view in one hook: recent events, their deliveries, the
 * routing table, and the job rows that join them.
 *
 * One hook rather than three because the join needs all three lists to agree on
 * a moment — three independently-reloading hooks would render jobs whose event
 * is from one fetch and whose subscription is from another, which is exactly
 * the kind of quiet wrongness an observability screen must not have.
 */
export default function useEventsOverview(
  options: UseEventsOverviewOptions = {},
): EventsOverviewApi {
  const {
    eventsEndpoint = EVENT_ENDPOINTS.events,
    deliveriesEndpoint = EVENT_ENDPOINTS.deliveries,
    subscriptionsEndpoint = EVENT_ENDPOINTS.subscriptions,
    limit = DEFAULT_EVENT_PAGE,
    type = '',
    status = '',
    nowSeconds,
    enabled = true,
  } = options
  const { request } = useConfigApi(options)

  const [events, setEvents] = useState<ProjectEvent[]>([])
  const [deliveries, setDeliveries] = useState<EventDelivery[]>([])
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState<string | null>(null)
  const [truncated, setTruncated] = useState(false)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    const eventsQuery = new URLSearchParams({ limit: String(limit) })
    if (type) eventsQuery.set('type', type)
    const deliveriesQuery = new URLSearchParams({ limit: String(limit) })
    if (status) deliveriesQuery.set('status', status)

    const [eventsResult, deliveriesResult, subscriptionsResult] = await Promise.allSettled([
      request<{ events?: unknown[] } | null>(`${eventsEndpoint}?${eventsQuery.toString()}`),
      request<{ deliveries?: unknown[] } | null>(
        `${deliveriesEndpoint}?${deliveriesQuery.toString()}`,
      ),
      request<{ subscriptions?: unknown[] } | null>(subscriptionsEndpoint),
    ])

    // allSettled, not all: a missing subscriptions route must not blank the
    // event list. Each list keeps whatever it got and the first failure is
    // reported once.
    const failures: string[] = []
    const rowsOf = <K extends string>(
      result: PromiseSettledResult<Partial<Record<K, unknown[]>> | null>,
      key: K,
      label: string,
    ): unknown[] => {
      if (result.status === 'rejected') {
        const reason = result.reason
        failures.push(reason instanceof Error ? reason.message : `failed to load ${label}`)
        return []
      }
      const value = result.value?.[key]
      return Array.isArray(value) ? value : []
    }

    const rawEvents = rowsOf(eventsResult, 'events', 'events')
    const rawDeliveries = rowsOf(deliveriesResult, 'deliveries', 'deliveries')
    const rawSubscriptions = rowsOf(subscriptionsResult, 'subscriptions', 'subscriptions')

    setEvents(rawEvents.map(coerceProjectEvent))
    setDeliveries(rawDeliveries.map(coerceDelivery))
    setSubscriptions(rawSubscriptions.map(coerceSubscription))
    setTruncated(rawEvents.length >= limit || rawDeliveries.length >= limit)
    setError(failures.length > 0 ? failures[0]! : null)
    setLoading(false)
  }, [deliveriesEndpoint, eventsEndpoint, limit, request, status, subscriptionsEndpoint, type])

  // Render-phase ref-guard, keyed on the filters, so changing a filter refetches
  // exactly once and an unstable `request` identity cannot loop.
  // The separator must be a character no filter value can contain, or two
  // different filter sets could share a key. It was a raw NUL, which made the
  // whole file grep as binary; U+001F holds the same guarantee and does not.
  const loadedFor = useRef<string | null>(null)
  const key = `${type}\u001f${status}\u001f${limit}`
  if (loadedFor.current !== key) {
    loadedFor.current = key
    void reload()
  }

  const jobs = useMemo(
    () =>
      buildJobRows(
        deliveries,
        events,
        subscriptions,
        nowSeconds ?? Math.floor(Date.now() / 1000),
      ),
    [deliveries, events, nowSeconds, subscriptions],
  )

  return { events, deliveries, subscriptions, jobs, loading, error, reload, truncated }
}

// ---------------------------------------------------------------------------
// Tokens
// ---------------------------------------------------------------------------

export interface UseSessionTokensOptions extends ConfigApiOptions {
  /** Override the query-events endpoint factory. */
  queryEventsEndpoint?: (sessionId: string) => string
  /** Fetch on mount. False leaves it to `load()` — see the note below. */
  auto?: boolean
  /**
   * Re-fetch when this changes (with `auto`). A running job's step count goes
   * stale the moment it is read, so the job table passes a coarse bucket from
   * `progressRefreshKey` — see jobprogress.ts for why it is coarse. A terminal
   * row passes nothing and is read exactly once.
   */
  refreshKey?: string | number | null
}

export interface SessionTokensApi {
  totals: TokenTotals | null
  /**
   * The same response read as progress — step count, last step, what was
   * produced (doc 21 §5-M6, W6). It rides along because it is a second reading
   * of a request the table was already making: a long job's honest affordance
   * costs zero extra round trips.
   */
  progress: JobProgress | null
  loading: boolean
  error: string | null
  /**
   * The session this job ran in is GONE (RD15): the read came back 404, which
   * on this route means the row is not there (or is not ours — same answer for
   * a reader looking at its own project's jobs). `event_deliveries.session_id`
   * has no foreign key, so job history outlives the session it points at, and
   * the delivery still renders a confident "open" link into nothing.
   *
   * Null when we have not asked, or asked and got an answer that was not a 404
   * — "the fetch failed" and "the transcript is deleted" are different facts
   * and only the second one may be shown to a user as deletion. Read from the
   * HTTP STATUS, never from the body text (see B6 / looksUnwired).
   */
  missing: boolean | null
  /** Fetch now. Safe to call repeatedly; the second call is a no-op in flight. */
  load: () => Promise<void>
}

/**
 * Token totals for one session.
 *
 * `event_deliveries` has no token column and there is no HTTP route in front of
 * the store's GetSessionTokenSummary, so this reads the session's whole
 * query-event stream and sums it — one request per session, which is why the
 * job table only auto-loads the first screenful and leaves the rest behind a
 * button. When a `tokens` field lands on the delivery row (or a token-summary
 * route appears), this hook is what gets deleted.
 */
export function useSessionTokens(
  sessionId: string,
  options: UseSessionTokensOptions = {},
): SessionTokensApi {
  const {
    queryEventsEndpoint = EVENT_ENDPOINTS.queryEvents,
    auto = false,
    refreshKey = null,
  } = options
  const { request } = useConfigApi(options)

  const [totals, setTotals] = useState<TokenTotals | null>(null)
  const [progress, setProgress] = useState<JobProgress | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [missing, setMissing] = useState<boolean | null>(null)
  const inFlight = useRef(false)

  const load = useCallback(async () => {
    if (!sessionId || inFlight.current) return
    inFlight.current = true
    setLoading(true)
    setError(null)
    try {
      const data = await request<unknown>(queryEventsEndpoint(sessionId))
      setTotals(sumTokens(data))
      setProgress(summariseJobProgress(data))
      setMissing(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'failed to load token usage')
      setMissing(configApiStatus(err) === 404)
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [queryEventsEndpoint, request, sessionId])

  // One shot per (session id, refresh key), guarded in render phase like the
  // other hooks here. With no refresh key that is one shot per session, exactly
  // as before; with one it is one shot per bucket, which is how a running row's
  // step count moves without a per-row timer.
  const loadedFor = useRef<string | null>(null)
  const shot = `${sessionId}|${refreshKey ?? ''}`
  if (auto && sessionId && loadedFor.current !== shot) {
    loadedFor.current = shot
    void load()
  }

  return { totals, progress, loading, error, missing, load }
}
