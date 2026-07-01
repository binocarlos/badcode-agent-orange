package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
)

func planRouter(planJSON, workerDraft string) ScriptedRouter {
	return ScriptedRouter{
		TierFull: &ScriptedModel{Default: planJSON},    // manager planning
		TierMid:  &ScriptedModel{Default: workerDraft}, // worker
	}
}

func newTestExchange(t *testing.T, router ScriptedRouter, goal string) (*ManagerExchange, *MemBoard, *MemTickets) {
	t.Helper()
	board := NewMemBoard()
	if _, err := board.Append(context.Background(), SeedFragment("role-writer", "You are a witty writer.")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tickets := NewMemTickets()
	ledger := NewSpawnLedger()
	budget := Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 100000}
	ledger.RegisterRoot("mgr", budget)
	m := &ManagerExchange{
		Board: board, Tickets: tickets, Router: router,
		Runtime: &InProcRuntime{Board: board, Router: router,
			Sink: &TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: NewTelemetry()},
		Ledger: ledger, Telemetry: NewTelemetry(),
		Goal: goal, ProjectID: "p1", ManagerSession: "mgr",
		PlanTier: TierFull, WorkerTier: TierMid, VerifyTier: TierFull,
		WorkerBudget:   budget,
		PlanTemplate:   "Plan this goal into tickets as JSON: {{input}}",
		WorkerTemplate: "{{fragment:role-writer}}\nTask: {{input}}",
		MaxAttempts:    2,
	}
	return m, board, tickets
}

