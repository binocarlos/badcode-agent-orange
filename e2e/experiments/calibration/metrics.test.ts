// metrics.test.ts — the offline unit layer for the arithmetic. No stack.
//
// Every rate the runbook §3 asks for is pinned here against fixture outcome
// tables, because the numbers are what will be quoted and the numbers are the
// part a running stack cannot check. The fixtures are hand-written so that each
// expected value can be worked out on paper from the table above it.

import { strict as assert } from 'node:assert'
import { describe, it } from 'node:test'
import {
  armMetrics,
  buildReport,
  isCorrect,
  KIND_CONFOUND,
  KIND_NULL,
  KIND_REAL,
  KIND_UNDERPOWERED,
  renderMarkdown,
  windowFor,
  type ArmOutcome,
  type HypothesisOutcome,
} from './metrics'
import type { CheckerCall, Verdict } from './verdict'

/** One outcome row, with only the interesting fields named. */
function row(
  id: string,
  kind: string,
  expected: 'effect' | 'no-effect',
  verdict: Verdict,
  extra: Partial<HypothesisOutcome> = {},
): HypothesisOutcome {
  return {
    id,
    kind,
    expected,
    truthEffect: expected === 'effect',
    verdict,
    conclusion: `…\nVERDICT: ${verdict}`,
    checkerCall: 'match' as CheckerCall,
    checkerReply: 'Verdict: match',
    deliveryStatuses: ['ok', 'ok'],
    promptWrites: [],
    freezeRefusals: 0,
    tokens: 20,
    ...extra,
  }
}

/**
 * A ten-hypothesis arm whose verdicts flip halfway: wrong on the first five,
 * right on the last five. Every window/rate expectation below is read off this.
 */
function learningArm(): ArmOutcome {
  return {
    arm: 'A',
    hypotheses: [
      row('h01', KIND_NULL, 'no-effect', 'effect'),
      row('h02', KIND_CONFOUND, 'no-effect', 'effect'),
      row('h03', KIND_NULL, 'no-effect', 'effect'),
      row('h04', KIND_UNDERPOWERED, 'no-effect', 'effect'),
      row('h05', KIND_REAL, 'effect', 'effect'),
      row('h06', KIND_NULL, 'no-effect', 'no-effect'),
      row('h07', KIND_CONFOUND, 'no-effect', 'no-effect'),
      row('h08', KIND_REAL, 'effect', 'effect'),
      row('h09', KIND_UNDERPOWERED, 'no-effect', 'no-effect'),
      row('h10', KIND_REAL, 'effect', 'effect'),
    ],
  }
}

describe('windowFor', () => {
  it('clamps to half the run so early and late never overlap', () => {
    assert.equal(windowFor(30, 10), 10)
    assert.equal(windowFor(20, 10), 10)
    assert.equal(windowFor(10, 10), 5)
    assert.equal(windowFor(4, 10), 2)
    assert.equal(windowFor(1, 10), 0)
    assert.equal(windowFor(0, 10), 0)
  })
})

describe('isCorrect', () => {
  it('scores against the honest answer, not the generating truth', () => {
    // The underpowered case: the effect is REAL (truthEffect) and the honest
    // report is still a null. Scoring against truthEffect would reward the
    // overclaim and punish the restraint.
    const restrained = row('h1', KIND_UNDERPOWERED, 'no-effect', 'no-effect', { truthEffect: true })
    const overclaim = row('h2', KIND_UNDERPOWERED, 'no-effect', 'effect', { truthEffect: true })
    assert.equal(isCorrect(restrained), true)
    assert.equal(isCorrect(overclaim), false)
  })

  it('never counts an unparseable verdict as correct', () => {
    assert.equal(isCorrect(row('h1', KIND_NULL, 'no-effect', 'unparseable')), false)
    assert.equal(isCorrect(row('h2', KIND_REAL, 'effect', 'unparseable')), false)
  })
})

