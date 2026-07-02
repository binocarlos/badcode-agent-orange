package orchestrator

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// TestLearningNarrative is the winning demo as an automated test: the manager is
// dumb, a human leaves a note, and the manager is then clever — with every step
// versioned and pinned so the story is auditable.
func TestLearningNarrative(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	seed, _ := board.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed guidance",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be basic.")}})

	manager := &Runner{
		Board:     board,
		Telemetry: NewTelemetry(),
		Model: &ScriptedModel{Default: "dumb plan", Rules: []Rule{
			{Contains: "clever", Reply: "clever plan"},
		}},
	}
	scope := Scope{Name: "manager", Template: "{{fragment:routing-guidance}}\nGoal: {{input}}", Input: "grow the brand"}

	before, err := manager.RunScope(ctx, scope)
	if err != nil {
		t.Fatalf("before: %v", err)
	}

	reviser := &ScriptedModel{Default: "Be basic.", Rules: []Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."},
	}}
	noteRev, err := ApplyFeedback(ctx, board, reviser, "routing-guidance", "stop being dumb, be more clever")
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}

	after, err := manager.RunScope(ctx, scope)
	if err != nil {
		t.Fatalf("after: %v", err)
	}

	// The behaviour changed.
	if before.Output != "dumb plan" || after.Output != "clever plan" {
		t.Fatalf("no learning: before=%q after=%q", before.Output, after.Output)
	}
	// Each run is pinned to a different board revision (the cause is auditable).
	if before.BoardRevision != seed || after.BoardRevision != noteRev || seed == noteRev {
		t.Fatalf("pins wrong: before=%s after=%s seed=%s note=%s", before.BoardRevision, after.BoardRevision, seed, noteRev)
	}
	// AsOf folds the exact guidance each run saw — the before/after is reproducible.
	pre, _ := board.AsOf(ctx, seed)
	post, _ := board.AsOf(ctx, noteRev)
	if pre.Fragments[0].Body != "Be basic." || post.Fragments[0].Body == pre.Fragments[0].Body {
		t.Fatalf("history not auditable: pre=%q post=%q", pre.Fragments[0].Body, post.Fragments[0].Body)
	}
}
