import { describe, it, expect } from 'vitest'
import {
  ORG_CHART_METRICS,
  PROPAGATION_CAVEAT,
  SCHEDULE_STRIKE_LIMIT,
  assignLabelLanes,
  clockGutterWidth,
  labelWidth,
  PROPAGATION_MAX_DEPTH,
  PROPAGATION_NOTHING_SUBSCRIBES,
  cronHours,
  inferConventions,
  layoutOrgChart,
  markBackEdges,
  matchesWorkerLifecycle,
  mentionsWorkerName,
  propagateEvent,
  subscriptionProducers,
  wireLabel,
  type OrgChartLayout,
} from './orgchart.js'
import { blankEnvelope, type Subscription, type ProjectEvent } from './events.js'
import type { Schedule } from './schedules.js'
import type { Worker } from './workers.js'

// ---------------------------------------------------------------------------
// Fixtures
//
// The thirteen seed shapes are HAND-DECLARED here — worker and subscription
// rows in the shape `go/topology` renders them, transcribed, not imported.
// `web/` cannot import from `go/`, and a fixture that could drift from the
// engine is exactly the fixture worth reading: if a seed changes shape, this
// file is where the chart's assumptions are re-stated.
// ---------------------------------------------------------------------------

function worker(name: string, overrides: Partial<Worker> = {}): Worker {
  return {
    project: 'p',
    name,
    description: `${name} does a thing`,
    system_prompt: `You are ${name}.`,
    mcp_config: {},
    image: '',
    briefing: null,
    max_instances: 1,
    enabled: true,
    frozen: false,
    created_at: 0,
    updated_at: 0,
    ...overrides,
  }
}

function sub(
  id: string,
  event_type: string,
  target: string,
  filter: Record<string, unknown> = {},
): Subscription {
  return {
    id,
    project: 'p',
    event_type,
    filter,
    worker: target,
    max_firings_per_hour: 0,
    enabled: true,
    created_at: 0,
    updated_at: 0,
  }
}

function schedule(id: string, target: string, cron: string): Schedule {
  return {
    id,
    project: 'p',
    worker: target,
    cron,
    input: 'do the thing',
    enabled: true,
    created_at: 0,
    updated_at: 0,
  }
}

function externalEvent(type: string): ProjectEvent {
  return {
    id: `e-${type}`,
    project: 'p',
    type,
    text: '',
    envelope: {
      depth: 0,
      source: 'external',
      worker: '',
      session_id: '',
      interactive: false,
      attention_requested: false,
    },
    occurred_at: 0,
    created_at: 0,
    delivered: true,
  }
}

interface Seed {
  name: string
  workers: Worker[]
  subscriptions: Subscription[]
  schedules: Schedule[]
  /** worker name → expected rank. */
  ranks: Record<string, number>
  pips: string[]
  wires: number
  clocks: number
}

