// route.test.ts — the offline unit layer for parsing and event text. No stack.
//
// Two things get pinned here, and both are ways a rig can silently score itself:
// what counts as a route (route.ts), and where truth is allowed to appear
// (text.ts). Neither is checkable from a running stack — a green run with a
// broken parser looks exactly like a green run.

import { strict as assert } from 'node:assert'
import { describe, it } from 'node:test'
import { ARM_A, ARM_B, armWorkerNames } from './arms'
import {
  auditMarker,
  canonicalRoute,
  ESCALATE,
  parseAuditorCall,
  parseRoute,
  parseStatedRoute,
  QUEUES,
  taskMarker,
  type QueueWorkers,
} from './route'
import { auditEventText, REPLY_QUOTE_LIMIT, routeContract, routingRules, ticketEventText } from './text'

const WORKERS: QueueWorkers = ARM_A.queues

describe('parseStatedRoute', () => {
  it('reads the contract line as written', () => {
    assert.equal(parseStatedRoute('I applied rule 2.\nROUTE-TO: tra-uptime'), 'tra-uptime')
    assert.equal(parseStatedRoute(`ROUTE-TO: ${ESCALATE}`), ESCALATE)
  })

  it('strips ornament without inventing content', () => {
    assert.equal(parseStatedRoute('**ROUTE-TO: tra-money**'), 'tra-money')
    assert.equal(parseStatedRoute('- `ROUTE-TO: tra-money`'), 'tra-money')
    assert.equal(parseStatedRoute('I think this is probably billing work'), '')
    assert.equal(parseStatedRoute('Route it to tra-money please'), '')
  })

  it('scans from the end, so the contract line wins over a summary', () => {
    const reply = ['Summary: ROUTE-TO: tra-money', 'On reflection the stated fact is an HTTP 503.', 'ROUTE-TO: tra-uptime'].join('\n')
    assert.equal(parseStatedRoute(reply), 'tra-uptime')
  })

  it('refuses the charter\'s own placeholder', () => {
    // The charter shows `ROUTE-TO: <queue-name>`. A dispatcher that echoed the
    // template has not routed anything, and crediting it would score the org on
    // the prompt it was given.
    assert.equal(parseStatedRoute('ROUTE-TO: <queue-name>'), '')
    assert.equal(parseStatedRoute('ROUTE-TO: <one of the queue names above>'), '')
  })

  it('returns nothing for a reply with no contract line at all', () => {
    assert.equal(parseStatedRoute(''), '')
    assert.equal(parseStatedRoute('The customer is upset.'), '')
  })
})

describe('canonicalRoute', () => {
  it('maps this arm\'s worker names onto the canonical queue ids', () => {
    assert.equal(canonicalRoute('tra-money', WORKERS), 'billing')
    assert.equal(canonicalRoute('tra-uptime', WORKERS), 'outage')
    assert.equal(canonicalRoute('tra-signin', WORKERS), 'access')
    assert.equal(canonicalRoute(ESCALATE, WORKERS), ESCALATE)
    assert.equal(canonicalRoute('ESCALATE', WORKERS), ESCALATE)
  })

  it('calls another arm\'s worker unparseable, not a wrong queue', () => {
    // Addressing something this project does not hold is output-contract
    // breakage, a different failure from misrouting. Folding them together
    // would inflate the headline with the wrong thing.
    assert.equal(canonicalRoute(ARM_B.queues.billing, WORKERS), 'unparseable')
    assert.equal(canonicalRoute('billing', WORKERS), 'unparseable')
    assert.equal(canonicalRoute('', WORKERS), 'unparseable')
  })

  it('is exact about names, not fuzzy', () => {
    assert.equal(canonicalRoute('tra-money-desk', WORKERS), 'unparseable')
    assert.equal(canonicalRoute('TRA-MONEY', WORKERS), 'unparseable')
  })
})

