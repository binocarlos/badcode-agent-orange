package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/binocarlos/badcode-agent-orange/extension/embedding"
)

// ---------------------------------------------------------------------------
// D3 — the memory MCP tools (§7.3).
//
// The invariants worth a test, in order of how much they would cost to get
// wrong: a session cannot reach another project's memories; provenance and the
// session permalink are on every result; the write path refuses rather than
// storing an unembeddable row; the read path degrades rather than failing; and
// there is no way to update or delete anything.
// ---------------------------------------------------------------------------

// fakeMemoryStore records what the tools asked for — the project argument is
// the whole tenancy story, so the test needs to see it.
type fakeMemoryStore struct {
	byID     map[string]*agentdb.Memory
	created  []*agentdb.Memory
	createdV [][]float32
	searches []*agentdb.MemorySearchQuery
	newest   []string // "project|selector"
	hits     []*agentdb.MemorySearchResult
	newestBy map[string]*agentdb.Memory
	err      error

	// noVectorColumn mirrors a Postgres where migration 022 could not create
	// the pgvector extension: an embedded create is REFUSED, never downgraded.
	noVectorColumn bool
	// reportsEmbedded, when non-nil, is what the fake claims it stored,
	// independent of the vector it was handed. RD3 is precisely the gap
	// between those two numbers, so the test needs to be able to open it.
	reportsEmbedded *bool
}

func newFakeMemoryStore() *fakeMemoryStore {
	return &fakeMemoryStore{byID: map[string]*agentdb.Memory{}, newestBy: map[string]*agentdb.Memory{}}
}

func (f *fakeMemoryStore) CreateMemory(_ context.Context, m *agentdb.Memory, emb []float32) (*agentdb.Memory, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if emb != nil && f.noVectorColumn {
		return nil, false, agentdb.ErrMemoryEmbeddingUnstorable
	}
	embedded := emb != nil
	if f.reportsEmbedded != nil {
		embedded = *f.reportsEmbedded
	}
	stored := *m
	if stored.ID == "" {
		stored.ID = fmt.Sprintf("mem-%d", len(f.created)+1)
	}
	stored.CreatedAt = 1700000000000
	f.created = append(f.created, &stored)
	f.createdV = append(f.createdV, emb)
	f.byID[stored.Project+"|"+stored.ID] = &stored
	return &stored, embedded, nil
}

func (f *fakeMemoryStore) GetMemory(_ context.Context, project, id string) (*agentdb.Memory, error) {
	if f.err != nil {
		return nil, f.err
	}
	if m, ok := f.byID[project+"|"+id]; ok {
		return m, nil
	}
	return nil, agentdb.ErrMemoryNotFound
}

