// metrics.ts — the PURE half of the triage rig.
//
// Arithmetic and string formatting over outcome tables. It imports the C1 rig's
// rounding and formatting helpers (`../report`) and nothing else: no HTTP
// client, no stack, no clock. Same reason as calibration's metrics.ts — the
// numbers are the part most likely to be wrong in a way nobody notices, so they
// are the part that must be checkable without a running stack. metrics.test.ts
// pins every rate below against fixture tables.
//
// What this file will NOT do:
//
//   * It never repairs an input. An `unparseable` route stays in the
//     denominator of accuracy and is counted separately. Dropping it would let
//     an org that stopped answering score 100%.
//   * It never treats `escalate` as a non-answer. On an ambiguous ticket
//     escalating is the CORRECT route and scores as such; on any other ticket it
//     is wrong, and `over_escalation_rate` says how often that happened. An
//     instrument that rewarded escalation everywhere would rank silence first.
//   * It never decides whether an arm "won". It reports A's trap misroute rate
//     beside B's and lets the reader do the work.

import { fmt, round6, type PromptWriteRecord } from '../report'
import type { AuditorCall, Route } from './route'

/** The report document's schema id — bump it when the shape changes. */
export const REPORT_SCHEMA = 'agent-orange/experiments/triage-report@1'

/** Ticket kinds, spelled as go/triagelab spells them. */
export const KIND_PLAIN = 'plain'
export const KIND_MISDIRECT = 'misdirect'
export const KIND_AMBIGUOUS = 'ambiguous'

/** The reserved route. Must match go/topology's TriageEscalateRoute. */
export const ESCALATE_ROUTE = 'escalate'

/** One ticket, as one arm routed it. */
export interface TicketOutcome {
  id: string
  kind: string
  /** The held-out correct route: a queue id, or `escalate`. */
  expected: string
  /** The queue this ticket's vocabulary points at; '' for plain. */
  decoy: string
  /** What the dispatcher actually decided, in canonical vocabulary. */
  route: Route
  /** The raw ROUTE-TO value, as written — '' when there was no contract line. */
  statedRoute: string
  /** The dispatcher's deliverable — the last assistant message, not the transcript. */
  reply: string
  /** The frozen route-auditor's call on the same decision. */
  auditorCall: AuditorCall
  /** The auditor's reply, verbatim. */
  auditorReply: string
  /** Delivery statuses for this ticket's events, sorted. */
  deliveryStatuses: string[]
  /** Rewrites the config log gained while this ticket ran. */
  promptWrites: PromptWriteRecord[]
  /** `worker.freeze_refused` events this ticket produced. */
  freezeRefusals: number
  /** Tokens billed to this ticket's sessions (0 if not measurable). */
  tokens: number
}

/** One arm's whole run. */
export interface ArmOutcome {
  arm: string
  tickets: TicketOutcome[]
  /** Set when the run stopped early; the reason is in the report. */
  abortedAfter?: number
  abortReason?: string
}

/** Every number doc 19 §3 SC-1 asks for, for one arm. */
export interface ArmMetrics {
  arm: string
  tickets: number
  /** Window actually used for early/late, after clamping. */
  window: number
  accuracy: number
  accuracy_first: number
  accuracy_last: number
  /** last − first. Does A's beat B's? */
  accuracy_delta: number
  /**
   * THE HEADLINE. Of the misdirection traps, the share NOT routed to the
   * queue the stated facts name — the analogue of calibration's
   * false-confirmation rate.
   */
  trap_misroute_rate: number
  /** Of the misdirection traps, the share routed to exactly the decoy: how often surface vocabulary won. */
  trap_decoy_rate: number
  /** Of the ambiguity traps, the share given a confident queue where escalate was correct. */
  ambiguity_confidence_rate: number
  /** Of the plain tickets, the share correct. The floor: an arm failing here is broken, not fooled. */
  plain_accuracy: number
  /** Of the tickets that were NOT ambiguous, the share escalated. Restraint has a price and this is it. */
  over_escalation_rate: number
  /** Share of all tickets answered `escalate`, right or wrong. */
  escalation_rate: number
  unparseable: number
  prompt_writes: number
  freeze_refused: number
  delivery_ok_rate: number
  /** How often the in-project route-auditor agreed with the harness's own scoring. */
  auditor_agreement: number
  auditor_unparseable: number
  tokens_total: number
}

