// chartmotion — the org chart's motion, as arithmetic (doc 21 §4.1, §5 M0–M3).
//
// Everything here is pure: no React, no clock, and — with one named exception
// (`offsetPathSupported`, which has to ask the browser a question only the
// browser can answer) — no DOM. The component decides *when* to move; this
// module decides *what* moving would mean: which wire a delivery travelled,
// how long a dot should take on a route of a given length, which pulses the
// ≤3-per-wire cap allows, what a flash's lifetime is, and how an elapsed
// number should read at each granularity.
//
// That split is deliberate. The *feel* of the chart can only be judged in a
// browser, and this file makes no claim about it. But every rule the research
// fixed is a number, and numbers are testable — so the rules live here, under
// test, rather than inside a component where they can only be eyeballed.
//
// The four binding rules from §4.1, and where each one lives:
//
//   1. **Motion must be caused.** A pulse is only ever planned from a delivery
//      row with an id — `planArrivalPulses` takes the ids the caller has
//      already seen and emits pulses for the rest. Nothing animates because a
//      render happened; something animates because a delivery arrived.
//   2. **Motion must terminate.** Every pulse carries a finite duration
//      (`pulseDurationSeconds`) and every flash a finite lifetime
//      (`flashLifetimeMs`) — with exactly one exception, below.
//   3. **N discrete deliveries ≠ a stream.** `MAX_CONCURRENT_PULSES` is 3; past
//      that the `↳ ×n` count (`trafficLabel`) carries the traffic instead.
//   4. **Nothing encoded only in motion.** The counts and the chevrons are
//      drawn whether or not anything ever animates — they are the still
//      screenshot, and the reduced-motion rendering *is* that screenshot.
//
// The exception to rule 2 is the one the research insisted on: **a failure is a
// state, not an event.** A fault flash has no lifetime and never decays
// (`flashLifetimeMs('fault') === null`). Animating a failure away is how a
// console loses an operator's trust.

import type { JobRow } from './events.js'
import type { OrgChartPoint, OrgChartWire, Propagation } from './orgchart.js'

// ---------------------------------------------------------------------------
// The constants — one place, so the component and the tests read the same ones
// ---------------------------------------------------------------------------

/** §4.1 rule 3: more than this many dots on one wire is a pipe, and we have no
 *  pipe. Beyond it the static `↳ ×n` count is the honest rendering. */
export const MAX_CONCURRENT_PULSES = 3

/** Speed-normalisation (§4.1): a constant ~600px/s, so a long wire does not
 *  look faster than a short one just because both were given a fixed duration. */
export const PULSE_SPEED_PX_PER_SEC = 600

/** The 350–600ms band the research names, widened to 900ms for the longest
 *  routes — still under its 1s ceiling. */
export const PULSE_MIN_SECONDS = 0.35
export const PULSE_MAX_SECONDS = 0.9

/** Spline easing: the one controlled study on animated edge drawings found
 *  easing measurably improved topology-task accuracy. */
export const PULSE_EASING = 'cubic-bezier(0.33, 0, 0.2, 1)'
/** The same curve as SMIL `keySplines`, for the `<animateMotion>` fallback. */
export const PULSE_KEY_SPLINES = '0.33 0 0.2 1'

/** Chart-open replay (§5 M1): the last few deliveries, staggered. */
export const REPLAY_LIMIT = 10
export const REPLAY_STAGGER_MS = 80

/** Flash-and-decay (§4.1): fast in, slow out. A colour change, not a position
 *  change — which is why it doubles as the reduced-motion substitute. */
export const FLASH_IN_MS = 60
export const FLASH_OUT_MS = 450

/** WCAG 2.3.1 caps flashing at three per second; bursts coalesce onto this
 *  grid rather than strobing. */
export const FLASH_MIN_GAP_MS = 340

/** Trace draw-in (§5 M3), and how far the rest of the graph is dimmed. */
export const TRACE_HOP_STAGGER_MS = 120
export const TRACE_DIM_OPACITY = 0.22

/** The running breathe (§5 M2): slow, opacity-only, no scale and no glow.
 *
 *  The floor is 0.55 rather than something deeper because the thing that
 *  breathes carries text (`● running 1/1`) — see the note on the state line
 *  in `OrgChartPage`. A status line that fades to near-invisible twice a
 *  minute is not calm, it is unreadable. */
