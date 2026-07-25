// useWorkers / useWorkerJobs — the data hooks behind the worker UI.
//
// useWorkers wraps the four CRUD routes. useWorkerJobs answers "which sessions
// has this worker run?" — and does so by filtering the ordinary session list
// client-side, because there is no server-side worker filter yet (see the note
// on `serverFiltered` below). That is a deliberate, visible degradation rather
// than an invented endpoint.

import { useCallback, useMemo, useRef, useState } from 'react'
import { useConfigApi, type ConfigApiOptions } from './configApi.js'
import { DEFAULT_ENDPOINTS } from './plugins.js'
import { coerceWorker, WORKER_ENDPOINTS, workerBody, type Worker, type WorkerDraft } from './workers.js'
import type { AgentSessionListItem } from './types.js'

export interface UseWorkersOptions extends ConfigApiOptions {
  /** Override the list endpoint (default `/agent/workers`). */
  listEndpoint?: string
  /** Override the single-worker endpoint factory. */
  workerEndpoint?: (name: string) => string
}

export interface WorkersApi {
  workers: Worker[]
  loading: boolean
  /** Load/save/delete failure, as the server phrased it. */
  error: string | null
  reload: () => Promise<void>
  /** PUT a worker. Returns the stored row the server echoed back, or null on
   *  failure (the reason lands in `error`). */
  save: (draft: WorkerDraft) => Promise<Worker | null>
  /** DELETE a worker. Returns true on success. */
  remove: (name: string) => Promise<boolean>
}

export default function useWorkers(options: UseWorkersOptions = {}): WorkersApi {
  const {
    listEndpoint = WORKER_ENDPOINTS.list,
    workerEndpoint = WORKER_ENDPOINTS.one,
  } = options
  const { request } = useConfigApi(options)

  const [workers, setWorkers] = useState<Worker[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await request<{ workers?: unknown[] } | null>(listEndpoint)
      const raw = Array.isArray(data?.workers) ? data!.workers! : []
      setWorkers(raw.map((w) => coerceWorker(w)))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'failed to load workers')
    } finally {
      setLoading(false)
    }
  }, [listEndpoint, request])

  // Ref-guard rather than useEffect — see the note in useProjectSettings.ts.
  const didLoad = useRef(false)
  if (!didLoad.current) {
    didLoad.current = true
    void reload()
  }

  const save = useCallback(
    async (draft: WorkerDraft): Promise<Worker | null> => {
      setError(null)
      try {
        const stored = await request<unknown>(workerEndpoint(draft.name), {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(workerBody(draft)),
        })
        const worker = coerceWorker(stored)
        // Replace in place when it already exists so the list does not reorder
        // under the human mid-edit; append otherwise.
        setWorkers((prev) => {
          const idx = prev.findIndex((w) => w.name === worker.name)
          if (idx === -1) return [...prev, worker]
          const next = prev.slice()
          next[idx] = worker
          return next
        })
        return worker
      } catch (err) {
        setError(err instanceof Error ? err.message : 'failed to save worker')
        return null
      }
    },
    [request, workerEndpoint],
  )

  const remove = useCallback(
    async (name: string): Promise<boolean> => {
      setError(null)
      try {
        await request<void>(workerEndpoint(name), { method: 'DELETE' })
        setWorkers((prev) => prev.filter((w) => w.name !== name))
        return true
      } catch (err) {
        setError(err instanceof Error ? err.message : 'failed to delete worker')
        return false
      }
    },
    [request, workerEndpoint],
  )

  return { workers, loading, error, reload, save, remove }
}

// ---------------------------------------------------------------------------
// Job history
// ---------------------------------------------------------------------------

export interface UseWorkerJobsOptions extends ConfigApiOptions {
  /** Override the session-list endpoint (default `/agent/sessions`). */
  listSessionsEndpoint?: string
  /** How many sessions to pull before filtering. Default 200. */
  limit?: number
}

export interface WorkerJobsApi {
  /** Sessions whose `worker` is this worker, newest first. */
  jobs: AgentSessionListItem[]
  loading: boolean
  error: string | null
  reload: () => Promise<void>
  /**
   * False while the filtering happens in the browser, which is the current
   * state of the world: `GET /agent/sessions` has no `worker` query parameter,
   * so this hook fetches one page of sessions and filters it locally. The
   * consequence a human must be told about is that a worker whose jobs fall
   * outside that page shows an incomplete history — `truncated` says so.
   * Flips to true, with `truncated` permanently false, once the server route
   * grows the filter and this hook starts passing it through.
   */
  serverFiltered: boolean
  /** True when the fetched page hit `limit`, so older jobs may be missing. */
  truncated: boolean
}

/**
 * Sessions run by one worker. `workerName` empty ⇒ no fetch, empty result.
 */
export function useWorkerJobs(
  workerName: string,
  options: UseWorkerJobsOptions = {},
): WorkerJobsApi {
  const { listSessionsEndpoint = DEFAULT_ENDPOINTS.listSessions, limit = 200 } = options
  const { request } = useConfigApi(options)

  const [sessions, setSessions] = useState<AgentSessionListItem[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [truncated, setTruncated] = useState(false)

  const reload = useCallback(async () => {
    if (!workerName) {
      setSessions([])
      setTruncated(false)
      return
    }
    setLoading(true)
    setError(null)
    try {
      const params = new URLSearchParams({ limit: String(limit), user_email: '*' })
      const data = await request<AgentSessionListItem[] | null>(
        `${listSessionsEndpoint}?${params.toString()}`,
      )
      const rows = Array.isArray(data) ? data : []
      setTruncated(rows.length >= limit)
      setSessions(rows)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'failed to load job history')
    } finally {
      setLoading(false)
    }
  }, [limit, listSessionsEndpoint, request, workerName])

  // Reload whenever the selected worker changes — a ref of the name we last
  // fetched for, checked in render phase, keeps that one-shot per worker.
  const loadedFor = useRef<string | null>(null)
  if (loadedFor.current !== workerName) {
    loadedFor.current = workerName
    void reload()
  }

  const jobs = useMemo(
    () =>
      sessions
        .filter((s) => s.worker === workerName)
        .sort((a, b) => (b.created_at ?? 0) - (a.created_at ?? 0)),
    [sessions, workerName],
  )

  return { jobs, loading, error, reload, serverFiltered: false, truncated }
}