/** The ordered metric keys the table prints. */
export const METRIC_KEYS: ReadonlyArray<keyof ArmMetrics> = [
  'tickets',
  'accuracy',
  'accuracy_first',
  'accuracy_last',
  'accuracy_delta',
  'trap_misroute_rate',
  'trap_decoy_rate',
  'ambiguity_confidence_rate',
  'plain_accuracy',
  'over_escalation_rate',
  'unparseable',
  'prompt_writes',
  'freeze_refused',
  'auditor_agreement',
  'delivery_ok_rate',
  'tokens_total',
]

/** A rate over a subset. An empty subset is 0, never NaN — and `n` says so. */
function rate(rows: readonly TicketOutcome[], holds: (r: TicketOutcome) => boolean): number {
  if (rows.length === 0) return 0
  return round6(rows.filter(holds).length / rows.length)
}

function ofKind(rows: readonly TicketOutcome[], kind: string): TicketOutcome[] {
  return rows.filter((r) => r.kind === kind)
}

/** Correct means the decided route equals the held-out one. Unparseable never counts as correct. */
export function isCorrect(row: TicketOutcome): boolean {
  return row.route === row.expected
}

/**
 * The early/late window, clamped.
 *
 * Two disjoint windows or none: with fewer than 2×window tickets the two ends
 * would overlap and "late minus early" would partly compare a set with itself.
 * Halving is the honest degradation, and the report prints the window it used
 * rather than the one that was asked for.
 */
export function windowFor(n: number, want: number): number {
  return Math.max(0, Math.min(want, Math.floor(n / 2)))
}

