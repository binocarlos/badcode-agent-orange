// Canonical session permalink — the one stable, project-scoped URL for a session.
//
// Format (F3, spec §7.3/§9/§13.2/§15.2):
//
//     <public base URL>/p/<projectId>/s/<sessionId>
//
// Server-side code mints the same string (agentd: AGENTKIT_PUBLIC_BASE_URL +
// go/cmd/agentd/permalink.go) so memory hits, config-log entries, image/skill
// provenance and `request_human_attention` webhooks all point at exactly this
// route. Keep the two implementations in step — the format is the contract.
//
// Everything here is pure: no React, no window access. The hook that binds the
// route to the live/replay session lives in useSessionPermalink.ts.

/** Path prefix for the project segment. */
export const PROJECT_SEGMENT = 'p'
/** Path prefix for the session segment. */
export const SESSION_SEGMENT = 's'

/** Human-readable template — the canonical format, for docs and error messages. */
export const SESSION_PERMALINK_FORMAT = `/${PROJECT_SEGMENT}/:projectId/${SESSION_SEGMENT}/:sessionId`

export interface SessionRoute {
  projectId: string
  sessionId: string
}

/**
 * Root-relative path for a session, e.g. `/p/acme/s/abc123`.
 * Both ids are URL-encoded, so ids containing `/` or `?` round-trip.
 */
export function buildSessionPath(projectId: string, sessionId: string): string {
  return (
    `/${PROJECT_SEGMENT}/${encodeURIComponent(projectId)}` +
    `/${SESSION_SEGMENT}/${encodeURIComponent(sessionId)}`
  )
}

/**
 * Absolute permalink: `baseUrl` (the externally-reachable origin of the UI,
 * with or without a trailing slash, optionally including a sub-path) + the
 * session path. An empty `baseUrl` yields the root-relative path.
 */
export function buildSessionPermalink(
  baseUrl: string,
  projectId: string,
  sessionId: string,
): string {
  return trimTrailingSlashes(baseUrl) + buildSessionPath(projectId, sessionId)
}

/**
 * Parse a permalink — absolute URL, root-relative path, or a path carrying a
 * query string / hash — back into its ids. Returns null when the input is not
 * a session route. Base paths are tolerated: only the final
 * `/p/<id>/s/<id>` pair is matched, so a UI mounted under a sub-path still
 * parses.
 */
export function parseSessionPermalink(input: string): SessionRoute | null {
  if (!input) return null

  let pathname = input
  // Strip scheme + authority for absolute URLs (URL() would need a base for
  // relative input, and we want one code path for both).
  const schemeEnd = pathname.indexOf('://')
  if (schemeEnd !== -1) {
    const afterAuthority = pathname.indexOf('/', schemeEnd + 3)
    pathname = afterAuthority === -1 ? '' : pathname.slice(afterAuthority)
  }
  // Drop query and hash.
  pathname = pathname.split('#')[0]!.split('?')[0]!

  const segments = pathname.split('/').filter((s) => s.length > 0)
  if (segments.length < 4) return null

  const [pSeg, rawProject, sSeg, rawSession] = segments.slice(-4)
  if (pSeg !== PROJECT_SEGMENT || sSeg !== SESSION_SEGMENT) return null
  if (!rawProject || !rawSession) return null

  const projectId = safeDecode(rawProject)
  const sessionId = safeDecode(rawSession)
  if (!projectId || !sessionId) return null

  return { projectId, sessionId }
}

function trimTrailingSlashes(s: string): string {
  let end = s.length
  while (end > 0 && s[end - 1] === '/') end--
  return s.slice(0, end)
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    // Malformed percent-escape — treat the raw segment as the id rather than
    // throwing out of a render path.
    return s
  }
}
