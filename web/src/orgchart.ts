// The org chart's layout — one pure, deterministic function (operator-console
// design `docs/product/15-operator-console-design.md` §6.1–§6.2, decision K6).
//
// Nodes are workers. Wires are subscriptions. Clocks are schedules. Entry pips
// at the top are the event types nothing in this project produces. Nothing is
// invented: every coordinate here is derived from rows the config log already
// holds, which is also why §6.3 forbids storing any of it.
//
// Pure: no React, no DOM, no fetch, no clock, no randomness. Same input →
// deep-equal output, pinned by test; every tie-break is by name, because a
// chart that reshuffled between renders would make "the shape changed" stop
// meaning "the organisation changed".
//
// **No graph library** (§6.2). Layered/Sugiyama-lite in ~one file: rank by
// longest path from an entry pip, one barycenter pass to order within a rank,
// orthogonal routing. Cycles cannot hang it — the lexicographically-smallest
// edge inside a cycle is marked `back` and dropped from the ranking, so a loop
// draws as a loop instead of spinning.
//
// What is NOT modelled here, and belongs to the page: live delivery counts (the
// `● running n/max` state line reads them from deliveries), rate limits,
// `max_instances` gating and budget stops. This module knows configuration.

import {
  blankEnvelope,
  eventTypeMatches,
  matchSubscriptions,
  type MatchableEvent,
  type ProjectEvent,
  type Subscription,
} from './events.js'
import type { Schedule } from './schedules.js'
import type { Worker } from './workers.js'

// ---------------------------------------------------------------------------
// Metrics — the schematic's one set of dimensions, in px
// ---------------------------------------------------------------------------

/** Every distance the layout uses. Exported so the renderer draws to the same
 *  grid the tests assert on, rather than a second copy of these numbers. */
export const ORG_CHART_METRICS = {
  nodeWidth: 200,
  nodeHeight: 68,
  /** Horizontal gap between two nodes in the same rank. */
  hGap: 48,
  /** Vertical gap between ranks — also the depth of a wire's elbow lane. */
  vGap: 84,
  /** Height of the entry-pip row at the top. */
  pipHeight: 22,
  /** Minimum horizontal distance between two pips (they carry mono labels). */
  pipGap: 150,
  /** A schedule dial, docked to the left of its worker's plate. */
  clockSize: 26,
  clockGap: 10,
  /** Left/right margin. Wide enough that a docked clock never goes negative. */
  marginX: 48,
  marginY: 16,
  /** Extra width claimed on the right when a back edge needs a return lane. */
  backLane: 44,
} as const

// ---------------------------------------------------------------------------
// The shapes
// ---------------------------------------------------------------------------

export interface OrgChartPoint {
  x: number
  y: number
}

/** One worker's name plate (§6.1). `maxInstances` is here so the page can
 *  render `n/max` without a second lookup; `n` is live and is not ours. */
export interface OrgChartNode {
  name: string
  description: string
  /** Longest path from an entry pip. 0 = woken from outside, or by nothing. */
  rank: number
  /** Position within the rank, left to right, after the barycenter pass. */
  order: number
  x: number
  y: number
  width: number
  height: number
  enabled: boolean
  frozen: boolean
  maxInstances: number
}

/** One subscription, drawn. A subscription with several possible producers
 *  (`worker.finished` with no `worker` filter) yields one wire per producer. */
export interface OrgChartWire {
  /** Unique within a layout: subscription id plus the end it comes from. */
  id: string
  subscriptionId: string
  /** The producing worker, or null when it comes from an entry pip. */
  from: string | null
  /** The entry pip's event type, or null when it comes from a worker. */
  fromPip: string | null
  /** The woken worker. Always a node in this layout. */
  to: string
  eventType: string
  filter: Record<string, unknown>
  enabled: boolean
  /** True when this edge closes a cycle: it was dropped from the ranking and
   *  is routed back up the return lane. */
  back: boolean
  /** Orthogonal route, first point at the source, last at the target. */
  points: OrgChartPoint[]
  /** The text that rides the wire (§3.7): the event type, plus its filter. */
  label: string
  labelX: number
  labelY: number
}

