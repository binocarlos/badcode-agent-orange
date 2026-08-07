// @vitest-environment jsdom
// W6 / doc 21 §4.2, §5-M5: GitHub's two transplants on a worker's lineage —
// the cumulative "changes since your last review" diff, and a Viewed state that
// auto-invalidates. The second test group is the important one: an invalidation
// that does not fire turns "viewed" into a lie, which is worse than not having
// the feature.

import { describe, it, expect, beforeEach } from 'vitest'
import { buildChangelog, workerLineage, type ConfigEvent } from './configLog.js'
import {
  CUMULATIVE_MIN_REWRITES,
  EMPTY_VIEWED,
  cumulativeHeading,
  cumulativeLineageDiff,
  isVersionViewed,
  lineageHeadEventId,
  markVersionViewed,
  readViewedState,
  viewedKey,
  viewedVersions,
  writeViewedState,
} from './lineageWaterline.js'

const WORKER = 'email-answerer'

/** A prompt write, at a given unix-ms instant. */
const write = (id: string, at: number, prompt: string): ConfigEvent => ({
  id,
  project: 'acme',
  actor_worker: 'email-reviewer',
  actor_session: `sess-${id}`,
  action: 'worker_prompt_write',
  payload: { name: WORKER, system_prompt: prompt },
  rationale: `because ${id}`,
  created_at: at,
})

function lineageOf(events: ConfigEvent[]) {
  // buildChangelog wants newest-first, as the route serves it.
  const newestFirst = events.slice().sort((a, b) => b.created_at - a.created_at)
  return workerLineage(buildChangelog(newestFirst), WORKER)
}

const V1 = 'line one\nline two'
const V2 = 'line one\nline two changed'
const V3 = 'line one\nline two changed\nline three'
const V4 = 'line one\nline four'

describe('cumulativeLineageDiff', () => {
  it('is null when the operator has never looked', () => {
    const lineage = lineageOf([write('c1', 1000, V1), write('c2', 2000, V2), write('c3', 3000, V3)])
    expect(cumulativeLineageDiff(lineage, 0)).toBeNull()
  })

  it('is null with one rewrite since the mark — the per-revision diff already says it', () => {
    const lineage = lineageOf([write('c1', 1000, V1), write('c2', 2000, V2)])
    expect(cumulativeLineageDiff(lineage, 1500)).toBeNull()
    expect(CUMULATIVE_MIN_REWRITES).toBe(2)
  })

  it('diffs the prompt the operator last saw against the live one', () => {
    const lineage = lineageOf([
      write('c1', 1000, V1),
      write('c2', 2000, V2),
      write('c3', 3000, V3),
    ])
    const cum = cumulativeLineageDiff(lineage, 1500)!
    expect(cum).not.toBeNull()
    expect(cum.fromVersion).toBe(1)
    expect(cum.toVersion).toBe(3)
    expect(cum.rewritesSince).toBe(2)
    expect(cum.latestAt).toBe(3000)
    // v1 → v3, in ONE diff: not the two intermediate ones stacked.
    const added = cum.diff.lines.filter((l) => l.type === 'add').map((l) => l.text)
    const removed = cum.diff.lines.filter((l) => l.type === 'del').map((l) => l.text)
    expect(added).toEqual(['line two changed', 'line three'])
    expect(removed).toEqual(['line two'])
    expect(cum.diff.added).toBe(2)
    expect(cum.diff.removed).toBe(1)
    expect(cum.diff.previousEventId).toBe('c1')
  })

  it('is null when a revert loop left the live prompt where the operator left it', () => {
    // A→B→A since the mark is two rewrites and zero changes. A banner over an
    // empty diff would read as a rendering bug.
    const lineage = lineageOf([write('c1', 1000, V1), write('c2', 2000, V2), write('c3', 3000, V1)])
    expect(cumulativeLineageDiff(lineage, 1500)).toBeNull()
  })

  it('treats "saw none of them" as a diff from nothing', () => {
    const lineage = lineageOf([write('c1', 1000, V1), write('c2', 2000, V2)])
    const cum = cumulativeLineageDiff(lineage, 500)!
    expect(cum.fromVersion).toBeNull()
    expect(cum.diff.removed).toBe(0)
    expect(cumulativeHeading(cum)).toBe('2 rewrites since you last looked · nothing you had seen → v2')
  })

  it('heads the banner with both numbers', () => {
    const lineage = lineageOf([
      write('c1', 1000, V1),
      write('c2', 2000, V2),
      write('c3', 3000, V3),
      write('c4', 4000, V4),
    ])
    const cum = cumulativeLineageDiff(lineage, 1500)!
    expect(cum.rewritesSince).toBe(3)
    expect(cumulativeHeading(cum)).toBe('3 rewrites since you last looked · v1 → v4')
  })

  it('ignores non-prompt events in the mark arithmetic', () => {
    const freeze: ConfigEvent = {
      id: 'f1',
      project: 'acme',
      actor_worker: '',
      actor_session: '',
      action: 'worker_freeze',
      payload: { name: WORKER },
      rationale: 'held',
      created_at: 2500,
    }
    const lineage = lineageOf([write('c1', 1000, V1), freeze, write('c2', 3000, V2)])
    // One prompt write after the mark, plus a freeze — still one rewrite.
    expect(cumulativeLineageDiff(lineage, 1500)).toBeNull()
  })
})

