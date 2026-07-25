// Package extension defines the seams a HOST application implements to use
// agentkit. The library is generic; everything product-specific is injected
// through one of these interfaces (or through tool/render plugins).
//
// See docs/10-extension-points.md.
package extension

import (
	"context"
	"io"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// ContextScope identifies who/what a turn is for.
type ContextScope struct {
	Customer  string
	Job       string
	Persona   string
	UserEmail string
}

// SessionContext carries the resolved per-session context (system prompt, base
// image and MCP configuration) that the Runner uses when provisioning or
// resuming a session.
type SessionContext struct {
	SystemPrompt string
	// BaseImage is the host's resolved image chain (docs/product §5:
	// worker image > project base_image > the host's own global default). The
	// Runner ranks it above Policy.BaseImage and below an explicit request
	// image — see runner.go:resolveLaunchImage.
	//
	// It may hold an UNRESOLVED §13 pointer when the winning layer was a
	// worker's `image` column; WorkerImage below says so, and is what the
	// Runner actually resolves. A host with no image catalogue simply never
	// sets WorkerImage, and BaseImage is used verbatim as it always was.
	BaseImage string

	// WorkerImage is the worker's §13 image pointer — a bare `name` (floating:
	// the latest version in the project) or `name:version` (pinned) — carried
	// UNRESOLVED, because §13.3 resolution belongs to the image catalogue and
	// not to this seam. Empty when the worker sets no pointer, which is the
	// only case a host without images ever produces.
	//
	// When it is set the Runner resolves it through Deps.Images and launches
	// from the result. A resolution failure (unknown name, reaped version,
	// nothing to materialise) FAILS THE LAUNCH: a worker that was pointed at an
	// environment and quietly got a different one is exactly the drift §13
	// exists to prevent (docs/product/08-images-and-skills.md §13.3, §13.5).
	WorkerImage string

	// MCPServers is the project ∪ worker MCP configuration the host resolved for
	// this session (docs/product/01-session-config.md §4.1, §5). Without this
	// field the union a host computes has no route into the container — the
	// Runner would only ever ship what the create request itself carried.
	//
	// These are *defaults*: the Runner merges them UNDER the request-supplied
	// servers, so a CreateSessionRequest entry wins a name collision (§5:
	// "the defaults which the request may extend").
	MCPServers agentdb.MCPServers
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
