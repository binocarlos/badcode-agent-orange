package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fakeProjectSettingsStore is a project-keyed in-memory stand-in for
// *agentdb.Store (whose real migrations need Postgres). It mirrors the store
// contract the handlers rely on: unknown project → defaults, whole-object write.
type fakeProjectSettingsStore struct {
	rows    map[string]*agentdb.ProjectSettings
	getErr  error
	putErr  error
	lastGet string
	lastPut *agentdb.ProjectSettings
	// The config-log actor the handler passed down, and how many writes it made.
	lastWrite agentdb.ConfigWrite
	puts      int
}

func newFakeProjectSettings() *fakeProjectSettingsStore {
	return &fakeProjectSettingsStore{rows: map[string]*agentdb.ProjectSettings{}}
}

func (f *fakeProjectSettingsStore) GetProjectSettings(_ context.Context, project string) (*agentdb.ProjectSettings, error) {
	f.lastGet = project
	if f.getErr != nil {
		return nil, f.getErr
	}
	if ps, ok := f.rows[project]; ok {
		return ps, nil
	}
	return agentdb.DefaultProjectSettings(project), nil
}

func (f *fakeProjectSettingsStore) PutProjectSettings(_ context.Context, ps *agentdb.ProjectSettings, cw agentdb.ConfigWrite) (*agentdb.ProjectSettings, error) {
	f.lastPut = ps
	f.lastWrite = cw
	f.puts++
	if f.putErr != nil {
		return nil, f.putErr
	}
	stored := *ps
	stored.UpdatedAt = 1700000000
	f.rows[ps.Project] = &stored
	return &stored, nil
}

// identityFor builds an IdentityFunc pinned to one project (the customer claim).
func identityFor(customer string) IdentityFunc {
	return func(*http.Request) (Identity, error) {
		return Identity{UserEmail: "u@" + customer + ".com", Customer: customer}, nil
	}
}

func newProjectSettingsHandlers(t *testing.T, store ProjectSettingsStore, identity IdentityFunc) *Handlers {
	t.Helper()
	return newHandlers(t, Config{
		Runner:          stubRunner{},
		Store:           stubStore{},
		Identity:        identity,
		ProjectSettings: store,
	})
}

func doProjectSettings(h *Handlers, method, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/agent/project-settings", nil)
	} else {
		r = httptest.NewRequest(method, "/agent/project-settings", strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, r)
	return rec
}

func decodeProjectSettings(t *testing.T, rec *httptest.ResponseRecorder) *agentdb.ProjectSettings {
	t.Helper()
	var out agentdb.ProjectSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %s: %v", rec.Body, err)
	}
	return &out
}

