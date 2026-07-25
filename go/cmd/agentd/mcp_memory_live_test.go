package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/extension/embedding"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// D3 against a real Postgres.
//
// The unit tests above prove the tools' *policy* against a fake store. This one
// proves the wiring: memory is Postgres-only by decision (D4), so the fake is
// the one thing that cannot tell us the SQL, the jsonb selectors and the
// project isolation actually hold when a session calls the tools over HTTP.
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./cmd/agentd/ -run TestMemoryTools
// ---------------------------------------------------------------------------

func openLiveStore(t *testing.T) *agentdb.Store {
	t.Helper()
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	s, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	return s
}

// liveSession creates a real session row so token auth resolves a real worker.
func liveSession(t *testing.T, s *agentdb.Store, project, worker string) *agentdb.Session {
	t.Helper()
	sess, err := s.CreateSession(context.Background(), &agentdb.Session{
		UserEmail: "u@x.y", Customer: project, WorkflowID: "chat", Worker: worker,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteSession(context.Background(), sess.ID) })
	return sess
}

// TestMemoryToolsLiveRoundTrip drives create → search → get → current over the
// HTTP transport against a real store, and proves a second project's session
// sees none of it.
func TestMemoryToolsLiveRoundTrip(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project := "proj-" + uuid.New().String()
	intruder := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		store.DB().Exec("DELETE FROM memories WHERE project IN (?, ?)", project, intruder)
	})

	sess := liveSession(t, store, project, "email-answerer")
	other := liveSession(t, store, intruder, "nosy-worker")

	secret := []byte("live-test-secret")
	srv := newMCPServer(coreMCPServerName, newSessionTokenAuth(secret, store).authenticate)
	srv.register(newMemoryTools(store, embedding.NewMock(), permalinker{base: "https://ui.example"}).tools()...)

	token := mintSessionToken(t, secret, time.Hour, project, sess.ID)
	otherToken := mintSessionToken(t, secret, time.Hour, intruder, other.ID)

	call := func(tok, name string, args map[string]any) map[string]any {
		t.Helper()
		_, res := rpc(t, srv, tok, "tools/call", map[string]any{"name": name, "arguments": args})
		result, _ := res["result"].(map[string]any)
		if result == nil {
			t.Fatalf("%s: no result in %#v", name, res)
		}
		if result["isError"] == true {
			t.Fatalf("%s failed: %v", name, result["content"])
		}
		structured, _ := result["structuredContent"].(map[string]any)
		if structured == nil {
			t.Fatalf("%s: no structuredContent in %#v", name, result)
		}
		return structured
	}

	// create
	created := call(token, "memory_create", map[string]any{
		"content": "The refund window is 30 days for physical goods.",
		"labels":  map[string]string{"kind": "fact", "worker": "email-answerer"},
	})
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %#v", created)
	}
	if created["created_by_worker"] != "email-answerer" || created["created_by_session"] != sess.ID {
		t.Fatalf("provenance = %#v", created)
	}
	wantURL := "https://ui.example/p/" + project + "/s/" + sess.ID
	if created["session_url"] != wantURL {
		t.Fatalf("session_url = %v, want %v", created["session_url"], wantURL)
	}
	if created["embedded"] != true {
		t.Fatalf("the mock embedder must have produced a vector: %#v", created)
	}

	// A named memory, for memory_current.
	call(token, "memory_create", map[string]any{
		"content": "Labels in use: kind, worker, thread.",
		"labels":  map[string]string{"name": "label-registry"},
	})
	call(token, "memory_create", map[string]any{
		"content": "Labels in use: kind, worker, thread, ticket.",
		"labels":  map[string]string{"name": "label-registry"},
	})

	// search — selector leg
	found := call(token, "memory_search", map[string]any{"label_selector": "kind=fact"})
	results, _ := found["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("selector search returned %d rows, want 1: %#v", len(results), found)
	}
	if hit, _ := results[0].(map[string]any); hit["id"] != id || hit["session_url"] != wantURL {
		t.Fatalf("hit = %#v", results[0])
	}

	// search — query leg (hybrid, over a real tsvector + pgvector)
	found = call(token, "memory_search", map[string]any{"query": "refund window"})
	if results, _ = found["results"].([]any); len(results) == 0 {
		t.Fatalf("query search found nothing: %#v", found)
	}

	// get — full content
	got := call(token, "memory_get", map[string]any{"id": id})
	if got["content"] != "The refund window is 30 days for physical goods." {
		t.Fatalf("get content = %v", got["content"])
	}

	// current — newest wins, append-only KV
	current := call(token, "memory_current", map[string]any{"name": "label-registry"})
	if current["found"] != true || !strings.Contains(fmt.Sprint(current["content"]), "ticket") {
		t.Fatalf("memory_current must return the NEWEST value: %#v", current)
	}
	missing := call(token, "memory_current", map[string]any{"name": "never-written"})
	if missing["found"] != false {
		t.Fatalf("memory_current for an unwritten name = %#v", missing)
	}

	// Project isolation, from the other side of the boundary.
	intruderSearch := call(otherToken, "memory_search", map[string]any{})
	if results, _ = intruderSearch["results"].([]any); len(results) != 0 {
		t.Fatalf("another project's session saw %d memories", len(results))
	}
	_, res := rpc(t, srv, otherToken, "tools/call", map[string]any{
		"name": "memory_get", "arguments": map[string]any{"id": id},
	})
	result, _ := res["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("another project must not be able to read the memory: %#v", result)
	}
	if intruderCurrent := call(otherToken, "memory_current", map[string]any{"name": "label-registry"}); intruderCurrent["found"] != false {
		t.Fatalf("named memories are project-scoped too: %#v", intruderCurrent)
	}

	// And nothing the intruder did leaked into this project's row count.
	rows, err := store.SearchMemories(ctx, &agentdb.MemorySearchQuery{Project: project, Limit: 100})
	if err != nil || len(rows) != 3 {
		t.Fatalf("project rows = %d err=%v, want the 3 written here", len(rows), err)
	}
}

