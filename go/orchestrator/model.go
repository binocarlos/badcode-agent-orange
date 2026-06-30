package orchestrator

import (
	"context"
	"strings"
)

// Model turns a composed prompt into text. The real impl (later slice) wraps the
// Claude Agent SDK harness; Slice 0 uses ScriptedModel for deterministic tests.
type Model interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// Rule fires Reply when Contains is a substring of the prompt.
type Rule struct {
	Contains string
	Reply    string
}

// ScriptedModel is a deterministic offline Model: first matching rule wins, else
// Default. It lets a test prove behaviour changed because the composed prompt (a
// fragment) changed — not because of any nondeterminism.
type ScriptedModel struct {
	Rules   []Rule
	Default string
}

// Run returns the first matching rule's reply, or Default.
func (s *ScriptedModel) Run(_ context.Context, prompt string) (string, error) {
	for _, r := range s.Rules {
		if strings.Contains(prompt, r.Contains) {
			return r.Reply, nil
		}
	}
	return s.Default, nil
}

var _ Model = (*ScriptedModel)(nil)
