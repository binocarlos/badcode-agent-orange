// useImages — load `/agent/images`, the project's image catalogue (B4).
//
// Read-only, so the whole API is the list plus a reload: there is no save and
// no remove, because a version is burned from inside a session and is never
// edited (§13.2).
//
// A missing route is not an error worth shouting about. The catalogue only
// enriches the worker editor's image field, so a host that has not mounted it
// (or has no Postgres) leaves `images` empty and the field stays the free-text
// box it has always been — `error` records why, for a caller that wants it.

import { useCallback, useRef, useState } from 'react'
import { useConfigApi, type ConfigApiOptions } from './configApi.js'
import { coerceImage, IMAGE_ENDPOINTS, imageOptionsFrom, type ProjectImage } from './images.js'

export interface UseImagesOptions extends ConfigApiOptions {
  /** Override the list endpoint (default `/agent/images`). */
  listEndpoint?: string
}

export interface ImagesApi {
  /** The catalogue, newest first — the server's order, never resorted. */
  images: ProjectImage[]
  /** Each distinct image name once, for a picker (see imageOptionsFrom). */
  imageOptions: string[]
  loading: boolean
  /** Load failure, as the server phrased it. */
  error: string | null
  reload: () => Promise<void>
}

export default function useImages(options: UseImagesOptions = {}): ImagesApi {
  const { listEndpoint = IMAGE_ENDPOINTS.list } = options
  const { request } = useConfigApi(options)

  const [images, setImages] = useState<ProjectImage[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await request<{ images?: unknown[] } | null>(listEndpoint)
      const raw = Array.isArray(data?.images) ? data!.images! : []
      setImages(raw.map((i) => coerceImage(i)))
    } catch (err) {
      setImages([])
      setError(err instanceof Error ? err.message : 'failed to load images')
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

  return { images, imageOptions: imageOptionsFrom(images), loading, error, reload }
}
