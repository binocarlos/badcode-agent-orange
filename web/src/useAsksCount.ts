// useAsksCount — the one number in the chrome (doc 21, X7).
//
// The shell's Desk badge used to count *open attention requests*, while the
// Desk's Asks stack lists requests JOINED to a parked delivery. The moment both
// are on screen they disagree — the walkthrough caught a badge of 2 above a
// stack of 1 — and a number that disagrees with the list it points at is worse
// than no number.
//
// So the badge asks the library for the count, and the library computes it with
// `countAsks`, the same predicate `buildDesk` uses. The shell does not fetch
// deliveries and re-do the join, and there is no second definition of "an ask"
// to drift.
//
// Deliberately lighter than `useDesk`: the badge needs deliveries and requests,
// so this reads `?status=awaiting_human` (a page of parked rows, not the whole
// delivery history) and the attention list. A host without the attention route
// degrades exactly as the Desk does — parked deliveries stand in for requests.

import { useMemo } from 'react'
import type { ConfigApiOptions } from './configApi.js'
import { countAsks } from './desk.js'
import useAttentionRequests from './useAttentionRequests.js'
import useEventsOverview from './useEvents.js'
import { deliveriesAsRequests } from './useDesk.js'

export interface UseAsksCountOptions extends ConfigApiOptions {
  /** Parked deliveries to read per poll. Default 100 (the route's page). */
  limit?: number
}

export interface AsksCountApi {
  /** How many asks the Desk would show right now. */
  count: number
  loading: boolean
  /** The first failure across the two loads, as the server phrased it. */
  error: string | null
  /** False when `GET /agent/attention-requests` is not mounted here. */
  asksHaveMessages: boolean
  reload: () => Promise<void>
}

export default function useAsksCount(options: UseAsksCountOptions = {}): AsksCountApi {
  const { limit } = options
  // Only the parked rows: the badge has no use for the rest of the page, and a
  // narrower query is a smaller answer on a busy project.
  const overview = useEventsOverview({ ...options, limit, status: 'awaiting_human' })
  const attention = useAttentionRequests(options)

  const count = useMemo(
    () =>
      countAsks(
        overview.deliveries,
        attention.available ? attention.requests : deliveriesAsRequests(overview.deliveries),
      ),
    [attention.available, attention.requests, overview.deliveries],
  )

  const reload = async (): Promise<void> => {
    await Promise.all([overview.reload(), attention.reload()])
  }

  return {
    count,
    loading: overview.loading || attention.loading,
    // "Not mounted here" is reported by `asksHaveMessages`, not as an error.
    error: overview.error,
    asksHaveMessages: attention.available,
    reload,
  }
}
