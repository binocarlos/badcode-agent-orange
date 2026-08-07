// Topologies — the browser-side mirror of the `/agent/topologies` routes
// (work plan 13 T2/T3; engine: go/topology, go/httpapi/topologies.go).
//
// Pure: no React, no window, no fetch. The hook lives in useTopologies.ts and
// the flow component in components/TopologyOnboarding.tsx.
//
// Validation policy: mirror the engine, do not out-legislate it. The server's
// ResolveAnswers enforces exactly three things a form can trip over — a
// required question left unanswered (with no default), an answer of the wrong
// type, and a choice outside the question's Choices — so this module enforces
// exactly those. The form's controls make wrong types unrepresentable, which
// leaves "required and blank" and "not one of the choices" as the two errors
// worth naming before the server does.

import { coerceWorker, type Worker } from './workers.js'
import { coerceSubscription, type Subscription } from './events.js'
import { coerceSchedule, type Schedule } from './schedules.js'
import { coerceConfigEvent, type ConfigEvent } from './configLog.js'

/** Endpoint paths for the topology routes. Overridable per host, like
 *  WORKER_ENDPOINTS. */
export const TOPOLOGY_ENDPOINTS = {
  list: '/agent/topologies',
  preview: '/agent/topologies/preview',
  apply: '/agent/topologies/apply',
}

/** The closed vocabulary of answer shapes (go/topology QuestionType). */
export type TopologyQuestionType = 'string' | 'bool' | 'choice'

/** One thing a topology asks before it can render. Field names are the wire
 *  names. `default` is null when the question has none — the wire omits it. */
export interface TopologyQuestion {
  id: string
  prompt: string
  type: TopologyQuestionType
  /** Populated for choice questions only. */
  choices: string[]
  default: string | boolean | null
  required: boolean
}

/** One catalogue entry: a named, versioned org chart (D1: defined in code).
 *  Identity is name@version, same naming posture as images and skills. */
export interface Topology {
  name: string
  version: string
  description: string
  questions: TopologyQuestion[]
}

/** The topology's name@version identity string, e.g. "solo@v1". */
export function topologyRef(t: Pick<Topology, 'name' | 'version'>): string {
  return `${t.name}@${t.version}`
}

/** Question ID → the answer the form holds. Strings for string/choice
 *  questions ('' = unanswered), booleans for bool questions. */
export type TopologyAnswers = Record<string, string | boolean>

/** One would-be subscription in the preview diff. */
export interface TopologyRouteSummary {
  event_type: string
  worker: string
}

/** One would-be schedule in the preview diff. */
export interface TopologyCronSummary {
  cron: string
  worker: string
  input: string
}

/** The preview against the project's CURRENT config: what would be created,
 *  and which worker names are already taken (workers are the one name-keyed
 *  kind, hence the one that can collide). */
export interface TopologyDiff {
  new_workers: string[]
  colliding_workers: string[]
  new_subscriptions: TopologyRouteSummary[]
  new_schedules: TopologyCronSummary[]
  /** Project-settings fields the bundle's patch would set. */
  settings_fields: string[]
  memory_seeds: number
}

/** One rendered org chart: rows of the existing config types, project-agnostic
 *  (apply stamps project/IDs/timestamps). Mirrors go/topology.Bundle. */
export interface TopologyBundle {
  workers: Worker[]
  subscriptions: Subscription[]
  schedules: Schedule[]
  settings_patch: Record<string, unknown> | null
  memory_seeds: unknown[]
  preconditions: { images: string[]; skills: string[] }
}

/** The whole preview response. `applicable` is the one-word verdict: no
 *  collisions and no missing preconditions — apply MUST be blocked when false. */
export interface TopologyPreview {
  topology: Topology
  bundle: TopologyBundle
  diff: TopologyDiff
  missing_images: string[]
  missing_skills: string[]
  applicable: boolean
}

/** Everything the apply created, read back (§9), plus the bracketing
 *  `topology_apply` config event — the receipt. */
export interface TopologyApplyResult {
  workers: Worker[]
  subscriptions: Subscription[]
  schedules: Schedule[]
  settings: Record<string, unknown> | null
  memories: unknown[]
  event: ConfigEvent
}

// ---------------------------------------------------------------------------
// Coercion — fill anything the server omitted so components never branch on
// undefined, the same posture as coerceWorker.
// ---------------------------------------------------------------------------

const str = (v: unknown, fallback = ''): string => (typeof v === 'string' ? v : fallback)
const strings = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : [])
const record = (v: unknown): Record<string, unknown> | null =>
  v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null

export function coerceTopologyQuestion(raw: unknown): TopologyQuestion {
  const r = record(raw) ?? {}
  const type = r.type === 'bool' || r.type === 'choice' ? r.type : 'string'
  return {
    id: str(r.id),
    prompt: str(r.prompt),
    type,
    choices: strings(r.choices),
    default: typeof r.default === 'string' || typeof r.default === 'boolean' ? r.default : null,
    required: r.required === true,
  }
}

export function coerceTopology(raw: unknown): Topology {
  const r = record(raw) ?? {}
  return {
    name: str(r.name),
    version: str(r.version),
    description: str(r.description),
    questions: Array.isArray(r.questions) ? r.questions.map(coerceTopologyQuestion) : [],
  }
}

