// jobprogress — the honest long-wait affordance, derived from the event spine
// (doc 21 §4.2 "Long agent jobs", §5-M6, item W6).
//
// The research's finding, restated: past ~10 seconds a spinner is inadequate,
// and a percentage is *dishonest* — agent work is unbounded and non-linear, so
// nothing on the client can know what fraction of it is done. What CAN be known
// truthfully is what the agent has already done: how many tool steps it has
// taken, and which one it is on. So the affordance is **step count + last-step
// label + elapsed**, and never a bar.
//
// FRONTEND ONLY, by construction. There is no `steps` column on a delivery and
// no progress route, but there does not need to be one: the job table already
// fetches `GET /agent/session/{id}/query-events` for its token totals, and that
// same payload carries every `tool_use_start` the session emitted. This module
// is the second reading of a request that was already being made — see
// `useSessionTokens`, which now returns both from one response. Adding a
// request per row to show progress would have cost more than the progress is
// worth; adding a backend field was out of scope for W6.
//
// The payload shapes are the two `sumTokens` already handles, for the same
// reason: `{ events: [ { …row, events: [envelope…] } ] }` from the AgentDB
// route, and a bare envelope list from the legacy one. The walk is generic
// rather than shape-pinned so neither reading can drift from the other.
//
// Pure. No clock, no React, no fetch.

import { getToolDisplayName, getToolSummary } from './tool-formatters.js'

/**
 * How long a job must run before it earns the step affordance (§4.2: "past
 * 10s a spinner is inadequate"). Under this, the elapsed alone is the whole
 * truth and a step line is noise.
 */
export const LONG_JOB_AFTER_SECONDS = 10

/** What one session has actually done, as far as its event stream says. */
export interface JobProgress {
  /** Tool calls started. This is the step count — the only countable unit of
   *  agent work the stream offers that is not a token. */
  steps: number
  /** The newest step's label, e.g. `Edit File` — '' when nothing has started. */
  lastStep: string
  /** The newest step's one-line detail (a path, a command), or ''. */
  lastStepDetail: string
  /** True when at least one step is still open (no matching `tool_use_end`). */
  active: boolean
  /**
   * What was produced: artifact/dashboard/webapp labels, newest last. §4.2's
   * completion rule — "report what was produced, not 'Done'".
   */
  produced: string[]
}

export const EMPTY_JOB_PROGRESS: JobProgress = {
  steps: 0,
  lastStep: '',
  lastStepDetail: '',
  active: false,
  produced: [],
}

interface Envelope {
  type: string
  data: Record<string, unknown>
}

/** Every `{type, data}` envelope anywhere in the response, in document order.
 *  The route nests them one level (a row per query) and the legacy one does
 *  not; a walk costs nothing at these sizes and cannot pick the wrong one. */
function collectEnvelopes(raw: unknown): Envelope[] {
  const out: Envelope[] = []
  const walk = (node: unknown): void => {
    if (Array.isArray(node)) {
      node.forEach(walk)
      return
    }
    if (!node || typeof node !== 'object') return
    const r = node as Record<string, unknown>
    if (typeof r.type === 'string') {
      const data = r.data
      out.push({
        type: r.type,
        data: (data && typeof data === 'object' ? data : {}) as Record<string, unknown>,
      })
      // Stop here on purpose. An envelope's `data` is a tool's own payload, and
      // tool inputs carry `type` keys of their own — recursing would count a
      // JSON-schema fragment as a step. No envelope nests another envelope
      // (a subagent's stream arrives as its own `subagent_event` envelopes).
      return
    }
    for (const value of Object.values(r)) {
      if (value && typeof value === 'object') walk(value)
    }
  }
  walk(raw)
  return out
}

/** A produced thing's name, from the three events that announce one. */
function producedLabel(env: Envelope): string {
  const d = env.data
  switch (env.type) {
    case 'artifact_registered': {
      const label = typeof d.label === 'string' ? d.label : ''
      const path = typeof d.filePath === 'string' ? d.filePath : ''
      return label || path.split('/').pop() || ''
    }
    case 'dashboard_created': {
      const title = typeof d.title === 'string' ? d.title : ''
      return title || 'a dashboard'
    }
    case 'webapp_ready': {
      const name = typeof d.name === 'string' ? d.name : ''
      return name || 'a web app'
    }
    default:
      return ''
  }
}

/**
 * Read a `query-events` response as progress.
 *
 * Steps count `tool_use_start`, not messages: a message is the model talking,
 * a tool call is the model *doing*, and "step 7 of an unknown number" is only
 * defensible about the second. `active` is the open-tool count, which is what
 * separates "working" from "stopped mid-answer" without consulting a clock.
 */
