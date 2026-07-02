package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	n, err := m.plan(ctx, cur, nil)
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
	if _, err := m.plan(ctx, board, nil); err != nil {
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
	if _, err := m.plan(ctx, board, nil); err != nil {
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

// §10c §B: plan stamps each ticket's Disposition — explicit "disposition" from
// the planned JSON wins; empty takes ManagerExchange.DefaultDisposition.
func TestPlanStampsDisposition(t *testing.T) {
	ctx := context.Background()
	planJSON := `[
		{"title":"launch post","objective":"write it","acceptance":"punchy","disposition":"publish"},
		{"title":"research notes","objective":"collect notes","acceptance":"thorough"}
	]`
	m, board, tickets := newTestExchange(t, planRouter(planJSON, "draft"), "grow the brand")
	m.DefaultDisposition = DispositionInternal

	cur, _ := board.Current(ctx)
	if n, err := m.plan(ctx, cur, nil); err != nil || n != 2 {
		t.Fatalf("plan: n=%d err=%v", n, err)
	}
	all, _ := tickets.List(ctx, "")
	if all[0].Disposition != DispositionPublish {
		t.Fatalf("explicit disposition lost: %q", all[0].Disposition)
	}
	if all[1].Disposition != DispositionInternal {
		t.Fatalf("default disposition not applied: %q", all[1].Disposition)
	}
}

// §10c §G: fenced / prose-wrapped plan output still parses (first '[' .. last ']').
func TestPlanParsesFencedJSON(t *testing.T) {
	ctx := context.Background()
	fenced := "Here is the plan:\n```json\n[{\"title\":\"draft\",\"objective\":\"write it\",\"acceptance\":\"ok\"}]\n```\nDone."
	m, board, tickets := newTestExchange(t, planRouter(fenced, "draft"), "g")
	cur, _ := board.Current(ctx)
	if n, err := m.plan(ctx, cur, nil); err != nil || n != 1 {
		t.Fatalf("fenced plan: n=%d err=%v", n, err)
	}
	all, _ := tickets.List(ctx, "")
	if len(all) != 1 || all[0].Title != "draft" {
		t.Fatalf("fenced plan tickets: %+v", all)
	}
}

// §10c §G: an unparseable plan reply is NOT a tick error — it records a
// "manager-plan-unparseable" run, plans 0, and the tick continues.
func TestPlanUnparseableDoesNotWedgeTheTick(t *testing.T) {
	ctx := context.Background()
	m, _, tickets := newTestExchange(t, planRouter("I cannot produce JSON today.", "draft"), "g")
	tel := m.Telemetry.(*MemTelemetry)

	rep, err := m.Tick(ctx)
	if err != nil {
		t.Fatalf("tick must continue past an unparseable plan: %v", err)
	}
	if rep.Planned != 0 {
		t.Fatalf("planned = %d, want 0", rep.Planned)
	}
	if all, _ := tickets.List(ctx, ""); len(all) != 0 {
		t.Fatalf("no tickets should exist: %+v", all)
	}
	runs, _ := tel.Runs(ctx)
	var found bool
	for _, r := range runs {
		if r.Scope == "manager-plan-unparseable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unparseable plan must be recorded in telemetry: %+v", runs)
	}
}

// §10c §G: plan runs EVERY tick; title dedup keeps a repeating plan stable, and a
// genuinely NEW title in a later tick IS created.
func TestPlanEveryTickDedupsAndAcceptsNewTitles(t *testing.T) {
	ctx := context.Background()
	planA := `[{"title":"first post","objective":"write the first post","acceptance":"the post must be witty"}]`
	planAB := `[{"title":"first post","objective":"write the first post","acceptance":"the post must be witty"},
		{"title":"second post","objective":"write a follow-up","acceptance":"the post must be witty"}]`
	router := ScriptedRouter{
		TierFull: &ScriptedModel{
			Default: "FAIL: not witty",
			Rules: []Rule{
				// tick ≥2: the prompt carries the existing-ticket summary ("- [")
				// → the model re-proposes the old ticket plus a NEW one.
				{Contains: "- [", Reply: planAB},
				{Contains: "Plan this goal", Reply: planA},
				{Contains: "witty", Reply: "PASS: witty"},
			},
		},
		TierMid: &ScriptedModel{Default: "a witty draft"},
	}
	m, _, tickets := newTestExchange(t, router, "grow the brand")

	if _, err := m.Tick(ctx); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	if all, _ := tickets.List(ctx, ""); len(all) != 1 {
		t.Fatalf("after tick1: %d tickets, want 1", len(all))
	}
	rep2, err := m.Tick(ctx)
	if err != nil {
		t.Fatalf("tick2: %v", err)
	}
	// "first post" deduped by title; "second post" is new → exactly one planned.
	if rep2.Planned != 1 {
		t.Fatalf("tick2 planned = %d, want 1 (dedup old + create new)", rep2.Planned)
	}
	all, _ := tickets.List(ctx, "")
	if len(all) != 2 || all[1].Title != "second post" {
		t.Fatalf("after tick2: %+v", all)
	}
	// A third tick re-proposes the same titles → nothing new.
	rep3, _ := m.Tick(ctx)
	if rep3.Planned != 0 {
		t.Fatalf("tick3 planned = %d, want 0 (stable under a repeating plan)", rep3.Planned)
	}
	if all, _ := tickets.List(ctx, ""); len(all) != 2 {
		t.Fatalf("tick3 duplicated tickets: %+v", all)
	}
}

// §10c §B: a retry dispatch composes the accumulated AttemptNotes into the worker
// prompt — verify Reasons / reject notes / answers reach the next attempt.
func TestChooseAndSpawnRetryPromptCarriesFeedback(t *testing.T) {
	ctx := context.Background()
	router := planRouter(`[]`, "an improved draft")
	m, _, tickets := newTestExchange(t, router, "g")
	workerTel := m.Runtime.(*InProcRuntime).Telemetry.(*MemTelemetry)

	scope, _ := json.Marshal(Scope{Name: "draft", Template: m.WorkerTemplate, Input: "write a launch post", Tier: TierMid, Budget: m.WorkerBudget})
	_, _ = tickets.Create(ctx, Ticket{
		Title: "draft", Objective: "write a launch post", Status: StatusTodo, Scope: scope,
		AttemptNotes: []string{"FAIL: too dull; add a hook", "use a playful tone"},
	})

	if spawned, _, err := m.chooseAndSpawn(ctx); err != nil || spawned != 1 {
		t.Fatalf("choose: spawned=%d err=%v", spawned, err)
	}
	runs, _ := workerTel.Runs(ctx)
	if len(runs) != 1 {
		t.Fatalf("expected 1 worker run, got %d", len(runs))
	}
	prompt := runs[0].Prompt
	for _, want := range []string{
		"write a launch post",
		"Feedback on previous attempts (address ALL of it):",
		"- FAIL: too dull; add a hook",
		"- use a playful tone",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("retry prompt missing %q:\n%s", want, prompt)
		}
	}
}

