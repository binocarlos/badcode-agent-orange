// Embed tokens: the short-lived, single-session credential an embedding
// application hands to a browser (design/2026-08-06-embeddable-agent-orange.md,
// T10).
//
//	POST /agent/embed-token   {session: "<name>", ttl_seconds?: int}
//	  auth : a project API KEY, and nothing else
//	  200  : {token, expires_at}
//	  400  : no session name in the body
//	  403  : the caller is not an API key (or carries no project)
//	  404  : no such name in this project, WHATEVER the reason
//	  501  : the deployment cannot support embed tokens (see below)
//
// The shape of the whole feature is: the embedding app's BACKEND asks for a
// token naming the session it wants shown, and drops the answer into an iframe
// URL fragment. The browser never authenticates to Agent Orange and never holds
// anything that outlives the page.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/golang-jwt/jwt/v5"
)

// embedSessionLookup is the narrow read seam this route needs: resolve a name
// inside one project. *agentdb.Store satisfies it (T6/T7), and the lookup is
// scoped to the customer by the (customer, name) index, so a foreign name is
// simply not found — there is no separate tenancy check to forget here.
type embedSessionLookup interface {
	GetSessionByName(ctx context.Context, customer, name string) (*agentdb.Session, error)
}

var _ embedSessionLookup = (*agentdb.Store)(nil)

// TTL bounds, in seconds. The default is short because the token travels in a
// URL fragment inside somebody else's page; the floor exists because a token
// that expires before the iframe finishes loading is just a broken embed, and
// the ceiling because this credential's confinement (one session id) is
// enforced only on session-by-id routes — see the design's HIGH entry — so its
// lifetime is the other half of the bound.
const (
	embedTokenDefaultTTL = 900 * time.Second
	embedTokenMinTTL     = 60 * time.Second
	embedTokenMaxTTL     = 3600 * time.Second
)