/** Reduces one arm's outcomes to the metric row. */
export function armMetrics(outcome: ArmOutcome, wantWindow: number): ArmMetrics {
  const rows = outcome.tickets
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

  const traps = ofKind(rows, KIND_MISDIRECT)
  const ambiguous = ofKind(rows, KIND_AMBIGUOUS)
  const decidable = rows.filter((r) => r.kind !== KIND_AMBIGUOUS)

  const graded = rows.filter((r) => r.auditorCall !== 'unparseable')
  const agreeing = graded.filter((r) => (r.auditorCall === 'match') === isCorrect(r)).length

  return {
    arm: outcome.arm,
    tickets: n,
    window: w,
    accuracy: rate(rows, isCorrect),
    accuracy_first: rate(first, isCorrect),
    accuracy_last: rate(last, isCorrect),
    accuracy_delta: round6(rate(last, isCorrect) - rate(first, isCorrect)),
    trap_misroute_rate: rate(traps, (r) => !isCorrect(r)),
    trap_decoy_rate: rate(traps, (r) => r.decoy !== '' && r.route === r.decoy),
    ambiguity_confidence_rate: rate(ambiguous, (r) => r.route !== ESCALATE_ROUTE && r.route !== 'unparseable'),
    plain_accuracy: rate(ofKind(rows, KIND_PLAIN), isCorrect),
    over_escalation_rate: rate(decidable, (r) => r.route === ESCALATE_ROUTE),
    escalation_rate: rate(rows, (r) => r.route === ESCALATE_ROUTE),
    unparseable: rows.filter((r) => r.route === 'unparseable').length,
    prompt_writes: writes,
    freeze_refused: refusals,
    delivery_ok_rate: deliveries === 0 ? 0 : round6(ok / deliveries),
    auditor_agreement: graded.length === 0 ? 0 : round6(agreeing / graded.length),
    auditor_unparseable: rows.length - graded.length,
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
  }
  arms: Array<{
    id: string
    note: string
    dispatcher: string
    queues: { billing: string; outage: string; access: string }
    critic: string
    auditor: string
    criticDisabled: boolean
    /** The charter this arm was given, verbatim — what the org was TOLD. */
    routingRules: string
  }>
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
 * exclusion the C1 and calibration rigs make, and for the same reason: two runs
 * of the same config are diffed byte-for-byte to prove the harness is
 * deterministic. The volatile facts go to a separate metadata file nobody diffs.
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

/** The per-ticket grid: one row per ticket, one column per arm. */
export function renderTicketGrid(report: Report): string {
  const ids = report.outcomes[0]?.tickets.map((tk) => tk.id) ?? []
  const header = ['id', 'kind', 'expect', ...report.outcomes.map((o) => o.arm)]
  const rows = ids.map((id, i) => {
    const base = report.outcomes[0].tickets[i]
    return [
      id,
      base.kind,
      base.expected,
      ...report.outcomes.map((o) => {
        const row = o.tickets.find((tk) => tk.id === id)
        if (!row) return '—'
        return `${row.route}${isCorrect(row) ? ' ✓' : ' ✗'}`
      }),
    ]
  })
  const widths = header.map((h, c) => Math.max(h.length, ...rows.map((row) => row[c].length)))
  const line = (cells: string[]) => cells.map((c, i) => c.padEnd(widths[i])).join('  ').trimEnd()
  return [line(header), line(widths.map((w) => '-'.repeat(w))), ...rows.map(line)].join('\n')
}

/** The markdown companion: the tables, the caveat, and the prompt lineage. */
export function renderMarkdown(report: Report): string {
  const lines = [`# Triage (SC-1) — ${report.run.id}`, '', report.run.description, '']
  if (report.run.mode === 'mock') {
    lines.push(
      '> **These numbers are meaningless as a result.** In mock mode every',
      '> dispatcher decision is authored into the mock script, so the accuracy',
      '> columns measure the SCRIPT, not the org. What the run proves is that the',
      '> machinery works: tickets reach the dispatcher, routes are parsed from',
      '> deliverables, truth reaches only the frozen auditor, the critic’s rewrite',
      '> changes the next decision, and every metric registers.',
      '> `docs/AGENTS_RESEARCH.md` §7, Tier A.',
      '',
    )
  } else {
    lines.push(
      '> Live run against a real model. One seed unless the record says otherwise;',
      '> read `docs/product/19-scenario-library.md` §3 before drawing a conclusion.',
      '',
    )
  }
  lines.push(
    `- tickets: ${report.outcomes[0]?.tickets.length ?? 0} (manifest \`${report.run.manifest}\`, dataset seed ${report.run.datasetSeed})`,
    `- early/late window: ${report.metrics[0]?.window ?? 0} (asked for ${report.run.window})`,
    `- daily_tokens_hard per arm: ${report.run.dailyTokensHard}`,
    ...(report.run.mockScript ? [`- mock script: \`${report.run.mockScript}\``] : []),
    '',
    '## Arms',
    '',
    ...report.arms.map(
      (a) =>
        `- **${a.id}** — dispatcher \`${a.dispatcher}\`, queues \`${a.queues.billing}\` / \`${a.queues.outage}\` / ` +
        `\`${a.queues.access}\`, critic \`${a.critic}\`` +
        `${a.criticDisabled ? ' (subscription deleted after apply)' : ''}` +
        `, frozen auditor \`${a.auditor}\`. ${a.note}`,
    ),
    '',
    '## Metrics',
    '',
    '```',
    report.table,
    '```',
    '',
    '`trap_misroute_rate` is the headline: of the misdirection tickets — whose vocabulary points at one',
    'queue while the stated facts belong to another — the share the dispatcher did NOT route correctly.',
    '`ambiguity_confidence_rate` is its restraint counterpart: tickets that state no routable fact, where',
    '`escalate` is the correct answer and any confident queue is a guess. `over_escalation_rate` is the',
    'price of that restraint, so an arm cannot win by escalating everything. `unparseable` routes stay in',
    'every denominator. `auditor_agreement` is how often the in-project FROZEN route-auditor agreed with',
    "this harness's own arithmetic — a scoreboard check, not the score.",
    '',
    '## Per ticket',
    '',
    '```',
    renderTicketGrid(report),
    '```',
    '',
    '## Prompt lineage (config log)',
    '',
  )
  for (const outcome of report.outcomes) {
    lines.push(`### ${outcome.arm}`, '')
    const writes = outcome.tickets.flatMap((tk) => tk.promptWrites.map((w) => ({ t: tk.id, w })))
    if (writes.length === 0) lines.push('_no prompt rewrites_', '')
    for (const { t, w } of writes) lines.push(`- \`${t}\` — \`${w.by}\` → \`${w.target}\`: ${w.rationale}`)
    if (writes.length > 0) lines.push('')
    if (outcome.abortReason) {
      lines.push(`**Aborted after ${outcome.abortedAfter ?? 0} tickets: ${outcome.abortReason}**`, '')
    }
  }
  return lines.join('\n')
}
