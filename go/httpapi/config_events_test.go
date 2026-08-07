package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fakeConfigLog records the query it was handed and returns a canned page, so
// the handler's parameter plumbing can be asserted without a database.
type fakeConfigLog struct {
	got  agentdb.ConfigEventQuery
	out  []*agentdb.ConfigEvent
	err  error
	call int
}

func (f *fakeConfigLog) ListConfigEvents(_ context.Context, q agentdb.ConfigEventQuery) ([]*agentdb.ConfigEvent, error) {
	f.call++
	f.got = q
	return f.out, f.err
}

func newConfigLogHandlers(t *testing.T, store ConfigLogStore, id IdentityFunc) *Handlers {
	t.Helper()
	return newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Identity:  id,
		ConfigLog: store,
	})
}

// Every filter in the pinned contract reaches the store, and the project comes
// from the token — never from the query string (P5).
func TestListConfigEvents_QueryPlumbing(t *testing.T) {
	tests := []struct {
		name string
		path string
		want agentdb.ConfigEventQuery
	}{
		{
			name: "no filters",
			path: "/agent/config-events",
			want: agentdb.ConfigEventQuery{Project: "acme"},
		},
		{
			name: "every filter",
			path: "/agent/config-events?action=worker_prompt_write&actor_worker=email-reviewer&since=1789000000000&until=1789000009999&limit=25&before_seq=42",
			want: agentdb.ConfigEventQuery{
				Project: "acme", Action: "worker_prompt_write", ActorWorker: "email-reviewer",
				Since: 1789000000000, Until: 1789000009999, Limit: 25, BeforeSeq: 42,
			},
		},
		{
			name: "action prefix passes through verbatim",
			path: "/agent/config-events?action=worker_*",
			want: agentdb.ConfigEventQuery{Project: "acme", Action: "worker_*"},
		},
		{
			name: "a project in the query is ignored, not honoured",
			path: "/agent/config-events?project=other",
			want: agentdb.ConfigEventQuery{Project: "acme"},
		},
		{
			name: "junk numerics degrade to unbounded rather than erroring",
			path: "/agent/config-events?since=abc&until=-5&limit=-1&before_seq=x",
			want: agentdb.ConfigEventQuery{Project: "acme"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeConfigLog{}
			h := newConfigLogHandlers(t, store, identityFor("acme"))
			if rec := do(h, http.MethodGet, tc.path, ""); rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if store.got != tc.want {
				t.Fatalf("query:\n got %+v\nwant %+v", store.got, tc.want)
			}
		})
	}
}

// The response is exactly {"config_events":[…]} — the shape web/src/configLog.ts
// is written against. An empty log is [] and not null, so the UI can tell
// "nothing has changed" from "the route is broken".
func TestListConfigEvents_ResponseShape(t *testing.T) {
	store := &fakeConfigLog{out: []*agentdb.ConfigEvent{
		{ID: "b", Project: "acme", Seq: 2, Action: agentdb.ActionWorkerPromptWrite,
			ActorWorker: "email-reviewer", ActorSession: "sess-1",
			Payload:   agentdb.JSONMap{"name": "email-answerer"},
			Rationale: "answers read as curt", CreatedAt: 1789000000123},
		{ID: "a", Project: "acme", Seq: 1, Action: agentdb.ActionWorkerCreate,
			Payload: agentdb.JSONMap{"name": "email-answerer"}, CreatedAt: 1789000000000},
	}}
	h := newConfigLogHandlers(t, store, identityFor("acme"))
	rec := do(h, http.MethodGet, "/agent/config-events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		ConfigEvents []map[string]any `json:"config_events"`
	}
	decodeInto(t, rec, &body)
	if len(body.ConfigEvents) != 2 {
		t.Fatalf("want 2 records, got %d", len(body.ConfigEvents))
	}
	// Newest first: the store orders by seq DESC and the handler must not resort.
	if body.ConfigEvents[0]["id"] != "b" {
		t.Fatalf("records must be newest-first: %+v", body.ConfigEvents)
	}
	first := body.ConfigEvents[0]
	for _, k := range []string{"id", "project", "seq", "actor_worker", "actor_session", "action", "payload", "rationale", "created_at"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("record is missing %q (the pinned ConfigEvent shape): %+v", k, first)
		}
	}
	if first["created_at"].(float64) != 1789000000123 {
		t.Fatalf("created_at must be the raw unix MILLISECONDS: %v", first["created_at"])
	}

	empty := &fakeConfigLog{}
	h = newConfigLogHandlers(t, empty, identityFor("acme"))
	rec = do(h, http.MethodGet, "/agent/config-events", "")
	if got := rec.Body.String(); got != "{\"config_events\":[]}\n" {
		t.Fatalf("empty log must be [], not null: %s", got)
	}
}

// Auth posture: 401 without an identity, 403 for a token carrying no project,
// 501 when the host wired no store, and no method other than GET.
func TestListConfigEvents_AuthAndAvailability(t *testing.T) {
	t.Run("401 without identity", func(t *testing.T) {
		store := &fakeConfigLog{}
		h := newConfigLogHandlers(t, store, func(*http.Request) (Identity, error) {
			return Identity{}, http.ErrNoCookie
		})
		if rec := do(h, http.MethodGet, "/agent/config-events", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", rec.Code)
		}
		if store.call != 0 {
			t.Fatal("an unauthenticated request must not reach the store")
		}
	})

	t.Run("403 with no project claim", func(t *testing.T) {
		store := &fakeConfigLog{}
		h := newConfigLogHandlers(t, store, identityFor(""))
		if rec := do(h, http.MethodGet, "/agent/config-events", ""); rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d", rec.Code)
		}
		if store.call != 0 {
			t.Fatal("a projectless token must not reach the store")
		}
	})

	t.Run("501 with no store", func(t *testing.T) {
		h := newConfigLogHandlers(t, nil, identityFor("acme"))
		if rec := do(h, http.MethodGet, "/agent/config-events", ""); rec.Code != http.StatusNotImplemented {
			t.Fatalf("status=%d", rec.Code)
		}
	})

	t.Run("write methods are not routed", func(t *testing.T) {
		// The log is append-only and written only as the shadow of a real
		// mutation (§15.4): there is no POST here, by design.
		h := newConfigLogHandlers(t, &fakeConfigLog{}, identityFor("acme"))
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			if rec := do(h, m, "/agent/config-events", `{}`); rec.Code == http.StatusOK {
				t.Fatalf("%s must not be served", m)
			}
		}
	})
}

