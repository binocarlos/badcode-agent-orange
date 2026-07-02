package orchestrator

import (
	"context"
	"testing"
)

func TestScriptedModelMatchesFirstRule(t *testing.T) {
	m := &ScriptedModel{
		Default: "dumb plan",
		Rules:   []Rule{{Contains: "clever", Reply: "clever plan"}},
	}
	got, _, _ := m.Run(context.Background(), "guidance: Be clever.\nGoal: x")
	if got != "clever plan" {
		t.Fatalf("got %q, want clever plan", got)
	}
	got, _, _ = m.Run(context.Background(), "guidance: Be basic.\nGoal: x")
	if got != "dumb plan" {
		t.Fatalf("got %q, want dumb plan", got)
	}
}

// §10c §A: the offline double reports deterministic pseudo-usage — len/4 per side,
// floored to 1 when the respective text is non-empty — so budget mechanics stay
// testable without a real tokenizer.
func TestScriptedModelPseudoUsage(t *testing.T) {
	ctx := context.Background()

	// len(prompt)=20 → 5 input; len(reply)=8 → 2 output.
	m := &ScriptedModel{Default: "12345678"}
	_, u, err := m.Run(ctx, "12345678901234567890")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if u.InputTokens != 5 || u.OutputTokens != 2 || u.Total() != 7 {
		t.Fatalf("usage = %+v (total %d), want {5 2} total 7", u, u.Total())
	}

	// Short non-empty text floors to 1 token per side.
	short := &ScriptedModel{Default: "ok"}
	_, u, _ = short.Run(ctx, "x")
	if u.InputTokens != 1 || u.OutputTokens != 1 {
		t.Fatalf("short usage = %+v, want {1 1}", u)
	}

	// Empty text counts zero (no floor when there is nothing to tokenize).
	empty := &ScriptedModel{Default: ""}
	_, u, _ = empty.Run(ctx, "")
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Fatalf("empty usage = %+v, want {0 0}", u)
	}
}
