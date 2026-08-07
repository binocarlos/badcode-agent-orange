// Clickjacking control for the embed page (T13 of
// design/2026-08-06-embeddable-agent-orange.md).
//
//	GET /embed/csp  →  204, Content-Security-Policy: frame-ancestors <origins>
//
// The embed page's HTML is a static file in the web image, served by nginx
// (deploy/web.nginx.conf), and nginx cannot know the origin list: it lives in
// the project map, which only agentd reads. So nginx asks agentd for the header
// with an `auth_request` subrequest and copies the answer onto the static
// response. The alternative the ticket offered — an nginx `map` keyed by
// project — is not implementable (see below) and would in any case have made
// this repo's second copy of the origin list, which drifts.
//
// # Which project's origins?
//
// Every project's, unioned, because the request does not identify a project and
// cannot be made to:
//
//   - the embed URL is /embed/session/<name> (the design's "Embed page
//     contract"), which carries no project segment;
//   - the only credential is in the URL FRAGMENT, which a browser never sends,
//     so the request that needs this header arrives with no identity at all;
//   - resolving <name> → project would need a cross-tenant name lookup, which
//     T6 deliberately does not provide (GetSessionByName is keyed on
//     (customer, name)) and which would itself be a session-name oracle.
//
// The union is sound because frame-ancestors is not the authority here, it is
// defence in depth against clickjacking. An origin that framed the wrong
// project's embed page would still need an embed token for that project, and a
// token is minted server-to-server against a project API key and confined to
// one session (embedtoken.go). Every origin in the union is one BadCode ops
// deliberately configured and T1 validated.
package main

import (
	"net/http"
	"sort"
	"strings"
)

// embedCSPPath is agentd's side of the subrequest. It is not reachable through
// nginx from a browser: nginx serves /embed/* statically and mounts this only as
// an `internal` location.
const embedCSPPath = "/embed/csp"

// frameAncestors renders the header value.
//
// Deduped and sorted so the value is byte-identical across restarts and across
// two hosts reading the same map — a header that reorders itself run to run is
// miserable to diff in an incident.
//
// The entries are interpolated verbatim, which is safe because T1 parses each
// one as scheme://host[:port] and rejects anything carrying a path, query,
// fragment, userinfo or trailing slash (googleauth.go) — so no entry can
// contain whitespace, let alone a CR/LF that would split the header.
func frameAncestors(origins []string) string {
	seen := make(map[string]bool, len(origins))
	list := make([]string, 0, len(origins))
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		list = append(list, o)
	}
	if len(list) == 0 {
		// No origins configured → nobody may frame the page. Emitting 'none'
		// rather than omitting the header: an absent CSP means "frame me
		// anywhere", which is the opposite of what an unconfigured project asked
		// for. A deployment that wants framing says so in the project map.
		return "frame-ancestors 'none'"
	}
	sort.Strings(list)
	return "frame-ancestors " + strings.Join(list, " ")
}

// embedCSPHandler answers the subrequest.
//
// The value is computed once, at wiring time: the project map is boot config
// (there is no api_keys table and no reload — see apikey.go), so recomputing it
// per request would only buy a sort on every embed page load.
func embedCSPHandler(origins []string) http.HandlerFunc {
	value := frameAncestors(origins)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", value)
		// 204: nginx's auth_request needs a 2xx and reads only the headers —
		// any body it received would be discarded.
		w.WriteHeader(http.StatusNoContent)
	}
}

// registerEmbedCSP mounts the route on the UNAUTHENTICATED mux, deliberately.
// Its caller is nginx's internal subrequest, which carries no credential — and
// neither does the browser request that triggers it, since the embed token
// travels in the fragment. The response discloses only the configured origin
// list, which the header itself broadcasts to every framer anyway.
func registerEmbedCSP(mux *http.ServeMux, origins []string) {
	mux.HandleFunc("GET "+embedCSPPath, embedCSPHandler(origins))
}
