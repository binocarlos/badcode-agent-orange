package agentdb

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The live half of soft delete (migration 041, doc 22 RD5). Only Postgres has
// the thing this exists to defeat: `agent_query_events.session_id REFERENCES
// agent_sessions(id) ON DELETE CASCADE` (migration 013, and the same for
// agent_messages and agent_artifacts). A hard DELETE of the session row took
// the entire conversation with it — which is why the 2026-07-28 calibration
// found `agent_query_events` empty for every session.
//
// Skipped unless AGENTKIT_TEST_POSTGRES_URL is set:
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./agentdb/ -run TestLivePG_SoftDelete
//
// These tests are strictly serial and open one store each (openLivePG leaks a
// pool per call, and the shared ao-test-pg is near its connection ceiling).
// They also HARD-delete their own rows on cleanup, with raw SQL: nothing in the
// store can do that any more, and leaving tombstones in a shared database is
// how the next executor's row counts drift.

// purgeLiveSession is the cleanup no production code path has: it removes the
// tombstone and, through the cascade, its children. Test-only on purpose — the
// operator purge is gated (work plan G3).
func purgeLiveSession(t *testing.T, s *Store, id string) {
	t.Helper()
	t.Cleanup(func() {
		_ = s.DB().Exec("DELETE FROM agent_sessions WHERE id = ?", id).Error
	})
}

func TestLivePG_SoftDeleteKeepsTheTranscript(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()

	sess, err := s.CreateSession(ctx, &Session{
		UserEmail: "u@x.com", Customer: customer, WorkflowID: "chat", Title: "the conversation",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	purgeLiveSession(t, s, sess.ID)

	if err := s.UpsertQueryEvents(ctx, &QueryEvents{
		SessionID: sess.ID, QueryID: "q-1",
		Events: JSONArray(`[{"type":"assistant_message","text":"the model said this"}]`),
	}); err != nil {
		t.Fatalf("persist turn: %v", err)
	}

	if err := s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// THE assertion. Before migration 041 this was 0 — the cascade had fired.
	var turns int64
	if err := s.DB().Raw("SELECT COUNT(*) FROM agent_query_events WHERE session_id = ?", sess.ID).
		Scan(&turns).Error; err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turns != 1 {
		t.Fatalf("the transcript must survive a delete, got %d rows in agent_query_events", turns)
	}

	// …and the session is gone from everything a user or the runtime can reach.
	if _, err := s.GetSession(ctx, sess.ID); err == nil {
		t.Fatalf("a deleted session must not be gettable by id")
	}
	rows, err := s.ListSessions(ctx, &SessionQuery{Customer: customer})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a deleted session must not be listed, got %d", len(rows))
	}
	exists, err := s.SessionExists(ctx, sess.ID)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Fatalf("SessionExists must answer false so the archive loop releases the container")
	}
	if err := s.DeleteSession(ctx, sess.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a second delete must report ErrSessionNotFound, not success: %v", err)
	}

	var deletedAt int64
	if err := s.DB().Raw("SELECT deleted_at FROM agent_sessions WHERE id = ?", sess.ID).
		Scan(&deletedAt).Error; err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	if deletedAt <= 0 {
		t.Fatalf("deleted_at must be stamped in seconds, got %d", deletedAt)
	}
}

// The name half. Migration 035's unique index is the reason this needs
// Postgres: sqlite in these tests has no such index, so name reuse would
// "pass" there for the wrong reason.
func TestLivePG_SoftDeleteReleasesTheSessionName(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()

	first, err := s.CreateSession(ctx, &Session{
		UserEmail: "u@x.com", Customer: customer, WorkflowID: "chat", Name: "hypothesis-a",
	})
	if err != nil {
		t.Fatalf("create named: %v", err)
	}
	purgeLiveSession(t, s, first.ID)

	if err := s.DeleteSession(ctx, first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// (a) it is not findable by name — the T6 lookup an embedding application
	//     uses instead of a uuid.
	if _, err := s.GetSessionByName(ctx, customer, "hypothesis-a"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a deleted session must not resolve by name, got %v", err)
	}

	// (b) it does not hold the name hostage. The purge is gated (G3), so a
	//     tombstone lives forever; if it kept its name, the host that chose
	//     `hypothesis-a` could never use it again and could not see why.
	second, err := s.CreateSession(ctx, &Session{
		UserEmail: "u@x.com", Customer: customer, WorkflowID: "chat", Name: "hypothesis-a",
	})
	if err != nil {
		t.Fatalf("the name must be free again after a delete: %v", err)
	}
	purgeLiveSession(t, s, second.ID)

	got, err := s.GetSessionByName(ctx, customer, "hypothesis-a")
	if err != nil {
		t.Fatalf("by name after reuse: %v", err)
	}
	if got.ID != second.ID {
		t.Fatalf("the name must resolve to the live session %s, got %s", second.ID, got.ID)
	}

	// The tombstone KEEPS the name it had — an operator reading the row later
	// can still see what it was called. Two rows now share (customer, name),
	// which is exactly what narrowing the index to `deleted_at = 0` allows.
	var stored string
	if err := s.DB().Raw("SELECT name FROM agent_sessions WHERE id = ?", first.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read tombstone name: %v", err)
	}
	if stored != "hypothesis-a" {
		t.Fatalf("the tombstone must keep its name, got %q", stored)
	}

	// And uniqueness still bites among LIVE rows.
	if _, err := s.CreateSession(ctx, &Session{
		UserEmail: "u@x.com", Customer: customer, WorkflowID: "chat", Name: "hypothesis-a",
	}); !errors.Is(err, ErrSessionNameTaken) {
		t.Fatalf("the narrowed index must still refuse a live duplicate, got %v", err)
	}
}

// SearchMessages is a listing too: a deleted session's messages must not come
// back through search, handing the user a conversation the UI said was gone and
// a session id every by-id route now 404s.
func TestLivePG_SoftDeletedSessionIsNotSearchable(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()
	needle := "zqxwv" + uuid.New().String()[:8]

	kept, err := s.CreateSession(ctx, &Session{
		UserEmail: "u@x.com", Customer: customer, WorkflowID: "chat", Title: "kept",
	})
	if err != nil {
		t.Fatalf("create kept: %v", err)
	}
	purgeLiveSession(t, s, kept.ID)
	gone, err := s.CreateSession(ctx, &Session{
		UserEmail: "u@x.com", Customer: customer, WorkflowID: "chat", Title: "gone",
	})
	if err != nil {
		t.Fatalf("create gone: %v", err)
	}
	purgeLiveSession(t, s, gone.ID)

	if err := s.CreateMessages(ctx, []*Message{
		{SessionID: kept.ID, Role: "user", Content: "keep " + needle, SequenceNum: 1},
		{SessionID: gone.ID, Role: "user", Content: "drop " + needle, SequenceNum: 1},
	}); err != nil {
		t.Fatalf("create messages: %v", err)
	}

	before, err := s.SearchMessages(ctx, &MessageSearchQuery{Customer: customer, Query: needle})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("both messages must be findable before the delete, got %d", len(before))
	}

	if err := s.DeleteSession(ctx, gone.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	after, err := s.SearchMessages(ctx, &MessageSearchQuery{Customer: customer, Query: needle})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(after) != 1 || after[0].SessionID != kept.ID {
		t.Fatalf("search must return only the live session's message, got %+v", after)
	}
}
