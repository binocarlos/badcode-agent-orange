// Tier B, step 3: rankings -> anchor-relative scores -> a per-round curve.
//
// THE MATH, in full, because a scorer nobody can re-derive is a scorer nobody
// should trust:
//
// 1. A ranking is a strict total order over the subjects in one batch. Expand
//    it into pairwise outcomes: subject a beats subject b iff a is ranked
//    ahead of b in a batch that contained both.
//
// 2. Only comparisons **against anchors** are counted. For a candidate c:
//        wins(c)        = # anchors c was ranked ahead of, summed over batches
//        comparisons(c) = # (batch, anchor) pairs c appeared with
//        score(c)       = wins(c) / comparisons(c)          in [0, 1]
//    Candidate-vs-candidate outcomes are discarded on purpose. Every batch
//    carries every anchor (§7.3), so every candidate is measured against the
//    same fixed yardstick — which is what puts separate batches, and separate
//    runs, on one scale. Batch-internal candidate comparisons would not.
//
// 3. Anchors are scored the same way against the OTHER anchors. Their scores
//    are the scale reference: with a fixed anchor set and an honest grader
//    they are constants, independent of the candidates, the seed, and the run.
//    An anchor score that moves between runs means the instrument moved, not
//    the loop — that is the pinned property, and it is why at least two
//    anchors are required (one anchor has nothing to be compared against).
//
// 4. Elo-style restatement, for readers who want a scale with room above and
//    below the anchors:
//        p    = (wins + 0.5) / (comparisons + 1)     Laplace-corrected
//        elo  = 400 * log10(p / (1 - p))
//    The correction only exists to keep a clean sweep (p = 0 or 1) finite; a
//    swept subject gets a large finite number instead of +/-Infinity. Anchors
//    sit near 0 by construction, so elo reads as "distance from the anchor
//    scale". It is a monotone restatement of `score`, not new evidence.
//
//    CAVEAT, pinned by test: the correction shrinks toward 0 as `comparisons`
//    falls, so elo is comparable ACROSS runs only when the comparison counts
//    match. The cross-run scale check — "did the instrument move?" — is the
//    anchors' `score`, which is a plain ratio and carries no such dependence.
//
// 5. Per round: pool the round's members' wins and comparisons (identical to
//    the mean of member scores when members have equal comparison counts, and
//    better behaved when they do not), and report the population standard
//    deviation of member scores as `spread`. Spread is the honesty column —
//    a rising line with overlapping spreads is not a result.
//
// Nothing here is a pass/fail. AGENTS_RESEARCH §7: Tier B is "a number with
// variance, not a verdict", recorded as a curve and NEVER wired as a CI gate.

import type { CandidateSet } from './collect.ts'
import type { Anchor, PreparedBatch, ResolvedRanking } from './grade.ts'
import { ANCHOR_PREFIX, anchorSubjectId, isAnchorSubject } from './grade.ts'

export interface SubjectScore {
  id: string
  wins: number
  comparisons: number
  /** wins / comparisons, in [0, 1]. */
  score: number
  /** 400 * log10(p/(1-p)) with Laplace-corrected p. Anchors sit near 0. */
  elo: number
}

export interface RoundScore {
  round: number
  /** Candidates in this round. */
  n: number
  score: number
  /** Population stddev of the round's per-candidate scores. */
  spread: number
  min: number
  max: number
  elo: number
  comparisons: number
}

export interface CandidateScore extends SubjectScore {
  round: number
  promptVersion?: string
  arm?: string
}

export interface CurveReport {
  version: 1
  task: string
  criterion: string
  /** Recorded so a run can be reproduced. */
  seed: string
  batchCount: number
  anchorIds: string[]
  /** The scale reference. Compare these across runs before comparing curves. */
  anchors: SubjectScore[]
  rounds: RoundScore[]
  candidates: CandidateScore[]
}

export interface ScoreInput {
  set: CandidateSet
  anchors: readonly Anchor[]
  batches: readonly PreparedBatch[]
  rankings: readonly ResolvedRanking[]
  /** Recorded in the report, for reproducibility. */
  seed?: string
}

/** Six decimal places: enough resolution, no float-associativity noise. */
function round6(x: number): number {
  return Math.round(x * 1e6) / 1e6
}

function eloOf(wins: number, comparisons: number): number {
  if (comparisons === 0) return 0
  const p = (wins + 0.5) / (comparisons + 1)
  return round6(400 * Math.log10(p / (1 - p)))
}