// §10c §D: ErrSpendCeiling on spawn reverts the ticket to todo (never stranded
// in_progress, never needs_human), records a manager-spend-halt run, and STOPS
// dispatch for the tick without failing it.
func TestChooseAndSpawnSpendHaltStopsDispatch(t *testing.T) {
	ctx := context.Background()
	m, _, tickets := newTestExchange(t, planRouter(`[]`, ""), "g")
	tel := m.Telemetry.(*MemTelemetry)
	m.Runtime = &InProcRuntime{Board: m.Board,
		Router: failRouter{err: fmt.Errorf("charge: %w", ErrSpendCeiling)},
		Sink:   &TicketResultSink{Tickets: tickets}, Ledger: m.Ledger, Telemetry: NewTelemetry()}

	scope, _ := json.Marshal(Scope{Template: m.WorkerTemplate, Input: "x", Tier: TierMid, Budget: m.WorkerBudget})
	a, _ := tickets.Create(ctx, Ticket{Title: "a", Status: StatusTodo, Scope: scope})
	b, _ := tickets.Create(ctx, Ticket{Title: "b", Status: StatusTodo, Scope: scope})

	spawned, refused, err := m.chooseAndSpawn(ctx)
	if err != nil {
		t.Fatalf("spend halt must not fail the tick: %v", err)
	}
	if spawned != 0 || refused != 0 {
		t.Fatalf("spawned=%d refused=%d, want 0/0", spawned, refused)
	}
	ta, _ := tickets.Get(ctx, a)
	tb, _ := tickets.Get(ctx, b)
	if ta.Status != StatusTodo || tb.Status != StatusTodo {
		t.Fatalf("spend halt must leave tickets todo (a=%s b=%s)", ta.Status, tb.Status)
	}
	if ta.Attempts != 0 {
		t.Fatalf("a transient halt must not burn an attempt: %d", ta.Attempts)
	}
	runs, _ := tel.Runs(ctx)
	var halts int
	for _, r := range runs {
		if r.Scope == "manager-spend-halt" {
			halts++
		}
	}
	if halts != 1 {
		t.Fatalf("want exactly 1 manager-spend-halt run (loop stopped), got %d: %+v", halts, runs)
	}
}

// §10c §D: any other spawn error reverts the ticket (never stranded in_progress),
// records a manager-spawn-error run with the TicketID, and CONTINUES dispatching.
func TestChooseAndSpawnErrorRevertsAndContinues(t *testing.T) {
	ctx := context.Background()
	m, _, tickets := newTestExchange(t, planRouter(`[]`, ""), "g")
	tel := m.Telemetry.(*MemTelemetry)
	m.Runtime = &InProcRuntime{Board: m.Board,
		Router: failRouter{err: errBoom},
		Sink:   &TicketResultSink{Tickets: tickets}, Ledger: m.Ledger, Telemetry: NewTelemetry()}

	scope, _ := json.Marshal(Scope{Template: m.WorkerTemplate, Input: "x", Tier: TierMid, Budget: m.WorkerBudget})
	a, _ := tickets.Create(ctx, Ticket{Title: "a", Status: StatusTodo, Scope: scope})
	b, _ := tickets.Create(ctx, Ticket{Title: "b", Status: StatusTodo, Scope: scope})

	spawned, refused, err := m.chooseAndSpawn(ctx)
	if err != nil || spawned != 0 || refused != 0 {
		t.Fatalf("spawned=%d refused=%d err=%v, want 0/0/nil", spawned, refused, err)
	}
	for _, id := range []string{a, b} {
		if tk, _ := tickets.Get(ctx, id); tk.Status != StatusTodo || tk.Attempts != 0 {
			t.Fatalf("ticket %s stranded: %s attempts=%d", id, tk.Status, tk.Attempts)
		}
	}
	runs, _ := tel.Runs(ctx)
	var errRuns []Run
	for _, r := range runs {
		if r.Scope == "manager-spawn-error" {
			errRuns = append(errRuns, r)
		}
	}
	if len(errRuns) != 2 || errRuns[0].TicketID != a || errRuns[1].TicketID != b {
		t.Fatalf("want 2 attributed manager-spawn-error runs, got %+v", errRuns)
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
