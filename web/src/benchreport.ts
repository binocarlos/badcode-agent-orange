// parseBenchReport — the comparison rig's `report.json`, read into the shape a
// screen can render (operator-console design §7.3, work-plan BR1).
//
// The rig lives in `e2e/experiments/` and runs outside the product; nothing
// here imports across that boundary. A report arrives as a dropped file, is
// parsed, and is displayed. There is no fetch, no React and no state in this
// module.
//
// Editorial rules are carried in the DATA, not left to the view, because the
// whole point of §7.3 is that a careless table lies:
//
//   - `prompt_writes` is churn, never an outcome. It is returned in its own
//     field and excluded from `metricColumns`, so a view cannot accidentally
//     sort by it or put it first.
//   - the outcome metric is whatever `task.rankBy` names — it leads.
//   - spread travels with every mean; a metric is never reduced to one number.
//   - rewrites are counted twice: total, and distinct after deduping identical
//     writes, because `SetWorkerPrompt` has no no-op short-circuit and an
//     identical re-fire is logged again ("47 rewrites" may be "9 distinct").

/** One metric's summary across an arm's repetitions, as the rig emits it. */
export interface BenchMetric {
  n: number
  mean: number
  min: number
  max: number
  spread: number
  stdev: number
}

/** One prompt rewrite recorded inside a round. */
export interface BenchPromptWrite {
  by: string
  target: string
  rationale: string
}

/** One round of one repetition. */
export interface BenchRound {
  round: number
  taskText: string
  deliveryStatuses: string[]
  promptWrites: BenchPromptWrite[]
  output: string
  workerPromptAfter: string
  properties: Record<string, boolean>
}

/** One repetition of one arm. */
export interface BenchRun {
  arm: string
  repetition: number
  rounds: BenchRound[]
}

/** The task the rig ran through every arm. */
export interface BenchTask {
  id: string
  description: string
  mockScript: string
  rounds: number
  repetitions: number
  /** The metric key the ranking is by — the outcome column. */
  rankBy: string
  rankDirection: string
}

/** One org chart under test. */
export interface BenchArm {
  id: string
  topology: string
  eventType: string
  primaryWorker: string
  answers: Record<string, string>
}

/** A property predicate the rig evaluated per round. */
export interface BenchProperty {
  id: string
  describe: string
}

/** One row of the ranked table: ranking joined to its arm and its metrics. */
export interface BenchRow {
  rank: number
  arm: string
  topology: string
  primaryWorker: string
  reps: number
  /** The rank metric's mean/spread, as ranked. */
  mean: number
  spread: number
  metrics: Record<string, BenchMetric>
  /** `prompt_writes` — churn. Null when the rig did not record it. */
  churn: BenchMetric | null
  /** Prompt rewrites counted across every repetition of this arm. */
  rewrites: number
  /** …after deduping identical writes (same author, target, rationale, text). */
  distinctRewrites: number
}

/** A parsed report, ready to render. */
export interface BenchReport {
  schema: string
  task: BenchTask
  arms: BenchArm[]
  properties: BenchProperty[]
  rows: BenchRow[]
  runs: BenchRun[]
  /** The rig's own fixed-width table, kept verbatim for copy-out. */
  table: string
  /** Metric keys for the table's columns: the outcome first, churn never in
   *  here at all (it has its own field and its own muted column). */
  metricColumns: string[]
  /** True when any metric's spread is non-zero — in mock that is an alarm. */
  hasSpread: boolean
}

/** The churn metric's key in the rig's output. Never an outcome. */
export const CHURN_METRIC = 'prompt_writes'

const isObj = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)

const num = (v: unknown, fallback = 0): number =>
  typeof v === 'number' && Number.isFinite(v) ? v : fallback
const str = (v: unknown, fallback = ''): string => (typeof v === 'string' ? v : fallback)
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : [])

const coerceMetric = (raw: unknown): BenchMetric => {
  const o = isObj(raw) ? raw : {}
  return {
    n: num(o.n),
    mean: num(o.mean),
    min: num(o.min),
    max: num(o.max),
    spread: num(o.spread),
    stdev: num(o.stdev),
  }
}

const coerceMetrics = (raw: unknown): Record<string, BenchMetric> => {
  const out: Record<string, BenchMetric> = {}
  if (isObj(raw)) for (const [k, v] of Object.entries(raw)) out[k] = coerceMetric(v)
  return out
}

const coercePromptWrite = (raw: unknown): BenchPromptWrite => {
  const o = isObj(raw) ? raw : {}
  return { by: str(o.by), target: str(o.target), rationale: str(o.rationale) }
}

const coerceRound = (raw: unknown): BenchRound => {
  const o = isObj(raw) ? raw : {}
  const properties: Record<string, boolean> = {}
  if (isObj(o.properties)) {
    for (const [k, v] of Object.entries(o.properties)) properties[k] = v === true
  }
  return {
    round: num(o.round),
    taskText: str(o.taskText),
    deliveryStatuses: arr(o.deliveryStatuses).map((s) => str(s)),
    promptWrites: arr(o.promptWrites).map((w) => coercePromptWrite(w)),
    output: str(o.output),
    workerPromptAfter: str(o.workerPromptAfter),
    properties,
  }
}

const coerceRun = (raw: unknown): BenchRun => {
  const o = isObj(raw) ? raw : {}
  return {
    arm: str(o.arm),
    repetition: num(o.repetition),
    rounds: arr(o.rounds).map((r) => coerceRound(r)),
  }
}

const coerceArm = (raw: unknown): BenchArm => {
  const o = isObj(raw) ? raw : {}
  const answers: Record<string, string> = {}
  if (isObj(o.answers)) for (const [k, v] of Object.entries(o.answers)) answers[k] = str(v)
  return {
    id: str(o.id),
    topology: str(o.topology),
    eventType: str(o.eventType),
    primaryWorker: str(o.primaryWorker),
    answers,
  }
}

