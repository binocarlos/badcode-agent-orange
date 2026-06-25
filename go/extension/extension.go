// Package extension defines the seams a HOST application implements to use
// agentkit. The library is generic; everything product-specific is injected
// through one of these interfaces (or through tool/render plugins).
//
// See docs/10-extension-points.md.
package extension

import (
	"context"
	"io"

	"github.com/bayes-price/agentkit/artifacts"
)

// ContextScope identifies who/what a turn is for.
type ContextScope struct {
	Customer  string
	Job       string
	Persona   string
	UserEmail string
}

// SessionContext carries the resolved per-session context (system prompt and
// base image) that the Runner uses when provisioning or resuming a session.
type SessionContext struct {
	SystemPrompt string
	BaseImage    string
}

// SessionContextProvider assembles the per-session context for a turn. Platinum
// merges cascading config + brand themes + persona; the Runner appends the result
// to systemPrompt and never interprets it. Default (nil) contributes "".
type SessionContextProvider interface {
	Resolve(ctx context.Context, scope ContextScope) (*SessionContext, error)
}

// BlobStore is the byte backend for a single scoped namespace (a session or a
// global bucket). Keys are opaque strings; the factory binds account+container.
type BlobStore interface {
	Write(ctx context.Context, key string, r io.Reader) error
	Read(ctx context.Context, key string) (io.ReadCloser, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// BlobStoreFactory creates BlobStore instances scoped to a session or a global
// namespace. ForSession resolves the customer/job from the session row and
// returns a store bound to that container. Global binds a named namespace.
type BlobStoreFactory interface {
	ForSession(ctx context.Context, sessionID string) (BlobStore, error)
	Global(namespace string) BlobStore
}

// ScopedClaimsIssuer mints the per-session token injected into the instance and
// forwarded on the message proxy. Platinum issues an HS256 JWT scoped to
// customer/job/session.
type ScopedClaimsIssuer interface {
	Issue(ctx context.Context, scope ContextScope, sessionID string) (token string, err error)
}

// Usage is token usage parsed from query_complete/result events.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalCostUSD float64
	Model        string
}

// TokenUsageLogger receives usage for costing. Default (nil) is a no-op.
type TokenUsageLogger interface {
	Log(ctx context.Context, sessionID string, usage Usage)
}

// ArtifactEnricher lets the host decorate artifact metadata before persistence
// (brand colours, publish paths, labels). Default (nil) is identity.
type ArtifactEnricher interface {
	Enrich(ctx context.Context, art *artifacts.Artifact) error
}

// Metrics is the pluggable metrics surface (Prometheus in Platinum). Default
// (nil) is a no-op.
type Metrics interface {
	ObserveLifecycle(phase string, seconds float64)
	SetGauge(name string, v float64)
	Inc(name string)
}
