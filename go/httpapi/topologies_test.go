package httpapi

// topologies_test.go — the T2 routes against a fake TopologyStore. The fake
// records every ApplyTopology call, which is what pins the two hard
// properties at this layer: preview writes NOTHING, and a refusal (409)
// reaches the store not at all when the preview already shows the conflict.
// The store-side halves (atomicity, the in-transaction re-check) are pinned in
// agentdb/topology_apply_test.go.
//
// The tests drive the real registered solo@v1 topology: the registry is
// code-defined (D1), so the catalogue IS the fixture.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

type fakeTopologyStore struct {
	workers []*agentdb.Worker
	images  map[string]bool
	skills  map[string]bool

	applies   int
	lastApply agentdb.TopologyApplication
	lastWrite agentdb.ConfigWrite
	applyErr  error
}

func newFakeTopologyStore() *fakeTopologyStore {
	return &fakeTopologyStore{images: map[string]bool{}, skills: map[string]bool{}}
}

func (f *fakeTopologyStore) ListWorkers(_ context.Context, project string) ([]*agentdb.Worker, error) {
	out := []*agentdb.Worker{}
	for _, w := range f.workers {
		if w.Project == project {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeTopologyStore) GetProjectSettings(_ context.Context, project string) (*agentdb.ProjectSettings, error) {
	return agentdb.DefaultProjectSettings(project), nil
}

func (f *fakeTopologyStore) ResolveCustomImage(_ context.Context, project, ref string) (*agentdb.CustomImage, error) {
	if f.images[project+"/"+ref] {
		return &agentdb.CustomImage{Name: ref, Customer: project}, nil
	}
	return nil, fmt.Errorf("%w: %s", agentdb.ErrCustomImageNotFound, ref)
}

func (f *fakeTopologyStore) GetProjectSkill(_ context.Context, project, name string) (*agentdb.Skill, error) {
	if f.skills[project+"/"+name] {
		return &agentdb.Skill{Name: name, Customer: project}, nil
	}
	return nil, fmt.Errorf("%w: %s", agentdb.ErrSkillNotFound, name)
}

func (f *fakeTopologyStore) ApplyTopology(_ context.Context, app agentdb.TopologyApplication, cw agentdb.ConfigWrite) (*agentdb.TopologyApplyResult, error) {
	f.applies++
	f.lastApply = app
	f.lastWrite = cw
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	return &agentdb.TopologyApplyResult{
		Workers:       app.Workers,
		Subscriptions: app.Subscriptions,
		Schedules:     app.Schedules,
		Event:         &agentdb.ConfigEvent{ID: "ce-topo", Action: agentdb.ActionTopologyApply},
	}, nil
}

// topologyHandlers builds a handler set whose principal is fixed to `project`.
func topologyHandlers(t *testing.T, store TopologyStore, project string) *Handlers {
	t.Helper()
	return newHandlers(t, Config{
		Runner:     stubRunner{},
		Store:      stubStore{},
		Topologies: store,
		Identity: func(*http.Request) (Identity, error) {
			return Identity{UserEmail: "kai@badcode.dev", Customer: project}, nil
		},
	})
}

func postJSON(t *testing.T, h http.HandlerFunc, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b)))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func soloBody(name string) map[string]any {
	return map[string]any{
		"name":    "solo",
		"version": "v1",
		"answers": map[string]any{
			"worker-name": name,
			"prompt-seed": "Write the daily tweet.",
		},
	}
}