const SEEDS: Seed[] = [
  {
    name: 'solo',
    workers: [worker('assistant')],
    subscriptions: [],
    schedules: [schedule('s1', 'assistant', '0 9 * * *')],
    ranks: { assistant: 0 },
    pips: [],
    wires: 0,
    clocks: 1,
  },
  {
    name: 'solo-memory',
    workers: [worker('assistant')],
    subscriptions: [],
    schedules: [schedule('s1', 'assistant', '0 6 * * *')],
    ranks: { assistant: 0 },
    pips: [],
    wires: 0,
    clocks: 1,
  },
  {
    name: 'actor-critic',
    workers: [worker('actor'), worker('critic')],
    subscriptions: [
      sub('s1', 'actor.task', 'actor'),
      sub('s2', 'worker.finished', 'critic', { worker: 'actor' }),
    ],
    schedules: [],
    ranks: { actor: 0, critic: 1 },
    pips: ['actor.task'],
    wires: 2,
    clocks: 0,
  },
  {
    name: 'sham-critic',
    workers: [worker('actor'), worker('sham-critic')],
    subscriptions: [
      sub('s1', 'actor.task', 'actor'),
      sub('s2', 'worker.finished', 'sham-critic', { worker: 'actor' }),
    ],
    schedules: [],
    ranks: { actor: 0, 'sham-critic': 1 },
    pips: ['actor.task'],
    wires: 2,
    clocks: 0,
  },
  {
    name: 'frozen-scorer',
    workers: [worker('actor'), worker('critic'), worker('scorer', { frozen: true })],
    subscriptions: [
      sub('s1', 'actor.task', 'actor'),
      sub('s2', 'worker.finished', 'critic', { worker: 'actor' }),
      sub('s3', 'worker.finished', 'scorer', { worker: 'actor' }),
    ],
    schedules: [],
    ranks: { actor: 0, critic: 1, scorer: 1 },
    pips: ['actor.task'],
    wires: 3,
    clocks: 0,
  },
  {
    name: 'escalation',
    workers: [worker('responder')],
    subscriptions: [sub('s1', 'support.request', 'responder')],
    schedules: [],
    ranks: { responder: 0 },
    pips: ['support.request'],
    wires: 1,
    clocks: 0,
  },
  {
    name: 'self-organizing',
    workers: [worker('team')],
    subscriptions: [sub('s1', 'work.request', 'team')],
    schedules: [],
    ranks: { team: 0 },
    pips: ['work.request'],
    wires: 1,
    clocks: 0,
  },
  {
    name: 'blackboard',
    workers: [worker('analyst'), worker('planner'), worker('writer')],
    subscriptions: [
      sub('s1', 'work.request', 'analyst'),
      sub('s2', 'work.request', 'planner'),
      sub('s3', 'work.request', 'writer'),
    ],
    schedules: [],
    ranks: { analyst: 0, planner: 0, writer: 0 },
    pips: ['work.request'],
    wires: 3,
    clocks: 0,
  },
  {
    name: 'supervisor',
    workers: [worker('dispatcher'), worker('researcher'), worker('writer')],
    subscriptions: [
      sub('s1', 'work.request', 'dispatcher'),
      sub('s2', 'worker.finished', 'researcher', { worker: 'dispatcher' }),
      sub('s3', 'worker.finished', 'writer', { worker: 'dispatcher' }),
    ],
    schedules: [],
    ranks: { dispatcher: 0, researcher: 1, writer: 1 },
    pips: ['work.request'],
    wires: 3,
    clocks: 0,
  },
  {
    name: 'debate',
    workers: [worker('aggregator'), worker('optimist'), worker('pessimist')],
    subscriptions: [
      sub('s1', 'work.request', 'optimist'),
      sub('s2', 'work.request', 'pessimist'),
      sub('s3', 'worker.finished', 'aggregator', { worker: 'optimist' }),
      sub('s4', 'worker.finished', 'aggregator', { worker: 'pessimist' }),
    ],
    schedules: [],
    ranks: { optimist: 0, pessimist: 0, aggregator: 1 },
    pips: ['work.request'],
    wires: 4,
    clocks: 0,
  },
  {
    name: 'assembly-line',
    workers: [worker('drafter'), worker('editor'), worker('publisher')],
    subscriptions: [
      sub('s1', 'work.request', 'drafter'),
      sub('s2', 'worker.finished', 'editor', { worker: 'drafter' }),
      sub('s3', 'worker.finished', 'publisher', { worker: 'editor' }),
    ],
    schedules: [],
    ranks: { drafter: 0, editor: 1, publisher: 2 },
    pips: ['work.request'],
    wires: 3,
    clocks: 0,
  },
  {
    name: 'temporal-hierarchy',
    workers: [worker('doer'), worker('helper'), worker('strategist')],
    subscriptions: [
      sub('s1', 'work.request', 'doer'),
      sub('s2', 'work.request', 'helper'),
    ],
    schedules: [schedule('sc1', 'strategist', '0 7 * * 1')],
    ranks: { doer: 0, helper: 0, strategist: 0 },
    pips: ['work.request'],
    wires: 2,
    clocks: 1,
  },
  {
    name: 'hypothesis-lab',
    workers: [worker('checker'), worker('critic'), worker('investigator')],
    subscriptions: [
      sub('s1', 'investigator.task', 'investigator'),
      sub('s2', 'worker.finished', 'critic', { worker: 'investigator' }),
      sub('s3', 'checker.task', 'checker'),
    ],
    schedules: [],
    ranks: { checker: 0, critic: 1, investigator: 0 },
    pips: ['checker.task', 'investigator.task'],
    wires: 3,
    clocks: 0,
  },
]

const run = (seed: Seed): OrgChartLayout =>
  layoutOrgChart(seed.workers, seed.subscriptions, seed.schedules, [])

// ---------------------------------------------------------------------------

