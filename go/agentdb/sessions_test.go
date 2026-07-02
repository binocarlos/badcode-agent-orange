package agentdb

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/imageregistry"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newSessionTestStore returns a Store over a temp sqlite DB with the session
// spine tables (sessions + messages + artifacts + query events) auto-migrated.
// AutoMigrate materialises the read-only aggregate fields (artifact_count,
// message_count, tool_call_count) as physical columns, which do NOT exist in
// the real Postgres schema — they are SELECT aliases in ListSessions. We drop
// them so `agent_sessions.*` matches production and the aliases are the only
// source of those values.
func newSessionTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sessions_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Session{}, &Message{}, &Artifact{}, &QueryEvents{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	for _, col := range []string{"artifact_count", "message_count", "tool_call_count"} {
		if err := db.Exec("ALTER TABLE agent_sessions DROP COLUMN " + col).Error; err != nil {
			t.Fatalf("drop virtual column %s: %v", col, err)
		}
	}
	return &Store{gdb: db}
}

func mustCreateSession(t *testing.T, s *Store, sess *Session) *Session {
	t.Helper()
	got, err := s.CreateSession(context.Background(), sess)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return got
}

// baseSession returns a minimal valid session.
func baseSession(id string) *Session {
	return &Session{
		ID:         id,
		UserEmail:  "u@acme.com",
		Customer:   "acme",
		WorkflowID: "chat",
	}
}

