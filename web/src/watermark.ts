// watermark — "since you last looked", as one integer per (operator, surface).
//
// Doc 21 §4.2's framing fact: an event-sourced feed gets "since you last
// looked" almost free, because ONE monotonic mark derives all four of — the
// waterline divider's position, the "N new" count, which items animate on
// entry, and which are highlighted. One integer, four renderings. Two integers
// would be four renderings that disagree.
//
// The mark was already here: `useDesk` kept a `localStorage` high-water mark
// keyed `agentkit.desk.lastSeen.<project>`. This module is that mark
// generalised over surfaces rather than a second storage scheme beside it —
// `watermarkKey('desk', p)` is byte-identical to the key the Desk has been
// writing, so an operator's existing mark survives this change.
//
// Two properties the research insists on:
//
//   1. **Frozen for the visit.** `useFeedWatermark` reads storage once per
//      (surface, project) and holds it. A divider that chases you down the page
//      as items arrive is worse than no divider, and a Changes stack that
//      emptied itself the moment anything else re-rendered is the bug the Desk
//      already avoided this way.
//   2. **Milliseconds, always.** Same rule as `timefmt`: this codebase mixes
//      units (delivery/event rows are unix SECONDS, config events are ms), so
//      the mark is ms and each caller converts in the place that knows which
//      table it read.
//
// Pure except for `useFeedWatermark`: no clock, no React, no storage in the
// functions the tests care about.

import { useCallback, useState } from 'react'
import { formatClock } from './timefmt.js'

/**
 * A feed surface that keeps its own mark. Not a closed union at the type level
 * — a host embedding another list gets its own key by naming it — but these are
 * the two the console ships.
 */
export type FeedSurface = 'desk' | 'events'

/** `agentkit.desk.lastSeen.<project>` — the key the Desk has always used. */
export function watermarkKey(surface: string, projectId: string): string {
  return `agentkit.${surface}.lastSeen.${projectId}`
}

/** Read the mark, in unix ms. Unreadable storage and rubbish both mean 0 —
 *  "never looked", which shows everything fetched. A first visit is not an
 *  empty screen. */
export function readWatermark(surface: string, projectId: string): number {
  try {
    const raw = globalThis.localStorage?.getItem(watermarkKey(surface, projectId))
    const ms = raw === null || raw === undefined ? 0 : Number(raw)
    return Number.isFinite(ms) && ms > 0 ? ms : 0
  } catch {
    return 0
  }
}

/** Write the mark. A storage that refuses is not an error the operator needs:
 *  the surface still works, it just repeats itself next visit. */
export function writeWatermark(surface: string, projectId: string, ms: number): void {
  try {
    globalThis.localStorage?.setItem(watermarkKey(surface, projectId), String(ms))
  } catch {
    /* private mode, quota, no storage. */
  }
}

// ---------------------------------------------------------------------------
// The four renderings
// ---------------------------------------------------------------------------

/** Everything stamped after the mark, and everything at or before it. */
export function partitionByWatermark<T>(
  items: readonly T[],
  atMs: (item: T) => number,
  markMs: number,
): { fresh: T[]; seen: T[] } {
  const fresh: T[] = []
  const seen: T[] = []
  for (const item of items) {
    // `> mark`, never `>=`: the mark is written as "now", and an item stamped
    // at exactly that millisecond was on screen when the operator marked it.
    // An unset mark (0) makes everything "seen" — a first visit has no news.
    if (markMs > 0 && atMs(item) > markMs) fresh.push(item)
    else seen.push(item)
  }
  return { fresh, seen }
}

/** How many items are new. 0 when the operator has never looked — everything
 *  is new to a first visit, and "N new" above a list of exactly N items says
 *  nothing. */
export function countNewSince<T>(
  items: readonly T[],
  atMs: (item: T) => number,
  markMs: number,
): number {
  if (markMs <= 0) return 0
  let n = 0
  for (const item of items) if (atMs(item) > markMs) n += 1
  return n
}