describe('layoutOrgChart over the thirteen seed shapes', () => {
  it('covers all thirteen', () => {
    expect(SEEDS).toHaveLength(13)
    expect(new Set(SEEDS.map((s) => s.name)).size).toBe(13)
  })

  for (const seed of SEEDS) {
    describe(seed.name, () => {
      it('draws one node per worker, at the expected rank', () => {
        const layout = run(seed)
        expect(layout.nodes.map((n) => n.name).sort()).toEqual(
          seed.workers.map((w) => w.name).sort(),
        )
        const ranks: Record<string, number> = {}
        for (const node of layout.nodes) ranks[node.name] = node.rank
        expect(ranks).toEqual(seed.ranks)
      })

      it('draws the expected entry pips and wires', () => {
        const layout = run(seed)
        expect(layout.entryPips.map((p) => p.type).sort()).toEqual([...seed.pips].sort())
        expect(layout.wires).toHaveLength(seed.wires)
        expect(layout.clocks).toHaveLength(seed.clocks)
      })

      it('is deterministic — same input, deep-equal output', () => {
        expect(run(seed)).toEqual(run(seed))
      })

      it('is order-independent — shuffled rows lay out identically', () => {
        const shuffled = layoutOrgChart(
          [...seed.workers].reverse(),
          [...seed.subscriptions].reverse(),
          [...seed.schedules].reverse(),
          [],
        )
        expect(shuffled).toEqual(run(seed))
      })

      it('places every node inside the canvas, without overlap', () => {
        const layout = run(seed)
        for (const node of layout.nodes) {
          expect(node.x).toBeGreaterThanOrEqual(0)
          expect(node.y).toBeGreaterThanOrEqual(0)
          expect(node.x + node.width).toBeLessThanOrEqual(layout.width)
          expect(node.y + node.height).toBeLessThanOrEqual(layout.height)
        }
        for (const a of layout.nodes) {
          for (const b of layout.nodes) {
            if (a === b) continue
            const overlaps =
              a.x < b.x + b.width &&
              b.x < a.x + a.width &&
              a.y < b.y + b.height &&
              b.y < a.y + a.height
            expect(overlaps).toBe(false)
          }
        }
      })

      it('routes every wire orthogonally, from a source to its target plate', () => {
        const layout = run(seed)
        const nodes = new Map(layout.nodes.map((n) => [n.name, n]))
        for (const wire of layout.wires) {
          expect(wire.points.length).toBeGreaterThanOrEqual(2)
          for (let i = 0; i < wire.points.length - 1; i += 1) {
            const a = wire.points[i]
            const b = wire.points[i + 1]
            // Orthogonal: every segment is horizontal or vertical (§3.7).
            expect(a.x === b.x || a.y === b.y).toBe(true)
          }
          const target = nodes.get(wire.to)
          expect(target).toBeDefined()
          const last = wire.points[wire.points.length - 1]
          expect(last.y).toBeGreaterThanOrEqual(target?.y ?? 0)
          expect(wire.back).toBe(false)
        }
      })

      it('gives every wire a unique id', () => {
        const layout = run(seed)
        expect(new Set(layout.wires.map((w) => w.id)).size).toBe(layout.wires.length)
      })

      it('spaces the entry pips so two mono labels never collide', () => {
        const layout = run(seed)
        for (let i = 1; i < layout.entryPips.length; i += 1) {
          expect(layout.entryPips[i].x - layout.entryPips[i - 1].x).toBeGreaterThanOrEqual(
            ORG_CHART_METRICS.pipGap,
          )
        }
      })

      // §2 X2: the populated walkthrough found two subscriptions sharing a rank
      // overprinting their riding labels into garbage.
      it('never overprints two riding labels', () => {
        expect(overprintedLabels(run(seed))).toEqual([])
      })

      // §2 X3: dials read as stray circles because they landed on the plate
      // next door. The gutter column is reserved, so they cannot.
      it('docks every dial in the gutter, clear of every plate', () => {
        const layout = run(seed)
        for (const clock of layout.clocks) {
          for (const node of layout.nodes) {
            const overlaps =
              clock.x < node.x + node.width &&
              node.x < clock.x + clock.size &&
              clock.y < node.y + node.height &&
              node.y < clock.y + clock.size
            expect(`${clock.id}/${node.name}: ${overlaps}`).toBe(`${clock.id}/${node.name}: false`)
          }
        }
      })
    })
  }
})

/** Every pair of riding labels whose drawn boxes intersect, as `a|b` strings.
 *  The renderer draws at `labelX`/`labelY` and nowhere else, so this is the
 *  same geometry the canvas has. */
function overprintedLabels(layout: OrgChartLayout): string[] {
  const clashes: string[] = []
  const boxes = layout.wires.map((w) => ({
    id: w.id,
    left: w.labelX - labelWidth(w.label) / 2,
    right: w.labelX + labelWidth(w.label) / 2,
    y: w.labelY,
  }))
  for (let i = 0; i < boxes.length; i += 1) {
    for (let j = i + 1; j < boxes.length; j += 1) {
      const a = boxes[i]
      const b = boxes[j]
      if (
        Math.abs(a.y - b.y) < ORG_CHART_METRICS.labelLineHeight &&
        a.left < b.right &&
        b.left < a.right
      ) {
        clashes.push(`${a.id}|${b.id}`)
      }
    }
  }
  return clashes
}