/** A schedule, as a 24-hour dial docked to its worker's plate (§3.7). */
export interface OrgChartClock {
  id: string
  worker: string
  cron: string
  /** The hours this cron fires at, 0–23, ascending. Empty when unparsed. */
  hours: number[]
  /** False when the cron's hour field is not one this reader understands —
   *  the dial then shows no ticks rather than the wrong ones. */
  hoursKnown: boolean
  enabled: boolean
  x: number
  y: number
  size: number
}

/** An event type that enters the project from outside it (§6.1). */
export interface OrgChartPip {
  type: string
  /** True when an event of this type appears in `recentEvents` with an
   *  `external` source — i.e. it has actually happened, not just been wired. */
  external: boolean
  x: number
  y: number
}

export interface OrgChartLayout {
  nodes: OrgChartNode[]
  wires: OrgChartWire[]
  clocks: OrgChartClock[]
  entryPips: OrgChartPip[]
  width: number
  height: number
}

// ---------------------------------------------------------------------------
// Producers — who emits the event a subscription waits for
// ---------------------------------------------------------------------------

/** The only event types a worker emits by finishing (§8.2). A subscription on
 *  anything else is fed from outside the fleet unless a prompt relays it, and
 *  a prompt relay is a *convention*, drawn by OC3 and never as a solid wire. */
export const WORKER_LIFECYCLE_EVENT_TYPES = ['worker.finished', 'worker.failed'] as const

/** Does this subscription's pattern match a worker lifecycle event? */
export function matchesWorkerLifecycle(pattern: string): boolean {
  return WORKER_LIFECYCLE_EVENT_TYPES.some((type) => eventTypeMatches(pattern, type))
}

/**
 * Which workers could produce the event this subscription waits for.
 *
 * Three cases, in order:
 *   1. `filter.worker` names a worker → exactly that one. This is the shape
 *      every seed topology uses, and it is the only precise answer available.
 *   2. Otherwise a lifecycle pattern → every worker, because every worker
 *      finishing would match. A fan-in looks alarming and is honest.
 *   3. Otherwise → nobody: the event enters from outside, and the caller draws
 *      an entry pip.
 */
export function subscriptionProducers(
  subscription: Pick<Subscription, 'event_type' | 'filter'>,
  workerNames: readonly string[],
): string[] {
  const filtered = subscription.filter?.worker
  if (typeof filtered === 'string' && workerNames.includes(filtered)) return [filtered]
  if (matchesWorkerLifecycle(subscription.event_type)) return [...workerNames].sort(compareText)
  return []
}

// ---------------------------------------------------------------------------
// Cron hours — the dial's ticks
// ---------------------------------------------------------------------------

/**
 * The hours a standard 5-field cron fires at.
 *
 * Deliberately narrow: it reads the HOUR field only, and only the forms cron
 * actually uses — a star, a star with a step, a single hour, a range, a range
 * with a step, and comma lists of those. Anything else returns `known: false`
 * and no ticks: a dial that guessed would be a dial that lies about when the
 * fleet wakes up.
 */
export function cronHours(cron: string): { hours: number[]; known: boolean } {
  const fields = cron.trim().split(/\s+/).filter((f) => f !== '')
  if (fields.length !== 5) return { hours: [], known: false }
  const hours = new Set<number>()
  for (const term of fields[1].split(',')) {
    const parsed = parseCronHourTerm(term)
    if (parsed === null) return { hours: [], known: false }
    for (const hour of parsed) hours.add(hour)
  }
  return { hours: [...hours].sort((a, b) => a - b), known: true }
}

