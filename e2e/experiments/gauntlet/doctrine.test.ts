// doctrine.test.ts — the offline guard on the gauntlet's doctrine axis.
//
// One thing is checked here that nothing else can check: the THREE-WAY
// agreement between the canonical doctrine file, the phrase this rig keys its
// tripwire on, and the mock script that uses it. Each is edited by a different
// kind of change — a doctrine revision, a rig refactor, a script tweak — and if
// they drift the tripwire silently stops being a tripwire: a rule whose
// `absent` string is no longer in any composed prompt never fires, so the
// doctrine arm would quietly behave like the control and the report would show
// a compliance delta of zero that looked like a finding.
//
// DR1 wrote the same test for the calibration rig's WD-2 phrase. This one is
// not a copy for its own sake: SC-3 keys on WD-1, the entry it exists to
// measure, and the two rigs must be able to move independently.

import { strict as assert } from 'node:assert'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { describe, it } from 'node:test'
import { ARM_DOCTRINE_OFF, ARM_DOCTRINE_V1, armWorkerNames } from './arms'
import { loadDoctrine, WD1_DELIVERY_PHRASE } from './doctrine'

/** Repo root from the COMPILED location — the same arithmetic gauntlet.ts does. */
const REPO_ROOT = path.resolve(__dirname, '../../../../..')
const SCRIPT_PATH = path.join(REPO_ROOT, 'e2e/mock-scripts/gauntlet-smoke.json')

interface Rule {
  match: string
  absent?: string
  turns: Array<{ blocks: Array<{ type: string; text?: string; name?: string; input?: Record<string, unknown> }> }>
}
const rules: Rule[] = (JSON.parse(fs.readFileSync(SCRIPT_PATH, 'utf8')) as { rules: Rule[] }).rules

describe('the WD-1 delivery phrase', () => {
  it('is a line of the canonical doctrine, so a rule keyed on it proves delivery', () => {
    assert.ok(
      loadDoctrine('v1').includes(WD1_DELIVERY_PHRASE),
      'the phrase the mock script keys on is not in doctrine-v1 — the tripwire can never fire',
    )
  })

  it('is WD-1, the entry this scenario exists to test', () => {
    // Not decoration: SC-3 is doc 20 §5's named instrument for WD-1, so the arm
    // that loses its doctrine has to lose THAT entry, not a neighbouring one.
    const block = loadDoctrine('v1')
    const line = block.split('\n').find((l) => l.includes(WD1_DELIVERY_PHRASE)) ?? ''
    assert.ok(/^\s*1\./.test(line), `the phrase is not on doctrine-v1's first entry: ${JSON.stringify(line)}`)
  })

  it('survives JSON encoding, so the substring match can actually match', () => {
    // The proxy matches a raw substring against the JSON-encoded request body. A
    // phrase containing a quote, a backslash or a newline would be escaped in
    // that body and the rule would be quietly always-false.
    assert.equal(JSON.stringify(WD1_DELIVERY_PHRASE), `"${WD1_DELIVERY_PHRASE}"`)
  })

  it('is pure ASCII, so no encoder in the path can change it', () => {
    // doctrine-v1's line 1 runs on into an em dash on its third line. A
    // non-ASCII rune survives Go's and JavaScript's encoders unescaped, but
    // "survives every encoder between here and the model" is not something this
    // rig can check — so the phrase stops before it.
    // eslint-disable-next-line no-control-regex
    assert.ok(/^[\x20-\x7E]+$/.test(WD1_DELIVERY_PHRASE), 'the delivery phrase carries a non-ASCII character')
  })

  it('lies within a single line of the block', () => {
    assert.ok(!WD1_DELIVERY_PHRASE.includes('\n'), 'a phrase spanning a newline can never match the encoded body')
  })
})

