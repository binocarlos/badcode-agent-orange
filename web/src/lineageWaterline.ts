// lineageWaterline — GitHub's two transplants, applied to a worker's prompt
// history (doc 21 §4.2's last two feed patterns, §5-M5, item W6).
//
//   1. **"Changes since your last review" as a cumulative diff.** When a prompt
//      was rewritten more than once since the operator last looked, the diffs
//      they actually want is not three small ones — it is one diff from the
//      text they remember to the text that is live now. The per-revision diffs
//      stay underneath it: the cumulative view answers "what changed", the
//      per-revision ones answer "who changed it and why", and neither replaces
//      the other.
//
//   2. **A Viewed state that auto-invalidates.** Marking a version viewed is
//      only useful if the mark stops being true when the thing changes again —
//      "without invalidation, 'viewed' quietly becomes a lie" (§4.2). Here the
//      thing that changes is the worker's prompt, so every viewed mark is
//      stamped with the head version it was made against, and a new prompt
//      write clears them all. That is exactly GitHub's semantics ("viewed" on a
//      file, cleared by a new push) translated to a config log.
//
// The mark this reads is the operator's ONE watermark (`watermark.ts`), not a
// second one. §4.2's framing fact is that one integer derives every "since you
// last looked" rendering; a lineage that kept its own mark would be a second
// integer disagreeing with the first about when the operator last looked.
//
// Pure — every function here is (data, mark) → answer. Storage and React live
// in the two thin wrappers at the bottom.

import { useCallback, useState } from 'react'
import { diffLines, type ChangelogDiff, type LineageEntry, type WorkerLineage } from './configLog.js'

// ---------------------------------------------------------------------------
// The cumulative diff
// ---------------------------------------------------------------------------

/** "Changes since your last review", when there were enough of them to need it. */
export interface CumulativeLineageDiff {
  /** The version the operator last saw, or `null` when they saw none of them. */
  fromVersion: number | null
  /** The live version — what the worker is running now. */
  toVersion: number
  /** How many prompt writes landed after the mark. Always ≥ 2 (see below). */
  rewritesSince: number
  /** The one diff, baseline → head. */
  diff: ChangelogDiff
  /** The instant of the newest of those writes, unix ms — for the label. */
  latestAt: number
}

/**
 * Below this many prompt writes since the mark there is no cumulative view.
 *
 * With one rewrite the cumulative diff is byte-identical to that rewrite's own
 * per-revision diff, and rendering both would be the same information twice
 * wearing two different headings. With zero there is nothing to say at all.
 */
export const CUMULATIVE_MIN_REWRITES = 2

/** Prompt-carrying rows, oldest first — the version track under a lineage. */
function versionRows(lineage: WorkerLineage): LineageEntry[] {
  return lineage.entries
    .filter((row) => row.version !== null && row.prompt !== null)
    .slice()
    .reverse()
}

/**
 * The cumulative diff from the operator's watermark to head, or `null` when
 * the per-revision view is already the right one.
 *
 * `null` in three cases, all deliberate:
 *   - the mark is unset (0) — a first visit has no "since", and everything on
 *     screen is equally new;
 *   - fewer than `CUMULATIVE_MIN_REWRITES` writes landed after the mark;
 *   - the baseline and head texts are identical (a revert loop A→B→A: two
 *     rewrites, nothing to show, and a diff of zero lines would read as a bug).
 */
export function cumulativeLineageDiff(
  lineage: WorkerLineage,
  markMs: number,
): CumulativeLineageDiff | null {
  if (markMs <= 0) return null
  const rows = versionRows(lineage)
  if (rows.length === 0) return null

  const after = rows.filter((row) => row.entry.createdAt > markMs)
  if (after.length < CUMULATIVE_MIN_REWRITES) return null

  const head = rows[rows.length - 1]!
  // The newest version at or before the mark IS what the operator last read.
  // None means they had not seen this worker's prompt at all, and the baseline
  // is the empty text: "all of this is new" is the honest diff for that.
  const baselineRow = rows.filter((row) => row.entry.createdAt <= markMs).pop() ?? null
  const baseline = baselineRow?.prompt ?? ''
  if (baseline === head.prompt) return null

  const lines = diffLines(baseline, head.prompt ?? '')
  return {
    fromVersion: baselineRow?.version ?? null,
    toVersion: head.version!,
    rewritesSince: after.length,
    latestAt: head.entry.createdAt,
    diff: {
      previousEventId: baselineRow?.entry.id ?? '',
      lines,
      added: lines.filter((l) => l.type === 'add').length,
      removed: lines.filter((l) => l.type === 'del').length,
    },
  }
}

/** `3 rewrites since you last looked · v2 → v5`, the banner's own heading.
 *  Says both numbers because they answer different questions, and neither can
 *  be inferred from the other once a revert is in the history. */
export function cumulativeHeading(cumulative: CumulativeLineageDiff): string {
  const n = cumulative.rewritesSince
  const from = cumulative.fromVersion === null ? 'nothing you had seen' : `v${cumulative.fromVersion}`
  return `${n} rewrites since you last looked · ${from} → v${cumulative.toVersion}`
}

// ---------------------------------------------------------------------------
// Viewed, and its invalidation
// ---------------------------------------------------------------------------

