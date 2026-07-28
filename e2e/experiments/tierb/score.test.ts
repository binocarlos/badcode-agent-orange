import assert from 'node:assert/strict'
import test from 'node:test'

import type { CandidateSet } from './collect.ts'
import type { GradeConfig, GraderSeam } from './grade.ts'
import { scriptedGrader } from './grade.ts'
import { FIXTURE_ANCHORS, SCRIPTED_RULES, fixtureSet } from './fixtures.ts'
import type { CurveReport } from './score.ts'
import { formatCurve } from './score.ts'
import { graderFromSpec, runTierB } from './run.ts'

/** The honest grader: it prefers later rounds, but only via their text. */
const honestGrader = (): GraderSeam => scriptedGrader({ rules: SCRIPTED_RULES })

/** The rigged grader: ranks purely by where the shuffle put things. */
const orderBiasedGrader = (): GraderSeam => scriptedGrader({ positionBias: 1 })

function conf(grader: GraderSeam, seed: string): GradeConfig {
  return { anchors: FIXTURE_ANCHORS, grader, seed, batchSize: 4 }
}

async function curve(set: CandidateSet, grader: GraderSeam, seed: string): Promise<CurveReport> {
  return runTierB(set, conf(grader, seed))
}

test('scriptedGrader end to end produces the expected rising curve', async () => {
  const report = await curve(fixtureSet(), honestGrader(), 'seed-alpha')

  assert.deepEqual(
    report.rounds.map((r) => [r.round, r.n, r.score, r.spread, r.comparisons]),
    [
      [0, 2, 0, 0, 4],
      [1, 2, 0.5, 0, 4],
      [2, 2, 1, 0, 4],
    ],
  )
  // Each candidate meets both anchors exactly once.
  for (const c of report.candidates) {
    assert.equal(c.comparisons, 2, `${c.id} did not meet both anchors`)
  }
  // Elo is a monotone restatement of score: rising too, anchors bracketing it.
  assert.ok(report.rounds[0]!.elo < report.rounds[1]!.elo)
  assert.ok(report.rounds[1]!.elo < report.rounds[2]!.elo)
  assert.equal(report.rounds[1]!.elo, 0, 'a 0.5 win rate should sit on the anchor scale')
})

test('anchors are the scale: fixed values, independent of the candidates', async () => {
  const report = await curve(fixtureSet(), honestGrader(), 'seed-alpha')
  assert.deepEqual(
    report.anchors.map((a) => [a.id, a.score, a.wins, a.comparisons]),
    [
      ['anchor-plain', 0, 0, 2],
      ['anchor-solid', 1, 2, 2],
    ],
  )

  // Drop half the candidates: the anchors' SCORES must not move.
  const half = fixtureSet()
  half.candidates = half.candidates.slice(0, 3)
  const smaller = await curve(half, honestGrader(), 'seed-alpha')
  assert.deepEqual(
    smaller.anchors.map((a) => [a.id, a.score]),
    report.anchors.map((a) => [a.id, a.score]),
    'anchor scale moved with the candidate set',
  )

  // ...but the counts do, and so therefore does elo. Three candidates fit in
  // one batch instead of two, so each anchor is met once rather than twice,
  // and the Laplace correction shrinks harder at n=1. This is why `score` —
  // not `elo` — is the cross-run scale check: elo is only comparable between
  // runs with the same number of comparisons per subject.
  assert.deepEqual(
    smaller.anchors.map((a) => a.comparisons),
    [1, 1],
  )
  assert.notDeepEqual(
    smaller.anchors.map((a) => a.elo),
    report.anchors.map((a) => a.elo),
  )
})

test('INVARIANCE: an honest grader gives identical scores under two seeds', async () => {
  const set = fixtureSet()
  const a = await curve(set, honestGrader(), 'seed-alpha')
  const b = await curve(set, honestGrader(), 'seed-beta')

  // Same fixture, demonstrably different presented orders...
  const orderOf = async (seed: string) => {
    const { buildBatches } = await import('./grade.ts')
    return buildBatches(set, conf(honestGrader(), seed)).map((p) =>
      p.batch.items.map((i) => i.text),
    )
  }
  assert.notDeepEqual(await orderOf('seed-alpha'), await orderOf('seed-beta'))

  // ...and identical anchors, rounds and per-candidate scores.
  assert.deepEqual(b.anchors, a.anchors)
  assert.deepEqual(b.rounds, a.rounds)
  assert.deepEqual(
    b.candidates.map((c) => [c.id, c.score]),
    a.candidates.map((c) => [c.id, c.score]),
  )
})

test('DETECTION: an order-biased grader is exposed by the shuffle', async () => {
  const set = fixtureSet()
  const a = await curve(set, orderBiasedGrader(), 'seed-alpha')
  const b = await curve(set, orderBiasedGrader(), 'seed-beta')

  // The same rigged grader on the same fixture disagrees with itself as soon
  // as the presentation is reshuffled. That divergence IS the detection:
  // §7.1's shuffle turns a hidden position preference into a visible one.
  assert.notDeepEqual(
    b.candidates.map((c) => [c.id, c.score]),
    a.candidates.map((c) => [c.id, c.score]),
    'order bias survived the shuffle undetected',
  )
  // And it does not reproduce the honest curve either.
  const honest = await curve(set, honestGrader(), 'seed-alpha')
  assert.notDeepEqual(a.rounds, honest.rounds)
})

test('report bytes are stable for a fixed seed (no timestamps, no run state)', async () => {
  const set = fixtureSet()
  const first = JSON.stringify(await curve(set, honestGrader(), 'seed-alpha'))
  const second = JSON.stringify(await curve(set, honestGrader(), 'seed-alpha'))
  assert.equal(first, second)
  assert.ok(!first.includes('Date'), 'report carries a timestamp')
})

test('the report records what a rerun needs and nothing that leaks a verdict', async () => {
  const report = await curve(fixtureSet(), honestGrader(), 'seed-alpha')
  assert.equal(report.version, 1)
  assert.equal(report.seed, 'seed-alpha')
  assert.equal(report.batchCount, 2)
  assert.deepEqual(report.anchorIds, ['anchor-plain', 'anchor-solid'])
  // No pass/fail anywhere: Tier B is a curve, never a gate.
  const keys = JSON.stringify(report)
  for (const forbidden of ['"pass"', '"fail"', '"verdict"', '"ok"']) {
    assert.ok(!keys.includes(forbidden), `report carries a verdict field ${forbidden}`)
  }
})

test('formatCurve renders a table that states it is not a gate', async () => {
  const text = formatCurve(await curve(fixtureSet(), honestGrader(), 'seed-alpha'))
  assert.match(text, /Tier B curve — seed seed-alpha/)
  assert.match(text, /anchors \(the scale/)
  assert.match(text, /Not a gate/)
  assert.equal(text.split('\n').filter((l) => /^\d/.test(l)).length, 3)
})

test('graderFromSpec builds the scripted grader and rejects unknown types', async () => {
  const grader = graderFromSpec({ type: 'scripted', rules: SCRIPTED_RULES })
  const report = await curve(fixtureSet(), grader, 'seed-alpha')
  assert.deepEqual(
    report.rounds.map((r) => r.score),
    [0, 0.5, 1],
  )
  assert.throws(
    () => graderFromSpec({ type: 'nonsense' } as never),
    /unknown grader type/,
  )
})
