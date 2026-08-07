// verdict.test.ts — the offline unit layer for the parsers. No stack.
//
// These are the two places the calibration can silently lie: a parser that
// guesses a verdict out of prose scores the harness's optimism, and a parser
// that misses a valid contract line inflates `unparseable` and hides a real
// answer. Both directions are pinned below.

import { strict as assert } from 'node:assert'
import { describe, it } from 'node:test'
import { checkEventText, datasetEventText, OUTPUT_CONTRACT } from './text'
import { checkMarker, parseCheckerCall, parseVerdict, taskMarker } from './verdict'

describe('parseVerdict', () => {
  it('reads the contract line', () => {
    assert.equal(parseVerdict('Some analysis.\nVERDICT: effect'), 'effect')
    assert.equal(parseVerdict('Some analysis.\nVERDICT: no-effect'), 'no-effect')
  })

  it('tolerates the ornament a model puts round a final line', () => {
    for (const line of [
      '**VERDICT: effect**',
      '`VERDICT: effect`',
      '  VERDICT:   effect  ',
      '- VERDICT: effect',
      'VERDICT: effect.',
      'verdict: EFFECT',
    ]) {
      assert.equal(parseVerdict(`prose\n${line}`), 'effect', line)
    }
    for (const line of ['VERDICT: no effect', 'VERDICT:no-effect', '**VERDICT: no-effect**']) {
      assert.equal(parseVerdict(`prose\n${line}`), 'no-effect', line)
    }
  })

  it('ignores trailing blank lines', () => {
    assert.equal(parseVerdict('VERDICT: no-effect\n\n   \n'), 'no-effect')
  })

  it('takes the LAST contract line when a reply states one twice', () => {
    // A summary at the top and the contract line at the bottom: the contract
    // names the final line, so that is the one scored.
    assert.equal(parseVerdict('VERDICT: effect\nOn reflection the control kills it.\nVERDICT: no-effect'), 'no-effect')
  })

  it('refuses to guess from prose', () => {
    for (const prose of [
      'There is clearly an effect here.',
      'No significant effect once age_group is controlled.',
      'My verdict is that the effect is real.',
      'VERDICT: maybe',
      'VERDICT: effect is present',
      '',
    ]) {
      assert.equal(parseVerdict(prose), 'unparseable', prose)
    }
  })

  it('does not read a verdict out of the instructions', () => {
    // The whole point of parsing the deliverable rather than the transcript:
    // the task text CONTAINS both contract lines, and a parser pointed at it
    // would score the harness's own words. This asserts the shape of the
    // failure, so that a future refactor pointing the parser at the transcript
    // trips here instead of in a live run.
    const task = datasetEventText({ hypothesisId: 'h01', csv: 'jumper,age_group,late\nred,young,yes\n', rows: 1 })
    assert.equal(parseVerdict(task), 'no-effect', 'the contract text ends with the no-effect line')
    assert.ok(task.includes('VERDICT: effect'), 'both contract lines are in the instructions')
  })
})

describe('parseCheckerCall', () => {
  it('reads the checker charter wording', () => {
    assert.equal(parseCheckerCall('Verdict: match — both agree.'), 'match')
    assert.equal(parseCheckerCall('Verdict: mismatch — they disagree.'), 'mismatch')
    assert.equal(parseCheckerCall('reasoning\n**Verdict: match**'), 'match')
  })

  it('is unparseable when the checker refused or wandered', () => {
    for (const reply of [
      'No ground truth was stated, so I refuse to judge.',
      'The conclusion looks fine to me.',
      '',
    ]) {
      assert.equal(parseCheckerCall(reply), 'unparseable', reply)
    }
  })
})

describe('markers', () => {
  it('are distinct per hypothesis and per stated verdict', () => {
    assert.equal(taskMarker('h01'), '[CAL-H01]')
    assert.equal(checkMarker('h02', 'effect'), '[CAL-CHECK-H02-YES]')
    assert.equal(checkMarker('h02', 'no-effect'), '[CAL-CHECK-H02-NO]')
    assert.equal(checkMarker('h02', 'unparseable'), '[CAL-CHECK-H02-UNPARSED]')
    // Neither is a substring of the other: a mock rule keyed on one must never
    // fire for the other (the body-match trap).
    assert.ok(!checkMarker('h02', 'effect').includes(checkMarker('h02', 'no-effect')))
    assert.ok(!checkMarker('h02', 'no-effect').includes(checkMarker('h02', 'effect')))
    // Zero-padded ids keep h01 from being a prefix of h10's marker.
    assert.ok(!taskMarker('h10').includes(taskMarker('h01')))
  })
})

describe('event text', () => {
  const csv = 'jumper,age_group,late\nred,young,yes\nother,old,no\n'

  it('the dataset event carries the data and the contract, and no truth', () => {
    const body = datasetEventText({ hypothesisId: 'h07', csv, rows: 2 })
    assert.ok(body.startsWith('[CAL-H07]'))
    assert.ok(body.includes('red,young,yes'))
    assert.ok(body.includes(OUTPUT_CONTRACT))
    for (const leak of ['ground truth', 'held-out', 'planted-null', 'confound', 'effect=']) {
      assert.ok(!body.toLowerCase().includes(leak.toLowerCase()), `dataset event leaks ${leak}`)
    }
  })

  it('the check event carries the conclusion and the truth, together and only there', () => {
    const body = checkEventText({
      hypothesisId: 'h07',
      conclusion: 'prose\nVERDICT: effect',
      verdict: 'effect',
      truthEffect: false,
      truthExplanation: 'jumper has no effect on late; any confirmation is a false positive.',
    })
    assert.ok(body.startsWith('[CAL-CHECK-H07-YES]'))
    assert.ok(body.includes('Stated verdict: effect'))
    assert.ok(body.includes('Held-out ground truth: effect=false.'))
    assert.ok(body.includes('any confirmation is a false positive'))
    // The harness does not hand the scoreboard its own grade — otherwise the
    // agreement metric would be a tautology.
    for (const grade of ['correct', 'incorrect', 'wrong', 'expected_verdict', 'the honest answer is']) {
      assert.ok(!body.toLowerCase().includes(grade), `check event leaks the harness's grade: ${grade}`)
    }
  })

  it('truncates a runaway conclusion rather than mailing a novel', () => {
    const body = checkEventText({
      hypothesisId: 'h07',
      conclusion: 'x'.repeat(9000),
      verdict: 'unparseable',
      truthEffect: true,
      truthExplanation: 'e',
    })
    assert.ok(body.includes('conclusion truncated at 4000 characters'))
    assert.ok(body.includes('[CAL-CHECK-H07-UNPARSED]'))
  })
})
