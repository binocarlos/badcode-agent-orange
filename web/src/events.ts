// Events, deliveries and jobs — the browser-side mirror of the `/agent/events`
// and `/agent/deliveries` read routes (spec docs/product/04-events-and-schedules.md
// §8.1–§8.4; engine: go/agentdb/events.go, go/httpapi/events.go).
//
// Pure: no React, no window, no fetch. The hooks live in useEvents.ts and the
// components in components/Event*.tsx.
//
// This module owns three things:
//
//   1. The wire shapes and their coercers (an event, its core-stamped envelope,
//      a delivery, a subscription).
//   2. The join that turns (deliveries × events × subscriptions) into the
//      job-history rows §8.4 step 2 calls "the job-history spine".
//   3. A *dry-run* copy of the router's matching rules, for the subscription
//      test (L27): given an event, which subscriptions would match, and why not
//      when they would not.
//
// On (3): this is deliberately config-time only and it never posts anything.
// The matcher is a second implementation of a rule the engine owns, which is a
// real risk of drift, so it is kept to the two austere predicates §8.3 defines
// (exact-or-trailing-`*` type, equality on envelope fields) and nothing else —
// there is no third pattern to get wrong. If §8.3 ever grows a pattern, this
// file and go/agentdb/events.go's validateSubscription move together.

import { formatCompactTime } from './timefmt.js'

// ---------------------------------------------------------------------------
// Endpoints
// ---------------------------------------------------------------------------

/** Endpoint paths for the event routes. Overridable per host, like DEFAULT_ENDPOINTS. */
export const EVENT_ENDPOINTS = {
  /** GET (list) + POST (ingest) — this UI only ever GETs. */
  events: '/agent/events',
  /** GET — the delivery/job-history spine. */
  deliveries: '/agent/deliveries',
  /** GET — the routing table, read here to explain matches. */
  subscriptions: '/agent/subscriptions',
  /** GET — per-session query events, the only place token counts exist today. */
  queryEvents: (sessionId: string) =>
    `/agent/session/${encodeURIComponent(sessionId)}/query-events`,
}

// ---------------------------------------------------------------------------
// Wire shapes
// ---------------------------------------------------------------------------

/** Who caused an event (§8.1). The engine's EventSources, verbatim. */
export const EVENT_SOURCES = ['worker', 'external', 'schedule', 'core'] as const
export type EventSource = (typeof EVENT_SOURCES)[number]

/** worker.failed reasons (§8.2). */
export const FAILURE_REASONS = ['error', 'lost'] as const

/**
 * The part of an event CORE stamps and a sender never controls (§8.1).
 * Every scalar is always present on the wire (the Go struct has no `omitempty`
 * on them) except `reason`, which only worker.failed carries.
 */
export interface EventEnvelope {
  depth: number
  source: string
  worker: string
  session_id: string
  interactive: boolean
  attention_requested: boolean
  reason?: string
}

/** One trigger: a name and a text payload, plus the envelope (§8.1). */
export interface ProjectEvent {
  id: string
  project: string
  type: string
  text: string
  envelope: EventEnvelope
  occurred_at: number
  created_at: number
  delivered: boolean
}

/** The complete, ordered delivery-status vocabulary (§8.4). Pinned by test. */
export const DELIVERY_STATUSES = [
  'pending',
  'running',
  'ok',
  'failed',
  'awaiting_human',
  'rate_limited',
] as const
export type DeliveryStatus = (typeof DELIVERY_STATUSES)[number]

/** One (event, subscription) attempt — the job-history row (§8.4 step 2). */
export interface EventDelivery {
  id: string
  project: string
  event_id: string
  subscription_id: string
  session_id: string
  status: string
  /**
   * Why this job failed, as the dispatcher recorded it (RD20 — the column the
   * engine gained in migration 037). '' when nothing failed, and '' on a row
   * that failed before the column existed: "no reason recorded" and "no reason"
   * are the same fact, and the UI says so rather than inventing one.
   */
  failure_reason: string
  started_at: number
  ended_at: number
  created_at: number
  updated_at: number
}

