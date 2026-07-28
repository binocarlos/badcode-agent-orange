package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fakeMemories records the query it was handed and returns a canned page, so the
// handler's parameter plumbing can be asserted without a database.
type fakeMemories struct {
	got  agentdb.MemorySearchQuery
	out  []*agentdb.MemorySearchResult
	err  error
	call int
}

func (f *fakeMemories) SearchMemories(_ context.Context, q *agentdb.MemorySearchQuery) ([]*agentdb.MemorySearchResult, error) {
	f.call++
	f.got = *q
	return f.out, f.err
}

func newMemoryHandlers(t *testing.T, store MemoryStore, id IdentityFunc) *Handlers {
	t.Helper()
	return newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: id,
		Memories: store,
	})
}

// Every parameter in the §7.6 contract reaches the store — and nothing else
// does. The project comes from the token, never from the query string (P5).
func TestListMemories_QueryPlumbing(t *testing.T) {
	tests := []struct {
		name string
		path string
		want agentdb.MemorySearchQuery
	}{
		{
			name: "no filters is the recency question",
			path: "/agent/memories",
			want: agentdb.MemorySearchQuery{Project: "acme"},
		},
		{
			name: "selector, query and limit",
			path: "/agent/memories?selector=kind%3Drolling-summary%2Cworker%3Demail-answerer&query=refund+policy&limit=25",
			want: agentdb.MemorySearchQuery{
				Project:       "acme",
				LabelSelector: "kind=rolling-summary,worker=email-answerer",
				Query:         "refund policy",
				Limit:         25,
			},
		},
		{
			name: "a project in the query is ignored, not honoured",
			path: "/agent/memories?project=other",
			want: agentdb.MemorySearchQuery{Project: "acme"},
		},
		{
			name: "a junk limit degrades to the store's default, not an error",
			path: "/agent/memories?limit=-3",
			want: agentdb.MemorySearchQuery{Project: "acme"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeMemories{}
			h := newMemoryHandlers(t, store, identityFor("acme"))
			if rec := do(h, http.MethodGet, tc.path, ""); rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if store.got.Project != tc.want.Project ||
				store.got.LabelSelector != tc.want.LabelSelector ||
				store.got.Query != tc.want.Query ||
				store.got.Limit != tc.want.Limit {
				t.Fatalf("query:\n got %+v\nwant %+v", store.got, tc.want)
			}
			if store.got.QueryEmbedding != nil {
				t.Fatalf("no embedder is wired, so the semantic leg must be off: %v", store.got.QueryEmbedding)
			}
		})
	}
}

// The embedder is consulted only when there is text to embed, and a nil return
// (a degraded provider) is passed through as "no semantic leg" rather than an
// error — §7.6.5: the result shape never changes.
func TestListMemories_Embedder(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		vec      []float32
		wantCall string
		wantVec  bool
	}{
		{name: "text is embedded", path: "/agent/memories?query=refunds", vec: []float32{0.5},
			wantCall: "refunds", wantVec: true},
		{name: "no text, no embedding call", path: "/agent/memories?selector=kind%3Dnote", vec: []float32{0.5}},
		{name: "a degraded provider costs the leg, not the answer",
			path: "/agent/memories?query=refunds", vec: nil, wantCall: "refunds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			store := &fakeMemories{}
			h := newHandlers(t, Config{
				Runner: stubRunner{}, Store: stubStore{}, Identity: identityFor("acme"),
				Memories: store,
				MemoryEmbedder: func(_ context.Context, text string) []float32 {
					seen = text
					return tc.vec
				},
			})
			if rec := do(h, http.MethodGet, tc.path, ""); rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if seen != tc.wantCall {
				t.Fatalf("embedder called with %q, want %q", seen, tc.wantCall)
			}
			if got := store.got.QueryEmbedding != nil; got != tc.wantVec {
				t.Fatalf("embedding present=%v, want %v", got, tc.wantVec)
			}
		})
	}
}

