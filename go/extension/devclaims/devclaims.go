// Package devclaims is a DEV-ONLY ScopedClaimsIssuer that signs short-lived
// HS256 JWTs. NOT for production: a single static secret, no rotation, no
// audience checks. Provided so examples/tests have a working issuer without
// requiring a real key-management system.
package devclaims

import (
	"context"
	"time"

	"github.com/bayes-price/agentkit/extension"
	"github.com/golang-jwt/jwt/v5"
)

// Issuer signs short-lived HS256 JWTs. DEV-ONLY — single static secret,
// no rotation, no audience validation.
type Issuer struct {
	secret []byte
	ttl    time.Duration
}

// New creates a new Issuer using the given HMAC secret. The TTL is 1 hour.
// Do NOT use in production.
func New(secret []byte) *Issuer { return &Issuer{secret: secret, ttl: time.Hour} }

// compile-time interface check
var _ extension.ScopedClaimsIssuer = (*Issuer)(nil)

// Issue signs an HS256 JWT containing claims: sid, customer, job, email, iat,
// exp (1h TTL). The token can be verified by any party holding the same secret.
func (i *Issuer) Issue(_ context.Context, scope extension.ContextScope, sessionID string) (string, error) {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sid":      sessionID,
		"customer": scope.Customer,
		"job":      scope.Job,
		"email":    scope.UserEmail,
		"iat":      now.Unix(),
		"exp":      now.Add(i.ttl).Unix(),
	})
	return tok.SignedString(i.secret)
}
