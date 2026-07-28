// learning.ts — the before/after join (design §7.2: "the product's thesis on
// one screen").
//
// The acceptance loop's whole claim is that the composed prompt changed the
// model's behaviour. Every column of the proof is data we already hold: a
// `worker_prompt_write` config event, and the two neighbouring `worker.finished`
// events for the *subject* worker — the last one before the rewrite and the
// first one after it.
//
// Pure: no React, no window, no fetch. The component is
// components/BeforeAfterView.tsx and it is mounted from the lineage rows.
//
// Two joins, both easy to get subtly wrong, so they are stated here once:
//
//   * The subject worker is the worker the config event *changed*
//     (`configEntity(ev).name`), never `actor_worker` — in the acceptance loop
//     those differ, and that is the whole point: a reviewer rewrote an answerer.
//   * Config events carry unix MILLISECONDS (J1) and project events carry unix
//     SECONDS. Comparing them raw puts every job on the same side of every
//     rewrite. Everything below compares in milliseconds.
//
// What this cannot show is stated in CAVEAT and rendered, not hidden: a
// `worker.finished` event's text is what the worker *said*; its tool calls are
// not in there.

import { configEntity, type ConfigEvent } from './configLog.js'
import type { ProjectEvent } from './events.js'

/** The event type a finished job emits — the transcript-carrying one (§8.2). */
export const FINISHED_EVENT_TYPE = 'worker.finished'

/**
 * Rendered verbatim on every before/after view (design §7.2). It is a limit of
 * the data, not of the screen, so it is never conditional and never dismissable.
 */
export const BEFORE_AFTER_CAVEAT =
  'Tool calls are absent from these transcripts — this shows what the worker said, never what it did.'

/** Shown in place of a column that has no job yet. */
export const NO_JOB_YET = 'no job has run since'

/** One side of the screen: a finished job, or nothing yet. */
export interface BeforeAfterSide {
  /** The `worker.finished` event, or null when that side hasn't happened. */
  event: ProjectEvent | null
  /** The transcript text, '' when there is no event. */
  text: string
  /** Unix milliseconds, or null. */
  atMs: number | null
}

/** The three columns of §7.2, resolved against one rewrite. */
export interface BeforeAfter {
  /** The worker whose prompt was rewritten — the subject, not the actor. */
  workerName: string
  /** The rewrite itself. */
  configEvent: ConfigEvent
  /** Unix milliseconds — when the prompt was rewritten. */
  atMs: number
  /** The worker that made the rewrite; '' when a human did. */
  actorWorker: string
  /** The mandatory why (§15.5). May be '' on human edits made over HTTP. */
  rationale: string
  /** The last job that finished before the rewrite. */
  before: BeforeAfterSide
  /** The first job that finished after it. */
  after: BeforeAfterSide
  /** False when the config event did not rewrite a prompt at all. */
  applicable: boolean
}

const EMPTY_SIDE: BeforeAfterSide = { event: null, text: '', atMs: null }

const side = (event: ProjectEvent | null): BeforeAfterSide =>
  event === null
    ? { ...EMPTY_SIDE }
    : { event, text: event.text, atMs: eventMs(event) }

/** A project event's moment in milliseconds; `occurred_at` is unix seconds. */
export function eventMs(event: ProjectEvent): number {
  return (event.occurred_at || event.created_at) * 1000
}

/**
 * Resolve the three columns for one prompt rewrite.
 *
 * `events` may be the whole recent-events list in any order — it is filtered to
 * the subject worker's `worker.finished` events and sorted here, because the
 * caller's list is whatever `/agent/events` last returned.
 *
 * Ties: a job finishing in the same millisecond as the write is counted as
 * *before* it. The write is caused by a job, so the boundary belongs on the
 * side that already happened.
 *
 * Either side is null when it hasn't happened yet — a brand new rewrite has no
 * after, and a worker's first jobs may all post-date its seeding. Callers
 * render NO_JOB_YET, never a blank.
 */
export function beforeAfter(configEvent: ConfigEvent, events: ProjectEvent[]): BeforeAfter {
  const entity = configEntity(configEvent)
  const workerName = entity.kind === 'worker' ? entity.name : ''
  const atMs = configEvent.created_at
  const applicable = configEvent.action === 'worker_prompt_write' && workerName !== ''

  const mine =
    workerName === ''
      ? []
      : events
          .filter(
            (e) => e.type === FINISHED_EVENT_TYPE && e.envelope.worker === workerName,
          )
          .slice()
          .sort((a, b) => eventMs(a) - eventMs(b))

  let before: ProjectEvent | null = null
  let after: ProjectEvent | null = null
  for (const e of mine) {
    if (eventMs(e) <= atMs) before = e
    else if (after === null) after = e
  }

  return {
    workerName,
    configEvent,
    atMs,
    actorWorker: configEvent.actor_worker,
    rationale: configEvent.rationale,
    before: side(before),
    after: side(after),
    applicable,
  }
}