function parseCronHourTerm(term: string): number[] | null {
  const [range, stepText] = term.split('/')
  if (stepText !== undefined && !/^\d+$/.test(stepText)) return null
  const step = stepText === undefined ? 1 : Number(stepText)
  if (step < 1) return null

  let start: number
  let end: number
  if (range === '*') {
    start = 0
    end = 23
  } else if (/^\d+$/.test(range)) {
    start = Number(range)
    end = stepText === undefined ? start : 23
  } else {
    const bounds = range.split('-')
    if (bounds.length !== 2 || !/^\d+$/.test(bounds[0]) || !/^\d+$/.test(bounds[1])) return null
    start = Number(bounds[0])
    end = Number(bounds[1])
  }
  if (start > 23 || end > 23 || start > end) return null
  const out: number[] = []
  for (let hour = start; hour <= end; hour += step) out.push(hour)
  return out
}

// ---------------------------------------------------------------------------
// The layout
// ---------------------------------------------------------------------------

const compareText = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0)

interface RawWire {
  key: string
  subscription: Subscription
  from: string | null
  fromPip: string | null
  to: string
}

/**
 * Lay the whole chart out.
 *
 * `recentEvents` is read for one thing only: which entry pips have actually
 * fired (`envelope.source === 'external'`). A pip with no traffic still draws —
 * it is wired, and "wired but never seen" is worth looking at.
 */
