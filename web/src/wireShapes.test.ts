// The browser half of the wire-shape guard (doc 22 RD29, work-plan item B3).
//
// `wire-shapes.json` is CAPTURED from the Go structs — the writers — by
// `go/agentdb/wire_shapes_test.go`. It is not hand-authored and must not be
// hand-edited; the recipe to regenerate it rides in the file's own `_README`.
//
// This test asserts that every `coerceX` in this package produces EXACTLY the
// key set the engine can send. It matters because the console PUTs these
// objects whole: `coerceX` builds a fresh object from an explicit field list,
// the body helper spreads that already-lossy object, and the server assigns
// every column. So a field the engine gained and the browser never learned
// about is written back as its zero value the next time a human presses Save —
// silently, with both suites green. That is the failure this file exists to
// make loud.
//
// When it goes red, decide which side moved:
//   * engine gained a field  -> add it here (interface + coerce + draft), and
//     make sure the write path either carries it or provably cannot clear it.
//   * browser invented a key -> remove it; the engine never sends it.
// Regenerating the JSON to match the browser is NOT a fix in the first case —
// the JSON is the engine's testimony, not a shared wish list.

import { describe, expect, it } from 'vitest'

import wireShapes from './wire-shapes.json'
import { coerceProjectSettings } from './projectSettings.js'
import { coerceWorker } from './workers.js'
import { coerceSubscription } from './events.js'
import { coerceSchedule } from './schedules.js'

const shapes = wireShapes.shapes as Record<string, string[]>

/** A body carrying every key the engine can send, so the assertion is about
 *  what `coerceX` KEEPS, not about what a sparse fixture happened to include. */
function fullFixture(keys: readonly string[]): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const k of keys) out[k] = `wire:${k}`
  return out
}

const cases: Array<[string, (raw: unknown) => object]> = [
  ['ProjectSettings', (raw) => coerceProjectSettings(raw)],
  ['Worker', (raw) => coerceWorker(raw)],
  ['Subscription', (raw) => coerceSubscription(raw)],
  ['Schedule', (raw) => coerceSchedule(raw)],
]

describe('wire shapes match the engine', () => {
  it('guards every struct in the captured file, and no phantom ones', () => {
    expect(Object.keys(shapes).sort()).toEqual(cases.map(([n]) => n).sort())
  })

  for (const [name, coerce] of cases) {
    it(`coerce${name} keeps exactly the keys the engine sends`, () => {
      const keys = shapes[name]
      expect(keys, `${name} missing from wire-shapes.json`).toBeTruthy()
      expect(Object.keys(coerce(fullFixture(keys))).sort()).toEqual([...keys].sort())
    })

    it(`coerce${name} produces the same key set from an empty body`, () => {
      // The shape must not depend on what the server happened to send: a
      // controlled input bound to `undefined` is how React flips to
      // uncontrolled mid-edit, and a missing key on save is how a column is
      // zeroed.
      expect(Object.keys(coerce({})).sort()).toEqual([...shapes[name]].sort())
    })
  }
})