/** The whole of routing configuration (§8.3). F2 owns editing these. */
export interface Subscription {
  id: string
  project: string
  event_type: string
  filter: Record<string, unknown>
  worker: string
  max_firings_per_hour: number
  enabled: boolean
  created_at: number
  updated_at: number
}

const num = (v: unknown, fallback = 0): number =>
  typeof v === 'number' && Number.isFinite(v) ? v : fallback
const str = (v: unknown, fallback = ''): string => (typeof v === 'string' ? v : fallback)
const bool = (v: unknown, fallback = false): boolean => (typeof v === 'boolean' ? v : fallback)
const obj = (v: unknown): Record<string, unknown> =>
  v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {}

/** Fill anything the server omitted, so no renderer ever binds `undefined`. */
export function coerceEnvelope(raw: unknown): EventEnvelope {
  const r = obj(raw)
  const env: EventEnvelope = {
    depth: num(r.depth),
    source: str(r.source),
    worker: str(r.worker),
    session_id: str(r.session_id),
    interactive: bool(r.interactive),
    attention_requested: bool(r.attention_requested),
  }
  // Absent (the common case) stays absent: "no reason" and "reason: ''" are the
  // same fact, and an empty chip in the UI is noise.
  if (typeof r.reason === 'string' && r.reason !== '') env.reason = r.reason
  return env
}

export function coerceProjectEvent(raw: unknown): ProjectEvent {
  const r = obj(raw)
  return {
    id: str(r.id),
    project: str(r.project),
    type: str(r.type),
    text: str(r.text),
    envelope: coerceEnvelope(r.envelope),
    occurred_at: num(r.occurred_at),
    created_at: num(r.created_at),
    delivered: bool(r.delivered),
  }
}

export function coerceDelivery(raw: unknown): EventDelivery {
  const r = obj(raw)
  return {
    id: str(r.id),
    project: str(r.project),
    event_id: str(r.event_id),
    subscription_id: str(r.subscription_id),
    session_id: str(r.session_id),
    status: str(r.status),
    failure_reason: str(r.failure_reason),
    started_at: num(r.started_at),
    ended_at: num(r.ended_at),
    created_at: num(r.created_at),
    updated_at: num(r.updated_at),
  }
}

export function coerceSubscription(raw: unknown): Subscription {
  const r = obj(raw)
  return {
    id: str(r.id),
    project: str(r.project),
    event_type: str(r.event_type),
    filter: obj(r.filter),
    worker: str(r.worker),
    max_firings_per_hour: num(r.max_firings_per_hour),
    enabled: bool(r.enabled, true),
    created_at: num(r.created_at),
    updated_at: num(r.updated_at),
  }
}

// ---------------------------------------------------------------------------
// Status vocabulary
// ---------------------------------------------------------------------------

export function isDeliveryStatus(s: string): s is DeliveryStatus {
  return (DELIVERY_STATUSES as readonly string[]).includes(s)
}

/**
 * Terminal statuses end the attempt and stamp `ended_at` — mirrors the engine's
 * isTerminalDeliveryStatus. Note `awaiting_human` is NOT terminal: it is a
 * pause, and the engine deliberately leaves `ended_at` unset so the duration
 * reads open-ended rather than finished (see UpdateDeliveryStatus).
 */
export function isTerminalDeliveryStatus(status: string): boolean {
  return status === 'ok' || status === 'failed' || status === 'rate_limited'
}

/** One sentence a human can act on, per status. */
export function describeDeliveryStatus(status: string): string {
  switch (status) {
    case 'pending':
      return 'Queued — waiting for a free instance of the worker, or for a budget stop to lift.'
    case 'running':
      return 'A session is working on it now.'
    case 'ok':
      return 'The job finished.'
    case 'failed':
      return 'The job ended badly. No reason is recorded — the delivery row has no reason column.'
    case 'awaiting_human':
      return 'Paused: the job asked for human attention and is waiting. Not finished — the duration keeps running.'
    case 'rate_limited':
      return "Dropped: the subscription's max_firings_per_hour was already used up this hour."
    default:
      return `Unknown status "${status}" — not one of the six the engine writes.`
  }
}