func TestCreateSession_Validation(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		in      *Session
		wantErr string
	}{
		{"missing customer", &Session{UserEmail: "u@x.com", WorkflowID: "chat"}, "customer is required"},
		{"missing workflow", &Session{UserEmail: "u@x.com", Customer: "acme"}, "workflow_id is required"},
		{"missing user email", &Session{Customer: "acme", WorkflowID: "chat"}, "user_email is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateSession(ctx, tc.in)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("expected error %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestCreateSession_GeneratesIDAndRoundTrips(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	created := mustCreateSession(t, s, &Session{
		UserEmail: "u@acme.com", Customer: "acme", WorkflowID: "chat",
		Title:    "hello",
		Metadata: JSONMap{"k": "v"},
	})
	if created.ID == "" {
		t.Fatalf("expected generated ID")
	}
	got, err := s.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "hello" || got.Customer != "acme" || got.Metadata["k"] != "v" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatalf("expected autoCreateTime/autoUpdateTime, got %d/%d", got.CreatedAt, got.UpdatedAt)
	}
}

func TestCreateSession_DuplicateIDErrors(t *testing.T) {
	s := newSessionTestStore(t)
	mustCreateSession(t, s, baseSession("dup"))
	if _, err := s.CreateSession(context.Background(), baseSession("dup")); err == nil {
		t.Fatalf("expected duplicate-PK error")
	}
}

func TestGetSession_Errors(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	if _, err := s.GetSession(ctx, ""); err == nil {
		t.Fatalf("expected error for empty id")
	}
	_, err := s.GetSession(ctx, "nope")
	if err == nil {
		t.Fatalf("expected not-found error")
	}
	if !isNotFound(err) {
		t.Fatalf("expected wrapped record-not-found, got %v", err)
	}
}

func TestUpdateSession(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	if _, err := s.UpdateSession(ctx, &Session{}); err == nil {
		t.Fatalf("expected error for empty id")
	}

	created := mustCreateSession(t, s, baseSession("s1"))
	created.Title = "renamed"
	created.Status = "archived"
	if _, err := s.UpdateSession(ctx, created); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "renamed" || got.Status != "archived" {
		t.Fatalf("update not persisted: %+v", got)
	}
}

func TestDeleteSession(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	if err := s.DeleteSession(ctx, ""); err == nil {
		t.Fatalf("expected error for empty id")
	}
	mustCreateSession(t, s, baseSession("s1"))
	if err := s.DeleteSession(ctx, "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetSession(ctx, "s1"); err == nil {
		t.Fatalf("expected not-found after delete")
	}
	// Deleting a nonexistent session is a no-op, not an error.
	if err := s.DeleteSession(ctx, "never-existed"); err != nil {
		t.Fatalf("delete nonexistent: %v", err)
	}
}

// seedListSessions creates a deterministic fixture across two customers.
func seedListSessions(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	mk := func(id, email, customer, job, status, workflow string, updatedAt int64) {
		mustCreateSession(t, s, &Session{
			ID: id, UserEmail: email, Customer: customer, Job: job,
			Status: status, WorkflowID: workflow,
			CreatedAt: updatedAt, UpdatedAt: updatedAt,
		})
	}
	mk("a1", "alice@acme.com", "acme", "job1", "active", "chat", 100)
	mk("a2", "bob@acme.com", "acme", "job2", "archived", "chat", 200)
	mk("a3", "Carol@Acme.com", "acme", "job1", "active", "eval-run", 300)
	mk("g1", "gus@globex.com", "globex", "job1", "active", "chat", 400)

	// a1 gets 2 messages (one tool call) and 1 artifact.
	if err := s.CreateMessages(ctx, []*Message{
		{SessionID: "a1", Role: "user", Content: "hi", SequenceNum: 1},
		{SessionID: "a1", Role: "assistant", Content: "run", ToolName: "bash", SequenceNum: 2},
	}); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	if _, err := s.CreateArtifact(ctx, &Artifact{SessionID: "a1", FilePath: "/workspace/out.txt"}); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
}

func TestListSessions_FiltersAndCounts(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()
	seedListSessions(t, s)

	ids := func(rows []*Session) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}
		return out
	}

	tests := []struct {
		name  string
		query *SessionQuery
		want  []string // in expected order (updated_at DESC)
	}{
		{"nil query returns all, newest first", nil, []string{"g1", "a3", "a2", "a1"}},
		{"by id", &SessionQuery{ID: "a2"}, []string{"a2"}},
		{"by user email", &SessionQuery{UserEmail: "alice@acme.com"}, []string{"a1"}},
		{"by customer", &SessionQuery{Customer: "acme"}, []string{"a3", "a2", "a1"}},
		{"by job", &SessionQuery{Job: "job2"}, []string{"a2"}},
		{"by status", &SessionQuery{Status: "archived"}, []string{"a2"}},
		{"has messages", &SessionQuery{HasMessages: true}, []string{"a1"}},
		{"exclude workflow prefix", &SessionQuery{Customer: "acme", ExcludeWorkflowIDPrefix: "eval-"}, []string{"a2", "a1"}},
		{"limit", &SessionQuery{Limit: 2}, []string{"g1", "a3"}},
		{"offset", &SessionQuery{Limit: 2, Offset: 2}, []string{"a2", "a1"}},
		{"no match is empty non-nil", &SessionQuery{Customer: "nobody"}, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListSessions(ctx, tc.query)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got == nil {
				t.Fatalf("expected non-nil slice")
			}
			gotIDs := ids(got)
			if fmt.Sprint(gotIDs) != fmt.Sprint(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, gotIDs)
			}
		})
	}

	// Aggregate aliases: a1 has 1 artifact, 2 messages, 1 tool call; a2 none.
	rows, err := s.ListSessions(ctx, &SessionQuery{Customer: "acme"})
	if err != nil {
		t.Fatalf("list acme: %v", err)
	}
	byID := map[string]*Session{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	a1 := byID["a1"]
	if a1.ArtifactCount != 1 || a1.MessageCount != 2 || a1.ToolCallCount != 1 {
		t.Fatalf("a1 counts wrong: artifacts=%d messages=%d tools=%d", a1.ArtifactCount, a1.MessageCount, a1.ToolCallCount)
	}
	a2 := byID["a2"]
	if a2.ArtifactCount != 0 || a2.MessageCount != 0 || a2.ToolCallCount != 0 {
		t.Fatalf("a2 counts wrong: %+v", a2)
	}
}

