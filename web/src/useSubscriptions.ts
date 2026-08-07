// useSubscriptions — load/create/update/delete `/agent/subscriptions` (§8.3).
// Work-plan item F2.
//
// Unlike workers, this route is not a whole-object PUT by name: creating is a
// POST to the collection (201, server-allocated uuid) and updating is a PUT to
// the id. `save` hides that behind one call — a draft with an empty id is a
// create — because the editor genuinely does not care which it is, and every
// component that had to care would get it wrong once.
//
// Init is a render-phase ref-guard, not a useEffect, matching
// useProjectSettings/useWorkers/useEvents.

import { useCallback, useRef, useState } from 'react'
import { useConfigApi, withRationale, type ConfigApiOptions } from './configApi.js'
import { coerceSubscription, type Subscription } from './events.js'
import {
  subscriptionBody,
  SUBSCRIPTION_ENDPOINTS,
  type SubscriptionDraft,
} from './subscriptions.js'

export interface UseSubscriptionsOptions extends ConfigApiOptions {
  /** Override the list/create endpoint (default `/agent/subscriptions`). */
  listEndpoint?: string
  /** Override the per-id endpoint. */
  subscriptionEndpoint?: (id: string) => string
}

export interface SubscriptionsApi {
  subscriptions: Subscription[]
  loading: boolean
  /** The last failure, as the server phrased it. */
  error: string | null
  reload: () => Promise<void>
  /** Create (empty id) or update (id set), with the operator's one-line reason
   *  (design B3 / K2). Returns the stored row, or null. */
  save: (draft: SubscriptionDraft, rationale?: string) => Promise<Subscription | null>
  /** Delete a subscription. The route has no body, so the reason rides
   *  `?rationale=`. */
  remove: (id: string, rationale?: string) => Promise<boolean>
}

export default function useSubscriptions(options: UseSubscriptionsOptions = {}): SubscriptionsApi {
  const {
    listEndpoint = SUBSCRIPTION_ENDPOINTS.list,
    subscriptionEndpoint = SUBSCRIPTION_ENDPOINTS.one,
  } = options
  const { request } = useConfigApi(options)

  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await request<{ subscriptions?: unknown[] } | null>(listEndpoint)
      const raw = Array.isArray(data?.subscriptions) ? data!.subscriptions! : []
      setSubscriptions(raw.map((s) => coerceSubscription(s)))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'failed to load subscriptions')
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
    async (draft: SubscriptionDraft, rationale = ''): Promise<Subscription | null> => {
      setError(null)
      const creating = draft.id.trim() === ''
      try {
        const stored = await request<unknown>(
          creating ? listEndpoint : subscriptionEndpoint(draft.id),
          {
            method: creating ? 'POST' : 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(subscriptionBody(draft, rationale)),
          },
        )
        const sub = coerceSubscription(stored)
        // Replace in place so the list does not reorder under the human
        // mid-edit; append a newly created one at the end.
        setSubscriptions((prev) => {
          const idx = prev.findIndex((s) => s.id === sub.id)
          if (idx === -1) return [...prev, sub]
          const next = prev.slice()
          next[idx] = sub
          return next
        })
        return sub
      } catch (err) {
        setError(err instanceof Error ? err.message : 'failed to save subscription')
        return null
      }
    },
    [listEndpoint, request, subscriptionEndpoint],
  )

  const remove = useCallback(
    async (id: string, rationale = ''): Promise<boolean> => {
      setError(null)
      try {
        await request<void>(withRationale(subscriptionEndpoint(id), rationale), { method: 'DELETE' })
        setSubscriptions((prev) => prev.filter((s) => s.id !== id))
        return true
      } catch (err) {
        setError(err instanceof Error ? err.message : 'failed to delete subscription')
        return false
      }
    },
    [request, subscriptionEndpoint],
  )

  return { subscriptions, loading, error, reload, save, remove }
}
