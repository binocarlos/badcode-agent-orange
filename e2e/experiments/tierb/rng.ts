// Seeded RNG for the Tier B graded harness.
//
// Why not Math.random or a library: the shuffle in grade.ts is a *tested
// property* — "same seed, same presented order" is asserted, and the whole
// order-bias detection rests on two seeds producing demonstrably different
// orders. A generator we do not own can change between runtimes or releases
// and silently turn those assertions into noise. The Go side already learned
// this (13-work-plan-self-improvement.md, L1: "math/rand is not a determinism
// guarantee across Go releases — hypolab carries its own splitmix64 with
// golden-byte tests"); this is the TypeScript twin of that decision, with the
// same golden values pinned in rng.test.ts.
//
// splitmix64 is used rather than a fancier generator because it is four lines,
// has no seeding ritual, and its reference outputs are widely published — so a
// golden test actually proves we implemented the published algorithm.

const MASK = (1n << 64n) - 1n
const GOLDEN_GAMMA = 0x9e3779b97f4a7c15n
const MIX_A = 0xbf58476d1ce4e5b9n
const MIX_B = 0x94d049bb133111ebn
const TWO_64 = 1n << 64n

/** A seed may be given as a number, a string label, or a raw 64-bit value. */
export type Seed = number | string | bigint

/** The reference splitmix64 generator. Returns successive 64-bit values. */
export function splitmix64(seed: bigint): () => bigint {
  let state = seed & MASK
  return () => {
    state = (state + GOLDEN_GAMMA) & MASK
    let z = state
    z = ((z ^ (z >> 30n)) * MIX_A) & MASK
    z = ((z ^ (z >> 27n)) * MIX_B) & MASK
    return (z ^ (z >> 31n)) & MASK
  }
}

const FNV_OFFSET = 14695981039346656037n
const FNV_PRIME = 1099511628211n

/** FNV-1a over the UTF-8 bytes of `text`, as a 64-bit value. */
export function hash64(text: string): bigint {
  let h = FNV_OFFSET
  for (const byte of new TextEncoder().encode(text)) {
    h = ((h ^ BigInt(byte)) * FNV_PRIME) & MASK
  }
  return h
}

/** Normalise any accepted seed form to a 64-bit state value. */
export function seedState(seed: Seed): bigint {
  if (typeof seed === 'bigint') return seed & MASK
  if (typeof seed === 'number') {
    if (!Number.isFinite(seed)) throw new Error(`seed must be finite, got ${seed}`)
    return BigInt(Math.trunc(seed)) & MASK
  }
  return hash64(seed)
}

/**
 * Derive an independent sub-stream from a base seed and a label. Batches use
 * this so that batch N's presentation order does not depend on how many items
 * batch N-1 happened to contain.
 */
export function deriveSeed(seed: Seed, label: string): bigint {
  return hash64(`${seedState(seed).toString(16)}|${label}`)
}

export interface Rng {
  /** Next raw 64-bit value. */
  nextU64(): bigint
  /** Uniform integer in [0, bound). Rejection-sampled, so unbiased. */
  nextInt(bound: number): number
}

export function makeRng(seed: Seed): Rng {
  const next = splitmix64(seedState(seed))
  return {
    nextU64: next,
    nextInt(bound: number): number {
      if (!Number.isInteger(bound) || bound <= 0) {
        throw new Error(`nextInt bound must be a positive integer, got ${bound}`)
      }
      const b = BigInt(bound)
      // Reject the ragged tail so every value in [0, bound) is equally likely.
      const limit = TWO_64 - (TWO_64 % b)
      for (;;) {
        const v = next()
        if (v < limit) return Number(v % b)
      }
    },
  }
}

/** Fisher-Yates, descending. Returns a new array; `items` is untouched. */
export function shuffled<T>(items: readonly T[], rng: Rng): T[] {
  const out = items.slice()
  for (let i = out.length - 1; i > 0; i--) {
    const j = rng.nextInt(i + 1)
    const tmp = out[i]!
    out[i] = out[j]!
    out[j] = tmp
  }
  return out
}
