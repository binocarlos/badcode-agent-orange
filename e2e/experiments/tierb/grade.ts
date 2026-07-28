// Tier B, step 2: build blind grading batches and put them in front of a grader.
//
// This file implements AGENTS_RESEARCH.md §7's grading protocol literally:
//
//   §7.1 Blind and shuffled — a GradingBatch contains ONLY {label, text}; the
//        label -> subject mapping is kept harness-side in PreparedBatch.key,
//        and presentation order is a seeded shuffle. Nothing in the payload
//        says which round or prompt version produced an item.
//   §7.2 Rank, don't score — the seam's return type is an ordering of labels.
//        There is no place to put a 0-10 score, deliberately.
//   §7.3 Fixed anchors in every batch — every batch carries all anchors, so
//        separate batches (and separate runs) share a scale. score.ts turns
//        that into the anchor-relative metric.
//   §7.4 The grader is not the model under test — enforced by the operator
//        choosing a different model for anthropicGrader; the code cannot know
//        which model produced the candidates, so the README states the rule
//        and the run record must record both model ids.
//
// **What is seeded and what is not.** Batch *membership* is deterministic (a
// round-robin partition of the candidates in file order — which interleaves
// rounds without consulting the rng). Only the *presentation order within a
// batch* is seeded. That split is what makes the anchor-relative score
// seed-invariant for an order-independent grader, and therefore what makes a
// score that DOES move between seeds a positive detection of order bias. If
// membership were seeded too, an honest grader's scores would wobble between
// seeds and the invariance test could not tell bias from partitioning noise.

import type { Candidate, CandidateSet } from './collect.ts'
import { validateCandidateSet } from './collect.ts'
import type { Seed } from './rng.ts'
import { deriveSeed, makeRng, shuffled } from './rng.ts'

/** A fixed reference output included in every batch. §7.3. */
export interface Anchor {
  id: string
  text: string
}

/** One presented item. This is all a grader ever sees of a candidate. */
export interface GradingItem {
  label: string
  text: string
}

/** The payload handed to the grader seam. Carries no provenance. */
export interface GradingBatch {
  batchId: string
  task: string
  criterion: string
  items: GradingItem[]
}

/** A grader's answer: presented labels, best first, no ties, no omissions. */
export interface Ranking {
  batchId: string
  order: string[]
}

/** The pluggable grader. Sync or async. */
export type GraderSeam = (batch: GradingBatch) => Ranking | Promise<Ranking>

/**
 * A batch plus the harness-side key. `key` maps a presented label to a subject
 * id: a candidate id, or `anchor:<anchorId>` for an anchor.
 */
export interface PreparedBatch {
  batch: GradingBatch
  key: Record<string, string>
}

/** A ranking with labels resolved back to subject ids. */
export interface ResolvedRanking {
  batchId: string
  /** Subject ids, best first. */
  order: string[]
}

export interface GradeConfig {
  /** Fixed anchor items, included in every batch. At least two — see score.ts. */
  anchors: Anchor[]
  grader: GraderSeam
  seed: Seed
  /** Candidates per batch, before anchors are added. Default 4. */
  batchSize?: number
  /** Override the candidate set's criterion / task text. */
  criterion?: string
  task?: string
}

export const ANCHOR_PREFIX = 'anchor:'

export function anchorSubjectId(anchorId: string): string {
  return `${ANCHOR_PREFIX}${anchorId}`
}

export function isAnchorSubject(subjectId: string): boolean {
  return subjectId.startsWith(ANCHOR_PREFIX)
}

/** Presented labels: A..Z, then AA, AB, … Positional only — never provenance. */
export function labelAt(index: number): string {
  let n = index
  let label = ''
  do {
    label = String.fromCharCode(65 + (n % 26)) + label
    n = Math.floor(n / 26) - 1
  } while (n >= 0)
  return label
}