describe('parseRoute', () => {
  it('is parseStatedRoute then canonicalRoute', () => {
    assert.equal(parseRoute('…\nROUTE-TO: tra-signin', WORKERS), 'access')
    assert.equal(parseRoute('…\nROUTE-TO: escalate', WORKERS), ESCALATE)
    assert.equal(parseRoute('…\nno line here', WORKERS), 'unparseable')
  })
})

describe('parseAuditorCall', () => {
  it('reads the charter\'s wording', () => {
    assert.equal(parseAuditorCall('Verdict: match - the route matches.'), 'match')
    assert.equal(parseAuditorCall('Verdict: mismatch — it went to the wrong desk.'), 'mismatch')
    assert.equal(parseAuditorCall('**Verdict: match**'), 'match')
  })

  it('refuses to guess from prose', () => {
    assert.equal(parseAuditorCall('It looks about right to me'), 'unparseable')
    assert.equal(parseAuditorCall(''), 'unparseable')
  })
})

describe('markers', () => {
  it('scopes the ticket marker to an arm, so a mock rule can name that arm\'s queue', () => {
    assert.equal(taskMarker('a', 't01'), '[TRI-A-T01]')
    assert.equal(taskMarker('b', 't01'), '[TRI-B-T01]')
    assert.notEqual(taskMarker('a', 't01'), taskMarker('b', 't01'))
  })

  it('keys the audit marker on ticket and STATED route only', () => {
    assert.equal(auditMarker('t02', 'billing'), '[TRI-AUDIT-T02-BILLING]')
    assert.equal(auditMarker('t03', ESCALATE), '[TRI-AUDIT-T03-ESCALATE]')
    assert.equal(auditMarker('t04', 'unparseable'), '[TRI-AUDIT-T04-UNPARSEABLE]')
  })

  it('carries no arm tag on the audit marker, so one rule serves every arm', () => {
    // An arm-shaped scoreboard would be a scoreboard per arm.
    assert.equal(auditMarker('t01', 'billing').includes('-A-'), false)
    assert.equal(auditMarker('t01', 'billing').includes('-B-'), false)
  })

  it('never says whether the harness thinks the route was right', () => {
    // Handing the auditor its answer would make auditor_agreement a tautology.
    for (const route of [...QUEUES, ESCALATE] as const) {
      const marker = auditMarker('t01', route)
      for (const grade of ['CORRECT', 'WRONG', 'MATCH', 'MISMATCH', 'TRUE', 'FALSE']) {
        assert.equal(marker.includes(grade), false, `${marker} leaks a grade`)
      }
    }
  })
})

describe('routingRules', () => {
  const rules = routingRules(WORKERS)

  it('names this arm\'s queues and the escalation', () => {
    for (const q of QUEUES) assert.equal(rules.includes(WORKERS[q]), true, `rules do not mention ${WORKERS[q]}`)
    assert.equal(rules.includes(ESCALATE), true)
  })

  it('states the content rules the generator generates against', () => {
    assert.equal(rules.includes('monetary amount'), true)
    assert.equal(rules.includes('HTTP status in the 500s'), true)
    assert.equal(rules.includes('cannot sign in'), true)
  })

  it('states the scenario\'s whole point in one sentence', () => {
    assert.equal(rules.includes('Vocabulary is not a fact'), true)
  })

  it('carries the output contract, because that is the only channel into the charter', () => {
    assert.equal(rules.includes(routeContract(WORKERS)), true)
  })

  it('is deterministic — the report records it verbatim', () => {
    assert.equal(routingRules(WORKERS), rules)
  })
})

