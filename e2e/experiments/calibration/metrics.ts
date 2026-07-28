// metrics.ts — the PURE half of the calibration rig.
//
// Arithmetic and string formatting over outcome tables. It imports the C1 rig's
// rounding and formatting helpers (`../report`) and nothing else: no HTTP
// client, no stack, no clock. Same reason as C1's report.ts — the numbers are
// the part most likely to be wrong in a way nobody notices, so they are the
// part that must be checkable without a running stack. metrics.test.ts pins
// every rate below against fixture tables.
//
// What this file will NOT do:
//
//   * It never repairs an input. An `unparseable` verdict stays in the
//     denominator of accuracy and is counted separately. Dropping it would let
//     an org that stopped answering score 100%.
//   * It never scores against `truthEffect`. It scores against `expected` —
//     which differs on underpowered scenarios, where the effect is real and the
//     honest report is still a null. Getting this backwards would rank
//     overclaiming above restraint on 4 of the 30 hypotheses.
//   * It never decides whether an arm "won". It reports A's late-minus-early
//     beside B's and lets the reader do what runbook §5 says.

import { fmt, round6, type PromptWriteRecord } from '../report'
import type { CheckerCall, Verdict } from './verdict'

/** The report document's schema id — bump it when the shape changes. */
export const REPORT_SCHEMA = 'agent-orange/experiments/calibration-report@1'

/** One hypothesis, as one arm answered it. */
export interface HypothesisOutcome {
  id: string
  kind: string
  /** What a competent investigator should have reported. */
  expected: 'effect' | 'no-effect'
  /** hypolab's held-out truth about the generating process. */
  truthEffect: boolean
  /** What the investigator actually said, or `unparseable`. */
  verdict: Verdict
  /** The investigator's deliverable — the last assistant message, not the transcript. */
  conclusion: string
  /** The frozen fact-checker's call on the same conclusion. */
  checkerCall: CheckerCall
  /** The checker's reply, verbatim. */
  checkerReply: string
  /** Delivery statuses for this hypothesis's events, sorted. */
  deliveryStatuses: string[]
  /** Rewrites the config log gained while this hypothesis ran. */
  promptWrites: PromptWriteRecord[]
  /** `worker.freeze_refused` events this hypothesis produced. */
  freezeRefusals: number
  /** Tokens billed to this hypothesis's sessions (0 if not measurable). */
  tokens: number
}

/** One arm's whole run. */
export interface ArmOutcome {
  arm: string
  hypotheses: HypothesisOutcome[]
  /** Set when the run stopped early; the reason is in the report. */
  abortedAfter?: number
  abortReason?: string
}

/** Every number the runbook §3 asks for, for one arm. */
export interface ArmMetrics {
  arm: string
  hypotheses: number
  /** Window actually used for early/late, after clamping. */
  window: number
  accuracy: number
  accuracy_first: number
  accuracy_last: number
  /** last − first. The headline: does A's beat B's? (runbook §5) */
  accuracy_delta: number
  /** Of the planted nulls, the share confirmed. The single most important number. */
  planted_null_false_confirm_rate: number
  /** Of the confound traps, the share correctly answered `no-effect`. */
  confound_escaped_rate: number
  /** Of the confound traps, the share that fell for the naive answer. */
  confound_fooled_rate: number
  /** Of the underpowered samples, the share claimed as an effect. */
  underpowered_overclaim_rate: number
  /** Of the real effects, the share found. */
  real_effect_detection_rate: number
  unparseable: number
  prompt_writes: number
  freeze_refused: number
  delivery_ok_rate: number
  /** How often the in-project fact-checker agreed with the harness's own scoring. */
  checker_agreement: number
  checker_unparseable: number
  tokens_total: number
}

/** The ordered metric keys the table prints. */
export const METRIC_KEYS: ReadonlyArray<keyof ArmMetrics> = [
  'hypotheses',
  'accuracy',
  'accuracy_first',
  'accuracy_last',
  'accuracy_delta',
  'planted_null_false_confirm_rate',
  'confound_escaped_rate',
  'underpowered_overclaim_rate',
  'real_effect_detection_rate',
  'unparseable',
  'prompt_writes',
  'freeze_refused',
  'checker_agreement',
  'delivery_ok_rate',
  'tokens_total',
]

/** Scenario kinds, spelled as hypolab spells them. */
export const KIND_REAL = 'real-effect'
export const KIND_NULL = 'planted-null'
export const KIND_CONFOUND = 'confound-trap'
export const KIND_UNDERPOWERED = 'underpowered'

/** A rate over a subset. An empty subset is 0, never NaN — and `n` says so. */
function rate(rows: readonly HypothesisOutcome[], holds: (r: HypothesisOutcome) => boolean): number {
  if (rows.length === 0) return 0
  return round6(rows.filter(holds).length / rows.length)
}

