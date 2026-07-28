import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProjectEvent } from '../../helpers/api.ts'
import {
  collectCandidates,
  roundOutputsFromEvents,
  validateCandidateSet,
} from './collect.ts'
import { FIXTURE_PROMPT_VERSIONS, fixtureSet } from './fixtures.ts'

test('collectCandidates produces {id, text, meta} with provenance in meta only', () => {
  const set = fixtureSet()
  validateCandidateSet(set)
  assert.equal(set.candidates.length, 6)
  for (const c of set.candidates) {
    assert.deepEqual(Object.keys(c).sort(), ['id', 'meta', 'text'])
    assert.ok(c.text.length > 0)
    // Provenance lives in meta, never inline in the gradeable text.
    assert.ok(!c.text.includes(c.meta.promptVersion!), 'prompt version leaked into text')
    assert.ok(!c.text.includes(c.meta.worker!), 'worker name leaked into text')
    assert.ok(!c.text.includes(String(c.meta.round)), 'round number leaked into text')
  }
  assert.deepEqual(
    set.candidates.map((c) => c.meta.round),
    [0, 0, 1, 1, 2, 2],
  )
  assert.equal(set.candidates[4]!.meta.promptVersion, FIXTURE_PROMPT_VERSIONS[2])
})

test('candidate ids are positional, not derived from provenance', () => {
  const set = fixtureSet()
  assert.deepEqual(
    set.candidates.map((c) => c.id),
    ['cand-1', 'cand-2', 'cand-3', 'cand-4', 'cand-5', 'cand-6'],
  )
})

test('collectCandidates refuses empty text, bad rounds and empty input', () => {
  const opts = { task: 't', criterion: 'c' }
  assert.throws(() => collectCandidates([], opts), /no outputs/)
  assert.throws(() => collectCandidates([{ round: 0, text: '   ' }], opts), /empty text/)
  assert.throws(() => collectCandidates([{ round: -1, text: 'x' }], opts), /round/)
})

test('validateCandidateSet catches duplicate ids', () => {
  const set = fixtureSet()
  set.candidates[1]!.id = set.candidates[0]!.id
  assert.throws(() => validateCandidateSet(set), /duplicate candidate id/)
})

function event(id: string, worker: string, text: string, at: number, type = 'worker.finished'): ProjectEvent {
  return {
    id,
    project: 'p-tierb',
    type,
    text,
    envelope: {
      depth: 0,
      source: 'worker',
      worker,
      session_id: `s-${id}`,
      interactive: false,
      attention_requested: false,
    },
    occurred_at: at,
    created_at: at,
    delivered: true,
  }
}

test('roundOutputsFromEvents filters by worker and type, orders by time, numbers rounds', () => {
  const events = [
    event('e3', 'analyst', 'third', 300),
    event('e1', 'analyst', 'first', 100),
    event('e-other', 'critic', 'not mine', 150),
    event('e-wrongtype', 'analyst', 'ignored', 120, 'worker.started'),
    event('e2', 'analyst', 'second', 200),
  ]
  const outputs = roundOutputsFromEvents(events, { worker: 'analyst' })
  assert.deepEqual(
    outputs.map((o) => [o.round, o.text, o.sourceEventId]),
    [
      [0, 'first', 'e1'],
      [1, 'second', 'e2'],
      [2, 'third', 'e3'],
    ],
  )
  assert.equal(outputs[0]!.project, 'p-tierb')
  assert.equal(outputs[0]!.worker, 'analyst')
})

test('roundOutputsFromEvents honours textOf, roundOf, promptVersionOf and arm', () => {
  const events = [event('e1', 'analyst', 'PREAMBLE<<deliverable one>>', 100)]
  const outputs = roundOutputsFromEvents(events, {
    worker: 'analyst',
    arm: 'B',
    textOf: (e) => e.text.replace(/^.*<<|>>.*$/g, ''),
    roundOf: () => 5,
    promptVersionOf: () => 'pv-abc',
  })
  assert.deepEqual(outputs, [
    {
      round: 5,
      text: 'deliverable one',
      worker: 'analyst',
      project: 'p-tierb',
      sourceEventId: 'e1',
      promptVersion: 'pv-abc',
      arm: 'B',
    },
  ])
})
