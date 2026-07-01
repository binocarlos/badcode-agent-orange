package orchestrator

import (
	"context"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Scope is declared in contracts.go (the frozen §4 type). RunScope uses its compose
// inputs (Name/Template/Input); the Tier/Tools/Budget/Prompt/Depth fields are for the
// worker-runtime path (Slices C/F) and are inert here.

// Runner composes a scope's prompt from the current board, runs the model, and
// records the run pinned to the board revision it ran against.
type Runner struct {
	Board     agentdb.BoardStore
	Model     Model
	Telemetry Telemetry
}

// RunScope folds the current board, composes the prompt, runs the model, and
// records a Run pinned to the board revision.
func (r *Runner) RunScope(ctx context.Context, s Scope) (Run, error) {
	board, err := r.Board.Current(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("runscope %s: current board: %w", s.Name, err)
	}
	prompt, err := Compose(board, s.Template, s.Input)
	if err != nil {
		return Run{}, fmt.Errorf("runscope %s: %w", s.Name, err)
	}
	out, err := r.Model.Run(ctx, prompt)
	if err != nil {
		return Run{}, fmt.Errorf("runscope %s: model: %w", s.Name, err)
	}
	run, err := r.Telemetry.Record(ctx, Run{
		Scope: s.Name, BoardRevision: board.Revision, Prompt: prompt, Output: out,
	})
	if err != nil {
		return Run{}, fmt.Errorf("runscope %s: telemetry: %w", s.Name, err)
	}
	return run, nil
}