export function layoutOrgChart(
  workers: readonly Worker[],
  subscriptions: readonly Subscription[],
  schedules: readonly Schedule[] = [],
  recentEvents: readonly ProjectEvent[] = [],
): OrgChartLayout {
  const M = ORG_CHART_METRICS
  const sortedWorkers = [...workers].sort((a, b) => compareText(a.name, b.name))
  const workerNames = sortedWorkers.map((w) => w.name)
  const known = new Set(workerNames)

  // --- 1. Wires and pips -------------------------------------------------
  const sortedSubscriptions = [...subscriptions].sort(
    (a, b) =>
      compareText(a.event_type, b.event_type) ||
      compareText(a.worker, b.worker) ||
      compareText(a.id, b.id),
  )
  const raw: RawWire[] = []
  const pipTypes = new Set<string>()
  for (const subscription of sortedSubscriptions) {
    // A subscription pointing at a worker that does not exist has nothing to
    // draw into. It is a real (and visible) problem, but it is the worker
    // page's problem, not a dangling line on the schematic.
    if (!known.has(subscription.worker)) continue
    const producers = subscriptionProducers(subscription, workerNames)
    if (producers.length === 0) {
      pipTypes.add(subscription.event_type)
      raw.push({
        key: `${subscription.id}#@${subscription.event_type}`,
        subscription,
        from: null,
        fromPip: subscription.event_type,
        to: subscription.worker,
      })
      continue
    }
    for (const from of producers) {
      raw.push({
        key: `${subscription.id}#${from}`,
        subscription,
        from,
        fromPip: null,
        to: subscription.worker,
      })
    }
  }
  for (const event of recentEvents) {
    if (event.envelope?.source === 'external' && event.type !== '') pipTypes.add(event.type)
  }
  const externalSeen = new Set(
    recentEvents.filter((e) => e.envelope?.source === 'external').map((e) => e.type),
  )

  // --- 2. Break cycles, then rank ----------------------------------------
  const workerEdges: Edge[] = raw
    .filter((w) => w.from !== null)
    .map((w) => ({ from: w.from as string, to: w.to, key: edgeKey(w) }))
  const back = markBackEdges(workerEdges, workerNames)
  const forward = workerEdges.filter((e) => !back.has(e.key))
  const rank = longestPathRanks(forward, workerNames)

  // --- 3. Order within each rank (alphabetical, then one barycenter pass) --
  const maxRank = workerNames.reduce((m, n) => Math.max(m, rank.get(n) ?? 0), 0)
  const ranks: string[][] = []
  const order = new Map<string, number>()
  for (let r = 0; r <= maxRank; r += 1) {
    const row = workerNames.filter((n) => (rank.get(n) ?? 0) === r).sort(compareText)
    if (r > 0) {
      const bary = new Map<string, number>()
      row.forEach((name, index) => {
        const parents = forward
          .filter((e) => e.to === name && order.has(e.from))
          .map((e) => order.get(e.from) as number)
        // No placed parent (a rank-r node fed only by a back edge) keeps its
        // alphabetical slot rather than drifting to one end.
        bary.set(name, parents.length === 0 ? index : mean(parents))
      })
      row.sort((a, b) => (bary.get(a) as number) - (bary.get(b) as number) || compareText(a, b))
    }
    row.forEach((name, index) => order.set(name, index))
    ranks.push(row)
  }

  // --- 4. Coordinates -----------------------------------------------------
  const widest = ranks.reduce((m, row) => Math.max(m, row.length), 0)
  const contentWidth =
    widest === 0 ? M.nodeWidth : widest * M.nodeWidth + (widest - 1) * M.hGap
  let width = M.marginX * 2 + contentWidth
  const rankTop = (r: number): number =>
    M.marginY + M.pipHeight + M.vGap + r * (M.nodeHeight + M.vGap)

  const byName = new Map(sortedWorkers.map((w) => [w.name, w]))
  const nodes: OrgChartNode[] = []
  ranks.forEach((row, r) => {
    const rowWidth = row.length * M.nodeWidth + (row.length - 1) * M.hGap
    const startX = Math.round((width - rowWidth) / 2)
    row.forEach((name, index) => {
      const worker = byName.get(name) as Worker
      nodes.push({
        name,
        description: worker.description,
        rank: r,
        order: index,
        x: startX + index * (M.nodeWidth + M.hGap),
        y: rankTop(r),
        width: M.nodeWidth,
        height: M.nodeHeight,
        enabled: worker.enabled,
        frozen: worker.frozen,
        maxInstances: worker.max_instances,
      })
    })
  })
  const nodeAt = new Map(nodes.map((n) => [n.name, n]))

  // Pips: at the barycenter of what they wake, then swept left-to-right so two
  // mono labels never sit on top of each other. Ties by type name.
  const pipY = M.marginY
  const pips: OrgChartPip[] = [...pipTypes]
    .sort(compareText)
    .map((type, index) => {
      const targets = raw
        .filter((w) => w.fromPip === type)
        .map((w) => nodeAt.get(w.to))
        .filter((n): n is OrgChartNode => n !== undefined)
        .map((n) => n.x + n.width / 2)
      return {
        type,
        external: externalSeen.has(type),
        x: targets.length === 0 ? M.marginX + index * M.pipGap : Math.round(mean(targets)),
        y: pipY,
      }
    })
    .sort((a, b) => a.x - b.x || compareText(a.type, b.type))
  let sweptX = -Infinity
  for (const pip of pips) {
    pip.x = Math.max(pip.x, sweptX + M.pipGap, M.marginX)
    sweptX = pip.x
  }
  if (pips.length > 0) width = Math.max(width, pips[pips.length - 1].x + M.marginX)

  // --- 5. Clocks ----------------------------------------------------------
  // A schedule whose worker is not on the chart has no plate to dock to; the
  // Automation page is where that row is answered.
  const clocks: OrgChartClock[] = []
  const perWorker = new Map<string, number>()
  const sortedSchedules = [...schedules]
    .filter((s) => nodeAt.has(s.worker))
    .sort(
      (a, b) =>
        compareText(a.worker, b.worker) || compareText(a.cron, b.cron) || compareText(a.id, b.id),
    )
  for (const schedule of sortedSchedules) {
    const node = nodeAt.get(schedule.worker) as OrgChartNode
    const index = perWorker.get(schedule.worker) ?? 0
    perWorker.set(schedule.worker, index + 1)
    const { hours, known: hoursKnown } = cronHours(schedule.cron)
    clocks.push({
      id: schedule.id,
      worker: schedule.worker,
      cron: schedule.cron,
      hours,
      hoursKnown,
      enabled: schedule.enabled,
      x: node.x - M.clockGap - M.clockSize,
      y: node.y + index * (M.clockSize + 6),
      size: M.clockSize,
    })
  }

  // --- 6. Routes ----------------------------------------------------------
  const anyBack = raw.some((w) => back.has(edgeKey(w)))
  if (anyBack) width += M.backLane
  const laneX = width - Math.round(M.backLane / 2)
  const pipBottom = pipY + M.pipHeight

  const wires: OrgChartWire[] = raw.map((w) => {
    const target = nodeAt.get(w.to) as OrgChartNode
    const isBack = back.has(edgeKey(w))
    let points: OrgChartPoint[]
    if (w.from === null) {
      const pip = pips.find((p) => p.type === w.fromPip) as OrgChartPip
      points = elbow(
        { x: pip.x, y: pipBottom },
        { x: target.x + target.width / 2, y: target.y },
        pipBottom + Math.round((target.y - pipBottom) / 2),
      )
    } else {
      const source = nodeAt.get(w.from) as OrgChartNode
      if (isBack) {
        points = returnLane(source, target, laneX)
      } else {
        points = elbow(
          { x: source.x + source.width / 2, y: source.y + source.height },
          { x: target.x + target.width / 2, y: target.y },
          source.y + source.height + Math.round(M.vGap / 2),
        )
      }
    }
    const anchor = longestSegmentMidpoint(points)
    return {
      id: w.key,
      subscriptionId: w.subscription.id,
      from: w.from,
      fromPip: w.fromPip,
      to: w.to,
      eventType: w.subscription.event_type,
      filter: w.subscription.filter ?? {},
      enabled: w.subscription.enabled,
      back: isBack,
      points,
      label: wireLabel(w.subscription),
      labelX: anchor.x,
      labelY: anchor.y,
    }
  })

  const height =
    ranks.length === 0
      ? M.marginY * 2 + M.pipHeight
      : rankTop(ranks.length - 1) + M.nodeHeight + M.marginY

  return { nodes, wires, clocks, entryPips: pips, width, height }
}