// The response is exactly {"memories":[…]}, carrying labels and provenance —
// §7.3 says provenance is part of the answer, not an extra.
func TestListMemories_ResponseShape(t *testing.T) {
	store := &fakeMemories{out: []*agentdb.MemorySearchResult{{
		ID:               "mem-1",
		Labels:           agentdb.LabelSet{"kind": "rolling-summary", "worker": "email-answerer"},
		Snippet:          "customers ask about refunds first",
		Score:            0.0163,
		CreatedByWorker:  "email-answerer",
		CreatedBySession: "sess-1",
		CreatedAt:        1789000000123,
	}}}
	h := newMemoryHandlers(t, store, identityFor("acme"))
	rec := do(h, http.MethodGet, "/agent/memories", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Memories []map[string]any `json:"memories"`
	}
	decodeInto(t, rec, &body)
	if len(body.Memories) != 1 {
		t.Fatalf("want 1 record, got %d", len(body.Memories))
	}
	first := body.Memories[0]
	for _, k := range []string{"id", "labels", "snippet", "score", "created_by_worker", "created_by_session", "created_at"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("record is missing %q (the pinned MemorySearchResult shape): %+v", first, k)
		}
	}
	if first["created_at"].(float64) != 1789000000123 {
		t.Fatalf("created_at must be the raw unix MILLISECONDS: %v", first["created_at"])
	}

	empty := &fakeMemories{}
	h = newMemoryHandlers(t, empty, identityFor("acme"))
	rec = do(h, http.MethodGet, "/agent/memories", "")
	if got := rec.Body.String(); got != "{\"memories\":[]}\n" {
		t.Fatalf("an empty result must be [], not null: %s", got)
	}
}

// Errors keep their posture: a bad selector is the caller's fault and carries the
// parser's own words; a non-Postgres store is a deployment fact, not a bad
// request, so it answers 501 like POST /agent/project-token does.
func TestListMemories_Errors(t *testing.T) {
	t.Run("400 with the parser's message on a bad selector", func(t *testing.T) {
		store := &fakeMemories{err: errors.New("agentdb: memory search selector: unexpected token \"~\"")}
		h := newMemoryHandlers(t, store, identityFor("acme"))
		rec := do(h, http.MethodGet, "/agent/memories?selector=k~v", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		if got := rec.Body.String(); got == "" || !strings.Contains(got, "unexpected token") {
			t.Fatalf("the parser's own message must survive: %q", got)
		}
	})

	t.Run("501 on a non-Postgres store", func(t *testing.T) {
		store := &fakeMemories{err: agentdb.ErrMemoryRequiresPostgres}
		h := newMemoryHandlers(t, store, identityFor("acme"))
		if rec := do(h, http.MethodGet, "/agent/memories", ""); rec.Code != http.StatusNotImplemented {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
	})
}

// Auth posture: 401 without an identity, 403 for a token carrying no project,
// 501 when the host wired no store, and no method other than GET.
func TestListMemories_AuthAndAvailability(t *testing.T) {
	t.Run("401 without identity", func(t *testing.T) {
		store := &fakeMemories{}
		h := newMemoryHandlers(t, store, func(*http.Request) (Identity, error) {
			return Identity{}, http.ErrNoCookie
		})
		if rec := do(h, http.MethodGet, "/agent/memories", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", rec.Code)
		}
		if store.call != 0 {
			t.Fatal("an unauthenticated request must not reach the store")
		}
	})

	t.Run("403 with no project claim", func(t *testing.T) {
		store := &fakeMemories{}
		h := newMemoryHandlers(t, store, identityFor(""))
		if rec := do(h, http.MethodGet, "/agent/memories", ""); rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d", rec.Code)
		}
		if store.call != 0 {
			t.Fatal("a projectless token must not reach the store")
		}
	})

	t.Run("501 with no store", func(t *testing.T) {
		h := newMemoryHandlers(t, nil, identityFor("acme"))
		if rec := do(h, http.MethodGet, "/agent/memories", ""); rec.Code != http.StatusNotImplemented {
			t.Fatalf("status=%d", rec.Code)
		}
	})

	t.Run("write methods are not routed", func(t *testing.T) {
		// Memories are append-only and written by workers through their tools
		// (§7.1): there is no POST here, by design.
		h := newMemoryHandlers(t, &fakeMemories{}, identityFor("acme"))
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			if rec := do(h, m, "/agent/memories", `{}`); rec.Code == http.StatusOK {
				t.Fatalf("%s must not be served", m)
			}
		}
	})
}

