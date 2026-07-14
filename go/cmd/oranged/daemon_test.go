package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// newTestDaemon wires a Daemon over in-memory stores and scripted models: the
// plan reply is keyed on the manager role line of the default plan template,
// verify falls through to PASS, the worker always drafts.
func newTestDaemon(t *testing.T) (*Daemon, *orchestrator.MemBoard, *orchestrator.MemTickets, *orchestrator.MemTelemetry) {
	t.Helper()
	ctx := context.Background()
	board := orchestrator.NewMemBoard()
	if _, err := orchestrator.WriteFragment(ctx, board,
		orchestrator.RoutingFragmentID, "seed guidance", "human", "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tickets := orchestrator.NewMemTickets()
	tel := orchestrator.NewTelemetry()
	planJSON := `[{"title":"draft","objective":"write the draft","acceptance":"has a title"}]`
	router := orchestrator.NewTierRouter(map[orchestrator.ModelTier]orchestrator.Model{
		orchestrator.TierFull: &orchestrator.ScriptedModel{
			Default: "PASS: fine",
			Rules:   []orchestrator.Rule{{Contains: "You are the manager", Reply: planJSON}},
		},
		orchestrator.TierMid: &orchestrator.ScriptedModel{Default: "the draft"},
	})
	ledger := orchestrator.NewSpawnLedger()
	budget := orchestrator.Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 100000}
	ex := &orchestrator.ManagerExchange{
		Board: board, Tickets: tickets, Router: router,
		Runtime: &orchestrator.InProcRuntime{Board: board, Router: router,
			Sink: &orchestrator.TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: tel},
		Ledger: ledger, Telemetry: tel,
		ProjectID: "test", ManagerSession: "mgr",
		PlanTier: orchestrator.TierFull, WorkerTier: orchestrator.TierMid, VerifyTier: orchestrator.TierFull,
		WorkerBudget:   budget,
		PlanTemplate:   defaultPlanTemplate,
		WorkerTemplate: defaultWorkerTemplate,
		Channel:        "drafts", DefaultDisposition: orchestrator.DispositionPublish,
	}
	cons := consultantScope{Board: board, Tickets: tickets, Tel: tel,
		Model: &orchestrator.ScriptedModel{Default: "OK"}}
	d, err := NewDaemon(ex, board, tel, orchestrator.NewMemSpendMeter(100), cons, time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	return d, board, tickets, tel
}

func TestNewDaemonValidatesGateWiring(t *testing.T) {
	ex := &orchestrator.ManagerExchange{Channel: "", DefaultDisposition: orchestrator.DispositionPublish}
	if _, err := NewDaemon(ex, nil, nil, nil, consultantScope{}, time.Minute, time.Hour); err == nil {
		t.Fatalf("empty Channel must fail loud")
	}
	ex = &orchestrator.ManagerExchange{Channel: "drafts", DefaultDisposition: orchestrator.DispositionInternal}
	if _, err := NewDaemon(ex, nil, nil, nil, consultantScope{}, time.Minute, time.Hour); err == nil {
		t.Fatalf("non-publish DefaultDisposition must fail loud")
	}
}

func TestTickSkipsWithoutGoal(t *testing.T) {
	ctx := context.Background()
	d, _, tickets, tel := newTestDaemon(t)
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	st := d.Status(ctx)
	if len(st.Ticks) != 1 || !st.Ticks[0].Skipped {
		t.Fatalf("skipped tick not recorded: %+v", st.Ticks)
	}
	// Zero work happened: no tickets planned, no model runs recorded.
	if all, _ := tickets.List(ctx, ""); len(all) != 0 {
		t.Fatalf("no tickets should exist: %+v", all)
	}
	if runs, _ := tel.Runs(ctx); len(runs) != 0 {
		t.Fatalf("no runs should exist: %+v", runs)
	}
}

func TestSetGoalPersistsAndTickPlans(t *testing.T) {
	ctx := context.Background()
	d, board, tickets, _ := newTestDaemon(t)
	rev, err := d.SetGoal(ctx, "  write a launch post  ")
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if d.Goal() != "write a launch post" {
		t.Fatalf("goal not trimmed/cached: %q", d.Goal())
	}
	cur, _ := board.Current(ctx)
	if cur.Revision != rev {
		t.Fatalf("goal must be a board revision: head=%s rev=%s", cur.Revision, rev)
	}
	var found bool
	for _, f := range cur.Fragments {
		if f.ID == goalFragmentID && f.Body == "write a launch post" {
			found = true
		}
	}
	if !found {
		t.Fatalf("goal fragment missing: %+v", cur.Fragments)
	}

	if err := d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	st := d.Status(ctx)
	if st.Ticks[0].Skipped || st.Ticks[0].Report.Planned != 1 || st.Ticks[0].Report.Spawned != 1 {
		t.Fatalf("tick report wrong: %+v", st.Ticks[0])
	}
	all, _ := tickets.List(ctx, "")
	if len(all) != 1 || all[0].Status != orchestrator.StatusInReview {
		t.Fatalf("planned ticket wrong: %+v", all)
	}
}

func TestSetGoalRejectsEmptyAndOversized(t *testing.T) {
	ctx := context.Background()
	d, _, _, _ := newTestDaemon(t)
	if _, err := d.SetGoal(ctx, "   "); err == nil {
		t.Fatalf("empty goal must be rejected")
	}
	if _, err := d.SetGoal(ctx, strings.Repeat("x", orchestrator.MaxFragmentLen+1)); err == nil {
		t.Fatalf("oversized goal must be rejected")
	}
	if d.Goal() != "" {
		t.Fatalf("rejected goals must not stick: %q", d.Goal())
	}
}

func TestGoalRestoredFromBoard(t *testing.T) {
	ctx := context.Background()
	d1, board, tickets, tel := newTestDaemon(t)
	if _, err := d1.SetGoal(ctx, "grow the brand"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// A second daemon over the same stores (a restart) restores the goal and
	// ignores the initial (env) goal.
	d2, err := NewDaemon(d1.ex, board, tel, orchestrator.NewMemSpendMeter(100), d1.cons, time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d2.loadGoal(ctx, "an env goal that must lose"); err != nil {
		t.Fatalf("loadGoal: %v", err)
	}
	if d2.Goal() != "grow the brand" {
		t.Fatalf("goal not restored: %q", d2.Goal())
	}
	_ = tickets

	// A fresh board falls back to the initial goal and persists it.
	d3, _, _, _ := newTestDaemon(t)
	if err := d3.loadGoal(ctx, "the env goal"); err != nil {
		t.Fatalf("loadGoal: %v", err)
	}
	if d3.Goal() != "the env goal" {
		t.Fatalf("initial goal not applied: %q", d3.Goal())
	}
}

func TestReviewGatesOnFreshEvidence(t *testing.T) {
	ctx := context.Background()
	d, _, _, tel := newTestDaemon(t)

	// No evidence at all → skipped, no consultant run recorded.
	if _, _, skipped, err := d.Review(ctx); err != nil || !skipped {
		t.Fatalf("empty telemetry must skip: skipped=%v err=%v", skipped, err)
	}
	if runs, _ := tel.Runs(ctx); len(runs) != 0 {
		t.Fatalf("skip must not call the model: %+v", runs)
	}

	// Fresh worker evidence → the review fires (and records its own run).
	_, _ = tel.Record(ctx, orchestrator.Run{Scope: "worker", Output: "a draft"})
	if _, _, skipped, err := d.Review(ctx); err != nil || skipped {
		t.Fatalf("fresh evidence must review: skipped=%v err=%v", skipped, err)
	}
	runs, _ := tel.Runs(ctx)
	if len(runs) != 2 || runs[1].Scope != "consultant" {
		t.Fatalf("consultant run not recorded: %+v", runs)
	}

	// The consultant's own run is not evidence → the next review skips.
	if _, _, skipped, err := d.Review(ctx); err != nil || !skipped {
		t.Fatalf("no new evidence must skip: skipped=%v err=%v", skipped, err)
	}
	if runs, _ := tel.Runs(ctx); len(runs) != 2 {
		t.Fatalf("skip must not add runs: %+v", runs)
	}
}

func TestRecoverStrandedRevertsInProgress(t *testing.T) {
	ctx := context.Background()
	d, _, tickets, _ := newTestDaemon(t)
	id, _ := tickets.Create(ctx, orchestrator.Ticket{Title: "stranded", Status: orchestrator.StatusInProgress})
	done, _ := tickets.Create(ctx, orchestrator.Ticket{Title: "finished", Status: orchestrator.StatusDone})
	if err := d.recoverStranded(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if tk, _ := tickets.Get(ctx, id); tk.Status != orchestrator.StatusTodo || tk.Attempts != 0 {
		t.Fatalf("stranded ticket must revert to todo without burning an attempt: %+v", tk)
	}
	if tk, _ := tickets.Get(ctx, done); tk.Status != orchestrator.StatusDone {
		t.Fatalf("terminal tickets must be untouched: %+v", tk)
	}
}

func TestStatusRingTrimsAndOrdersNewestFirst(t *testing.T) {
	ctx := context.Background()
	d, _, _, _ := newTestDaemon(t)
	seq := 0
	d.now = func() time.Time { seq++; return time.Unix(int64(seq), 0).UTC() }
	for range reportKeep + 5 {
		_ = d.Tick(ctx) // skipped ticks (no goal) — cheap ring fillers
	}
	st := d.Status(ctx)
	if len(st.Ticks) != reportKeep {
		t.Fatalf("ring not trimmed: %d", len(st.Ticks))
	}
	if !st.Ticks[0].At.After(st.Ticks[1].At) {
		t.Fatalf("ticks must be newest first: %v then %v", st.Ticks[0].At, st.Ticks[1].At)
	}
}