/**
 * MUI severity/colour bucket for a status chip.
 *
 * `awaiting_human` is deliberately `default`, not `warning` (doc 21, X11 and the
 * design's §3.3 rule): a worker waiting for a person is not a fault and must
 * never be rendered in an alarm colour. Its actual colour is rose, which the MUI
 * `color` prop cannot express — `DeliveryStatusChip` paints it from the theme.
 * A host laying out its own table with this bucket gets a quiet chip rather
 * than a wrong one.
 */
export function deliveryStatusSeverity(
  status: string,
): 'success' | 'error' | 'warning' | 'info' | 'default' {
  switch (status) {
    case 'ok':
      return 'success'
    case 'failed':
      return 'error'
    case 'awaiting_human':
      return 'default'
    case 'rate_limited':
      return 'warning'
    case 'running':
      return 'info'
    case 'pending':
      return 'default'
    default:
      return 'default'
  }
}

// ---------------------------------------------------------------------------
// Duration
// ---------------------------------------------------------------------------

/**
 * How long a delivery has been (or was) running, in seconds — null when it has
 * not started. An unfinished job measures against `nowSeconds`, which is why
 * that is a parameter and not `Date.now()`: a duration that depends on the wall
 * clock is untestable, and this one is rendered in a table.
 */
export function deliveryDurationSeconds(
  d: Pick<EventDelivery, 'started_at' | 'ended_at'>,
  nowSeconds: number,
): number | null {
  if (!d.started_at) return null
  const end = d.ended_at || nowSeconds
  const seconds = end - d.started_at
  return seconds < 0 ? 0 : seconds
}