// embedTokenRequest is the body. `session` is a NAME, never an id: an embedding
// app persists the name it chose ("hypothesis-a") and never has to store a uuid
// Agent Orange minted.
type embedTokenRequest struct {
	Session    string `json:"session"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// embedTokenResponse is what the app's backend puts in the iframe fragment.
// ExpiresAt is unix SECONDS and is the token's own `exp` claim, so the caller's
// idea of when to re-mint and the server's idea of when to refuse agree exactly.
type embedTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// clampEmbedTTL applies the [60s, 3600s] bound. Clamped, never rejected: an
// integrator who asks for a day should get an hour and a working iframe, not a
// 400 they find out about in production. A zero (absent, or literally 0) is the
// default rather than the floor — "I did not choose" is a different statement
// from "I chose 0", and JSON cannot tell them apart without a pointer.
func clampEmbedTTL(seconds int) time.Duration {
	if seconds == 0 {
		return embedTokenDefaultTTL
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl < embedTokenMinTTL {
		return embedTokenMinTTL
	}
	if ttl > embedTokenMaxTTL {
		return embedTokenMaxTTL
	}
	return ttl
}

// embedTokenHandler serves POST /agent/embed-token.
//
// secret must be the SAME value apiAuthMiddleware verifies bearer tokens with
// (main.go's jwtSecret). Signing with anything else mints a token the API
// answers 401 to, which is a failure nothing would report until an integrator
// hit it.
func embedTokenHandler(secret []byte, sessions embedSessionLookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// API-key auth ONLY. The middleware accepts a bearer JWT too, and a
		// browser holds exactly that — so without this check a console token, or
		// an embed token itself, could mint fresh embed tokens for any session in
		// the project. Same mechanism and same reasoning as T11's
		// /auth/verify-google (googleauth.go:446).
		if !authenticatedByAPIKey(r) {
			http.Error(w, "project api key required", http.StatusForbidden)
			return
		}
		// Two deployments cannot support this route, and both say so rather than
		// handing back a token nothing will accept. Session names exist only on
		// Postgres (there is no `name` column in the sqlite fallback's schema),
		// and with no AGENTKIT_JWT_SECRET the middleware's bearer path refuses
		// every token unconditionally (auth.go:84-89).
		if sessions == nil {
			http.Error(w, "session names are not configured on this host (embed tokens require DATABASE_URL)",
				http.StatusNotImplemented)
			return
		}
		if len(secret) == 0 {
			http.Error(w, "embed tokens require AGENTKIT_JWT_SECRET (without it no bearer token is accepted)",
				http.StatusNotImplemented)
			return
		}

		p, _ := principalFromContext(r.Context())
		if p.customer == "" {
			// Names live in the (customer, name) index; a credential with no
			// project has nothing to resolve against. Same answer, for the same
			// reason, as httpapi's by-name route (sessions_byname.go:107-113).
			http.Error(w, "no project in token", http.StatusForbidden)
			return
		}

		var body embedTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Session == "" {
			http.Error(w, "missing session name", http.StatusBadRequest)
			return
		}

		sess, err := sessions.GetSessionByName(r.Context(), p.customer, body.Session)
		if err != nil || sess == nil {
			if err != nil && !errors.Is(err, agentdb.ErrSessionNotFound) {
				// A store outage is not a missing session; reporting it as one
				// sends an operator hunting for a name that is sitting right there.
				http.Error(w, "session lookup failed", http.StatusInternalServerError)
				return
			}
			// Absent, malformed and "belongs to another project" are ONE answer.
			// The lookup is already scoped to p.customer, so a foreign name does
			// not match — and the three must stay indistinguishable, or a key for
			// one project becomes an oracle for another project's session names.
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		ttl := clampEmbedTTL(body.TTLSeconds)
		// TTL is an issuer property and the scope is a per-token one (T3's
		// deliberate split), so a per-request clamped TTL means constructing a
		// fresh two-field issuer per request — which costs nothing. The plan's
		// prose named a `devclaims.NewScoped`; no such function was built.
		//
		// The sessionID argument is deliberately EMPTY. It would become the `sid`
		// claim, and `sid` already means something else and dangerous: agentd
		// signs per-session container tokens with this same secret in every real
		// deployment (main.go:129-131), and the core MCP server authenticates a
		// caller by exactly that claim (mcpserver.go:464-505). An embed token
		// carrying sid=<session> would therefore be a working credential for
		// /mcp's memory and worker-prompt tools — handed to a browser inside a
		// third-party page. The confinement rides on the `scope` claim, which is
		// the claim built for the question "what may this token touch".
		token, err := devclaims.NewWithTTL(secret, ttl).IssueScoped(r.Context(), extension.ContextScope{
			UserEmail: p.email,
			Customer:  p.customer,
			Job:       "embed",
		}, "", devclaims.SessionScope(sess.ID))
		if err != nil {
			http.Error(w, "token generation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Read exp back off the token we just signed rather than recomputing it.
		// IssueScoped stamps exp from its own time.Now(), so a second computation
		// here lands one second later whenever the call straddles a second
		// boundary — and a body promising an expiry the token does not have is
		// exactly the kind of off-by-one that shows up as a rare, unreproducible
		// 401 in somebody else's product.
		exp, err := embedTokenExpiry(secret, token)
		if err != nil {
			http.Error(w, "token generation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedTokenResponse{Token: token, ExpiresAt: exp})
	}
}

// embedTokenExpiry returns the `exp` claim of a token this process just minted.
func embedTokenExpiry(secret []byte, token string) (int64, error) {
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return secret, nil
	}, jwt.WithValidMethods([]string{"HS256"})); err != nil {
		return 0, err
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return 0, errors.New("minted token carries no expiry")
	}
	return exp.Unix(), nil
}

// registerEmbedToken mounts POST /agent/embed-token on the AUTHENTICATED mux.
//
// Unlike registerVerifyGoogle, this one registers unconditionally: the route is
// part of the product on every deployment, and a host missing a dependency for
// it answers 501 with the reason (see the handler) rather than 404, which would
// read as "you spelled the path wrong".
func registerEmbedToken(mux *http.ServeMux, secret []byte, sessions embedSessionLookup) {
	mux.Handle("POST /agent/embed-token", embedTokenHandler(secret, sessions))
}
