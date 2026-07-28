// Shared fixture for the offline Tier B tests.
//
// The fixture is built so that scoring is exactly predictable by hand, which
// is what lets the tests assert a curve rather than "something changed".
//
// SCRIPTED_RULES award: "Title:" +4, "Signed:" +2, "Units:" +1. Every subject
// therefore has a distinct total, and — this is load-bearing — **no candidate
// ever ties with an anchor**. scriptedGrader breaks ties by presented order,
// so a candidate/anchor tie would make that pair's outcome seed-dependent and
// the seed-invariance property would fail for reasons that have nothing to do
// with the grader being biased.
//
//   anchor-plain   Units:                       = 1
//   round 0        (no markers)                 = 0   -> loses both anchors -> 0.0
//   round 1        Title:                       = 4   -> beats plain only   -> 0.5
//   anchor-solid   Title: + Signed:             = 6
//   round 2        Title: + Signed: + Units:    = 7   -> beats both         -> 1.0
//
// The candidate texts carry no provenance — a round-2 output is recognisable
// only by being a better answer, which is the point: a grader that "prefers
// later rounds" can only do so through quality, because round is not on the
// wire. grade.test.ts greps the batch payloads to prove that.

import type { CandidateSet } from './collect.ts'
import { collectCandidates } from './collect.ts'
import type { Anchor, ScriptedRule } from './grade.ts'

export const FIXTURE_TASK = 'Summarise the weekly throughput sweep for the team.'
export const FIXTURE_CRITERION =
  'Which summary is the most complete and useful to a reader who was not present?'

/** Distinctive so a leak test can grep for them. */
export const FIXTURE_WORKER = 'tierb-fixture-analyst'
export const FIXTURE_ARM = 'arm-alpha-QQ'
export const FIXTURE_PROMPT_VERSIONS = ['pv-QQ0-zero', 'pv-QQ1-one', 'pv-QQ2-two'] as const

export const SCRIPTED_RULES: ScriptedRule[] = [
  { match: 'Title:', score: 4 },
  { match: 'Signed:', score: 2 },
  { match: 'Units:', score: 1 },
]

export const FIXTURE_ANCHORS: Anchor[] = [
  {
    id: 'anchor-plain',
    text: 'The sweep completed. Units: requests per second.',
  },
  {
    id: 'anchor-solid',
    text: 'Title: Reference sweep\n\nThe sweep completed without incident.\n\nSigned: the reference analyst',
  },
]

const TEXTS: Array<{ round: number; text: string }> = [
  { round: 0, text: 'Ran the sweep. Results are attached.' },
  { round: 0, text: 'The sweep completed and the numbers are below.' },
  { round: 1, text: 'Title: Sweep results\n\nRan the sweep; the numbers are below.' },
  { round: 1, text: 'Title: Weekly sweep\n\nThe sweep completed and the numbers are below.' },
  {
    round: 2,
    text: 'Title: Sweep results\n\nThroughput rose over the week. Units: requests per second.\n\nSigned: the analyst',
  },
  {
    round: 2,
    text: 'Title: Weekly sweep\n\nLatency fell over the week. Units: milliseconds.\n\nSigned: the analyst',
  },
]

export function fixtureSet(): CandidateSet {
  return collectCandidates(
    TEXTS.map((t) => ({
      round: t.round,
      text: t.text,
      promptVersion: FIXTURE_PROMPT_VERSIONS[t.round]!,
      worker: FIXTURE_WORKER,
      arm: FIXTURE_ARM,
    })),
    { task: FIXTURE_TASK, criterion: FIXTURE_CRITERION, idPrefix: 'cand' },
  )
}

/** Every string that must never reach a grader's view. */
export function fixtureProvenanceStrings(set: CandidateSet): string[] {
  return [
    ...set.candidates.map((c) => c.id),
    ...FIXTURE_PROMPT_VERSIONS,
    FIXTURE_WORKER,
    FIXTURE_ARM,
    ...FIXTURE_ANCHORS.map((a) => a.id),
    'anchor:',
    'meta',
    'round',
    'promptVersion',
  ]
}
