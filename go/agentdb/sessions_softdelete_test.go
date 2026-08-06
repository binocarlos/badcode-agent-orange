package agentdb

import (
	"context"
	"errors"
	"testing"
)

// Soft delete (migration 041, doc 22 RD5). These are the STORE-logic half: they
// prove every lookup and listing hides a tombstone. What they deliberately do
// NOT prove is that the transcript survives — the FK cascade that used to
// destroy it exists only in the Postgres schema, and asserting survival against
// a sqlite AutoMigrate with no foreign keys would be a test that passes because
// there was never a cascade to escape. That half is
// sessions_softdelete_live_pg_test.go.

func TestSoftDeletedSessionIsGoneFromEveryLookup(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	mustCreateSession(t, s, baseSession("s1"))
	mustCreateSession(t, s, &Session{
		ID: "s2", UserEmail: "keep@acme.com", Customer: "acme", WorkflowID: "chat",
	})

	if err := s.DeleteSession(ctx, "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := s.GetSession(ctx, "s1"); err == nil {
		t.Fatalf("GetSession must not return a deleted session")
	}
	exists, err := s.SessionExists(ctx, "s1")
	if err != nil {
		t.Fatalf("SessionExists: %v", err)
	}
	if exists {
		t.Fatalf("SessionExists must answer false for a deleted session — otherwise the archive loop keeps its container and host port forever")
	}

	rows, err := s.ListSessions(ctx, &SessionQuery{Customer: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "s2" {
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		t.Fatalf("listing must hide the deleted session, got %v", ids)
	}

	users, err := s.ListSessionUsers(ctx, "acme")
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 || users[0] != "keep@acme.com" {
		t.Fatalf("a deleted session's user must not linger in the user list: %v", users)
	}

	// The row itself is still there — that is the whole point. Read it the only
	// way that can see it: raw SQL, past the filters.
	var deletedAt int64
	if err := s.DB().Raw("SELECT deleted_at FROM agent_sessions WHERE id = ?", "s1").Scan(&deletedAt).Error; err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	if deletedAt <= 0 {
		t.Fatalf("the row must survive with a deleted_at stamp, got %d", deletedAt)
	}
}

func TestSoftDeletedSessionIsNotFindableByName(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	mustCreateSession(t, s, &Session{
		ID: "n1", UserEmail: "u@acme.com", Customer: "acme", WorkflowID: "chat",
		Name: "hypothesis-a",
	})
	if _, err := s.GetSessionByName(ctx, "acme", "hypothesis-a"); err != nil {
		t.Fatalf("by name before delete: %v", err)
	}

	if err := s.DeleteSession(ctx, "n1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The by-name route is how an embedding application addresses a session it
	// never stored a uuid for; it must answer exactly as it does for a name that
	// never existed.
	if _, err := s.GetSessionByName(ctx, "acme", "hypothesis-a"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("by name after delete: want ErrSessionNotFound, got %v", err)
	}
}
