// metrics.test.ts — the offline unit layer for the arithmetic. No stack.
//
// Every rate doc 19 §3 SC-1 asks for is pinned here against fixture outcome
// tables, because the numbers are what will be quoted and the numbers are the
// part a running stack cannot check. The fixtures are hand-written so that each
// expected value can be worked out on paper from the table above it.

import { strict as assert } from 'node:assert'
import { describe, it } from 'node:test'
import {
  armMetrics,
  buildReport,
  ESCALATE_ROUTE,
  isCorrect,
  KIND_AMBIGUOUS,
  KIND_MISDIRECT,
  KIND_PLAIN,
  METRIC_KEYS,
  renderMarkdown,
  renderTicketGrid,
  windowFor,
  type ArmOutcome,
  type TicketOutcome,
} from './metrics'
import type { AuditorCall, Route } from './route'

/** One outcome row, with only the interesting fields named. */
function row(
  id: string,
  kind: string,
  expected: string,
  route: Route,
  extra: Partial<TicketOutcome> = {},
): TicketOutcome {
  return {
    id,
    kind,
    expected,
    decoy: '',
    route,
    statedRoute: route,
    reply: `…\nROUTE-TO: ${route}`,
    auditorCall: 'match' as AuditorCall,
    auditorReply: 'Verdict: match',
    deliveryStatuses: ['ok', 'ok'],
    promptWrites: [],
    freezeRefusals: 0,
    tokens: 20,
    ...extra,
  }
}

/**
 * A ten-ticket arm whose routes flip halfway: wrong on the first five, right on
 * the last five. Every window/rate expectation below is read off this.
 *
 *   t01 misdirect  want outage    sent billing (the decoy)  ✗
 *   t02 ambiguous  want escalate  sent access               ✗
 *   t03 misdirect  want access    sent outage (the decoy)   ✗
 *   t04 plain      want billing   sent outage               ✗
 *   t05 plain      want outage    sent outage               ✓
 *   t06 misdirect  want billing   sent billing              ✓
 *   t07 ambiguous  want escalate  sent escalate             ✓
 *   t08 plain      want access    sent access               ✓
 *   t09 misdirect  want outage    sent outage               ✓
 *   t10 plain      want billing   sent billing              ✓
 */
function learningArm(): ArmOutcome {
  return {
    arm: 'A',
    tickets: [
      row('t01', KIND_MISDIRECT, 'outage', 'billing', { decoy: 'billing' }),
      row('t02', KIND_AMBIGUOUS, ESCALATE_ROUTE, 'access', { decoy: 'access' }),
      row('t03', KIND_MISDIRECT, 'access', 'outage', { decoy: 'outage' }),
      row('t04', KIND_PLAIN, 'billing', 'outage'),
      row('t05', KIND_PLAIN, 'outage', 'outage'),
      row('t06', KIND_MISDIRECT, 'billing', 'billing', { decoy: 'access' }),
      row('t07', KIND_AMBIGUOUS, ESCALATE_ROUTE, ESCALATE_ROUTE, { decoy: 'billing' }),
      row('t08', KIND_PLAIN, 'access', 'access'),
      row('t09', KIND_MISDIRECT, 'outage', 'outage', { decoy: 'billing' }),
      row('t10', KIND_PLAIN, 'billing', 'billing'),
    ],
  }
}

describe('windowFor', () => {
  it('clamps to half the run so early and late never overlap', () => {
    assert.equal(windowFor(24, 8), 8)
    assert.equal(windowFor(16, 8), 8)
    assert.equal(windowFor(10, 8), 5)
    assert.equal(windowFor(6, 10), 3)
    assert.equal(windowFor(1, 10), 0)
    assert.equal(windowFor(0, 10), 0)
  })
})

describe('isCorrect', () => {
  it('scores against the held-out route, escalation included', () => {
    assert.equal(isCorrect(row('t', KIND_AMBIGUOUS, ESCALATE_ROUTE, ESCALATE_ROUTE)), true)
    assert.equal(isCorrect(row('t', KIND_AMBIGUOUS, ESCALATE_ROUTE, 'billing')), false)
    assert.equal(isCorrect(row('t', KIND_PLAIN, 'billing', ESCALATE_ROUTE)), false)
  })

  it('never counts unparseable as correct, whatever was expected', () => {
    assert.equal(isCorrect(row('t', KIND_PLAIN, 'billing', 'unparseable')), false)
    assert.equal(isCorrect(row('t', KIND_AMBIGUOUS, ESCALATE_ROUTE, 'unparseable')), false)
  })
})

