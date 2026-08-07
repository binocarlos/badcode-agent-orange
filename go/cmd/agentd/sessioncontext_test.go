package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/extension"
)

// fakeConfigStore is the projectConfigStore seam backed by maps: the §5
// precedence rules are policy, and policy is testable without a database.
type fakeConfigStore struct {
	settings map[string]*agentdb.ProjectSettings
	workers  map[string]*agentdb.Worker // key: project + "/" + name
	getErr   error
}

func (f *fakeConfigStore) GetProjectSettings(_ context.Context, project string) (*agentdb.ProjectSettings, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if ps, ok := f.settings[project]; ok {
		return ps, nil
	}
	// Mirrors B1: an unwritten project reads back the spec defaults.
	return agentdb.DefaultProjectSettings(project), nil
}

func (f *fakeConfigStore) GetWorker(_ context.Context, project, name string) (*agentdb.Worker, error) {
	if w, ok := f.workers[project+"/"+name]; ok {
		return w, nil
	}
	return nil, fmt.Errorf("%w: %s/%s", agentdb.ErrWorkerNotFound, project, name)
}

// mcpJSON expresses an mcp_config jsonb column the way the database hands it
// back: opaque JSON, not A1's typed struct.
func mcpJSON(servers map[string]any) agentdb.JSONMap { return agentdb.JSONMap(servers) }

func httpServer(url string, headers map[string]any) map[string]any {
	m := map[string]any{"url": url}
	if headers != nil {
		m["headers"] = headers
	}
	return m
}

