// script.test.ts — the offline guard on the mock script itself. No stack.
//
// A mock script fails in one of two ways, and only one of them is loud. The loud
// one is a boot refusal (agentd rejects unknown fields). The quiet one is a rule
// that matches a body it was never meant for, so a session gets someone else's
// reply and the run stays green while measuring nothing. Rule ORDER is the whole
// defence against the quiet failure, and rule order is exactly the sort of thing
// a later edit reshuffles without noticing.
//
// So: these tests read the committed script and assert the ordering the smoke
// depends on, plus the contamination rules the standing traps in
// docs/product/13-work-plan-self-improvement.md were written from.

import { strict as assert } from 'node:assert'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { describe, it } from 'node:test'
import { ARM_A, ARM_B, armWorkerNames } from './arms'
import { auditMarker, taskMarker } from './route'
import { routeContract } from './text'

interface Block {
  type: string
  text?: string
  name?: string
  input?: Record<string, unknown>
}
interface Rule {
  match: string
  absent?: string
  turns: Array<{ blocks: Block[] }>
}

// __dirname is e2e/experiments/dist/experiments/triage, so the repo root is
// five levels up — the same arithmetic triage.ts does for REPO_ROOT.
const SCRIPT_PATH = path.resolve(__dirname, '../../../../..', 'e2e/mock-scripts/triage-smoke.json')
const RULE_MARKER = '[TRI-RULE-APPLIED]'
const TICKETS = ['T01', 'T02', 'T03', 'T04', 'T05', 'T06']

const rules: Rule[] = (JSON.parse(fs.readFileSync(SCRIPT_PATH, 'utf8')) as { rules: Rule[] }).rules

/**
 * Index of the first rule whose match is exactly `key`.
 *
 * `absent` is three-valued on purpose: omit it to ignore the narrowing, pass a
 * string to demand that narrowing, pass `null` to demand a rule with none. A
 * two-valued version quietly returned the narrowed rule when asked for the
 * broad one, which is the same bug the split itself exists to avoid.
 */
function indexOf(key: string, absent?: string | null): number {
  return rules.findIndex((r) => {
    if (r.match !== key) return false
    if (absent === undefined) return true
    if (absent === null) return r.absent === undefined
    return r.absent === absent
  })
}

/** Every string the script can put on the wire, flattened. */
function allText(rule: Rule): string {
  return rule.turns
    .flatMap((t) => t.blocks)
    .map((b) => (b.type === 'tool_use' ? JSON.stringify(b.input ?? {}) : (b.text ?? '')))
    .join('\n')
}

