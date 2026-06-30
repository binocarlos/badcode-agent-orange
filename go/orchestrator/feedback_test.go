package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestApplyFeedbackWritesDeltaRevision(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be basic.")}})

	// Reviser mock: when the note mentions "clever", append the clever steer.
	reviser := &ScriptedModel{Default: "Be basic.", Rules: []Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."},
	}}
	rev, err := ApplyFeedback(ctx, board, reviser, "routing-guidance", "make it more clever")
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if rev != "r2" {
		t.Fatalf("rev = %q, want r2", rev)
	}
	cur, _ := board.Current(ctx)
	if !strings.Contains(cur.Fragments[0].Body, "clever") {
		t.Fatalf("body not revised: %q", cur.Fragments[0].Body)
	}

	// Guard: a reviser that returns empty must not wipe the fragment.
	empty := &ScriptedModel{Default: ""}
	if _, err := ApplyFeedback(ctx, board, empty, "routing-guidance", "x"); err == nil {
		t.Fatalf("expected error on empty revision")
	}
	// Guard: unknown fragment id errors.
	if _, err := ApplyFeedback(ctx, board, reviser, "nope", "x"); err == nil {
		t.Fatalf("expected error on unknown fragment")
	}
}