// TestMemoryToolsLiveBriefingLookup joins C4 to D3 against the real store: the
// briefing selector core resolves at composition time and the memory an
// archivist writes through the tool are the same row, found by the same query.
func TestMemoryToolsLiveBriefingLookup(t *testing.T) {
	store := openLiveStore(t)
	project := "proj-" + uuid.New().String()
	t.Cleanup(func() { store.DB().Exec("DELETE FROM memories WHERE project = ?", project) })

	sess := liveSession(t, store, project, "archivist")
	secret := []byte("live-test-secret")
	srv := newMCPServer(coreMCPServerName, newSessionTokenAuth(secret, store).authenticate)
	srv.register(newMemoryTools(store, nil, permalinker{}).tools()...)
	token := mintSessionToken(t, secret, time.Hour, project, sess.ID)

	// The archivist writes a rolling summary for another worker (§7.4).
	for _, body := range []string{"an older picture", "40 emails answered, mostly refunds"} {
		_, res := rpc(t, srv, token, "tools/call", map[string]any{
			"name": "memory_create",
			"arguments": map[string]any{
				"content": body,
				"labels":  map[string]string{"kind": "rolling-summary", "worker": "email-answerer"},
			},
		})
		if result, _ := res["result"].(map[string]any); result["isError"] == true {
			t.Fatalf("memory_create: %v", result["content"])
		}
		time.Sleep(2 * time.Millisecond) // created_at is milliseconds; keep the order unambiguous
	}

	// C4's lookup, exactly as composition performs it — the same exported
	// selector builder ComposeJob's caller uses, not a copy of the string.
	mem, err := store.NewestMemory(context.Background(), project, agentkit.RollingSummarySelector("email-answerer"))
	if err != nil {
		t.Fatalf("briefing lookup: %v", err)
	}
	if mem.Content != "40 emails answered, mostly refunds" {
		t.Fatalf("briefing took %q, want the newest summary", mem.Content)
	}
	if mem.CreatedBySession != sess.ID || mem.CreatedByWorker != "archivist" {
		t.Fatalf("provenance = %s/%s", mem.CreatedByWorker, mem.CreatedBySession)
	}
}
