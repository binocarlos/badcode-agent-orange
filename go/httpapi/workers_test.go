package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fakeWorkerStore is an in-memory WorkersStore keyed by project+name. It mirrors
// the real store's contract closely enough to prove the handlers' behaviour:
// the project argument is honoured verbatim (so a leak shows up as a cross-
// project hit) and the same sentinel errors come back.
type fakeWorkerStore struct {
	rows map[string]*agentdb.Worker
	err  error // when set, every method returns it
	// The config-log actor the handler passed down, and how many writes it made.
	lastWrite agentdb.ConfigWrite
	writes    int
}

func newFakeWorkerStore(workers ...*agentdb.Worker) *fakeWorkerStore {
	f := &fakeWorkerStore{rows: map[string]*agentdb.Worker{}}
	for _, w := range workers {
		f.rows[w.Project+"/"+w.Name] = w
	}
	return f
}

func (f *fakeWorkerStore) ListWorkers(_ context.Context, project string) ([]*agentdb.Worker, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []*agentdb.Worker{}
	for _, w := range f.rows {
		if w.Project == project {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeWorkerStore) GetWorker(_ context.Context, project, name string) (*agentdb.Worker, error) {
	if f.err != nil {
		return nil, f.err
	}
	w, ok := f.rows[project+"/"+name]
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s", agentdb.ErrWorkerNotFound, project, name)
	}
	return w, nil
}

func (f *fakeWorkerStore) UpsertWorker(_ context.Context, w *agentdb.Worker, cw agentdb.ConfigWrite) (*agentdb.Worker, error) {
	f.lastWrite = cw
	f.writes++
	if f.err != nil {
		return nil, f.err
	}
	if w.Name == "" {
		return nil, fmt.Errorf("%w: name is required", agentdb.ErrWorkerInvalid)
	}
	f.rows[w.Project+"/"+w.Name] = w
	return w, nil
}

func (f *fakeWorkerStore) DeleteWorker(_ context.Context, project, name string, cw agentdb.ConfigWrite) error {
	f.lastWrite = cw
	f.writes++
	if f.err != nil {
		return f.err
	}
	key := project + "/" + name
	if _, ok := f.rows[key]; !ok {
		return fmt.Errorf("%w: %s/%s", agentdb.ErrWorkerNotFound, project, name)
	}
	delete(f.rows, key)
	return nil
}

func workerHandlers(t *testing.T, store WorkersStore, identity IdentityFunc) *Handlers {
	t.Helper()
	if identity == nil {
		identity = okIdentity
	}
	return newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: identity,
		Workers:  store,
	})
}

func workerReq(method, path, name, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if name != "" {
		r.SetPathValue("name", name)
	}
	return r
}