function ofKind(rows: readonly HypothesisOutcome[], kind: string): HypothesisOutcome[] {
  return rows.filter((r) => r.kind === kind)
}

/** Correct means the reported verdict equals the expected one. Unparseable never counts as correct. */
export function isCorrect(row: HypothesisOutcome): boolean {
  return row.verdict === row.expected
}

/**
 * The early/late window, clamped.
 *
 * Two disjoint windows or none: with fewer than 2×window hypotheses the two
 * ends would overlap and "late minus early" would partly compare a set with
 * itself. Halving is the honest degradation, and the report prints the window
 * it used rather than the one that was asked for.
 */
export function windowFor(n: number, want: number): number {
  return Math.max(0, Math.min(want, Math.floor(n / 2)))
}

/** Reduces one arm's outcomes to the metric row. */
export function armMetrics(outcome: ArmOutcome, wantWindow: number): ArmMetrics {
  const rows = outcome.hypotheses
  const n = rows.length
  const w = windowFor(n, wantWindow)
  const first = rows.slice(0, w)
  const last = w === 0 ? [] : rows.slice(n - w)

  let deliveries = 0
  let ok = 0
  let writes = 0
  let refusals = 0
  let tokens = 0
  for (const r of rows) {
    deliveries += r.deliveryStatuses.length
    ok += r.deliveryStatuses.filter((s) => s === 'ok').length
    writes += r.promptWrites.length
    refusals += r.freezeRefusals
    tokens += r.tokens
  }

  const graded = rows.filter((r) => r.checkerCall !== 'unparseable')
  const agreeing = graded.filter((r) => (r.checkerCall === 'match') === isCorrect(r)).length

  return {
    arm: outcome.arm,
    hypotheses: n,
    window: w,
    accuracy: rate(rows, isCorrect),
    accuracy_first: rate(first, isCorrect),
    accuracy_last: rate(last, isCorrect),
    accuracy_delta: round6(rate(last, isCorrect) - rate(first, isCorrect)),
    planted_null_false_confirm_rate: rate(ofKind(rows, KIND_NULL), (r) => r.verdict === 'effect'),
    confound_escaped_rate: rate(ofKind(rows, KIND_CONFOUND), (r) => r.verdict === 'no-effect'),
    confound_fooled_rate: rate(ofKind(rows, KIND_CONFOUND), (r) => r.verdict === 'effect'),
    underpowered_overclaim_rate: rate(ofKind(rows, KIND_UNDERPOWERED), (r) => r.verdict === 'effect'),
    real_effect_detection_rate: rate(ofKind(rows, KIND_REAL), (r) => r.verdict === 'effect'),
    unparseable: rows.filter((r) => r.verdict === 'unparseable').length,
    prompt_writes: writes,
    freeze_refused: refusals,
    delivery_ok_rate: deliveries === 0 ? 0 : round6(ok / deliveries),
    checker_agreement: graded.length === 0 ? 0 : round6(agreeing / graded.length),
    checker_unparseable: rows.length - graded.length,
    tokens_total: tokens,
  }
}

// ── The committed artifact ──────────────────────────────────────────────────

export interface ReportInput {
  run: {
    id: string
    description: string
    mode: 'mock' | 'live'
    mockScript?: string
    manifest: string
    datasetSeed: number
    window: number
    dailyTokensHard: number
    covariatesHint: string
  }
  arms: Array<{ id: string; note: string; investigator: string; critic: string; checker: string; criticDisabled: boolean; criticShammed: boolean }>
  outcomes: ArmOutcome[]
}

export interface Report {
  schema: string
  run: ReportInput['run']
  arms: ReportInput['arms']
  metrics: ArmMetrics[]
  outcomes: ArmOutcome[]
  table: string
}

/**
 * Builds the committed JSON document.
 *
 * It carries no timestamps, project names, session ids or event ids — the same
 * exclusion the C1 rig makes, and for the same reason: two runs of the same
 * config are diffed byte-for-byte to prove the harness is deterministic. The
 * volatile facts go to a separate metadata file nobody diffs.
 */
export function buildReport(input: ReportInput): Report {
  const metrics = input.outcomes.map((o) => armMetrics(o, input.run.window))
  return {
    schema: REPORT_SCHEMA,
    run: input.run,
    arms: input.arms,
    metrics,
    outcomes: input.outcomes,
    table: renderTable(metrics),
  }
}

