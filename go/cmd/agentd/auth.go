package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/binocarlos/badcode-agent-orange/httpapi"
	"github.com/golang-jwt/jwt/v5"
)

// principal is the authenticated identity behind a request, from either
// credential the API accepts.
type principal struct {
	email, customer string
	// embedSession is non-empty only for a token carrying scope
	// "session:<id>" — an embed token, minted for a browser inside a
	// third-party page. It confines the credential to that one session;
	// enforcement lives in httpapi, beside the existing ownership check.
	embedSession string
}

type ctxKey struct{}

func contextWithPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func principalFromContext(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(principal)
	return p, ok
}

// extensionScope is a tiny helper used by tests + /dev/token to build a scope.
func extensionScope(email, customer string) extension.ContextScope {
	return extension.ContextScope{UserEmail: email, Customer: customer, Job: "demo-job"}
}

// apiKeyHeader is the header a project's backend sends. It is deliberately not
// Authorization: an API key and a bearer JWT are different credentials with
// different lifetimes, and keeping them in different headers means a proxy or a
// log filter can be taught about one without the other.
const apiKeyHeader = "X-API-Key"

// apiAuthMiddleware authenticates every API request from one of two credentials:
//
//	X-API-Key: <raw>            a long-lived project key, server-side only
//	Authorization: Bearer <jwt> an HS256 token signed with secret
//
// The key is tried first, because a caller that sent one meant to use it and
// should get a 401 rather than falling through to an anonymous mode.
//
// Both paths produce the same principal. The JWT path additionally carries an
// optional session scope (see principal.embedSession).
//
// An empty secret still enables dev-open mode — a default principal, no
// verification — for the zero-config demo, but ONLY when no project key is
// configured. A configured key means a real deployment, and dev-open there would
// hand every unauthenticated request the "demo" project.
func apiAuthMiddleware(secret []byte, keys projectKeys, next http.Handler) http.Handler {
	devOpen := len(secret) == 0 && !keys.hasKeys()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw := strings.TrimSpace(r.Header.Get(apiKeyHeader)); raw != "" {
			project, ok := keys.ProjectForKey(raw)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// There is no human behind an API key. The email is a stable,
			// obviously-synthetic label so anything that records "who did this"
			// records the project's key rather than an empty string, which is
			// how a human edit is spelled elsewhere.
			next.ServeHTTP(w, r.WithContext(contextWithPrincipal(
				r.Context(), principal{email: apiKeyEmail(project), customer: project})))
			return
		}
		if devOpen {
			next.ServeHTTP(w, r.WithContext(contextWithPrincipal(
				r.Context(), principal{email: "demo@example.com", customer: "demo"})))
			return
		}
		if len(secret) == 0 {
			// Keys are configured but this request carried none, and there is no
			// secret to verify a bearer token with. Nothing can authenticate it.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		auth := r.Header.Get("Authorization")
		if len(auth) < 8 || auth[:7] != "Bearer " {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		claims := jwt.MapClaims{}
		tok, err := jwt.ParseWithClaims(auth[7:], claims, func(*jwt.Token) (any, error) {
			return secret, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || !tok.Valid {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// A token carrying a non-empty `sid` is a SESSION token: the credential
		// agentd injects into a container as SESSION_TOKEN, readable by the
		// harness and therefore reachable by a prompt-injected model. It is a
		// different credential class from an API token and must never
		// authenticate a project route (doc 22, RD30).
		//
		// The two classes are signed with different keys (sessionsecret.go), so
		// a session token cannot reach this line at all. This is the second
		// lock, for the deployment that sets AGENTKIT_SESSION_JWT_SECRET to the
		// API secret and for any future issuer that stamps sid by accident.
		// Every API-class token — login, wildcard-exchange, /dev/token, embed —
		// is issued with an empty session id; embed tokens confine themselves
		// with the scope claim below, never with sid.
		if sid, _ := claims["sid"].(string); sid != "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		p := principal{}
		if v, ok := claims["email"].(string); ok {
			p.email = v
		}
		if v, ok := claims["customer"].(string); ok {
			p.customer = v
		}
		if v, ok := claims[devclaims.ScopeClaim].(string); ok {
			if sid, scoped := devclaims.ParseSessionScope(v); scoped {
				p.embedSession = sid
			}
		}
		next.ServeHTTP(w, r.WithContext(contextWithPrincipal(r.Context(), p)))
	})
}

// apiKeyEmail is the synthetic principal email an API key authenticates as.
func apiKeyEmail(project string) string { return "api-key:" + project }

// identityFromRequest is the httpapi.IdentityFunc: it reads what the middleware set.
func identityFromRequest(r *http.Request) (httpapi.Identity, error) {
	p, _ := principalFromContext(r.Context())
	return httpapi.Identity{
		UserEmail:    p.email,
		Customer:     p.customer,
		SessionScope: p.embedSession,
	}, nil
}