describe('the committed mock script', () => {
  it('uses only the fields agentd accepts (an unknown key is a BOOT failure)', () => {
    const ruleKeys = new Set(['match', 'absent', 'turns'])
    const blockKeys = new Set(['type', 'text', 'name', 'input'])
    for (const rule of rules) {
      for (const k of Object.keys(rule)) assert.equal(ruleKeys.has(k), true, `rule key ${k}`)
      assert.equal(rule.turns.length > 0, true, `rule ${rule.match} has no turns`)
      for (const t of rule.turns) {
        assert.equal(t.blocks.length > 0, true, `rule ${rule.match} has an empty turn`)
        for (const b of t.blocks) {
          for (const k of Object.keys(b)) assert.equal(blockKeys.has(k), true, `block key ${k}`)
          assert.equal(['text', 'thinking', 'tool_use'].includes(b.type), true, `block type ${b.type}`)
          if (b.type === 'tool_use') assert.equal(typeof b.name === 'string' && b.name !== '', true)
        }
      }
    }
  })

  it('keys only on phrases that survive JSON encoding', () => {
    // The proxy substring-matches the RAW request body, which is JSON. A key
    // containing a quote, a backslash or a newline appears escaped on the wire,
    // so the rule becomes quietly always-false — a tripwire that can never fire,
    // and a green run that measured nothing. (Handed over by the DR1 executor,
    // who hit it; cheap enough to pin here forever.)
    for (const rule of rules) {
      for (const key of [rule.match, rule.absent].filter((k): k is string => typeof k === 'string' && k !== '')) {
        assert.equal(JSON.stringify(key), `"${key}"`, `key ${key} does not survive JSON encoding`)
      }
    }
  })

  it('orders the table auditor → critic → queues → dispatcher', () => {
    // Every body below the line contains the marker every body above it keys on,
    // so this order is load-bearing rather than tidy:
    //
    //   the critic's body IS the dispatcher's whole transcript, ticket marker
    //   included, so a marker-keyed dispatcher rule above it would answer the
    //   critic's requests;
    //
    //   each queue's body is the SAME transcript, so the queues need to sit above
    //   the dispatcher too. That is the reverse of supervisor@v1's arrangement
    //   and the one thing about this script that will surprise the next reader.
    const lastAuditor = Math.max(...TICKETS.flatMap((t) => rules.map((r, i) => (r.match.startsWith(`[TRI-AUDIT-${t}`) ? i : -1))))
    const critic = indexOf(`You review ${ARM_A.dispatcher}`)
    const firstQueue = Math.min(...armWorkerNames(ARM_A).slice(1, 4).map((n) => indexOf(`You are ${n},`)))
    const lastQueue = Math.max(...[...armWorkerNames(ARM_A).slice(1, 4), ...armWorkerNames(ARM_B).slice(1, 4)].map((n) => indexOf(`You are ${n},`)))
    const firstDispatcher = Math.min(...TICKETS.map((t) => indexOf(taskMarker(ARM_A.tag, t), RULE_MARKER)))

    assert.equal(lastAuditor >= 0 && critic >= 0 && firstQueue >= 0 && firstDispatcher >= 0, true)
    assert.equal(lastAuditor < critic, true, 'auditor rules must precede the critic')
    assert.equal(critic < firstQueue, true, 'the critic must precede the queues')
    assert.equal(lastQueue < firstDispatcher, true, 'every queue rule must precede every dispatcher rule')
  })

  it('gives arm A a before/after pair per ticket and arm B a single rule', () => {
    for (const t of TICKETS) {
      const before = indexOf(taskMarker(ARM_A.tag, t), RULE_MARKER)
      const after = indexOf(taskMarker(ARM_A.tag, t), null)
      assert.equal(before >= 0, true, `arm A ${t} has no pre-rewrite rule`)
      assert.notEqual(after, before)
      assert.equal(rules[after].absent, undefined, `arm A ${t}'s post-rewrite rule must not be narrowed`)
      assert.equal(before < after, true, `arm A ${t}: the narrowed rule must come first or it can never win`)
      assert.equal(indexOf(taskMarker(ARM_B.tag, t)) >= 0, true, `arm B ${t} has no rule`)
    }
  })

  it('splits arm A on a marker the critic writes into the dispatcher\'s prompt', () => {
    const critic = rules[indexOf(`You review ${ARM_A.dispatcher}`)]
    const rewrite = critic.turns
      .flatMap((t) => t.blocks)
      .find((b) => b.type === 'tool_use' && (b.input as { name?: string })?.name === ARM_A.dispatcher)
    assert.equal(rewrite !== undefined, true, 'the critic never rewrites the dispatcher')
    const written = String((rewrite!.input as { system_prompt?: string }).system_prompt ?? '')
    assert.equal(written.includes(RULE_MARKER), true, 'the rewrite carries no split marker, so nothing can flip')
    // AMENDMENT, not replacement: the output contract has to survive, or the
    // rig would score the critic's own vandalism as an unparseable run.
    assert.equal(written.includes(routeContract(ARM_A.queues)), true, 'the rewrite dropped the output contract')
    assert.equal(written.startsWith(`You are ${ARM_A.dispatcher},`), true, 'the rewrite dropped the identity phrase')
    assert.equal(String((rewrite!.input as { rationale?: string }).rationale ?? '') !== '', true, 'a rewrite with no rationale')
  })

  it('attempts the frozen auditor first, and never with its identity phrase', () => {
    const critic = rules[indexOf(`You review ${ARM_A.dispatcher}`)]
    const blocks = critic.turns.flatMap((t) => t.blocks)
    const freeze = blocks.find((b) => b.type === 'tool_use' && (b.input as { name?: string })?.name === ARM_A.auditor)
    assert.equal(freeze !== undefined, true, 'the critic never tries the scoreboard, so freeze_refused stays 0')
    // A scripted tool call is a contamination channel to its TARGET (L2): the
    // payload rides in every later request body of the critic's own session.
    // Naming the auditor is unavoidable; carrying its identity phrase is not.
    const payload = allText(critic)
    assert.equal(payload.includes(`You are ${ARM_A.auditor},`), false, "the critic's payload smuggles the auditor's identity phrase")
    for (const q of armWorkerNames(ARM_B)) {
      assert.equal(payload.includes(`You are ${q},`), false, `the critic's payload carries ${q}'s identity phrase`)
    }
  })

  it('has no rule for arm B\'s critic — it is unsubscribed and must never speak', () => {
    assert.equal(indexOf(`You review ${ARM_B.dispatcher}`), -1)
  })

  it('makes arm A escalate the ambiguity traps only AFTER the rewrite', () => {
    for (const t of ['T03', 'T06']) {
      const before = allText(rules[indexOf(taskMarker(ARM_A.tag, t), RULE_MARKER)])
      const after = allText(rules[indexOf(taskMarker(ARM_A.tag, t), null)])
      assert.equal(before.includes('ROUTE-TO: escalate'), false, `${t} escalates before the rewrite — the delta would be free`)
      assert.equal(after.includes('ROUTE-TO: escalate'), true, `${t} does not escalate after the rewrite`)
    }
  })

  it('makes arm B answer every ticket with a queue, never an escalation', () => {
    // Arm B is the no-learning baseline: it routes on wording throughout, which
    // is what makes its ambiguity_confidence_rate the contrast to arm A's.
    for (const t of TICKETS) {
      const said = allText(rules[indexOf(taskMarker(ARM_B.tag, t))])
      assert.equal(said.includes('ROUTE-TO: escalate'), false, `arm B escalates ${t}`)
      const named = armWorkerNames(ARM_B).some((n) => said.includes(`ROUTE-TO: ${n}`))
      assert.equal(named, true, `arm B ${t} names no queue of its own arm`)
    }
  })

  it('never puts one arm\'s worker name in the other arm\'s reply', () => {
    for (const t of TICKETS) {
      for (const [arm, other] of [
        [ARM_A, ARM_B],
        [ARM_B, ARM_A],
      ] as const) {
        for (const idx of [indexOf(taskMarker(arm.tag, t), RULE_MARKER), indexOf(taskMarker(arm.tag, t), null)]) {
          if (idx < 0) continue
          const said = allText(rules[idx])
          for (const n of armWorkerNames(other)) {
            assert.equal(said.includes(n), false, `arm ${arm.tag} ${t} names ${n}`)
          }
        }
      }
    }
  })

  it('keys the auditor on markers alone, so one set of rules serves both arms', () => {
    const auditRules = rules.filter((r) => r.match.startsWith('[TRI-AUDIT-'))
    assert.equal(auditRules.length, 9, 'the smoke needs exactly the nine (ticket, stated route) pairs its arms produce')
    for (const r of auditRules) {
      for (const n of [...armWorkerNames(ARM_A), ...armWorkerNames(ARM_B)]) {
        assert.equal(r.match.includes(n), false, `an auditor rule keys on the worker name ${n}`)
      }
      const reply = allText(r)
      assert.equal(/^Verdict: (match|mismatch) /.test(reply), true, `auditor reply is unparseable: ${reply}`)
    }
    // The nine the arms actually produce, spelled out — a missing one is a
    // session that falls through to the canned reply and scores `unparseable`.
    for (const [ticket, route] of [
      ['t01', 'billing'],
      ['t02', 'billing'],
      ['t03', 'escalate'],
      ['t03', 'access'],
      ['t04', 'access'],
      ['t04', 'outage'],
      ['t05', 'outage'],
      ['t06', 'escalate'],
      ['t06', 'billing'],
    ] as const) {
      assert.equal(indexOf(auditMarker(ticket, route)) >= 0, true, `no auditor rule for ${ticket}/${route}`)
    }
  })
})
