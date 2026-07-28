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
   * JSON. Throws an Error whose message is the server's body when the status is
   * not 2xx — these routes answer 400/404/501 with plain text explaining why
   * (`worker not found`, `project settings not configured`), and that text is
   * far more use to the human than "HTTP 501".
   */
  request: <T>(path: string, init?: RequestInit) => Promise<T>
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
        throw new Error(body || `HTTP ${resp.status}`)
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
