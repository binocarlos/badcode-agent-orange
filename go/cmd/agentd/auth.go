package main

import (
	"context"
	"net/http"

	"github.com/bayes-price/agentkit/extension"
	"github.com/bayes-price/agentkit/httpapi"
	"github.com/golang-jwt/jwt/v5"
)

// principal is the authenticated identity derived from a verified JWT.
type principal struct{ email, customer string }

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

// jwtAuthMiddleware verifies an HS256 bearer JWT signed with secret (the
// JWT-delegation model: apps mint tokens, agentd only verifies). Claims follow
// devclaims: "email" + "customer". An empty secret enables dev-open mode (a
// default principal, no verification) for the zero-config demo.
func jwtAuthMiddleware(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(secret) == 0 {
			next.ServeHTTP(w, r.WithContext(contextWithPrincipal(
				r.Context(), principal{email: "demo@example.com", customer: "demo"})))
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
		p := principal{}
		if v, ok := claims["email"].(string); ok {
			p.email = v
		}
		if v, ok := claims["customer"].(string); ok {
			p.customer = v
		}
		next.ServeHTTP(w, r.WithContext(contextWithPrincipal(r.Context(), p)))
	})
}

// identityFromRequest is the httpapi.IdentityFunc: it reads what the middleware set.
func identityFromRequest(r *http.Request) (httpapi.Identity, error) {
	p, _ := principalFromContext(r.Context())
	return httpapi.Identity{UserEmail: p.email, Customer: p.customer}, nil
}