export function coerceTopologyDiff(raw: unknown): TopologyDiff {
  const r = record(raw) ?? {}
  return {
    new_workers: strings(r.new_workers),
    colliding_workers: strings(r.colliding_workers),
    new_subscriptions: Array.isArray(r.new_subscriptions)
      ? r.new_subscriptions.map((s) => {
          const e = record(s) ?? {}
          return { event_type: str(e.event_type), worker: str(e.worker) }
        })
      : [],
    new_schedules: Array.isArray(r.new_schedules)
      ? r.new_schedules.map((s) => {
          const e = record(s) ?? {}
          return { cron: str(e.cron), worker: str(e.worker), input: str(e.input) }
        })
      : [],
    settings_fields: strings(r.settings_fields),
    memory_seeds: typeof r.memory_seeds === 'number' ? r.memory_seeds : 0,
  }
}

export function coerceTopologyBundle(raw: unknown): TopologyBundle {
  const r = record(raw) ?? {}
  const pre = record(r.preconditions) ?? {}
  return {
    workers: Array.isArray(r.workers) ? r.workers.map((w) => coerceWorker(w)) : [],
    subscriptions: Array.isArray(r.subscriptions) ? r.subscriptions.map(coerceSubscription) : [],
    schedules: Array.isArray(r.schedules) ? r.schedules.map((s) => coerceSchedule(s)) : [],
    settings_patch: record(r.settings_patch),
    memory_seeds: Array.isArray(r.memory_seeds) ? r.memory_seeds : [],
    preconditions: { images: strings(pre.images), skills: strings(pre.skills) },
  }
}

export function coerceTopologyPreview(raw: unknown): TopologyPreview {
  const r = record(raw) ?? {}
  return {
    topology: coerceTopology(r.topology),
    bundle: coerceTopologyBundle(r.bundle),
    diff: coerceTopologyDiff(r.diff),
    missing_images: strings(r.missing_images),
    missing_skills: strings(r.missing_skills),
    // Absent/garbled reads as NOT applicable — the failure mode must block
    // apply, never wave it through.
    applicable: r.applicable === true,
  }
}

export function coerceTopologyApplyResult(raw: unknown): TopologyApplyResult {
  const r = record(raw) ?? {}
  return {
    workers: Array.isArray(r.workers) ? r.workers.map((w) => coerceWorker(w)) : [],
    subscriptions: Array.isArray(r.subscriptions) ? r.subscriptions.map(coerceSubscription) : [],
    schedules: Array.isArray(r.schedules) ? r.schedules.map((s) => coerceSchedule(s)) : [],
    settings: record(r.settings),
    memories: Array.isArray(r.memories) ? r.memories : [],
    event: coerceConfigEvent(r.event),
  }
}

// ---------------------------------------------------------------------------
// The answer form
// ---------------------------------------------------------------------------

/**
 * The form state a topology's questions start from: defaults pre-filled, bool
 * questions seeded false (a switch always shows a definite state), string and
 * choice questions '' (which means "unanswered" — see topologyAnswersBody).
 */
export function initialTopologyAnswers(questions: TopologyQuestion[]): TopologyAnswers {
  const answers: TopologyAnswers = {}
  for (const q of questions) {
    if (q.default !== null) answers[q.id] = q.default
    else if (q.type === 'bool') answers[q.id] = false
    else answers[q.id] = ''
  }
  return answers
}

/** Question ID → human-readable problem. Empty when the form can be sent. */
export type TopologyAnswerErrors = Record<string, string>

/**
 * The two form-level failures the server would refuse: a required string or
 * choice question left blank, and a choice answer outside the choices (which a
 * select cannot produce, but a host driving answers programmatically can).
 * Bool questions cannot be invalid: false is an answer, not an absence.
 */
export function validateTopologyAnswers(
  questions: TopologyQuestion[],
  answers: TopologyAnswers,
): TopologyAnswerErrors {
  const errors: TopologyAnswerErrors = {}
  for (const q of questions) {
    const v = answers[q.id]
    if (q.type === 'bool') continue
    const text = typeof v === 'string' ? v.trim() : ''
    if (text === '') {
      if (q.required) errors[q.id] = 'an answer is required'
      continue
    }
    if (q.type === 'choice' && !q.choices.includes(text)) {
      errors[q.id] = `must be one of: ${q.choices.join(', ')}`
    }
  }
  return errors
}

/**
 * The wire `answers` object for preview/apply: bool answers verbatim, string
 * and choice answers trimmed — and omitted when blank, because the server
 * distinguishes "unanswered" (default applies, optional passes) from "answered
 * with the empty string" (an answer the renderer must swallow).
 */
export function topologyAnswersBody(
  questions: TopologyQuestion[],
  answers: TopologyAnswers,
): Record<string, string | boolean> {
  const body: Record<string, string | boolean> = {}
  for (const q of questions) {
    const v = answers[q.id]
    if (q.type === 'bool') {
      if (typeof v === 'boolean') body[q.id] = v
      continue
    }
    const text = typeof v === 'string' ? v.trim() : ''
    if (text !== '') body[q.id] = text
  }
  return body
}