// ---------------------------------------------------------------------------
// The doc-21 §2 chart defects (W1)
// ---------------------------------------------------------------------------

describe('label lanes (X2)', () => {
  /** The exact shape the walkthrough broke on: ONE event fanning out to TWO
   *  workers in the same rank, so both labels ride the same elbow lane. */
  const fanOut = (): OrgChartLayout =>
    layoutOrgChart(
      [worker('email-answerer'), worker('archivist'), worker('email-reviewer')],
      [
        sub('s1', 'worker.finished', 'archivist', { worker: 'email-answerer' }),
        sub('s2', 'worker.finished', 'email-reviewer', { worker: 'email-answerer' }),
      ],
      [],
      [],
    )

  it('puts the two labels of a shared run in different lanes', () => {
    const layout = fanOut()
    expect(layout.wires).toHaveLength(2)
    expect(layout.wires.map((w) => w.labelLane).sort()).toEqual([0, 1])
    expect(overprintedLabels(layout)).toEqual([])
  })

  it('offsets labelY by exactly the lane, so the renderer needs no arithmetic', () => {
    const layout = fanOut()
    const [a, b] = [...layout.wires].sort((x, y) => x.labelLane - y.labelLane)
    expect(b.labelY - a.labelY).toBe(ORG_CHART_METRICS.labelLineHeight)
  })

  it('is deterministic and order-independent', () => {
    expect(fanOut()).toEqual(fanOut())
  })

  it('leaves a lone label on the wire itself', () => {
    const layout = layoutOrgChart([worker('a')], [sub('s1', 'x.y', 'a')], [], [])
    expect(layout.wires[0].labelLane).toBe(0)
  })

  describe('assignLabelLanes', () => {
    it('stacks labels that overlap and reuses lane 0 for ones that do not', () => {
      expect(
        assignLabelLanes([
          { x: 100, y: 50, width: 80 },
          { x: 110, y: 50, width: 80 },
          { x: 400, y: 50, width: 80 },
          { x: 120, y: 50, width: 80 },
        ]),
      ).toEqual([0, 1, 0, 2])
    })

    it('does not stack labels a whole line apart already', () => {
      expect(
        assignLabelLanes([
          { x: 100, y: 50, width: 80 },
          { x: 100, y: 50 + ORG_CHART_METRICS.labelLineHeight, width: 80 },
        ]),
      ).toEqual([0, 0])
    })

    it('is first-come-first-served, so the caller order fixes the result', () => {
      const boxes = [
        { x: 100, y: 0, width: 40 },
        { x: 110, y: 0, width: 40 },
      ]
      expect(assignLabelLanes(boxes)).toEqual([0, 1])
      expect(assignLabelLanes([...boxes].reverse())).toEqual([0, 1])
    })

    it('has nothing to say about an empty chart', () => {
      expect(assignLabelLanes([])).toEqual([])
    })
  })
})

describe('the dial gutter (X3)', () => {
  it('reserves a gutter column left of every plate when the project has clocks', () => {
    const withClocks = layoutOrgChart(
      [worker('a'), worker('b')],
      [],
      [schedule('s1', 'b', '0 9 * * *')],
      [],
    )
    const gutter = clockGutterWidth(true)
    expect(gutter).toBe(ORG_CHART_METRICS.clockSize + ORG_CHART_METRICS.clockGap)
    // Both plates keep the gutter, so the rank stays on one grid — and the
    // dial of the second worker cannot land on the first worker's plate.
    const [a, b] = withClocks.nodes
    expect(b.x - (a.x + a.width)).toBeGreaterThanOrEqual(ORG_CHART_METRICS.hGap + gutter)
    expect(withClocks.clocks[0].x + withClocks.clocks[0].size).toBeLessThanOrEqual(b.x)
    expect(withClocks.clocks[0].x).toBeGreaterThanOrEqual(a.x + a.width)
  })

  it('claims no gutter at all when nothing is scheduled', () => {
    const bare = layoutOrgChart([worker('a'), worker('b')], [], [], [])
    expect(clockGutterWidth(false)).toBe(0)
    expect(bare.nodes[1].x - (bare.nodes[0].x + bare.nodes[0].width)).toBe(ORG_CHART_METRICS.hGap)
  })

  it('claims no gutter for a schedule whose worker has no plate', () => {
    const ghost = layoutOrgChart([worker('a')], [], [schedule('s1', 'ghost', '0 9 * * *')], [])
    const bare = layoutOrgChart([worker('a')], [], [], [])
    expect(ghost.nodes[0].x).toBe(bare.nodes[0].x)
  })
})

