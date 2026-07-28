// useAttentionRequests — `GET /agent/attention-requests`, the sentences behind
// the Desk's Asks stack (design B1; go/httpapi/attention.go).
//
// Read-only, so the whole API is the list plus a reload. A request is answered
// by a human typing the next message in the thread and timed out by the sweep —
// there is nothing here to write, because there is no approval state machine.
//
// A host that has not mounted the route (or runs without Postgres) is not an
// error worth shouting about: `available` goes false, the list stays empty, and
// the Desk falls back to showing the parked deliveries without their messages —
// which is most of the value lost, and still better than a blank screen.

import { useCallback, useRef, useState } from 'react'
import { useConfigApi, type ConfigApiOptions } from './configApi.js'
import {
  ATTENTION_ENDPOINTS,
  coerceAttentionRequest,
  type AttentionRequest,
} from './desk.js'

export interface UseAttentionRequestsOptions extends ConfigApiOptions {
  /** Override the read route (default `/agent/attention-requests`). */
  endpoint?: string
  /** `open` (default — nobody has answered) or `all`. */
  state?: 'open' | 'all'
  /** Rows to ask for; 0 leaves the server's own default alone. */
  limit?: number
  /**
   * Fetch at all. Default true. `false` mounts without issuing a request, so
   * two components wanting the same list do not both ask for it (X7).
   */
  enabled?: boolean
}

export interface AttentionRequestsApi {
  /** The requests, in the server's order (newest first). */
  requests: AttentionRequest[]
  loading: boolean
  /** Load failure, as the server phrased it. */
  error: string | null
  /** False when the route answered 404/501 — not mounted here. */
  available: boolean
  reload: () => Promise<void>
}

/** Statuses that mean "this route is not mounted", not "this request failed". */
function looksUnwired(message: string): boolean {
  const m = message.toLowerCase()
  return (
    m.includes('404') ||
    m.includes('501') ||
    m.includes('not found') ||
    m.includes('not configured') ||
    m.includes('not implemented')
  )
}

export default function useAttentionRequests(
  options: UseAttentionRequestsOptions = {},
): AttentionRequestsApi {
  const { endpoint = ATTENTION_ENDPOINTS.list, state = 'open', limit = 0, enabled = true } = options
  const { request } = useConfigApi(options)

  const [requests, setRequests] = useState<AttentionRequest[]>([])
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState<string | null>(null)
  const [available, setAvailable] = useState(true)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    const params = new URLSearchParams({ state })
    if (limit > 0) params.set('limit', String(limit))
    try {
      const data = await request<{ attention_requests?: unknown[] } | null>(
        `${endpoint}?${params.toString()}`,
      )
      const raw = Array.isArray(data?.attention_requests) ? data!.attention_requests! : []
      setRequests(raw.map((r) => coerceAttentionRequest(r)))
      setAvailable(true)
    } catch (err) {
      const message = err instanceof Error ? err.message : 'failed to load attention requests'
      setRequests([])
      setAvailable(!looksUnwired(message))
      setError(message)
    } finally {
      setLoading(false)
    }
  }, [endpoint, limit, request, state])

  // Render-phase ref-guard keyed on the query, matching the sibling hooks: one
  // fetch per distinct query, and an unstable `request` identity cannot loop.
  const loadedFor = useRef<string | null>(null)
  const key = `${state} ${limit} ${endpoint}`
  if (enabled && loadedFor.current !== key) {
    loadedFor.current = key
    void reload()
  }

  return { requests, loading, error, available, reload }
}
