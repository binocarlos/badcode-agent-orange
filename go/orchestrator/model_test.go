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
	got, _ := m.Run(context.Background(), "guidance: Be clever.\nGoal: x")
	if got != "clever plan" {
		t.Fatalf("got %q, want clever plan", got)
	}
	got, _ = m.Run(context.Background(), "guidance: Be basic.\nGoal: x")
	if got != "dumb plan" {
		t.Fatalf("got %q, want dumb plan", got)
	}
}
