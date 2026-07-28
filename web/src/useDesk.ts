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
// W4 moved that mark into `watermark.ts` unchanged — same key, same semantics —
// so the Events view can keep its own by naming a different surface instead of
// inventing a second storage scheme. The three functions below are now thin
// re-exports; they keep their names because hosts import them.
//
// W4 also added the two things that make the surface live: an optional poll
// (`refreshMs`, off by default — a library that starts fetching on a timer
// without being asked is a surprise) and one shared elapsed ticker whose
// cadence comes from the rows themselves, so a Desk with nothing running holds
// no timer at all.
//
// When `GET /agent/attention-requests` is not mounted the Asks stack still
// renders: each parked delivery becomes a message-less ask, so the operator
// sees *that* something is waiting even where the sentence the worker wrote
// cannot be fetched. That degradation is named in the API (`asksHaveMessages`)
// so the page can say so rather than implying the workers said nothing.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ConfigApiOptions } from './configApi.js'
import { buildDesk, type AttentionRequest, type Desk } from './desk.js'
import useAttentionRequests from './useAttentionRequests.js'
import useConfigLog from './useConfigLog.js'
import useEventsOverview from './useEvents.js'
import useSchedules from './useSchedules.js'
import useWorkers from './useWorkers.js'
import { deliveryDurationSeconds, type EventDelivery } from './events.js'
import { readWatermark, useFeedWatermark, watermarkKey, writeWatermark } from './watermark.js'
import useElapsedTicker, { tickIntervalForRows } from './useElapsedTicker.js'

/** The surface name the Desk's mark is stored under. */
export const DESK_SURFACE = 'desk'

/** `localStorage` key for the "since you last looked" mark, per project. */
export function deskLastSeenKey(projectId: string): string {
  return watermarkKey(DESK_SURFACE, projectId)
}

/** Read the mark. Unreadable storage and rubbish values both mean 0. */
export function readDeskLastSeen(projectId: string): number {
  return readWatermark(DESK_SURFACE, projectId)
}

/** Write the mark. A storage that refuses is not an error the operator needs. */
export function writeDeskLastSeen(projectId: string, ms: number): void {
  writeWatermark(DESK_SURFACE, projectId, ms)
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
  /**
   * Poll every N ms. 0 (the default) never polls: this is a library, and a
   * component that starts fetching on a timer because it was mounted is a
   * surprise. A host that wants a live Desk asks for one.
   */
  refreshMs?: number
  /**
   * WCAG 2.2.2: the operator has paused live updates. Stops the poll AND the
   * elapsed ticker — a paused surface is a still surface, not a quieter one.
   */
  paused?: boolean
  /** How many already-seen changes to keep under the waterline. Default 10. */
  earlierChangesLimit?: number
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
  /**
   * The shared clock, in unix MILLISECONDS — re-rendered on the cadence the
   * rows ask for (per second while something runs, per minute once nothing is
   * young, not at all when everything has finished).
   */
  nowMs: number
}

export default function useDesk(options: UseDeskOptions = {}): DeskApi {
  const {
    projectId = '',
    limit,
    nowSeconds,
    lastSeenMs: lastSeenOverride,
    refreshMs = 0,
    paused = false,
    earlierChangesLimit,
  } = options

  const overview = useEventsOverview({ ...options, limit, nowSeconds })
  const attention = useAttentionRequests(options)
  const log = useConfigLog({ ...options, projectId })
  const schedules = useSchedules(options)
  const workers = useWorkers(options)

  // Frozen for the visit, per project: a mark that re-read on every render
  // would clear the Changes stack the moment anything else re-rendered, and a
  // waterline computed from it would chase the operator down the page.
  const watermark = useFeedWatermark(DESK_SURFACE, projectId, lastSeenOverride)
  const lastSeenMs = watermark.markMs
  const markSeen = watermark.mark

  // One ticker for the whole surface, at the cadence its own rows ask for
  // (useElapsedTicker's table). `nowSeconds` freezes it outright.
  const [tickMs, setTickMs] = useState(0)
  const nowMs = useElapsedTicker({
    intervalMs: tickMs,
    paused,
    nowMs: nowSeconds === undefined ? undefined : nowSeconds * 1000,
  })
  const tickNowSeconds = nowSeconds ?? Math.floor(nowMs / 1000)
  const wantedTick = useMemo(
    () =>
      tickIntervalForRows(
        overview.deliveries.map((d) => ({
          status: d.status,
          elapsedSeconds: deliveryDurationSeconds(d, tickNowSeconds) ?? 0,
        })),
      ),
    [overview.deliveries, tickNowSeconds],
  )
  useEffect(() => setTickMs(wantedTick), [wantedTick])

  // The poll. Opt-in, and it goes through `reload` so the three lists still
  // agree on one moment — three independently-refreshing lists would render a
  // job whose event came from one fetch and whose subscription came from
  // another, which is the quiet wrongness this hook exists to avoid.
  const reloadRef = useRef<() => Promise<void>>(async () => {})

  const reload = useCallback(async () => {
    await Promise.all([
      overview.reload(),
      attention.reload(),
      log.reload(),
      schedules.reload(),
      workers.reload(),
    ])
  }, [attention, log, overview, schedules, workers])
  // Held in a ref so the interval below is created once per cadence rather than
  // torn down and rebuilt every render (`reload`'s identity changes with its
  // five hooks) — a poll that restarted its own timer would never fire.
  reloadRef.current = reload

  useEffect(() => {
    if (refreshMs <= 0 || paused) return
    const id = setInterval(() => void reloadRef.current(), refreshMs)
    return () => clearInterval(id)
  }, [paused, refreshMs])

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
        nowSeconds: tickNowSeconds,
        lastSeenMs,
        projectId,
        earlierChangesLimit,
      }),
    [
      earlierChangesLimit,
      attention.available,
      attention.requests,
      lastSeenMs,
      log.events,
      tickNowSeconds,
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
    nowMs: tickNowSeconds * 1000,
  }
}
