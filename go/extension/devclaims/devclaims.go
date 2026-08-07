// Package devclaims is a DEV-ONLY ScopedClaimsIssuer that signs short-lived
// HS256 JWTs. NOT for production: a single static secret, no rotation, no
// audience checks. Provided so examples/tests have a working issuer without
// requiring a real key-management system.
package devclaims

import (
	"context"
	"time"

	"github.com/binocarlos/badcode-agent-orange/extension"
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

// NewWithTTL is New with a caller-chosen TTL — e.g. login tokens that must
// outlive the 1-hour session-claims default. Same DEV-ONLY caveats.
func NewWithTTL(secret []byte, ttl time.Duration) *Issuer {
	return &Issuer{secret: secret, ttl: ttl}
}

// compile-time interface check
var _ extension.ScopedClaimsIssuer = (*Issuer)(nil)

// ScopeClaim is the optional authorization-scope claim. It answers "what may
// this token touch", which is a different question from sid ("which session
// minted or owns this token") — and they must stay different questions, because
// agentd signs both families with the same secret in every real deployment.
//
// Absent or empty means unrestricted within the customer claim: the shape every
// console login token has, and the reason adding this claim changed no existing
// caller's behaviour.
const ScopeClaim = "scope"

// sessionScopePrefix namespaces the one scope value that exists today, so a
// later kind of scope ("project:…", "worker:…") cannot be confused for it.
const sessionScopePrefix = "session:"

// SessionScope builds the scope value confining a token to one session id.
func SessionScope(sessionID string) string { return sessionScopePrefix + sessionID }

// ParseSessionScope is SessionScope's inverse: it returns the session id a scope
// value confines a token to. ok=false for an empty scope (unrestricted) or a
// scope of some other kind.
func ParseSessionScope(scope string) (sessionID string, ok bool) {
	if len(scope) <= len(sessionScopePrefix) || scope[:len(sessionScopePrefix)] != sessionScopePrefix {
		return "", false
	}
	return scope[len(sessionScopePrefix):], true
}

// Issue signs an HS256 JWT containing claims: sid, customer, job, email, iat,
// exp (1h TTL). The token can be verified by any party holding the same secret.
func (i *Issuer) Issue(ctx context.Context, scope extension.ContextScope, sessionID string) (string, error) {
	return i.IssueScoped(ctx, scope, sessionID, "")
}

// IssueScoped is Issue plus an authorization scope carried on this one token.
//
// Per token, not per issuer, deliberately: an issuer-level scope would force a
// fresh Issuer for every embed-token request, and would duplicate what sid
// already carries. Callers wanting a different TTL still need NewWithTTL — TTL
// is a genuine property of an issuer in a way a scope is not.
//
// An empty scope writes no claim at all, so Issue's tokens are byte-identical
// to what they were before this existed.
func (i *Issuer) IssueScoped(_ context.Context, cs extension.ContextScope, sessionID, scope string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sid":      sessionID,
		"customer": cs.Customer,
		"job":      cs.Job,
		"email":    cs.UserEmail,
		"iat":      now.Unix(),
		"exp":      now.Add(i.ttl).Unix(),
	}
	if scope != "" {
		claims[ScopeClaim] = scope
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(i.secret)
}
