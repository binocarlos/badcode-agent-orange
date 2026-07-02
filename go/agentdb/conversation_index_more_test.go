package agentdb

import (
	"context"
	"fmt"
	"testing"
)

// newConvSpineStore returns a Store with the conversation-index spine tables:
// agent_conversation_index (summary rows, minus the PG-only vector/tsv columns)
// plus agent_query_events and agent_sessions for the staleness/backfill queries.
func newConvSpineStore(t *testing.T) *Store {
	t.Helper()
	s := newSessionTestStore(t) // sessions + messages + artifacts + query events
	if err := s.gdb.AutoMigrate(&ConversationIndex{}); err != nil {
		t.Fatalf("automigrate ConversationIndex: %v", err)
	}
	return s
}

func seedConvIndex(t *testing.T, s *Store, ci ConversationIndex) {
	t.Helper()
	if err := s.gdb.Create(&ci).Error; err != nil {
		t.Fatalf("seed index %s: %v", ci.SessionID, err)
	}
}

func seedQueryEvent(t *testing.T, s *Store, sessionID, queryID string, createdAt int64) {
	t.Helper()
	if err := s.UpsertQueryEvents(context.Background(), &QueryEvents{
		SessionID: sessionID, QueryID: queryID, Events: JSONArray(`[]`), CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("seed qe %s/%s: %v", sessionID, queryID, err)
	}
}

func TestListStaleConversationSessions(t *testing.T) {
	s := newConvSpineStore(t)
	ctx := context.Background()

	// s-never: idle, never indexed → stale.
	seedQueryEvent(t, s, "s-never", "q1", 100)
	// s-fresh-index: idle, indexed after last activity → NOT stale.
	seedQueryEvent(t, s, "s-fresh-index", "q1", 200)
	seedConvIndex(t, s, ConversationIndex{SessionID: "s-fresh-index", Customer: "acme", LastActivityAt: 200})
	// s-stale-index: idle, activity newer than the indexed row → stale.
	seedQueryEvent(t, s, "s-stale-index", "q1", 250)
	seedQueryEvent(t, s, "s-stale-index", "q2", 300) // MAX(created_at) = 300
	seedConvIndex(t, s, ConversationIndex{SessionID: "s-stale-index", Customer: "acme", LastActivityAt: 250})
	// s-active: newest event at/after the idle cutoff → NOT idle yet.
	seedQueryEvent(t, s, "s-active", "q1", 900)

	got, err := s.ListStaleConversationSessions(ctx, 500, 10)
	if err != nil {
		t.Fatalf("list stale: %v", err)
	}
	// Ordered by last_activity DESC: s-stale-index (300), then s-never (100).
	want := []string{"s-stale-index", "s-never"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("want %v, got %v", want, got)
	}

	// Limit applies; limit <= 0 falls back to the 100 default.
	got, err = s.ListStaleConversationSessions(ctx, 500, 1)
	if err != nil || len(got) != 1 || got[0] != "s-stale-index" {
		t.Fatalf("limit 1: got %v err=%v", got, err)
	}
	got, err = s.ListStaleConversationSessions(ctx, 500, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("default limit: got %v err=%v", got, err)
	}
}

func TestListConversations(t *testing.T) {
	s := newConvSpineStore(t)
	ctx := context.Background()

	seedConvIndex(t, s, ConversationIndex{SessionID: "c1", Customer: "acme", Job: "job1", UserEmail: "a@acme.com", Title: "first", Summary: "sum1", LastActivityAt: 100})
	seedConvIndex(t, s, ConversationIndex{SessionID: "c2", Customer: "acme", Job: "job2", UserEmail: "b@acme.com", Title: "second", Summary: "sum2", LastActivityAt: 300})
	seedConvIndex(t, s, ConversationIndex{SessionID: "c3", Customer: "acme", Job: "job1", Title: "marker", Summary: "", LastActivityAt: 999}) // empty summary skipped
	seedConvIndex(t, s, ConversationIndex{SessionID: "g1", Customer: "globex", Job: "job1", Title: "other", Summary: "sumg", LastActivityAt: 500})

	// All jobs for a customer, most recent first, marker rows skipped.
	got, err := s.ListConversations(ctx, "acme", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].SessionID != "c2" || got[1].SessionID != "c1" {
		t.Fatalf("expected [c2 c1], got %+v", got)
	}
	if got[0].Title != "second" || got[0].Summary != "sum2" || got[0].UserEmail != "b@acme.com" || got[0].Job != "job2" {
		t.Fatalf("row fields wrong: %+v", got[0])
	}
	if got[0].MatchType != "list" || got[0].Score != 0 {
		t.Fatalf("expected list/0 marker fields, got %+v", got[0])
	}

	// Job narrowing.
	got, err = s.ListConversations(ctx, "acme", "job1", 10)
	if err != nil || len(got) != 1 || got[0].SessionID != "c1" {
		t.Fatalf("job filter: got %+v err=%v", got, err)
	}

	// Limit clamps run without error; no cross-customer rows.
	got, err = s.ListConversations(ctx, "acme", "", 500)
	if err != nil || len(got) != 2 {
		t.Fatalf("big limit: got %+v err=%v", got, err)
	}
	got, err = s.ListConversations(ctx, "nobody", "", 5)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v err=%v", got, err)
	}
}

func TestListAllSessionIDs(t *testing.T) {
	s := newConvSpineStore(t)
	ctx := context.Background()

	mk := func(id string, createdAt int64) {
		mustCreateSession(t, s, &Session{
			ID: id, UserEmail: "u@acme.com", Customer: "acme", WorkflowID: "chat",
			CreatedAt: createdAt, UpdatedAt: createdAt,
		})
	}
	mk("old", 100)
	mk("mid", 200)
	mk("new", 300)

	got, err := s.ListAllSessionIDs(ctx, 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	want := []string{"new", "mid", "old"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("want %v, got %v", want, got)
	}

	got, err = s.ListAllSessionIDs(ctx, 2)
	if err != nil || fmt.Sprint(got) != fmt.Sprint([]string{"new", "mid"}) {
		t.Fatalf("limit 2: got %v err=%v", got, err)
	}
}

func TestEmbClause(t *testing.T) {
	if got := embClause(nil); got != "NULL" {
		t.Fatalf("nil embedding: want NULL, got %q", got)
	}
	if got := embClause([]float32{0.1}); got != "?::vector" {
		t.Fatalf("non-empty embedding: want ?::vector, got %q", got)
	}
}