export function summariseJobProgress(raw: unknown): JobProgress {
  let steps = 0
  let lastStep = ''
  let lastStepDetail = ''
  const open = new Set<string>()
  const produced: string[] = []

  for (const env of collectEnvelopes(raw)) {
    switch (env.type) {
      case 'tool_use_start': {
        steps += 1
        const name = typeof env.data.toolName === 'string' ? env.data.toolName : ''
        const input =
          env.data.input && typeof env.data.input === 'object'
            ? (env.data.input as Record<string, unknown>)
            : {}
        lastStep = name === '' ? 'a tool' : getToolDisplayName(name, input)
        lastStepDetail = name === '' ? '' : getToolSummary(name, input)
        const id = typeof env.data.toolCallId === 'string' ? env.data.toolCallId : ''
        if (id !== '') open.add(id)
        break
      }
      case 'tool_use_end': {
        const id = typeof env.data.toolCallId === 'string' ? env.data.toolCallId : ''
        if (id !== '') open.delete(id)
        break
      }
      case 'artifact_registered':
      case 'dashboard_created':
      case 'webapp_ready': {
        const label = producedLabel(env)
        if (label !== '' && !produced.includes(label)) produced.push(label)
        break
      }
      default:
        break
    }
  }

  return { steps, lastStep, lastStepDetail, active: open.size > 0, produced }
}

/**
 * Does this row want the step affordance? Only a job that is genuinely still
 * going, and only once the wait is long enough to need explaining.
 *
 * `awaiting_human` is deliberately included: it is the longest wait on the
 * console, and "12 steps, then it asked you something" is exactly the sentence
 * that tells an operator whether to read the ask now.
 */
export function isLongRunningJob(status: string, elapsedSeconds: number | null | undefined): boolean {
  if (status !== 'running' && status !== 'awaiting_human') return false
  return (elapsedSeconds ?? 0) >= LONG_JOB_AFTER_SECONDS
}

/**
 * `step 7 · Edit File` — the line that replaces a percentage.
 *
 * "step N", never "N of M": there is no M, and inventing one is the exact
 * dishonesty §5-M6 names. Before the first tool call it says so plainly rather
 * than showing `step 0`, which reads as stalled.
 */
export function formatStepLine(progress: JobProgress): string {
  if (progress.steps === 0) return 'starting up'
  const head = `step ${progress.steps}`
  return progress.lastStep === '' ? head : `${head} · ${progress.lastStep}`
}

/**
 * What a finished job produced, in words — '' when it produced nothing
 * nameable, in which case the caller shows the session link alone rather than
 * a generic "Done" (§4.2, NN/g's long-wait guidance).
 */
export function formatProduced(progress: JobProgress): string {
  const n = progress.produced.length
  if (n === 0) return ''
  if (n === 1) return progress.produced[0]!
  if (n === 2) return `${progress.produced[0]} and ${progress.produced[1]}`
  return `${progress.produced[0]}, ${progress.produced[1]} and ${n - 2} more`
}

/**
 * The screen-reader sentence for a progress line, on the container whose digits
 * are `aria-hidden` — same discipline as `coarseAgeLabel`: it is refreshed
 * rarely, so it must stay true for a while.
 */
export function progressAriaLabel(progress: JobProgress): string {
  if (progress.steps === 0) return 'starting up'
  const steps = `${progress.steps} step${progress.steps === 1 ? '' : 's'} so far`
  return progress.lastStep === '' ? steps : `${steps}, most recently ${progress.lastStep}`
}

/**
 * How often a running row's progress is re-read from the route, in ms.
 *
 * Slow on purpose. The elapsed ticks per second off a shared timer and costs
 * nothing; a step count costs one HTTP request per row, and a step that lands
 * 30 seconds late is still the honest answer to "is it moving". A terminal row
 * is read once and never again.
 */
export const PROGRESS_REFRESH_MS = 30_000

/** The refresh key for a row: a bucket number that changes once per
 *  `PROGRESS_REFRESH_MS`, or `null` for a row that must never re-fetch. */
export function progressRefreshKey(
  status: string,
  nowMs: number | undefined,
  intervalMs: number = PROGRESS_REFRESH_MS,
): number | null {
  if (status !== 'running' && status !== 'awaiting_human') return null
  if (nowMs === undefined || !Number.isFinite(nowMs)) return null
  return Math.floor(nowMs / intervalMs)
}
