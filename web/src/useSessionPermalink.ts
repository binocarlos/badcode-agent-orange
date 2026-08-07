// useSessionPermalink — binds the canonical session route (permalink.ts) to the
// chat context, in both directions:
//
//   URL → state:  on mount and on back/forward, a `/p/<project>/s/<session>`
//                 URL resumes that session (the same load/replay path the
//                 session list uses — one reducer, live and replayed alike).
//   state → URL:  whenever the active session changes, the address bar is
//                 updated to that session's canonical path, so every session a
//                 human is looking at is already permalinked.
//
// Deliberately router-free: the library must not impose react-router on hosts,
// so this uses the History API directly and stays a drop-in for hosts that do
// have a router (they can ignore the hook and use permalink.ts alone).
//
// Project scoping is a safety boundary, not decoration: a URL naming a
// different project is never resumed — the caller's token would not authorise
// it anyway — and is reported as `foreignProject`.

import { useCallback, useEffect, useRef, useState } from 'react'
import { useAgentChat } from './AgentChatProvider.js'
import {
  buildSessionPath,
  buildSessionPermalink,
  parseSessionPermalink,
  PROJECT_SEGMENT,
} from './permalink.js'

export interface UseSessionPermalinkOptions {
  /** The project the UI is currently scoped to (from the project token). */
  projectId: string
  /**
   * Externally-reachable base URL used to mint *absolute* permalinks for
   * sharing (the UI's own navigation always uses relative paths). Default ''
   * → `permalinkFor` returns root-relative paths.
   */
  baseUrl?: string
  /** Set false to disable all URL reading/writing (e.g. host owns routing). */
  enabled?: boolean
}

export interface SessionPermalinkApi {
  /** Session id currently named by the URL for *this* project, else null. */
  routeSessionId: string | null
  /** True when the URL names a session in a different project (never resumed). */
  foreignProject: boolean
  /** Shareable permalink for the active session, or null when none is active. */
  permalink: string | null
  /** Shareable permalink for any session id in this project. */
  permalinkFor: (sessionId: string) => string
  /** Resume a session and push its canonical URL (back button returns here). */
  openSession: (sessionId: string) => void
}

export default function useSessionPermalink(
  options: UseSessionPermalinkOptions,
): SessionPermalinkApi {
  const { projectId, baseUrl = '', enabled = true } = options
  const { session, resumeSession } = useAgentChat()

  const [routeSessionId, setRouteSessionId] = useState<string | null>(null)
  const [foreignProject, setForeignProject] = useState(false)

  // The session id the URL currently reflects. Guards both directions against
  // feedback loops and against React 18 StrictMode's double effect invocation.
  const urlSessionIdRef = useRef<string | null>(null)
  const resumedRef = useRef<string | null>(null)

  const hasWindow = typeof window !== 'undefined'
  const active = enabled && hasWindow

  // ── URL → state ────────────────────────────────────────────────────────────
  useEffect(() => {
    if (!active) return

    const applyLocation = () => {
      const route = parseSessionPermalink(window.location.pathname)
      if (!route) {
        urlSessionIdRef.current = null
        setRouteSessionId(null)
        setForeignProject(false)
        return
      }
      if (route.projectId !== projectId) {
        urlSessionIdRef.current = null
        setRouteSessionId(null)
        setForeignProject(true)
        return
      }
      urlSessionIdRef.current = route.sessionId
      setRouteSessionId(route.sessionId)
      setForeignProject(false)
      if (resumedRef.current !== route.sessionId) {
        resumedRef.current = route.sessionId
        void resumeSession(route.sessionId)
      }
    }

    applyLocation()
    window.addEventListener('popstate', applyLocation)
    return () => window.removeEventListener('popstate', applyLocation)
  }, [active, projectId, resumeSession])

  // ── state → URL ────────────────────────────────────────────────────────────
  const activeSessionId = session?.id ?? null
  useEffect(() => {
    if (!active) return
    if (!activeSessionId) return
    if (urlSessionIdRef.current === activeSessionId) return

    // First session on a bare URL replaces the entry; later switches push, so
    // back/forward walks the sessions a human actually visited.
    const isFirst = urlSessionIdRef.current === null
    const path = buildSessionPath(projectId, activeSessionId)
    const url = path + window.location.search + window.location.hash
    if (isFirst) window.history.replaceState(null, '', url)
    else window.history.pushState(null, '', url)

    urlSessionIdRef.current = activeSessionId
    resumedRef.current = activeSessionId
    setRouteSessionId(activeSessionId)
    setForeignProject(false)
  }, [active, activeSessionId, projectId])

  const permalinkFor = useCallback(
    (sessionId: string) => buildSessionPermalink(baseUrl, projectId, sessionId),
    [baseUrl, projectId],
  )

  const openSession = useCallback(
    (sessionId: string) => {
      if (active && urlSessionIdRef.current !== sessionId) {
        const path = buildSessionPath(projectId, sessionId)
        window.history.pushState(null, '', path + window.location.search + window.location.hash)
        urlSessionIdRef.current = sessionId
        setRouteSessionId(sessionId)
        setForeignProject(false)
      }
      if (resumedRef.current !== sessionId) {
        resumedRef.current = sessionId
        void resumeSession(sessionId)
      }
    },
    [active, projectId, resumeSession],
  )

  return {
    routeSessionId,
    foreignProject,
    permalink: activeSessionId ? permalinkFor(activeSessionId) : null,
    permalinkFor,
    openSession,
  }
}

/**
 * Project id named by the current URL, if any — useful before the chat
 * provider exists (e.g. to pre-select the project a permalink points at, so a
 * pasted link lands in the right project after login).
 */
export function projectIdFromLocation(pathname?: string): string | null {
  if (typeof window === 'undefined' && pathname === undefined) return null
  const route = parseSessionPermalink(pathname ?? window.location.pathname)
  if (route) return route.projectId
  // Bare project route: /p/<projectId>
  const segments = (pathname ?? window.location.pathname).split('/').filter(Boolean)
  const idx = segments.lastIndexOf(PROJECT_SEGMENT)
  if (idx !== -1 && segments[idx + 1]) return decodeURIComponent(segments[idx + 1]!)
  return null
}