function validateAnchors(anchors: readonly Anchor[]): void {
  if (anchors.length < 2) {
    // With a single anchor there is nothing to measure the anchor itself
    // against, so the scale reference has no value of its own and anchor
    // invariance becomes unobservable. §7.3 says "two or three".
    throw new Error('Tier B needs at least two anchors (AGENTS_RESEARCH §7.3)')
  }
  const seen = new Set<string>()
  for (const a of anchors) {
    if (!a.id) throw new Error('anchor with empty id')
    if (seen.has(a.id)) throw new Error(`duplicate anchor id ${a.id}`)
    seen.add(a.id)
    if (!a.text.trim()) throw new Error(`anchor ${a.id} has empty text`)
  }
}

/**
 * Deterministic round-robin partition into `groups` groups. Deliberately not
 * seeded — see the header note.
 */
function partition<T>(items: readonly T[], groups: number): T[][] {
  const out: T[][] = Array.from({ length: groups }, () => [])
  items.forEach((item, i) => {
    out[i % groups]!.push(item)
  })
  return out
}

export function buildBatches(set: CandidateSet, config: GradeConfig): PreparedBatch[] {
  validateCandidateSet(set)
  validateAnchors(config.anchors)
  const batchSize = config.batchSize ?? 4
  if (!Number.isInteger(batchSize) || batchSize < 1) {
    throw new Error(`batchSize must be a positive integer, got ${batchSize}`)
  }
  const anchorIds = new Set(config.anchors.map((a) => a.id))
  for (const c of set.candidates) {
    if (anchorIds.has(c.id)) throw new Error(`candidate id ${c.id} collides with an anchor id`)
  }

  const groups = partition(set.candidates, Math.ceil(set.candidates.length / batchSize))
  const task = config.task ?? set.task
  const criterion = config.criterion ?? set.criterion

  return groups.map((group, i) => {
    const batchId = `b${i + 1}`
    const subjects: Array<{ subjectId: string; text: string }> = [
      ...group.map((c: Candidate) => ({ subjectId: c.id, text: c.text })),
      ...config.anchors.map((a) => ({ subjectId: anchorSubjectId(a.id), text: a.text })),
    ]
    // Seeded per batch, so batch 2's order does not depend on batch 1's size.
    const rng = makeRng(deriveSeed(config.seed, `batch:${batchId}`))
    const presented = shuffled(subjects, rng)
    const items: GradingItem[] = []
    const key: Record<string, string> = {}
    presented.forEach((s, idx) => {
      const label = labelAt(idx)
      items.push({ label, text: s.text })
      key[label] = s.subjectId
    })
    return { batch: { batchId, task, criterion, items }, key }
  })
}

/** Check a grader's answer is a strict permutation of the presented labels. */
export function resolveRanking(prepared: PreparedBatch, ranking: Ranking): ResolvedRanking {
  const { batch, key } = prepared
  if (ranking.batchId !== batch.batchId) {
    throw new Error(`ranking is for batch ${ranking.batchId}, expected ${batch.batchId}`)
  }
  const expected = batch.items.map((i) => i.label)
  const seen = new Set<string>()
  for (const label of ranking.order) {
    if (!(label in key)) throw new Error(`batch ${batch.batchId}: unknown label ${label}`)
    if (seen.has(label)) throw new Error(`batch ${batch.batchId}: duplicate label ${label}`)
    seen.add(label)
  }
  if (seen.size !== expected.length) {
    const missing = expected.filter((l) => !seen.has(l))
    throw new Error(`batch ${batch.batchId}: ranking omits ${missing.join(', ')}`)
  }
  return { batchId: batch.batchId, order: ranking.order.map((l) => key[l]!) }
}

export interface GradeResult {
  batches: PreparedBatch[]
  rankings: ResolvedRanking[]
}

