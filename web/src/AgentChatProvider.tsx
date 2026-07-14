// AgentChatProvider — wraps useAgentSession and exposes two contexts:
//   ChatContext: current-session streaming state + actions
//   SessionsContext: list/search/select/delete lifecycle
//
// Usage:
//   <AgentChatProvider config={{ apiBaseUrl, models, ... }}>
//     <AgentChat />          /* reads ChatContext */
//     <AgentSessionList />   /* reads SessionsContext */
//   </AgentChatProvider>

import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import useAgentSession from './useAgentSession.js'
import type { AgentChatConfig } from './plugins.js'
import { DEFAULT_ENDPOINTS } from './plugins.js'
import type { AgentSessionListItem, AgentMessageSearchResult } from './types.js'

// ---------------------------------------------------------------------------
// Chat context
// ---------------------------------------------------------------------------

type ChatContextValue = ReturnType<typeof useAgentSession> & {
  config: AgentChatConfig
}

const ChatContext = createContext<ChatContextValue | null>(null)

// ---------------------------------------------------------------------------
// Sessions context
// ---------------------------------------------------------------------------

interface SessionsContextValue {
  sessions: AgentSessionListItem[]
  search: (query: string) => Promise<AgentMessageSearchResult[]>
  /**
   * Reload the session list. userEmail scopes it: omitted = the API default
   * (the caller's own sessions), '*' = all users, else that user's sessions.
   */
  refresh: (opts?: { userEmail?: string }) => Promise<void>
  select: (id: string) => void
  delete: (id: string) => Promise<void>
}

const SessionsContext = createContext<SessionsContextValue | null>(null)

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

export function AgentChatProvider({
  config,
  children,
}: {
  config: AgentChatConfig
  children: ReactNode
}) {
  const endpoints = useMemo(
    () => ({ ...DEFAULT_ENDPOINTS, ...(config.endpoints ?? {}) }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [config.endpoints],
  )

  const pluginEventTypes = useMemo(
    () => (config.plugins ?? []).flatMap((p) => p.eventTypes),
    [config.plugins],
  )

  const session = useAgentSession({
    apiBaseUrl: config.apiBaseUrl,
    endpoints,
    defaultModel: config.models[0]?.id,
    getAuthToken: config.getAuthToken,
    pluginEventTypes,
    onToolResult: config.onToolResult
      ? (_name: string, id: string, out: string) => config.onToolResult!(id, out as unknown)
      : undefined,
    onSessionTitle: config.onSessionTitle,
    onArtifactsUpdated: config.onArtifactsUpdated,
    onSnapshotComplete: config.onSnapshotComplete,
  })

  const chatValue = useMemo(() => ({ ...session, config }), [session, config])

  // -------------------------------------------------------------------------
  // Sessions context state
  // -------------------------------------------------------------------------

  const [sessions, setSessions] = useState<AgentSessionListItem[]>([])

  const { apiBaseUrl, getAuthToken } = config
  const { resumeSession } = session

  const resolveAuthHeader = useCallback(async (): Promise<Record<string, string>> => {
    const h: Record<string, string> = {}
    if (getAuthToken) {
      const token = await getAuthToken()
      if (token) h['Authorization'] = `Bearer ${token}`
    }
    return h
  }, [getAuthToken])

  const refresh = useCallback(async (opts?: { userEmail?: string }): Promise<void> => {
    try {
      const headers = await resolveAuthHeader()
      let url = apiBaseUrl + endpoints.listSessions
      if (opts?.userEmail) url += `?user_email=${encodeURIComponent(opts.userEmail)}`
      const resp = await fetch(url, { headers })
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
      const data = (await resp.json()) as AgentSessionListItem[]
      setSessions(Array.isArray(data) ? data : [])
    } catch (err) {
      console.error('[AgentSessions] refresh failed:', err)
    }
  }, [apiBaseUrl, endpoints, resolveAuthHeader])

  const search = useCallback(async (query: string): Promise<AgentMessageSearchResult[]> => {
    try {
      const headers = await resolveAuthHeader()
      const params = new URLSearchParams({ q: query })
      const resp = await fetch(
        `${apiBaseUrl}${endpoints.searchMessages}?${params.toString()}`,
        { headers },
      )
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
      const data = (await resp.json()) as { results?: AgentMessageSearchResult[] }
      return data.results ?? []
    } catch (err) {
      console.error('[AgentSessions] search failed:', err)
      return []
    }
  }, [apiBaseUrl, endpoints, resolveAuthHeader])

  const select = useCallback((id: string) => {
    resumeSession(id)
  }, [resumeSession])

  const deleteSessionItem = useCallback(async (id: string): Promise<void> => {
    try {
      const headers = await resolveAuthHeader()
      await fetch(apiBaseUrl + endpoints.deleteSession(id), {
        method: 'DELETE',
        headers,
      })
      setSessions((prev) => prev.filter((s) => s.id !== id))
    } catch (err) {
      console.error('[AgentSessions] delete failed:', err)
    }
  }, [apiBaseUrl, endpoints, resolveAuthHeader])

  const sessionsValue = useMemo<SessionsContextValue>(
    () => ({
      sessions,
      search,
      refresh,
      select,
      delete: deleteSessionItem,
    }),
    [sessions, search, refresh, select, deleteSessionItem],
  )

  return (
    <ChatContext.Provider value={chatValue}>
      <SessionsContext.Provider value={sessionsValue}>{children}</SessionsContext.Provider>
    </ChatContext.Provider>
  )
}

// ---------------------------------------------------------------------------
// Hooks
// ---------------------------------------------------------------------------

export function useAgentChat(): ChatContextValue {
  const ctx = useContext(ChatContext)
  if (!ctx) throw new Error('useAgentChat must be used within <AgentChatProvider>')
  return ctx
}

/** Non-throwing variant — returns null when used outside a Provider. */
export function useAgentChatContextOptional(): ChatContextValue | null {
  return useContext(ChatContext)
}

export function useAgentChatContext(): ChatContextValue {
  const ctx = useContext(ChatContext)
  if (!ctx) throw new Error('useAgentChatContext must be used within <AgentChatProvider>')
  return ctx
}

export function useAgentSessions(): SessionsContextValue {
  const ctx = useContext(SessionsContext)
  if (!ctx) throw new Error('useAgentSessions must be used within <AgentChatProvider>')
  return ctx
}