func TestListTopologies(t *testing.T) {
	h := topologyHandlers(t, newFakeTopologyStore(), "acme")
	req := httptest.NewRequest(http.MethodGet, "/agent/topologies", nil)
	rec := httptest.NewRecorder()
	h.ListTopologies(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Topologies []struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Questions []struct {
				ID string `json:"id"`
			} `json:"questions"`
		} `json:"topologies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, tp := range out.Topologies {
		if tp.Name == "solo" && tp.Version == "v1" {
			found = true
			if len(tp.Questions) != 3 {
				t.Fatalf("solo@v1 questions: %+v", tp.Questions)
			}
		}
	}
	if !found {
		t.Fatalf("solo@v1 missing from the catalogue: %+v", out.Topologies)
	}
}

func TestPreviewTopology_RendersDiffAndWritesNothing(t *testing.T) {
	store := newFakeTopologyStore()
	// One name is already taken IN THE CALLER'S project; a same-named worker in
	// another project must not collide (P5).
	store.workers = []*agentdb.Worker{
		agentdb.NewWorker("acme", "tweeter"),
		agentdb.NewWorker("other", "daily-writer"),
	}
	h := topologyHandlers(t, store, "acme")

	rec := postJSON(t, h.PreviewTopology, "/agent/topologies/preview", soloBody("daily-writer"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Diff struct {
			NewWorkers       []string `json:"new_workers"`
			CollidingWorkers []string `json:"colliding_workers"`
			NewSchedules     []struct {
				Cron   string `json:"cron"`
				Worker string `json:"worker"`
			} `json:"new_schedules"`
		} `json:"diff"`
		Applicable bool `json:"applicable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Diff.NewWorkers) != 1 || out.Diff.NewWorkers[0] != "daily-writer" {
		t.Fatalf("new workers: %v", out.Diff.NewWorkers)
	}
	if len(out.Diff.CollidingWorkers) != 0 {
		t.Fatalf("cross-project collision leak: %v", out.Diff.CollidingWorkers)
	}
	if len(out.Diff.NewSchedules) != 1 || out.Diff.NewSchedules[0].Cron != "0 9 * * *" {
		t.Fatalf("schedules: %+v", out.Diff.NewSchedules)
	}
	if !out.Applicable {
		t.Fatalf("clean preview must be applicable")
	}
	// THE pin: preview writes nothing — the seam's only mutating method was
	// never called.
	if store.applies != 0 {
		t.Fatalf("preview called ApplyTopology %d times", store.applies)
	}
}