export async function runGrading(set: CandidateSet, config: GradeConfig): Promise<GradeResult> {
  const batches = buildBatches(set, config)
  const rankings: ResolvedRanking[] = []
  for (const prepared of batches) {
    const ranking = await config.grader(prepared.batch)
    rankings.push(resolveRanking(prepared, ranking))
  }
  return { batches, rankings }
}

// ── scriptedGrader: the offline, table-driven grader ────────────────────────

export interface ScriptedRule {
  /** Substring searched for in the item text. */
  match: string
  /** Added to the item's score when present. */
  score: number
}

export interface ScriptedGraderOptions {
  rules?: ScriptedRule[]
  /** Score every item starts with. Default 0. */
  base?: number
  /** Exact-text score overrides, added on top of rule hits. */
  scores?: Record<string, number>
  /**
   * Rigging knob for the invariance test: adds `positionBias` per position
   * from the FRONT of the presented order. A non-zero value makes the grader
   * prefer whatever the shuffle happened to show first — exactly the pathology
   * §7.1's shuffle exists to expose.
   */
  positionBias?: number
}

/**
 * A deterministic grader driven by a substring table. Ranks by score
 * descending; ties break by presented order, so a fixture that must be
 * seed-stable has to avoid candidate/anchor score ties (fixtures.ts does).
 */
export function scriptedGrader(opts: ScriptedGraderOptions = {}): GraderSeam {
  const rules = opts.rules ?? []
  const base = opts.base ?? 0
  const scores = opts.scores ?? {}
  const positionBias = opts.positionBias ?? 0
  return (batch: GradingBatch): Ranking => {
    const scored = batch.items.map((item, index) => {
      let score = base + (scores[item.text] ?? 0)
      for (const rule of rules) {
        if (item.text.includes(rule.match)) score += rule.score
      }
      score += positionBias * (batch.items.length - 1 - index)
      return { label: item.label, index, score }
    })
    scored.sort((a, b) => b.score - a.score || a.index - b.index)
    return { batchId: batch.batchId, order: scored.map((s) => s.label) }
  }
}

// ── anthropicGrader: the real-model grader (BUILT, execution gated) ─────────

export interface AnthropicGraderOptions {
  /** Defaults to process.env.ANTHROPIC_API_KEY. */
  apiKey?: string
  /**
   * MUST be a different model than the one that produced the candidates
   * (§7.4). The harness cannot verify that; the run record must state both.
   */
  model?: string
  maxTokens?: number
  baseUrl?: string
  /** Injectable for a future smoke test. Defaults to global fetch. */
  fetchImpl?: typeof fetch
}

export const DEFAULT_GRADER_MODEL = 'claude-opus-5'
const ANTHROPIC_VERSION = '2023-06-01'

export const GRADER_SYSTEM_PROMPT = [
  'You are grading anonymised candidate outputs for a research harness.',
  'You will be shown several labelled items that all respond to the same task.',
  'Rank them against the stated criterion and nothing else.',
  'You have no information about who or what produced any item, and none is available; do not speculate.',
  'Produce a strict total order: every label exactly once, best first, no ties.',
  'Reply with ONLY a JSON array of the labels, e.g. ["B","A","C"]. No prose, no code fence, no explanation.',
].join(' ')

/** Build the user message for a batch. Exported so a test can inspect it. */
export function buildGraderPrompt(batch: GradingBatch): string {
  const items = batch.items
    .map((i) => `<item label="${i.label}">\n${i.text}\n</item>`)
    .join('\n\n')
  return [
    `Task the items respond to:\n${batch.task}`,
    '',
    `Criterion to rank by:\n${batch.criterion}`,
    '',
    `Items (${batch.items.length}):`,
    items,
    '',
    `Return a JSON array ranking all ${batch.items.length} labels, best first.`,
  ].join('\n')
}

/**
 * Parse a model reply into a ranking. Defensive on purpose: a grader that
 * returns prose around its JSON, a code fence, or lowercase labels should not
 * cost a run, but a grader that omits or invents a label MUST fail loudly —
 * silently patching a partial ranking would fabricate comparisons, and this
 * whole instrument exists to avoid fabricated signal.
 */