export const BREATHE_PERIOD_MS = 2000
export const BREATHE_MIN_OPACITY = 0.55

/**
 * Where a wire's direction chevron sits, as a fraction of the route's length.
 *
 * NOT the midpoint, though §5 M0 calls it a mid-point chevron: the riding label
 * already owns the middle of the longest run, and it is painted with a
 * background-coloured halo (`paintOrder="stroke"`), so a chevron drawn there
 * would be erased by the thing it sits under. Roughly a third of the way along
 * is clear of the label on every shape the layout produces, and still reads as
 * "on the wire".
 */
export const CHEVRON_FRACTION = 0.35

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------

/** Total length of an orthogonal route, in SVG user units. */
export function polylineLength(points: readonly OrgChartPoint[]): number {
  let total = 0
  for (let i = 0; i < points.length - 1; i += 1) {
    total += Math.hypot(points[i + 1]!.x - points[i]!.x, points[i + 1]!.y - points[i]!.y)
  }
  return total
}

/** The route as an SVG path `d`, for `offset-path` and for `<animateMotion>`. */
export function polylinePath(points: readonly OrgChartPoint[]): string {
  if (points.length === 0) return ''
  const [first, ...rest] = points
  return `M${first!.x} ${first!.y}${rest.map((p) => ` L${p.x} ${p.y}`).join('')}`
}

/** A dot's traversal time: `clamp(length / 600, .35, .9)` seconds (§4.1). */
export function pulseDurationSeconds(length: number): number {
  const raw = length / PULSE_SPEED_PX_PER_SEC
  if (!Number.isFinite(raw)) return PULSE_MIN_SECONDS
  return Math.min(PULSE_MAX_SECONDS, Math.max(PULSE_MIN_SECONDS, raw))
}

/**
 * A point on a route, plus the direction of the segment it landed on.
 *
 * The angle is SNAPPED to a right angle. This is §4.1's "never `rotate=auto`"
 * restated as arithmetic: on an orthogonal patchbay an auto-rotated chevron
 * snaps 90° at every corner, which reads as a glitch. Every segment the layout
 * emits is axis-aligned anyway, so snapping loses nothing and guarantees it.
 */
export function pointAlong(
  points: readonly OrgChartPoint[],
  fraction: number,
): { x: number; y: number; angle: number } | null {
  if (points.length < 2) return null
  const total = polylineLength(points)
  if (total === 0) return null
  const want = total * Math.min(1, Math.max(0, fraction))
  let walked = 0
  for (let i = 0; i < points.length - 1; i += 1) {
    const a = points[i]!
    const b = points[i + 1]!
    const segment = Math.hypot(b.x - a.x, b.y - a.y)
    if (segment === 0) continue
    if (walked + segment >= want || i === points.length - 2) {
      const t = Math.min(1, Math.max(0, (want - walked) / segment))
      return {
        x: Math.round(a.x + (b.x - a.x) * t),
        y: Math.round(a.y + (b.y - a.y) * t),
        angle: snapRightAngle(Math.atan2(b.y - a.y, b.x - a.x) * (180 / Math.PI)),
      }
    }
    walked += segment
  }
  return null
}

/** The direction chevron for a route: always present, never animated (§5 M0). */
export function chevronFor(
  points: readonly OrgChartPoint[],
): { x: number; y: number; angle: number } | null {
  return pointAlong(points, CHEVRON_FRACTION)
}

function snapRightAngle(degrees: number): number {
  const snapped = Math.round(degrees / 90) * 90
  return ((snapped % 360) + 360) % 360
}

// ---------------------------------------------------------------------------
// Traffic — which wire did a delivery travel down?
// ---------------------------------------------------------------------------

/** What a wire has carried, out of the deliveries currently fetched. */
export interface WireTraffic {
  count: number
  /** The newest delivery's status — the wire's outcome colour under a trace. */
  lastStatus: string
  /** The newest delivery's id, so a caller can key on it. */
  lastDeliveryId: string
}