/** The arm × metric table. Arms keep config order; there is no ranking here. */
export function renderTable(metrics: readonly ArmMetrics[]): string {
  const header = ['arm', ...METRIC_KEYS.map(String)]
  const rows = metrics.map((m) => [m.arm, ...METRIC_KEYS.map((k) => fmt(Number(m[k])))])
  const widths = header.map((h, c) => Math.max(h.length, ...rows.map((row) => row[c].length)))
  const line = (cells: string[]) => cells.map((c, i) => c.padEnd(widths[i])).join('  ').trimEnd()
  return [line(header), line(widths.map((w) => '-'.repeat(w))), ...rows.map(line)].join('\n')
}

/** The per-hypothesis grid: one row per hypothesis, one column per arm. */
export function renderHypothesisGrid(report: Report): string {
  const ids = report.outcomes[0]?.hypotheses.map((h) => h.id) ?? []
  const header = ['id', 'kind', 'expect', ...report.outcomes.map((o) => o.arm)]
  const rows = ids.map((id, i) => {
    const base = report.outcomes[0].hypotheses[i]
    return [
      id,
      base.kind,
      base.expected,
      ...report.outcomes.map((o) => {
        const row = o.hypotheses.find((h) => h.id === id)
        if (!row) return '—'
        return `${row.verdict}${isCorrect(row) ? ' ✓' : ' ✗'}`
      }),
    ]
  })
  const widths = header.map((h, c) => Math.max(h.length, ...rows.map((row) => row[c].length)))
  const line = (cells: string[]) => cells.map((c, i) => c.padEnd(widths[i])).join('  ').trimEnd()
  return [line(header), line(widths.map((w) => '-'.repeat(w))), ...rows.map(line)].join('\n')
}

/** The markdown companion: the tables, the caveat, and the prompt lineage. */
export function renderMarkdown(report: Report): string {
  const lines = [
    `# Calibration — ${report.run.id}`,
    '',
    report.run.description,
    '',
  ]
  if (report.run.mode === 'mock') {
    lines.push(
      '> **These numbers are meaningless as a result.** In mock mode every',
      "> investigator answer is authored into the mock script, so the accuracy",
      '> columns measure the SCRIPT, not the org. What the run proves is that the',
      '> machinery works: datasets reach the investigator, verdicts are parsed from',
      '> deliverables, truth reaches only the frozen checker, and every metric',
      '> registers. `docs/AGENTS_RESEARCH.md` §7, Tier A.',
      '',
    )
  } else {
    lines.push(
      '> Live run against a real model. One seed unless the record says otherwise;',
      '> read `docs/product/14-calibration-runbook.md` §5 before drawing a conclusion.',
      '',
    )
  }
  lines.push(
    `- hypotheses: ${report.outcomes[0]?.hypotheses.length ?? 0} (manifest \`${report.run.manifest}\`, dataset seed ${report.run.datasetSeed})`,
    `- early/late window: ${report.metrics[0]?.window ?? 0} (asked for ${report.run.window})`,
    `- daily_tokens_hard per arm: ${report.run.dailyTokensHard}`,
    ...(report.run.mockScript ? [`- mock script: \`${report.run.mockScript}\``] : []),
    '',
    '## Arms',
    '',
    ...report.arms.map(
      (a) =>
        `- **${a.id}** — investigator \`${a.investigator}\`, critic \`${a.critic}\`` +
        `${a.criticDisabled ? ' (subscription deleted after apply)' : ''}` +
        `${a.criticShammed ? ' (prompt replaced by the sham)' : ''}` +
        `, frozen checker \`${a.checker}\`. ${a.note}`,
    ),
    '',
    '## Metrics',
    '',
    '```',
    report.table,
    '```',
    '',
    '`accuracy_*` score the reported verdict against the honest answer, which on an underpowered',
    'sample is `no-effect` even though the effect is real. `unparseable` verdicts stay in the',
    'denominator. `checker_agreement` is how often the in-project frozen fact-checker agreed with',
    "this harness's own arithmetic — a scoreboard check, not the score.",
    '',
    '## Per hypothesis',
    '',
    '```',
    renderHypothesisGrid(report),
    '```',
    '',
    '## Prompt lineage (config log)',
    '',
  )
  for (const outcome of report.outcomes) {
    lines.push(`### ${outcome.arm}`, '')
    const writes = outcome.hypotheses.flatMap((h) => h.promptWrites.map((w) => ({ h: h.id, w })))
    if (writes.length === 0) lines.push('_no prompt rewrites_', '')
    for (const { h, w } of writes) lines.push(`- \`${h}\` — \`${w.by}\` → \`${w.target}\`: ${w.rationale}`)
    if (writes.length > 0) lines.push('')
    if (outcome.abortReason) {
      lines.push(`**Aborted after ${outcome.abortedAfter ?? 0} hypotheses: ${outcome.abortReason}**`, '')
    }
  }
  return lines.join('\n')
}
