// Schedules — the browser-side mirror of the `/agent/schedules` CRUD routes
// (spec docs/product/04-events-and-schedules.md §8.6, engine:
// go/agentdb/schedules.go, go/httpapi/schedules.go). Work-plan item F2.
//
// Pure: no React, no window, no fetch. The hook lives in useSchedules.ts and
// the components in components/Schedule*.tsx.
//
// Validation policy: mirror the engine, do not out-legislate it. The server
// enforces exactly three things — a worker, a cron expression, and that the
// expression parses under its own five-field grammar — so this module enforces
// exactly those. The cron parser here is a second implementation of a rule the
// engine owns (`ParseCron`), confined to the grammar as it stands so there is
// no third dialect to drift on: if §8.6's grammar changes, this file and
// go/agentdb/schedules.go must move together. It exists because "is this cron
// valid, and what does it mean?" is a question a human needs answered while
// typing, not after a round trip.
//
// Note what the engine deliberately REFUSES and this mirrors: cron nicknames
// (`@daily`, `@hourly`). The spec says five fields, and quietly accepting a
// second syntax is how "every minute" happens by accident.

/** Endpoint paths for the schedule routes. Overridable per host. */
export const SCHEDULE_ENDPOINTS = {
  list: '/agent/schedules',
  one: (id: string) => `/agent/schedules/${encodeURIComponent(id)}`,
}

/** One scheduled trigger. Field names are the wire names (snake_case). */
export interface Schedule {
  id: string
  project: string
  worker: string
  /** Standard 5-field cron expression, in agentd's local time zone. */
  cron: string
  /** The instruction this trigger delivers — it becomes the event text. */
  input: string
  enabled: boolean
  created_at: number
  updated_at: number
  /**
   * CONSECUTIVE firings that could not be started, from the engine's runtime
   * state (`ScheduleMaxProvisionFailures` = 5 disables the schedule). Read-only
   * here — no editor writes it — and optional so a draft built by hand stays
   * legal. The Desk's Trouble stack reads it (desk.ts).
   */
  provision_failures?: number
  /** Why the most recent start failed, kept so an operator can recover. */
  last_provision_error?: string
}

/** A schedule being edited. Same shape; the alias makes props read honestly. */
export type ScheduleDraft = Schedule

const num = (v: unknown, fallback = 0): number =>
  typeof v === 'number' && Number.isFinite(v) ? v : fallback
const str = (v: unknown, fallback = ''): string => (typeof v === 'string' ? v : fallback)
const bool = (v: unknown, fallback = false): boolean => (typeof v === 'boolean' ? v : fallback)

/** A blank schedule. `enabled` defaults true, matching the engine's
 *  NewSchedule — a zero-valued schedule would never fire. */
export function newScheduleDraft(project = ''): ScheduleDraft {
  return {
    id: '',
    project,
    worker: '',
    cron: '',
    input: '',
    enabled: true,
    created_at: 0,
    updated_at: 0,
    provision_failures: 0,
    last_provision_error: '',
  }
}

export function coerceSchedule(raw: unknown, project = ''): Schedule {
  const base = newScheduleDraft(project)
  if (!raw || typeof raw !== 'object') return base
  const r = raw as Record<string, unknown>
  return {
    id: str(r.id),
    project: str(r.project, project),
    worker: str(r.worker),
    cron: str(r.cron),
    input: str(r.input),
    enabled: bool(r.enabled, true),
    created_at: num(r.created_at),
    updated_at: num(r.updated_at),
    provision_failures: num(r.provision_failures),
    last_provision_error: str(r.last_provision_error),
  }
}

/** The write body. `project`, `id` and the timestamps are the server's. */
export function scheduleBody(s: ScheduleDraft, rationale = ''): {
  worker: string
  cron: string
  input: string
  enabled: boolean
  rationale?: string
} {
  const body = {
    worker: s.worker.trim(),
    cron: s.cron.trim(),
    input: s.input,
    enabled: s.enabled,
  }
  // The route threads `rationale` into the config event (§15.5). Omitted when
  // empty rather than sent blank, so an absent commit message reads as absent.
  return rationale.trim() === '' ? body : { ...body, rationale: rationale.trim() }
}

// ---------------------------------------------------------------------------
// Cron — the grammar of go/agentdb/schedules.go:ParseCron
// ---------------------------------------------------------------------------

export const CRON_FIELD_NAMES = ['minute', 'hour', 'day-of-month', 'month', 'day-of-week'] as const
export type CronFieldName = (typeof CRON_FIELD_NAMES)[number]

const CRON_BOUNDS: Record<CronFieldName, [number, number]> = {
  minute: [0, 59],
  hour: [0, 23],
  'day-of-month': [1, 31],
  month: [1, 12],
  'day-of-week': [0, 7], // 0 and 7 are both Sunday
}

