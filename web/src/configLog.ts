// The config log, browser side — the §15.10 changelog (spec
// docs/product/09-config-log.md; engine: go/agentdb/config_events.go).
//
// Pure: no React, no window, no fetch. The hook lives in useConfigLog.ts and the
// component in components/ChangelogView.tsx.
//
// ── THE MISSING ROUTE ──────────────────────────────────────────────────────
//
// There is currently NO HTTP read route for the config log. `config_events` is
// written (the store seam is adopted) and `Store.ListConfigEvents` exists, but
// nothing in `go/httpapi` serves it — J2/J3 own that. So this module is written
// against a narrow, injectable seam (`ConfigEventFetcher`, below) rather than
// against `fetch`, and useConfigLog defaults to hitting CONFIG_LOG_ENDPOINT so
// that wiring it later is nothing but adding the handler.
//
// The exact contract this UI needs:
//
//   GET /agent/config-events
//     query: ?action=<exact|prefix*>&actor_worker=<name>&since=<ms>&until=<ms>&limit=<n>
//     auth:  the ordinary session JWT; project comes from the Customer claim,
//            never from the query (P5) — same posture as GET /agent/events.
//     200:   {"config_events": [ConfigEvent, ...]}   // newest-first
//
//   ConfigEvent (go/agentdb.ConfigEvent's JSON, verbatim):
//     {
//       "id": "uuid",
//       "project": "badcode",
//       "actor_worker": "prompt-tuner",   // "" for human/API edits
//       "actor_session": "sess-uuid",     // "" for human/API edits
//       "action": "worker_prompt_write",  // §15.3 vocabulary
//       "payload": { ... },               // FULL new state, never a diff
//       "rationale": "shorter answers",   // required on prompt writes (§15.5)
//       "created_at": 1789000000123       // unix MILLISECONDS (J1)
//     }
//
//   Optionally `"session_url"`: the acting session's permalink, exactly as the
//   `config_history` MCP tool returns it (§15.9). When absent this module
//   builds the same link locally from `actor_session` with buildSessionPath, so
//   the route may omit it; supplying it is what makes an externally-hosted UI
//   correct.
//
// The route is read-only, and nothing here writes: records appear only as the
// shadow of a real mutation (§15.4).

import { buildSessionPath } from './permalink.js'

/** The read route this UI needs. J2/J3 owns adding the handler behind it. */
export const CONFIG_LOG_ENDPOINT = '/agent/config-events'

// ---------------------------------------------------------------------------
// The record (§15.2)
// ---------------------------------------------------------------------------

/** The closed §15.3 action vocabulary, in the engine's ConfigActions order. */
export const CONFIG_ACTIONS = [
  'worker_create',
  'worker_update',
  'worker_enable',
  'worker_disable',
  'worker_freeze',
  'worker_unfreeze',
  'worker_delete',
  'worker_prompt_write',
  'project_prompt_write',
  'project_settings_put',
  'subscription_create',
  'subscription_update',
  'subscription_delete',
  'schedule_create',
  'schedule_update',
  'schedule_delete',
  'image_create',
  'skill_create',
] as const
export type ConfigAction = (typeof CONFIG_ACTIONS)[number]

/** One record in the append-only configuration log. */
export interface ConfigEvent {
  id: string
  project: string
  actor_worker: string
  actor_session: string
  action: string
  /** The FULL new state of the mutated row, never a diff (§15.2). */
  payload: Record<string, unknown>
  /** Commit-message-style *why*; required on prompt writes (§15.5). */
  rationale: string
  /** Unix MILLISECONDS — finer than the rest of agentdb, deliberately (J1). */
  created_at: number
  /** The acting session's permalink, when the server supplies one (§15.9). */
  session_url?: string
}

const num = (v: unknown, fallback = 0): number =>
  typeof v === 'number' && Number.isFinite(v) ? v : fallback
const str = (v: unknown, fallback = ''): string => (typeof v === 'string' ? v : fallback)

export function coerceConfigEvent(raw: unknown): ConfigEvent {
  const r = raw && typeof raw === 'object' && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {}
  const ev: ConfigEvent = {
    id: str(r.id),
    project: str(r.project),
    actor_worker: str(r.actor_worker),
    actor_session: str(r.actor_session),
    action: str(r.action),
    payload:
      r.payload && typeof r.payload === 'object' && !Array.isArray(r.payload)
        ? (r.payload as Record<string, unknown>)
        : {},
    rationale: str(r.rationale),
    created_at: num(r.created_at),
  }
  if (typeof r.session_url === 'string' && r.session_url !== '') ev.session_url = r.session_url
  return ev
}

