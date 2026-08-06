// useConfigLog — the changelog's data hook (§15.10).
//
// The route it wants does not exist yet: `GET /agent/config-events` is J2/J3's
// to add (the store method `ListConfigEvents` is already there). So the hook
// takes a `fetchConfigEvents` seam, and only falls back to HTTP when none is
// given. Three consequences, all deliberate:
//
//   - The component and its tests are already finished and green against a
//     fixture fetcher, with no stubbed `fetch` and no invented Go endpoint.
//   - A host that already proxies its own config log passes one function.
//   - When the route lands, the default path starts working with no change
//     above this file — see configLog.ts's header for the exact contract.
//
// A 404/501 from the default path is reported as "not wired yet" rather than as
// a generic failure, because that is the true and actionable statement today.

import { useCallback, useMemo, useRef, useState } from 'react'
import { looksUnwired, useConfigApi, type ConfigApiOptions } from './configApi.js'
import {
  buildChangelog,
  changelogQueryParams,
  CONFIG_LOG_ENDPOINT,
  extractConfigEvents,
  filterChangelog,
  type ChangelogEntry,
  type ChangelogQuery,
  type ConfigEvent,
  type ConfigEventFetcher,
} from './configLog.js'

export interface UseConfigLogOptions extends ConfigApiOptions {
  /** Project id — builds the acting-session permalink when the server omits one. */
  projectId?: string
  /** Override the read route (default `/agent/config-events`). */
  endpoint?: string
  /**
   * Supply the records yourself. When set, no HTTP request is made at all —
   * this is the seam that lets the changelog ship before the route does.
   */
  fetchConfigEvents?: ConfigEventFetcher
  /** Server-side filter, also applied client-side so the two never disagree. */
  query?: ChangelogQuery
}

export interface ConfigLogApi {
  /** Entries newest-first, diffs already computed. */
  entries: ChangelogEntry[]
  /** The raw records, for a host that wants its own rendering. */
  events: ConfigEvent[]
  loading: boolean
  /** Load failure, as the server phrased it. */
  error: string | null
  /**
   * False when the read route answered 404/501 — the log is written, but
   * nothing serves it yet (J2/J3). The view says so instead of showing an
   * empty history, which would read as "nothing has changed".
   */
  available: boolean
  reload: () => Promise<void>
}

export default function useConfigLog(options: UseConfigLogOptions = {}): ConfigLogApi {
  const {
    projectId = '',
    endpoint = CONFIG_LOG_ENDPOINT,
    fetchConfigEvents,
    query,
  } = options
  const { request } = useConfigApi(options)

  const [events, setEvents] = useState<ConfigEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [available, setAvailable] = useState(true)

  // The query is a plain object a caller will almost always build inline, so
  // depending on its identity would refetch every render. Depend on its
  // serialisation instead — it is three scalars and a string.
  const queryKey = JSON.stringify(query ?? {})

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    const parsedQuery = JSON.parse(queryKey) as ChangelogQuery
    try {
      if (fetchConfigEvents) {
        setEvents(await fetchConfigEvents(parsedQuery))
      } else {
        const params = changelogQueryParams(parsedQuery).toString()
        const data = await request<unknown>(params ? `${endpoint}?${params}` : endpoint)
        setEvents(extractConfigEvents(data))
      }
      setAvailable(true)
    } catch (err) {
      const message = err instanceof Error ? err.message : 'failed to load the config log'
      setEvents([])
      setAvailable(!looksUnwired(message))
      setError(message)
    } finally {
      setLoading(false)
    }
  }, [endpoint, fetchConfigEvents, queryKey, request])

  // Render-phase ref-guard keyed on the query (the convention in
  // useProjectSettings/useWorkers): one fetch per distinct query, and an
  // unstable `request` identity can never turn into a GET loop.
  const loadedFor = useRef<string | null>(null)
  if (loadedFor.current !== queryKey) {
    loadedFor.current = queryKey
    void reload()
  }

  const entries = useMemo(() => {
    const built = buildChangelog(events, { projectId })
    // Filter again locally: a host-supplied fetcher may ignore the query, and
    // the entity filter has no server-side equivalent at all (§15.9 has one,
    // the route shape asked for does not carry it).
    return filterChangelog(built, JSON.parse(queryKey) as ChangelogQuery)
  }, [events, projectId, queryKey])

  return { entries, events, loading, error, available, reload }
}