// GET on a project nobody has configured returns the §5 defaults over the wire.
func TestProjectSettingsGetReturnsDefaults(t *testing.T) {
	store := newFakeProjectSettings()
	h := newProjectSettingsHandlers(t, store, identityFor("acme"))

	rec := doProjectSettings(h, "GET", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	got := decodeProjectSettings(t, rec)
	if got.Project != "acme" {
		t.Fatalf("project must come from the JWT, got %q", got.Project)
	}
	if got.MaxConcurrentJobs != 4 || got.BriefingMaxBytes != 2048 || got.SnapshotTTLDays != 30 {
		t.Fatalf("defaults: want 4/2048/30, got %d/%d/%d",
			got.MaxConcurrentJobs, got.BriefingMaxBytes, got.SnapshotTTLDays)
	}
	if store.lastGet != "acme" {
		t.Fatalf("store queried for %q, want acme", store.lastGet)
	}
}

// PUT round-trips the whole object, including the four §5 budget/cap columns.
func TestProjectSettingsPutRoundTrip(t *testing.T) {
	store := newFakeProjectSettings()
	h := newProjectSettingsHandlers(t, store, identityFor("acme"))

	body := `{
		"base_image": "acme/base:v2",
		"system_prompt": "be excellent",
		"mcp_config": {"gmail": {"url": "http://gmail-mcp:9000"}},
		"attention_channel": {"kind": "webhook", "url": "https://hooks.example/x"},
		"max_concurrent_jobs": 6,
		"daily_tokens_soft": 1000,
		"daily_tokens_hard": 2000,
		"briefing_max_bytes": 4096,
		"snapshot_ttl_days": 0
	}`
	rec := doProjectSettings(h, "PUT", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	got := decodeProjectSettings(t, rec)
	if got.BaseImage != "acme/base:v2" || got.SystemPrompt != "be excellent" {
		t.Fatalf("image/prompt: %+v", got)
	}
	if got.MaxConcurrentJobs != 6 || got.DailyTokensSoft != 1000 || got.DailyTokensHard != 2000 ||
		got.BriefingMaxBytes != 4096 || got.SnapshotTTLDays != 0 {
		t.Fatalf("budget/cap columns: %+v", got)
	}
	if got.MCPConfig["gmail"] == nil || got.AttentionChannel["kind"] != "webhook" {
		t.Fatalf("json columns: %+v", got)
	}

	// And it is readable back through GET.
	rec = doProjectSettings(h, "GET", "")
	if back := decodeProjectSettings(t, rec); back.SystemPrompt != "be excellent" {
		t.Fatalf("GET after PUT: %+v", back)
	}
}

// The caller may not choose its own project: a body-supplied `project` (or an
// updated_at) is overwritten by the JWT-derived scope before the store sees it.
func TestProjectSettingsPutIgnoresBodyProject(t *testing.T) {
	store := newFakeProjectSettings()
	h := newProjectSettingsHandlers(t, store, identityFor("acme"))

	rec := doProjectSettings(h, "PUT", `{"project":"victim","updated_at":99,"system_prompt":"mine"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if store.lastPut.Project != "acme" {
		t.Fatalf("store must be written under the JWT project, got %q", store.lastPut.Project)
	}
	if store.lastPut.UpdatedAt != 0 {
		t.Fatalf("caller-supplied updated_at must be dropped, got %d", store.lastPut.UpdatedAt)
	}
	if _, leaked := store.rows["victim"]; leaked {
		t.Fatalf("body project created a row for another project: %+v", store.rows)
	}
}

// §12 project isolation: a token scoped to project X can neither read nor write
// project Y's settings — not via the body, and not by reading someone else's row.
func TestProjectSettingsProjectIsolation(t *testing.T) {
	store := newFakeProjectSettings()
	alpha := newProjectSettingsHandlers(t, store, identityFor("alpha"))
	beta := newProjectSettingsHandlers(t, store, identityFor("beta"))

	if rec := doProjectSettings(alpha, "PUT", `{"system_prompt":"alpha secrets","base_image":"alpha/base:v1"}`); rec.Code != http.StatusOK {
		t.Fatalf("alpha put: status=%d body=%s", rec.Code, rec.Body)
	}

	// Beta reads its own (default) settings — never alpha's.
	rec := doProjectSettings(beta, "GET", "")
	got := decodeProjectSettings(t, rec)
	if got.Project != "beta" {
		t.Fatalf("beta got project %q", got.Project)
	}
	if got.SystemPrompt != "" || got.BaseImage != "" {
		t.Fatalf("alpha's settings leaked to beta: %+v", got)
	}

	// Beta writing while naming alpha lands on beta's row; alpha is untouched.
	if rec := doProjectSettings(beta, "PUT", `{"project":"alpha","system_prompt":"beta wrote this"}`); rec.Code != http.StatusOK {
		t.Fatalf("beta put: status=%d body=%s", rec.Code, rec.Body)
	}
	if store.rows["alpha"].SystemPrompt != "alpha secrets" {
		t.Fatalf("beta's write reached alpha's row: %+v", store.rows["alpha"])
	}
	if store.rows["beta"].SystemPrompt != "beta wrote this" {
		t.Fatalf("beta's own row: %+v", store.rows["beta"])
	}

	// And alpha still reads what alpha wrote.
	if back := decodeProjectSettings(t, doProjectSettings(alpha, "GET", "")); back.SystemPrompt != "alpha secrets" {
		t.Fatalf("alpha read back: %+v", back)
	}
}

func TestProjectSettingsErrorPaths(t *testing.T) {
	invalid := fmt.Errorf("%w: briefing_max_bytes must not be negative", agentdb.ErrInvalidProjectSettings)

	tests := []struct {
		name     string
		method   string
		body     string
		store    ProjectSettingsStore
		identity IdentityFunc
		want     int
	}{
		{
			name: "unauthenticated GET is 401", method: "GET",
			store:    newFakeProjectSettings(),
			identity: func(*http.Request) (Identity, error) { return Identity{}, errors.New("no token") },
			want:     http.StatusUnauthorized,
		},
		{
			name: "unauthenticated PUT is 401", method: "PUT", body: `{}`,
			store:    newFakeProjectSettings(),
			identity: func(*http.Request) (Identity, error) { return Identity{}, errors.New("no token") },
			want:     http.StatusUnauthorized,
		},
		{
			name: "no store configured is 501", method: "GET",
			store: nil, identity: identityFor("acme"), want: http.StatusNotImplemented,
		},
		{
			name: "identity without a project is 400", method: "GET",
			store:    newFakeProjectSettings(),
			identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "u@x.com"}, nil },
			want:     http.StatusBadRequest,
		},
		{
			name: "malformed body is 400", method: "PUT", body: `{not json`,
			store: newFakeProjectSettings(), identity: identityFor("acme"), want: http.StatusBadRequest,
		},
		{
			name: "store validation error is 400", method: "PUT", body: `{"briefing_max_bytes":-1}`,
			store:    &fakeProjectSettingsStore{rows: map[string]*agentdb.ProjectSettings{}, putErr: invalid},
			identity: identityFor("acme"), want: http.StatusBadRequest,
		},
		{
			name: "store failure is 500", method: "GET",
			store:    &fakeProjectSettingsStore{rows: map[string]*agentdb.ProjectSettings{}, getErr: errors.New("db down")},
			identity: identityFor("acme"), want: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newProjectSettingsHandlers(t, tc.store, tc.identity)
			rec := doProjectSettings(h, tc.method, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

// New() adopts AgentDB as the project-settings store so agentd gets the routes
// for free; with neither set the routes answer 501 rather than panicking.
func TestProjectSettingsStoreDefaultsToAgentDB(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity,
	})
	if h.cfg.ProjectSettings != nil {
		t.Fatalf("expected no project settings store when AgentDB is nil")
	}
	if rec := doProjectSettings(h, "GET", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501 without a store, got %d", rec.Code)
	}

	var db *agentdb.Store // typed nil must not become a non-nil interface
	h = newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity, AgentDB: db,
	})
	if h.cfg.ProjectSettings != nil {
		t.Fatalf("a nil *agentdb.Store must not be adopted as the store")
	}
}

// The routes are mounted on the canonical paths (GET + PUT, same URL).
func TestProjectSettingsRoutesAreMounted(t *testing.T) {
	if DefaultEndpoints.GetProjectSettings != "GET /agent/project-settings" ||
		DefaultEndpoints.PutProjectSettings != "PUT /agent/project-settings" {
		t.Fatalf("unexpected default routes: %q / %q",
			DefaultEndpoints.GetProjectSettings, DefaultEndpoints.PutProjectSettings)
	}
	h := newProjectSettingsHandlers(t, newFakeProjectSettings(), identityFor("acme"))
	// A method with no registered handler on that path is 405, proving the
	// pattern (not a catch-all) is what matched GET/PUT above.
	rec := doProjectSettings(h, "DELETE", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: want 405, got %d body=%s", rec.Code, rec.Body)
	}
}