/**
 * What is remembered about "viewed": which versions were marked, and the head
 * they were marked against. The second field is the whole mechanism — without
 * it there is no way to know the marks have gone stale.
 */
export interface ViewedState {
  /** The lineage's newest prompt-write event id when these marks were made. */
  headEventId: string
  /** Config-event ids of the versions marked viewed. */
  viewed: string[]
}

export const EMPTY_VIEWED: ViewedState = { headEventId: '', viewed: [] }

/** The head a viewed mark is stamped against: the newest prompt write. '' when
 *  the worker has no prompt history at all. */
export function lineageHeadEventId(lineage: WorkerLineage): string {
  for (const row of lineage.entries) {
    if (row.version !== null && row.prompt !== null) return row.entry.id
  }
  return ''
}

/**
 * The marks that are still true, given the head now.
 *
 * A different head means the prompt changed after those marks were made, so
 * every one of them is stale and ALL of them clear — not just the newest. The
 * operator's "I have read this worker" is a statement about the worker as it
 * was, and the whole statement expires together. Keeping the old marks and
 * only un-viewing the new version is the version of this feature that lies.
 */
export function viewedVersions(state: ViewedState, headEventId: string): Set<string> {
  if (state.headEventId === '' || state.headEventId !== headEventId) return new Set()
  return new Set(state.viewed)
}

/** True when this version is marked viewed AND the mark is still valid. */
export function isVersionViewed(
  state: ViewedState,
  headEventId: string,
  eventId: string,
): boolean {
  return viewedVersions(state, headEventId).has(eventId)
}

/**
 * Mark (or unmark) a version viewed, against the current head.
 *
 * Re-stamping on every write is what performs the invalidation: marking
 * anything under a new head silently drops the marks made under the old one,
 * so the stale set can never be resurrected by a later mark.
 */
export function markVersionViewed(
  state: ViewedState,
  headEventId: string,
  eventId: string,
  viewed: boolean = true,
): ViewedState {
  const live = viewedVersions(state, headEventId)
  if (viewed) live.add(eventId)
  else live.delete(eventId)
  return { headEventId, viewed: [...live] }
}

// ---------------------------------------------------------------------------
// Storage (the same shape as watermark.ts's, for the same reasons)
// ---------------------------------------------------------------------------

/** `agentkit.lineage.viewed.<project>.<worker>` */
export function viewedKey(projectId: string, workerName: string): string {
  return `agentkit.lineage.viewed.${projectId}.${workerName}`
}

export function readViewedState(projectId: string, workerName: string): ViewedState {
  try {
    const raw = globalThis.localStorage?.getItem(viewedKey(projectId, workerName))
    if (!raw) return EMPTY_VIEWED
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object') return EMPTY_VIEWED
    const p = parsed as { headEventId?: unknown; viewed?: unknown }
    return {
      headEventId: typeof p.headEventId === 'string' ? p.headEventId : '',
      viewed: Array.isArray(p.viewed) ? p.viewed.filter((v): v is string => typeof v === 'string') : [],
    }
  } catch {
    // Unparseable storage means "nothing viewed", never a thrown page.
    return EMPTY_VIEWED
  }
}

export function writeViewedState(projectId: string, workerName: string, state: ViewedState): void {
  try {
    globalThis.localStorage?.setItem(viewedKey(projectId, workerName), JSON.stringify(state))
  } catch {
    /* private mode, quota, no storage — the console still works, it just forgets. */
  }
}

// ---------------------------------------------------------------------------
// The hook
// ---------------------------------------------------------------------------

export interface ViewedVersionsApi {
  /** Marked viewed, and the mark is still valid under the current head. */
  isViewed: (eventId: string) => boolean
  /** Flip one version's mark, and persist. */
  toggle: (eventId: string) => void
  /** How many marks are live — 0 the moment the prompt changes again. */
  count: number
}

/**
 * Viewed marks for one worker, invalidated by `headEventId`.
 *
 * Unlike the watermark this is NOT frozen for the visit: the whole point is
 * that it reacts the instant the head moves. It does so without an effect —
 * validity is decided at read time by `viewedVersions`, so a lineage that
 * refetches and finds a new rewrite renders the marks gone in the same pass.
 */
export function useViewedVersions(
  projectId: string,
  workerName: string,
  headEventId: string,
): ViewedVersionsApi {
  const key = viewedKey(projectId, workerName)
  const [held, setHeld] = useState(() => ({ key, state: readViewedState(projectId, workerName) }))

  // Render-phase re-read on a key change — the pattern useFeedWatermark uses,
  // so switching worker cannot render one frame of another worker's marks.
  if (held.key !== key) setHeld({ key, state: readViewedState(projectId, workerName) })

  const state = held.key === key ? held.state : EMPTY_VIEWED

  const toggle = useCallback(
    (eventId: string) => {
      setHeld((prev) => {
        const current = prev.key === key ? prev.state : EMPTY_VIEWED
        const next = markVersionViewed(
          current,
          headEventId,
          eventId,
          !isVersionViewed(current, headEventId, eventId),
        )
        writeViewedState(projectId, workerName, next)
        return { key, state: next }
      })
    },
    [headEventId, key, projectId, workerName],
  )

  return {
    isViewed: (eventId: string) => isVersionViewed(state, headEventId, eventId),
    toggle,
    count: viewedVersions(state, headEventId).size,
  }
}

export default useViewedVersions
