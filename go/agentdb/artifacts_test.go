package agentdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newTestStore returns a Store backed by a temp-file sqlite DB with ONLY the
// agent_artifacts table created via AutoMigrate (the production Postgres
// migrations cannot run on sqlite). White-box: constructs Store{gdb} directly.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "agentdb_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Artifact{}); err != nil {
		t.Fatalf("automigrate Artifact: %v", err)
	}
	return &Store{gdb: db}
}

// TestUpsertArtifact_PreservesIsDir proves the IsDir field round-trips and that
// the update branch of UpsertArtifact carries IsDir (insert false, then upsert
// true on the same session_id+file_path must end up true).
func TestUpsertArtifact_PreservesIsDir(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert as a regular file (is_dir = false).
	if _, err := s.UpsertArtifact(ctx, &Artifact{
		SessionID: "s1", FilePath: "skills/demo", IsDir: false, Status: "live",
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Upsert the same (session, path) as a directory now (is_dir = true).
	if _, err := s.UpsertArtifact(ctx, &Artifact{
		SessionID: "s1", FilePath: "skills/demo", IsDir: true, Status: "extracted",
		AzureBlobPath: "agent-artifacts/s1/skills/demo",
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	rows, err := s.ListArtifacts(ctx, "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one row (upsert, not insert), got %d", len(rows))
	}
	if !rows[0].IsDir {
		t.Fatalf("expected IsDir true after upsert, got false")
	}
	if rows[0].Status != "extracted" {
		t.Fatalf("expected status extracted, got %q", rows[0].Status)
	}
}

// TestUpsertArtifact_FreshDirInsert proves a brand-new dir artifact persists IsDir.
func TestUpsertArtifact_FreshDirInsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.UpsertArtifact(ctx, &Artifact{
		SessionID: "s2", FilePath: "skills/x", IsDir: true, Status: "extracted",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, err := s.ListArtifacts(ctx, "s2")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || !rows[0].IsDir {
		t.Fatalf("expected one dir row, got %#v", rows)
	}
}
