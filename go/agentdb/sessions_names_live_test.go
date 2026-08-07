package agentdb

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The live twins of sessions_names_test.go. They exist because the sqlite half
// proves the STORE logic and nothing about the production schema: migration
// 035's partial unique index is the actual guarantee behind
// ErrSessionNameTaken, and sqlite in these tests only ever sees a hand-rolled
// copy of it. Skipped unless AGENTKIT_TEST_POSTGRES_URL is set — a green
// `go test ./agentdb/` without it says nothing about any of this.
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./agentdb/ -run TestLivePG_SessionName

// newLiveNamedSession creates a named session and registers cleanup. It returns
// the error rather than failing, because half these cases are about the error.
func newLiveNamedSession(t *testing.T, s *Store, customer, name string) (*Session, error) {
	t.Helper()
	sess, err := s.CreateSession(context.Background(), &Session{
		UserEmail: "u@x.com", Customer: customer, WorkflowID: "chat", Name: name,
	})
	if sess != nil {
		t.Cleanup(func() { _ = s.DeleteSession(context.Background(), sess.ID) })
	}
	return sess, err
}

func TestLivePG_SessionNameUniquenessIsScopedToTheProject(t *testing.T) {
	s := openLivePG(t)
	acme := "cust-" + uuid.New().String()
	globex := "cust-" + uuid.New().String()

	if _, err := newLiveNamedSession(t, s, acme, "hypothesis-a"); err != nil {
		t.Fatalf("first named session: %v", err)
	}
	if _, err := newLiveNamedSession(t, s, acme, "hypothesis-a"); !errors.Is(err, ErrSessionNameTaken) {
		t.Fatalf("expected ErrSessionNameTaken from the live index, got %v", err)
	}
	// Same name, different project: the index is on (customer, name), so this
	// is a different row and must be allowed.
	if _, err := newLiveNamedSession(t, s, globex, "hypothesis-a"); err != nil {
		t.Fatalf("same name in another project: %v", err)
	}
	// A second name in the same project is fine too — the index constrains the
	// pair, not the project.
	if _, err := newLiveNamedSession(t, s, acme, "hypothesis-b"); err != nil {
		t.Fatalf("second name in the same project: %v", err)
	}
}

// TestLivePG_SessionNameIndexIsPartial is the reason the index carries a WHERE
// clause at all: every console chat is unnamed, so a plain UNIQUE (customer,
// name) would refuse the second one. Both spellings of "no name" are exercised:
// GORM writes the empty string, and every row that predates migration 035 holds
// NULL.
func TestLivePG_SessionNameIndexIsPartial(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()

	for i := 0; i < 3; i++ {
		if _, err := newLiveNamedSession(t, s, customer, ""); err != nil {
			t.Fatalf("unnamed session %d: %v", i, err)
		}
	}

	// Two rows the way they exist in the live database today: NULL, written
	// before the column had a name to put in it. The index must not see them,
	// and — the part that would break every read in production if the column
	// were mishandled — GORM must scan NULL into the Go string field.
	for i := 0; i < 2; i++ {
		id := uuid.New().String()
		if err := s.DB().Exec(`INSERT INTO agent_sessions (id, user_email, customer, workflow_id, name)
			VALUES (?, ?, ?, 'chat', NULL)`, id, "legacy@x.com", customer).Error; err != nil {
			t.Fatalf("insert legacy NULL-name row %d: %v", i, err)
		}
		t.Cleanup(func() { _ = s.DeleteSession(ctx, id) })

		got, err := s.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("a NULL name must read back as the empty string, not an error: %v", err)
		}
		if got.Name != "" {
			t.Fatalf("NULL name scanned as %q", got.Name)
		}
	}

	// ListSessions selects agent_sessions.* — the same NULL must survive the
	// aggregate-join query too.
	rows, err := s.ListSessions(ctx, &SessionQuery{Customer: customer})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("expected 5 unnamed rows to coexist, got %d", len(rows))
	}
}

func TestLivePG_SessionNameIsImmutable(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()

	sess, err := newLiveNamedSession(t, s, customer, "hypothesis-a")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sess.Name = "hypothesis-b"
	sess.Title = "still updatable"
	if _, err := s.UpdateSession(ctx, sess); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Read the column with raw SQL rather than through the model: the point is
	// what is IN the database, and the model field is `<-:create` (unreadable
	// evidence, since GORM would simply not have written it).
	var stored string
	if err := s.DB().Raw("SELECT name FROM agent_sessions WHERE id = ?", sess.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read name: %v", err)
	}
	if stored != "hypothesis-a" {
		t.Fatalf("name was rewritten to %q — the column must be create-only", stored)
	}
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil || got.Title != "still updatable" {
		t.Fatalf("the rest of the row must still update: %+v err=%v", got, err)
	}
}

func TestLivePG_GetSessionByName(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	acme := "cust-" + uuid.New().String()
	globex := "cust-" + uuid.New().String()

	mine, err := newLiveNamedSession(t, s, acme, "hypothesis-a")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := newLiveNamedSession(t, s, globex, "hypothesis-a"); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	if _, err := newLiveNamedSession(t, s, acme, ""); err != nil {
		t.Fatalf("create unnamed: %v", err)
	}

	got, err := s.GetSessionByName(ctx, acme, "hypothesis-a")
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if got.ID != mine.ID {
		t.Fatalf("resolved to another project's session")
	}
	if _, err := s.GetSessionByName(ctx, acme, "hypothesis-zzz"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("absent name: %v", err)
	}
	// The empty name must not match the unnamed row.
	if _, err := s.GetSessionByName(ctx, acme, ""); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("empty name: %v", err)
	}
}