const MONTH_NAMES = ['JAN', 'FEB', 'MAR', 'APR', 'MAY', 'JUN', 'JUL', 'AUG', 'SEP', 'OCT', 'NOV', 'DEC']
const DAY_NAMES = ['SUN', 'MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT']

/** Long day names, indexed as cron's day-of-week 0–6. */
export const WEEKDAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
const MONTH_LABELS = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
]

function cronAtom(field: CronFieldName, token: string): number | null {
  const t = token.toUpperCase()
  if (field === 'month') {
    const i = MONTH_NAMES.indexOf(t)
    if (i >= 0) return i + 1
  }
  if (field === 'day-of-week') {
    const i = DAY_NAMES.indexOf(t)
    if (i >= 0) return i
  }
  if (!/^\d+$/.test(t)) return null
  return Number(t)
}

/**
 * Validate one cron field, returning a problem sentence or null.
 * Exported because the editor points at the offending field by name.
 */
export function validateCronField(field: CronFieldName, spec: string): string | null {
  const [lo, hi] = CRON_BOUNDS[field]
  if (spec === '') return `empty ${field} field`
  for (const element of spec.split(',')) {
    if (element === '') return `empty list element in "${spec}"`
    const [rangePart, stepPart, ...extra] = element.split('/')
    if (extra.length > 0) return `"${element}" has more than one step`
    if (stepPart !== undefined) {
      if (!/^\d+$/.test(stepPart) || Number(stepPart) < 1) {
        return `step "${stepPart}" in "${element}" must be a positive integer`
      }
    }
    if (rangePart === '*') continue
    const bounds = rangePart.split('-')
    if (bounds.length > 2) return `"${rangePart}" is not a range`
    const start = cronAtom(field, bounds[0])
    if (start === null) {
      return field === 'month'
        ? `"${bounds[0]}" is not a number or a month name (JAN-DEC)`
        : field === 'day-of-week'
          ? `"${bounds[0]}" is not a number or a day name (SUN-SAT)`
          : `"${bounds[0]}" is not a number`
    }
    if (start < lo || start > hi) return `${start} is out of range (${lo}-${hi})`
    if (bounds.length === 2) {
      const end = cronAtom(field, bounds[1])
      if (end === null) return `"${bounds[1]}" is not a number`
      if (end < lo || end > hi) return `${end} is out of range (${lo}-${hi})`
      if (start > end) return `range "${rangePart}" is inverted (${start} > ${end})`
    }
  }
  return null
}

/**
 * Validate a whole cron expression, returning a problem sentence or null.
 * The messages mirror the engine's, so a rejection reads the same whether the
 * browser or the server caught it.
 */
export function validateCron(expr: string): string | null {
  const trimmed = expr.trim()
  if (trimmed === '') {
    return 'cron expression is empty (want 5 fields: minute hour day-of-month month day-of-week)'
  }
  if (trimmed.startsWith('@')) {
    return 'nicknames like @daily are not supported — write the 5 fields (e.g. `0 0 * * *` for daily at midnight)'
  }
  const fields = trimmed.split(/\s+/)
  if (fields.length !== 5) {
    return `want exactly 5 fields (minute hour day-of-month month day-of-week), got ${fields.length}`
  }
  for (let i = 0; i < 5; i += 1) {
    const problem = validateCronField(CRON_FIELD_NAMES[i], fields[i])
    if (problem !== null) return `${CRON_FIELD_NAMES[i]} field: ${problem}`
  }
  return null
}

/** Expand one field to the values it matches, for describeCron. */
function cronValues(field: CronFieldName, spec: string): number[] {
  const [lo, hi] = CRON_BOUNDS[field]
  const out = new Set<number>()
  for (const element of spec.split(',')) {
    const [rangePart, stepPart] = element.split('/')
    const step = stepPart === undefined ? 1 : Number(stepPart)
    let start = lo
    let end = hi
    if (rangePart !== '*') {
      const bounds = rangePart.split('-')
      start = cronAtom(field, bounds[0]) ?? lo
      end = bounds.length === 2 ? (cronAtom(field, bounds[1]) ?? hi) : (stepPart === undefined ? start : hi)
    }
    for (let v = start; v <= end; v += step) out.add(v)
  }
  return [...out].sort((a, b) => a - b)
}

const pad = (n: number) => String(n).padStart(2, '0')

function listPhrase(parts: string[]): string {
  if (parts.length === 0) return ''
  if (parts.length === 1) return parts[0]
  if (parts.length === 2) return `${parts[0]} and ${parts[1]}`
  return `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}`
}

