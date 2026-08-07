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
  /**
   * Fetch at all. Default true.
   *
   * W4's collapse of X7's duplicate: while the Desk is open, `useDesk` already
   * has both lists and the badge is the same join over them, so the shell hands
   * the count DOWN from the Desk (`DeskPage.onAsksCount`) and stands this hook
   * down. Off the Desk, this is the only reader and it fetches. Either way the
   * lists are fetched once, and — the point of X7 — by one definition of "an
   * ask", never two.
   */
  enabled?: boolean
}

export interface AsksCountApi {
  /** How many asks the Desk would show right now. */
  count: number
  loading: boolean
  /** The first failure across the two loads, as the server phrased it. */
  error: string | null
  /**
   * False when the attention list did not load — not mounted here, or it
   * failed. The count then comes from the parked deliveries alone.
   */
  asksHaveMessages: boolean
  reload: () => Promise<void>
}

export default function useAsksCount(options: UseAsksCountOptions = {}): AsksCountApi {
  const { limit, enabled = true } = options
  // Only the parked rows: the badge has no use for the rest of the page, and a
  // narrower query is a smaller answer on a busy project.
  const overview = useEventsOverview({ ...options, limit, status: 'awaiting_human', enabled })
  const attention = useAttentionRequests({ ...options, enabled })

  const count = useMemo(
    () =>
      countAsks(
        overview.deliveries,
        // "Did this load succeed", not "is it mounted" — a failed fetch leaves
        // an empty list, and a badge reading 0 over three parked approvals is
        // the failure this branch exists to prevent (RD27).
        attention.ok ? attention.requests : deliveriesAsRequests(overview.deliveries),
      ),
    [attention.ok, attention.requests, overview.deliveries],
  )

  const reload = async (): Promise<void> => {
    await Promise.all([overview.reload(), attention.reload()])
  }

  return {
    count,
    loading: overview.loading || attention.loading,
    // "Not mounted here" is reported by `asksHaveMessages`, not as an error —
    // but a mounted route that failed is a real failure and belongs here.
    error: overview.error ?? (attention.available ? attention.error : null),
    asksHaveMessages: attention.ok,
    reload,
  }
}