describe('the tripwire in the committed mock script', () => {
  it('narrows a TICKET rule, never stands alone', () => {
    const tripwires = rules.filter((r) => r.absent === WD1_DELIVERY_PHRASE)
    assert.ok(tripwires.length > 0, 'no rule in the gauntlet script splits on the doctrine phrase')
    for (const r of tripwires) {
      assert.ok(
        /^\[GAU-[AD]-G\d\d\]$/.test(r.match),
        `the doctrine split must sit INSIDE a per-ticket partition, not on ${r.match}: the block rides ` +
          "every worker's prompt, so a rule keyed on a doctrine phrase alone would answer the queues, the " +
          'critic and the frozen auditor too',
      )
    }
  })

  it('gives every tripwire a fall-through rule BELOW it', () => {
    // Doctrine present → the narrowed rule is skipped → the request must land on
    // an un-narrowed rule for the same ticket. Without one it would fall through
    // to the canned reply and the doctrine arm would score `unparseable`
    // everywhere, which is a different (and much louder) failure than the one
    // this rig is measuring.
    for (const [i, r] of rules.entries()) {
      if (r.absent !== WD1_DELIVERY_PHRASE) continue
      const fallThrough = rules.findIndex((o, j) => j > i && o.match === r.match && o.absent === undefined)
      assert.notEqual(fallThrough, -1, `${r.match} has a tripwire and no rule below it to fall through to`)
    }
  })

  it('splits BOTH arms, so the difference is doctrine and not the arm', () => {
    // The script is doctrine-conditional, not arm-conditional. The control arm
    // takes the compliant branch because it has no doctrine; the doctrine arm
    // takes the other because the block reached its composed prompts. Break the
    // injection and the doctrine arm collapses onto the control — which is the
    // whole non-vacuity argument, and it only holds if both arms are scripted
    // the same way.
    for (const tag of ['A', 'D']) {
      const split = rules.filter((r) => r.absent === WD1_DELIVERY_PHRASE && r.match.startsWith(`[GAU-${tag}-`))
      assert.equal(split.length, 4, `arm ${tag} has ${split.length} doctrine-split tickets, want 4 (one per directive kind)`)
    }
  })

  it('never lets a doctrine phrase into a REPLY, which would make the wire self-tripping', () => {
    for (const rule of rules) {
      for (const t of rule.turns) {
        for (const b of t.blocks) {
          const said = b.type === 'tool_use' ? JSON.stringify(b.input ?? {}) : (b.text ?? '')
          assert.ok(
            !said.includes(WD1_DELIVERY_PHRASE),
            `rule ${rule.match} says the doctrine phrase out loud — it would then appear in the transcript ` +
              'of workers whose prompt never carried it, and the tripwire would stop firing mid-run',
          )
        }
      }
    }
  })
})

describe('the arms', () => {
  it('differ in the doctrine mutation and nothing else', () => {
    // Everything an arm carries beyond names: the fields that could make one arm
    // behave differently for a reason other than doctrine. `optional` would skip
    // an arm, and any future flag lands here too — the keys are compared, not
    // just the values, so a field added to one arm alone fails this test.
    const shape = (arm: typeof ARM_DOCTRINE_OFF) =>
      Object.keys(arm)
        .filter((k) => !['id', 'tag', 'dispatcher', 'queues', 'critic', 'auditor', 'note', 'doctrine'].includes(k))
        .sort()
    assert.deepEqual(shape(ARM_DOCTRINE_OFF), shape(ARM_DOCTRINE_V1), 'the arms differ in more than the doctrine field')
    assert.deepEqual(shape(ARM_DOCTRINE_OFF), [], 'an arm carries a behaviour flag beyond the doctrine axis')
    assert.equal(ARM_DOCTRINE_OFF.doctrine, undefined)
    assert.equal(ARM_DOCTRINE_V1.doctrine, 'v1')
    // Worker names are per-arm and must stay mutually non-substring: the mock
    // script is agentd-wide (the naming trap in docs/product/13).
    const names = [...armWorkerNames(ARM_DOCTRINE_OFF), ...armWorkerNames(ARM_DOCTRINE_V1)]
    for (const a of names) {
      for (const b of names) {
        if (a !== b) assert.ok(!a.includes(b), `${a} contains ${b} — a rule keyed on one would fire for the other`)
      }
    }
  })

  it('runs the critic live in both arms', () => {
    // Not an omission: the critic is what makes the two attributed compliance
    // signals non-trivial (it writes prompts and trips the frozen boundary on
    // its own account), and an arm difference beyond doctrine would break doc
    // 20 §2's one-mutation rule.
    for (const arm of [ARM_DOCTRINE_OFF, ARM_DOCTRINE_V1]) {
      assert.ok(arm.critic !== '', `${arm.id} has no critic`)
    }
  })
})