// ---------------------------------------------------------------------------
// What changed, and how to name it
// ---------------------------------------------------------------------------

export type ConfigEntityKind =
  | 'worker'
  | 'project-prompt'
  | 'project-settings'
  | 'subscription'
  | 'schedule'
  | 'image'
  | 'skill'
  | 'unknown'

/**
 * The identity of the thing an event changed.
 *
 * `key` is what "consecutive events for the same key" means in §15.10 — the
 * grouping the read-time diff is computed within. It is derived from the action
 * plus the payload's own id/name, because the log has no entity column: payload
 * is full state, and full state carries its own identity.
 */
export interface ConfigEntity {
  kind: ConfigEntityKind
  /** The name/id within the kind; '' for the singleton project entities. */
  name: string
  /** `worker:email-answerer`, `project-settings`, `schedule:<id>`, … */
  key: string
}

export function configEntity(ev: Pick<ConfigEvent, 'action' | 'payload'>): ConfigEntity {
  const p = ev.payload ?? {}
  const name = (k: string): string => str(p[k])
  const make = (kind: ConfigEntityKind, n: string): ConfigEntity => ({
    kind,
    name: n,
    key: n === '' ? kind : `${kind}:${n}`,
  })
  if (ev.action.startsWith('worker_')) return make('worker', name('name'))
  if (ev.action === 'project_prompt_write') return make('project-prompt', '')
  if (ev.action === 'project_settings_put') return make('project-settings', '')
  if (ev.action.startsWith('subscription_')) return make('subscription', name('id'))
  if (ev.action.startsWith('schedule_')) return make('schedule', name('id'))
  if (ev.action === 'image_create') {
    const v = p.version
    const label = typeof v === 'number' ? `${name('name')}:${v}` : name('name')
    return make('image', label)
  }
  if (ev.action === 'skill_create') return make('skill', name('name'))
  return make('unknown', '')
}

/** The verb, as a human would say it — "hired", "retired", "rewrote". */
export function describeConfigAction(action: string): string {
  switch (action) {
    case 'worker_create':
      return 'Hired worker'
    case 'worker_update':
      return 'Updated worker'
    case 'worker_enable':
      return 'Enabled worker'
    case 'worker_disable':
      return 'Disabled worker'
    case 'worker_freeze':
      // Frozen — cannot be changed by other workers (10-topology-library §3).
      return 'Froze worker'
    case 'worker_unfreeze':
      return 'Unfroze worker'
    case 'worker_delete':
      return 'Retired worker'
    case 'worker_prompt_write':
      return 'Rewrote the prompt of worker'
    case 'project_prompt_write':
      return 'Rewrote the project prompt'
    case 'project_settings_put':
      return 'Changed project settings'
    case 'subscription_create':
      return 'Created subscription'
    case 'subscription_update':
      return 'Retuned subscription'
    case 'subscription_delete':
      return 'Deleted subscription'
    case 'schedule_create':
      return 'Created schedule'
    case 'schedule_update':
      return 'Retuned schedule'
    case 'schedule_delete':
      return 'Deleted schedule'
    case 'image_create':
      return 'Published image'
    case 'skill_create':
      return 'Published skill'
    default:
      return action
  }
}

/** The one-line headline for an entry: verb plus the thing it acted on. */
export function changelogTitle(ev: Pick<ConfigEvent, 'action' | 'payload'>): string {
  const entity = configEntity(ev)
  const verb = describeConfigAction(ev.action)
  return entity.name === '' ? verb : `${verb} “${entity.name}”`
}

/**
 * The prompt text an event carries, or null when it carries none.
 *
 * Only prompts get a diff — they are the large strings whose rewrites §15.10
 * wants read as commits. Worker rows carry `system_prompt`; the project prompt
 * write is H1's route and has no shipped payload shape yet, so the three
 * plausible keys are all accepted rather than guessing one.
 */
export function configPromptText(ev: Pick<ConfigEvent, 'action' | 'payload'>): string | null {
  const p = ev.payload ?? {}
  for (const key of ['system_prompt', 'prompt', 'value']) {
    const v = p[key]
    if (typeof v === 'string') return v
  }
  return null
}

