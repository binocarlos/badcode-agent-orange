package agentkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// This file is the middle link of §13.5's chain — `worker.image > project
// base_image > global` — which I4 left literal.
//
// THE BUG IT CLOSES. `project_settings.base_image` was handed to the container
// runtime as a docker reference and nothing else, so writing a curated image
// name into it — which is exactly what §5 and §13.3 tell an operator to write —
// was accepted by the API, read back correctly, and then stopped EVERY session
// in the project from launching, with an error naming neither the setting nor
// the image.
//
// The fix resolves it through the same catalogue seam as the worker pointer,
// with one deliberate difference: a value the catalogue does not know is a
// literal registry reference and is used verbatim, because that is what this
// setting has always meant and what the standalone stack depends on.

// catalogueStub is a resolver with the REAL classification: names it does not
// hold are reported as ErrImageRefNotInCatalogue (what agentd's
// catalogueImageResolver does for ErrCustomImageNotFound / ErrCustomImageInvalid),
// while anything else is a plain, fatal error (reaped, unmaterialisable,
// database down). compose_test.go's stubImageResolver marks nothing, which is
// the right shape for the worker pointer — every failure there is fatal — but
// cannot express the distinction this file is about.
type catalogueStub struct {
	images map[string]string // ref → resolved launch image ("" = materialises to nothing)
	broken map[string]error  // ref → a fatal, catalogue-side failure
	calls  []string          // "project|ref", in order
}

func (s *catalogueStub) Resolve(_ context.Context, project, ref string) (string, error) {
	s.calls = append(s.calls, project+"|"+ref)
	if err, ok := s.broken[ref]; ok {
		return "", err
	}
	img, ok := s.images[ref]
	if !ok {
		return "", fmt.Errorf("%w: %q: no image of that name in project %s", ErrImageRefNotInCatalogue, ref, project)
	}
	return img, nil
}

func newCatalogueStub() *catalogueStub {
	return &catalogueStub{
		images: map[string]string{
			"toolbox":   "registry.local/acme/toolbox:3", // bare name → latest
			"toolbox:1": "registry.local/acme/toolbox:1", // pinned
			"ghost":     "",                              // in the catalogue, materialises to nothing
		},
		broken: map[string]error{
			"reaped:2": errors.New("custom image reaped: reaped:2 was reaped at 1750000000 by the snapshot_ttl_days reaper"),
		},
	}
}

