package agentdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newConvTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "conv_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&ConversationIndex{}); err != nil {
		t.Fatalf("automigrate ConversationIndex: %v", err)
	}
	return &Store{gdb: db}
}

func TestGetConversationIndexMeta_AbsentAndPresent(t *testing.T) {
	s := newConvTestStore(t)
	ctx := context.Background()

	// Absent -> (nil, nil)
	meta, err := s.GetConversationIndexMeta(ctx, "missing")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if meta != nil {
		t.Fatalf("expected nil meta for missing session")
	}

	// Insert a row via GORM (no vector/tsv columns) and read meta back.
	row := &ConversationIndex{SessionID: "s1", Customer: "acme", IndexedAt: 123, SourceHash: "abc"}
	if err := s.gdb.WithContext(ctx).Create(row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	meta, err = s.GetConversationIndexMeta(ctx, "s1")
	if err != nil || meta == nil {
		t.Fatalf("expected meta, got %v err=%v", meta, err)
	}
	if meta.IndexedAt != 123 || meta.SourceHash != "abc" {
		t.Fatalf("bad meta: %+v", meta)
	}
}
