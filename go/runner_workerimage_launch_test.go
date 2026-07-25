package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// This file reuses compose_test.go's stubImageResolver: composition and the
// launch chain resolve a worker pointer through the SAME seam, so pinning both
// against the same fake is the point, not a shortcut.

// errImageNotFound stands in for agentdb.ErrCustomImageNotFound &c. What
// matters here is that ANY resolver error becomes a launch failure — the
// sentinels themselves are pinned on the agentd side (imageresolver_test.go).
var errImageNotFound = errors.New("image not found")

// TestResolveLaunchImageWorkerPointer pins the §13.5/§13.6 launch chain:
//
//	explicit Image > worker image pointer > custom image id >
//	SessionContext.BaseImage > Policy.BaseImage
//
// and the asymmetry that matters — the worker pointer FAILS the launch where
// the legacy custom-image id falls back (§13.3: a worker that was pointed at an
// environment and quietly got a different one is the drift §13 exists to
// prevent).
func TestResolveLaunchImageWorkerPointer(t *testing.T) {
	const (
		project = "acme"
		global  = "base:dev"
	)
	resolved := map[string]string{
		"toolbox":   "sha256:toolbox-v2-materialised", // bare name → latest
		"toolbox:1": "sha256:toolbox-v1-materialised", // pinned
	}

	tests := []struct {
		name string
		// inputs
		explicitImage string
		customImageID string
		sctx          *extension.SessionContext
		resolver      *stubImageResolver
		noResolver    bool
		// expectations
		want        execenv.ImageRef
		wantErr     string // substring; empty = expect success
		wantResolve []string
	}{
		{
			// The floating pointer: `toolbox` is whatever the catalogue's newest
			// version is, resolved at LAUNCH time so curation can publish
			// improvements without touching a worker row (§13.3).
			name:        "worker pointer resolves and wins over the project and global images",
			sctx:        &extension.SessionContext{WorkerImage: "toolbox", BaseImage: "toolbox"},
			want:        "sha256:toolbox-v2-materialised",
			wantResolve: []string{"acme|toolbox"},
		},
		{
			// The pinned form still launches after a newer version was burned:
			// `toolbox:1` means 1, for ever (§13.3).
			name:        "a pinned version resolves exactly",
			sctx:        &extension.SessionContext{WorkerImage: "toolbox:1", BaseImage: "toolbox:1"},
			want:        "sha256:toolbox-v1-materialised",
			wantResolve: []string{"acme|toolbox:1"},
		},
		{
			// A worker JOB arrives with the pointer already resolved by
			// composition (§6.2 step 1), so the explicit image wins and nothing
			// is resolved twice. The same rule is what lets an e2e override the
			// image outright.
			name:          "explicit image wins and the pointer is not resolved again",
			explicitImage: "explicit:tag",
			sctx:          &extension.SessionContext{WorkerImage: "toolbox", BaseImage: "toolbox"},
			want:          "explicit:tag",
			wantResolve:   nil,
		},
		{
			// §13.3, the whole point of the item: NEVER a silent fallback.
			name:        "an unknown name fails the launch rather than falling back",
			sctx:        &extension.SessionContext{WorkerImage: "gone", BaseImage: "gone"},
			resolver:    &stubImageResolver{err: errImageNotFound},
			wantErr:     "image not found",
			wantResolve: []string{"acme|gone"},
		},
		{
			// A resolver that answers "" is as bad as one that errors: launching
			// on the base image would be exactly the substitution §13.3 forbids.
			name:        "a pointer that resolves to nothing fails the launch",
			sctx:        &extension.SessionContext{WorkerImage: "ghost", BaseImage: "ghost"},
			resolver:    &stubImageResolver{images: map[string]string{"ghost": ""}},
			wantErr:     "resolved to nothing",
			wantResolve: []string{"acme|ghost"},
		},
		{
			// A host wired with pointers but no resolver is misconfigured. It
			// must say so, not launch somewhere else.
			name:       "a pointer with no resolver wired is an error, not a fallback",
			sctx:       &extension.SessionContext{WorkerImage: "toolbox", BaseImage: "toolbox"},
			noResolver: true,
			wantErr:    "no Deps.Images resolver is wired",
		},
		{
			// B2's dead precedence, now live: the project's base_image reaches
			// the launch path instead of being computed and thrown away.
			name: "the project base image beats the global default",
			sctx: &extension.SessionContext{BaseImage: "acme-base:v2"},
			want: "acme-base:v2",
		},
		{
			// No provider at all — every host that wires none, unchanged.
			name: "no session context falls through to the global default",
			want: global,
		},
		{
			// The provider contributed nothing usable; the engine's own default
			// still starts the session.
			name: "an empty context base image falls through to the global default",
			sctx: &extension.SessionContext{},
			want: global,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _, _, _, _ := newTestRunner(t)
			r.deps.Policy.BaseImage = global
			resolver := tt.resolver
			if resolver == nil {
				resolver = &stubImageResolver{images: resolved}
			}
			if !tt.noResolver {
				r.deps.Images = resolver
			}

			got, err := r.resolveLaunchImage(context.Background(),
				tt.explicitImage, tt.customImageID, "a@acme.com", project, tt.sctx)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got image %q", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				// Every worker-pointer failure is the same sentinel, so a caller
				// can answer "this job cannot run" without string matching.
				if !errors.Is(err, ErrLaunchImageUnresolvable) {
					t.Errorf("error %q is not an ErrLaunchImageUnresolvable", err)
				}
				if got != "" {
					t.Errorf("a failed resolution must yield no image, got %q", got)
				}
			} else {
				if err != nil {
					t.Fatalf("resolveLaunchImage: %v", err)
				}
				if got != tt.want {
					t.Errorf("launch image = %q, want %q", got, tt.want)
				}
			}
			if len(resolver.calls) != len(tt.wantResolve) {
				t.Fatalf("resolver calls = %v, want %v", resolver.calls, tt.wantResolve)
			}
			for i, want := range tt.wantResolve {
				if resolver.calls[i] != want {
					t.Errorf("resolver call %d = %q, want %q", i, resolver.calls[i], want)
				}
			}
		})
	}
}

// TestResolveLaunchImageWorkerPointerOutranksCustomImageID pins the one
// ordering §13.6 changed for the legacy path: the §13 pointer sits ABOVE the
// custom-image id. The two fail in opposite ways on purpose — an unresolvable
// custom image still starts a session, an unresolvable worker pointer never
// does — so the order decides which contract applies, and §13.5 puts the worker
// pointer first.
func TestResolveLaunchImageWorkerPointerOutranksCustomImageID(t *testing.T) {
	r, _, reg, _, _, _ := newTestRunner(t)
	h, err := reg.Persist(context.Background(), execenv.ImageRef("mock-image:custom"), imageregistry.PersistOptions{SessionID: "x"})
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	r.deps.CustomImages = &fakeCustomImages{handle: h, ok: true}
	r.deps.Images = &stubImageResolver{images: map[string]string{"toolbox": "sha256:toolbox"}}

	got, err := r.resolveLaunchImage(context.Background(), "", "img-1", "a@acme.com", "acme",
		&extension.SessionContext{WorkerImage: "toolbox", BaseImage: "toolbox"})
	if err != nil {
		t.Fatalf("resolveLaunchImage: %v", err)
	}
	if got != execenv.ImageRef("sha256:toolbox") {
		t.Fatalf("launch image = %q, want the worker pointer's image", got)
	}
}
