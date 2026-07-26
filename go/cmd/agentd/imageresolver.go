package main

// imageresolver.go — the §13 image pointer, bound (spec
// 08-images-and-skills.md §13.3, §13.5, §13.6; work-plan I4).
//
// A worker's `image` column is one text field: empty, a bare `name` (floating —
// the latest version in the project), or `name:version` (pinned). Turning that
// into something a container can be launched from is three steps, and this file
// is the ONE place they happen:
//
//	worker.image ──ResolveCustomImage──▶ catalogue row ──Materialize──▶ launch ref
//	                     (I1, §13.3)         │            (imageregistry)
//	                                         └──MarkCustomImageResumed (§5 metadata)
//
// It is bound in exactly twice, in main.go, and both bindings are the same
// object:
//
//   - the dispatcher's `Images`, so job composition (§6.2 step 1) resolves the
//     pointer and the composed image travels to the Runner as an explicit image;
//   - the Runner's `Deps.Images`, so a session that reaches the launch path with
//     the pointer still unresolved (an interactive session on a worker — its
//     SessionContext carries WorkerImage) resolves it identically.
//
// One resolver, deliberately: a worker job and a chat with the same worker must
// not be able to launch from different environments.
//
// # Everything here fails the job
//
// §13.3 is explicit that resolution failure "fails the job loudly rather than
// silently falling back to the project default — a worker that was pointed at
// an environment and quietly got a different one is exactly the drift §13
// exists to prevent". So every error below propagates: an unknown name
// (ErrCustomImageNotFound), a version the TTL reaper tombstoned
// (ErrCustomImageReaped), a row with nothing to materialise
// (ErrCustomImageUnmaterialisable), and a registry that cannot produce the
// bytes. Composition turns that into a failed delivery (dispatch.go); the
// launch path turns it into ErrLaunchImageUnresolvable. Nothing anywhere
// substitutes another image.
//
// The ONE exception is the `last_resumed_at` stamp, which is best-effort by
// design: it is telemetry about an image, and losing it must never cost a job.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// imageResolveStore is the narrow slice of *agentdb.Store this needs. Read plus
// one runtime stamp — nothing that could publish, edit or delete a version, so
// §13.2's append-only surface survives the seam exactly as it does in
// mcp_images.go.
type imageResolveStore interface {
	ResolveCustomImage(ctx context.Context, project, ref string) (*agentdb.CustomImage, error)
	MarkCustomImageResumed(ctx context.Context, project, name string, version int, at int64) error
}

// imageMaterializer is the registry side: an opaque durable handle back into a
// ref the execution environment can run. *imageregistry* adapters satisfy it.
type imageMaterializer interface {
	Materialize(ctx context.Context, h imageregistry.Handle) (execenv.ImageRef, error)
}

// catalogueImageResolver implements agentkit.ImageResolver against the §13
// catalogue.
type catalogueImageResolver struct {
	store    imageResolveStore
	registry imageMaterializer
	logf     func(format string, v ...any)
}

// newCatalogueImageResolver binds the store and the registry. Both are
// required: a resolver missing either would resolve nothing, and a nil resolver
// is a meaningfully different (and correctly handled) state — see
// agentkit.Deps.Images.
func newCatalogueImageResolver(store imageResolveStore, registry imageMaterializer, logf func(string, ...any)) *catalogueImageResolver {
	if logf == nil {
		logf = log.Printf
	}
	return &catalogueImageResolver{store: store, registry: registry, logf: logf}
}

// Resolve implements agentkit.ImageResolver: `Resolve(ctx, project, ref)`.
//
// The project is passed in by the caller and never inferred — P5's hard
// namespace, the same rule every other §13 path follows.
func (c *catalogueImageResolver) Resolve(ctx context.Context, project, ref string) (string, error) {
	if c == nil || c.store == nil || c.registry == nil {
		return "", fmt.Errorf("image resolver is not configured")
	}
	// 1. §13.3 — bare name → latest, `name:version` → pinned, every failure loud.
	ci, err := c.store.ResolveCustomImage(ctx, project, ref)
	if err != nil {
		return "", classifyResolveError(ref, err)
	}
	// 2. The handle. I1 guarantees a non-empty RegistryHandle survives
	//    resolution, so an undecodable one is a genuinely corrupt row: report it
	//    as unmaterialisable rather than as a JSON error nobody can act on.
	handle, err := decodeRegistryHandle(ci.RegistryHandle)
	if err != nil {
		return "", fmt.Errorf("%w: %s:%d: %w", agentdb.ErrCustomImageUnmaterialisable, ci.Name, ci.Version, err)
	}
	launch, err := c.registry.Materialize(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("%w: %s:%d: %w", agentdb.ErrCustomImageUnmaterialisable, ci.Name, ci.Version, err)
	}
	if strings.TrimSpace(string(launch)) == "" {
		return "", fmt.Errorf("%w: %s:%d: the registry produced an empty ref", agentdb.ErrCustomImageUnmaterialisable, ci.Name, ci.Version)
	}

	// 3. §5's `last_resumed_at`. Best-effort AND after the materialise, so the
	//    stamp means "a session really did launch from this version", not "a
	//    launch was attempted". It does not extend the expiry (see
	//    MarkCustomImageResumed) — its whole job is to let an operator see that
	//    an image due for reaping is still in daily use.
	if err := c.store.MarkCustomImageResumed(ctx, project, ci.Name, ci.Version, 0); err != nil {
		c.logf("[images] %s: could not stamp last_resumed_at on %s:%d: %v", project, ci.Name, ci.Version, err)
	}
	return strings.TrimSpace(string(launch)), nil
}

// classifyResolveError marks the ONE failure that means "this string is not a
// reference to anything of mine" with agentkit.ErrImageRefNotInCatalogue.
//
// Two shapes qualify and no others:
//
//   - ErrCustomImageInvalid — the string is not even a §13.3 reference
//     (`agentkit-sandbox:dev` has a non-integer version; `ghcr.io/acme/x:1` is
//     not a §13 name). ParseImageRef answers this before any query runs, which
//     is why the standalone stack's literal image costs no database round trip;
//   - ErrCustomImageNotFound — a well-formed reference the catalogue has never
//     held (or held in a different project — P5's namespace is hard).
//
// ErrCustomImageReaped, ErrCustomImageUnmaterialisable and any database error
// are deliberately NOT marked: those mean the catalogue does know the name and
// cannot serve it, and the caller must fail rather than substitute (§13.3).
//
// The marking is additive — the original sentinel still matches errors.Is, so
// the worker-pointer path, which treats every failure identically, is unchanged.
func classifyResolveError(ref string, err error) error {
	if errors.Is(err, agentdb.ErrCustomImageInvalid) || errors.Is(err, agentdb.ErrCustomImageNotFound) {
		return fmt.Errorf("%w: %q: %w", agentkit.ErrImageRefNotInCatalogue, ref, err)
	}
	return err
}

// decodeRegistryHandle reverses what image_create stored (mcp_images.go marshals
// the imageregistry.Handle into the text column).
func decodeRegistryHandle(raw string) (imageregistry.Handle, error) {
	var h imageregistry.Handle
	if strings.TrimSpace(raw) == "" {
		return h, fmt.Errorf("the catalogue row carries no registry handle")
	}
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return h, fmt.Errorf("registry handle is not decodable: %w", err)
	}
	if strings.TrimSpace(h.Ref) == "" {
		return h, fmt.Errorf("registry handle names no ref")
	}
	return h, nil
}