/** What rides the wire (§3.7): the event type, and its filter when it has one. */
export function wireLabel(subscription: Pick<Subscription, 'event_type' | 'filter'>): string {
  const entries = Object.entries(subscription.filter ?? {})
  if (entries.length === 0) return subscription.event_type
  const filter = entries
    .sort(([a], [b]) => compareText(a, b))
    .map(([key, value]) => `${key}: ${String(value)}`)
    .join(', ')
  return `${subscription.event_type} {${filter}}`
}

// ---------------------------------------------------------------------------
// Propagation — "what fires when this event arrives?" (§6.5)
// ---------------------------------------------------------------------------

/** The router refuses to go deeper than this (§8.3). The ruler's stop line. */
export const PROPAGATION_MAX_DEPTH = 8

/**
 * The one line the propagation panel always carries, verbatim (§6.5, §11 rule
 * 3). It is a dry run of the two austere §8.3 predicates and nothing else; a
 * preview that guessed at live counters would be confidently wrong.
 */
export const PROPAGATION_CAVEAT =
  'This models only the two matching rules — event type and envelope filter. ' +
  'Rate limits, max_instances gating and budget stops depend on live counters ' +
  'and are deliberately not modelled here.'

/** The line under a depth with no matches. Design §11 rule 4: a shrug is not
 *  an answer, so it names the fact rather than leaving the row blank. */
export const PROPAGATION_NOTHING_SUBSCRIBES = '(nothing subscribes)'

/** What the stop line says. */
export const PROPAGATION_STOP_LINE = 'the router refuses deeper'

