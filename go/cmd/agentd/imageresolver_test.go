package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// --- fixtures ----------------------------------------------------------------

type resumeStamp struct {
	project string
	name    string
	version int
}

type fakeImageResolveStore struct {
	rows      map[string]*agentdb.CustomImage // "project|ref" → row
	resolveEr error
	resumes   []resumeStamp
	resumeEr  error
}

func (f *fakeImageResolveStore) ResolveCustomImage(_ context.Context, project, ref string) (*agentdb.CustomImage, error) {
	if f.resolveEr != nil {
		return nil, f.resolveEr
	}
	ci, ok := f.rows[project+"|"+ref]
	if !ok {
		return nil, fmt.Errorf("%w: no image named %q in project %s", agentdb.ErrCustomImageNotFound, ref, project)
	}
	return ci, nil
}

func (f *fakeImageResolveStore) MarkCustomImageResumed(_ context.Context, project, name string, version int, _ int64) error {
	f.resumes = append(f.resumes, resumeStamp{project: project, name: name, version: version})
	return f.resumeEr
}

type fakeMaterializer struct {
	refs map[string]execenv.ImageRef // handle.Ref → launch ref
	err  error
	last imageregistry.Handle
}

func (f *fakeMaterializer) Materialize(_ context.Context, h imageregistry.Handle) (execenv.ImageRef, error) {
	f.last = h
	if f.err != nil {
		return "", f.err
	}
	return f.refs[h.Ref], nil
}

func handleJSON(t *testing.T, ref string) string {
	t.Helper()
	b, err := json.Marshal(imageregistry.Handle{Kind: "blob-archive", Ref: ref})
	if err != nil {
		t.Fatalf("marshal handle: %v", err)
	}
	return string(b)
}

// --- tests -------------------------------------------------------------------

// TestCatalogueImageResolver covers the binding itself: I1's Resolve behind
// C2's seam, the registry materialisation, the §5 `last_resumed_at` stamp, and
// the rule that governs all of it — every failure propagates (§13.3), because
// launching a worker from an environment it was not pointed at is the drift §13
// exists to prevent.
func TestCatalogueImageResolver(t *testing.T) {
	const project = "acme"

	newStore := func() *fakeImageResolveStore {
		return &fakeImageResolveStore{rows: map[string]*agentdb.CustomImage{
			"acme|toolbox": {
				Customer: project, Name: "toolbox", Version: 2,
				RegistryHandle: handleJSON(t, "blob/toolbox-2.tar"),
			},
			"acme|toolbox:1": {
				Customer: project, Name: "toolbox", Version: 1,
				RegistryHandle: handleJSON(t, "blob/toolbox-1.tar"),
			},
		}}
	}
	newRegistry := func() *fakeMaterializer {
		return &fakeMaterializer{refs: map[string]execenv.ImageRef{
			"blob/toolbox-2.tar": "agentkit-snapshot:toolbox-2",
			"blob/toolbox-1.tar": "agentkit-snapshot:toolbox-1",
		}}
	}

	t.Run("a floating name resolves to the newest version and stamps last_resumed_at", func(t *testing.T) {
		store, registry := newStore(), newRegistry()
		r := newCatalogueImageResolver(store, registry, func(string, ...any) {})

		got, err := r.Resolve(context.Background(), project, "toolbox")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != "agentkit-snapshot:toolbox-2" {
			t.Errorf("launch image = %q, want the newest version's", got)
		}
		// B4's finding: nothing called MarkCustomImageResumed, so
		// `last_resumed_at` stayed permanently 0. This is the caller.
		want := []resumeStamp{{project: project, name: "toolbox", version: 2}}
		if len(store.resumes) != 1 || store.resumes[0] != want[0] {
			t.Fatalf("resume stamps = %+v, want %+v", store.resumes, want)
		}
	})

	t.Run("a pinned reference resolves exactly and stamps that version", func(t *testing.T) {
		store, registry := newStore(), newRegistry()
		r := newCatalogueImageResolver(store, registry, func(string, ...any) {})

		got, err := r.Resolve(context.Background(), project, "toolbox:1")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != "agentkit-snapshot:toolbox-1" {
			t.Errorf("launch image = %q, want the pinned version's", got)
		}
		if len(store.resumes) != 1 || store.resumes[0].version != 1 {
			t.Fatalf("resume stamps = %+v, want version 1", store.resumes)
		}
	})

	// Each of I1's three sentinels must reach the caller intact, so the
	// dispatcher can say WHY a job could not run — and so nothing anywhere is
	// tempted to treat one of them as "use the project default instead".
	for _, tt := range []struct {
		name     string
		storeErr error
		regErr   error
		row      *agentdb.CustomImage
		wantIs   error
	}{
		{
			name:     "unknown name",
			storeErr: fmt.Errorf("%w: no image named %q", agentdb.ErrCustomImageNotFound, "gone"),
			wantIs:   agentdb.ErrCustomImageNotFound,
		},
		{
			name:     "reaped version",
			storeErr: fmt.Errorf("%w: toolbox:1 was reaped", agentdb.ErrCustomImageReaped),
			wantIs:   agentdb.ErrCustomImageReaped,
		},
		{
			name:     "unmaterialisable version",
			storeErr: fmt.Errorf("%w: toolbox:1 has no registry handle", agentdb.ErrCustomImageUnmaterialisable),
			wantIs:   agentdb.ErrCustomImageUnmaterialisable,
		},
		{
			name:   "the registry cannot produce the bytes",
			regErr: errors.New("blob 404"),
			wantIs: agentdb.ErrCustomImageUnmaterialisable,
		},
	} {
		t.Run(tt.name+" fails the resolution", func(t *testing.T) {
			store, registry := newStore(), newRegistry()
			store.resolveEr = tt.storeErr
			registry.err = tt.regErr
			r := newCatalogueImageResolver(store, registry, func(string, ...any) {})

			got, err := r.Resolve(context.Background(), project, "toolbox")
			if err == nil {
				t.Fatalf("expected a failure, got image %q", got)
			}
			if !errors.Is(err, tt.wantIs) {
				t.Fatalf("error %q does not wrap %v", err, tt.wantIs)
			}
			if got != "" {
				t.Errorf("a failed resolution must yield no image, got %q", got)
			}
			if len(store.resumes) != 0 {
				t.Errorf("a failed resolution must not stamp last_resumed_at, got %+v", store.resumes)
			}
		})
	}

	t.Run("a corrupt registry handle reads as unmaterialisable", func(t *testing.T) {
		store, registry := newStore(), newRegistry()
		store.rows["acme|toolbox"].RegistryHandle = "{not json"
		r := newCatalogueImageResolver(store, registry, func(string, ...any) {})

		if _, err := r.Resolve(context.Background(), project, "toolbox"); !errors.Is(err, agentdb.ErrCustomImageUnmaterialisable) {
			t.Fatalf("error = %v, want ErrCustomImageUnmaterialisable", err)
		}
	})

	t.Run("a failed stamp never fails the launch", func(t *testing.T) {
		store, registry := newStore(), newRegistry()
		store.resumeEr = errors.New("db down")
		var logged []string
		r := newCatalogueImageResolver(store, registry, func(f string, v ...any) {
			logged = append(logged, fmt.Sprintf(f, v...))
		})

		got, err := r.Resolve(context.Background(), project, "toolbox")
		if err != nil {
			t.Fatalf("a launch must survive a failed telemetry stamp: %v", err)
		}
		if got != "agentkit-snapshot:toolbox-2" {
			t.Errorf("launch image = %q", got)
		}
		if len(logged) != 1 || !strings.Contains(logged[0], "last_resumed_at") {
			t.Fatalf("expected the failed stamp to be logged, got %v", logged)
		}
	})

	t.Run("the project is passed through, never inferred", func(t *testing.T) {
		store, registry := newStore(), newRegistry()
		r := newCatalogueImageResolver(store, registry, func(string, ...any) {})

		// The same name in another project does not resolve: P5's hard namespace.
		if _, err := r.Resolve(context.Background(), "globex", "toolbox"); !errors.Is(err, agentdb.ErrCustomImageNotFound) {
			t.Fatalf("error = %v, want ErrCustomImageNotFound", err)
		}
	})
}

