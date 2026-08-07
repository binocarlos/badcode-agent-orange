package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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

	// The single-record legs (T18). gotProject/gotID/gotSelector record what
	// crossed the seam — the project is the whole tenancy story on both routes,
	// so it is asserted, never assumed.
	one         *agentdb.Memory
	oneErr      error
	gotProject  string
	gotID       string
	gotSelector string
}

func (f *fakeMemories) SearchMemories(_ context.Context, q *agentdb.MemorySearchQuery) ([]*agentdb.MemorySearchResult, error) {
	f.call++
	f.got = *q
	return f.out, f.err
}

func (f *fakeMemories) GetMemory(_ context.Context, project, id string) (*agentdb.Memory, error) {
	f.call++
	f.gotProject, f.gotID = project, id
	return f.one, f.oneErr
}

func (f *fakeMemories) NewestMemory(_ context.Context, project, selector string) (*agentdb.Memory, error) {
	f.call++
	f.gotProject, f.gotSelector = project, selector
	return f.one, f.oneErr
}

// bigMemory is a body comfortably past the 500-byte snippet cut
// (agentdb/memories.go:35), because a test that reads back 200 bytes untruncated
// proves nothing at all about truncation.
func bigMemory() *agentdb.Memory {
	return &agentdb.Memory{
		ID:               "mem-1",
		Project:          "acme",
		Labels:           agentdb.LabelSet{"name": "hypothesis-a", "kind": "state"},
		Content:          strings.Repeat("the AI bubble bursts and liquidity floods in. ", 40), // 1840 bytes
		CreatedByWorker:  "reviewer-a",
		CreatedBySession: "sess-1",
		CreatedAt:        1789000000123,
	}
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

// ---------------------------------------------------------------------------
// T18 — the full-content read routes
// ---------------------------------------------------------------------------

// THE reason these routes exist: GET /agent/memories hands back a 500-byte
// snippet, so an embedding app rendering state from memory needs a read that
// does not stop mid-sentence.
func TestGetMemory_ReturnsFullContentUntruncated(t *testing.T) {
	mem := bigMemory()
	if len(mem.Content) <= 500 {
		t.Fatalf("this test is only meaningful past the snippet cut, got %d bytes", len(mem.Content))
	}
	store := &fakeMemories{one: mem}
	h := newMemoryHandlers(t, store, identityFor("acme"))

	rec := do(h, http.MethodGet, "/agent/memories/mem-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body map[string]any
	decodeInto(t, rec, &body)
	if body["content"] != mem.Content {
		t.Fatalf("content must come back whole: got %d bytes, want %d", len(body["content"].(string)), len(mem.Content))
	}
	if _, ok := body["snippet"]; ok {
		t.Fatal("this route answers with content; a snippet key would invite a client to read the short one")
	}
	// Provenance rides on the record here exactly as it does on a search hit
	// (§7.3): the caller must be able to say which worker wrote this, and when.
	for _, k := range []string{"id", "labels", "content", "created_by_worker", "created_by_session", "created_at"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("record is missing %q: %+v", k, body)
		}
	}
	if body["created_at"].(float64) != 1789000000123 {
		t.Fatalf("created_at must be the raw unix MILLISECONDS: %v", body["created_at"])
	}
	// The project is the token's, never the path's — the store call is where
	// tenancy is decided, so it is the thing asserted.
	if store.gotProject != "acme" || store.gotID != "mem-1" {
		t.Fatalf("store call: project=%q id=%q", store.gotProject, store.gotID)
	}
}

// A memory belonging to another project is ErrMemoryNotFound from the store
// (agentdb/memories.go:152-172), and the route must not dress that up as
// anything an attacker can tell apart from a typo — mirroring the MCP tool's
// posture at cmd/agentd/mcp_memory.go:357-365.
func TestGetMemory_CrossProjectIsIndistinguishableFromAbsent(t *testing.T) {
	body := func(id string) (int, string) {
		t.Helper()
		// One store, one error: the point is that "exists elsewhere" and "never
		// existed" reach the handler identically and must leave identically.
		store := &fakeMemories{oneErr: agentdb.ErrMemoryNotFound}
		h := newMemoryHandlers(t, store, identityFor("acme"))
		rec := do(h, http.MethodGet, "/agent/memories/"+id, "")
		return rec.Code, rec.Body.String()
	}
	foreignCode, foreignBody := body("mem-owned-by-theirs")
	absentCode, absentBody := body("mem-never-existed")
	if foreignCode != http.StatusNotFound || absentCode != http.StatusNotFound {
		t.Fatalf("both must be 404, got %d and %d", foreignCode, absentCode)
	}
	if foreignBody != absentBody {
		t.Fatalf("the two answers must be byte-identical:\n %q\n %q", foreignBody, absentBody)
	}
}

// memory_current's semantics, over HTTP: the newest memory labelled name=<n>.
func TestCurrentMemory(t *testing.T) {
	t.Run("the selector is exactly name=<n>", func(t *testing.T) {
		store := &fakeMemories{one: bigMemory()}
		h := newMemoryHandlers(t, store, identityFor("acme"))
		rec := do(h, http.MethodGet, "/agent/memories/current?name=hypothesis-a", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		if store.gotProject != "acme" || store.gotSelector != "name=hypothesis-a" {
			t.Fatalf("store call: project=%q selector=%q", store.gotProject, store.gotSelector)
		}
		var body map[string]any
		decodeInto(t, rec, &body)
		if body["content"] != bigMemory().Content {
			t.Fatal("current must answer with the whole body, like memory_current does")
		}
	})

	t.Run("nothing written under that name is a 404, not an error", func(t *testing.T) {
		store := &fakeMemories{oneErr: agentdb.ErrMemoryNotFound}
		h := newMemoryHandlers(t, store, identityFor("acme"))
		if rec := do(h, http.MethodGet, "/agent/memories/current?name=nothing-yet", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("a missing name is the caller's mistake", func(t *testing.T) {
		store := &fakeMemories{one: bigMemory()}
		h := newMemoryHandlers(t, store, identityFor("acme"))
		if rec := do(h, http.MethodGet, "/agent/memories/current", ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		if store.call != 0 {
			t.Fatal("an empty name must not reach the store: name= matches every unlabelled row")
		}
	})

	// The name is interpolated into selector text, so it is validated as a label
	// value first — the same guard memory_current applies (mcp_memory.go:384-389).
	// Without it a comma or '!' smuggles a second term into the query.
	t.Run("a name that is not a legal label value cannot smuggle a selector", func(t *testing.T) {
		for _, bad := range []string{"a,kind!=secret", "a=b", "hypothesis a", "a)"} {
			store := &fakeMemories{one: bigMemory()}
			h := newMemoryHandlers(t, store, identityFor("acme"))
			rec := do(h, http.MethodGet, "/agent/memories/current?name="+url.QueryEscape(bad), "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("name %q: status=%d body=%s", bad, rec.Code, rec.Body)
			}
			if store.call != 0 {
				t.Fatalf("name %q reached the store as selector %q", bad, store.gotSelector)
			}
		}
	})

	// Route precedence, not decoration: `current` is a literal segment and must
	// win over the {id} wildcard, or the by-id handler answers this request with
	// a 404 for a memory called "current".
	t.Run("current is not swallowed by the by-id wildcard", func(t *testing.T) {
		store := &fakeMemories{one: bigMemory()}
		h := newMemoryHandlers(t, store, identityFor("acme"))
		if rec := do(h, http.MethodGet, "/agent/memories/current?name=hypothesis-a", ""); rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		if store.gotID != "" {
			t.Fatalf("the by-id handler ran instead, with id=%q", store.gotID)
		}
	})
}

// Both routes carry the same auth/availability posture as GET /agent/memories,
// and neither grows a write counterpart: memories are append-only (§7.1).
func TestMemoryReadRoutes_AuthAndAvailability(t *testing.T) {
	paths := []string{"/agent/memories/mem-1", "/agent/memories/current?name=hypothesis-a"}

	for _, path := range paths {
		t.Run("401 without identity "+path, func(t *testing.T) {
			store := &fakeMemories{one: bigMemory()}
			h := newMemoryHandlers(t, store, func(*http.Request) (Identity, error) {
				return Identity{}, http.ErrNoCookie
			})
			if rec := do(h, http.MethodGet, path, ""); rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d", rec.Code)
			}
			if store.call != 0 {
				t.Fatal("an unauthenticated request must not reach the store")
			}
		})

		t.Run("403 with no project claim "+path, func(t *testing.T) {
			store := &fakeMemories{one: bigMemory()}
			h := newMemoryHandlers(t, store, identityFor(""))
			if rec := do(h, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d", rec.Code)
			}
			if store.call != 0 {
				t.Fatal("a projectless token has no namespace to read in")
			}
		})

		t.Run("501 with no store "+path, func(t *testing.T) {
			h := newMemoryHandlers(t, nil, identityFor("acme"))
			if rec := do(h, http.MethodGet, path, ""); rec.Code != http.StatusNotImplemented {
				t.Fatalf("status=%d", rec.Code)
			}
		})

		t.Run("501 on a non-Postgres store "+path, func(t *testing.T) {
			store := &fakeMemories{oneErr: agentdb.ErrMemoryRequiresPostgres}
			h := newMemoryHandlers(t, store, identityFor("acme"))
			if rec := do(h, http.MethodGet, path, ""); rec.Code != http.StatusNotImplemented {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
		})

		t.Run("500 on a store outage "+path, func(t *testing.T) {
			// A database that is down is not a memory that is missing: answering
			// 404 sends an operator hunting for a row that is sitting right there.
			store := &fakeMemories{oneErr: errors.New("agentdb: get memory: connection refused")}
			h := newMemoryHandlers(t, store, identityFor("acme"))
			if rec := do(h, http.MethodGet, path, ""); rec.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
		})

		t.Run("no write methods "+path, func(t *testing.T) {
			h := newMemoryHandlers(t, &fakeMemories{one: bigMemory()}, identityFor("acme"))
			for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
				if rec := do(h, m, path, `{}`); rec.Code == http.StatusOK {
					t.Fatalf("%s must not be served", m)
				}
			}
		})
	}
}

// The routes are where docs/19-embedding.md will say they are, and Mux mounts
// them.
func TestMemoryReadRoutes_Endpoints(t *testing.T) {
	if DefaultEndpoints.GetMemory != "GET /agent/memories/{id}" {
		t.Fatalf("route moved: %q", DefaultEndpoints.GetMemory)
	}
	if DefaultEndpoints.CurrentMemory != "GET /agent/memories/current" {
		t.Fatalf("route moved: %q", DefaultEndpoints.CurrentMemory)
	}
}

// The proof that matters, against the real store: the SAME memory read through
// the search route is cut at 500 bytes and through the new routes is not.
func TestMemoryReadRoutes_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	mine, theirs := "memfull-mine", "memfull-theirs"
	t.Cleanup(func() {
		for _, p := range []string{mine, theirs} {
			_ = store.DB().Exec("DELETE FROM memories WHERE project = ?", p).Error
		}
	})

	ctx := context.Background()
	long := strings.Repeat("the AI bubble bursts and liquidity floods in. ", 40)
	seed := func(project, content string, labels agentdb.LabelSet) *agentdb.Memory {
		t.Helper()
		m, err := store.CreateMemory(ctx, &agentdb.Memory{
			Project: project, Labels: labels, Content: content,
			CreatedByWorker: "reviewer-a", CreatedBySession: "sess-1",
		}, nil)
		if err != nil {
			t.Fatalf("seed %s: %v", project, err)
		}
		return m
	}
	stale := seed(mine, "an older reading of the hypothesis", agentdb.LabelSet{"name": "hypothesis-a"})
	current := seed(mine, long, agentdb.LabelSet{"name": "hypothesis-a"})
	foreign := seed(theirs, "their private hypothesis", agentdb.LabelSet{"name": "hypothesis-a"})

	h := func(project string) *Handlers {
		return newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{},
			Identity: identityFor(project), AgentDB: store,
		})
	}

	// 1. The search route still truncates — this is the problem being solved, so
	//    it is asserted rather than assumed.
	rec := do(h(mine), http.MethodGet, "/agent/memories?selector=name%3Dhypothesis-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("search: status=%d body=%s", rec.Code, rec.Body)
	}
	var search struct {
		Memories []*agentdb.MemorySearchResult `json:"memories"`
	}
	decodeInto(t, rec, &search)
	if len(search.Memories) != 2 || len(search.Memories[0].Snippet) != 500 {
		t.Fatalf("expected the newest of two hits cut to 500 bytes, got %d hits, first %d bytes",
			len(search.Memories), len(search.Memories[0].Snippet))
	}

	// 2. …and the by-id route does not.
	rec = do(h(mine), http.MethodGet, "/agent/memories/"+current.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("by id: status=%d body=%s", rec.Code, rec.Body)
	}
	var got struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	decodeInto(t, rec, &got)
	if got.Content != long {
		t.Fatalf("by id returned %d bytes, want the whole %d", len(got.Content), len(long))
	}

	// 3. current takes the newest of the two under that name, in full.
	rec = do(h(mine), http.MethodGet, "/agent/memories/current?name=hypothesis-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("current: status=%d body=%s", rec.Code, rec.Body)
	}
	decodeInto(t, rec, &got)
	if got.ID != current.ID || got.Content != long {
		t.Fatalf("current must be the newest match in full: id=%s (want %s, older is %s), %d bytes",
			got.ID, current.ID, stale.ID, len(got.Content))
	}

	// 4. Tenancy: the other project's memory is not reachable by id, and its own
	//    name= reading is its own. Both projects used the SAME name, which is the
	//    case that would break a route scoping on the label instead of the row.
	if rec := do(h(mine), http.MethodGet, "/agent/memories/"+foreign.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a foreign id must be 404: status=%d body=%s", rec.Code, rec.Body)
	}
	rec = do(h(theirs), http.MethodGet, "/agent/memories/current?name=hypothesis-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("theirs: status=%d body=%s", rec.Code, rec.Body)
	}
	decodeInto(t, rec, &got)
	if got.ID != foreign.ID {
		t.Fatalf("each project reads its own name=hypothesis-a, got %s", got.ID)
	}

	// 5. A name nobody has written under is absent, not an error.
	if rec := do(h(mine), http.MethodGet, "/agent/memories/current?name=never-written", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}