// TestSessionContextProvider locks docs/product/01-session-config.md §5:
// worker beats project beats global for image and prompt, and MCP config is the
// project ∪ worker union (worker wins per key — never a filter of project tools).
func TestSessionContextProvider(t *testing.T) {
	const globalImage = "agentkit-sandbox:dev"

	tests := []struct {
		name       string
		store      *fakeConfigStore
		scope      extension.ContextScope
		wantImage  string
		wantPrompt string
		wantMCP    []string // sorted server names expected in the union
		wantErr    string   // substring; empty = expect success
	}{
		{
			// Nothing written for the project: the global defaults stand and no
			// session is blocked on missing configuration.
			name:      "unwritten project falls back to global image and empty prompt",
			store:     &fakeConfigStore{},
			scope:     extension.ContextScope{Customer: "acme"},
			wantImage: globalImage,
		},
		{
			// No tenancy in scope → no project layer to read at all.
			name:      "empty project short-circuits to global",
			store:     &fakeConfigStore{getErr: errors.New("must not be called")},
			scope:     extension.ContextScope{},
			wantImage: globalImage,
		},
		{
			name: "project settings override the global image and set the prompt",
			store: &fakeConfigStore{settings: map[string]*agentdb.ProjectSettings{
				"acme": {Project: "acme", BaseImage: "acme-base:v2", SystemPrompt: "You work for ACME."},
			}},
			scope:      extension.ContextScope{Customer: "acme"},
			wantImage:  "acme-base:v2",
			wantPrompt: "You work for ACME.",
		},
		{
			// §5: the project prompt is *prepended* to the worker's, and the
			// worker image wins the image chain.
			name: "worker beats project beats global",
			store: &fakeConfigStore{
				settings: map[string]*agentdb.ProjectSettings{
					"acme": {Project: "acme", BaseImage: "acme-base:v2", SystemPrompt: "You work for ACME."},
				},
				workers: map[string]*agentdb.Worker{
					"acme/marketing": {Project: "acme", Name: "marketing", Image: "acme-marketing:v7", SystemPrompt: "You write campaigns."},
				},
			},
			scope:      extension.ContextScope{Customer: "acme", Persona: "marketing"},
			wantImage:  "acme-marketing:v7",
			wantPrompt: "You work for ACME.\n\nYou write campaigns.",
		},
		{
			// A worker that sets no image inherits the project's, not the global.
			name: "worker without an image inherits the project image",
			store: &fakeConfigStore{
				settings: map[string]*agentdb.ProjectSettings{
					"acme": {Project: "acme", BaseImage: "acme-base:v2"},
				},
				workers: map[string]*agentdb.Worker{
					"acme/marketing": {Project: "acme", Name: "marketing", SystemPrompt: "You write campaigns."},
				},
			},
			scope:      extension.ContextScope{Customer: "acme", Persona: "marketing"},
			wantImage:  "acme-base:v2",
			wantPrompt: "You write campaigns.",
		},
		{
			// Personas predate the workers table: an unknown one is not an error.
			name: "unknown persona contributes no worker layer",
			store: &fakeConfigStore{settings: map[string]*agentdb.ProjectSettings{
				"acme": {Project: "acme", BaseImage: "acme-base:v2", SystemPrompt: "You work for ACME."},
			}},
			scope:      extension.ContextScope{Customer: "acme", Persona: "not-a-worker"},
			wantImage:  "acme-base:v2",
			wantPrompt: "You work for ACME.",
		},
		{
			// The load-bearing MCP rule: union, not replacement — the worker
			// cannot shed a project tool it did not ask for.
			name: "project MCP union worker MCP",
			store: &fakeConfigStore{
				settings: map[string]*agentdb.ProjectSettings{
					"acme": {Project: "acme", MCPConfig: mcpJSON(map[string]any{
						"gmail":  httpServer("https://gmail.example/mcp", map[string]any{"Authorization": "${GMAIL_TOKEN}"}),
						"notion": httpServer("https://notion.example/mcp", nil),
					})},
				},
				workers: map[string]*agentdb.Worker{
					"acme/marketing": {Project: "acme", Name: "marketing", MCPConfig: mcpJSON(map[string]any{
						"analytics": httpServer("https://ga.example/mcp", nil),
					})},
				},
			},
			scope:     extension.ContextScope{Customer: "acme", Persona: "marketing"},
			wantImage: globalImage,
			wantMCP:   []string{"analytics", "gmail", "notion"},
		},
		{
			name: "worker MCP overrides the project entry of the same name",
			store: &fakeConfigStore{
				settings: map[string]*agentdb.ProjectSettings{
					"acme": {Project: "acme", MCPConfig: mcpJSON(map[string]any{
						"gmail": httpServer("https://project.example/mcp", nil),
					})},
				},
				workers: map[string]*agentdb.Worker{
					"acme/marketing": {Project: "acme", Name: "marketing", MCPConfig: mcpJSON(map[string]any{
						"gmail": httpServer("https://worker.example/mcp", nil),
					})},
				},
			},
			scope:     extension.ContextScope{Customer: "acme", Persona: "marketing"},
			wantImage: globalImage,
			wantMCP:   []string{"gmail"},
		},
		{
			// §4.1: a partially interpolated credential must never reach the
			// sandbox as a literal — the resolution fails loudly instead.
			name: "invalid MCP config fails loudly",
			store: &fakeConfigStore{settings: map[string]*agentdb.ProjectSettings{
				"acme": {Project: "acme", MCPConfig: mcpJSON(map[string]any{
					"gmail": httpServer("https://gmail.example/mcp", map[string]any{"Authorization": "Bearer ${GMAIL_TOKEN}"}),
				})},
			}},
			scope:   extension.ContextScope{Customer: "acme", Persona: ""},
			wantErr: "whole-value",
		},
		{
			name: "undecodable MCP config is an error, not a silent drop",
			store: &fakeConfigStore{settings: map[string]*agentdb.ProjectSettings{
				"acme": {Project: "acme", MCPConfig: mcpJSON(map[string]any{"gmail": "not-an-object"})},
			}},
			scope:   extension.ContextScope{Customer: "acme"},
			wantErr: "mcp_config",
		},
		{
			name:    "store failure surfaces",
			store:   &fakeConfigStore{getErr: errors.New("db down")},
			scope:   extension.ContextScope{Customer: "acme"},
			wantErr: "db down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newSessionContextProvider(tt.store, globalImage)

			sc, err := p.Resolve(context.Background(), tt.scope)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got none (%+v)", tt.wantErr, sc)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if sc.BaseImage != tt.wantImage {
				t.Errorf("BaseImage = %q, want %q", sc.BaseImage, tt.wantImage)
			}
			if sc.SystemPrompt != tt.wantPrompt {
				t.Errorf("SystemPrompt = %q, want %q", sc.SystemPrompt, tt.wantPrompt)
			}

			servers, err := p.ResolveMCPServers(context.Background(), tt.scope)
			if err != nil {
				t.Fatalf("ResolveMCPServers: %v", err)
			}
			if len(servers) != len(tt.wantMCP) {
				t.Fatalf("MCP servers = %v, want %v", sortedKeys(servers), tt.wantMCP)
			}
			for _, name := range tt.wantMCP {
				if _, ok := servers[name]; !ok {
					t.Errorf("MCP server %q missing (got %v)", name, sortedKeys(servers))
				}
			}
		})
	}
}