// The route is where the UI expects it, and it is mounted by Mux().
func TestListMemories_Endpoint(t *testing.T) {
	if DefaultEndpoints.ListMemories != "GET /agent/memories" {
		t.Fatalf("route moved: %q", DefaultEndpoints.ListMemories)
	}
}

// PROJECT ISOLATION and the live relevance contract, against the real store: two
// projects append their own memories and neither route can see the other's,
// whatever the query says.
func TestListMemories_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	mine, theirs := "memread-mine", "memread-theirs"
	t.Cleanup(func() {
		for _, p := range []string{mine, theirs} {
			_ = store.DB().Exec("DELETE FROM memories WHERE project = ?", p).Error
		}
	})

	seed := func(project, content string, labels agentdb.LabelSet) {
		t.Helper()
		if _, err := store.CreateMemory(context.Background(), &agentdb.Memory{
			Project: project, Labels: labels, Content: content,
			CreatedByWorker: "email-answerer", CreatedBySession: "sess-1",
		}, nil); err != nil {
			t.Fatalf("seed %s: %v", project, err)
		}
	}
	seed(mine, "customers ask about refunds before anything else", agentdb.LabelSet{"kind": "rolling-summary", "worker": "email-answerer"})
	seed(mine, "the office is closed on Fridays", agentdb.LabelSet{"kind": "note"})
	seed(theirs, "their secret refunds policy", agentdb.LabelSet{"kind": "rolling-summary"})

	read := func(project, query string) []*agentdb.MemorySearchResult {
		t.Helper()
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{},
			Identity: identityFor(project), AgentDB: store,
		})
		rec := do(h, http.MethodGet, "/agent/memories"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("read %s%s: status=%d body=%s", project, query, rec.Code, rec.Body)
		}
		var body struct {
			Memories []*agentdb.MemorySearchResult `json:"memories"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Memories
	}

	got := read(mine, "")
	if len(got) != 2 {
		t.Fatalf("a project must see exactly its own memories, got %d: %+v", len(got), got)
	}
	// No query text ⇒ newest first (§7.6.2).
	if got[0].Snippet != "the office is closed on Fridays" {
		t.Fatalf("no query text must answer newest-first: %+v", got)
	}
	if got[0].CreatedByWorker != "email-answerer" || got[0].CreatedBySession != "sess-1" {
		t.Fatalf("provenance must ride on the row: %+v", got[0])
	}

	// The selector filters within the project…
	if got := read(mine, "?selector=kind%3Dnote"); len(got) != 1 || got[0].Labels["kind"] != "note" {
		t.Fatalf("selector filter is wrong: %+v", got)
	}
	// …and cannot be used to reach across it, nor can a project= parameter.
	if got := read(mine, "?query=refunds&project="+theirs); len(got) != 1 ||
		got[0].Snippet != "customers ask about refunds before anything else" {
		t.Fatalf("a project= query must never cross the boundary, got %+v", got)
	}
	if got := read(theirs, ""); len(got) != 1 || got[0].Snippet != "their secret refunds policy" {
		t.Fatalf("the other project's own memories are wrong: %+v", got)
	}

	// A malformed selector is the caller's fault, reported with the parser's words.
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{},
		Identity: identityFor(mine), AgentDB: store,
	})
	if rec := do(h, http.MethodGet, "/agent/memories?selector=kind%20in%20(a", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad selector must be 400: status=%d body=%s", rec.Code, rec.Body)
	}
}