/**
 * Which wire a delivery travelled down, or null when it cannot be told.
 *
 * A subscription can be drawn as SEVERAL wires: an unfiltered `worker.finished`
 * subscription has every worker as a possible producer, and the layout draws
 * one wire per producer. The delivery's own event says which one actually
 * happened (`envelope.worker`), so that is what decides. When the event fell
 * outside the fetched page and the subscription has more than one wire, the
 * honest answer is "unknown" — attributing it to an arbitrary one would draw a
 * dot down a wire nothing travelled, which is exactly rule 1's failure mode.
 */
export function wireForJob(job: JobRow, wires: readonly OrgChartWire[]): OrgChartWire | null {
  const candidates = wires.filter((w) => w.subscriptionId === job.delivery.subscription_id)
  if (candidates.length === 0) return null
  if (candidates.length === 1) return candidates[0]!
  const producer = job.event?.envelope.worker ?? ''
  if (producer !== '') {
    const fromWorker = candidates.find((w) => w.from === producer)
    if (fromWorker !== undefined) return fromWorker
  }
  const type = job.eventType
  if (type !== '') {
    const fromPip = candidates.find((w) => w.fromPip === type)
    if (fromPip !== undefined) return fromPip
  }
  return null
}

/**
 * Per-wire traffic, from the deliveries the page already fetched.
 *
 * `jobs` is `buildJobRows`' output: newest first. The first delivery seen for a
 * wire is therefore the newest one, which is the one whose status colours it.
 */
export function wireTraffic(
  wires: readonly OrgChartWire[],
  jobs: readonly JobRow[],
): Map<string, WireTraffic> {
  const traffic = new Map<string, WireTraffic>()
  for (const job of jobs) {
    const wire = wireForJob(job, wires)
    if (wire === null) continue
    const seen = traffic.get(wire.id)
    if (seen === undefined) {
      traffic.set(wire.id, { count: 1, lastStatus: job.status, lastDeliveryId: job.delivery.id })
      continue
    }
    seen.count += 1
  }
  return traffic
}

/**
 * The static traffic count that rides a wire (§5 M0): `↳ ×3`.
 *
 * Drawn for ANY non-zero count, not only past the pulse cap. The cap decides
 * how much may *move*; rule 4 decides what must be readable when nothing does,
 * and "this wire carried two deliveries" is not a fact that should exist only
 * during a 400ms animation.
 */
export function trafficLabel(count: number): string | null {
  return count > 0 ? `↳ ×${count}` : null
}

// ---------------------------------------------------------------------------
// Flashes
// ---------------------------------------------------------------------------

export type FlashKind = 'ember' | 'fault'

/** A delivery's flash colour. Only a failure is fault — a rate limit is a wait
 *  and `awaiting_human` is a pause, and neither is an error (§3.3: rose is
 *  never rendered as an error). */
export function flashKindFor(status: string): FlashKind {
  return status === 'failed' ? 'fault' : 'ember'
}

/**
 * How long a flash lives, in ms — or `null` for "forever".
 *
 * §4.1: *failure is a state, not an event*. The fault flash stays until the
 * data says otherwise. Everything else decays: 60ms in, 450ms out.
 */
export function flashLifetimeMs(kind: FlashKind): number | null {
  return kind === 'fault' ? null : FLASH_IN_MS + FLASH_OUT_MS
}

/**
 * When a flash may start, given when the last one on the same wire did.
 *
 * WCAG 2.3.1 caps flashing at three per second, so a burst of arrivals is
 * pushed onto a 340ms grid instead of strobing. Returns `nowMs` when the wire
 * has been quiet.
 */
export function coalesceFlashStart(
  lastStartMs: number | null,
  nowMs: number,
  minGapMs: number = FLASH_MIN_GAP_MS,
): number {
  if (lastStartMs === null) return nowMs
  return Math.max(nowMs, lastStartMs + minGapMs)
}

// ---------------------------------------------------------------------------
// Pulses
// ---------------------------------------------------------------------------

/** One planned dot: which wire it runs down, and when it starts. */
export interface ChartPulse {
  /** Unique per planned pulse — the delivery id plus the wire it ran down. */
  key: string
  wireId: string
  deliveryId: string
  kind: FlashKind
  delayMs: number
}

export interface ArrivalPlan {
  pulses: ChartPulse[]
  /** Every delivery id in `jobs` — what the caller should ADD to its seen set,
   *  including the ones no pulse was planned for. A delivery the cap dropped
   *  must not pulse later just because another render happened. */
  seen: string[]
}

