// report.test.ts — the offline unit layer for the rig's arithmetic.
//
// Runs with node's built-in test runner and needs NO stack, no docker, no
// network:
//
//   ./e2e/experiments/run.sh test
//
// These fixtures are the outcome tables a real run of
// configs/actor-critic-vs-sham-vs-solo.ts produces, hand-written here so the
// ranking and variance computation can be broken without a stack being
// available to notice. If the demo report ever disagrees with
// `demoOutcomes()`, one of the two is wrong and both are cheap to check.

import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  buildReport,
  metricKeys,
  rank,
  renderTable,
  round6,
  runMetrics,
  stat,
  summarise,
  type RunOutcome,
} from './report'

const PROPS = ['headline-rule', 'reordered-only', 'output-changed'] as const

const BASELINE = 'The catalogue note is filed, plain and unadorned.'
const IMPROVED = 'Headline: the apple crop is in.\nThe catalogue note is filed.'
const REORDERED = 'XPS-REORDERED: date first, totals last. The catalogue note is filed.'

/** One round, with only the fields a metric reads spelled out. */
function round(
  n: number,
  output: string,
  properties: Record<string, boolean>,
  opts: { statuses?: string[]; writes?: number } = {},
): RunOutcome['rounds'][number] {
  return {
    round: n,
    taskText: 'task',
    deliveryStatuses: opts.statuses ?? ['ok'],
    promptWrites: Array.from({ length: opts.writes ?? 0 }, (_, i) => ({
      by: 'critic',
      target: 'actor',
      rationale: `rationale ${i}`,
    })),
    output,
    workerPromptAfter: 'prompt',
    properties,
  }
}

/** The three-arm, two-round, two-repetition table the demo config produces. */
function demoOutcomes(): RunOutcome[] {
  const runs: RunOutcome[] = []
  for (const rep of [1, 2]) {
    runs.push({
      arm: 'actor-critic',
      repetition: rep,
      rounds: [
        round(1, BASELINE, { 'headline-rule': false, 'reordered-only': false, 'output-changed': false }, { writes: 1 }),
        round(2, IMPROVED, { 'headline-rule': true, 'reordered-only': false, 'output-changed': true }, { writes: 1 }),
      ],
    })
  }
  for (const rep of [1, 2]) {
    runs.push({
      arm: 'sham-critic',
      repetition: rep,
      rounds: [
        round(1, BASELINE, { 'headline-rule': false, 'reordered-only': false, 'output-changed': false }, { writes: 1 }),
        round(2, REORDERED, { 'headline-rule': false, 'reordered-only': true, 'output-changed': true }, { writes: 1 }),
      ],
    })
  }
  for (const rep of [1, 2]) {
    runs.push({
      arm: 'solo',
      repetition: rep,
      rounds: [
        round(1, BASELINE, { 'headline-rule': false, 'reordered-only': false, 'output-changed': false }),
        round(2, BASELINE, { 'headline-rule': false, 'reordered-only': false, 'output-changed': false }),
      ],
    })
  }
  return runs
}

// ── Per-run metrics ─────────────────────────────────────────────────────────

test('runMetrics turns one run into rates and counts', () => {
  const run = demoOutcomes()[0]
  assert.deepEqual(runMetrics(run, PROPS), {
    rounds_completed: 2,
    delivery_ok_rate: 1,
    prompt_writes: 2,
    'prop:headline-rule': 0.5,
    'prop:reordered-only': 0,
    'prop:output-changed': 0.5,
  })
})

test('a failed delivery drags the ok rate down without hiding the round', () => {
  const run: RunOutcome = {
    arm: 'a',
    repetition: 1,
    rounds: [
      round(1, 'x', {}, { statuses: ['ok', 'failed'] }),
      round(2, 'x', {}, { statuses: ['ok', 'ok'] }),
    ],
  }
  const m = runMetrics(run, [])
  assert.equal(m.rounds_completed, 2)
  assert.equal(m.delivery_ok_rate, 0.75)
})

test('an empty run is zeroes, never NaN', () => {
  const m = runMetrics({ arm: 'a', repetition: 1, rounds: [] }, PROPS)
  for (const [key, value] of Object.entries(m)) {
    assert.equal(Number.isFinite(value), true, `${key} must be a number, got ${value}`)
    assert.equal(value, 0)
  }
})

