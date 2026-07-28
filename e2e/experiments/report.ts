// report.ts — the PURE half of the comparison rig (C1).
//
// Everything here is arithmetic and string formatting over outcome tables. It
// imports nothing: no HTTP client, no stack, no clock. That is deliberate and
// load-bearing — the ranking and variance computation is the part most likely
// to be wrong in a way nobody notices, so it is the part that must be testable
// without a running stack (report.test.ts pins it against fixture tables).
//
// The unit of measurement is one ARM RUN: one topology applied to one
// throwaway project, driven through the task's rounds once. `repetitions` of
// the same arm produce several runs, and the spread across those runs is the
// only honest statement the rig can make about stability. In mock mode the
// spread is expected to be exactly zero — which is not a boring result, it is
// the determinism claim the whole Tier A tier rests on (AGENTS_RESEARCH §7).
//
// Nothing here knows what a "good" topology is. It ranks by whichever metric
// the task spec nominates, and reports the rest beside it.

/** One prompt rewrite as the config log recorded it (§15.5). */
export interface PromptWriteRecord {
  /** The worker whose session made the write — `actor_worker` on the record. */
  by: string
  /** The worker whose prompt was rewritten — `payload.name`. */
  target: string
  /** The required rationale. The control arm's honesty is readable here. */
  rationale: string
}

/** What one round of one arm run produced, reduced to comparable facts. */
export interface RoundOutcome {
  round: number
  /** The inbound event text this round was driven with. */
  taskText: string
  /** Delivery statuses for the round's inbound event, sorted for determinism. */
  deliveryStatuses: string[]
  /** Rewrites logged between the previous round settling and this one. */
  promptWrites: PromptWriteRecord[]
  /** The primary worker's reply — the thing a property predicate reads. */
  output: string
  /** The primary worker's stored system prompt once the round had settled. */
  workerPromptAfter: string
  /** Property id → whether it held this round. Keys are the task spec's. */
  properties: Record<string, boolean>
}

/** One repetition of one arm. */
export interface RunOutcome {
  arm: string
  repetition: number
  rounds: RoundOutcome[]
}

/** A metric's distribution across an arm's repetitions. */
export interface Stat {
  n: number
  mean: number
  min: number
  max: number
  /** max − min. The plain-language spread; zero is the determinism claim. */
  spread: number
  /** Population standard deviation (n, not n−1: these are all the runs there are). */
  stdev: number
}

export interface ArmSummary {
  arm: string
  reps: number
  metrics: Record<string, Stat>
}

export interface RankRow {
  rank: number
  arm: string
  mean: number
  spread: number
}

// ── Metric vocabulary ───────────────────────────────────────────────────────

/** Metrics every comparison reports, whatever the task. */
export const BASE_METRICS = ['rounds_completed', 'delivery_ok_rate', 'prompt_writes'] as const

/** The metric key a property id is reported under. */
export function propertyMetric(propertyId: string): string {
  return `prop:${propertyId}`
}

/** The full, ordered metric key list for a task with these properties. */
export function metricKeys(propertyIds: readonly string[]): string[] {
  return [...BASE_METRICS, ...propertyIds.map(propertyMetric)]
}

// ── Per-run metrics ─────────────────────────────────────────────────────────

/**
 * Reduces one arm run to a flat metric row.
 *
 * Rates are per-round or per-delivery fractions rather than counts so that arms
 * with different round counts stay comparable; counts (`prompt_writes`) stay
 * counts because "how much churn did this org produce" is the question C7 asks,
 * and a rate would hide it.
 */
export function runMetrics(run: RunOutcome, propertyIds: readonly string[]): Record<string, number> {
  const rounds = run.rounds.length
  let deliveries = 0
  let ok = 0
  let writes = 0
  for (const r of run.rounds) {
    deliveries += r.deliveryStatuses.length
    ok += r.deliveryStatuses.filter((s) => s === 'ok').length
    writes += r.promptWrites.length
  }
  const out: Record<string, number> = {
    rounds_completed: rounds,
    delivery_ok_rate: deliveries === 0 ? 0 : round6(ok / deliveries),
    prompt_writes: writes,
  }
  for (const id of propertyIds) {
    const held = run.rounds.filter((r) => r.properties[id] === true).length
    out[propertyMetric(id)] = rounds === 0 ? 0 : round6(held / rounds)
  }
  return out
}

