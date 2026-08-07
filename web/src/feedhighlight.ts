// feedhighlight — the entrance and the hold-then-fade, and the static thing
// that replaces them (doc 21 §4.2, §5-M4 step 3).
//
// The 37signals Yellow Fade Technique, minus the yellow: `yellow` in this
// console means `rate_limited`, so an arrival is tinted in its AUTHORSHIP
// colour — ember-soft for a worker-authored arrival, rose-soft for an ask —
// which is the same vocabulary the spine glyphs already speak.
//
// Four rules from the research are enforced here rather than left to call
// sites, because all four are cheap to get wrong and silent when wrong:
//
// Which rows are arrivals is decided next door, by `useStagedFeed` — one
// mechanism, not two — and it enforces the three rules that are cheap to get
// wrong and silent when wrong: animate on ARRIVAL and never on render (the
// first batch is the backfill), dedupe by id so an SSE reconnect or a refetch
// re-fires nothing, and cap concurrency so twenty arrivals highlight the block
// boundary rather than twenty rows. This module owns only what an arrival looks
// like once something else has decided it is one, plus `HIGHLIGHT_CAP`, which
// is a rendering budget and so belongs with the rendering.
//
// The fourth rule is here: **the reduced-motion branch is better, not lesser.**
// Instead of a fade that has ended before you look up, a persistent 2px left
// border and a `NEW` chip that stay until the watermark advances.
//
// Pure: every export is a function of its arguments and `highlightSx` returns
// MUI `sx`. No timers — the decay is one CSS animation that runs once and
// stops, which is also why a highlight survives a re-render without re-firing.

import type { SxProps, Theme } from '@mui/material'
import { consoleTint, consoleTokenColor, type ConsoleTokenName } from './spine.js'

/**
 * An `sx` value that is NOT itself an array — the shape MUI's `sx={[a, b]}`
 * composition accepts as an element. Spelled here rather than imported from
 * `@mui/system` (which is not a declared dependency of this package).
 */
export type ConsoleSx = Exclude<SxProps<Theme>, readonly unknown[]>

/** Slide-fade entrance: 150–200ms, decelerating (§5-M4). */
export const HIGHLIGHT_ENTRANCE_MS = 180

/** How long the tint holds at full strength before it starts to go. */
export const HIGHLIGHT_HOLD_MS = 800

/** How long the tint takes to fade to nothing after the hold. */
export const HIGHLIGHT_FADE_MS = 1500

/** Total life of the tint animation. */
export const HIGHLIGHT_TOTAL_MS = HIGHLIGHT_HOLD_MS + HIGHLIGHT_FADE_MS

/**
 * More arrivals than this in one batch and the block boundary carries the news
 * instead of the rows. Six is "a glance can count them"; twenty is a disco.
 */
export const HIGHLIGHT_CAP = 6

/** Who authored the arrival — the tint, and nothing else, comes from this. */
export type HighlightTone = 'agent' | 'ask' | 'human' | 'failure'

const TONE_TOKEN: Record<HighlightTone, ConsoleTokenName> = {
  agent: 'ember',
  ask: 'rose',
  human: 'ink',
  failure: 'fault',
}

/** Alpha of the tint at full strength — a wash, never a band (X6's lesson). */
export const HIGHLIGHT_TINT_ALPHA = 0.14

// ---------------------------------------------------------------------------
// The rendering (motion and its static equivalent)
// ---------------------------------------------------------------------------

export interface HighlightOptions {
  /** True when this row is one of the current arrivals. */
  active: boolean
  /** The authorship colour. */
  tone: HighlightTone
  /** The one gate — `usePrefersReducedMotion()`, or an explicit "calm" setting. */
  reduced: boolean
}

/**
 * What an arriving row looks like.
 *
 * With motion: a 180ms decelerating slide-fade in, and a tint that holds 800ms
 * then decays over 1.5s. Both are `animation`, not `transition`: an animation
 * with no iteration count runs once and is done, so the row is a still,
 * untinted row a moment later — the motion terminated, as §4.1 rule 2 requires.
 *
 * Under reduced motion: no animation at all, and instead a persistent 2px
 * left border in the same authorship colour. The row still says "I am new", it
 * says it in a way that survives a screenshot, and it keeps saying it until the
 * watermark advances — which is arguably the better design, and is why §5-M4
 * offers it to everyone.
 */