describe('ticketEventText', () => {
  const ticket = 'Subject: Invoice looks wrong\nReported by: Someone (Somewhere)\n\nThe amount taken was GBP 90.00 instead of the agreed GBP 40.00.\n'
  const text = ticketEventText({ armTag: 'a', ticketId: 't01', ticket, workers: WORKERS })

  it('carries the marker, the ticket and the contract', () => {
    assert.equal(text.startsWith('[TRI-A-T01]'), true)
    assert.equal(text.includes('The amount taken was GBP 90.00'), true)
    assert.equal(text.includes(routeContract(WORKERS)), true)
  })

  it('carries NO ground truth of any kind', () => {
    for (const leak of ['correct route', 'held-out', 'escalate is', 'misdirect', 'decoy', 'the answer']) {
      assert.equal(text.toLowerCase().includes(leak.toLowerCase()), false, `ticket event leaks "${leak}"`)
    }
  })

  it('frames every ticket identically, so the ticket is the only thing that varies', () => {
    const other = ticketEventText({ armTag: 'a', ticketId: 't02', ticket: 'Subject: Other\n', workers: WORKERS })
    const strip = (s: string) => s.replace(/--- ticket ---[\s\S]*--- end of ticket ---/, '').replace(/t0\d/gi, 'Tnn')
    assert.equal(strip(text), strip(other))
  })
})

describe('auditEventText', () => {
  const text = auditEventText({
    ticketId: 't02',
    reply: 'I applied the billing rule.\nROUTE-TO: tra-money',
    route: 'billing',
    statedRoute: 'tra-money',
    truthRoute: 'outage',
    truthExplanation: 'the ticket reads like billing work and is outage work.',
  })

  it('is the ONE place truth enters the project', () => {
    assert.equal(text.includes('Held-out correct route: outage'), true)
    assert.equal(text.includes('the ticket reads like billing work'), true)
  })

  it('quotes the decision and states the route, without grading either', () => {
    assert.equal(text.includes('ROUTE-TO: tra-money'), true)
    assert.equal(text.includes('Stated route: tra-money'), true)
    for (const grade of ['is wrong', 'is correct', 'the dispatcher was']) {
      assert.equal(text.includes(grade), false, `audit event grades the decision ("${grade}")`)
    }
  })

  it('says so plainly when nothing was stated', () => {
    const mute = auditEventText({
      ticketId: 't02',
      reply: 'no idea',
      route: 'unparseable',
      statedRoute: '',
      truthRoute: 'outage',
      truthExplanation: 'x',
    })
    assert.equal(mute.includes('Stated route: (none stated)'), true)
    assert.equal(mute.startsWith('[TRI-AUDIT-T02-UNPARSEABLE]'), true)
  })

  it('truncates a runaway reply rather than mailing a novel', () => {
    const huge = auditEventText({
      ticketId: 't02',
      reply: 'x'.repeat(REPLY_QUOTE_LIMIT + 500),
      route: 'billing',
      statedRoute: 'tra-money',
      truthRoute: 'billing',
      truthExplanation: 'x',
    })
    assert.equal(huge.includes('reply truncated at'), true)
    assert.equal(huge.length < REPLY_QUOTE_LIMIT + 1000, true)
  })
})

describe('the arms', () => {
  const names = [...armWorkerNames(ARM_A), ...armWorkerNames(ARM_B)]

  it('gives each arm six workers', () => {
    assert.equal(armWorkerNames(ARM_A).length, 6)
    assert.equal(armWorkerNames(ARM_B).length, 6)
  })

  it('keeps all twelve names distinct and mutually non-substring', () => {
    // The mock script is agentd-wide: a name that contains another silently
    // matches the wrong rule (docs/product/13's standing traps). triage-lab@v1's
    // renderer enforces this within an arm; ACROSS arms only this test does.
    for (let i = 0; i < names.length; i++) {
      for (let j = i + 1; j < names.length; j++) {
        assert.notEqual(names[i], names[j], `duplicate worker name ${names[i]}`)
        assert.equal(
          names[i].includes(names[j]) || names[j].includes(names[i]),
          false,
          `${names[i]} and ${names[j]} are substrings of one another`,
        )
      }
    }
  })

  it('keeps the identity phrases distinct too — that is what mock rules key on', () => {
    const phrases = names.map((n) => `You are ${n},`)
    assert.equal(new Set(phrases).size, phrases.length)
  })

  it('differs in exactly one operator mutation', () => {
    assert.equal(ARM_A.disableCritic, undefined)
    assert.equal(ARM_B.disableCritic, true)
  })

  it('gives the arms different marker tags', () => {
    assert.notEqual(ARM_A.tag, ARM_B.tag)
  })
})
