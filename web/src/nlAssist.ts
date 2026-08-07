// nlAssist — "describe when this should fire" → a cron expression or an
// envelope filter, compiled AT CONFIG TIME and echoed back for confirmation.
// Work-plan item F2, learning L28.
//
// # The one rule
//
// **Config time only, never the firing path.** What gets saved is the compiled
// artifact — five cron fields, or a flat equality map — and from then on the
// scheduler and the router behave exactly as they would for an expression typed
// by hand. Nothing in this file is consulted when a schedule fires or an event
// is routed, and nothing here is ever saved without a human seeing the result
// first: `compile*` returns a proposal, the editor shows it, and the human
// clicks Apply. A description that produces a cron nobody read is worse than no
// assist at all, because it looks like it was checked.
//
// # Deterministic first, model second
//
// The built-in compiler is a small deterministic grammar over the phrasings
// people actually type ("every weekday at 09:00", "every 15 minutes", "when the
// email-answerer worker finishes"). It refuses everything else, loudly, rather
// than guessing — a wrong cron that parses is the expensive failure here, since
// it will run wrong every day until somebody notices.
//
// A host that wants more can inject an LLM through the `NlCompiler` seam: same
// shape, same confirmation step, same refusal semantics. Absent the injection,
// the deterministic compiler is what runs, which is why the assist works
// offline and in tests.
//
// Pure: no React, no window, no fetch.

import { ENVELOPE_FILTER_KEYS } from './events.js'
import { describeCron, validateCron, WEEKDAY_LABELS } from './schedules.js'

/** A compiler's answer. Discriminated so a caller cannot read `value` without
 *  having checked `ok`, exactly like projectSettings' JsonParseResult. */
export type CompileResult<T> =
  | { ok: true; value: T; explanation: string }
  | { ok: false; error: string }

/** A compiled schedule proposal: the expression, and what it means in words. */
export interface CronProposal {
  cron: string
}

/** A compiled subscription proposal. */
export interface FilterProposal {
  /** Event-type pattern (exact or trailing-`*`), when the text named one. */
  event_type?: string
  /** Equality match on envelope fields. */
  filter: Record<string, string | number | boolean>
}

/** The injectable seam: a host may compile with a model instead. It must obey
 *  the same contract — propose, never save, and refuse rather than guess. */
export type NlCompiler<T> = (text: string) => Promise<CompileResult<T>>

const DAY_WORDS: Record<string, number> = {
  sunday: 0, sun: 0,
  monday: 1, mon: 1,
  tuesday: 2, tue: 2, tues: 2,
  wednesday: 3, wed: 3,
  thursday: 4, thu: 4, thur: 4, thurs: 4,
  friday: 5, fri: 5,
  saturday: 6, sat: 6,
}

const NUMBER_WORDS: Record<string, number> = {
  a: 1, an: 1, one: 1, two: 2, three: 3, four: 4, five: 5, six: 6,
  seven: 7, eight: 8, nine: 9, ten: 10, twelve: 12, fifteen: 15, twenty: 20, thirty: 30,
}

/** Parse "9am", "09:00", "17:30", "5.30pm", "midnight", "noon" → [hour, minute]. */
function parseClock(text: string): [number, number] | null {
  if (/\bmidnight\b/.test(text)) return [0, 0]
  if (/\b(noon|midday)\b/.test(text)) return [12, 0]
  const m = /\b(\d{1,2})(?:[:.](\d{2}))?\s*(am|pm)?\b/.exec(text)
  if (m === null) return null
  let hour = Number(m[1])
  const minute = m[2] === undefined ? 0 : Number(m[2])
  const meridiem = m[3]
  if (meridiem === 'pm' && hour < 12) hour += 12
  if (meridiem === 'am' && hour === 12) hour = 0
  if (hour > 23 || minute > 59) return null
  // A bare "9" with no am/pm and no colon is ambiguous enough to be dangerous
  // ("every 9 minutes"? "at 9"?), so it is only a clock when the text said so.
  if (meridiem === undefined && m[2] === undefined && !/\b(at|from)\b/.test(text)) return null
  return [hour, minute]
}

function daysOfWeek(text: string): number[] {
  if (/\bweekdays?\b/.test(text) || /\bworking days\b/.test(text)) return [1, 2, 3, 4, 5]
  if (/\bweekends?\b/.test(text)) return [0, 6]
  const found = new Set<number>()
  for (const [word, value] of Object.entries(DAY_WORDS)) {
    if (new RegExp(`\\b${word}s?\\b`).test(text)) found.add(value)
  }
  return [...found].sort((a, b) => a - b)
}