/**
 * Where the divider goes in a NEWEST-FIRST list: the index of the first item
 * at or before the mark, i.e. the count of fresh items. `-1` means no divider —
 * either nothing is new, or everything is (a first visit, or a page that starts
 * after the mark, where a line at the top or the bottom says nothing).
 *
 * Position, not a filter: the seen items stay on screen underneath it. A
 * waterline that hides what is below it is the "since you last looked" filter
 * the Desk already had, and §3 filed that as the defect.
 */
export function waterlineIndex<T>(
  items: readonly T[],
  atMs: (item: T) => number,
  markMs: number,
): number {
  if (markMs <= 0 || items.length === 0) return -1
  const fresh = countNewSince(items, atMs, markMs)
  if (fresh === 0 || fresh === items.length) return -1
  return fresh
}

/** `New since 14:32` — the divider's own label (§4.2). '' when unmarked. */
export function waterlineLabel(markMs: number, nowMs: number = Date.now()): string {
  if (markMs <= 0) return ''
  const clock = formatClock(markMs)
  // A mark from another day is not "since 14:32" — say the day too, using the
  // console's one stamp vocabulary rather than a second one.
  const sameDay = new Date(markMs).toDateString() === new Date(nowMs).toDateString()
  if (sameDay) return `New since ${clock}`
  const d = new Date(markMs)
  return `New since ${d.getDate()}/${d.getMonth() + 1} ${clock}`
}

/**
 * The one debounced sentence the `role="status"` pill announces (§4.2: never
 * per-item announcements) — "3 new deliveries, 1 failed".
 *
 * Both noun forms are parameters rather than an `+ 's'` rule, because the
 * console's own vocabulary includes `delivery`/`deliveries`, and a screen
 * reader announcing "3 new deliverys" is the kind of small wrongness that
 * makes an operator stop trusting the voice.
 */
export function newItemsSummary(
  total: number,
  singular: string,
  plural: string = `${singular}s`,
  qualifiers: readonly { count: number; label: string }[] = [],
): string {
  if (total <= 0) return ''
  const head = `${total} new ${total === 1 ? singular : plural}`
  const tail = qualifiers
    .filter((q) => q.count > 0)
    .map((q) => `${q.count} ${q.label}`)
    .join(', ')
  return tail === '' ? head : `${head}, ${tail}`
}

// ---------------------------------------------------------------------------
// The hook
// ---------------------------------------------------------------------------

export interface FeedWatermark {
  /** Unix MILLISECONDS; 0 means "never looked". Frozen for the visit. */
  markMs: number
  /** Advance the mark to now, and persist it. */
  mark: () => void
  /** Advance it to a given instant (tests, and a host that owns the clock). */
  markAt: (ms: number) => void
}

/**
 * The mark for one surface, frozen for the visit.
 *
 * `override` replaces storage entirely — for tests, and for a host that keeps
 * the mark server-side and hands it down. `mark()` still fires so the host can
 * observe the intent; it simply is not what the component reads back.
 */
export function useFeedWatermark(
  surface: string,
  projectId: string,
  override?: number,
): FeedWatermark {
  const key = watermarkKey(surface, projectId)
  const [held, setHeld] = useState(() => ({ key, markMs: readWatermark(surface, projectId) }))

  // Render-phase re-read on a key change, the pattern this package uses for
  // one-shot init (useEventsOverview's ref-guard): switching project must
  // switch the mark with it, and an effect would render one frame of the wrong
  // project's "new" state first.
  if (held.key !== key) {
    setHeld({ key, markMs: readWatermark(surface, projectId) })
  }

  const markAt = useCallback(
    (ms: number) => {
      writeWatermark(surface, projectId, ms)
      setHeld({ key: watermarkKey(surface, projectId), markMs: ms })
    },
    [projectId, surface],
  )

  const mark = useCallback(() => markAt(Date.now()), [markAt])

  return { markMs: override ?? (held.key === key ? held.markMs : 0), mark, markAt }
}

export default useFeedWatermark
