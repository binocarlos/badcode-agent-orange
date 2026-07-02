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

// §10c I-7: a fresh board must not reject its first lesson. A ticket:/run:
// target with no routing-guidance fragment SEEDS it (note text as initial body,
// author "human-feedback") instead of erroring.
func TestApplyHumanFeedbackSeedsRoutingGuidanceOnFreshBoard(t *testing.T) {
	ctx := context.Background()

	// Entirely fresh board: zero revisions.
	board := NewMemBoard()
	reviser := &ScriptedModel{Default: "should not be called"}
	rev, err := ApplyHumanFeedback(ctx, board, reviser, HumanFeedback{
		TargetRef: "ticket:t1", Note: "always mention the demo",
	})
	if err != nil {
		t.Fatalf("first lesson rejected: %v", err)
	}
	if rev != "r1" {
		t.Fatalf("seed revision = %q, want r1", rev)
	}
	cur, _ := board.Current(ctx)
	if len(cur.Fragments) != 1 || cur.Fragments[0].ID != RoutingFragmentID ||
		cur.Fragments[0].Body != "always mention the demo" {
		t.Fatalf("routing-guidance not seeded with note body: %+v", cur.Fragments)
	}
	revs, _ := board.Revisions(ctx)
	if revs[0].Author != "human-feedback" {
		t.Fatalf("seed author = %q, want human-feedback", revs[0].Author)
	}

	// Non-empty board that lacks routing-guidance: run: target seeds it too.
	board2 := NewMemBoard()
	_, _ = board2.Append(ctx, SeedFragment("post-writer-role", "Write posts."))
	if _, err := ApplyHumanFeedback(ctx, board2, reviser, HumanFeedback{
		TargetRef: "run:run1", Note: "be dry",
	}); err != nil {
		t.Fatalf("seed on partial board: %v", err)
	}
	cur2, _ := board2.Current(ctx)
	var found bool
	for _, f := range cur2.Fragments {
		if f.ID == RoutingFragmentID && f.Body == "be dry" {
			found = true
		}
	}
	if !found {
		t.Fatalf("routing-guidance not seeded on partial board: %+v", cur2.Fragments)
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
