import assert from 'node:assert/strict'
import test from 'node:test'

import {
  anchorSubjectId,
  buildBatches,
  buildGraderPrompt,
  labelAt,
  parseRankingResponse,
  resolveRanking,
  runGrading,
  scriptedGrader,
} from './grade.ts'
import type { GradeConfig, GradingBatch } from './grade.ts'
import {
  FIXTURE_ANCHORS,
  SCRIPTED_RULES,
  fixtureProvenanceStrings,
  fixtureSet,
} from './fixtures.ts'

function config(overrides: Partial<GradeConfig> = {}): GradeConfig {
  return {
    anchors: FIXTURE_ANCHORS,
    grader: scriptedGrader({ rules: SCRIPTED_RULES }),
    seed: 'seed-alpha',
    batchSize: 4,
    ...overrides,
  }
}

test('labels are positional and carry nothing else', () => {
  assert.equal(labelAt(0), 'A')
  assert.equal(labelAt(25), 'Z')
  assert.equal(labelAt(26), 'AA')
  assert.equal(labelAt(27), 'AB')
})

test('batch membership is deterministic and interleaves rounds', () => {
  const set = fixtureSet()
  const batches = buildBatches(set, config())
  assert.equal(batches.length, 2)
  const members = batches.map((b) =>
    Object.values(b.key).filter((id) => !id.startsWith('anchor:')).sort(),
  )
  assert.deepEqual(members, [
    ['cand-1', 'cand-3', 'cand-5'],
    ['cand-2', 'cand-4', 'cand-6'],
  ])
  // One candidate from each round in each batch — round-robin partition.
  for (const batchMembers of members) {
    const rounds = batchMembers.map(
      (id) => set.candidates.find((c) => c.id === id)!.meta.round,
    )
    assert.deepEqual(rounds.slice().sort(), [0, 1, 2])
  }
})

test('anchors appear in every batch', () => {
  const batches = buildBatches(fixtureSet(), config())
  for (const prepared of batches) {
    const subjects = new Set(Object.values(prepared.key))
    for (const anchor of FIXTURE_ANCHORS) {
      assert.ok(
        subjects.has(anchorSubjectId(anchor.id)),
        `batch ${prepared.batch.batchId} is missing anchor ${anchor.id}`,
      )
      assert.ok(
        prepared.batch.items.some((i) => i.text === anchor.text),
        `batch ${prepared.batch.batchId} does not present anchor ${anchor.id}`,
      )
    }
    assert.equal(prepared.batch.items.length, 5)
  }
})

test('provenance never reaches the grader view', () => {
  const set = fixtureSet()
  const batches = buildBatches(set, config())
  const leaky = fixtureProvenanceStrings(set)
  for (const prepared of batches) {
    // Serialise exactly what the seam receives.
    const payload = JSON.stringify(prepared.batch)
    for (const needle of leaky) {
      assert.ok(!payload.includes(needle), `batch payload leaked ${JSON.stringify(needle)}`)
    }
    assert.deepEqual(Object.keys(prepared.batch).sort(), ['batchId', 'criterion', 'items', 'task'])
    for (const item of prepared.batch.items) {
      assert.deepEqual(Object.keys(item).sort(), ['label', 'text'])
    }
  }
})

test('the anthropic grader prompt carries no provenance either', () => {
  const set = fixtureSet()
  const batches = buildBatches(set, config())
  const leaky = fixtureProvenanceStrings(set)
  for (const prepared of batches) {
    const prompt = buildGraderPrompt(prepared.batch)
    for (const needle of leaky) {
      assert.ok(!prompt.includes(needle), `grader prompt leaked ${JSON.stringify(needle)}`)
    }
  }
})

test('presentation order is seeded-deterministic and moves with the seed', () => {
  const set = fixtureSet()
  const a = buildBatches(set, config({ seed: 'seed-alpha' }))
  const aAgain = buildBatches(set, config({ seed: 'seed-alpha' }))
  const b = buildBatches(set, config({ seed: 'seed-beta' }))

  assert.deepEqual(a, aAgain, 'same seed produced a different presentation')
  assert.notDeepEqual(
    a.map((p) => p.batch.items.map((i) => i.text)),
    b.map((p) => p.batch.items.map((i) => i.text)),
    'different seeds produced the same presented order',
  )
  // The label -> subject key must move with the presentation, not drift from it.
  for (const prepared of [...a, ...b]) {
    prepared.batch.items.forEach((item, idx) => {
      assert.equal(item.label, labelAt(idx))
      assert.ok(prepared.key[item.label] !== undefined)
    })
  }
})

test('buildBatches refuses fewer than two anchors and colliding ids', () => {
  const set = fixtureSet()
  assert.throws(
    () => buildBatches(set, config({ anchors: [FIXTURE_ANCHORS[0]!] })),
    /at least two anchors/,
  )
  assert.throws(
    () => buildBatches(set, config({ anchors: [{ id: 'cand-1', text: 'x' }, FIXTURE_ANCHORS[0]!] })),
    /collides with an anchor id/,
  )
})

