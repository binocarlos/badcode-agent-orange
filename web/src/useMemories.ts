// useMemories — load `GET /agent/memories`, the memory browser's read path
// (design B2 / §8; go/httpapi/memories.go).
//
// Read-only, so the whole API is the list plus a reload and a re-query: memory
// is append-only and is written by workers through their tools (§7.1), so there
// is nothing here to save and nothing to delete.
//
// Two failure modes are told apart deliberately:
//   * a BAD SELECTOR is the operator's, comes back 400 with the parser's own
//     message, and belongs next to the selector bar — `selectorError`;
//   * a route that is not mounted (404) or a host without Postgres (501) is
//     nobody's mistake to fix in this screen — `available` goes false and the
//     page says memory is not available here rather than showing an error the
//     operator cannot act on.

import { useCallback, useRef, useState } from 'react'
import { useConfigApi, type ConfigApiOptions } from './configApi.js'
import { coerceMemory, MEMORY_ENDPOINTS, type MemoryRow } from './memories.js'

export interface UseMemoriesOptions extends ConfigApiOptions {
  /** Override the read route (default `/agent/memories`). */
  endpoint?: string
  /** Initial label selector. */
  selector?: string
  /** Initial free-text query — present means RRF, absent means newest-first. */
  query?: string
  /** Rows to ask for; 0 leaves the server's own default (20, capped at 100). */
  limit?: number
}

export interface MemoriesApi {
  /** The rows, in the server's order — newest first, or RRF-ranked. */
  memories: MemoryRow[]
  /** The selector this list was fetched with. */
  selector: string
  /** The free-text query this list was fetched with. */
  query: string
  /** Re-run the search. Empty strings are legal for both. */
  search: (selector: string, query: string) => Promise<void>
  loading: boolean
  /** A 400 from the selector parser, phrased by the engine. Null otherwise. */
  selectorError: string | null
  /** Any other load failure, as the server phrased it. */
  error: string | null
  /** False when the route answered 404/501 — not mounted, or no Postgres. */
  available: boolean
  reload: () => Promise<void>
}

/** Statuses that mean "memory is not served here", not "this search failed".
 *  Mirrors useAttentionRequests' looksUnwired, minus 400. */
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

/** A 400 is the selector parser talking. The message is the parser's and is
 *  shown verbatim: it names the exact term that failed. */
function looksLikeSelectorError(message: string): boolean {
  return message.includes('400') || message.toLowerCase().includes('selector')
}

export default function useMemories(options: UseMemoriesOptions = {}): MemoriesApi {
  const {
    endpoint = MEMORY_ENDPOINTS.list,
    selector: initialSelector = '',
    query: initialQuery = '',
    limit = 0,
  } = options
  const { request } = useConfigApi(options)

  const [selector, setSelector] = useState(initialSelector)
  const [query, setQuery] = useState(initialQuery)
  const [memories, setMemories] = useState<MemoryRow[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selectorError, setSelectorError] = useState<string | null>(null)
  const [available, setAvailable] = useState(true)

  const fetchRows = useCallback(
    async (sel: string, q: string) => {
      setLoading(true)
      setError(null)
      setSelectorError(null)
      const params = new URLSearchParams()
      if (sel !== '') params.set('selector', sel)
      if (q !== '') params.set('query', q)
      if (limit > 0) params.set('limit', String(limit))
      const suffix = params.toString()
      try {
        const data = await request<{ memories?: unknown[] } | null>(
          suffix === '' ? endpoint : `${endpoint}?${suffix}`,
        )
        const raw = Array.isArray(data?.memories) ? data!.memories! : []
        setMemories(raw.map((m) => coerceMemory(m)))
        setAvailable(true)
      } catch (err) {
        const message = err instanceof Error ? err.message : 'failed to load memories'
        setMemories([])
        if (looksUnwired(message)) {
          setAvailable(false)
          setError(message)
        } else if (looksLikeSelectorError(message)) {
          setSelectorError(message)
        } else {
          setError(message)
        }
      } finally {
        setLoading(false)
      }
    },
    [endpoint, limit, request],
  )

  // Render-phase ref-guard keyed on the query, matching the sibling hooks: one
  // fetch per distinct query, and an unstable `request` identity cannot loop.
  const loadedFor = useRef<string | null>(null)
  const queryKey = (sel: string, q: string) => `${endpoint} ${limit} ${sel} ${q}`

  const search = useCallback(
    async (nextSelector: string, nextQuery: string) => {
      // Claim the guard before the state change, or the re-render that follows
      // would see a new key and run the same search a second time.
      loadedFor.current = queryKey(nextSelector, nextQuery)
      setSelector(nextSelector)
      setQuery(nextQuery)
      await fetchRows(nextSelector, nextQuery)
    },
    [endpoint, fetchRows, limit],
  )

  const reload = useCallback(() => fetchRows(selector, query), [fetchRows, query, selector])

  const key = queryKey(selector, query)
  if (loadedFor.current !== key) {
    loadedFor.current = key
    void fetchRows(selector, query)
  }

  return {
    memories,
    selector,
    query,
    search,
    loading,
    selectorError,
    error,
    available,
    reload,
  }
}