/** One worker woken at one depth, and the subscription that woke it. */
export interface PropagationWake {
  depth: number
  /** The event that arrived. */
  eventType: string
  /** The worker whose finishing produced it; '' at depth 0 (it came from
   *  outside, or from a schedule). */
  from: string
  worker: string
  subscriptionId: string
}

/** One rung of the depth ruler. */
export interface PropagationHop {
  depth: number
  wakes: PropagationWake[]
}

export interface Propagation {
  hops: PropagationHop[]
  /** True when the chain was still going when it reached the stop line. */
  stopped: boolean
  maxDepth: number
}

/**
 * Chain `matchSubscriptions` hop by hop, down the depth ruler.
 *
 * Hop 0 is the event as given. Every woken worker will emit `worker.finished`
 * when it ends, so hop n+1 matches one such event per worker woken at hop n —
 * exactly what the router will do. The chain stops when a hop wakes nobody, or
 * at `maxDepth`, which is where the engine stops too: a loop shows up here as a
 * chain that runs into the stop line, the cheapest runaway detector there is.
 */
export function propagateEvent(
  event: MatchableEvent,
  subscriptions: readonly Subscription[],
  maxDepth: number = PROPAGATION_MAX_DEPTH,
): Propagation {
  const hops: PropagationHop[] = []
  let frontier: MatchableEvent[] = [event]
  for (let depth = 0; depth <= maxDepth; depth += 1) {
    const wakes: PropagationWake[] = []
    for (const arriving of frontier) {
      for (const match of matchSubscriptions(arriving, [...subscriptions])) {
        if (!match.matched) continue
        wakes.push({
          depth,
          eventType: arriving.type,
          from: arriving.envelope.worker,
          worker: match.subscription.worker,
          subscriptionId: match.subscription.id,
        })
      }
    }
    wakes.sort(
      (a, b) =>
        compareText(a.worker, b.worker) ||
        compareText(a.eventType, b.eventType) ||
        compareText(a.subscriptionId, b.subscriptionId),
    )
    hops.push({ depth, wakes })
    if (wakes.length === 0 || depth === maxDepth) break
    const woken = [...new Set(wakes.map((w) => w.worker))].sort(compareText)
    frontier = woken.map((worker) => ({
      type: 'worker.finished',
      envelope: { ...blankEnvelope(), depth: depth + 1, source: 'worker', worker },
    }))
  }
  const last = hops[hops.length - 1]
  return { hops, stopped: last.depth === maxDepth && last.wakes.length > 0, maxDepth }
}

// ---------------------------------------------------------------------------
// Conventions — the honest dashed line (§6.6, decision K4)
// ---------------------------------------------------------------------------

/**
 * The label every inferred edge carries, verbatim (§6.6, K4, §11 rule 3).
 *
 * Several real behaviours live only in prompts and are invisible to the graph:
 * the `ROUTE-TO: <name>` relay the supervisor seed uses (workers cannot emit
 * typed events), memory-label handoffs, temporal-hierarchy's review channel.
 * Drawing them is worth doing *because* the gap between the org chart people
 * think they have and the one the router will execute is where specification
 * failures live — but it is a heuristic, and it must announce itself as one.
 */
export const CONVENTION_CAVEAT = 'convention — written in a prompt, not enforced by the engine'

/** The relay marker the supervisor seed's prompts use. Matched case-sensitively
 *  because that is how the seeds write it and a lowercase `route-to:` in prose
 *  is far more likely to be a sentence than an instruction. */
export const ROUTE_TO_MARKER = 'ROUTE-TO:'

/** One edge that exists only in a prompt. `line` is the source line, verbatim
 *  (trimmed of surrounding whitespace and nothing else) — the tooltip quotes it
 *  so the operator can judge the heuristic instead of trusting it. */
export interface OrgChartConvention {
  /** Unique within a result: `<from>→<to>`. */
  id: string
  /** The worker whose prompt this line is in. */
  from: string
  /** The worker the line names. */
  to: string
  /** `route-to` when the line is a `ROUTE-TO:` relay, `mention` when the prompt
   *  merely names the other worker. Both are conventions; neither is wiring. */
  kind: 'route-to' | 'mention'
  /** The matched prompt line, verbatim. */
  line: string
}

