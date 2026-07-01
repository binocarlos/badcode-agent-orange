package orchestrator

import (
	"context"
	"testing"
)

// TestManagerLoopGoalToDone is the Slice-C demo as a test: two ticks drive a vague
// goal to a verified, Done ticket — plan, spawn fire-and-forget, then verify next tick.
func TestManagerLoopGoalToDone(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"draft launch post","objective":"write a witty launch post","acceptance":"the post must be witty"}]`
	router := ScriptedRouter{
		TierFull: &ScriptedModel{ // manager plan AND verify share the full tier, keyed by prompt text
			Default: "FAIL: not witty",
			Rules: []Rule{
				{Contains: "Plan this goal", Reply: planJSON},
				{Contains: "witty", Reply: "PASS: reads as witty"}, // verify sees acceptance+output "witty"
			},
		},
		TierMid: &ScriptedModel{Default: "a witty launch post draft"}, // worker
	}
	m, _, tickets := newTestExchange(t, router, "grow the brand")

	// TICK 1: no tickets → plan → spawn (in-proc worker drafts synchronously → In-Review).
	rep1, err := m.Tick(ctx)
	if err != nil {
		t.Fatalf("tick1: %v", err)
	}
	if rep1.Planned != 1 || rep1.Spawned != 1 {
		t.Fatalf("tick1 report: %+v", rep1)
	}
	after1, _ := tickets.List(ctx, "")
	if after1[0].Status != StatusInReview {
		t.Fatalf("after tick1 = %s, want in_review", after1[0].Status)
	}

	// TICK 2: reconcile In-Review → verify PASS → Done. No new work.
	rep2, err := m.Tick(ctx)
	if err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if rep2.Verified != 1 || rep2.Done != 1 {
		t.Fatalf("tick2 report: %+v", rep2)
	}
	final, _ := tickets.List(ctx, "")
	if final[0].Status != StatusDone {
		t.Fatalf("final = %s, want done", final[0].Status)
	}
}

// TestManagerLoopReplansThenNeedsHuman: a failing draft re-plans (back to Todo),
// and on the next attempt failing again hits MaxAttempts → needs-human (fail-loud).
func TestManagerLoopReplansThenNeedsHuman(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"draft","objective":"write a formal post","acceptance":"the post must be FORMAL"}]`
	router := ScriptedRouter{
		TierFull: &ScriptedModel{
			Default: "FAIL: not formal",
			Rules:   []Rule{{Contains: "Plan this goal", Reply: planJSON}},
		},
		TierMid: &ScriptedModel{Default: "a jokey casual draft"}, // never satisfies "formal"
	}
	m, _, tickets := newTestExchange(t, router, "grow the brand") // MaxAttempts=2

	_, _ = m.Tick(ctx) // plan + spawn → In-Review
	_, _ = m.Tick(ctx) // verify FAIL → Attempts=1 → Todo; then choose+spawn again → In-Review
	_, _ = m.Tick(ctx) // verify FAIL → Attempts=2 == MaxAttempts → needs-human

	final, _ := tickets.List(ctx, "")
	if final[0].Status != StatusNeedsHuman {
		t.Fatalf("final = %s (attempts=%d), want needs_human", final[0].Status, final[0].Attempts)
	}
}