describe('armMetrics', () => {
  it('computes every runbook §3 number off a hand-checkable table', () => {
    const m = armMetrics(learningArm(), 10)
    assert.equal(m.hypotheses, 10)
    assert.equal(m.window, 5, '10 hypotheses clamp a window of 10 to 5')
    // Correct: h05, h06, h07, h08, h09, h10 = 6 of 10.
    assert.equal(m.accuracy, 0.6)
    // First five: only h05. Last five: all.
    assert.equal(m.accuracy_first, 0.2)
    assert.equal(m.accuracy_last, 1)
    assert.equal(m.accuracy_delta, 0.8)
    // Planted nulls h01, h03, h06: two confirmed.
    assert.equal(m.planted_null_false_confirm_rate, round(2 / 3))
    // Confound traps h02, h07: one escaped, one fooled.
    assert.equal(m.confound_escaped_rate, 0.5)
    assert.equal(m.confound_fooled_rate, 0.5)
    // Underpowered h04, h09: one overclaim.
    assert.equal(m.underpowered_overclaim_rate, 0.5)
    // Real effects h05, h08, h10: all found.
    assert.equal(m.real_effect_detection_rate, 1)
    assert.equal(m.unparseable, 0)
    assert.equal(m.tokens_total, 200)
    assert.equal(m.delivery_ok_rate, 1)
  })

  it('keeps unparseable verdicts in the denominator', () => {
    // An org that stops answering must not score 100%.
    const silent: ArmOutcome = {
      arm: 'silent',
      hypotheses: [
        row('h1', KIND_REAL, 'effect', 'effect'),
        row('h2', KIND_NULL, 'no-effect', 'unparseable'),
        row('h3', KIND_NULL, 'no-effect', 'unparseable'),
        row('h4', KIND_REAL, 'effect', 'effect'),
      ],
    }
    const m = armMetrics(silent, 10)
    assert.equal(m.hypotheses, 4)
    assert.equal(m.accuracy, 0.5)
    assert.equal(m.unparseable, 2)
    // An unparseable verdict is not a confirmation either — it inflates
    // neither the false-confirm rate nor the escape rate.
    assert.equal(m.planted_null_false_confirm_rate, 0)
  })

  it('counts churn and refusals from the per-hypothesis records', () => {
    const busy: ArmOutcome = {
      arm: 'busy',
      hypotheses: [
        row('h1', KIND_REAL, 'effect', 'effect', {
          promptWrites: [{ by: 'critic', target: 'invest', rationale: 'no method stated' }],
          freezeRefusals: 1,
        }),
        row('h2', KIND_NULL, 'no-effect', 'no-effect', {
          promptWrites: [
            { by: 'critic', target: 'invest', rationale: 'still naive' },
            { by: 'critic', target: 'invest', rationale: 'again' },
          ],
          freezeRefusals: 2,
        }),
      ],
    }
    const m = armMetrics(busy, 10)
    assert.equal(m.prompt_writes, 3)
    assert.equal(m.freeze_refused, 3)
  })

  it('reports checker agreement without letting the checker be the score', () => {
    const arm: ArmOutcome = {
      arm: 'checked',
      hypotheses: [
        // right, and the checker agrees
        row('h1', KIND_REAL, 'effect', 'effect', { checkerCall: 'match' }),
        // wrong, and the checker says so
        row('h2', KIND_NULL, 'no-effect', 'effect', { checkerCall: 'mismatch' }),
        // wrong, but the checker called it a match — a disagreeing scoreboard
        row('h3', KIND_NULL, 'no-effect', 'effect', { checkerCall: 'match' }),
        // right, checker unreadable — excluded from the agreement denominator
        row('h4', KIND_REAL, 'effect', 'effect', { checkerCall: 'unparseable' }),
      ],
    }
    const m = armMetrics(arm, 10)
    assert.equal(m.accuracy, 0.5, 'accuracy comes from the harness, never from the checker')
    assert.equal(m.checker_agreement, round(2 / 3))
    assert.equal(m.checker_unparseable, 1)
  })

  it('does not divide by zero on an empty or single-kind arm', () => {
    const m = armMetrics({ arm: 'empty', hypotheses: [] }, 10)
    assert.equal(m.accuracy, 0)
    assert.equal(m.planted_null_false_confirm_rate, 0)
    assert.equal(m.delivery_ok_rate, 0)
    assert.equal(m.checker_agreement, 0)
    assert.equal(m.window, 0)
  })

  it('counts a failed delivery against the ok rate', () => {
    const m = armMetrics(
      {
        arm: 'flaky',
        hypotheses: [
          row('h1', KIND_REAL, 'effect', 'effect', { deliveryStatuses: ['failed', 'ok'] }),
          row('h2', KIND_REAL, 'effect', 'effect'),
        ],
      },
      10,
    )
    assert.equal(m.delivery_ok_rate, 0.75)
  })
})

describe('buildReport', () => {
  const input = {
    run: {
      id: 'fixture',
      description: 'a fixture',
      mode: 'mock' as const,
      mockScript: 'e2e/mock-scripts/calibration-smoke.json',
      manifest: 'e2e/experiments/calibration/manifest-smoke-4.json',
      datasetSeed: 20260728,
      window: 10,
      dailyTokensHard: 5_000_000,
      covariatesHint: 'age_group',
    },
    arms: [
      { id: 'A', note: 'critic live', investigator: 'a-i', critic: 'a-c', checker: 'a-j', criticDisabled: false, criticShammed: false },
      { id: 'B', note: 'critic off', investigator: 'b-i', critic: 'b-c', checker: 'b-j', criticDisabled: true, criticShammed: false },
    ],
    outcomes: [learningArm(), { ...learningArm(), arm: 'B' }],
  }

  it('carries no timestamp, project name, session id or event id', () => {
    const encoded = JSON.stringify(buildReport(input))
    for (const volatile of ['startedAt', 'finishedAt', 'session_id', 'sessionId', 'project', 'event_id']) {
      assert.ok(!encoded.includes(volatile), `the report carries ${volatile} and cannot be diffed for determinism`)
    }
    assert.ok(!/\d{4}-\d{2}-\d{2}T\d{2}:/.test(encoded), 'the report carries an ISO timestamp')
  })

  it('builds a metric row per arm, in config order', () => {
    const report = buildReport(input)
    assert.deepEqual(report.metrics.map((m) => m.arm), ['A', 'B'])
    assert.ok(report.table.includes('accuracy_delta'))
  })

  it('states the mock caveat on the markdown\'s own face', () => {
    const md = renderMarkdown(buildReport(input))
    assert.ok(md.includes('meaningless as a result'))
    assert.ok(md.includes('Tier A'))
    assert.ok(md.includes('subscription deleted after apply'), 'the arm legend says what B is')
  })

  it('records an abort on the arm that hit it', () => {
    const aborted: ArmOutcome = { arm: 'A', hypotheses: [], abortedAfter: 3, abortReason: 'ceiling hit' }
    const md = renderMarkdown(buildReport({ ...input, outcomes: [aborted] }))
    assert.ok(md.includes('Aborted after 3 hypotheses: ceiling hit'))
  })
})

/** Mirrors metrics.ts's 6-decimal rounding so expectations stay exact. */
function round(n: number): number {
  return Math.round(n * 1e6) / 1e6
}