// ---------------------------------------------------------------------------
// Read-time diff (§15.2: "diffs are a read-time concern")
// ---------------------------------------------------------------------------

export interface DiffLine {
  /** `+` added, `-` removed, ` ` unchanged context. */
  type: 'add' | 'del' | 'ctx'
  text: string
}

/** Beyond this many lines a side, the LCS table gets expensive; see diffLines. */
export const DIFF_LINE_BUDGET = 2000

/**
 * Line diff, longest-common-subsequence style — the smallest thing that renders
 * a prompt rewrite honestly.
 *
 * Above DIFF_LINE_BUDGET lines the O(n·m) table would cost more than the answer
 * is worth in a browser, so it degrades to a whole-block replace: still true,
 * just less precise, and never a frozen tab.
 */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = before === '' ? [] : before.split('\n')
  const b = after === '' ? [] : after.split('\n')
  if (a.length > DIFF_LINE_BUDGET || b.length > DIFF_LINE_BUDGET) {
    return [
      ...a.map((text): DiffLine => ({ type: 'del', text })),
      ...b.map((text): DiffLine => ({ type: 'add', text })),
    ]
  }

  // lcs[i][j] = length of the LCS of a[i:] and b[j:].
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () =>
    new Array<number>(b.length + 1).fill(0),
  )
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i]![j] = a[i] === b[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!)
    }
  }

  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ type: 'ctx', text: a[i]! })
      i++
      j++
    } else if (lcs[i + 1]![j]! >= lcs[i]![j + 1]!) {
      out.push({ type: 'del', text: a[i]! })
      i++
    } else {
      out.push({ type: 'add', text: b[j]! })
      j++
    }
  }
  while (i < a.length) out.push({ type: 'del', text: a[i++]! })
  while (j < b.length) out.push({ type: 'add', text: b[j++]! })
  return out
}

/** A prompt rewrite, diffed against the previous state of the same key. */
export interface ChangelogDiff {
  /** The older event this was diffed against. */
  previousEventId: string
  lines: DiffLine[]
  added: number
  removed: number
}

// ---------------------------------------------------------------------------
// The changelog
// ---------------------------------------------------------------------------

/** One rendered entry: the record, plus everything read-time computes from it. */
export interface ChangelogEntry {
  event: ConfigEvent
  id: string
  action: string
  entity: ConfigEntity
  title: string
  /** The rationale, shown as the commit message it is (§15.10). */
  rationale: string
  actorWorker: string
  actorSession: string
  /** Deep link to the acting session; null for human/API edits (no actor). */
  sessionPath: string | null
  /** Unix milliseconds. */
  createdAt: number
  /** Null unless this event rewrote a prompt AND an older state exists. */
  diff: ChangelogDiff | null
}

export interface BuildChangelogOptions {
  /**
   * Project id, used to build the actor-session permalink when the server did
   * not supply `session_url`. Empty ⇒ no locally-built link.
   */
  projectId?: string
}

/**
 * Turn config events into changelog entries, newest first.
 *
 * The one piece of real work is the diff: §15.10 says "computed between
 * consecutive events for the same key", so events are grouped by
 * `configEntity().key`, walked oldest→newest, and a prompt-carrying event is
 * diffed against the previous prompt-carrying event of the same key. The very
 * first record for a key has nothing to diff against and gets none — the
 * payload is the whole story there.
 *
 * Input order is not trusted: the route returns newest-first, but a caller
 * concatenating pages could hand us anything, and a diff computed against the
 * wrong neighbour is worse than no diff.
 */
export function buildChangelog(
  events: ConfigEvent[],
  options: BuildChangelogOptions = {},
): ChangelogEntry[] {
  const { projectId = '' } = options
  const oldestFirst = events
    .slice()
    .sort((a, b) => a.created_at - b.created_at || a.id.localeCompare(b.id))

  /** key → the last prompt-carrying event seen for it. */
  const lastPrompt = new Map<string, { id: string; text: string }>()
  const entries: ChangelogEntry[] = []

  for (const event of oldestFirst) {
    const entity = configEntity(event)
    const prompt = configPromptText(event)
    let diff: ChangelogDiff | null = null

    if (prompt !== null) {
      const previous = lastPrompt.get(entity.key)
      if (previous && previous.text !== prompt) {
        const lines = diffLines(previous.text, prompt)
        diff = {
          previousEventId: previous.id,
          lines,
          added: lines.filter((l) => l.type === 'add').length,
          removed: lines.filter((l) => l.type === 'del').length,
        }
      }
      lastPrompt.set(entity.key, { id: event.id, text: prompt })
    }

    entries.push({
      event,
      id: event.id,
      action: event.action,
      entity,
      title: changelogTitle(event),
      rationale: event.rationale,
      actorWorker: event.actor_worker,
      actorSession: event.actor_session,
      sessionPath:
        event.session_url ??
        (event.actor_session && projectId
          ? buildSessionPath(projectId, event.actor_session)
          : null),
      createdAt: event.created_at,
      diff,
    })
  }

  return entries.reverse()
}