describe('a dead clock (X10)', () => {
  const halted = (over: Partial<Schedule>): OrgChartLayout =>
    layoutOrgChart([worker('a')], [], [{ ...schedule('s1', 'a', '0 9 * * *'), ...over }], [])

  it('is halted only when disabled AND out of strikes', () => {
    expect(halted({ enabled: false, provision_failures: SCHEDULE_STRIKE_LIMIT }).clocks[0].halted).toBe(
      true,
    )
    expect(halted({ enabled: false, provision_failures: 4 }).clocks[0].halted).toBe(false)
    expect(halted({ enabled: true, provision_failures: 9 }).clocks[0].halted).toBe(false)
    expect(halted({}).clocks[0].halted).toBe(false)
  })

  it('keeps the five-strike number where the engine put it', () => {
    expect(SCHEDULE_STRIKE_LIMIT).toBe(5)
  })
})

describe('the entry pip caption (X4)', () => {
  it('marks a pip wired when a wire leaves it — its label is on the wire', () => {
    const layout = layoutOrgChart([worker('a')], [sub('s1', 'email.received', 'a')], [], [])
    expect(layout.entryPips[0]).toMatchObject({ type: 'email.received', wired: true })
    expect(layout.wires[0].label).toBe('email.received')
  })

  it('leaves a pip nothing subscribes to unwired, so it keeps its caption', () => {
    const layout = layoutOrgChart([worker('a')], [], [], [externalEvent('email.received')])
    expect(layout.entryPips[0].wired).toBe(false)
  })
})

describe('layoutOrgChart — specifics', () => {
  it('is empty but legal for an empty project', () => {
    const layout = layoutOrgChart([], [], [], [])
    expect(layout.nodes).toEqual([])
    expect(layout.wires).toEqual([])
    expect(layout.entryPips).toEqual([])
    expect(layout.width).toBeGreaterThan(0)
    expect(layout.height).toBeGreaterThan(0)
  })

  it('marks a pip external only when an external event of that type was seen', () => {
    const layout = layoutOrgChart(
      [worker('responder')],
      [sub('s1', 'support.request', 'responder'), sub('s2', 'billing.query', 'responder')],
      [],
      [externalEvent('support.request')],
    )
    const pips = Object.fromEntries(layout.entryPips.map((p) => [p.type, p.external]))
    expect(pips).toEqual({ 'billing.query': false, 'support.request': true })
  })

  it('draws a pip for an external event nothing subscribes to', () => {
    const layout = layoutOrgChart([worker('a')], [], [], [externalEvent('email.received')])
    expect(layout.entryPips.map((p) => p.type)).toEqual(['email.received'])
    expect(layout.wires).toEqual([])
  })

  it('drops a subscription that points at a worker which does not exist', () => {
    const layout = layoutOrgChart([worker('a')], [sub('s1', 'x.y', 'ghost')], [], [])
    expect(layout.wires).toEqual([])
    expect(layout.entryPips).toEqual([])
  })

  it('carries frozen, enabled and max_instances onto the plate', () => {
    const layout = layoutOrgChart(
      [worker('scorer', { frozen: true, enabled: false, max_instances: 3 })],
      [],
      [],
      [],
    )
    expect(layout.nodes[0]).toMatchObject({ frozen: true, enabled: false, maxInstances: 3 })
  })

  it('keeps a disabled subscription on the chart, marked disabled', () => {
    const layout = layoutOrgChart(
      [worker('a')],
      [{ ...sub('s1', 'x.y', 'a'), enabled: false }],
      [],
      [],
    )
    expect(layout.wires).toHaveLength(1)
    expect(layout.wires[0].enabled).toBe(false)
  })

  it('stacks two schedules on one worker without overlapping the dials', () => {
    const layout = layoutOrgChart(
      [worker('a')],
      [],
      [schedule('s2', 'a', '0 17 * * *'), schedule('s1', 'a', '0 9 * * *')],
      [],
    )
    expect(layout.clocks.map((c) => c.cron)).toEqual(['0 17 * * *', '0 9 * * *'])
    expect(layout.clocks[1].y - layout.clocks[0].y).toBeGreaterThanOrEqual(
      ORG_CHART_METRICS.clockSize,
    )
    // Docked to the LEFT of the plate, and still on the canvas.
    expect(layout.clocks[0].x).toBeGreaterThanOrEqual(0)
    expect(layout.clocks[0].x + layout.clocks[0].size).toBeLessThanOrEqual(layout.nodes[0].x)
  })

  it('ignores a schedule whose worker has no plate', () => {
    const layout = layoutOrgChart([worker('a')], [], [schedule('s1', 'ghost', '0 9 * * *')], [])
    expect(layout.clocks).toEqual([])
  })

  it('fans in from every worker when a lifecycle subscription has no filter', () => {
    const layout = layoutOrgChart(
      [worker('a'), worker('b'), worker('watcher')],
      [sub('s1', 'worker.finished', 'watcher')],
      [],
      [],
    )
    expect(layout.wires.map((w) => w.from).sort()).toEqual(['a', 'b', 'watcher'])
    // watcher → watcher closes a cycle and is the one back edge.
    expect(layout.wires.filter((w) => w.back).map((w) => w.from)).toEqual(['watcher'])
  })
})

