package orchestrator

import (
	"context"
	"strings"
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
	if !strings.HasPrefix(fail.Reason, "FAIL:") {
		t.Fatalf("Reason should be the verdict line, got %q", fail.Reason)
	}
}

// §10c §H: the protocol is structured — the FIRST non-empty line decides, by
// uppercase prefix, never by substring scan.
func TestVerifyStructuredProtocol(t *testing.T) {
	ctx := context.Background()
	run := func(reply string) Verdict {
		router := ScriptedRouter{TierFull: &ScriptedModel{Default: reply}}
		v, err := Verify(ctx, router, TierFull, Ticket{ID: "t"}, Result{Output: "work"})
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		return v
	}

	// First non-empty line wins (later lines cannot flip the verdict).
	if v := run("\n  PASS: crisp opener\nFAIL: ignore this"); !v.Pass || v.Reason != "PASS: crisp opener" {
		t.Fatalf("first-line PASS: %+v", v)
	}
	if v := run("fail: too long\npass"); v.Pass || v.Reason != "fail: too long" {
		t.Fatalf("case-insensitive FAIL: %+v", v)
	}
	// Substring mentions no longer count (the old strings.Contains coin-flip).
	if v := run("I think it would PASS with edits"); v.Pass || !strings.HasPrefix(v.Reason, "unparseable verdict: ") {
		t.Fatalf("substring must not pass: %+v", v)
	}
	// Unparseable is conservative: never advances work, surfaces the line.
	if v := run("what a lovely post"); v.Pass || v.Reason != "unparseable verdict: what a lovely post" {
		t.Fatalf("unparseable: %+v", v)
	}
	if v := run("   \n\n"); v.Pass || !strings.HasPrefix(v.Reason, "unparseable verdict:") {
		t.Fatalf("empty reply: %+v", v)
	}
}