/** Worker names are kebab-case, so `\b` is useless here: it treats `-` as a
 *  boundary and would find `keeper` inside `book-keeper`. A name character is
 *  anything that could be part of one. */
const NAME_CHAR = /[A-Za-z0-9_-]/

/**
 * Does this line name this worker, on word boundaries?
 *
 * Substring hygiene is the whole game: `keeper` must never match inside
 * `book-keeper`, and `email-answerer` must not match inside
 * `email-answerer-v2`. Exported because it is the rule the overlay stands on.
 */
export function mentionsWorkerName(line: string, name: string): boolean {
  return mentionIndex(line, name) !== -1
}

/** The first boundary-respecting index of `name` in `line`, or -1. */
function mentionIndex(line: string, name: string): number {
  if (name === '') return -1
  let at = line.indexOf(name)
  while (at !== -1) {
    const before = at === 0 ? '' : line[at - 1]
    const after = line[at + name.length] ?? ''
    if (!NAME_CHAR.test(before) && !NAME_CHAR.test(after)) return at
    at = line.indexOf(name, at + 1)
  }
  return -1
}

/**
 * Read the conventions out of the prompts (§6.6).
 *
 * For every worker, every line of its `system_prompt` is scanned for (a) a
 * `ROUTE-TO:` relay naming another worker and (b) any other worker's exact
 * name. One edge per ordered pair: a `route-to` line always wins over a bare
 * mention, and otherwise the FIRST matching line is the one quoted, because the
 * first time a prompt names another worker is usually where it says why.
 *
 * Self-references are dropped — "you are email-answerer" is not a convention.
 * Pure and deterministic: sorted by source then target, ties impossible.
 */
export function inferConventions(workers: readonly Worker[]): OrgChartConvention[] {
  const names = [...workers].map((w) => w.name).filter((n) => n !== '').sort(compareText)
  const found = new Map<string, OrgChartConvention>()
  for (const source of [...workers].sort((a, b) => compareText(a.name, b.name))) {
    for (const line of (source.system_prompt ?? '').split(/\r?\n/)) {
      const trimmed = line.trim()
      if (trimmed === '') continue
      const relayAt = trimmed.indexOf(ROUTE_TO_MARKER)
      for (const target of names) {
        if (target === source.name) continue
        const at = mentionIndex(trimmed, target)
        if (at === -1) continue
        const kind: OrgChartConvention['kind'] =
          relayAt !== -1 && at > relayAt ? 'route-to' : 'mention'
        const id = `${source.name}→${target}`
        const already = found.get(id)
        // A relay upgrades an earlier bare mention; a bare mention never
        // downgrades a relay, and never replaces an earlier mention.
        if (already !== undefined && !(already.kind === 'mention' && kind === 'route-to')) continue
        found.set(id, { id, from: source.name, to: target, kind, line: trimmed })
      }
    }
  }
  return [...found.values()].sort(
    (a, b) => compareText(a.from, b.from) || compareText(a.to, b.to),
  )
}

// ---------------------------------------------------------------------------
// Graph helpers
// ---------------------------------------------------------------------------

const edgeKey = (w: RawWire): string =>
  `${w.from ?? ''} ${w.to} ${w.subscription.event_type} ${w.subscription.id}`

const mean = (values: number[]): number => values.reduce((a, b) => a + b, 0) / values.length

interface Edge {
  from: string
  to: string
  key: string
}

/**
 * Mark the edges that close cycles, smallest key first.
 *
 * Kahn's algorithm leaves exactly the nodes that are in — or downstream of — a
 * cycle. Among the edges with both ends in that residue, the lexicographically
 * smallest is marked and the peel is repeated. Deterministic, and it always
 * terminates because every pass marks one edge.
 */