/**
 * Plan the pulses for deliveries that have ARRIVED since the last look.
 *
 * The arrival-not-render rule (§4.2, and the most common live-feed motion bug):
 * `seen` is what the caller already knew about, so the first hydration — where
 * every row would look "new" — is handled by the caller seeding `seen` from it
 * instead of calling this. Here, a delivery pulses exactly once, on the first
 * call in which its id appears and is not already seen.
 *
 * `inFlight` is how many dots are already running on each wire. Together with
 * the pulses planned in this same call it enforces §4.1 rule 3: at most three
 * dots per wire, after which the traffic count is the rendering.
 */
export function planArrivalPulses(
  jobs: readonly JobRow[],
  wires: readonly OrgChartWire[],
  seen: ReadonlySet<string>,
  inFlight: ReadonlyMap<string, number> = new Map(),
): ArrivalPlan {
  const pulses: ChartPulse[] = []
  const planned = new Map<string, number>()
  // `jobs` is newest-first; arrivals are played oldest-first so a burst reads
  // in the order it happened.
  const arrivals = jobs.filter((job) => !seen.has(job.delivery.id)).slice().reverse()
  for (const job of arrivals) {
    const wire = wireForJob(job, wires)
    if (wire === null) continue
    const running = (inFlight.get(wire.id) ?? 0) + (planned.get(wire.id) ?? 0)
    if (running >= MAX_CONCURRENT_PULSES) continue
    planned.set(wire.id, (planned.get(wire.id) ?? 0) + 1)
    pulses.push({
      key: `${job.delivery.id}@${wire.id}`,
      wireId: wire.id,
      deliveryId: job.delivery.id,
      kind: flashKindFor(job.status),
      delayMs: 0,
    })
  }
  return { pulses, seen: jobs.map((job) => job.delivery.id) }
}

/**
 * The chart-open replay (§5 M1): the last few deliveries, staggered.
 *
 * Oldest first, so the replay reads forwards. The per-wire cap still applies —
 * a chart whose whole history ran down one wire replays three dots and lets the
 * count say the rest.
 */
export function planReplayPulses(
  jobs: readonly JobRow[],
  wires: readonly OrgChartWire[],
  limit: number = REPLAY_LIMIT,
  staggerMs: number = REPLAY_STAGGER_MS,
): ChartPulse[] {
  const pulses: ChartPulse[] = []
  const planned = new Map<string, number>()
  const recent = jobs.slice(0, Math.max(0, limit)).slice().reverse()
  for (const job of recent) {
    const wire = wireForJob(job, wires)
    if (wire === null) continue
    const running = planned.get(wire.id) ?? 0
    if (running >= MAX_CONCURRENT_PULSES) continue
    planned.set(wire.id, running + 1)
    pulses.push({
      key: `replay:${job.delivery.id}@${wire.id}`,
      wireId: wire.id,
      deliveryId: job.delivery.id,
      kind: flashKindFor(job.status),
      delayMs: pulses.length * staggerMs,
    })
  }
  return pulses
}

/** When a pulse has finished, in ms from now — what the caller waits for
 *  before clearing it. */
export function pulseEndMs(pulse: ChartPulse, routeLength: number): number {
  return pulse.delayMs + pulseDurationSeconds(routeLength) * 1000
}

/**
 * Can this browser drive the dot with WAAPI + `offset-path`?
 *
 * Feature-detected at runtime rather than sniffed, because the answer differs
 * by engine AND by element type — `offset-path` on an SVG child is exactly the
 * case §4.1 flagged as needing verification, and `CSS.supports` answers only
 * the first half of that question (does the property parse). So there are two
 * gates: the property parses, AND setting it on a real `<circle>`'s inline
 * style survives the round trip. When either fails the caller falls back to
 * SMIL `begin="indefinite"` + `beginElement()`, which every engine has.
 *
 * jsdom fails the second gate, so the test environment exercises the SMIL
 * branch by default — which is the branch that has to keep working anyway.
 */
export function offsetPathSupported(
  doc: Document | undefined = typeof document === 'undefined' ? undefined : document,
): boolean {
  if (typeof CSS === 'undefined' || typeof CSS.supports !== 'function') return false
  try {
    if (!CSS.supports('offset-path', 'path("M0 0")')) return false
  } catch {
    return false
  }
  if (doc === undefined) return false
  try {
    const circle = doc.createElementNS('http://www.w3.org/2000/svg', 'circle')
    const style = circle.style as CSSStyleDeclaration & { offsetPath?: string }
    style.offsetPath = 'path("M0 0")'
    return typeof style.offsetPath === 'string' && style.offsetPath !== ''
  } catch {
    return false
  }
}

