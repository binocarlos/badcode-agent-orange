// useDesk — everything the Desk reads, in one hook (design §5.2).
//
// The Desk decides nothing and stores nothing on the server: it is a query over
// `event_deliveries`, `attention_requests`, `config_events`, `project_events`
// and the schedule rows, folded by the pure `buildDesk` in desk.ts. This hook
// is only the plumbing — the five existing read hooks, one clock, and one
// high-water mark.
//
// The high-water mark ("since you last looked") is a `localStorage` entry keyed
// by project, per §5.2: no server state, no read receipts, and switching
// project switches the mark with it. A host with no `localStorage` (SSR, a
// locked-down browser) silently gets 0, which shows everything fetched — a
// first visit is not an empty screen.
//
// When `GET /agent/attention-requests` is not mounted the Asks stack still
// renders: each parked delivery becomes a message-less ask, so the operator
// sees *that* something is waiting even where the sentence the worker wrote
// cannot be fetched. That degradation is named in the API (`asksHaveMessages`)
// so the page can say so rather than implying the workers said nothing.

import { useCallback, useMemo, useState } from 'react'
import type { ConfigApiOptions } from './configApi.js'
import { buildDesk, type AttentionRequest, type Desk } from './desk.js'
import useAttentionRequests from './useAttentionRequests.js'
import useConfigLog from './useConfigLog.js'
import useEventsOverview from './useEvents.js'
import useSchedules from './useSchedules.js'
import useWorkers from './useWorkers.js'
import type { EventDelivery } from './events.js'

/** `localStorage` key for the "since you last looked" mark, per project. */
export function deskLastSeenKey(projectId: string): string {
  return `agentkit.desk.lastSeen.${projectId}`
}

/** Read the mark. Unreadable storage and rubbish values both mean 0. */
export function readDeskLastSeen(projectId: string): number {
  try {
    const raw = globalThis.localStorage?.getItem(deskLastSeenKey(projectId))
    const ms = raw === null || raw === undefined ? 0 : Number(raw)
    return Number.isFinite(ms) && ms > 0 ? ms : 0
  } catch {
    return 0
  }
}

/** Write the mark. A storage that refuses is not an error the operator needs. */
export function writeDeskLastSeen(projectId: string, ms: number): void {
  try {
    globalThis.localStorage?.setItem(deskLastSeenKey(projectId), String(ms))
  } catch {
    /* private mode, quota, no storage — the Desk still works, it just repeats. */
  }
}

/**
 * Parked deliveries as stand-in requests, for a host without the read route.
 *
 * Every field the sentence would carry is empty, deliberately: the ask renders
 * with its worker, its age and its thread link, and no invented message.
 *
 * Exported for `useAsksCount`, which needs the same degradation: on a host
 * without the attention route the badge counts parked deliveries, which is what
 * the Desk lists there.
 */
export function deliveriesAsRequests(deliveries: EventDelivery[]): AttentionRequest[] {
  return deliveries
    .filter((d) => d.status === 'awaiting_human' && d.session_id !== '')
    .map((d) => ({
      id: `delivery:${d.id}`,
      project: d.project,
      session_id: d.session_id,
      worker: '',
      message: '',
      session_url: '',
      channel: '',
      delivered: false,
      expires_at: 0,
      created_at: d.started_at || d.created_at,
      answered_at: 0,
      timed_out_at: 0,
    }))
}

export interface UseDeskOptions extends ConfigApiOptions {
  /** Project id — the high-water mark's key, and the permalink builder's. */
  projectId?: string
  /** Rows per underlying list. Passed straight to useEventsOverview. */
  limit?: number
  /** Clock in unix seconds. Injectable so ages are testable. */
  nowSeconds?: number
  /**
   * Override the high-water mark instead of reading `localStorage` (tests, and
   * a host that keeps the mark itself).
   */
  lastSeenMs?: number
}

export interface DeskApi {
  desk: Desk
  /** True while any of the underlying lists is still loading. */
  loading: boolean
  /** The first failure across the loads, as the server phrased it. */
  error: string | null
  /** False when the attention route is not mounted — asks carry no message. */
  asksHaveMessages: boolean
  /** The project's workers, for the first-run state. */
  workerCount: number
  /** Unix MILLISECONDS; 0 means "never looked". */
  lastSeenMs: number
  /** Mark everything currently shown as seen, from now on. */
  markSeen: () => void
  /** Reload every underlying list. */
  reload: () => Promise<void>
}

export default function useDesk(options: UseDeskOptions = {}): DeskApi {
  const { projectId = '', limit, nowSeconds, lastSeenMs: lastSeenOverride } = options

  const overview = useEventsOverview({ ...options, limit, nowSeconds })
  const attention = useAttentionRequests(options)
  const log = useConfigLog({ ...options, projectId })
  const schedules = useSchedules(options)
  const workers = useWorkers(options)

  // Read once, at mount, per project: a mark that re-read on every render would
  // clear the Changes stack the moment anything else re-rendered.
  const [storedLastSeen, setStoredLastSeen] = useState(() => readDeskLastSeen(projectId))
  const lastSeenMs = lastSeenOverride ?? storedLastSeen

  const markSeen = useCallback(() => {
    const now = Date.now()
    writeDeskLastSeen(projectId, now)
    setStoredLastSeen(now)
  }, [projectId])

  const reload = useCallback(async () => {
    await Promise.all([
      overview.reload(),
      attention.reload(),
      log.reload(),
      schedules.reload(),
      workers.reload(),
    ])
  }, [attention, log, overview, schedules, workers])

  const desk = useMemo(
    () =>
      buildDesk({
        deliveries: overview.deliveries,
        events: overview.events,
        subscriptions: overview.subscriptions,
        configEvents: log.events,
        attentionRequests: attention.available
          ? attention.requests
          : deliveriesAsRequests(overview.deliveries),
        schedules: schedules.schedules,
        nowSeconds: nowSeconds ?? Math.floor(Date.now() / 1000),
        lastSeenMs,
        projectId,
      }),
    [
      attention.available,
      attention.requests,
      lastSeenMs,
      log.events,
      nowSeconds,
      overview.deliveries,
      overview.events,
      overview.subscriptions,
      projectId,
      schedules.schedules,
    ],
  )

  return {
    desk,
    loading:
      overview.loading || attention.loading || log.loading || schedules.loading || workers.loading,
    // The changelog and the attention route each report "not mounted here"
    // through their own flag; only real failures belong in `error`.
    error: overview.error ?? (log.available ? log.error : null) ?? schedules.error ?? workers.error,
    asksHaveMessages: attention.available,
    workerCount: workers.workers.length,
    lastSeenMs,
    markSeen,
    reload,
  }
}