// ── Aggregation across repetitions ──────────────────────────────────────────

/** The distribution of one sample list. Empty input is a zeroed stat, not NaN. */
export function stat(values: readonly number[]): Stat {
  if (values.length === 0) return { n: 0, mean: 0, min: 0, max: 0, spread: 0, stdev: 0 }
  const n = values.length
  const mean = values.reduce((a, b) => a + b, 0) / n
  const min = Math.min(...values)
  const max = Math.max(...values)
  const variance = values.reduce((a, b) => a + (b - mean) ** 2, 0) / n
  return {
    n,
    mean: round6(mean),
    min: round6(min),
    max: round6(max),
    spread: round6(max - min),
    stdev: round6(Math.sqrt(variance)),
  }
}

/**
 * Groups runs by arm and summarises each metric across the repetitions.
 *
 * Arms come back in first-appearance order — the task spec's order — so the
 * JSON artifact is byte-stable across runs of the same config. Ranking reorders
 * a copy; it never reorders this.
 */
export function summarise(runs: readonly RunOutcome[], propertyIds: readonly string[]): ArmSummary[] {
  const keys = metricKeys(propertyIds)
  const order: string[] = []
  const byArm = new Map<string, Record<string, number>[]>()
  for (const run of runs) {
    if (!byArm.has(run.arm)) {
      byArm.set(run.arm, [])
      order.push(run.arm)
    }
    byArm.get(run.arm)!.push(runMetrics(run, propertyIds))
  }
  return order.map((arm) => {
    const rows = byArm.get(arm)!
    const metrics: Record<string, Stat> = {}
    for (const key of keys) metrics[key] = stat(rows.map((r) => r[key] ?? 0))
    return { arm, reps: rows.length, metrics }
  })
}

// ── Ranking ─────────────────────────────────────────────────────────────────

/**
 * Ranks arms by one metric's mean. Ties share a rank (competition ranking:
 * 1, 2, 2, 4), because a tie is the honest answer and inventing an order
 * between two identical arms is exactly the false precision this rig exists to
 * avoid. Tied arms keep their task-spec order.
 */
export function rank(
  summaries: readonly ArmSummary[],
  metricKey: string,
  direction: 'desc' | 'asc' = 'desc',
): RankRow[] {
  const rows = summaries.map((s, i) => ({
    i,
    arm: s.arm,
    mean: s.metrics[metricKey]?.mean ?? 0,
    spread: s.metrics[metricKey]?.spread ?? 0,
  }))
  rows.sort((a, b) => (a.mean === b.mean ? a.i - b.i : direction === 'desc' ? b.mean - a.mean : a.mean - b.mean))
  const out: RankRow[] = []
  let lastMean: number | undefined
  let lastRank = 0
  rows.forEach((r, idx) => {
    const position = lastMean !== undefined && r.mean === lastMean ? lastRank : idx + 1
    lastMean = r.mean
    lastRank = position
    out.push({ rank: position, arm: r.arm, mean: r.mean, spread: r.spread })
  })
  return out
}

// ── The human-readable table ────────────────────────────────────────────────

/** Trims a number to at most 3 decimals without exponent notation. */
export function fmt(n: number): string {
  if (Number.isInteger(n)) return String(n)
  return String(Number(n.toFixed(3)))
}

/**
 * The arm × metric table, arms in ranked order, each cell `mean ±spread`.
 *
 * The spread is printed even when it is zero, and that is the point: a reader
 * must be able to see at a glance that the mock run was deterministic, and a
 * non-zero spread in mock mode must be impossible to miss.
 */
export function renderTable(
  summaries: readonly ArmSummary[],
  propertyIds: readonly string[],
  ranking: readonly RankRow[],
): string {
  const keys = metricKeys(propertyIds)
  const byArm = new Map(summaries.map((s) => [s.arm, s]))
  const header = ['#', 'arm', 'reps', ...keys]
  const rows = ranking.map((r) => {
    const s = byArm.get(r.arm)
    const cells = keys.map((k) => {
      const m = s?.metrics[k]
      return m ? `${fmt(m.mean)} ±${fmt(m.spread)}` : '—'
    })
    return [String(r.rank), r.arm, String(s?.reps ?? 0), ...cells]
  })
  const widths = header.map((h, c) => Math.max(h.length, ...rows.map((row) => row[c].length)))
  const line = (cells: string[]) => cells.map((c, i) => c.padEnd(widths[i])).join('  ').trimEnd()
  return [line(header), line(widths.map((w) => '-'.repeat(w))), ...rows.map(line)].join('\n')
}

