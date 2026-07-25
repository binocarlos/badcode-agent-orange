package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fakeScheduleStore is an in-memory ScheduleStore. Like fakeWorkerStore it
// honours the project argument verbatim, so a tenancy leak in a handler shows up
// as a cross-project hit rather than as a passing test.
type fakeScheduleStore struct {
	rows      map[string]*agentdb.Schedule
	lastWrite agentdb.ConfigWrite
	writes    int
}

func newFakeScheduleStore(rows ...*agentdb.Schedule) *fakeScheduleStore {
	f := &fakeScheduleStore{rows: map[string]*agentdb.Schedule{}}
	for _, s := range rows {
		f.rows[s.ID] = s
	}
	return f
}

func (f *fakeScheduleStore) CreateSchedule(_ context.Context, s *agentdb.Schedule, cw agentdb.ConfigWrite) (*agentdb.Schedule, error) {
	f.lastWrite = cw
	f.writes++
	if s.Worker == "" {
		return nil, fmt.Errorf("%w: worker is required", agentdb.ErrScheduleInvalid)
	}
	if _, err := agentdb.ParseCron(s.Cron); err != nil {
		return nil, fmt.Errorf("%w: %w", agentdb.ErrScheduleInvalid, err)
	}
	if s.ID == "" {
		s.ID = fmt.Sprintf("sched-%d", len(f.rows)+1)
	}
	f.rows[s.ID] = s
	return s, nil
}

func (f *fakeScheduleStore) GetSchedule(_ context.Context, project, id string) (*agentdb.Schedule, error) {
	s, ok := f.rows[id]
	if !ok || s.Project != project {
		return nil, fmt.Errorf("%w: %s/%s", agentdb.ErrScheduleNotFound, project, id)
	}
	return s, nil
}

func (f *fakeScheduleStore) ListSchedules(_ context.Context, project string) ([]*agentdb.Schedule, error) {
	out := []*agentdb.Schedule{}
	for _, s := range f.rows {
		if s.Project == project {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeScheduleStore) UpdateSchedule(_ context.Context, s *agentdb.Schedule, cw agentdb.ConfigWrite) (*agentdb.Schedule, error) {
	f.lastWrite = cw
	f.writes++
	existing, ok := f.rows[s.ID]
	if !ok || existing.Project != s.Project {
		return nil, fmt.Errorf("%w: %s", agentdb.ErrScheduleNotFound, s.ID)
	}
	if _, err := agentdb.ParseCron(s.Cron); err != nil {
		return nil, fmt.Errorf("%w: %w", agentdb.ErrScheduleInvalid, err)
	}
	f.rows[s.ID] = s
	return s, nil
}

func (f *fakeScheduleStore) DeleteSchedule(_ context.Context, project, id string, cw agentdb.ConfigWrite) error {
	f.lastWrite = cw
	f.writes++
	s, ok := f.rows[id]
	if !ok || s.Project != project {
		return fmt.Errorf("%w: %s/%s", agentdb.ErrScheduleNotFound, project, id)
	}
	delete(f.rows, id)
	return nil
}

func scheduleHandlers(t *testing.T, store ScheduleStore, identity IdentityFunc) *Handlers {
	t.Helper()
	if identity == nil {
		identity = okIdentity
	}
	return newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Identity:  identity,
		Schedules: store,
	})
}

func scheduleReq(method, path, id, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if id != "" {
		r.SetPathValue("id", id)
	}
	return r
}