// ---------------------------------------------------------------------------
// The ticking status line (§5 M2, on §4.2's discipline)
// ---------------------------------------------------------------------------

/** Past this many seconds an elapsed number stops ticking every second: the
 *  research is blunt that a per-second clock "accentuates the passing of each
 *  and every second", and past two minutes the seconds are not a decision. */
export const ELAPSED_COARSEN_AFTER_SECONDS = 120

/** How often an elapsed number of this age needs redrawing. */
export function tickIntervalMs(elapsedSeconds: number): number {
  return elapsedSeconds < ELAPSED_COARSEN_AFTER_SECONDS ? 1000 : 60_000
}

/**
 * `42s` · `1m 05s` · `4m` · `1h 12m`.
 *
 * Not `timefmt.agoShort`, deliberately: that one collapses everything under a
 * minute to `now`, and the first minute of a running job is precisely where an
 * operator is watching the seconds. Above the coarsening threshold the two
 * agree in spirit — minutes, then hours.
 *
 * Negative (a clock skew) reads as `0s`, never `-1s`.
 */
export function formatElapsed(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds))
  if (total < ELAPSED_COARSEN_AFTER_SECONDS) {
    const minutes = Math.floor(total / 60)
    const rest = total % 60
    return minutes === 0 ? `${rest}s` : `${minutes}m ${String(rest).padStart(2, '0')}s`
  }
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return `${minutes}m`
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, '0')}m`
}

/**
 * The coarse form a screen reader gets (§4.2): minutes, never seconds.
 *
 * The ticking text itself is hidden from the accessibility tree; this is what
 * its container's label says, and it changes at most once a minute, so a
 * screen reader is not read a new number every second.
 */
export function coarseElapsed(seconds: number): string {
  const minutes = Math.floor(Math.max(0, seconds) / 60)
  if (minutes < 1) return 'under a minute'
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'}`
  const hours = Math.floor(minutes / 60)
  return `${hours} hour${hours === 1 ? '' : 's'}`
}

/**
 * When each worker's oldest RUNNING delivery started, in unix seconds.
 *
 * Oldest, not newest: with two instances running, the number the operator needs
 * is how long this worker has been busy, which is the older one. A delivery
 * with no `started_at` (it never got that far) falls back to `created_at`, and
 * one with neither contributes nothing rather than an elapsed measured from
 * the epoch.
 */
export function runningSince(jobs: readonly JobRow[]): Map<string, number> {
  const since = new Map<string, number>()
  for (const job of jobs) {
    if (job.status !== 'running' || job.worker === '') continue
    const started = job.delivery.started_at || job.delivery.created_at
    if (!started) continue
    const known = since.get(job.worker)
    if (known === undefined || started < known) since.set(job.worker, started)
  }
  return since
}

// ---------------------------------------------------------------------------
// Trace (§5 M3)
// ---------------------------------------------------------------------------

/** The circled hop numbers the trace paints at each hop (§4.1: hop numbers are
 *  what makes causality reconstructible from a screenshot, which motion never
 *  allows). Depth 0 is the arriving event itself, so the first *wake* is ①. */
const HOP_GLYPHS = ['⓪', '①', '②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨'] as const

export function hopGlyph(depth: number): string {
  return HOP_GLYPHS[depth] ?? `(${depth})`
}

/** Each lit subscription's FIRST depth — the hop number it wears, and the step
 *  its draw-in waits for. */
export function traceHopDepths(propagation: Propagation | null): Map<string, number> {
  const depths = new Map<string, number>()
  for (const hop of propagation?.hops ?? []) {
    for (const wake of hop.wakes) {
      if (!depths.has(wake.subscriptionId)) depths.set(wake.subscriptionId, wake.depth)
    }
  }
  return depths
}

/** How long the draw-in of a hop-`depth` wire waits before it starts. */
export function traceDrawDelayMs(depth: number): number {
  return Math.max(0, depth) * TRACE_HOP_STAGGER_MS
}
