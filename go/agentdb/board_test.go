package agentdb

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newBoardTestStore returns a Store over a temp sqlite DB with the board log
// tables auto-migrated. (Grows to include the current-state tables in Task 2.)
func newBoardTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "board_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&BoardRevision{}, &BoardHead{}); err != nil {
		t.Fatalf("automigrate board log: %v", err)
	}
	return &Store{gdb: db}
}

func TestBoardRevision_OpsRoundTrip(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()

	ops, err := json.Marshal([]Op{
		{Op: OpAdd, EntityType: "staff", EntityID: "legal-expert", Body: json.RawMessage(`{"model_tier":"mid"}`)},
	})
	if err != nil {
		t.Fatalf("marshal ops: %v", err)
	}
	rev := &BoardRevision{ID: "rev1", Seq: 1, Status: "applied", Author: "kai@x", Message: "init board", Ops: JSONArray(ops)}
	if err := s.gdb.WithContext(ctx).Create(rev).Error; err != nil {
		t.Fatalf("create revision: %v", err)
	}

	var got BoardRevision
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "rev1").Error; err != nil {
		t.Fatalf("read revision: %v", err)
	}
	var decoded []Op
	if err := json.Unmarshal([]byte(got.Ops), &decoded); err != nil {
		t.Fatalf("unmarshal ops: %v", err)
	}
	if len(decoded) != 1 || decoded[0].EntityID != "legal-expert" || decoded[0].Op != OpAdd {
		t.Fatalf("ops round-trip wrong: %+v", decoded)
	}
}

func TestBoardHead_SingleRowPointer(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()
	if err := s.gdb.WithContext(ctx).Create(&BoardHead{Singleton: true, RevisionID: "rev1"}).Error; err != nil {
		t.Fatalf("create head: %v", err)
	}
	var got BoardHead
	if err := s.gdb.WithContext(ctx).First(&got).Error; err != nil {
		t.Fatalf("read head: %v", err)
	}
	if got.RevisionID != "rev1" {
		t.Fatalf("expected head -> rev1, got %q", got.RevisionID)
	}
}