function interval(text: string, unit: 'minute' | 'hour'): number | null {
  const re = new RegExp(`\\bevery\\s+(\\d+|[a-z]+)\\s*(?:${unit}s?)\\b`)
  const m = re.exec(text)
  if (m === null) {
    // "every hour" / "every minute" with no count.
    return new RegExp(`\\bevery\\s+${unit}\\b`).test(text) ? 1 : null
  }
  const raw = m[1]
  const n = /^\d+$/.test(raw) ? Number(raw) : NUMBER_WORDS[raw]
  return n === undefined || n < 1 ? null : n
}

/**
 * Compile a natural-language description into a five-field cron expression.
 *
 * Understood, and nothing else:
 *   - "every N minutes" / "every N hours" / "every hour"
 *   - "every day at 09:00" / "daily at 9am" / "at midnight"
 *   - "every weekday at 17:30" / "on weekends at noon"
 *   - "every Monday and Thursday at 08:00" / "on Fridays at 6pm"
 *   - "on the 1st of the month at 09:00"
 *
 * Refused: anything with no recognisable time, sub-minute rates ("every 30
 * seconds" — the scheduler wakes once a minute), cron nicknames (`@daily`, as
 * the engine refuses them too), and anything ambiguous.
 */
export function compileCron(text: string): CompileResult<CronProposal> {
  const raw = text.trim()
  if (raw === '') return { ok: false, error: 'Describe when this should fire, e.g. "every weekday at 09:00".' }
  const lower = raw.toLowerCase()

  if (lower.startsWith('@')) {
    return {
      ok: false,
      error: 'Cron nicknames like @daily are not supported — describe the timing in words instead ("every day at midnight").',
    }
  }
  if (/\bsecond(s)?\b/.test(lower)) {
    return {
      ok: false,
      error: 'The scheduler wakes once a minute, so sub-minute timings cannot be expressed. The fastest is "every minute".',
    }
  }

  // Already a cron expression? Accept it rather than making a human retype it.
  if (validateCron(raw) === null) {
    return { ok: true, value: { cron: raw }, explanation: describeCron(raw) ?? raw }
  }

  const everyMinutes = interval(lower, 'minute')
  const everyHours = interval(lower, 'hour')
  const clock = parseClock(lower)
  const days = daysOfWeek(lower)
  const dayOfMonth = /\b(?:on the\s+)?(\d{1,2})(?:st|nd|rd|th)\b/.exec(lower)

  let minute: string
  let hour: string
  if (everyMinutes !== null) {
    if (everyMinutes > 59) {
      return { ok: false, error: `"every ${everyMinutes} minutes" does not divide an hour — say it in hours instead.` }
    }
    minute = everyMinutes === 1 ? '*' : `*/${everyMinutes}`
    hour = '*'
  } else if (everyHours !== null) {
    if (everyHours > 23) {
      return { ok: false, error: `"every ${everyHours} hours" does not divide a day — describe the times of day instead.` }
    }
    minute = clock === null ? '0' : String(clock[1])
    hour = everyHours === 1 ? '*' : `*/${everyHours}`
  } else if (clock !== null) {
    minute = String(clock[1])
    hour = String(clock[0])
  } else {
    return {
      ok: false,
      error:
        'I could not find a time in that. Try "every day at 09:00", "every weekday at 17:30", or "every 15 minutes" — ' +
        'or write the five cron fields yourself.',
    }
  }

  const dom = dayOfMonth === null ? '*' : String(Number(dayOfMonth[1]))
  const dow = days.length === 0 ? '*' : days.join(',')
  if (dom !== '*' && dow !== '*') {
    return {
      ok: false,
      error:
        'A day of the month AND a day of the week means "either one" in cron, which is almost never what people mean. ' +
        'Pick one.',
    }
  }
  if (dom !== '*' && (Number(dom) < 1 || Number(dom) > 31)) {
    return { ok: false, error: `There is no day ${dom} of the month.` }
  }

  const expr = `${minute} ${hour} ${dom} * ${dow}`
  const problem = validateCron(expr)
  if (problem !== null) {
    return { ok: false, error: `That produced an invalid expression (${expr}): ${problem}` }
  }
  return {
    ok: true,
    value: { cron: expr },
    explanation: describeCron(expr) ?? expr,
  }
}

