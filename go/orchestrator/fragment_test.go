package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestWriteFragmentGuardsAndAppends(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("routing-guidance", "Be basic."))

	rev, err := WriteFragment(ctx, board, "routing-guidance", "Be clever and dry.", "consultant", "tighten tone")
	if err != nil || rev != "r2" {
		t.Fatalf("write: rev=%q err=%v", rev, err)
	}
	cur, _ := board.Current(ctx)
	if cur.Fragments[0].Body != "Be clever and dry." {
		t.Fatalf("body = %q", cur.Fragments[0].Body)
	}

	// Guard: empty body refuses (never wipe a load-bearing fragment).
	if _, err := WriteFragment(ctx, board, "routing-guidance", "", "x", "y"); err == nil {
		t.Fatalf("expected empty-body refusal")
	}
	// Guard: over-length refuses.
	if _, err := WriteFragment(ctx, board, "routing-guidance", strings.Repeat("x", MaxFragmentLen+1), "x", "y"); err == nil {
		t.Fatalf("expected over-length refusal")
	}
}

func TestApplyHumanFeedbackRoutesTargets(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("routing-guidance", "Be basic."))
	reviser := &ScriptedModel{Default: "Be basic.", Rules: []Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."},
	}}

	// fragment:<id> edits that fragment directly.
	rev, err := ApplyHumanFeedback(ctx, board, reviser, HumanFeedback{
		TargetRef: "fragment:routing-guidance", Note: "make it more clever",
	})
	if err != nil || rev != "r2" {
		t.Fatalf("feedback: rev=%q err=%v", rev, err)
	}
	cur, _ := board.Current(ctx)
	if !strings.Contains(cur.Fragments[0].Body, "clever") {
		t.Fatalf("not revised: %q", cur.Fragments[0].Body)
	}

	// §10b S-3: a ticket:/run: target resolves to the routing-guidance fragment
	// (it does NOT error). A note without "clever" revises via the reviser Default.
	rev2, err := ApplyHumanFeedback(ctx, board, reviser, HumanFeedback{TargetRef: "ticket:t1", Note: "tone down"})
	if err != nil || rev2 != "r3" {
		t.Fatalf("ticket-target feedback: rev=%q err=%v", rev2, err)
	}

	// A malformed target_ref (no ":") is an explicit error.
	if _, err := ApplyHumanFeedback(ctx, board, reviser, HumanFeedback{TargetRef: "bogus", Note: "x"}); err == nil {
		t.Fatalf("expected malformed target_ref error")
	}
}

// HumanFeedbackApplier satisfies the frozen FeedbackApplier seam via the S-3 rule.
func TestHumanFeedbackApplierSatisfiesSeam(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("routing-guidance", "Be basic."))
	var applier FeedbackApplier = HumanFeedbackApplier{
		Board:   board,
		Reviser: &ScriptedModel{Default: "Be basic. Also: be clever."},
	}
	rev, err := applier.Apply(ctx, HumanFeedback{TargetRef: "run:run1", Note: "be clever"})
	if err != nil || rev != "r2" {
		t.Fatalf("apply: rev=%q err=%v", rev, err)
	}
}
