package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
)

// The agentd half of "a §13 pointer in project_settings.base_image".
//
// The Runner decides WHAT to do with an unresolvable reference; this file pins
// the one thing agentd must tell it — whether the string named a catalogue
// image at all. Get that classification wrong in either direction and you get
// one of two bugs back: mark too much and a reaped image silently becomes a
// docker pull of its own name; mark too little and `agentkit-sandbox:dev` stops
// the standalone stack dead, which is the bug this whole change is about.

// TestClassifyResolveErrorMarksOnlyNonMembership is the classification itself.
func TestClassifyResolveErrorMarksOnlyNonMembership(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantMark bool
		// The original sentinel must survive the wrapping: the worker-pointer
		// path and the dispatcher both still match on it.
		wantKeeps error
	}{
		{
			name:      "a well-formed reference the catalogue never held",
			err:       fmt.Errorf("%w: no image named %q in project acme", agentdb.ErrCustomImageNotFound, "toolbox"),
			wantMark:  true,
			wantKeeps: agentdb.ErrCustomImageNotFound,
		},
		{
			name:      "a string that is not a §13 reference at all",
			err:       fmt.Errorf("%w: version %q is not an integer", agentdb.ErrCustomImageInvalid, "dev"),
			wantMark:  true,
			wantKeeps: agentdb.ErrCustomImageInvalid,
		},
		{
			// The catalogue KNOWS this name and cannot serve it. Falling back to
			// pulling "toolbox:1" from a registry would be §13.3's silent
			// substitution wearing a different hat.
			name:      "a tombstoned version is not a membership failure",
			err:       fmt.Errorf("%w: toolbox:1 was reaped at 1750000000", agentdb.ErrCustomImageReaped),
			wantMark:  false,
			wantKeeps: agentdb.ErrCustomImageReaped,
		},
		{
			name:      "a row with nothing to materialise is not a membership failure",
			err:       fmt.Errorf("%w: toolbox:1 has no registry handle", agentdb.ErrCustomImageUnmaterialisable),
			wantMark:  false,
			wantKeeps: agentdb.ErrCustomImageUnmaterialisable,
		},
		{
			// A database that will not answer says nothing about membership. If
			// this were marked, a Postgres blip would silently move every
			// project whose base_image is curated onto a nonexistent registry
			// reference — failing anyway, but with the wrong explanation.
			name:     "a database failure is not a membership failure",
			err:      errors.New("agentdb: resolve image \"toolbox\": connection refused"),
			wantMark: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyResolveError("toolbox", tt.err)
			if isMark := errors.Is(got, agentkit.ErrImageRefNotInCatalogue); isMark != tt.wantMark {
				t.Errorf("marked as not-in-catalogue = %v, want %v (error: %v)", isMark, tt.wantMark, got)
			}
			if tt.wantKeeps != nil && !errors.Is(got, tt.wantKeeps) {
				t.Errorf("the original sentinel %v did not survive: %v", tt.wantKeeps, got)
			}
		})
	}
}

// TestLiteralRegistryReferencesAreNotCatalogueRefs runs the REAL parser over the
// values operators and the stack actually write, because the compatibility
// constraint lives or dies on this list. Every one of them must classify as
// "not a catalogue image" so the Runner uses it verbatim — and, since
// ParseImageRef answers before any query runs, none of them costs a database
// round trip either.
func TestLiteralRegistryReferencesAreNotCatalogueRefs(t *testing.T) {
	for _, ref := range []string{
		"agentkit-sandbox:dev",    // the standalone stack's own image
		"agentkit-sandbox:latest", // any non-numeric tag
		"ghcr.io/acme/base:v1",    // a fully qualified reference
		"europe-west1-docker.pkg.dev/webkit-servers/agent-orange/core:2026-07", // the live GCP one
		"acme/base",                      // a repository with an owner
		"ubuntu@sha256:0123456789abcdef", // a digest reference
		"REGISTRY.example.com/Base:1",    // uppercase is not a §13 name
	} {
		t.Run(ref, func(t *testing.T) {
			_, err := agentdb.ParseImageRef(ref)
			if err == nil {
				t.Fatalf("%q parsed as a §13 reference — it is a registry reference and must not", ref)
			}
			if !errors.Is(classifyResolveError(ref, err), agentkit.ErrImageRefNotInCatalogue) {
				t.Fatalf("%q must classify as not-in-catalogue so it is used verbatim; got %v", ref, err)
			}
		})
	}
}