func (f *fakeMemoryStore) SearchMemories(_ context.Context, q *agentdb.MemorySearchQuery) ([]*agentdb.MemorySearchResult, error) {
	f.searches = append(f.searches, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

func (f *fakeMemoryStore) NewestMemory(_ context.Context, project, selector string) (*agentdb.Memory, error) {
	f.newest = append(f.newest, project+"|"+selector)
	if f.err != nil {
		return nil, f.err
	}
	if m, ok := f.newestBy[project+"|"+selector]; ok {
		return m, nil
	}
	return nil, agentdb.ErrMemoryNotFound
}

// failingEmbedder is a configured provider that is down — the case that
// separates the strict write path from the degrading read path.
type failingEmbedder struct{ calls int }

func (f *failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	f.calls++
	return nil, errors.New("embedding endpoint unreachable")
}

func testPermalinker() permalinker { return permalinker{base: "https://orange.example.com"} }

func testMemoryTools(store memoryStore, emb embedding.Provider) *memoryTools {
	return newMemoryTools(store, emb, testPermalinker())
}

// callTool invokes one registered tool directly and returns the decoded result.
func callTool(t *testing.T, tools *memoryTools, name string, caller mcpCaller, args any) (map[string]any, error) {
	t.Helper()
	var tool *mcpTool
	for _, tt := range tools.tools() {
		if tt.Name == name {
			tool = tt
		}
	}
	if tool == nil {
		t.Fatalf("no tool %q", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := tool.Handler(context.Background(), caller, raw)
	if err != nil {
		return nil, err
	}
	// Round-trip through JSON: what the model actually sees, tags and all.
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out, nil
}

func testCaller() mcpCaller {
	// Identified: authenticate sets it whenever the session row reads, which is
	// every real caller. Leaving it false here would make every mutating tool
	// refuse for the wrong reason.
	return mcpCaller{Project: "acme", SessionID: "sess-1", Worker: "email-answerer", Identified: true}
}

// TestMemoryToolsSurface pins the whole surface: create / search / get /
// current, and NOTHING that updates or deletes (§7.1, §7.3).
func TestMemoryToolsSurface(t *testing.T) {
	tools := testMemoryTools(newFakeMemoryStore(), nil)

	var names []string
	for _, tool := range tools.tools() {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Fatalf("tool %q has no description — descriptions are prompt, not documentation", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", tool.Name)
		}
	}
	want := []string{"memory_create", "memory_search", "memory_get", "memory_current"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool surface = %v, want exactly %v", names, want)
	}
	for _, name := range names {
		for _, forbidden := range []string{"update", "delete", "remove", "edit"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("tool %q: memories are immutable (§7.1)", name)
			}
		}
	}

	// The D1 finding: RRF has no relevance floor, so the tail of any result set
	// is filled with real-looking scores. The search description must say so.
	var searchDesc string
	for _, tool := range tools.tools() {
		if tool.Name == "memory_search" {
			searchDesc = tool.Description
		}
	}
	for _, phrase := range []string{"low score", "nothing good", "threshold"} {
		if !strings.Contains(strings.ToLower(searchDesc), phrase) {
			t.Fatalf("memory_search description must warn that a low fused score means nothing good; missing %q", phrase)
		}
	}
}

// TestMemoryToolsCreate: provenance from the caller (never from an argument),
// labels validated up front, and the stored row echoed back (§9 read-back).
func TestMemoryToolsCreate(t *testing.T) {
	store := newFakeMemoryStore()
	tools := testMemoryTools(store, embedding.NewMock())

	res, err := callTool(t, tools, "memory_create", testCaller(), map[string]any{
		"content": "The refund window is 30 days.",
		"labels":  map[string]string{"kind": "fact", "worker": "email-answerer"},
	})
	if err != nil {
		t.Fatalf("memory_create: %v", err)
	}

	if len(store.created) != 1 {
		t.Fatalf("created %d memories, want 1", len(store.created))
	}
	got := store.created[0]
	if got.Project != "acme" {
		t.Fatalf("project = %q, want the caller's project", got.Project)
	}
	if got.CreatedByWorker != "email-answerer" || got.CreatedBySession != "sess-1" {
		t.Fatalf("provenance = %q/%q, want the caller's worker and session", got.CreatedByWorker, got.CreatedBySession)
	}
	if store.createdV[0] == nil || len(store.createdV[0]) != embedding.Dim {
		t.Fatalf("a configured provider must produce a %d-dim embedding on the write path", embedding.Dim)
	}
	if res["session_url"] != "https://orange.example.com/p/acme/s/sess-1" {
		t.Fatalf("session_url = %v", res["session_url"])
	}
	if res["embedded"] != true {
		t.Fatalf("embedded = %v, want true", res["embedded"])
	}
	if res["id"] != got.ID {
		t.Fatalf("result must echo the stored row: id %v vs %v", res["id"], got.ID)
	}

	t.Run("labels are identifiers, and bad ones fail loudly", func(t *testing.T) {
		_, err := callTool(t, tools, "memory_create", testCaller(), map[string]any{
			"content": "x",
			"labels":  map[string]string{"subject": "Re: your refund, please help!"},
		})
		if err == nil || !strings.Contains(err.Error(), "labels") {
			t.Fatalf("want a labels complaint, got %v", err)
		}
		if len(store.created) != 1 {
			t.Fatalf("a rejected create must write nothing")
		}
	})

	t.Run("blank content is refused", func(t *testing.T) {
		if _, err := callTool(t, tools, "memory_create", testCaller(), map[string]any{"content": "  \n "}); err == nil {
			t.Fatalf("want an error for blank content")
		}
	})

	t.Run("an unknown argument fails rather than being ignored", func(t *testing.T) {
		_, err := callTool(t, tools, "memory_create", testCaller(), map[string]any{
			"content": "x", "lables": map[string]string{"kind": "fact"},
		})
		if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
			t.Fatalf("a typo'd argument must fail loudly, got %v", err)
		}
	})
}

// TestMemoryToolsCreateReportsTheStoresAnswer is RD3 at the tool seam.
//
// `embedded` used to be `vec != nil` — the EMBEDDER's success, echoed as if it
// were the row's state, two lines below a comment stating the opposite rule
// ("CreateMemory returns the row as the database holds it… that is what is
// echoed"). The two only differ when the store did something other than what
// the caller assumed, which is exactly the case the field exists to report.
func TestMemoryToolsCreateReportsTheStoresAnswer(t *testing.T) {
	no := false
	store := newFakeMemoryStore()
	store.reportsEmbedded = &no // the embedder produced a vector; the row has none
	tools := testMemoryTools(store, embedding.NewMock())

	res, err := callTool(t, tools, "memory_create", testCaller(), map[string]any{
		"content": "The refund window is 30 days.",
	})
	if err != nil {
		t.Fatalf("memory_create: %v", err)
	}
	if store.createdV[0] == nil {
		t.Fatalf("the test is vacuous unless a vector actually reached the store")
	}
	if res["embedded"] != false {
		t.Fatalf("embedded = %v, want false: the tool must report what the STORE wrote, "+
			"not that the embedder returned a vector", res["embedded"])
	}
}

// TestMemoryToolsCreateRefusesWhenTheVectorCannotBeStored: on a Postgres
// without pgvector the store refuses (memories are append-only, so a row
// written without its vector is unembeddable forever) and the model must be
// told, in a message it can act on.
func TestMemoryToolsCreateRefusesWhenTheVectorCannotBeStored(t *testing.T) {
	store := newFakeMemoryStore()
	store.noVectorColumn = true
	tools := testMemoryTools(store, embedding.NewMock())

	res, err := callTool(t, tools, "memory_create", testCaller(), map[string]any{
		"content": "The refund window is 30 days.",
	})
	if err == nil {
		t.Fatalf("want a refusal, got a result: %v", res)
	}
	if !strings.Contains(err.Error(), "content_embedding") {
		t.Fatalf("the refusal must name what is missing, got %v", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("a refused create must write nothing, wrote %d", len(store.created))
	}
}

// TestMemoryToolsCreateEmbedFailureIsFatal is the D2 asymmetry on the write
// side: a NULL embedding written during an outage is permanently invisible to
// semantic search, so the create fails instead.
func TestMemoryToolsCreateEmbedFailureIsFatal(t *testing.T) {
	store := newFakeMemoryStore()
	emb := &failingEmbedder{}
	tools := testMemoryTools(store, emb)

	_, err := callTool(t, tools, "memory_create", testCaller(), map[string]any{"content": "something worth keeping"})
	if err == nil {
		t.Fatalf("a failing embedder must fail the create")
	}
	if !strings.Contains(err.Error(), "NOT stored") {
		t.Fatalf("the message must tell the model nothing was stored, got %q", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("nothing may be written when the embedding failed, wrote %d", len(store.created))
	}

	t.Run("no provider configured is not a failure", func(t *testing.T) {
		plain := testMemoryTools(store, nil)
		res, err := callTool(t, plain, "memory_current", testCaller(), map[string]any{"name": "x"})
		if err != nil {
			t.Fatalf("nil provider must be a supported deployment: %v", err)
		}
		_ = res
		if _, err := callTool(t, plain, "memory_create", testCaller(), map[string]any{"content": "no embeddings here"}); err != nil {
			t.Fatalf("create with no provider: %v", err)
		}
		if store.createdV[len(store.createdV)-1] != nil {
			t.Fatalf("no provider ⇒ NULL embedding column")
		}
	})
}

// TestMemoryToolsSearch: the project binds in code, the selector and query pass
// through untouched, and every hit carries provenance + a permalink.
func TestMemoryToolsSearch(t *testing.T) {
	store := newFakeMemoryStore()
	store.hits = []*agentdb.MemorySearchResult{{
		ID: "m1", Labels: agentdb.LabelSet{"kind": "lesson"}, Snippet: "never promise a date",
		Score: 0.0163, CreatedByWorker: "reviewer", CreatedBySession: "sess-9", CreatedAt: 1700000000000,
	}}
	tools := testMemoryTools(store, embedding.NewMock())

	res, err := callTool(t, tools, "memory_search", testCaller(), map[string]any{
		"label_selector": "kind=lesson", "query": "refund promises", "limit": 5,
	})
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	q := store.searches[0]
	if q.Project != "acme" {
		t.Fatalf("search project = %q — the project is never a tool argument", q.Project)
	}
	if q.LabelSelector != "kind=lesson" || q.Query != "refund promises" || q.Limit != 5 {
		t.Fatalf("search query = %#v", q)
	}
	if q.QueryEmbedding == nil {
		t.Fatalf("a configured provider must embed the query text")
	}

	results, _ := res["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %#v", res["results"])
	}
	hit, _ := results[0].(map[string]any)
	if hit["session_url"] != "https://orange.example.com/p/acme/s/sess-9" {
		t.Fatalf("session_url = %v — provenance is part of the result (§7.3)", hit["session_url"])
	}
	if hit["created_by_worker"] != "reviewer" || hit["snippet"] != "never promise a date" {
		t.Fatalf("hit = %#v", hit)
	}
	note, _ := res["note"].(string)
	if !strings.Contains(note, "low score") {
		t.Fatalf("every result set must repeat the no-relevance-floor warning, got %q", note)
	}

	t.Run("no query text means no embedding call at all", func(t *testing.T) {
		store.searches = nil
		if _, err := callTool(t, tools, "memory_search", testCaller(), map[string]any{"label_selector": "kind=lesson"}); err != nil {
			t.Fatalf("bare selector search: %v", err)
		}
		if store.searches[0].QueryEmbedding != nil {
			t.Fatalf("a bare selector is a recency question — nothing to embed")
		}
	})

	t.Run("a failing embedder degrades the query, it does not fail it", func(t *testing.T) {
		emb := &failingEmbedder{}
		degraded := testMemoryTools(store, emb)
		store.searches = nil
		if _, err := callTool(t, degraded, "memory_search", testCaller(), map[string]any{"query": "refunds"}); err != nil {
			t.Fatalf("read path must degrade, not fail: %v", err)
		}
		if emb.calls == 0 {
			t.Fatalf("the provider should have been tried")
		}
		if store.searches[0].QueryEmbedding != nil {
			t.Fatalf("a failed embedding must leave the semantic leg off, not send a bad vector")
		}
		if store.searches[0].Query != "refunds" {
			t.Fatalf("the keyword leg must still run: %#v", store.searches[0])
		}
	})

	t.Run("a negative limit is refused", func(t *testing.T) {
		if _, err := callTool(t, tools, "memory_search", testCaller(), map[string]any{"limit": -1}); err == nil {
			t.Fatalf("want an error for a negative limit")
		}
	})
}

// TestMemoryToolsGet: full content, and a memory of another project is simply
// not found — no existence leak.
func TestMemoryToolsGet(t *testing.T) {
	store := newFakeMemoryStore()
	tools := testMemoryTools(store, nil)
	if _, err := callTool(t, tools, "memory_create", testCaller(), map[string]any{"content": "the whole thing"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := store.created[0].ID

	res, err := callTool(t, tools, "memory_get", testCaller(), map[string]any{"id": id})
	if err != nil {
		t.Fatalf("memory_get: %v", err)
	}
	if res["content"] != "the whole thing" {
		t.Fatalf("get must return the whole content, got %v", res["content"])
	}

	other := mcpCaller{Project: "other-corp", SessionID: "sess-x"}
	if _, err := callTool(t, tools, "memory_get", other, map[string]any{"id": id}); err == nil {
		t.Fatalf("a memory of another project must not be readable")
	} else if !strings.Contains(err.Error(), "no memory with id") {
		t.Fatalf("cross-project get should look exactly like 'not found', got %q", err)
	}

	if _, err := callTool(t, tools, "memory_get", testCaller(), map[string]any{"id": "  "}); err == nil {
		t.Fatalf("a blank id must be refused")
	}
}

// TestMemoryCurrent covers the `name=` KV sugar (§7.1, §7.3): newest match, full
// content, absence reported as an answer, and no selector injection through the
// name.
func TestMemoryCurrent(t *testing.T) {
	store := newFakeMemoryStore()
	store.newestBy["acme|name=label-registry"] = &agentdb.Memory{
		ID: "m-current", Project: "acme", Content: strings.Repeat("registry ", 200),
		Labels:          agentdb.LabelSet{"name": "label-registry"},
		CreatedByWorker: "archivist", CreatedBySession: "sess-7", CreatedAt: 1700000000000,
	}
	tools := testMemoryTools(store, nil)

	res, err := callTool(t, tools, "memory_current", testCaller(), map[string]any{"name": "label-registry"})
	if err != nil {
		t.Fatalf("memory_current: %v", err)
	}
	if res["found"] != true || res["name"] != "label-registry" {
		t.Fatalf("result = %#v", res)
	}
	// memory_get semantics: the whole content, not a snippet.
	if res["content"] != strings.Repeat("registry ", 200) {
		t.Fatalf("memory_current must return full content, got %d chars", len(fmt.Sprint(res["content"])))
	}
	if res["session_url"] != "https://orange.example.com/p/acme/s/sess-7" {
		t.Fatalf("session_url = %v", res["session_url"])
	}
	// Exactly the sugar it claims to be: newest match of name=<name>, one query.
	if len(store.newest) != 1 || store.newest[0] != "acme|name=label-registry" {
		t.Fatalf("lookups = %v, want one newest-match query for name=label-registry", store.newest)
	}

	t.Run("nothing written yet is an answer, not an error", func(t *testing.T) {
		res, err := callTool(t, tools, "memory_current", testCaller(), map[string]any{"name": "never-written"})
		if err != nil {
			t.Fatalf("absence must not be an error: %v", err)
		}
		if res["found"] != false || res["name"] != "never-written" {
			t.Fatalf("result = %#v, want found:false", res)
		}
		if _, ok := res["content"]; ok {
			t.Fatalf("a not-found result must carry no content to mistake for a value: %#v", res)
		}
	})

	t.Run("the name cannot smuggle a second selector term", func(t *testing.T) {
		for _, bad := range []string{"x,kind=secret", "x=y", "!archived", "", "   "} {
			if _, err := callTool(t, tools, "memory_current", testCaller(), map[string]any{"name": bad}); err == nil {
				t.Fatalf("name %q must be rejected as a label value", bad)
			}
		}
	})

	t.Run("project scope comes from the caller", func(t *testing.T) {
		store.newest = nil
		other := mcpCaller{Project: "other-corp", SessionID: "s"}
		res, err := callTool(t, tools, "memory_current", other, map[string]any{"name": "label-registry"})
		if err != nil {
			t.Fatalf("memory_current: %v", err)
		}
		if res["found"] != false {
			t.Fatalf("another project must not see acme's named memory: %#v", res)
		}
		if store.newest[0] != "other-corp|name=label-registry" {
			t.Fatalf("lookup = %v", store.newest)
		}
	})
}

// ---------------------------------------------------------------------------
// Transport + auth
// ---------------------------------------------------------------------------

// fakeSessionLookup is the session row behind a token.
type fakeSessionLookup struct{ sessions map[string]*agentdb.Session }

func (f *fakeSessionLookup) GetSession(_ context.Context, id string) (*agentdb.Session, error) {
	if s, ok := f.sessions[id]; ok {
		return s, nil
	}
	return nil, errors.New("session not found")
}

func mintSessionToken(t *testing.T, secret []byte, ttl time.Duration, project, sessionID string) string {
	t.Helper()
	tok, err := devclaims.NewWithTTL(secret, ttl).Issue(context.Background(),
		extension.ContextScope{Customer: project, Job: "j", UserEmail: "u@x.y"}, sessionID)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return tok
}

// rpc posts one JSON-RPC request and returns the decoded response.
func rpc(t *testing.T, srv *mcpServer, token, method string, params any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	blob, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, coreMCPPath, strings.NewReader(string(blob)))
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, out
}

// TestMemoryToolsOverHTTP drives the whole path a session container takes:
// initialize → tools/list → tools/call, authenticated by a real session token.
func TestMemoryToolsOverHTTP(t *testing.T) {
	secret := []byte("test-secret")
	store := newFakeMemoryStore()
	sessions := &fakeSessionLookup{sessions: map[string]*agentdb.Session{
		"sess-1": {ID: "sess-1", Customer: "acme", Worker: "email-answerer"},
	}}
	srv := newMCPServer(coreMCPServerName, newSessionTokenAuth(secret, sessions).authenticate)
	srv.register(testMemoryTools(store, nil).tools()...)
	token := mintSessionToken(t, secret, time.Hour, "acme", "sess-1")

	// initialize
	rec, res := rpc(t, srv, token, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize: %d %s", rec.Code, rec.Body.String())
	}
	result, _ := res["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize result = %#v", result)
	}
	if info, _ := result["serverInfo"].(map[string]any); info["name"] != coreMCPServerName {
		t.Fatalf("serverInfo = %#v", result["serverInfo"])
	}

	// tools/list
	_, res = rpc(t, srv, token, "tools/list", nil)
	result, _ = res["result"].(map[string]any)
	list, _ := result["tools"].([]any)
	if len(list) != 4 {
		t.Fatalf("tools/list returned %d tools, want 4", len(list))
	}

	// tools/call — the caller's worker comes from the session row, not the token.
	_, res = rpc(t, srv, token, "tools/call", map[string]any{
		"name":      "memory_create",
		"arguments": map[string]any{"content": "remember this"},
	})
	result, _ = res["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("tools/call: %#v", result)
	}
	if len(store.created) != 1 || store.created[0].CreatedByWorker != "email-answerer" {
		t.Fatalf("worker provenance must come from the session row: %#v", store.created)
	}
	if store.created[0].Project != "acme" {
		t.Fatalf("project must come from the token: %q", store.created[0].Project)
	}

	t.Run("a tool error is an isError result, not a transport failure", func(t *testing.T) {
		rec, res := rpc(t, srv, token, "tools/call", map[string]any{
			"name": "memory_get", "arguments": map[string]any{"id": "nope"},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		result, _ := res["result"].(map[string]any)
		if result["isError"] != true {
			t.Fatalf("want isError, got %#v", res)
		}
	})

	t.Run("an unknown tool is a protocol error", func(t *testing.T) {
		_, res := rpc(t, srv, token, "tools/call", map[string]any{"name": "memory_delete"})
		rpcErr, _ := res["error"].(map[string]any)
		if rpcErr == nil || !strings.Contains(fmt.Sprint(rpcErr["message"]), "unknown tool") {
			t.Fatalf("want an unknown-tool error, got %#v", res)
		}
	})

	t.Run("notifications get no body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, coreMCPPath,
			strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
		req.Header.Set("Authorization", token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
			t.Fatalf("notification: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("GET is refused rather than left hanging", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, coreMCPPath, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET status = %d", rec.Code)
		}
	})
}

// TestMemoryToolsAuth is the tenancy boundary: which tokens get in, and what
// project they get.
func TestMemoryToolsAuth(t *testing.T) {
	secret := []byte("test-secret")
	sessions := &fakeSessionLookup{sessions: map[string]*agentdb.Session{
		"sess-1": {ID: "sess-1", Customer: "acme", Worker: "email-answerer"},
		"sess-2": {ID: "sess-2", Customer: "other-corp"},
	}}
	auth := newSessionTokenAuth(secret, sessions)

	call := func(header string) (mcpCaller, error) {
		req := httptest.NewRequest(http.MethodPost, coreMCPPath, strings.NewReader("{}"))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		return auth.authenticate(req)
	}

	t.Run("a valid session token resolves project, session and worker", func(t *testing.T) {
		got, err := call(mintSessionToken(t, secret, time.Hour, "acme", "sess-1"))
		if err != nil {
			t.Fatalf("authenticate: %v", err)
		}
		// Identified: the session row was read. It is what separates "this
		// session has no worker" (a human) from "we never found out" (refused).
		want := mcpCaller{Project: "acme", SessionID: "sess-1", Worker: "email-answerer", Identified: true}
		if got != want {
			t.Fatalf("caller = %#v, want %#v", got, want)
		}
	})

	t.Run("Bearer and bare tokens both work", func(t *testing.T) {
		// Session MCP headers may only hold whole-value ${VAR} references
		// (§4.4), so the bare form is what actually arrives.
		tok := mintSessionToken(t, secret, time.Hour, "acme", "sess-1")
		bare, err1 := call(tok)
		bearer, err2 := call("Bearer " + tok)
		if err1 != nil || err2 != nil || bare != bearer {
			t.Fatalf("bare=%v/%v bearer=%v/%v", bare, err1, bearer, err2)
		}
	})

	t.Run("no token, junk token and a foreign signature are all unauthorized", func(t *testing.T) {
		for _, header := range []string{
			"", "   ", "Bearer not-a-jwt",
			mintSessionToken(t, []byte("some-other-secret"), time.Hour, "acme", "sess-1"),
		} {
			if _, err := call(header); !errors.Is(err, errMCPUnauthorized) {
				t.Fatalf("header %q: err = %v, want unauthorized", header, err)
			}
		}
	})

	t.Run("a token with no project claim is refused", func(t *testing.T) {
		if _, err := call(mintSessionToken(t, secret, time.Hour, "", "sess-1")); !errors.Is(err, errMCPUnauthorized) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a token whose project contradicts its session row is refused", func(t *testing.T) {
		// sess-2 belongs to other-corp; a token claiming acme for it must not
		// be resolved to either project.
		_, err := call(mintSessionToken(t, secret, time.Hour, "acme", "sess-2"))
		if err == nil || !strings.Contains(err.Error(), "does not match session") {
			t.Fatalf("err = %v, want a project/session mismatch", err)
		}
	})

	t.Run("an expired token still works while its session exists", func(t *testing.T) {
		// A job that runs longer than the token TTL must not lose its memory
		// mid-task: the live authority is the session row, which is checked on
		// every call. See the Discovered Issues Log (D3) — the real fix is
		// re-issuing tokens, which is not this item.
		expired := mintSessionToken(t, secret, -time.Minute, "acme", "sess-1")
		got, err := call(expired)
		if err != nil || got.Project != "acme" {
			t.Fatalf("expired token for a live session: %#v err=%v", got, err)
		}
	})

	t.Run("an expired token for an unknown session is refused", func(t *testing.T) {
		expired := mintSessionToken(t, secret, -time.Minute, "acme", "sess-gone")
		if _, err := call(expired); !errors.Is(err, errMCPUnauthorized) {
			t.Fatalf("err = %v, want unauthorized", err)
		}
	})

	t.Run("with no session store, provenance degrades but scope does not", func(t *testing.T) {
		bare := newSessionTokenAuth(secret, nil)
		req := httptest.NewRequest(http.MethodPost, coreMCPPath, strings.NewReader("{}"))
		req.Header.Set("Authorization", mintSessionToken(t, secret, time.Hour, "acme", "sess-1"))
		got, err := bare.authenticate(req)
		if err != nil {
			t.Fatalf("authenticate: %v", err)
		}
		if got.Project != "acme" || got.SessionID != "sess-1" || got.Worker != "" {
			t.Fatalf("caller = %#v", got)
		}
	})

	t.Run("an unauthenticated request never reaches a tool", func(t *testing.T) {
		store := newFakeMemoryStore()
		srv := newMCPServer(coreMCPServerName, auth.authenticate)
		srv.register(testMemoryTools(store, nil).tools()...)
		rec, _ := rpc(t, srv, "", "tools/call", map[string]any{
			"name": "memory_create", "arguments": map[string]any{"content": "x"},
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if len(store.created) != 0 {
			t.Fatalf("an unauthenticated call must write nothing")
		}
	})
}

// TestMemoryToolsServerRegistry covers the seam I2/I3/E4/J3 extend: more tools
// on the same server, and a duplicate name caught at boot rather than silently
// shadowing.
func TestMemoryToolsServerRegistry(t *testing.T) {
	srv := newMCPServer("core", func(*http.Request) (mcpCaller, error) { return testCaller(), nil })
	srv.register(testMemoryTools(newFakeMemoryStore(), nil).tools()...)

	// What I2 does, in miniature.
	srv.register(&mcpTool{
		Name: "image_create", Description: "…",
		Handler: func(context.Context, mcpCaller, json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil },
	})
	if got := len(srv.toolNames()); got != 5 {
		t.Fatalf("tools = %v", srv.toolNames())
	}
	_, res := rpc(t, srv, "", "tools/call", map[string]any{"name": "image_create"})
	if result, _ := res["result"].(map[string]any); result["isError"] != false {
		t.Fatalf("registered tool did not run: %#v", res)
	}

	t.Run("a duplicate tool name panics at boot", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatalf("registering a duplicate tool name must panic")
			}
		}()
		srv.register(&mcpTool{
			Name:    "memory_search",
			Handler: func(context.Context, mcpCaller, json.RawMessage) (any, error) { return nil, nil },
		})
	})
}

// TestMemoryToolsCoreMCPConfig pins the config sessions receive: an http server
// whose only credential is a whole-value ${VAR} reference (§4.4), so the config
// is safe to persist and display.
func TestMemoryToolsCoreMCPConfig(t *testing.T) {
	servers := coreMCPServers("http://172.17.0.1:8099/")
	if err := servers.Validate(); err != nil {
		t.Fatalf("core MCP config must be valid: %v", err)
	}
	cfg, ok := servers[coreMCPServerName]
	if !ok {
		t.Fatalf("servers = %#v", servers)
	}
	if cfg.URL != "http://172.17.0.1:8099"+coreMCPPath {
		t.Fatalf("url = %q", cfg.URL)
	}
	if cfg.Headers["Authorization"] != "${SESSION_TOKEN}" {
		t.Fatalf("headers = %#v — the token must be a reference, never a value", cfg.Headers)
	}
	if cfg.Command != "" {
		t.Fatalf("the core server is http, not stdio: %#v", cfg)
	}
}
