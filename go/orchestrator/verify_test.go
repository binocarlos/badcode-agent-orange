package orchestrator

import (
	"context"
	"testing"
)

func TestVerifyChecksResultAgainstAcceptance(t *testing.T) {
	ctx := context.Background()
	// The verify model rules on the ACCEPTANCE + OUTPUT text — not the worker's say-so.
	router := ScriptedRouter{TierFull: &ScriptedModel{
		Default: "FAIL: does not meet criteria",
		Rules:   []Rule{{Contains: "witty", Reply: "PASS: reads as witty"}},
	}}

	tk := Ticket{ID: "t1", Acceptance: "the post must be witty"}
	pass, err := Verify(ctx, router, TierFull, tk, Result{Output: "a witty launch post"})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !pass.Pass {
		t.Fatalf("expected PASS, got %+v", pass)
	}

	tkDry := Ticket{ID: "t2", Acceptance: "the post must be formal"}
	fail, _ := Verify(ctx, router, TierFull, tkDry, Result{Output: "a dry corporate memo"})
	if fail.Pass {
		t.Fatalf("expected FAIL, got %+v", fail)
	}
}
