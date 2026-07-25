package main

// permalink.go — the canonical session permalink minted server-side.
//
// A permalink is the one stable, project-scoped URL for a session:
//
//	<public base URL>/p/<projectID>/s/<sessionID>
//
// The web UI owns the same route (web/src/permalink.ts,
// SESSION_PERMALINK_FORMAT = "/p/:projectId/s/:sessionId"); keep the two in
// step, the format is the contract between them.
//
// It is load-bearing, not cosmetic: memory search results carry the session
// that wrote each memory (§7.3), config-log entries carry the acting session
// (§15.2), image/skill records carry their creating session (§13.2, §14.1) and
// `request_human_attention` posts `{message, session_url}` to the project's
// attention channel (§9). Every one of those is this string.
//
// Config:
//
//	AGENTKIT_PUBLIC_BASE_URL=https://orange.example.com
//
// The externally-reachable base URL of the *web UI* — where a human clicking a
// permalink should land. This is deliberately not AGENTKIT_SELF_URL: that one
// is how a session container reaches agentd from inside DinD (a bridge IP),
// which no human can open. Unset → the compose default (http://localhost:8080,
// matching WEB_PORT), so the standalone stack mints working links with no
// configuration.

import (
	"fmt"
	"net/url"
	"strings"
)

// defaultPublicBaseURL matches the standalone stack's web service
// (docker-compose.yml: WEB_PORT defaults to 8080).
const defaultPublicBaseURL = "http://localhost:8080"

// permalinker mints session permalinks against a fixed public base URL.
// The zero value mints root-relative paths, which is what a host that serves
// the UI and the API from one origin wants.
type permalinker struct {
	base string // no trailing slash; may be "" for root-relative links
}

// resolvePublicBaseURL reads AGENTKIT_PUBLIC_BASE_URL into a validated,
// trailing-slash-free base. It must be absolute (scheme + host) because the
// links it produces are pasted into webhooks, memory results and chat messages
// far away from any browsing context that could resolve a relative one.
func resolvePublicBaseURL(env func(string) string) (permalinker, error) {
	raw := strings.TrimSpace(getOr(env, "AGENTKIT_PUBLIC_BASE_URL", defaultPublicBaseURL))
	base := strings.TrimRight(raw, "/")
	if base == "" {
		return permalinker{}, fmt.Errorf("AGENTKIT_PUBLIC_BASE_URL is empty (want e.g. %s)", defaultPublicBaseURL)
	}
	u, err := url.Parse(base)
	if err != nil {
		return permalinker{}, fmt.Errorf("AGENTKIT_PUBLIC_BASE_URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return permalinker{}, fmt.Errorf("AGENTKIT_PUBLIC_BASE_URL %q: want an absolute http(s) URL (e.g. %s)", raw, defaultPublicBaseURL)
	}
	if u.Host == "" {
		return permalinker{}, fmt.Errorf("AGENTKIT_PUBLIC_BASE_URL %q: missing host", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return permalinker{}, fmt.Errorf("AGENTKIT_PUBLIC_BASE_URL %q: must not carry a query or fragment", raw)
	}
	return permalinker{base: base}, nil
}

// SessionURL returns the canonical permalink for a session in a project.
// Both ids are path-escaped, so the result round-trips through the UI's
// parseSessionPermalink. An empty session id yields "" — callers stamping
// provenance on rows that have no session (a human edit through the API, say)
// get an empty session_url rather than a link to nowhere.
func (p permalinker) SessionURL(projectID, sessionID string) string {
	if sessionID == "" || projectID == "" {
		return ""
	}
	return p.base + "/p/" + url.PathEscape(projectID) + "/s/" + url.PathEscape(sessionID)
}

// BaseURL is the resolved public base, without a trailing slash.
func (p permalinker) BaseURL() string { return p.base }