func TestWorkersHTTP_ListAndGet(t *testing.T) {
	answerer := agentdb.NewWorker("acme", "email-answerer")
	answerer.Description = "answers inbound email"
	answerer.Briefing = agentdb.SelectorList{"kind=house-style"}
	archivist := agentdb.NewWorker("acme", "archivist")
	h := workerHandlers(t, newFakeWorkerStore(answerer, archivist), nil)

	rec := httptest.NewRecorder()
	h.ListWorkers(rec, workerReq("GET", "/agent/workers", "", ""))
	if rec.Code != 200 {
		t.Fatalf("list status %d body=%s", rec.Code, rec.Body)
	}
	var listed struct {
		Workers []*agentdb.Worker `json:"workers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v (%s)", err, rec.Body)
	}
	if len(listed.Workers) != 2 || listed.Workers[0].Name != "archivist" {
		t.Fatalf("list body: %s", rec.Body)
	}

	rec = httptest.NewRecorder()
	h.GetWorker(rec, workerReq("GET", "/agent/workers/email-answerer", "email-answerer", ""))
	if rec.Code != 200 {
		t.Fatalf("get status %d body=%s", rec.Code, rec.Body)
	}
	var got agentdb.Worker
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v (%s)", err, rec.Body)
	}
	if got.Name != "email-answerer" || got.MaxInstances != 1 || !got.Enabled {
		t.Fatalf("get body: %+v", got)
	}
	if len(got.Briefing) != 1 || got.Briefing[0] != "kind=house-style" {
		t.Fatalf("briefing not serialised: %+v", got.Briefing)
	}

	rec = httptest.NewRecorder()
	h.GetWorker(rec, workerReq("GET", "/agent/workers/nobody", "nobody", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing worker: want 404, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestWorkersHTTP_PutDefaultsAndEcho(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		wantMaxInstances int
		wantEnabled      bool
		wantBriefing     agentdb.SelectorList
	}{
		{
			name:             "omitted fields take spec defaults",
			body:             `{"description":"answers email"}`,
			wantMaxInstances: 1,
			wantEnabled:      true,
			wantBriefing:     nil,
		},
		{
			name:             "explicit values win",
			body:             `{"max_instances":4,"enabled":false,"briefing":["kind=house-style","topic=pricing"]}`,
			wantMaxInstances: 4,
			wantEnabled:      false,
			wantBriefing:     agentdb.SelectorList{"kind=house-style", "topic=pricing"},
		},
		{
			name:             "explicit enabled false is not swallowed",
			body:             `{"enabled":false}`,
			wantMaxInstances: 1,
			wantEnabled:      false,
			wantBriefing:     nil,
		},
		{
			name:             "empty briefing list is preserved",
			body:             `{"briefing":[]}`,
			wantMaxInstances: 1,
			wantEnabled:      true,
			wantBriefing:     agentdb.SelectorList{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeWorkerStore()
			h := workerHandlers(t, store, nil)
			rec := httptest.NewRecorder()
			h.PutWorker(rec, workerReq("PUT", "/agent/workers/email-answerer", "email-answerer", tc.body))
			if rec.Code != 200 {
				t.Fatalf("put status %d body=%s", rec.Code, rec.Body)
			}
			// The response is the stored row read back (§9), not the request.
			var echoed agentdb.Worker
			if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
				t.Fatalf("decode echo: %v (%s)", err, rec.Body)
			}
			stored := store.rows["acme/email-answerer"]
			if stored == nil {
				t.Fatalf("nothing stored: %#v", store.rows)
			}
			if stored.Project != "acme" {
				t.Fatalf("project must come from the token, got %q", stored.Project)
			}
			if stored.MaxInstances != tc.wantMaxInstances || echoed.MaxInstances != tc.wantMaxInstances {
				t.Fatalf("max_instances: want %d, stored %d, echoed %d",
					tc.wantMaxInstances, stored.MaxInstances, echoed.MaxInstances)
			}
			if stored.Enabled != tc.wantEnabled || echoed.Enabled != tc.wantEnabled {
				t.Fatalf("enabled: want %v, stored %v, echoed %v",
					tc.wantEnabled, stored.Enabled, echoed.Enabled)
			}
			if len(stored.Briefing) != len(tc.wantBriefing) {
				t.Fatalf("briefing: want %#v, got %#v", tc.wantBriefing, stored.Briefing)
			}
			if (tc.wantBriefing == nil) != (stored.Briefing == nil) {
				t.Fatalf("briefing nil-ness: want nil=%v, got %#v", tc.wantBriefing == nil, stored.Briefing)
			}
		})
	}
}

func TestWorkersHTTP_PutRejectsBadInput(t *testing.T) {
	h := workerHandlers(t, newFakeWorkerStore(), nil)

	rec := httptest.NewRecorder()
	h.PutWorker(rec, workerReq("PUT", "/agent/workers/x", "x", `{not json`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed json: want 400, got %d", rec.Code)
	}

	// A validation failure from the store surfaces as 400, never 500.
	store := newFakeWorkerStore()
	store.err = fmt.Errorf("%w: name %q is not kebab-case", agentdb.ErrWorkerInvalid, "Bad Name")
	h = workerHandlers(t, store, nil)
	rec = httptest.NewRecorder()
	h.PutWorker(rec, workerReq("PUT", "/agent/workers/Bad%20Name", "Bad Name", `{}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid worker: want 400, got %d (%s)", rec.Code, rec.Body)
	}

	// Anything else is a 500.
	store = newFakeWorkerStore()
	store.err = errors.New("database on fire")
	h = workerHandlers(t, store, nil)
	rec = httptest.NewRecorder()
	h.PutWorker(rec, workerReq("PUT", "/agent/workers/x", "x", `{}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure: want 500, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestWorkersHTTP_Delete(t *testing.T) {
	store := newFakeWorkerStore(agentdb.NewWorker("acme", "archivist"))
	h := workerHandlers(t, store, nil)

	rec := httptest.NewRecorder()
	h.DeleteWorker(rec, workerReq("DELETE", "/agent/workers/archivist", "archivist", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d (%s)", rec.Code, rec.Body)
	}
	if len(store.rows) != 0 {
		t.Fatalf("row not deleted: %#v", store.rows)
	}

	rec = httptest.NewRecorder()
	h.DeleteWorker(rec, workerReq("DELETE", "/agent/workers/archivist", "archivist", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: want 404, got %d", rec.Code)
	}
}

// The project comes from the token and nowhere else: a caller scoped to "other"
// must not read, overwrite, or delete acme's worker, and must not be able to
// smuggle a project in the request body.
func TestWorkersHTTP_ProjectIsolation(t *testing.T) {
	acmeWorker := agentdb.NewWorker("acme", "email-answerer")
	acmeWorker.SystemPrompt = "acme prompt"
	store := newFakeWorkerStore(acmeWorker)

	otherIdentity := func(*http.Request) (Identity, error) {
		return Identity{UserEmail: "eve@other.com", Customer: "other"}, nil
	}
	h := workerHandlers(t, store, otherIdentity)

	rec := httptest.NewRecorder()
	h.ListWorkers(rec, workerReq("GET", "/agent/workers", "", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"workers":[]`) {
		t.Fatalf("list leaked across projects: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.GetWorker(rec, workerReq("GET", "/agent/workers/email-answerer", "email-answerer", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project get: want 404, got %d (%s)", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.DeleteWorker(rec, workerReq("DELETE", "/agent/workers/email-answerer", "email-answerer", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project delete: want 404, got %d", rec.Code)
	}
	if store.rows["acme/email-answerer"] == nil {
		t.Fatalf("acme's worker was deleted by a caller from another project")
	}

	// A body-supplied project is ignored — the token wins.
	rec = httptest.NewRecorder()
	h.PutWorker(rec, workerReq("PUT", "/agent/workers/email-answerer", "email-answerer",
		`{"project":"acme","system_prompt":"hijacked"}`))
	if rec.Code != 200 {
		t.Fatalf("put status %d body=%s", rec.Code, rec.Body)
	}
	if store.rows["acme/email-answerer"].SystemPrompt != "acme prompt" {
		t.Fatalf("cross-project write leaked: %+v", store.rows["acme/email-answerer"])
	}
	if store.rows["other/email-answerer"] == nil {
		t.Fatalf("write landed outside the caller's project: %#v", store.rows)
	}
}

func TestWorkersHTTP_Unauthorized(t *testing.T) {
	h := workerHandlers(t, newFakeWorkerStore(), func(*http.Request) (Identity, error) {
		return Identity{}, errors.New("no token")
	})
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		req     *http.Request
	}{
		{"list", h.ListWorkers, workerReq("GET", "/agent/workers", "", "")},
		{"get", h.GetWorker, workerReq("GET", "/agent/workers/w", "w", "")},
		{"put", h.PutWorker, workerReq("PUT", "/agent/workers/w", "w", `{}`)},
		{"delete", h.DeleteWorker, workerReq("DELETE", "/agent/workers/w", "w", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, tc.req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d", rec.Code)
			}
		})
	}
}

// With no worker store wired the routes are honestly unimplemented rather than
// silently returning an empty catalogue.
func TestWorkersHTTP_NotConfigured(t *testing.T) {
	h := newHandlers(t, Config{Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity})
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		req     *http.Request
	}{
		{"list", h.ListWorkers, workerReq("GET", "/agent/workers", "", "")},
		{"get", h.GetWorker, workerReq("GET", "/agent/workers/w", "w", "")},
		{"put", h.PutWorker, workerReq("PUT", "/agent/workers/w", "w", `{}`)},
		{"delete", h.DeleteWorker, workerReq("DELETE", "/agent/workers/w", "w", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, tc.req)
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("want 501, got %d", rec.Code)
			}
		})
	}
}