// TestListSessions_ExcludeUserEmailsIsCaseInsensitive proves the exclusion
// filter honours its own case-insensitivity intent (the SQL lowercases the
// column side — LOWER(user_email) NOT IN ?). Emails as stored/passed by hosts
// may be mixed-case; exclusion must not silently stop working.
func TestListSessions_ExcludeUserEmailsIsCaseInsensitive(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()
	seedListSessions(t, s) // a3 is stored as Carol@Acme.com

	tests := []struct {
		name    string
		exclude []string
		want    []string
	}{
		{"lowercase exclusion vs mixed-case row", []string{"carol@acme.com"}, []string{"a2", "a1"}},
		{"mixed-case exclusion vs lowercase row", []string{"Alice@Acme.com"}, []string{"a3", "a2"}},
		{"mixed-case exclusion vs same mixed-case row", []string{"Carol@Acme.com"}, []string{"a2", "a1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListSessions(ctx, &SessionQuery{Customer: "acme", ExcludeUserEmails: tc.exclude})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			gotIDs := make([]string, len(got))
			for i, r := range got {
				gotIDs[i] = r.ID
			}
			if fmt.Sprint(gotIDs) != fmt.Sprint(tc.want) {
				t.Fatalf("exclude %v: want %v, got %v", tc.exclude, tc.want, gotIDs)
			}
		})
	}
}

func TestCountSessionsBySnapshotState(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	mk := func(id, customer, state string) {
		sess := baseSession(id)
		sess.Customer = customer
		sess.SnapshotState = state
		mustCreateSession(t, s, sess)
	}
	mk("s1", "acme", "running")
	mk("s2", "acme", "running")
	mk("s3", "acme", "archived")
	mk("s4", "globex", "archived")

	all, err := s.CountSessionsBySnapshotState(ctx, "")
	if err != nil {
		t.Fatalf("count all: %v", err)
	}
	if all["running"] != 2 || all["archived"] != 2 {
		t.Fatalf("unexpected counts: %v", all)
	}

	acme, err := s.CountSessionsBySnapshotState(ctx, "acme")
	if err != nil {
		t.Fatalf("count acme: %v", err)
	}
	if acme["running"] != 2 || acme["archived"] != 1 {
		t.Fatalf("unexpected acme counts: %v", acme)
	}

	empty, err := s.CountSessionsBySnapshotState(ctx, "nobody")
	if err != nil {
		t.Fatalf("count nobody: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty map, got %v", empty)
	}
}

