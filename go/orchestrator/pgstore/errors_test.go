package pgstore

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// Mission A: the pgstore error paths — unknown ids, empty boards, unknown
// revisions, and corrupt stored JSON.

func TestPgTicketStoreGetUnknownID(t *testing.T) {
	ts := NewPgTicketStore(newTestDB(t))
	_, err := ts.Get(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), `get "ghost"`) {
		t.Fatalf("get unknown id: err = %v", err)
	}
}

func TestPgTicketStoreUpdateEmptyIDFailsLoud(t *testing.T) {
	ts := NewPgTicketStore(newTestDB(t))
	err := ts.Update(context.Background(), orchestrator.Ticket{Title: "no id"})
	if err == nil || !strings.Contains(err.Error(), "requires an id") {
		t.Fatalf("update empty id: err = %v", err)
	}
}

func TestPgTicketStoreCreatePreservesProvidedIDAndCreatedAt(t *testing.T) {
	ctx := context.Background()
	ts := NewPgTicketStore(newTestDB(t))
	id, err := ts.Create(ctx, orchestrator.Ticket{ID: "explicit-id", Title: "w", CreatedAt: 42,
		Status: orchestrator.StatusTodo})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "explicit-id" {
		t.Fatalf("id = %q, want explicit-id", id)
	}
	got, err := ts.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CreatedAt != 42 {
		t.Fatalf("CreatedAt = %d, want the preset 42", got.CreatedAt)
	}
	// A duplicate explicit id is a store error, never a silent overwrite.
	if _, err := ts.Create(ctx, orchestrator.Ticket{ID: "explicit-id", Title: "again"}); err == nil {
		t.Fatalf("duplicate id create succeeded")
	}
}

func TestPgTicketStoreCreateDefaultsStatusToBacklog(t *testing.T) {
	ctx := context.Background()
	ts := NewPgTicketStore(newTestDB(t))
	id, err := ts.Create(ctx, orchestrator.Ticket{Title: "no status"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _ := ts.Get(ctx, id)
	if got.Status != orchestrator.StatusBacklog {
		t.Fatalf("status = %q, want backlog", got.Status)
	}
}

// fromRow tolerates corrupt stored JSON in attempt_notes / depends_on: the
// unmarshal error is deliberately discarded and the field folds to nil rather
// than wedging every Get/List on one bad row.
func TestFromRowCorruptAttemptNotesFoldsToNil(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	row := agentdb.Ticket{
		ID: "corrupt-1", Title: "bad json", Status: string(orchestrator.StatusTodo),
		Scope: agentdb.JSONArray("{}"), Result: agentdb.JSONArray("{}"), PendingPost: agentdb.JSONArray("{}"),
		AttemptNotes: agentdb.JSONArray(`{not json`), DependsOn: agentdb.JSONArray(`[broken`),
		CreatedAt: 1, UpdatedAt: 1,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert raw row: %v", err)
	}
	ts := NewPgTicketStore(db)
	got, err := ts.Get(ctx, "corrupt-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AttemptNotes != nil || got.DependsOn != nil {
		t.Fatalf("corrupt JSON should fold to nil: notes=%v deps=%v", got.AttemptNotes, got.DependsOn)
	}
}

func TestPgBoardEmptyBoard(t *testing.T) {
	ctx := context.Background()
	b := NewPgBoard(newTestDB(t))

	if _, err := b.Head(ctx); err == nil || !strings.Contains(err.Error(), "board empty") {
		t.Fatalf("Head on empty board: err = %v", err)
	}
	if _, err := b.Current(ctx); err == nil || !strings.Contains(err.Error(), "board empty") {
		t.Fatalf("Current on empty board: err = %v", err)
	}
	revs, err := b.Revisions(ctx)
	if err != nil {
		t.Fatalf("Revisions on empty board: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("Revisions on empty board: %d, want 0", len(revs))
	}
}

func TestPgBoardAsOfUnknownRevision(t *testing.T) {
	ctx := context.Background()
	b := NewPgBoard(newTestDB(t))
	if _, err := b.Append(ctx, agentdb.Changeset{
		Author: "human", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "f1", "hello")},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := b.AsOf(ctx, "r999"); err == nil {
		t.Fatalf("AsOf unknown revision succeeded")
	}
}

// closedDB returns a migrated sqlite DB whose underlying connection is closed —
// every subsequent query errors, driving the stores' db-error branches.
func closedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return db
}

func TestPgStoresSurfaceDBErrors(t *testing.T) {
	ctx := context.Background()
	db := closedDB(t)
	ts := NewPgTicketStore(db)
	b := NewPgBoard(db)
	tel := NewPgTelemetry(db)

	if _, err := ts.Create(ctx, orchestrator.Ticket{Title: "w"}); err == nil {
		t.Fatalf("ticket create on dead DB succeeded")
	}
	if err := ts.Update(ctx, orchestrator.Ticket{ID: "x", Title: "w"}); err == nil {
		t.Fatalf("ticket update on dead DB succeeded")
	}
	if _, err := ts.Get(ctx, "x"); err == nil {
		t.Fatalf("ticket get on dead DB succeeded")
	}
	if _, err := ts.List(ctx, ""); err == nil {
		t.Fatalf("ticket list on dead DB succeeded")
	}
	if _, err := b.Append(ctx, agentdb.Changeset{
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "f1", "x")},
	}); err == nil {
		t.Fatalf("board append on dead DB succeeded")
	}
	if _, err := b.AsOf(ctx, "r1"); err == nil {
		t.Fatalf("board asof on dead DB succeeded")
	}
	if _, err := b.Revisions(ctx); err == nil {
		t.Fatalf("board revisions on dead DB succeeded")
	}
	if _, err := b.Current(ctx); err == nil {
		t.Fatalf("board current on dead DB succeeded")
	}
	if _, err := tel.Record(ctx, orchestrator.Run{Scope: "s"}); err == nil {
		t.Fatalf("telemetry record on dead DB succeeded")
	}
	if _, err := tel.Runs(ctx); err == nil {
		t.Fatalf("telemetry runs on dead DB succeeded")
	}
}
