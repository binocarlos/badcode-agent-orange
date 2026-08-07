// useTopologies — the data hook behind the topology onboarding flow (T3).
//
// Wraps the three routes: the catalogue (loaded once, like useWorkers), and
// preview/apply as imperative calls — a preview is a question the human asks,
// not state this hook should refetch on its own.

import { useCallback, useRef, useState } from 'react'
import { useConfigApi, type ConfigApiOptions } from './configApi.js'
import {
  coerceTopology,
  coerceTopologyApplyResult,
  coerceTopologyPreview,
  TOPOLOGY_ENDPOINTS,
  type Topology,
  type TopologyApplyResult,
  type TopologyPreview,
} from './topologies.js'

export interface UseTopologiesOptions extends ConfigApiOptions {
  /** Override the catalogue endpoint (default `/agent/topologies`). */
  listEndpoint?: string
  /** Override the preview endpoint. */
  previewEndpoint?: string
  /** Override the apply endpoint. */
  applyEndpoint?: string
}

export interface TopologiesApi {
  /** The built-in catalogue, ordered by name then version (the server's List). */
  topologies: Topology[]
  loading: boolean
  /** Catalogue-load failure, as the server phrased it. */
  error: string | null
  reload: () => Promise<void>
  /** POST /agent/topologies/preview. Returns the preview, or null on failure
   *  (the server's reason lands in `previewError`). Writes nothing. */
  preview: (
    name: string,
    version: string,
    answers: Record<string, string | boolean>,
  ) => Promise<TopologyPreview | null>
  previewing: boolean
  previewError: string | null
  /** POST /agent/topologies/apply. Returns the read-back result, or null on
   *  failure — a 409 (collision / unmet precondition) lands its refusal text
   *  in `applyError` verbatim. */
  apply: (
    name: string,
    version: string,
    answers: Record<string, string | boolean>,
  ) => Promise<TopologyApplyResult | null>
  applying: boolean
  applyError: string | null
}

export default function useTopologies(options: UseTopologiesOptions = {}): TopologiesApi {
  const {
    listEndpoint = TOPOLOGY_ENDPOINTS.list,
    previewEndpoint = TOPOLOGY_ENDPOINTS.preview,
    applyEndpoint = TOPOLOGY_ENDPOINTS.apply,
  } = options
  const { request } = useConfigApi(options)

  const [topologies, setTopologies] = useState<Topology[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [previewing, setPreviewing] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [applying, setApplying] = useState(false)
  const [applyError, setApplyError] = useState<string | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await request<{ topologies?: unknown[] } | null>(listEndpoint)
      const raw = Array.isArray(data?.topologies) ? data!.topologies! : []
      setTopologies(raw.map(coerceTopology))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'failed to load topologies')
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

  const preview = useCallback(
    async (
      name: string,
      version: string,
      answers: Record<string, string | boolean>,
    ): Promise<TopologyPreview | null> => {
      setPreviewing(true)
      setPreviewError(null)
      // A new preview supersedes whatever the last apply attempt said.
      setApplyError(null)
      try {
        const raw = await request<unknown>(previewEndpoint, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name, version, answers }),
        })
        return coerceTopologyPreview(raw)
      } catch (err) {
        setPreviewError(err instanceof Error ? err.message : 'failed to preview topology')
        return null
      } finally {
        setPreviewing(false)
      }
    },
    [previewEndpoint, request],
  )

  const apply = useCallback(
    async (
      name: string,
      version: string,
      answers: Record<string, string | boolean>,
    ): Promise<TopologyApplyResult | null> => {
      setApplying(true)
      setApplyError(null)
      try {
        const raw = await request<unknown>(applyEndpoint, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name, version, answers }),
        })
        return coerceTopologyApplyResult(raw)
      } catch (err) {
        setApplyError(err instanceof Error ? err.message : 'failed to apply topology')
        return null
      } finally {
        setApplying(false)
      }
    },
    [applyEndpoint, request],
  )

  return {
    topologies,
    loading,
    error,
    reload,
    preview,
    previewing,
    previewError,
    apply,
    applying,
    applyError,
  }
}