func TestManagerPlansGoalIntoTickets(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"draft launch post","objective":"write a witty launch post","acceptance":"the post must be witty"}]`
	m, board, tickets := newTestExchange(t, planRouter(planJSON, "a witty launch post"), "grow the brand")

	cur, _ := board.Current(ctx)
	n, err := m.plan(ctx, cur)
	if err != nil || n != 1 {
		t.Fatalf("plan: n=%d err=%v", n, err)
	}
	all, _ := tickets.List(ctx, "")
	if len(all) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(all))
	}
	tk := all[0]
	if tk.Status != StatusTodo || tk.Objective != "write a witty launch post" || tk.Acceptance != "the post must be witty" {
		t.Fatalf("planned ticket wrong: %+v", tk)
	}
	var sc Scope
	if err := json.Unmarshal(tk.Scope, &sc); err != nil {
		t.Fatalf("scope unmarshal: %v", err)
	}
	if sc.Tier != TierMid || sc.Parent != "mgr" || sc.TicketID != tk.ID || sc.Input != tk.Objective {
		t.Fatalf("planned scope wrong: %+v", sc)
	}
}

func TestReconcileVerifiesInReviewToDoneOrReplan(t *testing.T) {
	ctx := context.Background()
	// Verify model: PASS when the output is witty, FAIL otherwise. NOTE: the trigger
	// word "witty" must not appear in a FAIL ticket's acceptance, or the acceptance
	// text alone would satisfy the rule. (Fixes a self-contradiction in the plan's
	// verbatim test, where both tickets had acceptance "must be witty".)
	router := ScriptedRouter{TierFull: &ScriptedModel{
		Default: "FAIL: not witty enough",
		Rules:   []Rule{{Contains: "witty", Reply: "PASS: witty"}},
	}}
	m, _, tickets := newTestExchange(t, router, "grow the brand")

	// A passing In-Review ticket.
	passRes, _ := json.Marshal(Result{TicketID: "t1", Output: "a witty draft", Status: ResultDone})
	pid, _ := tickets.Create(ctx, Ticket{Objective: "x", Acceptance: "must be witty", Status: StatusInReview, Result: passRes})

	// A failing In-Review ticket (Attempts near the cap → re-plan then needs-human).
	failRes, _ := json.Marshal(Result{TicketID: "t2", Output: "a dull draft", Status: ResultDone})
	fid, _ := tickets.Create(ctx, Ticket{Objective: "y", Acceptance: "must be formal", Status: StatusInReview, Result: failRes, Attempts: 0})

	v, d, rp, err := m.reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if v != 2 || d != 1 || rp != 1 {
		t.Fatalf("counts: verified=%d done=%d replanned=%d", v, d, rp)
	}
	if got, _ := tickets.Get(ctx, pid); got.Status != StatusDone {
		t.Fatalf("pass ticket = %s, want done", got.Status)
	}
	if got, _ := tickets.Get(ctx, fid); got.Status != StatusTodo || got.Attempts != 1 {
		t.Fatalf("fail ticket = %s attempts=%d, want todo/1", got.Status, got.Attempts)
	}

	// Re-set to in_review to simulate another worker attempt failing again; second
	// failing verify pushes Attempts to MaxAttempts(2) → needs-human (fail-loud).
	f2, _ := tickets.Get(ctx, fid)
	f2.Status = StatusInReview
	_ = tickets.Update(ctx, f2)
	if _, _, _, err := m.reconcile(ctx); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if got, _ := tickets.Get(ctx, fid); got.Status != StatusNeedsHuman {
		t.Fatalf("after 2 fails = %s, want needs_human", got.Status)
	}
}

func TestReconcileClearsBlockedDeps(t *testing.T) {
	ctx := context.Background()
	router := ScriptedRouter{TierFull: &ScriptedModel{Default: "PASS"}}
	m, _, tickets := newTestExchange(t, router, "g")
	dep, _ := tickets.Create(ctx, Ticket{Status: StatusDone})
	blocked, _ := tickets.Create(ctx, Ticket{Status: StatusBlocked, DependsOn: []string{dep}})
	if _, _, _, err := m.reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, _ := tickets.Get(ctx, blocked); got.Status != StatusTodo {
		t.Fatalf("blocked ticket = %s, want todo (deps done)", got.Status)
	}
}

func TestChooseAndSpawnDispatchesTodo(t *testing.T) {
	ctx := context.Background()
	router := planRouter(`[{"title":"draft","objective":"write a witty post","acceptance":"must be witty"}]`, "a witty post")
	m, _, tickets := newTestExchange(t, router, "grow the brand")

	// Plan first (creates the Todo ticket + scope).
	board, _ := m.Board.Current(ctx)
	if _, err := m.plan(ctx, board); err != nil {
		t.Fatalf("plan: %v", err)
	}
	spawned, refused, err := m.chooseAndSpawn(ctx)
	if err != nil || spawned != 1 || refused != 0 {
		t.Fatalf("choose: spawned=%d refused=%d err=%v", spawned, refused, err)
	}
	all, _ := tickets.List(ctx, "")
	// In-proc runtime delivered synchronously → the worker's draft flipped it to In-Review.
	if all[0].Status != StatusInReview {
		t.Fatalf("ticket after spawn = %s, want in_review", all[0].Status)
	}
}

func TestChooseAndSpawnFloorRefusalGoesNeedsHuman(t *testing.T) {
	ctx := context.Background()
	router := planRouter(`[{"title":"draft","objective":"write it","acceptance":"ok"}]`, "draft")
	m, _, tickets := newTestExchange(t, router, "g")

	// Starve the tree budget so the spawn path refuses.
	m.WorkerBudget = Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 0}
	// Rebuild ledger + runtime to use the starved budget.
	m.Ledger = NewSpawnLedger()
	m.Runtime = &InProcRuntime{Board: m.Board, Router: router,
		Sink: &TicketResultSink{Tickets: tickets}, Ledger: m.Ledger, Telemetry: m.Telemetry}
	m.Ledger.RegisterRoot("mgr", m.WorkerBudget)

	board, _ := m.Board.Current(ctx)
	if _, err := m.plan(ctx, board); err != nil {
		t.Fatalf("plan: %v", err)
	}
	spawned, refused, err := m.chooseAndSpawn(ctx)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if spawned != 0 || refused != 1 {
		t.Fatalf("expected 0 spawned/1 refused, got %d/%d", spawned, refused)
	}
	all, _ := tickets.List(ctx, "")
	if all[0].Status != StatusNeedsHuman {
		t.Fatalf("refused ticket = %s, want needs_human", all[0].Status)
	}
}

func TestExchangeTriggerSatisfiesTriggerer(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"draft","objective":"write a witty post","acceptance":"the post must be witty"}]`
	router := ScriptedRouter{
		TierFull: &ScriptedModel{Default: "FAIL", Rules: []Rule{{Contains: "Plan this goal", Reply: planJSON}}},
		TierMid:  &ScriptedModel{Default: "a witty post"},
	}
	m, _, tickets := newTestExchange(t, router, "grow the brand")
	var trig Triggerer = ExchangeTrigger{Exchange: m}
	if err := trig.Tick(ctx); err != nil {
		t.Fatalf("trigger tick: %v", err)
	}
	all, _ := tickets.List(ctx, "")
	if len(all) != 1 {
		t.Fatalf("expected 1 ticket after triggered tick, got %d", len(all))
	}
}