describe('layoutOrgChart — cycles', () => {
  const cycle = (): OrgChartLayout =>
    layoutOrgChart(
      [worker('a'), worker('b')],
      [
        sub('s1', 'worker.finished', 'b', { worker: 'a' }),
        sub('s2', 'worker.finished', 'a', { worker: 'b' }),
      ],
      [],
      [],
    )

  it('terminates and breaks exactly one edge', () => {
    const layout = cycle()
    expect(layout.wires.filter((w) => w.back)).toHaveLength(1)
  })

  it('breaks the lexicographically smallest edge, so the cut never moves', () => {
    const layout = cycle()
    // Keys sort "a b …" before "b a …": the a→b edge is the smallest.
    const broken = layout.wires.find((w) => w.back)
    expect(broken?.from).toBe('a')
    expect(broken?.to).toBe('b')
    expect(layout).toEqual(cycle())
  })

  it('routes a back edge out through the return lane', () => {
    const layout = cycle()
    const broken = layout.wires.find((w) => w.back)
    expect(broken?.points).toHaveLength(4)
    const laneX = Math.max(...(broken?.points ?? []).map((p) => p.x))
    expect(laneX).toBeLessThanOrEqual(layout.width)
    for (const node of layout.nodes) expect(laneX).toBeGreaterThan(node.x + node.width)
  })

  it('does not hang on a three-worker ring', () => {
    const layout = layoutOrgChart(
      [worker('a'), worker('b'), worker('c')],
      [
        sub('s1', 'worker.finished', 'b', { worker: 'a' }),
        sub('s2', 'worker.finished', 'c', { worker: 'b' }),
        sub('s3', 'worker.finished', 'a', { worker: 'c' }),
      ],
      [],
      [],
    )
    expect(layout.wires.filter((w) => w.back)).toHaveLength(1)
    expect(layout.nodes.map((n) => n.rank)).toEqual([0, 1, 2])
  })

  it('handles a worker that wakes itself', () => {
    const layout = layoutOrgChart(
      [worker('a')],
      [sub('s1', 'worker.finished', 'a', { worker: 'a' })],
      [],
      [],
    )
    expect(layout.wires).toHaveLength(1)
    expect(layout.wires[0].back).toBe(true)
    expect(layout.nodes[0].rank).toBe(0)
    // A self-wake leaves and re-enters at different heights, or it is a dot.
    const ys = layout.wires[0].points.map((p) => p.y)
    expect(ys[0]).not.toBe(ys[ys.length - 1])
  })
})

describe('markBackEdges', () => {
  it('marks nothing on a DAG', () => {
    expect(
      markBackEdges(
        [
          { from: 'a', to: 'b', key: 'a b' },
          { from: 'b', to: 'c', key: 'b c' },
        ],
        ['a', 'b', 'c'],
      ).size,
    ).toBe(0)
  })

  it('marks one edge per independent cycle', () => {
    const back = markBackEdges(
      [
        { from: 'a', to: 'b', key: 'a b' },
        { from: 'b', to: 'a', key: 'b a' },
        { from: 'c', to: 'd', key: 'c d' },
        { from: 'd', to: 'c', key: 'd c' },
      ],
      ['a', 'b', 'c', 'd'],
    )
    expect([...back].sort()).toEqual(['a b', 'c d'])
  })
})