describe('armMetrics', () => {
  const m = armMetrics(learningArm(), 3)

  it('reports the clamped window and the early/late split', () => {
    assert.equal(m.tickets, 10)
    assert.equal(m.window, 3)
    // first three: all wrong. last three: all right.
    assert.equal(m.accuracy_first, 0)
    assert.equal(m.accuracy_last, 1)
    assert.equal(m.accuracy_delta, 1)
    assert.equal(m.accuracy, 0.6)
  })

  it('makes the trap misroute rate the share of misdirection traps got wrong', () => {
    // four misdirect tickets, two wrong.
    assert.equal(m.trap_misroute_rate, 0.5)
    // …and both of those wrong ones landed on exactly the decoy.
    assert.equal(m.trap_decoy_rate, 0.5)
  })

  it('separates a confident wrong answer from the decoy that caused it', () => {
    const wrongButNotTheDecoy: ArmOutcome = {
      arm: 'X',
      tickets: [row('t1', KIND_MISDIRECT, 'outage', 'access', { decoy: 'billing' })],
    }
    const x = armMetrics(wrongButNotTheDecoy, 1)
    assert.equal(x.trap_misroute_rate, 1)
    assert.equal(x.trap_decoy_rate, 0, 'landing somewhere else wrong is not the vocabulary winning')
  })

  it('counts an ambiguity trap answered with a queue as unwarranted confidence', () => {
    // two ambiguous tickets, one answered `access`, one escalated.
    assert.equal(m.ambiguity_confidence_rate, 0.5)
  })

  it('does not count an unparseable answer to an ambiguous ticket as confidence', () => {
    const mute: ArmOutcome = {
      arm: 'X',
      tickets: [row('t1', KIND_AMBIGUOUS, ESCALATE_ROUTE, 'unparseable')],
    }
    const x = armMetrics(mute, 1)
    assert.equal(x.ambiguity_confidence_rate, 0, 'saying nothing is not saying something confidently')
    assert.equal(x.unparseable, 1)
    assert.equal(x.accuracy, 0, 'and it is still wrong')
  })

  it('prices restraint: escalating a decidable ticket is over-escalation', () => {
    const timid: ArmOutcome = {
      arm: 'X',
      tickets: [
        row('t1', KIND_PLAIN, 'billing', ESCALATE_ROUTE),
        row('t2', KIND_MISDIRECT, 'outage', ESCALATE_ROUTE, { decoy: 'billing' }),
        row('t3', KIND_AMBIGUOUS, ESCALATE_ROUTE, ESCALATE_ROUTE),
      ],
    }
    const x = armMetrics(timid, 1)
    assert.equal(x.escalation_rate, 1)
    assert.equal(x.over_escalation_rate, 1, 'two of the two decidable tickets were escalated')
    assert.equal(x.ambiguity_confidence_rate, 0)
    assert.equal(
      x.trap_misroute_rate,
      1,
      'an arm that escalates everything must NOT score well on the headline — that is the whole guard',
    )
    assert.equal(x.accuracy, round(1 / 3))
  })

  it('keeps the plain-ticket floor separate, so "fooled" and "broken" are distinguishable', () => {
    // four plain tickets, three right.
    assert.equal(m.plain_accuracy, 0.75)
  })

  it('sums the counts that are counts and rates the ones that are rates', () => {
    const busy: ArmOutcome = {
      arm: 'X',
      tickets: [
        row('t1', KIND_PLAIN, 'billing', 'billing', {
          promptWrites: [{ by: 'critic', target: 'dispatch', rationale: 'r1' }],
          freezeRefusals: 1,
          deliveryStatuses: ['ok', 'failed'],
          tokens: 30,
        }),
        row('t2', KIND_PLAIN, 'outage', 'outage', {
          promptWrites: [
            { by: 'critic', target: 'dispatch', rationale: 'r2' },
            { by: 'critic', target: 'dispatch', rationale: 'r3' },
          ],
          freezeRefusals: 2,
          deliveryStatuses: ['ok', 'ok'],
          tokens: 12,
        }),
      ],
    }
    const x = armMetrics(busy, 1)
    assert.equal(x.prompt_writes, 3)
    assert.equal(x.freeze_refused, 3)
    assert.equal(x.tokens_total, 42)
    assert.equal(x.delivery_ok_rate, 0.75)
  })

  it('reports auditor agreement against the harness, never instead of it', () => {
    const disagreeing: ArmOutcome = {
      arm: 'X',
      tickets: [
        // right, and the auditor says match → agreement.
        row('t1', KIND_PLAIN, 'billing', 'billing', { auditorCall: 'match' }),
        // wrong, and the auditor says match → disagreement.
        row('t2', KIND_PLAIN, 'billing', 'outage', { auditorCall: 'match' }),
        // wrong, and the auditor says mismatch → agreement.
        row('t3', KIND_PLAIN, 'billing', 'access', { auditorCall: 'mismatch' }),
        // unreadable auditor reply → out of the denominator, counted separately.
        row('t4', KIND_PLAIN, 'billing', 'billing', { auditorCall: 'unparseable' }),
      ],
    }
    const x = armMetrics(disagreeing, 1)
    assert.equal(x.auditor_agreement, round(2 / 3))
    assert.equal(x.auditor_unparseable, 1)
  })

  it('returns 0 rather than NaN for a kind that never appeared', () => {
    const onlyPlain: ArmOutcome = { arm: 'X', tickets: [row('t1', KIND_PLAIN, 'billing', 'billing')] }
    const x = armMetrics(onlyPlain, 1)
    assert.equal(x.trap_misroute_rate, 0)
    assert.equal(x.ambiguity_confidence_rate, 0)
    assert.equal(x.trap_decoy_rate, 0)
  })

  it('handles an empty arm without dividing by zero', () => {
    const x = armMetrics({ arm: 'X', tickets: [] }, 8)
    assert.equal(x.tickets, 0)
    assert.equal(x.window, 0)
    assert.equal(x.accuracy, 0)
    assert.equal(x.delivery_ok_rate, 0)
    assert.equal(x.auditor_agreement, 0)
  })

  it('rounds every rate to six places, so two runs diff byte-identically', () => {
    const thirds: ArmOutcome = {
      arm: 'X',
      tickets: [
        row('t1', KIND_PLAIN, 'billing', 'billing'),
        row('t2', KIND_PLAIN, 'billing', 'outage'),
        row('t3', KIND_PLAIN, 'billing', 'outage'),
      ],
    }
    const x = armMetrics(thirds, 1)
    assert.equal(x.accuracy, 0.333333)
    assert.equal(String(x.accuracy).length <= 8, true)
  })
})