export function highlightSx({ active, tone, reduced }: HighlightOptions): ConsoleSx {
  if (!active) return {}
  const token = TONE_TOKEN[tone]

  // A theme callback rather than per-property callbacks: the tint has to appear
  // inside `@keyframes`, and nested keyframe values are not theme-resolved.
  return (theme: Theme) => {
    if (reduced) {
      return {
        borderLeft: '2px solid',
        borderColor: consoleTokenColor(theme, token),
        // The border eats 2px; give it back so the text does not shift.
        marginLeft: '-2px',
        paddingLeft: '2px',
      }
    }
    const tint = consoleTint(theme, token, HIGHLIGHT_TINT_ALPHA)
    return {
      '@keyframes ao-feed-enter': {
        from: { opacity: 0, transform: 'translateY(-6px)' },
        to: { opacity: 1, transform: 'none' },
      },
      [`@keyframes ao-feed-tint-${tone}`]: {
        // Hold, then decay — 800ms of 2300ms is ~35%.
        '0%': { backgroundColor: tint },
        '35%': { backgroundColor: tint },
        '100%': { backgroundColor: 'transparent' },
      },
      animation: [
        `ao-feed-enter ${HIGHLIGHT_ENTRANCE_MS}ms cubic-bezier(0, 0, 0.2, 1)`,
        `ao-feed-tint-${tone} ${HIGHLIGHT_TOTAL_MS}ms ease-out`,
      ].join(', '),
    }
  }
}

/**
 * The same decision, as data — what a row should render, without touching CSS.
 *
 * Components use this for the parts that are not styling: whether to hang a
 * `NEW` chip off the row (reduced motion only — under motion the tint says it,
 * and a chip that vanished with the tint would be a lie the moment it faded).
 */
export function highlightMarker({ active, reduced }: HighlightOptions): boolean {
  return active && reduced
}

// ---------------------------------------------------------------------------
// Status transitions (§4.2: the chip is the payload, the row pulse is the
// peripheral-vision cue)
// ---------------------------------------------------------------------------

/** The chip's crossfade, in ms — 120–160 per the research. */
export const STATUS_CROSSFADE_MS = 140

/** The row's one destination-tinted pulse. */
export const STATUS_PULSE_MS = 1200

/** Which colour a row pulses in when it LANDS on a status. Severity-weighted:
 *  a failure is the loudest thing on the table and stays that way. */
const STATUS_TOKEN: Record<string, ConsoleTokenName> = {
  running: 'ember',
  ok: 'steel',
  failed: 'fault',
  awaiting_human: 'rose',
  rate_limited: 'fault',
}

/**
 * Which rows changed status since the last look, keyed by row id.
 *
 * Pure, and a projection by construction: a delivery is ONE row and its status
 * is a field of it. §4.2's precondition — render the raw event log instead and
 * `pending→running→ok` is three rows with nothing to animate between them.
 */
export function diffStatuses(
  previous: ReadonlyMap<string, string>,
  rows: readonly { id: string; status: string }[],
): { next: Map<string, string>; changed: Set<string> } {
  const next = new Map<string, string>()
  const changed = new Set<string>()
  for (const row of rows) {
    next.set(row.id, row.status)
    const before = previous.get(row.id)
    // A row seen for the first time did not "change" — that is an arrival, and
    // arrivals are the highlight module's business, not this one's.
    if (before !== undefined && before !== row.status) changed.add(row.id)
  }
  return { next, changed }
}

/**
 * One pulse in the destination colour, once, when a row's status lands.
 *
 * Reduced motion gets nothing here at all — and loses nothing, because the chip
 * beside it already changed and the chip is the payload. This is the one place
 * where "drop the animation entirely" is the right substitute (§4.1's trace
 * rule: if dropping it loses no information, it was decoration worth keeping).
 */
export function statusPulseSx(status: string, reduced: boolean): ConsoleSx {
  const token = STATUS_TOKEN[status]
  if (reduced || token === undefined) return {}
  return (theme: Theme) => {
    const tint = consoleTint(theme, token, HIGHLIGHT_TINT_ALPHA)
    return {
      [`@keyframes ao-status-pulse-${status}`]: {
        '0%': { backgroundColor: 'transparent' },
        '15%': { backgroundColor: tint },
        '100%': { backgroundColor: 'transparent' },
      },
      animation: `ao-status-pulse-${status} ${STATUS_PULSE_MS}ms ease-out`,
    }
  }
}

/**
 * The chip's own crossfade. Not gated on a status list: a chip whose label
 * changed is always worth 140ms, and under reduced motion it is instant.
 */
export function statusCrossfadeSx(reduced: boolean): ConsoleSx {
  if (reduced) return {}
  return {
    '@keyframes ao-chip-crossfade': {
      from: { opacity: 0 },
      to: { opacity: 1 },
    },
    animation: `ao-chip-crossfade ${STATUS_CROSSFADE_MS}ms ease-out`,
  }
}

/**
 * The chip an arriving row carries under reduced motion — `NEW`, in words,
 * because §4.1 rule 4 says nothing may be encoded only in motion and the
 * border alone is colour-plus-position, not text.
 *
 * Returned as data rather than a component so a caller renders it in whatever
 * its row already is (a `Chip`, a `Typography`, a table cell).
 */
export const NEW_MARKER_LABEL = 'NEW'
