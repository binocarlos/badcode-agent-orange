// timefmt — the console's one compact time vocabulary (doc 21 §2 X8).
//
// The populated walkthrough found `7/28/2026, 6:43:20 AM` in every surface: a
// full US-locale stamp, four times the width of the thing it says, in a column
// whose job is "when, roughly". The design shows compact times, so every stamp
// in the console now goes through here:
//
//   today        14:32
//   this week    Mon 14:32
//   older        21 Jul 2026
//
// plus `agoShort` for open states — `3h`, with no "ago" (the word is implied by
// every place it renders, and the column is narrow).
//
// Two deliberate decisions:
//
//  1. **Explicit milliseconds, always.** Timestamps in this codebase are mixed
//     units — `agent_*` rows are unix SECONDS, `config_events` and `memories`
//     are MILLISECONDS — and every past bug in this area was a unit guess. This
//     module takes ms and nothing else; each call site converts, in the one
//     place that knows which table it read.
//  2. **Hand-built strings, not `toLocaleString`.** The forms above are fixed
//     by the design (24-hour clock, three-letter weekday and month), so a
//     formatter whose output depends on the viewer's locale would be a
//     different design per browser — and untestable besides. Only the *clock*
//     is local: the parts come from a local-time `Date`.

/** Three-letter weekday names, indexed by `Date#getDay`. */
const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'] as const

/** Three-letter month names, indexed by `Date#getMonth`. */
const MONTHS = [
  'Jan',
  'Feb',
  'Mar',
  'Apr',
  'May',
  'Jun',
  'Jul',
  'Aug',
  'Sep',
  'Oct',
  'Nov',
  'Dec',
] as const

const pad2 = (n: number): string => (n < 10 ? `0${n}` : String(n))

/** Local midnight of the day `d` falls in, as unix ms — the day boundary this
 *  module counts in (calendar days, not 24-hour blocks: 23:58 and 00:02 are
 *  four minutes and one day apart, and the second one is not "today"). */
function startOfDayMs(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

/** Whole calendar days between two instants, positive when `ms` is in the past. */
export function calendarDaysAgo(ms: number, nowMs: number): number {
  const day = 24 * 60 * 60 * 1000
  return Math.round((startOfDayMs(new Date(nowMs)) - startOfDayMs(new Date(ms))) / day)
}

/** `14:32` — 24-hour local time. */
export function formatClock(ms: number): string {
  const d = new Date(ms)
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`
}

/** `21 Jul 2026` — the form for anything older than a week. */
export function formatDay(ms: number): string {
  const d = new Date(ms)
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}`
}

/**
 * The console's stamp: `14:32` today, `Mon 14:32` earlier this week,
 * `21 Jul 2026` beyond it. 0/null/absent renders as `''`, matching the
 * formatters this replaced — an unset timestamp is not "1 Jan 1970".
 *
 * `nowMs` is a parameter so a rendered stamp is testable; it defaults to the
 * wall clock for the ordinary call.
 */
export function formatCompactTime(
  ms: number | null | undefined,
  nowMs: number = Date.now(),
): string {
  if (!ms) return ''
  const days = calendarDaysAgo(ms, nowMs)
  if (days === 0) return formatClock(ms)
  // 1–6 days back is "this week"; anything in the future, or older, gets the
  // unambiguous date — a weekday name only reads as recent looking backwards.
  if (days >= 1 && days <= 6) return `${WEEKDAYS[new Date(ms).getDay()]} ${formatClock(ms)}`
  return formatDay(ms)
}

/**
 * A short age: `3h`, `12m`, `4d`, `2w`. No "ago" — every place this renders
 * already says it, and the word is a third of the column.
 *
 * Under a minute reads `now`, and so does anything in the future: a clock skew
 * of a few seconds must not render `-1m`.
 */
export function agoShort(ms: number | null | undefined, nowMs: number = Date.now()): string {
  if (!ms) return ''
  const seconds = Math.floor((nowMs - ms) / 1000)
  if (seconds < 60) return 'now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days}d`
  return `${Math.floor(days / 7)}w`
}