func TestPreviewTopology_FlagsCollisionsAndMissingPreconditions(t *testing.T) {
	store := newFakeTopologyStore()
	store.workers = []*agentdb.Worker{agentdb.NewWorker("acme", "daily-writer")}
	h := topologyHandlers(t, store, "acme")

	rec := postJSON(t, h.PreviewTopology, "/agent/topologies/preview", soloBody("daily-writer"))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview is a report, not a refusal — status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Diff struct {
			CollidingWorkers []string `json:"colliding_workers"`
		} `json:"diff"`
		Applicable bool `json:"applicable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Diff.CollidingWorkers) != 1 || out.Diff.CollidingWorkers[0] != "daily-writer" {
		t.Fatalf("collisions: %v", out.Diff.CollidingWorkers)
	}
	if out.Applicable {
		t.Fatalf("a colliding preview must not be applicable")
	}
	if store.applies != 0 {
		t.Fatalf("preview wrote")
	}
}

func TestPreviewTopology_BadInputStatusCodes(t *testing.T) {
	h := topologyHandlers(t, newFakeTopologyStore(), "acme")

	// Unknown topology → 404.
	rec := postJSON(t, h.PreviewTopology, "/agent/topologies/preview",
		map[string]any{"name": "no-such", "version": "v1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown topology: %d", rec.Code)
	}
	// A missing required answer → 400 (ErrBadAnswers).
	rec = postJSON(t, h.PreviewTopology, "/agent/topologies/preview",
		map[string]any{"name": "solo", "version": "v1", "answers": map[string]any{}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad answers: %d: %s", rec.Code, rec.Body)
	}
	// A semantically bad answer → 400 (ErrRender).
	body := soloBody("Not Kebab")
	rec = postJSON(t, h.PreviewTopology, "/agent/topologies/preview", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("render error: %d: %s", rec.Code, rec.Body)
	}
	// Malformed JSON → 400.
	req := httptest.NewRequest(http.MethodPost, "/agent/topologies/preview", strings.NewReader("{"))
	rr := httptest.NewRecorder()
	h.PreviewTopology(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: %d", rr.Code)
	}
}

func TestApplyTopology_HappyPathStampsProjectAndRecordsResolvedAnswers(t *testing.T) {
	store := newFakeTopologyStore()
	h := topologyHandlers(t, store, "acme")

	rec := postJSON(t, h.ApplyTopologyHandler, "/agent/topologies/apply", soloBody("daily-writer"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if store.applies != 1 {
		t.Fatalf("applies = %d", store.applies)
	}
	app := store.lastApply
	// Project comes from the TOKEN, never the body (the body has no project
	// field to begin with — that is the tenancy boundary).
	if app.Project != "acme" {
		t.Fatalf("project = %q", app.Project)
	}
	if app.Topology != "solo@v1" {
		t.Fatalf("topology = %q", app.Topology)
	}
	// The recorded answers are RESOLVED: the unanswered cadence question got
	// its default.
	if app.Answers["cadence"] != "daily" {
		t.Fatalf("answers not resolved: %v", app.Answers)
	}
	if len(app.Workers) != 1 || app.Workers[0].Name != "daily-writer" {
		t.Fatalf("workers: %+v", app.Workers)
	}
	if len(app.Schedules) != 1 || app.Schedules[0].Cron != "0 9 * * *" {
		t.Fatalf("schedules: %+v", app.Schedules)
	}
	// A human/API edit: the zero ConfigWrite (§15.2).
	if app.Project == "" || store.lastWrite != (agentdb.ConfigWrite{}) {
		t.Fatalf("config write: %+v", store.lastWrite)
	}
}

func TestApplyTopology_RefusesCollisionWithoutTouchingTheStore(t *testing.T) {
	store := newFakeTopologyStore()
	store.workers = []*agentdb.Worker{agentdb.NewWorker("acme", "daily-writer")}
	h := topologyHandlers(t, store, "acme")

	rec := postJSON(t, h.ApplyTopologyHandler, "/agent/topologies/apply", soloBody("daily-writer"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "daily-writer") ||
		!strings.Contains(rec.Body.String(), "nothing was changed") {
		t.Fatalf("refusal must name the collision: %s", rec.Body)
	}
	if store.applies != 0 {
		t.Fatalf("a refused apply reached the store %d times", store.applies)
	}
}

func TestApplyTopology_StoreRefusalsMapTo409(t *testing.T) {
	// The store may still refuse (a concurrent mutation after the handler's
	// check); both sentinels are 409, and the handler passes the message on.
	for _, sentinel := range []error{
		agentdb.ErrTopologyNameCollision,
		agentdb.ErrTopologyPreconditionUnmet,
	} {
		store := newFakeTopologyStore()
		store.applyErr = fmt.Errorf("%w: raced", sentinel)
		h := topologyHandlers(t, store, "acme")
		rec := postJSON(t, h.ApplyTopologyHandler, "/agent/topologies/apply", soloBody("daily-writer"))
		if rec.Code != http.StatusConflict {
			t.Fatalf("%v → %d, want 409", sentinel, rec.Code)
		}
	}
}

func TestTopologyRoutes_UnauthenticatedAndUnconfigured(t *testing.T) {
	// 401 when identity fails.
	denied := newHandlers(t, Config{
		Runner:     stubRunner{},
		Store:      stubStore{},
		Topologies: newFakeTopologyStore(),
		Identity:   func(*http.Request) (Identity, error) { return Identity{}, fmt.Errorf("no token") },
	})
	rec := postJSON(t, denied.ApplyTopologyHandler, "/agent/topologies/apply", soloBody("w"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated apply: %d", rec.Code)
	}

	// 501 when the host wired no store (the sqlite fallback).
	bare := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{Customer: "acme"}, nil },
	})
	rec = postJSON(t, bare.PreviewTopology, "/agent/topologies/preview", soloBody("w"))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured preview: %d", rec.Code)
	}
}

func TestTopologyRoutesAreMounted(t *testing.T) {
	if DefaultEndpoints.ListTopologies != "GET /agent/topologies" ||
		DefaultEndpoints.PreviewTopology != "POST /agent/topologies/preview" ||
		DefaultEndpoints.ApplyTopology != "POST /agent/topologies/apply" {
		t.Fatalf("route surface moved: %q %q %q",
			DefaultEndpoints.ListTopologies, DefaultEndpoints.PreviewTopology, DefaultEndpoints.ApplyTopology)
	}
	h := topologyHandlers(t, newFakeTopologyStore(), "acme")
	mux := h.Mux()
	req := httptest.NewRequest(http.MethodGet, "/agent/topologies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /agent/topologies via the mux: %d", rec.Code)
	}
}
