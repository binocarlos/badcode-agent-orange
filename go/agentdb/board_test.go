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
	if err := db.AutoMigrate(
		&BoardRevision{}, &BoardHead{},
		&BoardStaff{}, &BoardEventType{}, &BoardSubscription{},
		&BoardPipeline{}, &BoardPromptFragment{},
	); err != nil {
		t.Fatalf("automigrate board: %v", err)
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

func TestBoardStaff_RoundTrip(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()
	in := &BoardStaff{
		ID:              "legal-expert",
		RoleFragments:   JSONArray(`["role-legal"]`),
		Skills:          JSONArray(`["search","summarize"]`),
		ModelTier:       "mid",
		MemoryNamespace: "legal",
		SelfArchiving:   JSONMap{"fragment_id": "archive-legal"},
		LastChangedIn:   "rev1",
	}
	if err := s.gdb.WithContext(ctx).Create(in).Error; err != nil {
		t.Fatalf("create staff: %v", err)
	}
	var got BoardStaff
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "legal-expert").Error; err != nil {
		t.Fatalf("read staff: %v", err)
	}
	if got.ModelTier != "mid" || got.MemoryNamespace != "legal" || got.LastChangedIn != "rev1" {
		t.Fatalf("staff round-trip wrong: %+v", got)
	}
	if got.SelfArchiving["fragment_id"] != "archive-legal" {
		t.Fatalf("self_archiving round-trip wrong: %+v", got.SelfArchiving)
	}
}

func TestBoardSubscription_LookupByEventEnabled(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()
	rows := []*BoardSubscription{
		{ID: "archive-on-complete", EventType: "session.completed", ReactionKind: "staff", ReactionRef: "archival-expert", Enabled: true},
		{ID: "plan-on-goal", EventType: "human-goal", ReactionKind: "pipeline", ReactionRef: "interview-plan", Enabled: true},
		{ID: "disabled-one", EventType: "session.completed", ReactionKind: "staff", ReactionRef: "old-bot", Enabled: false},
	}
	for _, r := range rows {
		if err := s.gdb.WithContext(ctx).Create(r).Error; err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}
	// The Routing Manager's hot query: enabled reactions to a given event.
	var got []BoardSubscription
	if err := s.gdb.WithContext(ctx).
		Where("event_type = ? AND enabled = ?", "session.completed", true).
		Find(&got).Error; err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 1 || got[0].ID != "archive-on-complete" {
		t.Fatalf("expected only the enabled session.completed sub, got %+v", got)
	}
}

func TestBoardEventType_EmptyPayloadSchemaIsNoSchema(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()
	if err := s.gdb.WithContext(ctx).Create(&BoardEventType{
		ID: "session.completed", Kind: "lifecycle", Description: "a worker session finished",
	}).Error; err != nil {
		t.Fatalf("create event type: %v", err)
	}
	var got BoardEventType
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "session.completed").Error; err != nil {
		t.Fatalf("read event type: %v", err)
	}
	if got.Kind != "lifecycle" || len(got.PayloadSchema) != 0 {
		t.Fatalf("expected lifecycle + empty payload schema, got %+v", got)
	}
}

// nopBoardStore is a test-only stub proving BoardStore is implementable. It is
// NOT a production implementation (that lands in a later spec).
type nopBoardStore struct{}

func (nopBoardStore) Current(ctx context.Context) (Board, error)                     { return Board{}, nil }
func (nopBoardStore) AsOf(ctx context.Context, revisionID string) (Board, error)     { return Board{Revision: revisionID}, nil }
func (nopBoardStore) Head(ctx context.Context) (string, error)                       { return "", nil }
func (nopBoardStore) Append(ctx context.Context, cs Changeset) (string, error)       { return "", nil }

// Compile-time check: nopBoardStore satisfies BoardStore.
var _ BoardStore = nopBoardStore{}

func TestBoard_AggregateComposesEntities(t *testing.T) {
	b := Board{
		Revision:      "rev1",
		Staff:         []BoardStaff{{ID: "legal-expert"}},
		EventTypes:    []BoardEventType{{ID: "session.completed"}},
		Subscriptions: []BoardSubscription{{ID: "archive-on-complete", EventType: "session.completed"}},
		Pipelines:     []BoardPipeline{{ID: "interview-plan"}},
		Fragments:     []BoardPromptFragment{{ID: "role-legal"}},
	}
	if b.Revision != "rev1" || len(b.Staff) != 1 || len(b.Subscriptions) != 1 {
		t.Fatalf("aggregate did not compose entities: %+v", b)
	}

	var store BoardStore = nopBoardStore{}
	got, err := store.AsOf(context.Background(), "rev9")
	if err != nil {
		t.Fatalf("AsOf: %v", err)
	}
	if got.Revision != "rev9" {
		t.Fatalf("expected AsOf to echo revision, got %q", got.Revision)
	}
}
