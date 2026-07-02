package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fragOp builds a single fragment op for tests.
func fragOp(kind agentdb.OpKind, id, body string) agentdb.Op {
	b, _ := json.Marshal(agentdb.BoardPromptFragment{ID: id, Kind: "role", Body: body})
	return agentdb.Op{Op: kind, EntityType: "prompt_fragment", EntityID: id, Body: b}
}

func TestMemBoardAppendFoldAndPin(t *testing.T) {
	ctx := context.Background()
	b := NewMemBoard()

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

// §10c I-4: Append fails loud on a changeset carrying a non-prompt_fragment op
// instead of silently discarding the write at fold time.
func TestMemBoardAppendRejectsNonFragmentOps(t *testing.T) {
	ctx := context.Background()
	b := NewMemBoard()
	_, err := b.Append(ctx, agentdb.Changeset{Author: "h", Message: "bad", Ops: []agentdb.Op{
		fragOp(agentdb.OpAdd, "g", "x"),
		{Op: agentdb.OpAdd, EntityType: "staff", EntityID: "s1", Body: json.RawMessage(`{"id":"s1"}`)},
	}})
	if err == nil || !strings.Contains(err.Error(), "staff") {
		t.Fatalf("expected fail-loud rejection naming the entity type, got %v", err)
	}
	// Nothing was appended: the board is still empty.
	if _, err := b.Head(ctx); err == nil {
		t.Fatalf("rejected changeset must not append a revision")
	}
}

func TestMemBoardEmptyAndUnknownRevision(t *testing.T) {
	ctx := context.Background()
	b := NewMemBoard()
	if _, err := b.Current(ctx); err == nil {
		t.Fatalf("expected error on empty board")
	}
	_, _ = b.Append(ctx, SeedFragment("g", "x"))
	if _, err := b.AsOf(ctx, "r99"); err == nil {
		t.Fatalf("expected error on unknown revision")
	}
}
