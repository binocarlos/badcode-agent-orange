// Shared authenticated-fetch seam for the configuration surfaces (project
// settings, workers). Internal to this package — not exported from index.ts.
//
// Both surfaces need the same three things: the API base URL, a bearer token,
// and an error message a human can read. All three already exist on
// AgentChatConfig, so the default is "take them from the nearest
// AgentChatProvider"; explicit options win so the pages can also be mounted
// standalone (a settings screen that is not inside a chat).

import { useCallback, useMemo } from 'react'
import { useAgentChatContextOptional } from './AgentChatProvider.js'

export interface ConfigApiOptions {
  /** API base URL. Defaults to the provider's, else '' (same origin). */
  apiBaseUrl?: string
  /** Bearer-token provider. Defaults to the provider's. */
  getAuthToken?: () => Promise<string> | string
}

export interface ConfigApi {
  apiBaseUrl: string
  /**
   * Fetch `path` (relative to apiBaseUrl) with auth applied, returning parsed
   * JSON. Throws a {@link ConfigApiError} whose message is the server's body
   * when the status is not 2xx — these routes answer 400/404/501 with plain
   * text explaining why (`worker not found`, `project settings not
   * configured`), and that text is far more use to the human than "HTTP 501".
   * The status rides along on the error so a caller can tell a *missing route*
   * from a *broken* one without reading the prose (see {@link looksUnwired}).
   */
  request: <T>(path: string, init?: RequestInit) => Promise<T>
}

/**
 * The error `ConfigApi.request` throws for a non-2xx response: the server's own
 * body as the message (that is what a human should read), plus the HTTP status
 * (that is what the code should branch on).
 *
 * Nothing else throws this. A `fetch` that never got a response — DNS failure,
 * offline, CORS — rejects with a plain `TypeError`, which is exactly the case
 * where no status exists and `looksUnwired` has to fall back to the text.
 */
export class ConfigApiError extends Error {
  readonly status: number
  constructor(message: string, status: number) {
    super(message)
    this.name = 'ConfigApiError'
    this.status = status
  }
}

/** The HTTP status behind a rejection, when there was one. */
export function configApiStatus(err: unknown): number | undefined {
  return err instanceof ConfigApiError ? err.status : undefined
}

/**
 * `path` with the operator's reason appended as `?rationale=` — how a DELETE,
 * which carries no body, says why (design B3 / K2). An empty reason leaves the
 * path untouched rather than sending a blank parameter, so an absent reason
 * reads as absent in the config log. Pure; exported for the hooks and for test.
 */
export function withRationale(path: string, rationale: string): string {
  const trimmed = rationale.trim()
  if (trimmed === '') return path
  return `${path}${path.includes('?') ? '&' : '?'}rationale=${encodeURIComponent(trimmed)}`
}

/**
 * Does this failure mean "the route is not mounted here" rather than "the
 * request failed"? **The HTTP status decides whenever there is one**: 404 and
 * 501 are the two answers a host gives for a route it does not serve, and
 * every other status — 500 above all — is a failure no matter how its prose
 * reads.
 *
 * The text match survives only as the fallback for a rejection that carries no
 * status at all (a `fetch` that never reached a server, or a host-supplied
 * fetcher such as `useConfigLog`'s `fetchConfigEvents`, which throws whatever
 * it likes). Passing a bare string opts into that fallback deliberately.
 *
 * One definition, because three hooks had byte-identical private copies
 * (`useAttentionRequests`, `useConfigLog`, `useMemories`) and a fourth reader
 * would have written a fourth. A caller that treats another status specially —
 * `useMemories` reads a 400 as the selector parser talking — layers that on top
 * rather than forking this.
 *
 * B6, filed by B2: this used to classify by message text alone, so a genuine
 * 500 whose body happened to contain "not found" was reported to the user as a
 * *deployment limitation* instead of a *failure* — a calm explanatory note over
 * a broken deployment. It bit B2's own executor inside its own test. Note that
 * "did the load succeed" (RD27) remains a separate question from "is the route
 * mounted": an empty-state gate must still ask the former.
 */
export function looksUnwired(err: unknown): boolean {
  const status = configApiStatus(err)
  if (status !== undefined) return status === 404 || status === 501
  const message = err instanceof Error ? err.message : typeof err === 'string' ? err : ''
  const m = message.toLowerCase()
  return (
    m.includes('404') ||
    m.includes('501') ||
    m.includes('not found') ||
    m.includes('not configured') ||
    m.includes('not implemented')
  )
}

export function useConfigApi(options: ConfigApiOptions = {}): ConfigApi {
  const ctx = useAgentChatContextOptional()
  const apiBaseUrl = options.apiBaseUrl ?? ctx?.config.apiBaseUrl ?? ''
  const getAuthToken = options.getAuthToken ?? ctx?.config.getAuthToken

  const request = useCallback(
    async <T,>(path: string, init: RequestInit = {}): Promise<T> => {
      const headers: Record<string, string> = {
        ...(init.headers as Record<string, string> | undefined),
      }
      if (getAuthToken) {
        const token = await getAuthToken()
        if (token) headers['Authorization'] = `Bearer ${token}`
      }
      const resp = await fetch(apiBaseUrl + path, { ...init, headers })
      if (!resp.ok) {
        const body = (await resp.text().catch(() => '')).trim()
        throw new ConfigApiError(body || `HTTP ${resp.status}`, resp.status)
      }
      // 204 No Content (DELETE) has no body to parse.
      if (resp.status === 204) return undefined as T
      const text = await resp.text()
      if (text.trim() === '') return undefined as T
      return JSON.parse(text) as T
    },
    [apiBaseUrl, getAuthToken],
  )

  return useMemo(() => ({ apiBaseUrl, request }), [apiBaseUrl, request])
}