// TestSessionContextProvider_WorkerWinsOnCollisionValue pins *which* config wins
// a name collision: the merge must take the worker's whole entry, not merge
// field-by-field into the project's.
func TestSessionContextProvider_WorkerWinsOnCollisionValue(t *testing.T) {
	store := &fakeConfigStore{
		settings: map[string]*agentdb.ProjectSettings{
			"acme": {Project: "acme", MCPConfig: mcpJSON(map[string]any{
				"gmail": httpServer("https://project.example/mcp", map[string]any{"Authorization": "${PROJECT_TOKEN}"}),
			})},
		},
		workers: map[string]*agentdb.Worker{
			"acme/marketing": {Project: "acme", Name: "marketing", MCPConfig: mcpJSON(map[string]any{
				"gmail": httpServer("https://worker.example/mcp", nil),
			})},
		},
	}
	p := newSessionContextProvider(store, "base:dev")
	servers, err := p.ResolveMCPServers(context.Background(),
		extension.ContextScope{Customer: "acme", Persona: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	got := servers["gmail"]
	if got.URL != "https://worker.example/mcp" {
		t.Errorf("URL = %q, want the worker's", got.URL)
	}
	if len(got.Headers) != 0 {
		t.Errorf("Headers = %v, want none — the project entry must be replaced wholesale, not merged", got.Headers)
	}
}

// TestSessionContextProvider_RequestMCPIsAdditive covers §5's "request-supplied
// values are additive for MCP": the session-create path lays the request over
// the resolved defaults, and the project's servers survive.
func TestSessionContextProvider_RequestMCPIsAdditive(t *testing.T) {
	defaults := agentdb.MCPServers{
		"gmail": {URL: "https://gmail.example/mcp"},
	}
	request := agentdb.MCPServers{
		"scratch": {Command: "/usr/local/bin/scratch-mcp"},
		"gmail":   {URL: "https://override.example/mcp"},
	}
	merged := MergeMCPServers(defaults, request)
	if len(merged) != 2 {
		t.Fatalf("merged = %v, want gmail + scratch", sortedKeys(merged))
	}
	if merged["gmail"].URL != "https://override.example/mcp" {
		t.Errorf("request entry did not win: %q", merged["gmail"].URL)
	}
	if merged["scratch"].Command == "" {
		t.Error("request-only server was dropped")
	}
	// Inputs are untouched — the defaults are reused across sessions.
	if defaults["gmail"].URL != "https://gmail.example/mcp" || len(defaults) != 1 {
		t.Errorf("MergeMCPServers mutated its input: %v", defaults)
	}
}

// TestSessionContextProvider_ResolveCarriesMCPServers pins the connection that
// was missing: Resolve must put the resolved project ∪ worker union ON the
// returned SessionContext, because that struct is the only thing the Runner
// reads. A2 added the field and the Runner's merge and B2 computed the union,
// but nothing filled it in between — so a project's tools resolved perfectly
// and then reached no container. Resolving them is not the feature; delivering
// them is.
func TestSessionContextProvider_ResolveCarriesMCPServers(t *testing.T) {
	store := &fakeConfigStore{
		settings: map[string]*agentdb.ProjectSettings{
			"acme": {Project: "acme", MCPConfig: mcpJSON(map[string]any{
				"gmail": httpServer("https://project.example/mcp", nil),
			})},
		},
		workers: map[string]*agentdb.Worker{
			"acme/marketing": {Project: "acme", Name: "marketing", MCPConfig: mcpJSON(map[string]any{
				"notion": httpServer("https://worker.example/mcp", nil),
			})},
		},
	}
	p := newSessionContextProvider(store, "base:dev")

	sc, err := p.Resolve(context.Background(),
		extension.ContextScope{Customer: "acme", Persona: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.MCPServers) != 2 {
		t.Fatalf("SessionContext.MCPServers = %v, want both the project and worker servers — "+
			"an unpopulated field means the project's tools never reach the container",
			sortedKeys(sc.MCPServers))
	}
	if sc.MCPServers["gmail"].URL != "https://project.example/mcp" {
		t.Errorf("project server lost: %+v", sc.MCPServers["gmail"])
	}
	if sc.MCPServers["notion"].URL != "https://worker.example/mcp" {
		t.Errorf("worker server lost: %+v", sc.MCPServers["notion"])
	}

	// And what Resolve carries must be exactly what ResolveMCPServers computes —
	// two code paths that can disagree are how this broke in the first place.
	direct, err := p.ResolveMCPServers(context.Background(),
		extension.ContextScope{Customer: "acme", Persona: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sc.MCPServers, direct) {
		t.Errorf("Resolve and ResolveMCPServers disagree:\n  Resolve = %v\n  direct  = %v",
			sortedKeys(sc.MCPServers), sortedKeys(direct))
	}
}

// TestSessionContextProvider_ImplementsSeam keeps the provider assignable to
// agentkit.Deps.SessionContext — the wiring main.go depends on.
func TestSessionContextProvider_ImplementsSeam(t *testing.T) {
	var _ extension.SessionContextProvider = newSessionContextProvider(&fakeConfigStore{}, "base:dev")
	// *agentdb.Store must satisfy the narrow read seam too (compile-time only).
	var _ projectConfigStore = (*agentdb.Store)(nil)
}

func sortedKeys(m agentdb.MCPServers) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