// TestSessionContextCarriesTheWorkerPointerUnresolved pins the OTHER half of
// the binding: the provider hands the Runner the worker's §13 pointer as a
// pointer, distinguishable from a plain image ref, so the launch chain knows it
// must resolve it (and knows to fail if it cannot) rather than guessing from
// the string. A project or global image sets no pointer — nothing to resolve.
func TestSessionContextCarriesTheWorkerPointerUnresolved(t *testing.T) {
	const globalImage = "agentkit-sandbox:dev"
	store := &fakeConfigStore{
		settings: map[string]*agentdb.ProjectSettings{
			"acme": {Project: "acme", BaseImage: "acme-base:v2"},
		},
		workers: map[string]*agentdb.Worker{
			"acme/curated": {Project: "acme", Name: "curated", Image: "toolbox"},
			"acme/pinned":  {Project: "acme", Name: "pinned", Image: "toolbox:1"},
			"acme/plain":   {Project: "acme", Name: "plain"},
		},
	}
	p := newSessionContextProvider(store, globalImage)

	for _, tt := range []struct {
		persona     string
		wantImage   string
		wantPointer string
	}{
		// The pointer travels UNRESOLVED — `toolbox` is not an image ref, and
		// turning it into one is the catalogue's job, once, at launch.
		{persona: "curated", wantImage: "toolbox", wantPointer: "toolbox"},
		{persona: "pinned", wantImage: "toolbox:1", wantPointer: "toolbox:1"},
		// No pointer: BaseImage is a real ref and the Runner uses it verbatim.
		{persona: "plain", wantImage: "acme-base:v2", wantPointer: ""},
		{persona: "", wantImage: "acme-base:v2", wantPointer: ""},
	} {
		t.Run("persona="+tt.persona, func(t *testing.T) {
			sc, err := p.Resolve(context.Background(), extension.ContextScope{Customer: "acme", Persona: tt.persona})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if sc.BaseImage != tt.wantImage {
				t.Errorf("BaseImage = %q, want %q", sc.BaseImage, tt.wantImage)
			}
			if sc.WorkerImage != tt.wantPointer {
				t.Errorf("WorkerImage = %q, want %q", sc.WorkerImage, tt.wantPointer)
			}
		})
	}
}

// TestCatalogueImageResolverSatisfiesTheSeam is a compile-time pin: the type
// C2 designed the seam for (agentkit.ImageResolver) is the type both the
// dispatcher and the Runner are handed, so composition and the launch chain
// cannot drift onto two different resolvers.
func TestCatalogueImageResolverSatisfiesTheSeam(t *testing.T) {
	var _ agentkit.ImageResolver = (*catalogueImageResolver)(nil)
	var store imageResolveStore = &fakeImageResolveStore{}
	var registry imageMaterializer = &fakeMaterializer{}
	if r := newCatalogueImageResolver(store, registry, nil); r.logf == nil {
		t.Fatal("a nil logger must default to log.Printf, never stay nil")
	}
	// *agentdb.Store is the production implementation of the store seam.
	var _ imageResolveStore = (*agentdb.Store)(nil)
}
