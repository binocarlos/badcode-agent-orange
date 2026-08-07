package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ---------------------------------------------------------------------------
// T16 — `session_list`
// (design/2026-08-06-embeddable-agent-orange.md).
//
// What is worth a test here is the same short list every core tool answers to:
// the project scope comes from the token and cannot be argued about; the worker
// defaults to the caller's own and can be overridden; the limit is clamped
// rather than trusted; and — specific to this tool — a caller with no worker
// gets an EMPTY LIST rather than an error or the whole project, because the
// worker column is populated only for dispatched jobs.
// ---------------------------------------------------------------------------

type fakeSessionListStore struct {
	queries []agentdb.SessionQuery
	rows    []*agentdb.Session
	err     error
}

func (f *fakeSessionListStore) ListSessions(_ context.Context, q *agentdb.SessionQuery) ([]*agentdb.Session, error) {
	if q != nil {
		f.queries = append(f.queries, *q)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func sessionListTools(store sessionListStore) []*mcpTool {
	return newSessionTools(store, permalinker{base: "https://ui.example"}).tools()
}

func sessionRow(id, name, worker, status string, created, updated int64) *agentdb.Session {
	return &agentdb.Session{
		ID: id, Name: name, Worker: worker, Status: status,
		Customer: "acme", CreatedAt: created, UpdatedAt: updated,
		ArtifactCount: 2, MessageCount: 11,
	}
}

// TestSessionListIsProjectScopedByTheToken: there is no project parameter and
// there never will be — the scope is the caller's, in code (P5, D3's rule).
func TestSessionListIsProjectScopedByTheToken(t *testing.T) {
	store := &fakeSessionListStore{}
	tools := sessionListTools(store)

	if _, err := invokeTool(t, tools, "session_list",
		mcpCaller{Project: "acme", SessionID: "s-1", Worker: "reviewer-a"}, map[string]any{}); err != nil {
		t.Fatalf("session_list: %v", err)
	}
	if len(store.queries) != 1 || store.queries[0].Customer != "acme" {
		t.Fatalf("query did not carry the caller's project: %+v", store.queries)
	}

	schema := tools[0].InputSchema["properties"].(map[string]any)
	if _, found := schema["project"]; found {
		t.Fatal("session_list exposes a project argument — scope must come from the token only")
	}
	if _, found := schema["customer"]; found {
		t.Fatal("session_list exposes a customer argument — scope must come from the token only")
	}
}

// TestSessionListDefaultsToTheCallersOwnWorker: "when did I last run" is the
// question the tool exists for, so the common call takes no arguments at all.
func TestSessionListDefaultsToTheCallersOwnWorker(t *testing.T) {
	store := &fakeSessionListStore{}
	if _, err := invokeTool(t, sessionListTools(store), "session_list",
		mcpCaller{Project: "acme", SessionID: "s-1", Worker: "reviewer-a"}, map[string]any{}); err != nil {
		t.Fatalf("session_list: %v", err)
	}
	if store.queries[0].Worker != "reviewer-a" {
		t.Fatalf("worker filter: want the caller's own, got %q", store.queries[0].Worker)
	}
}

// TestSessionListExplicitWorkerWins is the sibling-worker case: a long-lived
// chat session has no worker of its own and must be able to name one, which is
// how the two-bot pattern reads its reviewer's run history.
func TestSessionListExplicitWorkerWins(t *testing.T) {
	store := &fakeSessionListStore{}
	out, err := invokeTool(t, sessionListTools(store), "session_list",
		mcpCaller{Project: "acme", SessionID: "s-1", Worker: "manager"},
		map[string]any{"worker": "  reviewer-a  "})
	if err != nil {
		t.Fatalf("session_list: %v", err)
	}
	if store.queries[0].Worker != "reviewer-a" {
		t.Fatalf("worker filter: want the argument (trimmed), got %q", store.queries[0].Worker)
	}
	if out["worker"] != "reviewer-a" {
		t.Fatalf("the result must echo which worker was listed, got %v", out["worker"])
	}
}

// TestSessionListWithoutAWorkerReturnsAnEmptyList pins the decision recorded in
// the tool's description: no fallback to Session.Persona. A chat session's
// worker column is empty, and an unfiltered project-wide list would answer a
// different question than the one asked.
func TestSessionListWithoutAWorkerReturnsAnEmptyList(t *testing.T) {
	store := &fakeSessionListStore{rows: []*agentdb.Session{sessionRow("s-9", "", "reviewer-a", "completed", 10, 20)}}
	out, err := invokeTool(t, sessionListTools(store), "session_list",
		mcpCaller{Project: "acme", SessionID: "s-1"}, map[string]any{})
	if err != nil {
		t.Fatalf("a workerless caller must get an answer, not an error: %v", err)
	}
	if len(store.queries) != 0 {
		t.Fatalf("an unfiltered project-wide query was issued: %+v", store.queries)
	}
	if out["count"] != float64(0) {
		t.Fatalf("count: want 0, got %v", out["count"])
	}
	if sessions, _ := out["sessions"].([]any); len(sessions) != 0 {
		t.Fatalf("sessions: want empty, got %v", sessions)
	}
	// Empty must not read as "I have no history" — say why.
	if note, _ := out["note"].(string); !strings.Contains(strings.ToLower(note), "worker") {
		t.Fatalf("the empty answer must explain itself, got note %q", note)
	}
}

// TestSessionListClampsTheLimit — the cap is not decoration: ListSessions runs
// three unconditional COUNT(*) subqueries per row (agentdb/sessions.go:293-300).
func TestSessionListClampsTheLimit(t *testing.T) {
	cases := []struct {
		name string
		arg  any
		want int
	}{
		{"absent", nil, sessionListDefaultLimit},
		{"zero", 0, sessionListDefaultLimit},
		{"negative", -5, sessionListDefaultLimit},
		{"in range", 7, 7},
		{"at the cap", sessionListMaxLimit, sessionListMaxLimit},
		{"over the cap", 5000, sessionListMaxLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeSessionListStore{}
			args := map[string]any{}
			if tc.arg != nil {
				args["limit"] = tc.arg
			}
			if _, err := invokeTool(t, sessionListTools(store), "session_list",
				mcpCaller{Project: "acme", Worker: "reviewer-a"}, args); err != nil {
				t.Fatalf("session_list: %v", err)
			}
			if store.queries[0].Limit != tc.want {
				t.Fatalf("limit: want %d, got %d", tc.want, store.queries[0].Limit)
			}
		})
	}
}

// TestSessionListReturnsProvenanceAndNoMessageContent: metadata plus a
// permalink, in the store's newest-first order — and nothing that could carry a
// transcript, which is the whole point of the tool's narrowness.
func TestSessionListReturnsProvenanceAndNoMessageContent(t *testing.T) {
	store := &fakeSessionListStore{rows: []*agentdb.Session{
		sessionRow("s-91", "hypothesis-a", "reviewer-a", "completed", 1_700_000_000, 1_700_000_900),
		sessionRow("s-90", "", "reviewer-a", "error", 1_600_000_000, 1_600_000_500),
	}}
	out, err := invokeTool(t, sessionListTools(store), "session_list",
		mcpCaller{Project: "acme", SessionID: "s-1", Worker: "reviewer-a"}, map[string]any{})
	if err != nil {
		t.Fatalf("session_list: %v", err)
	}
	sessions, _ := out["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %v", out["sessions"])
	}
	first, _ := sessions[0].(map[string]any)
	if first["id"] != "s-91" {
		t.Fatalf("the store's newest-first order was not preserved: %v", first["id"])
	}
	if first["name"] != "hypothesis-a" || first["status"] != "completed" {
		t.Fatalf("metadata lost: %+v", first)
	}
	if first["session_url"] != "https://ui.example/p/acme/s/s-91" {
		t.Fatalf("session_url: %v", first["session_url"])
	}
	if first["created_at"] != float64(1_700_000_000) || first["updated_at"] != float64(1_700_000_900) {
		t.Fatalf("timestamps lost: %+v", first)
	}
	if first["artifact_count"] != float64(2) || first["message_count"] != float64(11) {
		t.Fatalf("counts lost: %+v", first)
	}

	// The exclusion is the feature: no transcript, no message bodies, no prompt.
	for _, banned := range []string{"messages", "content", "transcript", "composed_prompt", "system_prompt", "title"} {
		if _, found := first[banned]; found {
			t.Fatalf("session_list leaked %q — this tool is provenance only", banned)
		}
	}

	// An unnamed session reports an absent name rather than a name of "".
	second, _ := sessions[1].(map[string]any)
	if _, found := second["name"]; found {
		t.Fatalf("an unnamed session was given a name key: %v", second["name"])
	}
}

