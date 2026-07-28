// doctrine.test.ts — the offline unit layer for the doctrine axis (DR1).
//
// Two things are checked here that nothing else can check:
//
//   1. **The marker rule**, on synthetic text, in both directions. The block
//      begins at the marker line and runs to EOF; the editorial header above it
//      never travels; a file without the marker fails loudly rather than
//      injecting itself whole.
//   2. **The three-way agreement** between the canonical doctrine file, the
//      phrase the mock script keys on, and the mock script itself. Each of the
//      three is edited by a different kind of change — a doctrine revision, a
//      rig refactor, a script tweak — and if they drift the tripwire silently
//      stops being a tripwire: a rule whose `absent` string is no longer in any
//      composed prompt never fires, and a delivery assertion that cannot fail
//      is decoration. That is the whole reason this test reads real files.

import { strict as assert } from 'node:assert'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { describe, it } from 'node:test'
import {
  DOCTRINE_DELIVERY_PHRASE,
  doctrineMarker,
  doctrinePath,
  extractDoctrine,
  loadDoctrine,
} from './doctrine'

/** Repo root from the COMPILED location — the same arithmetic doctrine.ts does. */
const REPO_ROOT = path.resolve(__dirname, '../../../../..')

const MARKER = doctrineMarker('v1')

describe('extractDoctrine', () => {
  it('takes the marker line and everything after it, verbatim', () => {
    const file = ['# Title', '', '<!-- editorial note -->', '', MARKER, '', '1. First rule.', '2. Second rule.', ''].join('\n')
    assert.equal(extractDoctrine(file, MARKER), [MARKER, '', '1. First rule.', '2. Second rule.', ''].join('\n'))
  })

  it('never lets the editorial header travel into a prompt', () => {
    const file = ['# Operations doctrine v1', '', '<!-- Status: every entry CANDIDATE -->', MARKER, '1. A rule.'].join('\n')
    const block = extractDoctrine(file, MARKER)
    assert.ok(!block.includes('CANDIDATE'), 'the CANDIDATE status reached the injected block')
    assert.ok(!block.includes('# Operations doctrine'), 'the markdown title reached the injected block')
  })

  it('refuses a file with no marker line instead of guessing', () => {
    assert.throws(
      () => extractDoctrine('# Operations doctrine v1\n\n1. A rule with no marker above it.\n', MARKER),
      /no marker line/,
    )
  })

  it('will not accept a marker mentioned inside prose as the marker line', () => {
    // "Rigs inject everything below the === operations doctrine v1 === line" is
    // a sentence ABOUT the marker. Only a line that IS the marker counts.
    const file = `Rigs inject everything below the ${MARKER} line, verbatim.\n\n1. A rule.\n`
    assert.throws(() => extractDoctrine(file, MARKER), /no marker line/)
  })

  it('tolerates trailing whitespace on the marker line', () => {
    assert.equal(extractDoctrine(`# T\n${MARKER}   \n1. A rule.\n`, MARKER), `${MARKER}   \n1. A rule.\n`)
  })
})

describe('loadDoctrine', () => {
  it('reads doctrine-v1 from its canonical file and starts at the marker', () => {
    const block = loadDoctrine('v1')
    assert.ok(block.startsWith(MARKER), `the block does not open with ${MARKER}`)
    assert.ok(block.length > 200, 'the block is implausibly short')
    assert.ok(!block.includes('CANDIDATE'), 'doc 20 prose leaked into the injectable block')
  })

  it('is byte-identical to the tail of the canonical file', () => {
    const raw = fs.readFileSync(path.join(REPO_ROOT, doctrinePath('v1')), 'utf8')
    assert.ok(raw.endsWith(loadDoctrine('v1')), 'the loader is transforming the canonical bytes')
  })
})

describe('the doctrine delivery phrase', () => {
  it('is a line of the canonical doctrine, so a rule keyed on it proves delivery', () => {
    assert.ok(
      loadDoctrine('v1').includes(DOCTRINE_DELIVERY_PHRASE),
      'the phrase the mock script keys on is not in doctrine-v1 — the tripwire can never fire',
    )
  })

  it('survives JSON encoding, so the substring match can actually match', () => {
    // The mock matches a raw substring against the JSON-encoded request body. A
    // phrase containing a quote, a backslash or a newline would be escaped in
    // that body and the rule would be quietly always-false.
    assert.equal(JSON.stringify(DOCTRINE_DELIVERY_PHRASE), `"${DOCTRINE_DELIVERY_PHRASE}"`)
  })

  it('is the `absent` of a rule partitioned by the investigator identity phrase', () => {
    const script = JSON.parse(
      fs.readFileSync(path.join(REPO_ROOT, 'e2e/mock-scripts/calibration-doctrine-smoke.json'), 'utf8'),
    ) as { rules: Array<{ match?: string; absent?: string }> }

    const tripwire = script.rules.findIndex((r) => r.absent === DOCTRINE_DELIVERY_PHRASE)
    assert.notEqual(tripwire, -1, 'no rule in the doctrine script splits on the doctrine phrase')
    assert.equal(
      script.rules[tripwire].match,
      'You are cald-invest,',
      'the doctrine split must sit INSIDE a worker-identity partition — the block rides every ' +
        "worker's prompt, so a doctrine phrase alone would answer the critic and the frozen checker too",
    )

    // Rule order carries the partition: the critic's body is the investigator's
    // whole transcript (the H0 trap), so every critic rule must sit above it.
    const lastCritic = script.rules.map((r) => r.match ?? '').lastIndexOf('You review cald-invest')
    assert.notEqual(lastCritic, -1, 'the doctrine arm has no critic rule — its critic would never rewrite')
    assert.ok(lastCritic < tripwire, "the doctrine arm's critic rule must sit above the investigator's")
  })

  it('appears in no OTHER calibration mock script, so smoke-4 is unaffected', () => {
    const plain = fs.readFileSync(path.join(REPO_ROOT, 'e2e/mock-scripts/calibration-smoke.json'), 'utf8')
    assert.ok(!plain.includes(DOCTRINE_DELIVERY_PHRASE))
    assert.ok(!plain.includes('cald-'), 'the doctrine arm leaked into the plain smoke script')
  })
})