// ---------------------------------------------------------------------------
// Filtering (the §15.9 query, applied client-side)
// ---------------------------------------------------------------------------

/** Exact action, or a trailing-`*` prefix like `worker_*` (§15.9). */
export function actionMatches(pattern: string, action: string): boolean {
  if (pattern === '') return true
  if (pattern.endsWith('*')) return action.startsWith(pattern.slice(0, -1))
  return pattern === action
}

/** The §15.9 filter object — every field optional, equality plus a time range. */
export interface ChangelogQuery {
  /** `worker:email-answerer`, `project-settings`, … — an entity key. */
  entity?: string
  /** One action, or a trailing-`*` prefix. */
  action?: string
  actorWorker?: string
  /** Inclusive, unix milliseconds. */
  since?: number
  until?: number
  limit?: number
}

/**
 * Apply a §15.9-shaped query to already-fetched entries.
 *
 * The same austerity as the tool: equality and a range, nothing smarter. It
 * exists so the view can filter without a round trip; the hook passes the same
 * query to the server too, so a narrow filter does not depend on the page size.
 */
export function filterChangelog(
  entries: ChangelogEntry[],
  query: ChangelogQuery = {},
): ChangelogEntry[] {
  const out = entries.filter((e) => {
    if (query.entity && e.entity.key !== query.entity) return false
    if (query.action && !actionMatches(query.action, e.action)) return false
    if (query.actorWorker && e.actorWorker !== query.actorWorker) return false
    if (query.since && e.createdAt < query.since) return false
    if (query.until && e.createdAt > query.until) return false
    return true
  })
  return query.limit && query.limit > 0 ? out.slice(0, query.limit) : out
}

/** Query-string parameters for the read route, from a ChangelogQuery. */
export function changelogQueryParams(query: ChangelogQuery = {}): URLSearchParams {
  const params = new URLSearchParams()
  if (query.action) params.set('action', query.action)
  if (query.actorWorker) params.set('actor_worker', query.actorWorker)
  if (query.since) params.set('since', String(query.since))
  if (query.until) params.set('until', String(query.until))
  if (query.limit && query.limit > 0) params.set('limit', String(query.limit))
  return params
}

/** Unix MILLISECONDS → a local, human-readable stamp. 0/absent renders as ''. */
export function formatConfigTimestamp(ms: number | null | undefined): string {
  if (!ms) return ''
  return new Date(ms).toLocaleString()
}

// ---------------------------------------------------------------------------
// The data seam
// ---------------------------------------------------------------------------

/**
 * How the changelog gets its records.
 *
 * Injectable because the route does not exist yet (see the header). A host with
 * its own gateway, a test with a fixture, and the default HTTP implementation
 * in useConfigLog all satisfy this one function — which is the whole point:
 * when J2/J3 ship `GET /agent/config-events`, nothing above this line changes.
 */
export type ConfigEventFetcher = (query: ChangelogQuery) => Promise<ConfigEvent[]>

/**
 * Pull the records out of whatever the route answered.
 *
 * Accepts `{config_events: [...]}` (the shape asked for above, matching
 * `{events: …}` / `{workers: …}` / `{deliveries: …}` on the sibling routes),
 * `{events: [...]}`, or a bare array — so a host that mounts its own endpoint
 * with a slightly different envelope still works.
 */
export function extractConfigEvents(payload: unknown): ConfigEvent[] {
  const raw = Array.isArray(payload)
    ? payload
    : payload && typeof payload === 'object'
      ? ((payload as Record<string, unknown>).config_events ??
        (payload as Record<string, unknown>).events)
      : null
  return Array.isArray(raw) ? raw.map(coerceConfigEvent) : []
}