describe('Viewed, and its auto-invalidation', () => {
  it('remembers a mark while the prompt is unchanged', () => {
    const head = 'c3'
    const state = markVersionViewed(EMPTY_VIEWED, head, 'c2')
    expect(isVersionViewed(state, head, 'c2')).toBe(true)
    expect(isVersionViewed(state, head, 'c1')).toBe(false)
  })

  it('CLEARS every mark the moment the prompt changes again', () => {
    // The whole point (§4.2): without this, "viewed" quietly becomes a lie.
    let state = markVersionViewed(EMPTY_VIEWED, 'c3', 'c2')
    state = markVersionViewed(state, 'c3', 'c3')
    expect(viewedVersions(state, 'c3').size).toBe(2)

    // A new rewrite lands: head moves to c4.
    expect(viewedVersions(state, 'c4').size).toBe(0)
    expect(isVersionViewed(state, 'c4', 'c2')).toBe(false)
    expect(isVersionViewed(state, 'c4', 'c3')).toBe(false)
  })

  it('cannot resurrect stale marks by marking something under the new head', () => {
    let state = markVersionViewed(EMPTY_VIEWED, 'c3', 'c2')
    state = markVersionViewed(state, 'c4', 'c4')
    expect(viewedVersions(state, 'c4')).toEqual(new Set(['c4']))
    expect(state.headEventId).toBe('c4')
  })

  it('unmarks', () => {
    const state = markVersionViewed(markVersionViewed(EMPTY_VIEWED, 'c3', 'c2'), 'c3', 'c2', false)
    expect(isVersionViewed(state, 'c3', 'c2')).toBe(false)
  })

  it('never claims viewed when there is no head to stamp against', () => {
    expect(viewedVersions({ headEventId: '', viewed: ['c2'] }, '').size).toBe(0)
  })

  it('takes its head from the newest PROMPT write, not the newest event', () => {
    const freeze: ConfigEvent = {
      id: 'f1',
      project: 'acme',
      actor_worker: '',
      actor_session: '',
      action: 'worker_freeze',
      payload: { name: WORKER },
      rationale: 'held',
      created_at: 9000,
    }
    const lineage = lineageOf([write('c1', 1000, V1), write('c2', 2000, V2), freeze])
    // Freezing a worker does not change its prompt, so it must not invalidate.
    expect(lineageHeadEventId(lineage)).toBe('c2')
    expect(lineageHeadEventId(lineageOf([]))).toBe('')
  })
})

describe('viewed storage', () => {
  beforeEach(() => {
    globalThis.localStorage?.clear?.()
  })

  it('round-trips through localStorage under a per-worker key', () => {
    expect(viewedKey('acme', WORKER)).toBe(`agentkit.lineage.viewed.acme.${WORKER}`)
    writeViewedState('acme', WORKER, { headEventId: 'c3', viewed: ['c2'] })
    expect(readViewedState('acme', WORKER)).toEqual({ headEventId: 'c3', viewed: ['c2'] })
    // Another worker's marks are its own.
    expect(readViewedState('acme', 'copy-editor')).toEqual(EMPTY_VIEWED)
  })

  it('reads rubbish as "nothing viewed" rather than throwing a page away', () => {
    globalThis.localStorage?.setItem(viewedKey('acme', WORKER), '{not json')
    expect(readViewedState('acme', WORKER)).toEqual(EMPTY_VIEWED)
    globalThis.localStorage?.setItem(viewedKey('acme', WORKER), '{"viewed": [1, "c2"]}')
    expect(readViewedState('acme', WORKER)).toEqual({ headEventId: '', viewed: ['c2'] })
  })
})
