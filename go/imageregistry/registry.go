// Package imageregistry defines ImageRegistry — how images become available to
// an ExecutionEnvironment and how snapshots of running sessions are preserved and
// retrieved. Orthogonal to, and composable with, execenv.
//
// See docs/03-image-registry.md.
package imageregistry

import (
	"context"

	"github.com/bayes-price/agentkit/execenv"
)

// ImageRegistry provides images to an ExecutionEnvironment and persists images
// produced from it. Implementations range from "build locally, save to a tar"
// (dev) to "push/pull a real OCI registry" (production).
type ImageRegistry interface {
	// EnsurePresent guarantees the image is available to the engine that will run
	// it (pull if needed; no-op if locally built/loaded).
	EnsurePresent(ctx context.Context, ref execenv.ImageRef) error

	// Build produces an image from a build context and returns the resulting ref.
	Build(ctx context.Context, spec BuildSpec) (execenv.ImageRef, error)

	// Resolve returns an existing ref for a BuildSpec WITHOUT building (ok=false
	// on cache miss). The Runner calls Resolve first and Build only on a miss, so
	// a returning user with unchanged customisations cache-hits instantly. Tags are
	// content-addressed: sha256(base digest + sorted overlay/artifact hashes + build
	// args + SourceKey).
	Resolve(ctx context.Context, spec BuildSpec) (ref execenv.ImageRef, ok bool, err error)

	// Persist stores an in-engine image ref durably and returns a Handle that
	// survives process/host restarts.
	Persist(ctx context.Context, ref execenv.ImageRef, opts PersistOptions) (Handle, error)

	// Materialize is the inverse of Persist: make the persisted image present for
	// the engine and return a runnable ref.
	Materialize(ctx context.Context, h Handle) (execenv.ImageRef, error)

	// Remove discards a persisted image.
	Remove(ctx context.Context, h Handle) error

	// Capabilities lets the orchestration core adapt (e.g. choose ForceFull
	// snapshots when the registry cannot do diffs).
	Capabilities() Capabilities
}

// Handle is an opaque, durable, JSON-serialisable pointer to a persisted image.
// Its concrete meaning is adapter-specific (file path, blob path + metadata,
// registry reference). The orchestration core stores it on the session row and
// passes it back to Materialize on restore.
type Handle struct {
	Kind string            `json:"kind"` // "local-tar" | "blob-archive" | "registry"
	Ref  string            `json:"ref"`  // path / blobPath / registry-ref
	Meta map[string]string `json:"meta,omitempty"`
}

// BuildSpec describes an image to build. BaseImage+Overlays is the skill-layering
// path (copy folders in at build time); Dockerfile is the alternative.
type BuildSpec struct {
	BaseImage  execenv.ImageRef
	Overlays   []Overlay
	Dockerfile string
	BuildArgs  map[string]string
	Tag        string
	ContextDir string

	// Layer names which stratum this build produces (core | app | user). Lets the
	// registry/fleet reason about cache affinity and diff bases.
	Layer ImageLayer

	// SourceKey is the identity of the inputs (e.g. user id, skill-set id) folded
	// into the content-hash tag so a returning user with unchanged inputs cache-hits.
	SourceKey string
}

// ImageLayer names which stratum a build produces.
type ImageLayer string

const (
	// LayerCore is the agentkit base image (in-image agent + all harness binaries).
	LayerCore ImageLayer = "core"
	// LayerApp is the base + product binaries/skills (e.g. Platinum's pt) — the
	// default launch image.
	LayerApp ImageLayer = "app"
	// LayerUser is app + a curated set of artifacts (see "User images").
	LayerUser ImageLayer = "user"
)

// Overlay is a directory copied into the image at build time (e.g. a skill folder).
type Overlay struct {
	Source string
	Target string
}

// PersistOptions controls how an image is persisted.
type PersistOptions struct {
	SessionID  string
	PreferDiff bool             // request the diff-archive fast path where supported
	BaseImage  execenv.ImageRef // for diff: the base to diff against (MUST be the launch image)
}

// Capabilities describes the registry's abilities.
type Capabilities struct {
	SupportsDiff   bool
	SupportsBuild  bool
	SupportsRemote bool
	// PortableHandles is true when a Handle produced here can be Materialized on a
	// DIFFERENT worker (blob-archive with a shared BlobStore, or a remote registry).
	// local-tar is NOT portable. Multi-worker fleets require PortableHandles=true;
	// validated at Fleet construction.
	PortableHandles bool
}