/** `1h 4m`, `3m 20s`, `12s`. Null/negative renders as an em dash. */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || seconds < 0) return '—'
  if (seconds < 60) return `${Math.round(seconds)}s`
  const m = Math.floor(seconds / 60)
  if (m < 60) return `${m}m ${Math.round(seconds % 60)}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

/** Unix SECONDS → the console's compact stamp (timefmt: `14:32`, `Mon 14:32`,
 *  `21 Jul 2026`). 0/absent renders as ''. The `* 1000` is the whole reason
 *  this wrapper exists — event rows are seconds, timefmt takes milliseconds,
 *  and the conversion belongs where the unit is known. */
export function formatTimestamp(seconds: number | null | undefined): string {
  if (!seconds) return ''
  return formatCompactTime(seconds * 1000)
}

// ---------------------------------------------------------------------------
// The job-history join
// ---------------------------------------------------------------------------

/** One row of job history: a delivery with the event and subscription it joins. */
export interface JobRow {
  delivery: EventDelivery
  /** The event that triggered it — null when it fell outside the fetched page. */
  event: ProjectEvent | null
  /** The subscription that matched — null when it has since been deleted. */
  subscription: Subscription | null
  /** The worker whose job this is; '' when the subscription is gone. */
  worker: string
  /** The event type; '' when the event fell outside the fetched page. */
  eventType: string
  status: string
  durationSeconds: number | null
  /** The session that ran the job, '' when it never got that far. */
  sessionId: string
}

/**
 * Join deliveries onto their events and subscriptions, newest-first.
 *
 * Both joins are left joins on purpose. A delivery can outlive its subscription
 * (§8.3 deletes are ordinary deletes on the projection table) and can point at
 * an event older than the page of events we fetched — neither is a reason to
 * drop a job from the history, so the row renders with what it has and the
 * component says which part is missing.
 */
export function buildJobRows(
  deliveries: EventDelivery[],
  events: ProjectEvent[],
  subscriptions: Subscription[],
  nowSeconds: number,
): JobRow[] {
  const eventsById = new Map(events.map((e) => [e.id, e]))
  const subsById = new Map(subscriptions.map((s) => [s.id, s]))
  return deliveries
    .slice()
    .sort((a, b) => (b.created_at || 0) - (a.created_at || 0))
    .map((delivery) => {
      const event = eventsById.get(delivery.event_id) ?? null
      const subscription = subsById.get(delivery.subscription_id) ?? null
      return {
        delivery,
        event,
        subscription,
        worker: subscription?.worker ?? '',
        eventType: event?.type ?? '',
        status: delivery.status,
        durationSeconds: deliveryDurationSeconds(delivery, nowSeconds),
        sessionId: delivery.session_id,
      }
    })
}

// ---------------------------------------------------------------------------
// The dry-run matcher (subscription test, L27)
// ---------------------------------------------------------------------------

/**
 * Is `pattern` a legal §8.3 event-type pattern? Mirrors the engine's
 * validateSubscription: exact match or a single trailing `*`, and bare `*`
 * (match everything) is refused.
 */
export function validateEventTypePattern(pattern: string): string | null {
  if (pattern === '') return 'event_type is required'
  if (pattern !== pattern.trim()) return 'event_type must not have surrounding whitespace'
  if (pattern === '*') return '`*` (match everything) is not a supported pattern'
  const star = pattern.indexOf('*')
  if (star >= 0 && star !== pattern.length - 1) {
    return '`*` is only legal as a trailing wildcard'
  }
  return null
}

/** Exact match, or trailing-`*` prefix match. No other patterns exist (§8.3). */
export function eventTypeMatches(pattern: string, type: string): boolean {
  if (pattern === '') return false
  if (pattern.endsWith('*')) return type.startsWith(pattern.slice(0, -1))
  return pattern === type
}

/** Envelope fields a subscription filter may key on. Anything else is a typo. */
export const ENVELOPE_FILTER_KEYS = [
  'depth',
  'source',
  'worker',
  'session_id',
  'interactive',
  'attention_requested',
  'reason',
] as const

/**
 * Equality match on envelope fields (§8.3). The engine compares the jsonb value
 * as text (`envelope->>'worker' = ?`), so this compares stringified scalars —
 * `{"interactive": false}` and `{"interactive": "false"}` both match a
 * non-interactive event, exactly as they would server-side.
 */
export function envelopeFilterMatches(
  filter: Record<string, unknown>,
  envelope: EventEnvelope,
): boolean {
  const env = envelope as unknown as Record<string, unknown>
  for (const [key, want] of Object.entries(filter)) {
    const have = env[key]
    // `reason` is absent on everything but worker.failed; a filter on it must
    // not match an envelope that simply has no such field.
    if (have === undefined) return false
    if (String(have) !== String(want)) return false
  }
  return true
}

/** One subscription's verdict for one event, with the reason a human wants. */
export interface SubscriptionMatch {
  subscription: Subscription
  matched: boolean
  /** Why it matched, or the first reason it did not. */
  reason: string
}

/** The event shape the matcher needs — a stored event or a pasted draft. */
export interface MatchableEvent {
  type: string
  envelope: EventEnvelope
}

/**
 * Which subscriptions would match this event, and why the others would not.
 *
 * Matched first, then by event type, so the answer to "will anything happen?"
 * is the top of the list. Rate limiting, budget stops and `max_instances`
 * gating are deliberately NOT modelled: they depend on live counters, and a
 * dry run that guessed at them would be confidently wrong.
 */
export function matchSubscriptions(
  event: MatchableEvent,
  subscriptions: Subscription[],
): SubscriptionMatch[] {
  const out = subscriptions.map((subscription): SubscriptionMatch => {
    if (!subscription.enabled) {
      return { subscription, matched: false, reason: 'Disabled.' }
    }
    if (!eventTypeMatches(subscription.event_type, event.type)) {
      return {
        subscription,
        matched: false,
        reason: `Type "${event.type}" does not match "${subscription.event_type}".`,
      }
    }
    const filter = subscription.filter ?? {}
    const entries = Object.entries(filter)
    if (entries.length > 0 && !envelopeFilterMatches(filter, event.envelope)) {
      const env = event.envelope as unknown as Record<string, unknown>
      const failed = entries.find(([k, v]) => env[k] === undefined || String(env[k]) !== String(v))
      const [key, want] = failed ?? ['', '']
      const have = env[key]
      return {
        subscription,
        matched: false,
        reason:
          have === undefined
            ? `Filter wants ${key}=${String(want)}; the envelope has no ${key}.`
            : `Filter wants ${key}=${String(want)}; the envelope has ${String(have)}.`,
      }
    }
    return {
      subscription,
      matched: true,
      reason:
        entries.length > 0
          ? `Type and filter both match — starts a job for "${subscription.worker}".`
          : `Type matches — starts a job for "${subscription.worker}".`,
    }
  })
  return out.sort((a, b) => {
    if (a.matched !== b.matched) return a.matched ? -1 : 1
    return a.subscription.event_type.localeCompare(b.subscription.event_type)
  })
}

// ---------------------------------------------------------------------------
// Pasting an event (the replay panel's input)
// ---------------------------------------------------------------------------

/** A blank envelope — every scalar present, as core would stamp it. */
export function blankEnvelope(): EventEnvelope {
  return {
    depth: 0,
    source: 'external',
    worker: '',
    session_id: '',
    interactive: false,
    attention_requested: false,
  }
}

/** The starting text in the replay editor: a minimal, legal external event. */
export const EVENT_DRAFT_TEMPLATE = JSON.stringify(
  {
    type: 'email.received',
    text: 'From: someone@example.com\nSubject: a question\n\n…',
    envelope: blankEnvelope(),
  },
  null,
  2,
)

export type EventDraftParse =
  | { ok: true; event: MatchableEvent & { text: string } }
  | { ok: false; error: string }

/**
 * Parse pasted JSON into something matchable. Accepts a whole stored event
 * (id/project/occurred_at and friends are ignored) or the minimal
 * `{type, text, envelope}`; a missing envelope defaults to the external one
 * core would stamp on `POST /agent/events`.
 */
export function parseEventDraft(text: string): EventDraftParse {
  const trimmed = text.trim()
  if (trimmed === '') return { ok: false, error: 'Paste an event, or load one from the list.' }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : 'Invalid JSON' }
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { ok: false, error: 'Expected a JSON object with at least a "type".' }
  }
  const r = parsed as Record<string, unknown>
  const type = str(r.type).trim()
  if (type === '') return { ok: false, error: '"type" is required.' }
  const envelope = r.envelope === undefined ? blankEnvelope() : coerceEnvelope(r.envelope)
  if (envelope.source === '') envelope.source = 'external'
  return { ok: true, event: { type, text: str(r.text), envelope } }
}

/** A stored event, re-serialised as the replay editor's text. */
export function eventToDraftText(event: ProjectEvent): string {
  return JSON.stringify({ type: event.type, text: event.text, envelope: event.envelope }, null, 2)
}

// ---------------------------------------------------------------------------
// Router-free selection in the URL
// ---------------------------------------------------------------------------
//
// Same rule as workers.ts: the thing a human is looking at belongs in the
// address bar, but this package must not impose a router. One query parameter,
// parsed here and written through the History API by the component.

/** Query parameter naming the selected event, e.g. `?event=<uuid>`. */
export const EVENT_QUERY_PARAM = 'event'

/** The event named by a query string, or null. Accepts `?a=b` or `a=b`. */
export function eventFromSearch(search: string): string | null {
  if (!search) return null
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  const id = params.get(EVENT_QUERY_PARAM)
  return id ? id : null
}

/** `search` with the event parameter set to `id`, or removed when id is null. */
export function buildEventSearch(search: string, id: string | null): string {
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  if (id === null) params.delete(EVENT_QUERY_PARAM)
  else params.set(EVENT_QUERY_PARAM, id)
  const out = params.toString()
  return out === '' ? '' : `?${out}`
}

// ---------------------------------------------------------------------------
// Token totals
// ---------------------------------------------------------------------------

/** Input/output token totals for one session. */
export interface TokenTotals {
  input: number
  output: number
  total: number
}

/**
 * Reads one `usage` object, or null if it carries no token counts.
 *
 * Both spellings are accepted. camelCase is what is actually stored —
 * `sandbox/src/harness/claude-agent-sdk.ts` converts the provider's snake_case
 * into it before emitting `query_complete` — but that conversion is one line of
 * one PLUGGABLE harness, and `input_tokens` is the provider's own spelling on
 * the wire, so a harness forwarding its usage object verbatim must not read as
 * zero. The Go readers tolerate exactly the same pair.
 *
 * Input is the SUM of three separately-billed components, not just
 * `inputTokens`. The provider bills `input_tokens`,
 * `cache_creation_input_tokens` and `cache_read_input_tokens` independently and
 * none contains the others, so with caching active most input arrives as cache
 * reads. Reading only the first showed a plausible fraction of true spend in the
 * UI while `go/agentdb/token_usage.go` — the brake's own reader — is summing the
 * same three. These two must agree, or the number an operator sees is not the
 * number that stops their jobs.
 *
 * A usage object carrying ONLY cache components still counts: `null` is
 * reserved for "this object has no token counts at all", which is what stops the
 * walk in sumTokens from descending past a real usage object.
 */
function readUsage(node: unknown): { input: number; output: number } | null {
  if (!node || typeof node !== 'object' || Array.isArray(node)) return null
  const r = node as Record<string, unknown>
  const pick = (keys: string[]): number | null => {
    for (const k of keys) {
      if (typeof r[k] === 'number') return r[k] as number
    }
    return null
  }
  const uncached = pick(['inputTokens', 'input_tokens'])
  const cacheCreation = pick(['cacheCreationInputTokens', 'cache_creation_input_tokens'])
  const cacheRead = pick(['cacheReadInputTokens', 'cache_read_input_tokens'])
  const output = pick(['outputTokens', 'output_tokens'])
  if (uncached === null && cacheCreation === null && cacheRead === null && output === null) {
    return null
  }
  return {
    input: (uncached ?? 0) + (cacheCreation ?? 0) + (cacheRead ?? 0),
    output: output ?? 0,
  }
}

/**
 * Sum token counts out of a `GET /agent/session/{id}/query-events` response.
 *
 * There is no token column on `event_deliveries` and no HTTP route in front of
 * the store's GetSessionTokenSummary, so the only place these numbers exist for
 * the browser is the raw query-event stream. The walk descends the JSON until
 * it finds an object carrying a token-bearing `usage` and does not descend past
 * it, which makes it indifferent to whether the host serves the nested
 * `{events: [{events: [...]}]}` rows (the AgentDB path) or a flat envelope list
 * (the legacy path) — and stops a future envelope repeating usage nested inside
 * itself from double-counting.
 *
 * It reads `data.usage.inputTokens`, which is where the harness actually puts
 * it. Until 2026-07-28 it read a flat `input_tokens` on the envelope root — a
 * shape this file's own unit fixture invented and no writer has ever emitted —
 * so every token total in the UI was 0. The fixture is now a captured response;
 * see events.test.ts. Do not change these key paths without re-capturing one.
 */
export function sumTokens(raw: unknown): TokenTotals {
  let input = 0
  let output = 0
  const walk = (node: unknown): void => {
    if (Array.isArray(node)) {
      node.forEach(walk)
      return
    }
    if (!node || typeof node !== 'object') return
    const r = node as Record<string, unknown>
    const usage = readUsage(r.usage)
    if (usage) {
      input += usage.input
      output += usage.output
      return
    }
    Object.values(r).forEach(walk)
  }
  walk(raw)
  return { input, output, total: input + output }
}

/** `12,340` — thousands-separated for a table cell. */
export function formatTokens(n: number): string {
  return n.toLocaleString()
}
