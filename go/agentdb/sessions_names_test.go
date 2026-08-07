package agentdb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// newNamedSessionTestStore is newSessionTestStore plus the one thing AutoMigrate
// cannot express: migration 035's PARTIAL unique index. GORM's `uniqueIndex` tag
// has no WHERE clause, so declaring the constraint on the struct would index the
// unnamed rows too and the second unnamed session in a project would be refused.
// The index text here is copied from the migration on purpose — same clause, so
// a divergence shows up as a failing test rather than as a production-only bug.
// (Same trick, same reason, as newImageCatalogueTestStore.)
func newNamedSessionTestStore(t *testing.T) *Store {
	t.Helper()
	s := newSessionTestStore(t)
	if err := s.gdb.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_sessions_name
		ON agent_sessions(customer, name) WHERE name IS NOT NULL AND name <> ''`).Error; err != nil {
		t.Fatalf("create partial unique index: %v", err)
	}
	return s
}

func namedSession(id, customer, name string) *Session {
	sess := baseSession(id)
	sess.Customer = customer
	sess.Name = name
	return sess
}

func TestValidateSessionName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"simple", "hypothesis-a", false},
		{"single word", "wolf", false},
		{"digits", "hypothesis-2026", false},
		{"all digits", "42", false},
		{"64 chars", strings.Repeat("a", 64), false},
		{"empty", "", true},
		{"65 chars", strings.Repeat("a", 65), true},
		{"uppercase", "Hypothesis-A", true},
		{"underscore", "hypothesis_a", true},
		{"leading hyphen", "-hypothesis", true},
		{"trailing hyphen", "hypothesis-", true},
		{"double hyphen", "hypothesis--a", true},
		{"space", "hypothesis a", true},
		{"slash", "hypothesis/a", true},
		{"uuid-looking", "9f8c2a10-3b4d-4e5f-8a9b-0c1d2e3f4a5b", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSessionName(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateSessionName(%q): expected an error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateSessionName(%q): %v", tc.in, err)
			}
			if tc.wantErr && err != nil && !errors.Is(err, ErrSessionNameInvalid) {
				t.Fatalf("ValidateSessionName(%q): error must wrap ErrSessionNameInvalid, got %v", tc.in, err)
			}
		})
	}
}

func TestCreateSession_RejectsMalformedName(t *testing.T) {
	s := newNamedSessionTestStore(t)
	_, err := s.CreateSession(context.Background(), namedSession("s1", "acme", "Not Kebab"))
	if !errors.Is(err, ErrSessionNameInvalid) {
		t.Fatalf("expected ErrSessionNameInvalid, got %v", err)
	}
	// Validate-first, never half-write (the §9 rule the worker store follows):
	// the row must not exist.
	if _, err := s.GetSession(context.Background(), "s1"); err == nil {
		t.Fatalf("a rejected name must not leave a session row behind")
	}
}

func TestCreateSession_DuplicateNameInSameProject(t *testing.T) {
	s := newNamedSessionTestStore(t)
	ctx := context.Background()
	mustCreateSession(t, s, namedSession("s1", "acme", "hypothesis-a"))

	_, err := s.CreateSession(ctx, namedSession("s2", "acme", "hypothesis-a"))
	if !errors.Is(err, ErrSessionNameTaken) {
		t.Fatalf("expected ErrSessionNameTaken, got %v", err)
	}

	// The same name in another project is a different session entirely (P5:
	// names never cross projects).
	mustCreateSession(t, s, namedSession("s3", "globex", "hypothesis-a"))
}

func TestCreateSession_UnnamedSessionsCoexist(t *testing.T) {
	s := newNamedSessionTestStore(t)
	// The whole point of the index being partial: a project has exactly one
	// `hypothesis-a` but any number of anonymous console chats.
	for _, id := range []string{"s1", "s2", "s3"} {
		mustCreateSession(t, s, namedSession(id, "acme", ""))
	}
}

// TestCreateSession_DuplicateIDIsNotReportedAsANameClash pins the discrimination
// in isSessionNameCollision: both failures are unique violations, and telling a
// caller "that name is taken" when it re-used a session id would send them
// looking in the wrong place.
func TestCreateSession_DuplicateIDIsNotReportedAsANameClash(t *testing.T) {
	s := newNamedSessionTestStore(t)
	mustCreateSession(t, s, namedSession("dup", "acme", "hypothesis-a"))

	_, err := s.CreateSession(context.Background(), namedSession("dup", "acme", "hypothesis-b"))
	if err == nil {
		t.Fatalf("expected a duplicate-PK error")
	}
	if errors.Is(err, ErrSessionNameTaken) {
		t.Fatalf("duplicate id must not surface as ErrSessionNameTaken: %v", err)
	}
}

// TestUpdateSession_CannotRename is the immutability criterion. The field is
// `<-:create` in the schema, so it is not that renaming is refused — it is that
// no UPDATE the store can emit carries the column at all.
func TestUpdateSession_CannotRename(t *testing.T) {
	s := newNamedSessionTestStore(t)
	ctx := context.Background()
	mustCreateSession(t, s, namedSession("s1", "acme", "hypothesis-a"))

	row, err := s.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	row.Name = "hypothesis-b"
	row.Title = "still updatable"
	if _, err := s.UpdateSession(ctx, row); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if got.Name != "hypothesis-a" {
		t.Fatalf("name must be immutable, got %q", got.Name)
	}
	if got.Title != "still updatable" {
		t.Fatalf("the rest of the row must still be writable, got title %q", got.Title)
	}

	// …and the name is still claimed, so the "rename" cannot be laundered into
	// a second row either.
	if _, err := s.CreateSession(ctx, namedSession("s2", "acme", "hypothesis-a")); !errors.Is(err, ErrSessionNameTaken) {
		t.Fatalf("expected the original name to still be taken, got %v", err)
	}
}

func TestGetSessionByName(t *testing.T) {
	s := newNamedSessionTestStore(t)
	ctx := context.Background()
	mustCreateSession(t, s, namedSession("s1", "acme", "hypothesis-a"))
	mustCreateSession(t, s, namedSession("s2", "globex", "hypothesis-a"))
	mustCreateSession(t, s, namedSession("s3", "acme", "")) // unnamed

	got, err := s.GetSessionByName(ctx, "acme", "hypothesis-a")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if got.ID != "s1" {
		t.Fatalf("resolved to the wrong session: %q", got.ID)
	}
	if got.Name != "hypothesis-a" {
		t.Fatalf("name not read back: %+v", got)
	}

	tests := []struct {
		name             string
		customer, target string
	}{
		{"absent", "acme", "hypothesis-zzz"},
		// Cross-project is indistinguishable from absent by construction: the
		// customer is part of the WHERE clause, not a post-filter.
		{"other project's name", "initech", "hypothesis-a"},
		// An empty name must NOT match the unnamed rows — otherwise
		// GET …/by-name/ would hand out an arbitrary console chat.
		{"empty name", "acme", ""},
		{"malformed name", "acme", "Hypothesis A"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.GetSessionByName(ctx, tc.customer, tc.target); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("expected ErrSessionNotFound, got %v", err)
			}
		})
	}

	if _, err := s.GetSessionByName(ctx, "", "hypothesis-a"); err == nil {
		t.Fatalf("an empty customer is a caller bug, not a 404")
	}
}