func TestGetSessionArchiveStats(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	mk := func(id, customer, state string, size int64) {
		sess := baseSession(id)
		sess.Customer = customer
		sess.SnapshotState = state
		sess.ArchiveSizeBytes = size
		mustCreateSession(t, s, sess)
	}
	mk("s1", "acme", "archived", 100)
	mk("s2", "acme", "archived", 50)
	mk("s3", "acme", "running", 999) // not archived — must not count
	mk("s4", "globex", "archived", 7)

	count, bytes, err := s.GetSessionArchiveStats(ctx, "")
	if err != nil {
		t.Fatalf("stats all: %v", err)
	}
	if count != 3 || bytes != 157 {
		t.Fatalf("all stats wrong: count=%d bytes=%d", count, bytes)
	}

	count, bytes, err = s.GetSessionArchiveStats(ctx, "acme")
	if err != nil {
		t.Fatalf("stats acme: %v", err)
	}
	if count != 2 || bytes != 150 {
		t.Fatalf("acme stats wrong: count=%d bytes=%d", count, bytes)
	}

	// No archived rows → zero count, COALESCE'd zero sum, no error.
	count, bytes, err = s.GetSessionArchiveStats(ctx, "nobody")
	if err != nil {
		t.Fatalf("stats nobody: %v", err)
	}
	if count != 0 || bytes != 0 {
		t.Fatalf("expected zeros, got count=%d bytes=%d", count, bytes)
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"gorm sentinel", gorm.ErrRecordNotFound, true},
		{"wrapped sentinel", fmt.Errorf("get: %w", gorm.ErrRecordNotFound), true},
		{"string match", errors.New("driver: record not found somewhere"), true},
		{"other error", errors.New("connection refused"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotFound(tc.err); got != tc.want {
				t.Fatalf("isNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWorkerBinding_Lifecycle(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()
	mustCreateSession(t, s, baseSession("s1"))

	// No binding yet.
	id, ok, err := s.GetWorkerBinding(ctx, "s1")
	if err != nil || ok || id != "" {
		t.Fatalf("expected no binding, got id=%q ok=%v err=%v", id, ok, err)
	}

	if err := s.SetWorkerBinding(ctx, "s1", "worker-7"); err != nil {
		t.Fatalf("set binding: %v", err)
	}
	id, ok, err = s.GetWorkerBinding(ctx, "s1")
	if err != nil || !ok || id != "worker-7" {
		t.Fatalf("expected worker-7, got id=%q ok=%v err=%v", id, ok, err)
	}

	if err := s.ClearWorkerBinding(ctx, "s1"); err != nil {
		t.Fatalf("clear binding: %v", err)
	}
	id, ok, err = s.GetWorkerBinding(ctx, "s1")
	if err != nil || ok || id != "" {
		t.Fatalf("expected cleared binding, got id=%q ok=%v err=%v", id, ok, err)
	}
}

// Pseudo-session IDs (no DB row) must be treated as "no binding", not errors —
// fleet placement uses placeholder IDs like "_composition-image-build_".
func TestWorkerBinding_PseudoSessionIsNotAnError(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	id, ok, err := s.GetWorkerBinding(ctx, "_composition-image-build_")
	if err != nil || ok || id != "" {
		t.Fatalf("expected (\"\", false, nil) for pseudo-session, got id=%q ok=%v err=%v", id, ok, err)
	}
	if err := s.SetWorkerBinding(ctx, "_composition-image-build_", "w1"); err != nil {
		t.Fatalf("set on pseudo-session must be a no-op, got %v", err)
	}
}

func TestSnapshotHandle_RoundTrip(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()
	mustCreateSession(t, s, baseSession("s1"))

	// Unset → (zero, false, nil).
	h, ok, err := s.GetSnapshotHandle(ctx, "s1")
	if err != nil || ok || h.Kind != "" {
		t.Fatalf("expected empty handle, got %+v ok=%v err=%v", h, ok, err)
	}

	want := imageregistry.Handle{Kind: "blob-archive", Ref: "blobs/s1.tar", Meta: map[string]string{"layer": "user"}}
	if err := s.SetSnapshotHandle(ctx, "s1", want); err != nil {
		t.Fatalf("set handle: %v", err)
	}
	h, ok, err = s.GetSnapshotHandle(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("get handle: ok=%v err=%v", ok, err)
	}
	if h.Kind != want.Kind || h.Ref != want.Ref || h.Meta["layer"] != "user" {
		t.Fatalf("handle round-trip mismatch: %+v", h)
	}

	// Missing session → error (unlike worker bindings, snapshots need a real row).
	if _, _, err := s.GetSnapshotHandle(ctx, "missing"); err == nil {
		t.Fatalf("expected error for missing session")
	}
	if err := s.SetSnapshotHandle(ctx, "missing", want); err == nil {
		t.Fatalf("expected error setting handle on missing session")
	}
}

func TestGetSnapshotHandle_MalformedJSON(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()
	sess := baseSession("s1")
	sess.SnapshotHandle = "{not json"
	mustCreateSession(t, s, sess)

	if _, _, err := s.GetSnapshotHandle(ctx, "s1"); err == nil {
		t.Fatalf("expected decode error for malformed snapshot handle")
	}
}

func TestListSessionUsers(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	mk := func(id, email, customer string) {
		sess := baseSession(id)
		sess.UserEmail = email
		sess.Customer = customer
		mustCreateSession(t, s, sess)
	}
	mk("s1", "zoe@acme.com", "acme")
	mk("s2", "amy@acme.com", "acme")
	mk("s3", "amy@acme.com", "acme") // duplicate — must be distinct
	mk("s4", "gus@globex.com", "globex")

	got, err := s.ListSessionUsers(ctx, "acme")
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	want := []string{"amy@acme.com", "zoe@acme.com"} // distinct + ordered
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("want %v, got %v", want, got)
	}

	empty, err := s.ListSessionUsers(ctx, "nobody")
	if err != nil {
		t.Fatalf("list users nobody: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v", empty)
	}
}