/**
 * The column order §7.3 demands: the ranked outcome first, then the remaining
 * property predicates alphabetically, then the operational metrics — and never
 * `prompt_writes`, which is churn and lives in its own field.
 */
const orderColumns = (keys: string[], rankBy: string): string[] => {
  const rest = keys.filter((k) => k !== rankBy && k !== CHURN_METRIC)
  const props = rest.filter((k) => k.startsWith('prop:')).sort()
  const ops = rest.filter((k) => !k.startsWith('prop:')).sort()
  const lead = keys.includes(rankBy) ? [rankBy] : []
  return [...lead, ...props, ...ops]
}

const writeKey = (w: BenchPromptWrite, promptAfter: string): string =>
  JSON.stringify([w.by, w.target, w.rationale, promptAfter])

/**
 * Read a comparison-rig `report.json` (schema
 * `agent-orange/experiments/compare-report@1`).
 *
 * Accepts either the parsed object or the raw JSON text. Throws an `Error`
 * whose message is meant to be shown to the operator when the input is not a
 * report — a viewer that silently rendered an empty table would be worse.
 */
export function parseBenchReport(input: unknown): BenchReport {
  let raw: unknown = input
  if (typeof raw === 'string') {
    try {
      raw = JSON.parse(raw)
    } catch {
      throw new Error('That file is not JSON.')
    }
  }
  if (!isObj(raw)) throw new Error('That file is not a comparison report.')

  const schema = str(raw.schema)
  if (!schema.startsWith('agent-orange/experiments/compare-report')) {
    throw new Error(
      `That file is not a comparison report — expected schema "agent-orange/experiments/compare-report@1", found ${
        schema === '' ? 'none' : `"${schema}"`
      }.`,
    )
  }

  const taskRaw = isObj(raw.task) ? raw.task : {}
  const task: BenchTask = {
    id: str(taskRaw.id),
    description: str(taskRaw.description),
    mockScript: str(taskRaw.mockScript),
    rounds: num(taskRaw.rounds),
    repetitions: num(taskRaw.repetitions),
    rankBy: str(taskRaw.rankBy),
    rankDirection: str(taskRaw.rankDirection, 'desc'),
  }

  const arms = arr(raw.arms).map((a) => coerceArm(a))
  const armById = new Map(arms.map((a) => [a.id, a]))
  const properties: BenchProperty[] = arr(raw.properties).map((p) => {
    const o = isObj(p) ? p : {}
    return { id: str(o.id), describe: str(o.describe) }
  })
  const runs = arr(raw.runs).map((r) => coerceRun(r))

  const summaries = new Map<string, { reps: number; metrics: Record<string, BenchMetric> }>()
  for (const s of arr(raw.summaries)) {
    const o = isObj(s) ? s : {}
    summaries.set(str(o.arm), { reps: num(o.reps), metrics: coerceMetrics(o.metrics) })
  }

  // Rewrites, counted from the runs rather than trusted from a metric: the
  // distinct count has no metric at all.
  const totals = new Map<string, number>()
  const distinct = new Map<string, Set<string>>()
  for (const run of runs) {
    for (const round of run.rounds) {
      for (const w of round.promptWrites) {
        totals.set(run.arm, (totals.get(run.arm) ?? 0) + 1)
        let seen = distinct.get(run.arm)
        if (seen === undefined) {
          seen = new Set<string>()
          distinct.set(run.arm, seen)
        }
        seen.add(writeKey(w, round.workerPromptAfter))
      }
    }
  }

  const rows: BenchRow[] = arr(raw.ranking).map((r) => {
    const o = isObj(r) ? r : {}
    const armId = str(o.arm)
    const arm = armById.get(armId)
    const summary = summaries.get(armId)
    const metrics = summary?.metrics ?? {}
    return {
      rank: num(o.rank),
      arm: armId,
      topology: arm?.topology ?? '',
      primaryWorker: arm?.primaryWorker ?? '',
      reps: summary?.reps ?? 0,
      mean: num(o.mean),
      spread: num(o.spread),
      metrics,
      churn: metrics[CHURN_METRIC] ?? null,
      rewrites: totals.get(armId) ?? 0,
      distinctRewrites: distinct.get(armId)?.size ?? 0,
    }
  })

  const keys: string[] = []
  for (const row of rows) {
    for (const k of Object.keys(row.metrics)) if (!keys.includes(k)) keys.push(k)
  }

  const hasSpread = rows.some(
    (row) => row.spread !== 0 || Object.values(row.metrics).some((m) => m.spread !== 0),
  )

  return {
    schema,
    task,
    arms,
    properties,
    rows,
    runs,
    table: str(raw.table),
    metricColumns: orderColumns(keys, task.rankBy),
    hasSpread,
  }
}

/** `0.5 ±0` — a mean is never shown without its spread (§7.3). */
export function formatMeanSpread(mean: number, spread: number): string {
  return `${formatNumber(mean)} ±${formatNumber(spread)}`
}

/** Short, stable number formatting: no trailing zeros, three decimals maximum. */
export function formatNumber(n: number): string {
  if (!Number.isFinite(n)) return '—'
  if (Number.isInteger(n)) return String(n)
  return String(Math.round(n * 1000) / 1000)
}

/** "2 rewrites · 1 distinct" — the dedupe note §7.3 requires beside churn. */
export function describeRewrites(row: BenchRow): string {
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`
  if (row.rewrites === 0) return 'no rewrites'
  return `${plural(row.rewrites, 'rewrite')} · ${row.distinctRewrites} distinct`
}