// New() fills Workers from AgentDB so a host that already passes its agentdb
// store gets the worker routes for free — but a nil *agentdb.Store must not
// become a non-nil interface, which would turn the honest 501 into a panic.
func TestWorkersHTTP_StoreDefaultsFromAgentDB(t *testing.T) {
	t.Run("auto-filled from AgentDB", func(t *testing.T) {
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity,
			AgentDB: &agentdb.Store{},
		})
		if h.cfg.Workers == nil {
			t.Fatal("Workers should default to the AgentDB store")
		}
	})

	t.Run("explicit store wins over AgentDB", func(t *testing.T) {
		fake := newFakeWorkerStore(agentdb.NewWorker("acme", "archivist"))
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity,
			AgentDB: &agentdb.Store{}, Workers: fake,
		})
		rec := httptest.NewRecorder()
		h.ListWorkers(rec, workerReq("GET", "/agent/workers", "", ""))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "archivist") {
			t.Fatalf("explicit store not used: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("nil AgentDB leaves the routes unimplemented", func(t *testing.T) {
		var nilDB *agentdb.Store
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity,
			AgentDB: nilDB,
		})
		if h.cfg.Workers != nil {
			t.Fatal("a nil *agentdb.Store must not become a non-nil WorkersStore")
		}
		rec := httptest.NewRecorder()
		h.ListWorkers(rec, workerReq("GET", "/agent/workers", "", ""))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("want 501, got %d", rec.Code)
		}
	})
}