export function markBackEdges(edges: readonly Edge[], nodes: readonly string[]): Set<string> {
  const back = new Set<string>()
  for (;;) {
    const live = edges.filter((e) => !back.has(e.key))
    const residue = unsortableNodes(live, nodes)
    if (residue.size === 0) return back
    const candidates = live
      .filter((e) => residue.has(e.from) && residue.has(e.to))
      .map((e) => e.key)
      .sort(compareText)
    if (candidates.length === 0) return back
    back.add(candidates[0])
  }
}

/** The nodes Kahn's algorithm cannot peel — i.e. those a cycle holds down. */
function unsortableNodes(edges: readonly Edge[], nodes: readonly string[]): Set<string> {
  const indegree = new Map<string, number>(nodes.map((n) => [n, 0]))
  for (const e of edges) indegree.set(e.to, (indegree.get(e.to) ?? 0) + 1)
  const queue = [...nodes].filter((n) => (indegree.get(n) ?? 0) === 0).sort(compareText)
  const peeled = new Set<string>()
  while (queue.length > 0) {
    const name = queue.shift() as string
    if (peeled.has(name)) continue
    peeled.add(name)
    for (const edge of edges.filter((candidate) => candidate.from === name)) {
      const left = (indegree.get(edge.to) ?? 0) - 1
      indegree.set(edge.to, left)
      if (left === 0) queue.push(edge.to)
    }
    queue.sort(compareText)
  }
  return new Set(nodes.filter((n) => !peeled.has(n)))
}

/** Rank = longest path from an entry (a node with no forward predecessor). */
function longestPathRanks(edges: readonly Edge[], nodes: readonly string[]): Map<string, number> {
  const rank = new Map<string, number>(nodes.map((n) => [n, 0]))
  // |V| relaxation passes settle a longest path on any DAG, and the graphs
  // here are a dozen nodes at most.
  for (let pass = 0; pass < nodes.length; pass += 1) {
    let changed = false
    for (const e of [...edges].sort((a, b) => compareText(a.key, b.key))) {
      const want = (rank.get(e.from) ?? 0) + 1
      if (want > (rank.get(e.to) ?? 0)) {
        rank.set(e.to, want)
        changed = true
      }
    }
    if (!changed) break
  }
  return rank
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

/** A down-across-down route. Collapses to a straight drop when it can. */
function elbow(start: OrgChartPoint, end: OrgChartPoint, midY: number): OrgChartPoint[] {
  if (start.x === end.x) return [start, end]
  return [start, { x: start.x, y: midY }, { x: end.x, y: midY }, end]
}

/** A back edge's return: out of the source's right flank, up the lane, and in
 *  through the target's right flank. A self-wake uses two different heights so
 *  it reads as a loop rather than a dot. */
function returnLane(
  source: OrgChartNode,
  target: OrgChartNode,
  laneX: number,
): OrgChartPoint[] {
  const out = source.y + (source.name === target.name ? source.height / 3 : source.height / 2)
  const back = target.y + (source.name === target.name ? (target.height * 2) / 3 : target.height / 2)
  return [
    { x: source.x + source.width, y: out },
    { x: laneX, y: out },
    { x: laneX, y: back },
    { x: target.x + target.width, y: back },
  ]
}

/** Where a label sits on a route: the middle of its longest straight run. */
function longestSegmentMidpoint(points: readonly OrgChartPoint[]): OrgChartPoint {
  let best = 0
  let bestLength = -1
  for (let i = 0; i < points.length - 1; i += 1) {
    const length =
      Math.abs(points[i + 1].x - points[i].x) + Math.abs(points[i + 1].y - points[i].y)
    if (length > bestLength) {
      bestLength = length
      best = i
    }
  }
  const a = points[best]
  const b = points[Math.min(best + 1, points.length - 1)]
  return { x: Math.round((a.x + b.x) / 2), y: Math.round((a.y + b.y) / 2) }
}
