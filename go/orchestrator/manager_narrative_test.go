package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
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

// TestGoalToPublishedPost is the §10c acceptance narrative: the previously
// disconnected chain, end to end — goal → plan (publish disposition) → worker
// draft → verify PASS → PendingPost on needs_human (NOT Done; nothing published
// yet) → ApprovalService.Approve → the FakeConnector records the publish → the
// ticket is Done carrying the PublishedRef.
func TestGoalToPublishedPost(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"launch post","objective":"write a punchy launch post","acceptance":"the post must be punchy"}]`
	router := ScriptedRouter{
		TierFull: &ScriptedModel{ // plan AND verify share the full tier, keyed by prompt text
			Default: "FAIL: not punchy",
			Rules: []Rule{
				{Contains: "Plan this goal", Reply: planJSON},
				{Contains: "a punchy draft", Reply: "PASS: punchy indeed"}, // verify sees the work
			},
		},
		TierMid: &ScriptedModel{Default: "a punchy draft"}, // worker
	}
	m, _, tickets := newTestExchange(t, router, "grow the brand")
	m.Channel = "bluesky"
	m.DefaultDisposition = DispositionPublish // BadCode marketing: passing work is FOR publishing

	conn := &FakeConnector{}
	approval := NewApprovalService(tickets, conn, NewTelemetry())

	// TICK 1: plan → spawn → draft lands In-Review.
	if _, err := m.Tick(ctx); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	// TICK 2: verify PASS + DispositionPublish → the disposition hop: a
	// PendingPost on needs_human, NOT Done — and NOTHING has published.
	if _, err := m.Tick(ctx); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	all, _ := tickets.List(ctx, "")
	tk := all[0]
	if tk.Status != StatusNeedsHuman || tk.Disposition != DispositionPublish {
		t.Fatalf("verified publishable work must wait at the gate: %+v", tk)
	}
	if len(tk.PendingPost) == 0 || tk.PublishedRef != "" {
		t.Fatalf("want a pending post and no published ref yet: %+v", tk)
	}
	var p Post
	if err := json.Unmarshal(tk.PendingPost, &p); err != nil {
		t.Fatalf("pending post decode: %v", err)
	}
	if p.Channel != "bluesky" || p.Text != "a punchy draft" {
		t.Fatalf("pending post = %+v, want the verified draft on the exchange channel", p)
	}
	if conn.Calls != 0 {
		t.Fatalf("the manager loop must NEVER publish (calls=%d)", conn.Calls)
	}

	// The single human click: Approve publishes exactly once → Done + PublishedRef.
	ref, err := approval.Approve(ctx, tk.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if conn.Calls != 1 || conn.Published[0].Text != "a punchy draft" ||
		conn.Published[0].Channel != "bluesky" || conn.Published[0].DedupeKey != tk.ID {
		t.Fatalf("publish record wrong: calls=%d %+v", conn.Calls, conn.Published)
	}
	done, _ := tickets.Get(ctx, tk.ID)
	if done.Status != StatusDone || done.PublishedRef != ref || len(done.PendingPost) != 0 {
		t.Fatalf("published ticket must be Done with the ref: %+v", done)
	}
}

// TestVerifyFailFeedbackDrivesRetryToPublished is the acceptance sibling: verify
// FAIL → the Reason lands in AttemptNotes → the retry prompt carries it (the
// improving reply is keyed on the feedback text, so a prompt WITHOUT the feedback
// could never produce it) → the improved draft passes → pending → approve →
// published.
func TestVerifyFailFeedbackDrivesRetryToPublished(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"launch post","objective":"write a launch post","acceptance":"the post must have a hook","disposition":"publish"}]`
	const feedback = "FAIL: too dull; open with a hook"
	router := ScriptedRouter{
		TierFull: &ScriptedModel{
			Default: feedback, // verify: the first draft fails with actionable feedback
			Rules: []Rule{
				{Contains: "Plan this goal", Reply: planJSON},
				{Contains: "HOOKED:", Reply: "PASS: opens with a hook"},
			},
		},
		TierMid: &ScriptedModel{
			Default: "a dull first draft",
			// Fires ONLY when the composed prompt carries the verify feedback —
			// the improved draft is unreachable without the learning loop.
			Rules: []Rule{{Contains: "open with a hook", Reply: "HOOKED: an improved draft"}},
		},
	}
	m, _, tickets := newTestExchange(t, router, "grow the brand")
	m.Channel = "bluesky"
	workerTel := m.Runtime.(*InProcRuntime).Telemetry.(*MemTelemetry)

	conn := &FakeConnector{}
	approval := NewApprovalService(tickets, conn, NewTelemetry())

	// TICK 1: plan → dull draft → In-Review.
	if _, err := m.Tick(ctx); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	// TICK 2: verify FAIL → Reason into AttemptNotes → todo → retry spawns with
	// the feedback in the prompt → improved draft → In-Review.
	if _, err := m.Tick(ctx); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	all, _ := tickets.List(ctx, "")
	tk := all[0]
	if len(tk.AttemptNotes) != 1 || tk.AttemptNotes[0] != feedback {
		t.Fatalf("verify Reason must land in AttemptNotes: %+v", tk.AttemptNotes)
	}
	if tk.Attempts != 1 {
		t.Fatalf("the failed attempt must be counted: %d", tk.Attempts)
	}
	runs, _ := workerTel.Runs(ctx)
	if len(runs) != 2 {
		t.Fatalf("want 2 worker runs (first + retry), got %d", len(runs))
	}
	if strings.Contains(runs[0].Prompt, "Feedback on previous attempts") {
		t.Fatalf("first attempt must not carry feedback:\n%s", runs[0].Prompt)
	}
	if !strings.Contains(runs[1].Prompt, "Feedback on previous attempts (address ALL of it):") ||
		!strings.Contains(runs[1].Prompt, feedback) {
		t.Fatalf("retry prompt must carry the verify Reason:\n%s", runs[1].Prompt)
	}
	if runs[1].Output != "HOOKED: an improved draft" {
		t.Fatalf("retry did not improve: %q", runs[1].Output)
	}

	// TICK 3: verify PASS → PendingPost at the gate; approve → published.
	if _, err := m.Tick(ctx); err != nil {
		t.Fatalf("tick3: %v", err)
	}
	pending, _ := tickets.Get(ctx, tk.ID)
	if pending.Status != StatusNeedsHuman || len(pending.PendingPost) == 0 {
		t.Fatalf("improved draft must wait at the gate: %+v", pending)
	}
	ref, err := approval.Approve(ctx, tk.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if conn.Calls != 1 || conn.Published[0].Text != "HOOKED: an improved draft" {
		t.Fatalf("published the wrong draft: %+v", conn.Published)
	}
	done, _ := tickets.Get(ctx, tk.ID)
	if done.Status != StatusDone || done.PublishedRef != ref {
		t.Fatalf("final ticket wrong: %+v", done)
	}
}