// ── The committed artifact ──────────────────────────────────────────────────

/** The report document's schema id — bump it when the shape changes. */
export const REPORT_SCHEMA = 'agent-orange/experiments/compare-report@1'

export interface ReportInput {
  task: {
    id: string
    description: string
    mockScript: string
    rounds: number
    repetitions: number
    rankBy: string
    rankDirection: 'desc' | 'asc'
  }
  arms: Array<{ id: string; topology: string; eventType: string; primaryWorker: string; answers: Record<string, unknown> }>
  properties: Array<{ id: string; describe: string }>
  runs: RunOutcome[]
}

export interface Report {
  schema: string
  task: ReportInput['task']
  arms: ReportInput['arms']
  properties: ReportInput['properties']
  ranking: RankRow[]
  summaries: ArmSummary[]
  runs: RunOutcome[]
  table: string
}

/**
 * Builds the committed JSON document.
 *
 * It carries NO timestamps, project names, session ids or event ids — every one
 * of those changes between two identical runs, and the rig's determinism claim
 * is checked by diffing two of these files. The volatile facts (when it ran,
 * which projects it used) go to a separate metadata file that nobody diffs.
 */
export function buildReport(input: ReportInput): Report {
  const propertyIds = input.properties.map((p) => p.id)
  const summaries = summarise(input.runs, propertyIds)
  const ranking = rank(summaries, input.task.rankBy, input.task.rankDirection)
  return {
    schema: REPORT_SCHEMA,
    task: input.task,
    arms: input.arms,
    properties: input.properties,
    ranking,
    summaries,
    runs: input.runs,
    table: renderTable(summaries, propertyIds, ranking),
  }
}

/** The markdown companion: the table plus the legend a reader needs. */
export function renderMarkdown(report: Report): string {
  const lines = [
    `# Comparison — ${report.task.id}`,
    '',
    report.task.description,
    '',
    '**Tier A (mock) result: this proves the machinery transmits a difference, never that the',
    'system discovered one** — the improvement is authored into the mock script',
    '(`docs/AGENTS_RESEARCH.md` §7).',
    '',
    `- rounds per run: ${report.task.rounds}`,
    `- repetitions per arm: ${report.task.repetitions}`,
    `- ranked by: \`${report.task.rankBy}\` (${report.task.rankDirection})`,
    `- mock script: \`${report.task.mockScript}\``,
    '',
    '## Ranking',
    '',
    '```',
    report.table,
    '```',
    '',
    'Cells are `mean ±spread` across repetitions. In mock mode every spread must be 0.',
    '',
    '## Arms',
    '',
    ...report.arms.map((a) => `- **${a.id}** — \`${a.topology}\`, woken by \`${a.eventType}\`, primary worker \`${a.primaryWorker}\``),
    '',
    '## Properties',
    '',
    ...report.properties.map((p) => `- **${p.id}** — ${p.describe}`),
    '',
    '## Prompt rewrites recorded (config log)',
    '',
  ]
  for (const summary of report.summaries) {
    const writes = report.runs
      .filter((r) => r.arm === summary.arm && r.repetition === 1)
      .flatMap((r) => r.rounds.flatMap((round) => round.promptWrites))
    lines.push(`### ${summary.arm} (repetition 1)`)
    lines.push('')
    if (writes.length === 0) lines.push('_no prompt rewrites_')
    for (const w of writes) lines.push(`- \`${w.by}\` → \`${w.target}\`: ${w.rationale}`)
    lines.push('')
  }
  return lines.join('\n')
}

// ── Arithmetic hygiene ──────────────────────────────────────────────────────

/**
 * Rounds to 6 decimals.
 *
 * Applied to every number that reaches the artifact, because binary floating
 * point makes 1/3 + 1/3 + 1/3 print differently depending on the order the
 * additions happened in — and the artifact is diffed byte-for-byte to prove
 * determinism. Six decimals is far below any difference a reader would act on
 * and far above the noise.
 */
export function round6(n: number): number {
  return Math.round(n * 1e6) / 1e6
}