function contiguous(values: number[]): boolean {
  return values.every((v, i) => i === 0 || v === values[i - 1] + 1)
}

/**
 * Describe a cron expression in words — the "echo it back for confirmation"
 * half of the NL assist, and the helper text under the field when a human
 * types the expression themselves.
 *
 * Returns null when the expression does not parse; the caller shows the
 * validation message instead of a description of something meaningless.
 */
export function describeCron(expr: string): string | null {
  if (validateCron(expr) !== null) return null
  const [minute, hour, dom, month, dow] = expr.trim().split(/\s+/)

  // Times of day.
  let when: string
  const minutes = cronValues('minute', minute)
  const hours = cronValues('hour', hour)
  if (minute === '*' && hour === '*') {
    when = 'every minute'
  } else if (minute.startsWith('*/') && hour === '*') {
    when = `every ${minute.slice(2)} minutes`
  } else if (hour === '*') {
    when = `at ${listPhrase(minutes.map((m) => `minute ${m}`))} of every hour`
  } else if (minutes.length === 1 && hours.length === 1) {
    when = `at ${pad(hours[0])}:${pad(minutes[0])}`
  } else if (minutes.length === 1 && hour.startsWith('*/')) {
    when = `every ${hour.slice(2)} hours at ${pad(minutes[0])} past`
  } else if (minutes.length === 1) {
    when = `at ${listPhrase(hours.map((h) => `${pad(h)}:${pad(minutes[0])}`))}`
  } else {
    when = `at minute ${listPhrase(minutes.map(String))} of hour ${listPhrase(hours.map(String))}`
  }

  // Days.
  const days: string[] = []
  if (dow !== '*') {
    const values = cronValues('day-of-week', dow).map((d) => (d === 7 ? 0 : d))
    const unique = [...new Set(values)].sort((a, b) => a - b)
    if (unique.length === 5 && unique.join() === '1,2,3,4,5') {
      days.push('on weekdays')
    } else if (unique.length === 2 && unique.join() === '0,6') {
      days.push('at weekends')
    } else if (unique.length >= 3 && contiguous(unique)) {
      days.push(`from ${WEEKDAY_LABELS[unique[0]]} to ${WEEKDAY_LABELS[unique[unique.length - 1]]}`)
    } else {
      days.push(`on ${listPhrase(unique.map((d) => WEEKDAY_LABELS[d]))}`)
    }
  }
  if (dom !== '*') {
    const values = cronValues('day-of-month', dom)
    days.push(`on day ${listPhrase(values.map(String))} of the month`)
  }
  if (month !== '*') {
    const values = cronValues('month', month)
    days.push(`in ${listPhrase(values.map((m) => MONTH_LABELS[m - 1]))}`)
  }

  // The dom/dow union rule: when BOTH are restricted, cron fires if EITHER
  // matches. Saying so is the difference between a description and a trap.
  const both = dom !== '*' && dow !== '*'
  const scope = days.length === 0 ? 'every day' : days.join(both ? ' OR ' : ', ')
  // "Every 15 minutes, every day" is noise; "At 09:00, every day" is not.
  const omitScope = days.length === 0 && when.startsWith('every ')
  const text = omitScope ? when : `${when}, ${scope}`
  return text.charAt(0).toUpperCase() + text.slice(1) + '.'
}

// ---------------------------------------------------------------------------
// Whole-schedule validation
// ---------------------------------------------------------------------------

/** Field name → human-readable problem. Empty object means "safe to save". */
export type ScheduleFieldErrors = Record<string, string>

export function validateSchedule(s: ScheduleDraft): ScheduleFieldErrors {
  const errors: ScheduleFieldErrors = {}
  if (s.worker.trim() === '') errors.worker = 'worker is required'
  const cronProblem = validateCron(s.cron)
  if (cronProblem !== null) errors.cron = cronProblem
  // `input` may legitimately be empty — the engine does not require it — but an
  // empty instruction wakes a worker with nothing to do, so the editor says so
  // as helper text rather than as an error.
  return errors
}

// ---------------------------------------------------------------------------
// Router-free URL helpers (the WorkersPage/EventsPage convention)
// ---------------------------------------------------------------------------

export const SCHEDULE_QUERY_PARAM = 'schedule'

export function scheduleFromSearch(search: string): string | null {
  return new URLSearchParams(search).get(SCHEDULE_QUERY_PARAM)
}

export function buildScheduleSearch(search: string, id: string | null): string {
  const params = new URLSearchParams(search)
  if (id === null || id === '') params.delete(SCHEDULE_QUERY_PARAM)
  else params.set(SCHEDULE_QUERY_PARAM, id)
  const qs = params.toString()
  return qs === '' ? '' : `?${qs}`
}