// TestCatalogueImageResolverMarksUnknownNames is the same classification, but
// through the resolver an operator's session actually calls.
func TestCatalogueImageResolverMarksUnknownNames(t *testing.T) {
	store := &fakeImageResolveStore{rows: map[string]*agentdb.CustomImage{
		"acme|toolbox": {
			Customer: "acme", Name: "toolbox", Version: 2,
			RegistryHandle: handleJSON(t, "blob/toolbox-2.tar"),
		},
	}}
	registry := &fakeMaterializer{refs: map[string]execenv.ImageRef{
		"blob/toolbox-2.tar": "agentkit-snapshot:toolbox-2",
	}}
	r := newCatalogueImageResolver(store, registry, func(string, ...any) {})

	if _, err := r.Resolve(context.Background(), "acme", "never-burned"); !errors.Is(err, agentkit.ErrImageRefNotInCatalogue) {
		t.Fatalf("an unknown name must be reported as not-in-catalogue, got %v", err)
	}
	// …and a name it DOES hold is not, even when the bytes are gone.
	store.resolveEr = fmt.Errorf("%w: toolbox:2 was reaped", agentdb.ErrCustomImageReaped)
	if _, err := r.Resolve(context.Background(), "acme", "toolbox"); errors.Is(err, agentkit.ErrImageRefNotInCatalogue) {
		t.Fatalf("a reaped version must NOT be reported as not-in-catalogue: %v", err)
	}
}

// TestSessionContextCarriesTheProjectBaseImageUnresolved is the other half of
// the wiring: `project_settings.base_image` reaches the Runner both as the
// winner of §5's chain AND as a separately labelled pointer, which is the only
// thing that lets the launch path ask the catalogue about it.
//
// Before this, the value travelled on BaseImage alone, indistinguishable from a
// docker reference — so a curated name was pulled as one and every session in
// the project failed to start.
func TestSessionContextCarriesTheProjectBaseImageUnresolved(t *testing.T) {
	const globalImage = "agentkit-sandbox:dev"
	store := &fakeConfigStore{
		settings: map[string]*agentdb.ProjectSettings{
			"acme":   {Project: "acme", BaseImage: "toolbox:1"},
			"globex": {Project: "globex"}, // no base_image at all
		},
		workers: map[string]*agentdb.Worker{
			"acme/curated": {Project: "acme", Name: "curated", Image: "other-toolbox"},
			"acme/plain":   {Project: "acme", Name: "plain"},
		},
	}
	p := newSessionContextProvider(store, globalImage)

	for _, tt := range []struct {
		name        string
		project     string
		persona     string
		wantImage   string
		wantProject string
		wantWorker  string
	}{
		{
			name:    "the project's base_image wins and is labelled a pointer",
			project: "acme", persona: "",
			wantImage: "toolbox:1", wantProject: "toolbox:1",
		},
		{
			name:    "a worker with no pointer still launches on the project default",
			project: "acme", persona: "plain",
			wantImage: "toolbox:1", wantProject: "toolbox:1",
		},
		{
			// The worker pointer wins the chain, so the project label is never
			// consulted — but it is still reported honestly, because it IS what
			// the project setting holds.
			name:    "a worker pointer outranks it",
			project: "acme", persona: "curated",
			wantImage: "other-toolbox", wantProject: "toolbox:1", wantWorker: "other-toolbox",
		},
		{
			// The GLOBAL default is not a project setting: it must never be
			// labelled, or a host whose default happens to share a name with a
			// curated image would have its sessions moved.
			name:    "an unset base_image labels nothing",
			project: "globex", persona: "",
			wantImage: globalImage, wantProject: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sc, err := p.Resolve(context.Background(), extension.ContextScope{
				Customer: tt.project, Persona: tt.persona,
			})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if sc.BaseImage != tt.wantImage {
				t.Errorf("BaseImage = %q, want %q", sc.BaseImage, tt.wantImage)
			}
			if sc.ProjectBaseImage != tt.wantProject {
				t.Errorf("ProjectBaseImage = %q, want %q", sc.ProjectBaseImage, tt.wantProject)
			}
			if sc.WorkerImage != tt.wantWorker {
				t.Errorf("WorkerImage = %q, want %q", sc.WorkerImage, tt.wantWorker)
			}
		})
	}
}
