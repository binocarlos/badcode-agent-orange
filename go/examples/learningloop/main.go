// Command learningloop runs the Slice-0 learning narrative and prints the
// before/after + the versioned story. Offline, deterministic, no real side effects.
package main

import (
	"context"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func main() {
	ctx := context.Background()
	board := orchestrator.NewMemBoard()
	if _, err := board.Append(ctx, orchestrator.SeedFragment("routing-guidance", "Be basic.")); err != nil {
		panic(err)
	}

	mgr := &orchestrator.Runner{
		Board:     board,
		Telemetry: orchestrator.NewTelemetry(),
		Model: &orchestrator.ScriptedModel{Default: "dumb plan", Rules: []orchestrator.Rule{
			{Contains: "clever", Reply: "clever plan"},
		}},
	}
	scope := orchestrator.Scope{
		Name:     "manager",
		Template: "{{fragment:routing-guidance}}\nGoal: {{input}}",
		Input:    "grow the brand",
	}

	before, err := mgr.RunScope(ctx, scope)
	if err != nil {
		panic(err)
	}
	fmt.Printf("BEFORE (pinned %s): %q\n", before.BoardRevision, before.Output)

	reviser := &orchestrator.ScriptedModel{Default: "Be basic.", Rules: []orchestrator.Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."},
	}}
	if _, err := orchestrator.ApplyFeedback(ctx, board, reviser, "routing-guidance", "stop being dumb, be more clever"); err != nil {
		panic(err)
	}

	after, err := mgr.RunScope(ctx, scope)
	if err != nil {
		panic(err)
	}
	fmt.Printf("AFTER  (pinned %s): %q\n", after.BoardRevision, after.Output)

	fmt.Println("\n--- the story (current board) ---")
	cur, _ := board.Current(ctx)
	for _, f := range cur.Fragments {
		fmt.Printf("fragment %q now: %q (last changed in %s)\n", f.ID, f.Body, f.LastChangedIn)
	}
}
