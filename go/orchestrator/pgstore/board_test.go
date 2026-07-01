package pgstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pgstore.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&agentdb.BoardRevision{}, &agentdb.BoardHead{}, &agentdb.BoardPromptFragment{},
		&agentdb.Ticket{}, &agentdb.TelemetryRun{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func fragOp(kind agentdb.OpKind, id, body string) agentdb.Op {
	b, _ := json.Marshal(agentdb.BoardPromptFragment{ID: id, Kind: "role", Body: body})
	return agentdb.Op{Op: kind, EntityType: "prompt_fragment", EntityID: id, Body: b}
}

// TestPgBoardAppendFoldAndPin mirrors orchestrator.TestMemBoardAppendFoldAndPin.
func TestPgBoardAppendFoldAndPin(t *testing.T) {
	ctx := context.Background()
	b := NewPgBoard(newTestDB(t))

	r1, err := b.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be basic.")}})
	if err != nil {
		t.Fatalf("append r1: %v", err)
	}
	if r1 != "r1" {
		t.Fatalf("first revision id = %q, want r1", r1)
	}

	r2, err := b.Append(ctx, agentdb.Changeset{Author: "human", Message: "note",
		Ops: []agentdb.Op{fragOp(agentdb.OpUpdate, "routing-guidance", "Be clever.")}})
	if err != nil {
		t.Fatalf("append r2: %v", err)
	}
	if r2 != "r2" {
		t.Fatalf("second revision id = %q, want r2", r2)
	}

	cur, err := b.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if cur.Revision != r2 || len(cur.Fragments) != 1 || cur.Fragments[0].Body != "Be clever." {
		t.Fatalf("current folded wrong: rev=%s frags=%+v", cur.Revision, cur.Fragments)
	}
	if cur.Fragments[0].LastChangedIn != r2 {
		t.Fatalf("LastChangedIn = %q, want r2", cur.Fragments[0].LastChangedIn)
	}

	as1, err := b.AsOf(ctx, r1)
	if err != nil {
		t.Fatalf("asof: %v", err)
	}
	if len(as1.Fragments) != 1 || as1.Fragments[0].Body != "Be basic." {
		t.Fatalf("asof r1 wrong: %+v", as1.Fragments)
	}
	if head, _ := b.Head(ctx); head != r2 {
		t.Fatalf("head = %q, want r2", head)
	}
}

// TestPgBoardRemoveFolds mirrors MemBoard's remove behaviour.
func TestPgBoardRemoveFolds(t *testing.T) {
	ctx := context.Background()
	b := NewPgBoard(newTestDB(t))
	_, _ = b.Append(ctx, agentdb.Changeset{Author: "h", Message: "add",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "f1", "one")}})
	_, _ = b.Append(ctx, agentdb.Changeset{Author: "h", Message: "rm",
		Ops: []agentdb.Op{{Op: agentdb.OpRemove, EntityType: "prompt_fragment", EntityID: "f1"}}})
	cur, err := b.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if len(cur.Fragments) != 0 {
		t.Fatalf("expected fragment removed, got %+v", cur.Fragments)
	}
}

// TestPgBoardRevisionsInSeqOrder mirrors orchestrator.TestMemBoardRevisionsInSeqOrder.
func TestPgBoardRevisionsInSeqOrder(t *testing.T) {
	ctx := context.Background()
	b := NewPgBoard(newTestDB(t))
	r1, _ := b.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be basic.")}})
	r2, _ := b.Append(ctx, agentdb.Changeset{Author: "human-feedback", Message: "be clever",
		Ops: []agentdb.Op{fragOp(agentdb.OpUpdate, "routing-guidance", "Be clever.")}})

	revs, err := b.Revisions(ctx)
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if len(revs) != 2 || revs[0].ID != r1 || revs[1].ID != r2 {
		t.Fatalf("revisions not in seq order: %+v (want %s, %s)", revs, r1, r2)
	}
	if revs[0].Seq >= revs[1].Seq {
		t.Fatalf("seq not ascending: %d then %d", revs[0].Seq, revs[1].Seq)
	}
	if revs[0].Author != "human" || revs[1].Message != "be clever" {
		t.Fatalf("revision metadata lost: %+v", revs)
	}
}

// TestMemVsPgParity feeds identical changesets to both impls and asserts equal folds.
func TestMemVsPgParity(t *testing.T) {
	ctx := context.Background()
	mem := orchestrator.NewMemBoard()
	pg := NewPgBoard(newTestDB(t))
	changes := []agentdb.Changeset{
		{Author: "h", Message: "seed", Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing", "Be basic.")}},
		{Author: "h", Message: "role", Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "role", "Writer.")}},
		{Author: "h", Message: "note", Ops: []agentdb.Op{fragOp(agentdb.OpUpdate, "routing", "Be clever.")}},
	}
	for i, cs := range changes {
		mr, err := mem.Append(ctx, cs)
		if err != nil {
			t.Fatalf("mem append %d: %v", i, err)
		}
		pr, err := pg.Append(ctx, cs)
		if err != nil {
			t.Fatalf("pg append %d: %v", i, err)
		}
		if mr != pr {
			t.Fatalf("revision id mismatch at %d: mem=%q pg=%q", i, mr, pr)
		}
	}
	mc, _ := mem.Current(ctx)
	pc, _ := pg.Current(ctx)
	if mc.Revision != pc.Revision || len(mc.Fragments) != len(pc.Fragments) {
		t.Fatalf("parity broke: mem=%+v pg=%+v", mc, pc)
	}
	for i := range mc.Fragments {
		if mc.Fragments[i] != pc.Fragments[i] {
			t.Fatalf("fragment %d differs: mem=%+v pg=%+v", i, mc.Fragments[i], pc.Fragments[i])
		}
	}
}