// The routes must be reachable through Mux() with their path wildcards bound,
// and *agentdb.Store must satisfy the WorkersStore seam.
func TestWorkersHTTP_MuxRouting(t *testing.T) {
	var _ WorkersStore = (*agentdb.Store)(nil)

	store := newFakeWorkerStore(agentdb.NewWorker("acme", "archivist"))
	h := workerHandlers(t, store, nil)
	mux := h.Mux()

	for _, tc := range []struct {
		method, path string
		body         string
		wantCode     int
	}{
		{"GET", "/agent/workers", "", 200},
		{"GET", "/agent/workers/archivist", "", 200},
		{"PUT", "/agent/workers/new-worker", `{"description":"d"}`, 200},
		{"DELETE", "/agent/workers/archivist", "", http.StatusNoContent},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var req *http.Request
			if tc.body == "" {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			} else {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("want %d, got %d (%s)", tc.wantCode, rec.Code, rec.Body)
			}
		})
	}
	if store.rows["acme/new-worker"] == nil {
		t.Fatalf("PUT through the mux did not reach the store: %#v", store.rows)
	}

	// A host carrying an Endpoints value from before the worker routes existed
	// leaves those four fields empty; registering an empty pattern would panic,
	// so they are guarded like Snapshot/Archive.
	noWorkerRoutes := DefaultEndpoints
	noWorkerRoutes.ListWorkers = ""
	noWorkerRoutes.GetWorker = ""
	noWorkerRoutes.PutWorker = ""
	noWorkerRoutes.DeleteWorker = ""
	older := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity, Workers: store,
		Endpoints: noWorkerRoutes,
	})
	m := older.Mux() // must not panic
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/agent/workers", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unmounted worker route: want 404, got %d", rec.Code)
	}
}
