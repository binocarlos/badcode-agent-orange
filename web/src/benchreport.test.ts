// BR1 — the comparison rig's report.json, read.
//
// The fixture is a COPY of e2e/experiments/reports/actor-critic-vs-sham-vs-solo
// .report.json (that directory is owned elsewhere and never imported across).
// It is also the design's own worked example: sham and genuine critic tie at
// 2 ±0 churn and only the property predicates separate them — so the churn
// rules are tested against the exact report that motivates them.

import { describe, it, expect } from 'vitest'
import fixture from './__fixtures__/actor-critic-vs-sham-vs-solo.report.json'
import {
  CHURN_METRIC,
  describeRewrites,
  formatMeanSpread,
  formatNumber,
  parseBenchReport,
} from './benchreport.js'

describe('parseBenchReport', () => {
  const report = parseBenchReport(fixture)

  it('reads the task header', () => {
    expect(report.schema).toBe('agent-orange/experiments/compare-report@1')
    expect(report.task).toMatchObject({
      id: 'actor-critic-vs-sham-vs-solo',
      rounds: 2,
      repetitions: 2,
      rankBy: 'prop:headline-rule',
      rankDirection: 'desc',
    })
    expect(report.task.mockScript).toBe('e2e/mock-scripts/experiments-compare.json')
  })

  it('accepts raw JSON text as well as a parsed object', () => {
    expect(parseBenchReport(JSON.stringify(fixture))).toEqual(report)
  })

  it('joins ranking to arms and summaries, in ranked order', () => {
    expect(report.rows.map((r) => [r.rank, r.arm])).toEqual([
      [1, 'actor-critic'],
      [2, 'sham-critic'],
      [2, 'solo'],
    ])
    expect(report.rows[0]).toMatchObject({
      topology: 'actor-critic@v1',
      primaryWorker: 'xpa-scribe',
      reps: 2,
      mean: 0.5,
      spread: 0,
    })
  })

  it('leads with the ranked outcome column and never lists churn', () => {
    expect(report.metricColumns[0]).toBe('prop:headline-rule')
    expect(report.metricColumns).not.toContain(CHURN_METRIC)
    expect(report.metricColumns).toEqual([
      'prop:headline-rule',
      'prop:output-changed',
      'prop:prompt-intact',
      'prop:reordered-only',
      'delivery_ok_rate',
      'rounds_completed',
    ])
  })

  it('keeps churn in its own field, where a sort cannot reach it', () => {
    const byArm = Object.fromEntries(report.rows.map((r) => [r.arm, r]))
    expect(byArm['actor-critic'].churn).toMatchObject({ mean: 2, spread: 0 })
    // The design's point: the placebo ties the genuine critic on churn.
    expect(byArm['sham-critic'].churn).toMatchObject({ mean: 2, spread: 0 })
    expect(byArm['solo'].churn).toMatchObject({ mean: 0, spread: 0 })
    // …and only the outcome separates them.
    expect(byArm['actor-critic'].metrics['prop:headline-rule'].mean).toBe(0.5)
    expect(byArm['sham-critic'].metrics['prop:headline-rule'].mean).toBe(0)
  })

  it('counts rewrites and dedupes identical ones', () => {
    const byArm = Object.fromEntries(report.rows.map((r) => [r.arm, r]))
    // Four logged writes (2 reps × 2 rounds), one distinct: SetWorkerPrompt has
    // no no-op short-circuit, so the identical re-fire is logged again.
    expect(byArm['actor-critic'].rewrites).toBe(4)
    expect(byArm['actor-critic'].distinctRewrites).toBe(1)
    expect(byArm['solo'].rewrites).toBe(0)
    expect(byArm['solo'].distinctRewrites).toBe(0)
  })

  it('reports every spread as zero for a mock report', () => {
    expect(report.hasSpread).toBe(false)
  })

  it('flags a non-zero spread', () => {
    const noisy = JSON.parse(JSON.stringify(fixture)) as typeof fixture
    ;(noisy.summaries[0].metrics as Record<string, { spread: number }>)[
      'prop:headline-rule'
    ].spread = 0.25
    expect(parseBenchReport(noisy).hasSpread).toBe(true)
  })

  it('keeps the properties, the arms, the runs and the rig table', () => {
    expect(report.properties.map((p) => p.id)).toEqual([
      'headline-rule',
      'reordered-only',
      'output-changed',
      'prompt-intact',
    ])
    expect(report.arms).toHaveLength(3)
    expect(report.runs).toHaveLength(6)
    expect(report.runs[0].rounds[1].properties['headline-rule']).toBe(true)
    expect(report.table).toContain('actor-critic')
  })

  it('tolerates a report with no ranking or summaries', () => {
    const bare = parseBenchReport({ schema: 'agent-orange/experiments/compare-report@1' })
    expect(bare.rows).toEqual([])
    expect(bare.metricColumns).toEqual([])
    expect(bare.hasSpread).toBe(false)
  })

  const rejections: [string, unknown, RegExp][] = [
    ['not JSON', 'not json at all', /not JSON/],
    ['not an object', '[1,2,3]', /not a comparison report/],
    ['a foreign schema', { schema: 'something/else@3' }, /"something\/else@3"/],
    ['no schema at all', { arms: [] }, /found none/],
  ]
  for (const [name, input, message] of rejections) {
    it(`refuses ${name}`, () => {
      expect(() => parseBenchReport(input)).toThrowError(message)
    })
  }
})

describe('formatting', () => {
  it('never shows a mean without its spread', () => {
    expect(formatMeanSpread(0.5, 0)).toBe('0.5 ±0')
    expect(formatMeanSpread(2, 0.25)).toBe('2 ±0.25')
  })

  it('formats numbers shortly and stably', () => {
    expect(formatNumber(1)).toBe('1')
    expect(formatNumber(1 / 3)).toBe('0.333')
    expect(formatNumber(Number.NaN)).toBe('—')
  })

  it('phrases the rewrite dedupe', () => {
    const row = { rewrites: 4, distinctRewrites: 1 } as never
    expect(describeRewrites(row)).toBe('4 rewrites · 1 distinct')
    expect(describeRewrites({ rewrites: 1, distinctRewrites: 1 } as never)).toBe(
      '1 rewrite · 1 distinct',
    )
    expect(describeRewrites({ rewrites: 0, distinctRewrites: 0 } as never)).toBe('no rewrites')
  })
})