test('a property absent from a round counts as not held, not as missing', () => {
  const run: RunOutcome = { arm: 'a', repetition: 1, rounds: [round(1, 'x', {})] }
  assert.equal(runMetrics(run, ['never-set'])['prop:never-set'], 0)
})

// ── Variance ────────────────────────────────────────────────────────────────

test('identical repetitions have zero spread — the mock determinism claim', () => {
  const s = summarise(demoOutcomes(), PROPS)
  for (const arm of s) {
    for (const [key, m] of Object.entries(arm.metrics)) {
      assert.equal(m.spread, 0, `${arm.arm}.${key} spread`)
      assert.equal(m.stdev, 0, `${arm.arm}.${key} stdev`)
      assert.equal(m.n, 2)
    }
  }
})

test('stat reports mean, range and population stdev', () => {
  assert.deepEqual(stat([1, 3]), { n: 2, mean: 2, min: 1, max: 3, spread: 2, stdev: 1 })
  assert.deepEqual(stat([2, 2, 2]), { n: 3, mean: 2, min: 2, max: 2, spread: 0, stdev: 0 })
  assert.deepEqual(stat([]), { n: 0, mean: 0, min: 0, max: 0, spread: 0, stdev: 0 })
})

test('a divergent repetition shows up as spread, and is not averaged away', () => {
  const runs = demoOutcomes().filter((r) => r.arm === 'actor-critic')
  // Repetition 2 fails to improve.
  runs[1] = {
    ...runs[1],
    rounds: [
      runs[1].rounds[0],
      round(2, BASELINE, { 'headline-rule': false, 'reordered-only': false, 'output-changed': false }, { writes: 1 }),
    ],
  }
  const [arm] = summarise(runs, PROPS)
  assert.equal(arm.metrics['prop:headline-rule'].mean, 0.25)
  assert.equal(arm.metrics['prop:headline-rule'].spread, 0.5)
  assert.equal(arm.metrics['prop:headline-rule'].min, 0)
  assert.equal(arm.metrics['prop:headline-rule'].max, 0.5)
})

test('summarise keeps arms in first-appearance order, whatever the ranking says', () => {
  assert.deepEqual(
    summarise(demoOutcomes(), PROPS).map((s) => s.arm),
    ['actor-critic', 'sham-critic', 'solo'],
  )
})

test('every metric key is present for every arm, so the table can never be ragged', () => {
  const keys = metricKeys(PROPS)
  for (const arm of summarise(demoOutcomes(), PROPS)) {
    assert.deepEqual(Object.keys(arm.metrics), keys)
  }
})

// ── Ranking ─────────────────────────────────────────────────────────────────

test('the diagnosing critic wins on the property, and the controls tie behind it', () => {
  const s = summarise(demoOutcomes(), PROPS)
  assert.deepEqual(rank(s, 'prop:headline-rule'), [
    { rank: 1, arm: 'actor-critic', mean: 0.5, spread: 0 },
    { rank: 2, arm: 'sham-critic', mean: 0, spread: 0 },
    { rank: 2, arm: 'solo', mean: 0, spread: 0 },
  ])
})

test('the sham matches the genuine critic on churn — C7, in one assertion', () => {
  const s = summarise(demoOutcomes(), PROPS)
  const writes = (arm: string) => s.find((x) => x.arm === arm)!.metrics.prompt_writes.mean
  assert.equal(writes('sham-critic'), writes('actor-critic'))
  assert.equal(writes('solo'), 0)
  // Same churn, different diagnosis: the whole point of the control arm.
  const changed = (arm: string) => s.find((x) => x.arm === arm)!.metrics['prop:output-changed'].mean
  assert.equal(changed('sham-critic'), changed('actor-critic'))
})

test('ties share a rank and keep spec order (competition ranking)', () => {
  const s = summarise(demoOutcomes(), PROPS)
  const ranked = rank(s, 'prompt_writes')
  assert.deepEqual(
    ranked.map((r) => [r.rank, r.arm]),
    [
      [1, 'actor-critic'],
      [1, 'sham-critic'],
      [3, 'solo'],
    ],
  )
})