test('resolveRanking rejects rankings that are not strict permutations', () => {
  const prepared = buildBatches(fixtureSet(), config())[0]!
  const labels = prepared.batch.items.map((i) => i.label)
  const id = prepared.batch.batchId
  assert.throws(() => resolveRanking(prepared, { batchId: 'nope', order: labels }), /expected/)
  assert.throws(() => resolveRanking(prepared, { batchId: id, order: labels.slice(1) }), /omits/)
  assert.throws(
    () => resolveRanking(prepared, { batchId: id, order: [...labels.slice(1), labels[1]!] }),
    /duplicate label/,
  )
  assert.throws(
    () => resolveRanking(prepared, { batchId: id, order: [...labels.slice(1), 'ZZ'] }),
    /unknown label/,
  )
  const resolved = resolveRanking(prepared, { batchId: id, order: labels })
  assert.deepEqual(resolved.order, labels.map((l) => prepared.key[l]))
})

test('scriptedGrader ranks by the rule table, ties by presented order', () => {
  const grader = scriptedGrader({ rules: SCRIPTED_RULES })
  const batch: GradingBatch = {
    batchId: 'b1',
    task: 't',
    criterion: 'c',
    items: [
      { label: 'A', text: 'nothing' },
      { label: 'B', text: 'Title: x Signed: y Units: z' },
      { label: 'C', text: 'Title: x' },
      { label: 'D', text: 'nothing at all' },
    ],
  }
  assert.deepEqual(grader(batch), { batchId: 'b1', order: ['B', 'C', 'A', 'D'] })
})

test('a purely order-biased scriptedGrader echoes the presented order', () => {
  const grader = scriptedGrader({ positionBias: 1 })
  const batch: GradingBatch = {
    batchId: 'b1',
    task: 't',
    criterion: 'c',
    items: [
      { label: 'A', text: 'nothing' },
      { label: 'B', text: 'Title: x Signed: y Units: z' },
      { label: 'C', text: 'Title: x' },
    ],
  }
  assert.deepEqual(grader(batch), { batchId: 'b1', order: ['A', 'B', 'C'] })
})

test('runGrading resolves labels back to subject ids', async () => {
  const set = fixtureSet()
  const { batches, rankings } = await runGrading(set, config())
  assert.equal(rankings.length, batches.length)
  for (const [i, ranking] of rankings.entries()) {
    const prepared = batches[i]!
    assert.equal(ranking.batchId, prepared.batch.batchId)
    assert.deepEqual(ranking.order.slice().sort(), Object.values(prepared.key).slice().sort())
  }
})

test('runGrading accepts an async grader', async () => {
  const inner = scriptedGrader({ rules: SCRIPTED_RULES })
  const asyncGrader = async (batch: GradingBatch) => inner(batch)
  const sync = await runGrading(fixtureSet(), config())
  const async_ = await runGrading(fixtureSet(), config({ grader: asyncGrader }))
  assert.deepEqual(async_.rankings, sync.rankings)
})

// ── parseRankingResponse: pure, offline; the network path is never called ───

test('parseRankingResponse accepts a bare JSON array', () => {
  assert.deepEqual(parseRankingResponse('["B","A","C"]', ['A', 'B', 'C']), ['B', 'A', 'C'])
})

test('parseRankingResponse survives fences, prose and lowercase labels', () => {
  const raw = 'Here is my ranking:\n```json\n["b", "c", "a"]\n```\nHope that helps.'
  assert.deepEqual(parseRankingResponse(raw, ['A', 'B', 'C']), ['B', 'C', 'A'])
})

test('parseRankingResponse falls back to mention order when there is no array', () => {
  assert.deepEqual(
    parseRankingResponse('C is best, then A, and finally B.', ['A', 'B', 'C']),
    ['C', 'A', 'B'],
  )
})

test('parseRankingResponse throws rather than inventing a partial ranking', () => {
  assert.throws(() => parseRankingResponse('["A","B"]', ['A', 'B', 'C']), /not a strict ranking/)
  assert.throws(() => parseRankingResponse('no idea', ['A', 'B', 'C']), /not a strict ranking/)
  assert.throws(() => parseRankingResponse('["A","Q"]', ['A', 'B']), /not a strict ranking/)
})

test('parseRankingResponse drops a repeated label when the rest is unambiguous', () => {
  // ["A","A","B"] still states one order over {A, B}; repairing it fabricates
  // nothing. An omission or an invention would, hence the throws above.
  assert.deepEqual(parseRankingResponse('["A","A","B"]', ['A', 'B']), ['A', 'B'])
})

test('parseRankingResponse handles multi-letter labels without partial matches', () => {
  const labels = ['A', 'AA', 'AB']
  assert.deepEqual(parseRankingResponse('["AB","A","AA"]', labels), ['AB', 'A', 'AA'])
})