/**
 * Compile a natural-language description into an event-type pattern and an
 * envelope filter.
 *
 * Understood: an event type quoted or named plainly ("email.received",
 * "`worker.finished`", "email.*"), "from/by the <name> worker", "when <name>
 * finishes", "only failures" (reason=error), "ignore chats" (interactive
 * false), "only when attention was requested".
 *
 * Refused: anything that would need a predicate cron and the router cannot
 * express — §8.3 is equality and trailing-`*`, deliberately, and a filter that
 * silently means something looser is worse than no filter.
 */
export function compileEnvelopeFilter(text: string): CompileResult<FilterProposal> {
  const raw = text.trim()
  if (raw === '') {
    return { ok: false, error: 'Describe what should trigger this, e.g. "when the email-answerer worker finishes".' }
  }
  const lower = raw.toLowerCase()

  if (/\b(more than|less than|greater|older than|contains|matches|regex|between)\b/.test(lower)) {
    return {
      ok: false,
      error:
        'Subscriptions match on equality and a trailing * only (§8.3). Anything cleverer belongs in the reacting ' +
        "worker's prompt — subscribe broadly, and let the worker decide it does not care.",
    }
  }

  const proposal: FilterProposal = { filter: {} }

  // An explicit event type wins: "email.*", "worker.finished", `config.changed`.
  // No trailing \b: a trailing `*` is a non-word character, so requiring a word
  // boundary after it would reject exactly the wildcard patterns §8.3 allows.
  const typed = /\b([a-z][a-z0-9_]*(?:\.[a-z0-9_*]+)+)/.exec(lower.replace(/[`'"]/g, ''))
  if (typed !== null) proposal.event_type = typed[1]
  else if (/\bfinish(es|ed)?\b/.test(lower)) proposal.event_type = 'worker.finished'
  else if (/\bfail(s|ed|ure|ures)?\b/.test(lower)) proposal.event_type = 'worker.failed'
  else if (/\bconfig(uration)?\b/.test(lower)) proposal.event_type = 'config.changed'
  else if (/\bschedule[ds]?\b/.test(lower)) proposal.event_type = 'schedule.fired'

  const worker =
    /\b(?:from|by|of)\s+(?:the\s+)?([a-z0-9]+(?:-[a-z0-9]+)*)\s+worker\b/.exec(lower) ??
    /\bwhen\s+(?:the\s+)?([a-z0-9]+(?:-[a-z0-9]+)*)\s+(?:worker\s+)?(?:finish|fail)/.exec(lower)
  if (worker !== null) proposal.filter.worker = worker[1]

  if (/\b(only|just)\s+failures?\b/.test(lower) || /\bfailed\s+with\s+an?\s+error\b/.test(lower)) {
    proposal.filter.reason = 'error'
  }
  if (/\b(ignore|not|no|exclude|skip)\b[^.]*\bchat/.test(lower) || /\bnon-?interactive\b/.test(lower)) {
    proposal.filter.interactive = false
  }
  if (/\battention\b/.test(lower)) proposal.filter.attention_requested = true

  if (proposal.event_type === undefined && Object.keys(proposal.filter).length === 0) {
    return {
      ok: false,
      error:
        'I could not find an event type or an envelope field in that. Name the event ("email.received", ' +
        '"worker.finished", "email.*") and optionally the worker ("from the email-answerer worker").',
    }
  }
  for (const key of Object.keys(proposal.filter)) {
    if (!(ENVELOPE_FILTER_KEYS as readonly string[]).includes(key)) {
      return { ok: false, error: `"${key}" is not an envelope field.` }
    }
  }
  return { ok: true, value: proposal, explanation: explainFilterProposal(proposal) }
}

function explainFilterProposal(p: FilterProposal): string {
  const type = p.event_type === undefined ? 'any event' : `an \`${p.event_type}\` event`
  const entries = Object.entries(p.filter)
  if (entries.length === 0) return `Match ${type}.`
  return `Match ${type} whose envelope has ${entries.map(([k, v]) => `${k}=${String(v)}`).join(' and ')}.`
}

/** Human-readable list of the weekdays a compiled cron covers — used by the
 *  editor's confirmation line when it wants more than describeCron's sentence. */
export function weekdayNames(dow: string): string[] {
  if (dow === '*') return [...WEEKDAY_LABELS]
  return dow
    .split(',')
    .map((d) => WEEKDAY_LABELS[Number(d) % 7])
    .filter((d) => d !== undefined)
}