test('ascending rank inverts the order', () => {
  const s = summarise(demoOutcomes(), PROPS)
  assert.deepEqual(rank(s, 'prompt_writes', 'asc')[0], { rank: 1, arm: 'solo', mean: 0, spread: 0 })
})

test('ranking by a metric nobody has is a flat tie, not a crash', () => {
  const s = summarise(demoOutcomes(), PROPS)
  assert.deepEqual(
    rank(s, 'prop:nonexistent').map((r) => r.rank),
    [1, 1, 1],
  )
})

// ── Rendering ───────────────────────────────────────────────────────────────

test('the table has one row per arm, in ranked order, with mean and spread', () => {
  const s = summarise(demoOutcomes(), PROPS)
  const table = renderTable(s, PROPS, rank(s, 'prop:headline-rule'))
  const lines = table.split('\n')
  assert.equal(lines.length, 5) // header, rule, three arms
  assert.match(lines[0], /^#\s+arm\s+reps\s+rounds_completed/)
  assert.match(lines[2], /^1\s+actor-critic\s+2\s+2 ±0\s+1 ±0\s+2 ±0\s+0\.5 ±0/)
  assert.match(lines[3], /^2\s+sham-critic/)
  assert.match(lines[4], /^2\s+solo/)
})

test('the table is byte-stable across two identical computations', () => {
  const a = renderTable(summarise(demoOutcomes(), PROPS), PROPS, rank(summarise(demoOutcomes(), PROPS), 'prompt_writes'))
  const b = renderTable(summarise(demoOutcomes(), PROPS), PROPS, rank(summarise(demoOutcomes(), PROPS), 'prompt_writes'))
  assert.equal(a, b)
})

// ── The artifact ────────────────────────────────────────────────────────────

const reportInput = () => ({
  task: {
    id: 'demo',
    description: 'demo',
    mockScript: 'e2e/mock-scripts/experiments-compare.json',
    rounds: 2,
    repetitions: 2,
    rankBy: 'prop:headline-rule',
    rankDirection: 'desc' as const,
  },
  arms: [
    { id: 'actor-critic', topology: 'actor-critic@v1', eventType: 'a.task', primaryWorker: 'a', answers: {} },
    { id: 'sham-critic', topology: 'sham-critic@v1', eventType: 'b.task', primaryWorker: 'b', answers: {} },
    { id: 'solo', topology: 'solo@v1', eventType: 'c.poke', primaryWorker: 'c', answers: {} },
  ],
  properties: PROPS.map((id) => ({ id, describe: id })),
  runs: demoOutcomes(),
})

test('the report carries no timestamp, project name, session id or event id', () => {
  const json = JSON.stringify(buildReport(reportInput()))
  for (const forbidden of ['startedAt', 'finishedAt', 'session_id', 'event_id', 'occurred_at', 'created_at']) {
    assert.equal(json.includes(forbidden), false, `report must not carry ${forbidden}`)
  }
  // No ISO-8601 timestamp anywhere in the bytes.
  assert.equal(/\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(json), false)
})

test('two reports built from the same outcomes are byte-identical', () => {
  assert.equal(JSON.stringify(buildReport(reportInput())), JSON.stringify(buildReport(reportInput())))
})

test('the report ranks, summarises and keeps the raw runs for re-analysis', () => {
  const report = buildReport(reportInput())
  assert.equal(report.ranking[0].arm, 'actor-critic')
  assert.equal(report.summaries.length, 3)
  assert.equal(report.runs.length, 6)
  assert.equal(report.table.split('\n').length, 5)
})

// ── Arithmetic hygiene ──────────────────────────────────────────────────────

test('round6 removes float noise that would otherwise break a byte diff', () => {
  assert.equal(round6(0.1 + 0.2), 0.3)
  assert.equal(round6(1 / 3), 0.333333)
})

test('a one-third rate is stable however the sum was ordered', () => {
  const a = runMetrics(
    { arm: 'a', repetition: 1, rounds: [round(1, 'x', { p: true }), round(2, 'x', { p: false }), round(3, 'x', { p: false })] },
    ['p'],
  )
  assert.equal(a['prop:p'], 0.333333)
})