// TestResolveLaunchImageProjectBaseImage pins the three behaviours the fix owes
// an operator, plus the two the standalone stack owes it.
func TestResolveLaunchImageProjectBaseImage(t *testing.T) {
	const (
		project = "acme"
		global  = "agentkit-sandbox:dev"
	)

	tests := []struct {
		name string
		// inputs
		sctx       *extension.SessionContext
		noResolver bool
		// expectations
		want        execenv.ImageRef
		wantErr     []string // substrings; empty = expect success
		wantLiteral bool     // the origin says "used verbatim"
		wantSetting string   // "" = no setting recorded
		wantResolve []string
	}{
		{
			// (1) THE BUG. A bare curated name floats to the catalogue's newest
			// version, resolved at launch — the same rule §13.3 gives the worker
			// pointer, now for the same string in the other column.
			name:        "a curated name resolves to the latest version",
			sctx:        projectBase("toolbox"),
			want:        "registry.local/acme/toolbox:3",
			wantSetting: projectBaseImageSetting,
			wantResolve: []string{"acme|toolbox"},
		},
		{
			// (1) pinned: `toolbox:1` means 1, for ever — the exact value the
			// red e2e writes and the exact one that used to be pulled literally.
			name:        "a pinned curated version resolves exactly",
			sctx:        projectBase("toolbox:1"),
			want:        "registry.local/acme/toolbox:1",
			wantSetting: projectBaseImageSetting,
			wantResolve: []string{"acme|toolbox:1"},
		},
		{
			// (2) THE COMPATIBILITY CONSTRAINT. `agentkit-sandbox:dev` is not a
			// §13 reference at all (a non-integer version), so in the real
			// resolver it never reaches the catalogue's tables — the standalone
			// stack pays no database round trip and behaves exactly as before.
			name:        "the standalone stack's literal image is used verbatim",
			sctx:        projectBase("agentkit-sandbox:dev"),
			want:        "agentkit-sandbox:dev",
			wantLiteral: true,
			wantSetting: projectBaseImageSetting,
			wantResolve: []string{"acme|agentkit-sandbox:dev"},
		},
		{
			// (2) A fully qualified registry reference: still verbatim. This is
			// the case write-time validation could never safely reject.
			name:        "a registry reference the catalogue does not know is used verbatim",
			sctx:        projectBase("ghcr.io/acme/base:v1"),
			want:        "ghcr.io/acme/base:v1",
			wantLiteral: true,
			wantSetting: projectBaseImageSetting,
			wantResolve: []string{"acme|ghcr.io/acme/base:v1"},
		},
		{
			// (3) DIAGNOSABLE. The catalogue knows the name and cannot serve it
			// — the one case that must NOT fall back, because launching
			// something else is §13.3's silent substitution. The error names
			// the setting and the value.
			name: "a reaped curated version fails the launch, naming the setting",
			sctx: projectBase("reaped:2"),
			wantErr: []string{
				"project_settings.base_image", `"reaped:2"`, `project "acme"`,
				"cannot be launched", "was reaped at",
			},
			wantResolve: []string{"acme|reaped:2"},
		},
		{
			// A resolver that answers "" is as bad as one that errors.
			name: "a curated name that resolves to nothing fails the launch",
			sctx: projectBase("ghost"),
			wantErr: []string{
				"project_settings.base_image", `"ghost"`, "resolved to nothing",
			},
			wantResolve: []string{"acme|ghost"},
		},
		{
			// Back-compat for every host with no image catalogue: the setting
			// keeps its original literal meaning and costs nothing.
			name:        "with no catalogue wired the setting stays literal",
			sctx:        projectBase("acme-base:v2"),
			noResolver:  true,
			want:        "acme-base:v2",
			wantLiteral: true,
			wantSetting: projectBaseImageSetting,
		},
		{
			// The GLOBAL default is not a project setting and is never resolved
			// — a host that happens to name its default the same as a curated
			// image must not have its sessions moved out from under it.
			name: "the global default is never sent to the catalogue",
			sctx: &extension.SessionContext{BaseImage: "toolbox"},
			want: "toolbox",
		},
		{
			// Belt and braces: the field is only honoured when it is the string
			// that WON the chain, so a host computing BaseImage some other way
			// gets verbatim behaviour rather than a surprise resolution.
			name: "a project pointer that did not win the chain is not resolved",
			sctx: &extension.SessionContext{BaseImage: "something-else:1", ProjectBaseImage: "toolbox"},
			want: "something-else:1",
		},
		{
			// §13.5's order is unchanged: the worker pointer still outranks the
			// project default.
			name:        "the worker pointer still outranks the project base image",
			sctx:        &extension.SessionContext{WorkerImage: "toolbox:1", BaseImage: "toolbox:1", ProjectBaseImage: "toolbox"},
			want:        "registry.local/acme/toolbox:1",
			wantSetting: "worker.image",
			wantResolve: []string{"acme|toolbox:1"},
		},
		{
			// …and it still fails LOUDLY where the project default falls back:
			// an unburned worker pointer must never quietly become the project
			// image (§13.3). Marking "not in the catalogue" must not have
			// softened the worker link.
			name:        "an unburned worker pointer still fails rather than falling back",
			sctx:        &extension.SessionContext{WorkerImage: "never-burned", BaseImage: "never-burned", ProjectBaseImage: "toolbox"},
			wantErr:     []string{"worker image", `"never-burned"`, "no image of that name"},
			wantResolve: []string{"acme|never-burned"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _, _, _, _ := newTestRunner(t)
			r.deps.Policy.BaseImage = global
			resolver := newCatalogueStub()
			if !tt.noResolver {
				r.deps.Images = resolver
			}

			got, origin, err := r.resolveLaunchImage(context.Background(),
				"", "", "a@acme.com", project, tt.sctx)

			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected an error, got image %q", got)
				}
				t.Logf("operator sees: resolve launch image: %v", err)
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not contain %q", err, want)
					}
				}
				// The same sentinel the worker pointer uses, so a caller can
				// answer "this session cannot start" without string matching.
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
				if origin.Setting != tt.wantSetting {
					t.Errorf("origin setting = %q, want %q", origin.Setting, tt.wantSetting)
				}
				if origin.Literal != tt.wantLiteral {
					t.Errorf("origin literal = %v, want %v", origin.Literal, tt.wantLiteral)
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

// TestProjectBaseImageFailureNamesTheSetting is requirement (3) at the level the
// operator meets it: a CreateSession that cannot pull the image.
//
// Before the fix the whole diagnosis was `ensure image present: Error response
// from daemon: ...` — true of every launch failure that has ever happened, and
// silent about the setting that caused it. That is the two-hour debug.
func TestProjectBaseImageFailureNamesTheSetting(t *testing.T) {
	const pullFailure = "Error response from daemon: pull access denied"

	tests := []struct {
		name      string
		sctx      *extension.SessionContext
		wantErr   []string
		wantNotIn string
	}{
		{
			// The literal branch: says the value is not a curated image AND
			// that it was therefore pulled as a docker reference — which is
			// usually the entire answer ("but I burned that image").
			name: "a literal base_image that cannot be pulled",
			sctx: projectBase("definitely-not-an-image:v9"),
			wantErr: []string{
				"project_settings.base_image", `"definitely-not-an-image:v9"`,
				`project "acme"`, "names no image in the §13 catalogue",
				"used as a literal registry reference", pullFailure,
			},
		},
		{
			// The catalogue branch: names the setting, the pointer the operator
			// wrote, AND the image it resolved to — three facts, one line.
			name: "a curated base_image whose resolved image cannot be pulled",
			sctx: projectBase("toolbox:1"),
			wantErr: []string{
				"project_settings.base_image", `"toolbox:1"`, `project "acme"`,
				"resolved through the §13 catalogue to", "registry.local/acme/toolbox:1",
				pullFailure,
			},
		},
		{
			// Nothing that is not configuration grows an explanation: a session
			// launching on the engine's own default fails exactly as before.
			name:      "the global default is not blamed on a setting",
			sctx:      nil,
			wantErr:   []string{pullFailure},
			wantNotIn: "project_settings.base_image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := imageregistry.NewMock()
			reg.Err = errors.New(pullFailure)
			store := agentkittest.NewMemStore()
			var provider extension.SessionContextProvider
			if tt.sctx != nil {
				provider = staticSessionContext{sc: tt.sctx}
			}
			runner, err := NewRunner(Deps{
				Env: execenv.NewMock(), Registry: reg, Store: store, Artifacts: artifacts.NewMock(),
				Claims:         agentkittest.StaticClaims{Token: "test-token"},
				Events:         events.NewPipeline(events.NewMockSink()),
				SessionContext: provider,
				Images:         newCatalogueStub(),
				Policy:         Policy{BaseImage: "agentkit-sandbox:test"},
			})
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})

			_, createErr := runner.CreateSession(context.Background(), CreateSessionRequest{
				SessionID: "s1", Customer: "acme",
			})
			if createErr == nil {
				t.Fatal("expected the launch to fail")
			}
			t.Logf("operator sees: %v", createErr)
			for _, want := range tt.wantErr {
				if !strings.Contains(createErr.Error(), want) {
					t.Errorf("launch error does not contain %q:\n  %v", want, createErr)
				}
			}
			if tt.wantNotIn != "" && strings.Contains(createErr.Error(), tt.wantNotIn) {
				t.Errorf("launch error must not mention %q:\n  %v", tt.wantNotIn, createErr)
			}
		})
	}
}

// projectBase builds the session context agentd's provider produces when only
// `project_settings.base_image` is set: the value wins the §5 chain AND is
// carried separately so the Runner can ask the catalogue about it.
func projectBase(image string) *extension.SessionContext {
	return &extension.SessionContext{BaseImage: image, ProjectBaseImage: image}
}