describe('subscriptionProducers', () => {
  const names = ['actor', 'critic']
  const cases: [string, Record<string, unknown>, string[]][] = [
    ['actor.task', {}, []],
    ['worker.finished', { worker: 'actor' }, ['actor']],
    ['worker.failed', { worker: 'critic' }, ['critic']],
    ['worker.finished', { worker: 'ghost' }, ['actor', 'critic']],
    ['worker.finished', {}, ['actor', 'critic']],
    ['worker.*', {}, ['actor', 'critic']],
    ['*', {}, ['actor', 'critic']],
    ['email.received', { source: 'external' }, []],
  ]
  for (const [event_type, filter, want] of cases) {
    it(`${event_type} ${JSON.stringify(filter)} → ${want.join(',') || 'entry pip'}`, () => {
      expect(subscriptionProducers({ event_type, filter }, names)).toEqual(want)
    })
  }

  it('knows which patterns name a lifecycle event', () => {
    expect(matchesWorkerLifecycle('worker.finished')).toBe(true)
    expect(matchesWorkerLifecycle('worker.failed')).toBe(true)
    expect(matchesWorkerLifecycle('worker.*')).toBe(true)
    expect(matchesWorkerLifecycle('worker.started')).toBe(false)
    expect(matchesWorkerLifecycle('')).toBe(false)
  })
})

describe('cronHours', () => {
  const cases: [string, number[], boolean][] = [
    ['0 9 * * *', [9], true],
    ['0 0 * * *', [0], true],
    ['30 9,17 * * *', [9, 17], true],
    ['0 9-11 * * *', [9, 10, 11], true],
    ['0 * * * *', [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23], true],
    ['0 */6 * * *', [0, 6, 12, 18], true],
    ['0 9-17/4 * * *', [9, 13, 17], true],
    ['0 9 * *', [], false],
    ['0 24 * * *', [], false],
    ['0 nine * * *', [], false],
    ['', [], false],
  ]
  for (const [cron, hours, known] of cases) {
    it(`${cron || '(empty)'} → ${known ? hours.join(',') : 'unknown'}`, () => {
      expect(cronHours(cron)).toEqual({ hours, known })
    })
  }

  it('shows no ticks rather than the wrong ones', () => {
    const layout = layoutOrgChart([worker('a')], [], [schedule('s1', 'a', 'nonsense')], [])
    expect(layout.clocks[0]).toMatchObject({ hours: [], hoursKnown: false })
  })
})

describe('propagateEvent', () => {
  const chain = [
    sub('s1', 'email.received', 'answerer'),
    sub('s2', 'worker.finished', 'reviewer', { worker: 'answerer' }),
    sub('s3', 'worker.finished', 'archivist', { worker: 'reviewer' }),
  ]
  const arriving = (type: string) => ({ type, envelope: blankEnvelope() })

  it('chains hop by hop, the way the router would', () => {
    const { hops, stopped } = propagateEvent(arriving('email.received'), chain)
    expect(hops.map((h) => h.wakes.map((w) => w.worker))).toEqual([
      ['answerer'],
      ['reviewer'],
      ['archivist'],
      [],
    ])
    expect(stopped).toBe(false)
  })

  it('names the event that arrived at each hop', () => {
    const { hops } = propagateEvent(arriving('email.received'), chain)
    expect(hops[0].wakes[0]).toMatchObject({
      depth: 0,
      eventType: 'email.received',
      from: '',
      worker: 'answerer',
      subscriptionId: 's1',
    })
    expect(hops[1].wakes[0]).toMatchObject({
      depth: 1,
      eventType: 'worker.finished',
      from: 'answerer',
      worker: 'reviewer',
    })
  })

  it('ends with an empty hop, which the panel renders as "nothing subscribes"', () => {
    const { hops } = propagateEvent(arriving('email.received'), chain)
    expect(hops[hops.length - 1].wakes).toEqual([])
    expect(PROPAGATION_NOTHING_SUBSCRIBES).toBe('(nothing subscribes)')
  })

  it('wakes nobody, and says so, when nothing matches', () => {
    const { hops, stopped } = propagateEvent(arriving('nothing.matches'), chain)
    expect(hops).toEqual([{ depth: 0, wakes: [] }])
    expect(stopped).toBe(false)
  })

  it('runs a loop into the stop line rather than for ever', () => {
    const loop = [
      sub('l1', 'go', 'a'),
      sub('l2', 'worker.finished', 'b', { worker: 'a' }),
      sub('l3', 'worker.finished', 'a', { worker: 'b' }),
    ]
    const { hops, stopped, maxDepth } = propagateEvent(arriving('go'), loop)
    expect(maxDepth).toBe(PROPAGATION_MAX_DEPTH)
    expect(hops[hops.length - 1].depth).toBe(PROPAGATION_MAX_DEPTH)
    expect(stopped).toBe(true)
  })

  it('honours a shallower ruler when one is asked for', () => {
    const { hops, stopped } = propagateEvent(arriving('email.received'), chain, 1)
    expect(hops.map((h) => h.depth)).toEqual([0, 1])
    expect(stopped).toBe(true)
  })

  it('does not model rate limits, max_instances or budget, and says so once', () => {
    expect(PROPAGATION_CAVEAT).toContain('Rate limits, max_instances gating and budget stops')
    expect(PROPAGATION_CAVEAT).toContain('not modelled')
  })

  it('is deterministic', () => {
    expect(propagateEvent(arriving('email.received'), chain)).toEqual(
      propagateEvent(arriving('email.received'), chain),
    )
  })
})