// PROJECT ISOLATION, against the real store: two projects write their own
// configuration history and neither route can see the other's, whatever the
// query says.
func TestListConfigEvents_ProjectIsolation_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	mine, theirs := "cfgread-mine-"+t.Name(), "cfgread-theirs-"+t.Name()
	t.Cleanup(func() {
		for _, p := range []string{mine, theirs} {
			_ = store.PurgeConfigEvents(context.Background(), p)
			_ = store.DB().Exec("DELETE FROM workers WHERE project = ?", p).Error
		}
	})

	seed := func(project, worker string) {
		t.Helper()
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{},
			Identity: identityFor(project), AgentDB: store,
		})
		if rec := do(h, http.MethodPut, "/agent/workers/"+worker, `{"description":"seeded"}`); rec.Code != http.StatusOK {
			t.Fatalf("seed %s/%s: status=%d body=%s", project, worker, rec.Code, rec.Body)
		}
	}
	seed(mine, "email-answerer")
	seed(theirs, "their-secret-worker")

	read := func(project, query string) []*agentdb.ConfigEvent {
		t.Helper()
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{},
			Identity: identityFor(project), AgentDB: store,
		})
		rec := do(h, http.MethodGet, "/agent/config-events"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("read %s: status=%d body=%s", project, rec.Code, rec.Body)
		}
		var body struct {
			ConfigEvents []*agentdb.ConfigEvent `json:"config_events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.ConfigEvents
	}

	got := read(mine, "")
	if len(got) != 1 || got[0].Project != mine {
		t.Fatalf("a project must see exactly its own history, got %+v", got)
	}
	if got[0].Payload["name"] != "email-answerer" {
		t.Fatalf("payload must be the full new row: %+v", got[0].Payload)
	}
	// …and asking for the other project by name changes nothing: the query
	// parameter is not a project selector.
	if got := read(mine, "?project="+theirs); len(got) != 1 || got[0].Project != mine {
		t.Fatalf("a project= query must never cross the boundary, got %+v", got)
	}
	// An actor filter cannot be used to reach across either.
	if got := read(mine, "?action=worker_*"); len(got) != 1 || got[0].Project != mine {
		t.Fatalf("filters must apply within the project only, got %+v", got)
	}
	// The store's guarded write path still logged exactly one event per project.
	if got := read(theirs, ""); len(got) != 1 || got[0].Payload["name"] != "their-secret-worker" {
		t.Fatalf("the other project's own history is wrong: %+v", got)
	}
}

// Filters and the seq page cursor behave against the real store: `since`/`until`
// key on the millisecond clock, paging keys on seq (J2).
func TestListConfigEvents_FiltersAndPaging_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	project := "cfgread-page-" + t.Name()
	t.Cleanup(func() {
		_ = store.PurgeConfigEvents(context.Background(), project)
		_ = store.DB().Exec("DELETE FROM workers WHERE project = ?", project).Error
	})
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{},
		Identity: identityFor(project), AgentDB: store,
	})
	for _, w := range []string{"w1", "w2", "w3"} {
		if rec := do(h, http.MethodPut, "/agent/workers/"+w, `{"description":"d"}`); rec.Code != http.StatusOK {
			t.Fatalf("seed %s: status=%d body=%s", w, rec.Code, rec.Body)
		}
	}

	read := func(query string) []*agentdb.ConfigEvent {
		t.Helper()
		rec := do(h, http.MethodGet, "/agent/config-events"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		var body struct {
			ConfigEvents []*agentdb.ConfigEvent `json:"config_events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.ConfigEvents
	}

	all := read("")
	if len(all) != 3 {
		t.Fatalf("want 3 records, got %d", len(all))
	}
	if !(all[0].Seq > all[1].Seq && all[1].Seq > all[2].Seq) {
		t.Fatalf("records must be ordered by seq DESC: %d %d %d", all[0].Seq, all[1].Seq, all[2].Seq)
	}
	page1 := read("?limit=2")
	if len(page1) != 2 || page1[0].ID != all[0].ID {
		t.Fatalf("limit must cut the newest page: %+v", page1)
	}
	page2 := read("?limit=2&before_seq=" + itoa(page1[1].Seq))
	if len(page2) != 1 || page2[0].ID != all[2].ID {
		t.Fatalf("before_seq must continue exactly where the page ended: %+v", page2)
	}
	if got := read("?action=worker_create"); len(got) != 3 {
		t.Fatalf("exact action filter: got %d", len(got))
	}
	if got := read("?action=schedule_*"); len(got) != 0 {
		t.Fatalf("a prefix matching nothing must return nothing: got %d", len(got))
	}
	if got := read("?until=" + itoa(all[2].CreatedAt-1)); len(got) != 0 {
		t.Fatalf("until is a millisecond clock bound: got %d", len(got))
	}
	if got := read("?since=" + itoa(all[0].CreatedAt+1)); len(got) != 0 {
		t.Fatalf("since is a millisecond clock bound: got %d", len(got))
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