// TestSessionListSurfacesStoreErrors — a database failure must not read as "no
// runs", which is the answer a model would act on.
func TestSessionListSurfacesStoreErrors(t *testing.T) {
	store := &fakeSessionListStore{err: errors.New("connection refused")}
	if _, err := invokeTool(t, sessionListTools(store), "session_list",
		mcpCaller{Project: "acme", Worker: "reviewer-a"}, map[string]any{}); err == nil {
		t.Fatal("a store failure was reported as an empty history")
	}
}

// TestSessionListRejectsUnknownArguments: decodeArgs is strict everywhere else
// and this tool is no exception — `name` instead of `worker` must complain
// rather than silently list the caller's own runs.
func TestSessionListRejectsUnknownArguments(t *testing.T) {
	store := &fakeSessionListStore{}
	if _, err := invokeTool(t, sessionListTools(store), "session_list",
		mcpCaller{Project: "acme", Worker: "reviewer-a"}, map[string]any{"name": "reviewer-a"}); err == nil {
		t.Fatal("an unknown argument was accepted")
	}
}

// TestSessionListDescriptionStatesTheJobSessionCaveat is a prose test on
// purpose. The decision NOT to fall back to Session.Persona (see the file's
// header) is only safe if the model is told, or it reads an empty list from a
// chat session as "I have never run".
func TestSessionListDescriptionStatesTheJobSessionCaveat(t *testing.T) {
	desc := strings.ToLower(sessionListTools(&fakeSessionListStore{})[0].Description)
	for _, want := range []string{"chat", "empty", "worker"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("the description must warn that a chat session lists nothing; %q is missing from:\n%s", want, desc)
		}
	}
	if !strings.Contains(desc, "second") && !strings.Contains(desc, "seconds") {
		t.Fatalf("the description must name the timestamp unit (config_history's is milliseconds):\n%s", desc)
	}
}
