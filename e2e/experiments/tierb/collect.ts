// Tier B, step 1: gather the outputs to be graded.
//
// The single job of this file is to produce a candidates file whose shape
// enforces AGENTS_RESEARCH.md §7.1: **what the grader sees is separated from
// where it came from**. A candidate is {id, text, meta}; `text` is the only
// field grade.ts is allowed to put in front of a grader, and `meta` (round,
// prompt version, worker, arm) never leaves the harness. Keeping provenance in
// a sibling object rather than inline in the text is what makes the blindness
// property testable at all — grade.test.ts greps the batch payloads for every
// meta value and every id.
//
// There are two ways in: raw strings (offline, fixtures, tests) and
// worker.finished project events (a real run). Neither performs I/O against
// the stack — a real run fetches events with e2e/helpers/api.ts and hands the
// rows here, so this module stays pure and offline-testable.

import { readFileSync, writeFileSync } from 'node:fs'
// Type-only: erased at runtime, so importing it costs the tests nothing and
// pulls in no @playwright/test dependency.
import type { ProjectEvent } from '../../helpers/api.ts'

/** Provenance. Never shown to a grader. */
export interface CandidateMeta {
  /** Which round of the loop produced this output. The x-axis of the curve. */
  round: number
  /** Identifier for the prompt version in force at the time (config-log id, hash, label). */
  promptVersion?: string
  /** Worker that produced it. */
  worker?: string
  /** Project the run lived in. */
  project?: string
  /** Experiment arm (e.g. 'A' critic-live, 'B' no-critic) — see 14-calibration-runbook.md §2. */
  arm?: string
  /** Originating project event, when collected from an event stream. */
  sourceEventId?: string
  /** Free-text note for the operator. */
  note?: string
}

export interface Candidate {
  /** Stable, unique within a set. Harness-side only. */
  id: string
  /** The output under test. The ONLY field a grader ever sees. */
  text: string
  meta: CandidateMeta
}

export interface CandidateSet {
  version: 1
  /** What the worker was asked to do. Shown to the grader as context. */
  task: string
  /** The single criterion the grader ranks by. Shown to the grader. */
  criterion: string
  candidates: Candidate[]
}

/** One gathered output, before it becomes a candidate. */
export interface RoundOutput {
  round: number
  text: string
  promptVersion?: string
  worker?: string
  project?: string
  arm?: string
  sourceEventId?: string
  note?: string
}

export interface CollectOptions {
  task: string
  criterion: string
  /** Prefix for generated ids. Ids are `<prefix>-<n>` in input order. */
  idPrefix?: string
}

/**
 * Build a candidate set from gathered round outputs.
 *
 * Ids are positional (`c-1`, `c-2`, …) rather than derived from round or
 * prompt version: a leaky id would defeat the blinding just as surely as a
 * leaky text, and grade.ts logs ids in its harness-side key.
 */
export function collectCandidates(outputs: readonly RoundOutput[], opts: CollectOptions): CandidateSet {
  if (outputs.length === 0) throw new Error('collectCandidates: no outputs given')
  const prefix = opts.idPrefix ?? 'c'
  const candidates: Candidate[] = outputs.map((o, i) => {
    const text = o.text.trim()
    if (text === '') throw new Error(`collectCandidates: output ${i} has empty text`)
    if (!Number.isInteger(o.round) || o.round < 0) {
      throw new Error(`collectCandidates: output ${i} has a non-round round (${o.round})`)
    }
    const meta: CandidateMeta = { round: o.round }
    if (o.promptVersion !== undefined) meta.promptVersion = o.promptVersion
    if (o.worker !== undefined) meta.worker = o.worker
    if (o.project !== undefined) meta.project = o.project
    if (o.arm !== undefined) meta.arm = o.arm
    if (o.sourceEventId !== undefined) meta.sourceEventId = o.sourceEventId
    if (o.note !== undefined) meta.note = o.note
    return { id: `${prefix}-${i + 1}`, text, meta }
  })
  const set: CandidateSet = {
    version: 1,
    task: opts.task,
    criterion: opts.criterion,
    candidates,
  }
  validateCandidateSet(set)
  return set
}

export function validateCandidateSet(set: CandidateSet): void {
  if (set.version !== 1) throw new Error(`unsupported candidate-set version ${set.version}`)
  if (!set.task.trim()) throw new Error('candidate set has no task')
  if (!set.criterion.trim()) throw new Error('candidate set has no criterion')
  if (!Array.isArray(set.candidates) || set.candidates.length === 0) {
    throw new Error('candidate set has no candidates')
  }
  const seen = new Set<string>()
  for (const c of set.candidates) {
    if (!c.id) throw new Error('candidate with empty id')
    if (seen.has(c.id)) throw new Error(`duplicate candidate id ${c.id}`)
    seen.add(c.id)
    if (!c.text.trim()) throw new Error(`candidate ${c.id} has empty text`)
    if (!Number.isInteger(c.meta?.round)) throw new Error(`candidate ${c.id} has no round`)
  }
}

export interface EventCollectOptions {
  /** Only events whose envelope.worker matches are taken. */
  worker: string
  /** Event type carrying the output. `worker.finished` is the only routable worker output today. */
  eventType?: string
  /**
   * Map an event to its round. Default: order of appearance (0-based), which is
   * correct when the events are the successive rounds of one loop.
   */
  roundOf?: (event: ProjectEvent, index: number) => number
  /** Map an event to a prompt-version label (e.g. from the config log). */
  promptVersionOf?: (event: ProjectEvent, index: number) => string | undefined
  /** Extract the gradeable text from the event. Default: the whole event text. */
  textOf?: (event: ProjectEvent) => string
  arm?: string
}

/**
 * Turn a project-event stream into round outputs.
 *
 * Caveat worth stating out loud: `worker.finished` event text is the actor's
 * ENTIRE transcript (13-work-plan-self-improvement.md, Standing traps), not a
 * tidy deliverable. A real run will nearly always want a `textOf` that slices
 * out the deliverable — otherwise the grader is ranking transcripts, and a
 * transcript carries provenance (worker names, tool calls) straight past the
 * blinding. `textOf` is the seam where that extraction lives.
 */
export function roundOutputsFromEvents(
  events: readonly ProjectEvent[],
  opts: EventCollectOptions,
): RoundOutput[] {
  const type = opts.eventType ?? 'worker.finished'
  const textOf = opts.textOf ?? ((e: ProjectEvent) => e.text)
  const matching = events
    .filter((e) => e.type === type && e.envelope?.worker === opts.worker)
    .slice()
    .sort((a, b) => a.occurred_at - b.occurred_at || a.id.localeCompare(b.id))
  return matching.map((e, i) => {
    const out: RoundOutput = {
      round: opts.roundOf ? opts.roundOf(e, i) : i,
      text: textOf(e),
      worker: opts.worker,
      project: e.project,
      sourceEventId: e.id,
    }
    const pv = opts.promptVersionOf?.(e, i)
    if (pv !== undefined) out.promptVersion = pv
    if (opts.arm !== undefined) out.arm = opts.arm
    return out
  })
}

export function writeCandidatesFile(path: string, set: CandidateSet): void {
  validateCandidateSet(set)
  writeFileSync(path, `${JSON.stringify(set, null, 2)}\n`, 'utf8')
}

export function readCandidatesFile(path: string): CandidateSet {
  const set = JSON.parse(readFileSync(path, 'utf8')) as CandidateSet
  validateCandidateSet(set)
  return set
}
