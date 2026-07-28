import assert from 'node:assert/strict'
import test from 'node:test'

import { deriveSeed, hash64, makeRng, seedState, shuffled, splitmix64 } from './rng.ts'

test('splitmix64 matches the published reference stream for seed 0', () => {
  const next = splitmix64(0n)
  // Golden values for the reference splitmix64. If these change, the
  // implementation drifted from the published algorithm and every seeded
  // property in this harness silently changed meaning.
  assert.equal(next(), 0xe220a8397b1dcdafn)
  assert.equal(next(), 0x6e789e6aa1b965f4n)
  assert.equal(next(), 0x06c45d188009454fn)
})

test('splitmix64 output stays inside 64 bits', () => {
  const next = splitmix64(0xdeadbeefcafebaben)
  for (let i = 0; i < 200; i++) {
    const v = next()
    assert.ok(v >= 0n && v < 1n << 64n, `out of range: ${v}`)
  }
})

test('seeds normalise from number, string and bigint', () => {
  assert.equal(seedState(7), 7n)
  assert.equal(seedState(7n), 7n)
  assert.equal(seedState('run-1'), hash64('run-1'))
  assert.notEqual(seedState('run-1'), seedState('run-2'))
})

test('derived sub-streams differ per label but are stable per (seed, label)', () => {
  assert.equal(deriveSeed('run-1', 'batch:b1'), deriveSeed('run-1', 'batch:b1'))
  assert.notEqual(deriveSeed('run-1', 'batch:b1'), deriveSeed('run-1', 'batch:b2'))
  assert.notEqual(deriveSeed('run-1', 'batch:b1'), deriveSeed('run-2', 'batch:b1'))
})

test('nextInt is in range and rejects bad bounds', () => {
  const rng = makeRng('range')
  for (let i = 0; i < 500; i++) {
    const v = rng.nextInt(7)
    assert.ok(Number.isInteger(v) && v >= 0 && v < 7, `out of range: ${v}`)
  }
  assert.throws(() => makeRng('x').nextInt(0), /positive integer/)
  assert.throws(() => makeRng('x').nextInt(2.5), /positive integer/)
})

test('shuffle is a permutation, is seed-deterministic, and moves with the seed', () => {
  const items = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h']
  const one = shuffled(items, makeRng('seed-one'))
  const oneAgain = shuffled(items, makeRng('seed-one'))
  const two = shuffled(items, makeRng('seed-two'))

  assert.deepEqual(one.slice().sort(), items.slice().sort(), 'not a permutation')
  assert.deepEqual(one, oneAgain, 'same seed produced a different order')
  assert.notDeepEqual(one, two, 'different seeds produced the same order')
})

test('shuffle does not mutate its input', () => {
  const items = ['a', 'b', 'c', 'd']
  const copy = items.slice()
  shuffled(items, makeRng('nomutate'))
  assert.deepEqual(items, copy)
})
