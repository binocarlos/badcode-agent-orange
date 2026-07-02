package orchestrator

import (
	"context"
	"strings"
)

// The Model seam is declared in contracts.go. Slice 0 uses ScriptedModel, a
// deterministic offline impl, so a test can prove behaviour changed because the
// composed prompt (a fragment) changed — not because of any nondeterminism.

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

// Run returns the first matching rule's reply, or Default, plus a deterministic
// pseudo-usage (§10c §A) so ledger/budget mechanics stay testable offline.
func (s *ScriptedModel) Run(_ context.Context, prompt string) (string, Usage, error) {
	reply := s.Default
	for _, r := range s.Rules {
		if strings.Contains(prompt, r.Contains) {
			reply = r.Reply
			break
		}
	}
	return reply, pseudoUsage(prompt, reply), nil
}

// pseudoUsage is the §10c §A deterministic offline usage: len/4 per side, floored
// to 1 when the respective text is non-empty (so no real call ever counts as free).
func pseudoUsage(prompt, reply string) Usage {
	return Usage{
		InputTokens:  pseudoTokens(prompt),
		OutputTokens: pseudoTokens(reply),
	}
}

func pseudoTokens(text string) int64 {
	if text == "" {
		return 0
	}
	if n := int64(len(text) / 4); n > 0 {
		return n
	}
	return 1
}

var _ Model = (*ScriptedModel)(nil)
