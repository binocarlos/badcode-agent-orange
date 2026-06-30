package orchestrator

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestRunnerRunsScopePinnedToHead(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	rev, _ := board.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be clever.")}})

	r := &Runner{
		Board:     board,
		Model:     &ScriptedModel{Default: "dumb plan", Rules: []Rule{{Contains: "clever", Reply: "clever plan"}}},
		Telemetry: NewTelemetry(),
	}
	run, err := r.RunScope(ctx, Scope{
		Name:     "manager",
		Template: "{{fragment:routing-guidance}}\nGoal: {{input}}",
		Input:    "grow the brand",
	})
	if err != nil {
		t.Fatalf("runscope: %v", err)
	}
	if run.Output != "clever plan" {
		t.Fatalf("output = %q, want clever plan", run.Output)
	}
	if run.BoardRevision != rev {
		t.Fatalf("pinned to %q, want %q", run.BoardRevision, rev)
	}
	if len(r.Telemetry.Runs()) != 1 {
		t.Fatalf("expected 1 recorded run")
	}
}