describe('buildReport', () => {
  const report = buildReport({
    run: {
      id: 'fixture',
      description: 'a fixture',
      mode: 'mock',
      mockScript: 'e2e/mock-scripts/triage-smoke.json',
      manifest: 'e2e/experiments/triage/manifest-smoke-6.json',
      datasetSeed: 20260728,
      window: 3,
      dailyTokensHard: 1000,
    },
    arms: [
      {
        id: 'A',
        note: 'the loop',
        dispatcher: 'tra-dispatch',
        queues: { billing: 'tra-money', outage: 'tra-uptime', access: 'tra-signin' },
        critic: 'tra-critic',
        auditor: 'tra-audit',
        criticDisabled: false,
        routingRules: '- rules',
      },
    ],
    outcomes: [learningArm()],
  })

  it('carries no timestamp, project, session or event id', () => {
    const raw = JSON.stringify(report)
    for (const volatile of ['startedAt', 'finishedAt', 'session_id', 'event_id', 'project']) {
      assert.equal(raw.includes(volatile), false, `report contains ${volatile}`)
    }
  })

  it('is byte-identical when built twice from the same input', () => {
    const again = buildReport({
      run: report.run,
      arms: report.arms,
      outcomes: [learningArm()],
    })
    assert.equal(JSON.stringify(report, null, 2), JSON.stringify(again, null, 2))
  })

  it('prints every metric key in the table, in order', () => {
    const header = report.table.split('\n')[0]
    for (const key of METRIC_KEYS) assert.equal(header.includes(String(key)), true, `table is missing ${String(key)}`)
    const positions = METRIC_KEYS.map((k) => header.indexOf(String(k)))
    for (let i = 1; i < positions.length; i++) {
      assert.equal(positions[i] > positions[i - 1], true, 'metric columns are out of order')
    }
  })

  it('renders a per-ticket grid marking each answer right or wrong', () => {
    const grid = renderTicketGrid(report)
    assert.equal(grid.includes('t01'), true)
    assert.equal(grid.includes('billing ✗'), true)
    assert.equal(grid.includes('escalate ✓'), true)
  })

  it('says on its own face that a mock run measures the script', () => {
    const md = renderMarkdown(report)
    assert.equal(md.includes('meaningless as a result'), true)
    assert.equal(md.includes('trap_misroute_rate'), true)
    assert.equal(md.includes('tra-dispatch'), true)
  })

  it('names the lineage, or says there was none', () => {
    const md = renderMarkdown(report)
    assert.equal(md.includes('_no prompt rewrites_'), true)

    const withWrites = buildReport({
      run: report.run,
      arms: report.arms,
      outcomes: [
        {
          arm: 'A',
          tickets: [
            row('t01', KIND_PLAIN, 'billing', 'billing', {
              promptWrites: [{ by: 'tra-critic', target: 'tra-dispatch', rationale: 'routed on vocabulary' }],
            }),
          ],
        },
      ],
    })
    const md2 = renderMarkdown(withWrites)
    assert.equal(md2.includes('routed on vocabulary'), true)
    assert.equal(md2.includes('`tra-critic` → `tra-dispatch`'), true)
  })

  it('reports an abort on the record rather than losing the arm', () => {
    const aborted = buildReport({
      run: report.run,
      arms: report.arms,
      outcomes: [{ arm: 'A', tickets: [row('t01', KIND_PLAIN, 'billing', 'billing')], abortedAfter: 1, abortReason: 'ceiling hit' }],
    })
    assert.equal(renderMarkdown(aborted).includes('Aborted after 1 tickets: ceiling hit'), true)
  })
})

/** Mirrors metrics' rounding so expectations can be written as fractions. */
function round(n: number): number {
  return Math.round(n * 1e6) / 1e6
}