export function scoreRun(input: ScoreInput): CurveReport {
  const { set, anchors, batches, rankings } = input
  if (batches.length !== rankings.length) {
    throw new Error(`have ${batches.length} batches but ${rankings.length} rankings`)
  }
  const anchorSubjects = anchors.map((a) => anchorSubjectId(a.id))
  const anchorSet = new Set(anchorSubjects)

  const wins = new Map<string, number>()
  const comparisons = new Map<string, number>()
  const bump = (map: Map<string, number>, key: string, by = 1): void => {
    map.set(key, (map.get(key) ?? 0) + by)
  }

  for (const [i, prepared] of batches.entries()) {
    const ranking = rankings[i]!
    if (ranking.batchId !== prepared.batch.batchId) {
      throw new Error(`ranking ${i} is for ${ranking.batchId}, expected ${prepared.batch.batchId}`)
    }
    const rank = new Map<string, number>()
    ranking.order.forEach((subjectId, position) => rank.set(subjectId, position))
    const present = ranking.order
    for (const subject of present) {
      for (const anchor of present) {
        if (!anchorSet.has(anchor)) continue
        if (subject === anchor) continue
        bump(comparisons, subject)
        if (rank.get(subject)! < rank.get(anchor)!) bump(wins, subject)
      }
    }
  }

  const subjectScore = (id: string): SubjectScore => {
    const w = wins.get(id) ?? 0
    const c = comparisons.get(id) ?? 0
    return { id, wins: w, comparisons: c, score: c === 0 ? 0 : round6(w / c), elo: eloOf(w, c) }
  }

  const anchorScores = anchorSubjects.map((id) => ({
    ...subjectScore(id),
    id: id.slice(ANCHOR_PREFIX.length),
  }))

  // Sorted by id so accumulation order (and therefore the emitted bytes) does
  // not depend on the seed or on batch layout.
  const ordered = set.candidates.slice().sort((a, b) => a.id.localeCompare(b.id))
  const candidateScores: CandidateScore[] = ordered.map((c) => {
    const base = subjectScore(c.id)
    const out: CandidateScore = { ...base, round: c.meta.round }
    if (c.meta.promptVersion !== undefined) out.promptVersion = c.meta.promptVersion
    if (c.meta.arm !== undefined) out.arm = c.meta.arm
    return out
  })

  const byRound = new Map<number, CandidateScore[]>()
  for (const c of candidateScores) {
    const bucket = byRound.get(c.round)
    if (bucket) bucket.push(c)
    else byRound.set(c.round, [c])
  }

  const rounds: RoundScore[] = [...byRound.keys()]
    .sort((a, b) => a - b)
    .map((round) => {
      const members = byRound.get(round)!
      const totalWins = members.reduce((s, m) => s + m.wins, 0)
      const totalComparisons = members.reduce((s, m) => s + m.comparisons, 0)
      const scores = members.map((m) => m.score)
      const mean = scores.reduce((s, v) => s + v, 0) / scores.length
      const variance = scores.reduce((s, v) => s + (v - mean) ** 2, 0) / scores.length
      return {
        round,
        n: members.length,
        score: totalComparisons === 0 ? 0 : round6(totalWins / totalComparisons),
        spread: round6(Math.sqrt(variance)),
        min: round6(Math.min(...scores)),
        max: round6(Math.max(...scores)),
        elo: eloOf(totalWins, totalComparisons),
        comparisons: totalComparisons,
      }
    })

  return {
    version: 1,
    task: set.task,
    criterion: set.criterion,
    seed: input.seed ?? '',
    batchCount: batches.length,
    anchorIds: anchors.map((a) => a.id),
    anchors: anchorScores,
    rounds,
    candidates: candidateScores,
  }
}

function pad(text: string, width: number): string {
  return text.length >= width ? text : text + ' '.repeat(width - text.length)
}

function padLeft(text: string, width: number): string {
  return text.length >= width ? text : ' '.repeat(width - text.length) + text
}

/** A small human-readable table. Deliberately carries no timestamp. */
export function formatCurve(report: CurveReport): string {
  const lines: string[] = []
  lines.push(`Tier B curve — seed ${report.seed || '(unrecorded)'}, ${report.batchCount} batches`)
  lines.push(`criterion: ${report.criterion}`)
  lines.push('')
  lines.push(
    [pad('round', 7), padLeft('n', 3), padLeft('score', 8), padLeft('spread', 8), padLeft('min', 6), padLeft('max', 6), padLeft('elo', 8), padLeft('cmp', 5)].join(' '),
  )
  for (const r of report.rounds) {
    lines.push(
      [
        pad(String(r.round), 7),
        padLeft(String(r.n), 3),
        padLeft(r.score.toFixed(3), 8),
        padLeft(r.spread.toFixed(3), 8),
        padLeft(r.min.toFixed(2), 6),
        padLeft(r.max.toFixed(2), 6),
        padLeft(r.elo.toFixed(1), 8),
        padLeft(String(r.comparisons), 5),
      ].join(' '),
    )
  }
  lines.push('')
  lines.push('anchors (the scale — must not move between runs):')
  for (const a of report.anchors) {
    lines.push(
      `  ${pad(a.id, 20)} ${padLeft(a.score.toFixed(3), 8)} ${padLeft(a.elo.toFixed(1), 8)}  (${a.wins}/${a.comparisons})`,
    )
  }
  lines.push('')
  lines.push('Not a gate. A curve with variance — read spread before reading the trend.')
  return lines.join('\n')
}

export { isAnchorSubject }