// ---------------------------------------------------------------------------
// OC3 — the conventions overlay (§6.6, K4)
// ---------------------------------------------------------------------------

describe('mentionsWorkerName', () => {
  const cases: [string, string, boolean][] = [
    ['hand off to keeper when done', 'keeper', true],
    // The trap from the mock-script lore: a substring match here would draw an
    // edge to a worker the prompt never names.
    ['hand off to book-keeper when done', 'keeper', false],
    ['ask keeper-of-records first', 'keeper', false],
    ['keeper starts the line', 'keeper', true],
    ['the line ends with keeper', 'keeper', true],
    ['(keeper) in brackets', 'keeper', true],
    ['keeper2 is someone else', 'keeper', false],
    ['keeper_two is someone else', 'keeper', false],
    ['email-answerer answers', 'email-answerer', true],
    ['email-answerer-v2 answers', 'email-answerer', false],
    ['nobody here', 'keeper', false],
  ]
  for (const [line, name, want] of cases) {
    it(`${want ? 'finds' : 'does not find'} ${name} in "${line}"`, () => {
      expect(mentionsWorkerName(line, name)).toBe(want)
    })
  }
})

describe('inferConventions', () => {
  const fleet = (prompts: Record<string, string>): Worker[] =>
    Object.entries(prompts).map(([name, system_prompt]) => worker(name, { system_prompt }))

  it('reads a ROUTE-TO relay and quotes the line verbatim', () => {
    expect(
      inferConventions(
        fleet({
          supervisor: 'Delegate.\n  ROUTE-TO: researcher when the question needs sources.\n',
          researcher: 'You find sources.',
        }),
      ),
    ).toEqual([
      {
        id: 'supervisor→researcher',
        from: 'supervisor',
        to: 'researcher',
        kind: 'route-to',
        line: 'ROUTE-TO: researcher when the question needs sources.',
      },
    ])
  })

  it('reads a bare mention as a mention, not a relay', () => {
    const found = inferConventions(
      fleet({ writer: 'Ask editor to check your work.', editor: 'You check work.' }),
    )
    expect(found).toHaveLength(1)
    expect(found[0].kind).toBe('mention')
    expect(found[0].line).toBe('Ask editor to check your work.')
  })

  it('never draws a worker to itself', () => {
    expect(inferConventions(fleet({ solo: 'You are solo. solo does everything.' }))).toEqual([])
  })

  it('does not match a name inside a longer name', () => {
    expect(
      inferConventions(fleet({ ledger: 'Send totals to the book-keeper.', keeper: 'You keep books.' })),
    ).toEqual([])
  })

  it('a relay upgrades an earlier bare mention, and keeps the relay line', () => {
    const found = inferConventions(
      fleet({
        supervisor: 'researcher is the one who reads.\nROUTE-TO: researcher',
        researcher: 'You read.',
      }),
    )
    expect(found).toHaveLength(1)
    expect(found[0].kind).toBe('route-to')
    expect(found[0].line).toBe('ROUTE-TO: researcher')
  })

  it('keeps the first line when a worker is named twice', () => {
    const found = inferConventions(fleet({ a: 'first: b.\nsecond: b again.', b: 'You are b.' }))
    expect(found).toHaveLength(1)
    expect(found[0].line).toBe('first: b.')
  })

  it('is deterministic and sorted by source then target', () => {
    const workers = fleet({
      zeta: 'talk to alpha and beta.',
      alpha: 'talk to beta.',
      beta: 'You are beta.',
    })
    const once = inferConventions(workers)
    expect(once.map((c) => c.id)).toEqual(['alpha→beta', 'zeta→alpha', 'zeta→beta'])
    expect(inferConventions([...workers].reverse())).toEqual(once)
  })

  it('says nothing about a fleet whose prompts name nobody', () => {
    for (const seed of SEEDS) expect(inferConventions(seed.workers)).toEqual([])
  })
})

describe('wireLabel', () => {
  it('is the event type alone when there is no filter', () => {
    expect(wireLabel({ event_type: 'work.request', filter: {} })).toBe('work.request')
  })

  it('carries the filter, keys sorted, so the label never reshuffles', () => {
    expect(
      wireLabel({ event_type: 'worker.finished', filter: { worker: 'actor', source: 'worker' } }),
    ).toBe('worker.finished {source: worker, worker: actor}')
  })
})
