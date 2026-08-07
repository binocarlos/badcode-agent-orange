// useSchedules — load/create/update/delete `/agent/schedules` (§8.6).
// Work-plan item F2.
//
// Same shape as useSubscriptions: POST to the collection creates, PUT to the id
// updates, and `save` decides from the draft's id. The one addition is
// `rationale`: the schedule routes thread it into the config event (§15.5), so
// a human retuning a schedule can leave the commit message that the changelog
// (§15.10) and any `config.changed` subscriber will read.

import { useCallback, useRef, useState } from 'react'
import { useConfigApi, withRationale, type ConfigApiOptions } from './configApi.js'
import {
  coerceSchedule,
  scheduleBody,
  SCHEDULE_ENDPOINTS,
  type Schedule,
  type ScheduleDraft,
} from './schedules.js'

export interface UseSchedulesOptions extends ConfigApiOptions {
  /** Override the list/create endpoint (default `/agent/schedules`). */
  listEndpoint?: string
  /** Override the per-id endpoint. */
  scheduleEndpoint?: (id: string) => string
}

export interface SchedulesApi {
  schedules: Schedule[]
  loading: boolean
  error: string | null
  reload: () => Promise<void>
  /** Create (empty id) or update (id set), with an optional rationale. */
  save: (draft: ScheduleDraft, rationale?: string) => Promise<Schedule | null>
  /**
   * Delete a schedule. The route carries no body, so the reason rides
   * `?rationale=` (design B3 — it did not read one before that item).
   */
  remove: (id: string, rationale?: string) => Promise<boolean>
}

export default function useSchedules(options: UseSchedulesOptions = {}): SchedulesApi {
  const {
    listEndpoint = SCHEDULE_ENDPOINTS.list,
    scheduleEndpoint = SCHEDULE_ENDPOINTS.one,
  } = options
  const { request } = useConfigApi(options)

  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await request<{ schedules?: unknown[] } | null>(listEndpoint)
      const raw = Array.isArray(data?.schedules) ? data!.schedules! : []
      setSchedules(raw.map((s) => coerceSchedule(s)))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'failed to load schedules')
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
    async (draft: ScheduleDraft, rationale = ''): Promise<Schedule | null> => {
      setError(null)
      const creating = draft.id.trim() === ''
      try {
        const stored = await request<unknown>(
          creating ? listEndpoint : scheduleEndpoint(draft.id),
          {
            method: creating ? 'POST' : 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(scheduleBody(draft, rationale)),
          },
        )
        const schedule = coerceSchedule(stored)
        setSchedules((prev) => {
          const idx = prev.findIndex((s) => s.id === schedule.id)
          if (idx === -1) return [...prev, schedule]
          const next = prev.slice()
          next[idx] = schedule
          return next
        })
        return schedule
      } catch (err) {
        setError(err instanceof Error ? err.message : 'failed to save schedule')
        return null
      }
    },
    [listEndpoint, request, scheduleEndpoint],
  )

  const remove = useCallback(
    async (id: string, rationale = ''): Promise<boolean> => {
      setError(null)
      try {
        await request<void>(withRationale(scheduleEndpoint(id), rationale), { method: 'DELETE' })
        setSchedules((prev) => prev.filter((s) => s.id !== id))
        return true
      } catch (err) {
        setError(err instanceof Error ? err.message : 'failed to delete schedule')
        return false
      }
    },
    [request, scheduleEndpoint],
  )

  return { schedules, loading, error, reload, save, remove }
}
