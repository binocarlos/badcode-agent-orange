package orchestrator

import (
	"context"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Scope is one manager/worker invocation: a named prompt template + its input.
// (The full Scope of ARCHITECTURE.md §7 also carries tools/model/budget; Slice 0
// needs only the compose inputs.)
type Scope struct {
	Name     string
	Template string
	Input    string
}

// Runner composes a scope's prompt from the current board, runs the model, and
// records the run pinned to the board revision it ran against.
type Runner struct {
	Board     agentdb.BoardStore
	Model     Model
	Telemetry *Telemetry
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
	return r.Telemetry.Record(Run{
		Scope: s.Name, BoardRevision: board.Revision, Prompt: prompt, Output: out,
	}), nil
}