func TestSchedulesHTTPCRUD(t *testing.T) {
	store := newFakeScheduleStore()
	h := scheduleHandlers(t, store, nil)

	// Create: project comes from the token, never from the body.
	rec := httptest.NewRecorder()
	h.Schedules(rec, scheduleReq("POST", "/agent/schedules", "",
		`{"worker":"tweet-author","cron":"0 10 * * *","input":"write the morning tweet",
		  "project":"someone-else","rationale":"the strategy says daily"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d body=%s", rec.Code, rec.Body)
	}
	var created agentdb.Schedule
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if created.Project != "acme" {
		t.Fatalf("project must come from the token, got %q", created.Project)
	}
	if !created.Enabled {
		t.Fatalf("a schedule you just created is live unless you say otherwise")
	}
	if created.Input != "write the morning tweet" {
		t.Fatalf("input did not round-trip: %+v", created)
	}
	if store.lastWrite.Rationale != "the strategy says daily" {
		t.Fatalf("rationale not threaded into the config write: %+v", store.lastWrite)
	}
	if store.lastWrite.Worker != "" || store.lastWrite.Session != "" {
		t.Fatalf("an HTTP edit logs no actor (§15.2): %+v", store.lastWrite)
	}

	// List + get.
	rec = httptest.NewRecorder()
	h.Schedules(rec, scheduleReq("GET", "/agent/schedules", "", ""))
	var listed struct {
		Schedules []*agentdb.Schedule `json:"schedules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v (%s)", err, rec.Body)
	}
	if len(listed.Schedules) != 1 {
		t.Fatalf("list body: %s", rec.Body)
	}

	rec = httptest.NewRecorder()
	h.Schedule(rec, scheduleReq("GET", "/agent/schedules/"+created.ID, created.ID, ""))
	if rec.Code != 200 {
		t.Fatalf("get status %d body=%s", rec.Code, rec.Body)
	}

	// Update: partial, and the echo is the stored row (§9).
	rec = httptest.NewRecorder()
	h.Schedule(rec, scheduleReq("PUT", "/agent/schedules/"+created.ID, created.ID,
		`{"cron":"0 17 * * *","enabled":false}`))
	if rec.Code != 200 {
		t.Fatalf("update status %d body=%s", rec.Code, rec.Body)
	}
	var updated agentdb.Schedule
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Cron != "0 17 * * *" || updated.Enabled {
		t.Fatalf("update did not stick: %+v", updated)
	}
	if updated.Input != "write the morning tweet" {
		t.Fatalf("an absent field must not blank the instruction: %+v", updated)
	}

	// Delete.
	rec = httptest.NewRecorder()
	h.Schedule(rec, scheduleReq("DELETE", "/agent/schedules/"+created.ID, created.ID, ""))
	if rec.Code != 200 {
		t.Fatalf("delete status %d body=%s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.Schedule(rec, scheduleReq("GET", "/agent/schedules/"+created.ID, created.ID, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted schedule should 404, got %d", rec.Code)
	}
}

// TestSchedulesHTTPRejectsBadCron proves §9's validate-before-write: a cron the
// parser cannot read is a 400, not a row that silently never fires.
func TestSchedulesHTTPRejectsBadCron(t *testing.T) {
	store := newFakeScheduleStore()
	h := scheduleHandlers(t, store, nil)

	for _, body := range []string{
		`{"worker":"w","cron":"@daily","input":"x"}`,
		`{"worker":"w","cron":"0 99 * * *","input":"x"}`,
		`{"worker":"","cron":"0 10 * * *","input":"x"}`,
	} {
		rec := httptest.NewRecorder()
		h.Schedules(rec, scheduleReq("POST", "/agent/schedules", "", body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: want 400, got %d (%s)", body, rec.Code, rec.Body)
		}
	}
	if len(store.rows) != 0 {
		t.Fatalf("a refused create must write nothing: %+v", store.rows)
	}
}

// TestSchedulesHTTPProjectIsolation is the §12 negative test at the HTTP edge.
func TestSchedulesHTTPProjectIsolation(t *testing.T) {
	theirs := agentdb.NewSchedule("globex", "tweet-author", "0 10 * * *", "theirs")
	theirs.ID = "sched-theirs"
	store := newFakeScheduleStore(theirs)
	h := scheduleHandlers(t, store, nil) // okIdentity → project "acme"

	for _, tc := range []struct {
		name    string
		method  string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"get", "GET", "", h.Schedule},
		{"update", "PUT", `{"input":"hijacked"}`, h.Schedule},
		{"delete", "DELETE", "", h.Schedule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, scheduleReq(tc.method, "/agent/schedules/sched-theirs", "sched-theirs", tc.body))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("cross-project %s: want 404, got %d (%s)", tc.name, rec.Code, rec.Body)
			}
		})
	}
	if store.rows["sched-theirs"].Input != "theirs" {
		t.Fatalf("cross-project write leaked through: %+v", store.rows["sched-theirs"])
	}

	// The list is scoped too.
	rec := httptest.NewRecorder()
	h.Schedules(rec, scheduleReq("GET", "/agent/schedules", "", ""))
	var listed struct {
		Schedules []*agentdb.Schedule `json:"schedules"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	if len(listed.Schedules) != 0 {
		t.Fatalf("list leaked another project's schedules: %s", rec.Body)
	}
}

// TestSchedulesHTTPUnconfigured501 mirrors the other optional route sets: a host
// with no store gets 501, never a panic.
func TestSchedulesHTTPUnconfigured501(t *testing.T) {
	h := newHandlers(t, Config{Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity})
	rec := httptest.NewRecorder()
	h.Schedules(rec, scheduleReq("GET", "/agent/schedules", "", ""))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", rec.Code)
	}
}

// TestSchedulesHTTPMethodNotAllowed pins the multi-method switch.
func TestSchedulesHTTPMethodNotAllowed(t *testing.T) {
	h := scheduleHandlers(t, newFakeScheduleStore(), nil)
	rec := httptest.NewRecorder()
	h.Schedules(rec, scheduleReq("DELETE", "/agent/schedules", "", ""))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}