export function parseRankingResponse(raw: string, labels: readonly string[]): string[] {
  const known = new Map(labels.map((l) => [l.toUpperCase(), l]))
  const cleaned = raw.replace(/```[a-zA-Z]*\n?/g, '').replace(/```/g, '')

  const fromArray = (): string[] | null => {
    for (const match of cleaned.matchAll(/\[[^[\]]*\]/g)) {
      let parsed: unknown
      try {
        parsed = JSON.parse(match[0])
      } catch {
        continue
      }
      if (!Array.isArray(parsed)) continue
      const out: string[] = []
      const seen = new Set<string>()
      for (const entry of parsed) {
        if (typeof entry !== 'string') continue
        const label = known.get(entry.trim().toUpperCase())
        if (label === undefined || seen.has(label)) continue
        seen.add(label)
        out.push(label)
      }
      if (out.length === labels.length) return out
    }
    return null
  }

  const fromMentions = (): string[] | null => {
    const hits: Array<{ at: number; label: string }> = []
    for (const label of labels) {
      const re = new RegExp(`(?<![A-Za-z])${label}(?![A-Za-z])`, 'i')
      const at = cleaned.search(re)
      if (at >= 0) hits.push({ at, label })
    }
    if (hits.length !== labels.length) return null
    hits.sort((a, b) => a.at - b.at)
    return hits.map((h) => h.label)
  }

  const order = fromArray() ?? fromMentions()
  if (order === null) {
    throw new Error(
      `grader reply is not a strict ranking of [${labels.join(', ')}]: ${JSON.stringify(raw.slice(0, 400))}`,
    )
  }
  return order
}

/**
 * The real-model grader. BUILT but not exercised by the offline suite and not
 * to be run without the L3 gate (Kai's explicit go on credential mode and
 * token ceiling — 13-work-plan-self-improvement.md, Wave 4).
 */
export function anthropicGrader(opts: AnthropicGraderOptions = {}): GraderSeam {
  const model = opts.model ?? DEFAULT_GRADER_MODEL
  const maxTokens = opts.maxTokens ?? 4096
  const baseUrl = (opts.baseUrl ?? 'https://api.anthropic.com').replace(/\/$/, '')
  return async (batch: GradingBatch): Promise<Ranking> => {
    const apiKey = opts.apiKey ?? process.env.ANTHROPIC_API_KEY
    if (!apiKey) throw new Error('anthropicGrader: ANTHROPIC_API_KEY is not set')
    const doFetch = opts.fetchImpl ?? fetch
    const res = await doFetch(`${baseUrl}/v1/messages`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        'x-api-key': apiKey,
        'anthropic-version': ANTHROPIC_VERSION,
      },
      body: JSON.stringify({
        model,
        max_tokens: maxTokens,
        system: GRADER_SYSTEM_PROMPT,
        messages: [{ role: 'user', content: buildGraderPrompt(batch) }],
      }),
    })
    if (!res.ok) {
      const body = await res.text().catch(() => '')
      throw new Error(`anthropicGrader: HTTP ${res.status} ${res.statusText} ${body.slice(0, 400)}`)
    }
    const payload = (await res.json()) as {
      stop_reason?: string
      content?: Array<{ type: string; text?: string }>
    }
    if (payload.stop_reason === 'refusal') {
      throw new Error(`anthropicGrader: grader model refused batch ${batch.batchId}`)
    }
    const text = (payload.content ?? [])
      .filter((b) => b.type === 'text' && typeof b.text === 'string')
      .map((b) => b.text as string)
      .join('\n')
      .trim()
    if (!text) throw new Error(`anthropicGrader: empty reply for batch ${batch.batchId}`)
    const labels = batch.items.map((i) => i.label)
    return { batchId: batch.batchId, order: parseRankingResponse(text, labels) }
  }
}
