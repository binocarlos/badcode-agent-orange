// useElapsedTicker — one interval for a whole surface, and the rules about
// which durations are allowed to tick at all (doc 21 §4.2, §5-M4 step 5).
//
// The research on elapsed timers is more negative than we expected: a ticking
// second hand "accentuates the passing of each and every second", and on a
// screen full of them it is pure anxiety with no decision attached. The one
// rule that survived: **tick only when knowing the elapsed IS the operator's
// decision input.** Which yields exactly this table —
//
//   running         ticks per second for the first two minutes, then coarsens
//                   to 60s: past two minutes nobody is reading the seconds.
//   awaiting_human  ticks, and ESCALATES (amber at 1h, fault at 4h). This one
//                   is not a progress indicator, it is an SLA on the operator:
//                   the number is the call to action.
//   ok / failed /   static totals. A ticking clock on a finished thing is a
//   rate_limited    bug, and one this console shipped (§3: "a `running`
//                   delivery looks identical to one dead for an hour").
//
// §4.2 also wanted `rate_limited` to count DOWN to its retry. It does not, and
// deliberately: `isTerminalDeliveryStatus` counts that status as terminal and
// the delivery row carries no retry-after, so there is no instant to count to.
// Inventing one would be motion with no cause (§4.1 rule 1). Filed, not faked.
//
// Two implementation rules, both from the research: ONE interval per surface,
// never one per row (a hundred rows is a hundred timers and a hundred renders);
// and the elapsed is computed from the SERVER's `started_at`, never accumulated
// client-side, so a backgrounded tab comes back correct rather than behind.
//
// A11y (§4.2): the ticking text is `aria-hidden` and the container carries a
// coarse static label refreshed every few minutes — otherwise a screen reader
// announces every single second. `coarseAgeLabel` is that label.
//
// Pure except for the hook: every decision above is a function of (status,
// seconds) and is what vitest covers.

import { useEffect, useState } from 'react'
import { isTerminalDeliveryStatus } from './events.js'

/** Per-second, for the first stretch of a running job. */
export const TICK_FAST_MS = 1000

/** Per-minute, after it. */
export const TICK_COARSE_MS = 60_000

/** How long a `running` job ticks per second before coarsening. */
export const TICK_COARSE_AFTER_SECONDS = 120

/** An ask this old is amber; §4.2's escalation, in seconds. */
export const ESCALATE_AMBER_SECONDS = 60 * 60

/** And this old is fault. */
export const ESCALATE_FAULT_SECONDS = 4 * 60 * 60

/**
 * How often this status wants re-rendering, in ms. `0` means never — the value
 * is a static total and the row must not repaint.
 */
export function tickIntervalMs(status: string, elapsedSeconds: number): number {
  if (isTerminalDeliveryStatus(status)) return 0
  switch (status) {
    case 'running':
      return elapsedSeconds < TICK_COARSE_AFTER_SECONDS ? TICK_FAST_MS : TICK_COARSE_MS
    case 'awaiting_human':
      // Prominently, and forever: the age is the point of the row.
      return TICK_FAST_MS
    case 'pending':
      // Nothing has started, so nothing is elapsing.
      return 0
    default:
      return 0
  }
}

/**
 * The interval one surface needs: the fastest any of its rows asks for, or 0
 * when nothing on screen is moving. A page whose jobs have all finished holds
 * no timer at all, which is the property that makes "one shared ticker" cheap.
 */
export function tickIntervalForRows(
  rows: readonly { status: string; elapsedSeconds: number }[],
): number {
  let fastest = 0
  for (const row of rows) {
    const ms = tickIntervalMs(row.status, row.elapsedSeconds)
    if (ms === 0) continue
    if (fastest === 0 || ms < fastest) fastest = ms
  }
  return fastest
}

/** How loudly an age is shouting. Nothing is encoded only in this colour — the
 *  number is right beside it (§4.1 rule 4). */
export type AgeEscalation = 'none' | 'amber' | 'fault'

export function ageEscalation(seconds: number): AgeEscalation {
  if (seconds >= ESCALATE_FAULT_SECONDS) return 'fault'
  if (seconds >= ESCALATE_AMBER_SECONDS) return 'amber'
  return 'none'
}

/**
 * The coarse sentence a screen reader gets, on the container, while the ticking
 * digits beside it are `aria-hidden`. Deliberately vague: it is refreshed every
 * few minutes, so it must stay true for a few minutes.
 */
export function coarseAgeLabel(seconds: number): string {
  if (seconds < 60) return 'less than a minute'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `about ${minutes} minute${minutes === 1 ? '' : 's'}`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `about ${hours} hour${hours === 1 ? '' : 's'}`
  const days = Math.floor(hours / 24)
  return `about ${days} day${days === 1 ? '' : 's'}`
}

/** How often the coarse label is allowed to change — every few minutes (§4.2). */
export const COARSE_LABEL_REFRESH_MS = 5 * 60_000

export interface UseElapsedTickerOptions {
  /**
   * The interval, in ms. 0 (the default) holds no timer at all — pass
   * `tickIntervalForRows(rows)` and the surface stops ticking by itself when
   * everything on it has finished.
   */
  intervalMs?: number
  /** WCAG 2.2.2: the operator can stop it. A paused ticker holds no timer. */
  paused?: boolean
  /** Freeze the clock (tests, and a host that owns it). */
  nowMs?: number
}

/**
 * The current instant, re-rendered on the surface's own cadence.
 *
 * Returns unix MILLISECONDS — callers divide for the seconds-stamped tables,
 * in the place that knows the unit, as everywhere else in this package.
 */
export function useElapsedTicker(options: UseElapsedTickerOptions = {}): number {
  const { intervalMs = 0, paused = false, nowMs } = options
  const [tick, setTick] = useState(() => nowMs ?? Date.now())
  // A frozen clock holds no timer at all — a test that pins `nowMs` must not
  // also have to fake timers.
  const frozen = nowMs !== undefined

  useEffect(() => {
    if (frozen || paused || intervalMs <= 0) return
    const id = setInterval(() => setTick(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [frozen, intervalMs, paused])

  return nowMs ?? tick
}

export default useElapsedTicker
