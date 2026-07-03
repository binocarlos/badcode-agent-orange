//go:build anthropic_e2e

package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveConsultantLearningLoop is the live-model sibling of
// TestConsultantLearningLoopE2E (learning_e2e_test.go): the same five beats —
// event → manager dispatches per policy → worker runs → consultant reviews the
// evidence and revises the policy → a similar event is dispatched differently —
// but every model call hits the REAL Anthropic API, so it exercises the whole
// stack: compose, plan parsing, verify protocol, evidence digest, advice
// protocol, and the board round-trip.
//
// Everything runs on the cheap tier (Haiku unless AGENTKIT_MODEL_CHEAP
// overrides), ~10-15 short calls per run. Excluded from the default build by
// the anthropic_e2e tag; skipped without ANTHROPIC_API_KEY. Run manually:
//
//	cd go && ANTHROPIC_API_KEY=sk-ant-... go test -tags anthropic_e2e ./orchestrator/ -run TestLiveConsultantLearningLoop -v -timeout 600s
//
// Assertions are behavioural, not verbatim: the seeded policy is strict enough
// that dispatch beat 1 and beat 5 are near-deterministic, and the consultant's
// charter states its revision criterion explicitly. A failure prints every run
// (prompt + output) for diagnosis.
func TestLiveConsultantLearningLoop(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("set ANTHROPIC_API_KEY to run the live learning-loop e2e")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	cheap := ModelIDsFromEnv()[TierCheap]
	router := NewAnthropicRouter(RouterConfig{
		APIKey: key,
		ModelIDs: map[ModelTier]string{ // the whole fleet on the cheap tier
			TierFull: cheap, TierMid: cheap, TierCheap: cheap,
		},
		MaxTokens: 1024,
	})

	board := NewMemBoard()
	seedRev, err := WriteFragment(ctx, board, RoutingFragmentID,
		"STRICT DISPATCH POLICY: the available specialists are 'web-generalist' and "+
			"'database-specialist'. You MUST assign every incident to the web-generalist, "+
			"regardless of the incident's nature. Do not deviate.",
		"human", "seed dispatch policy")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	tel := NewTelemetry()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		runs, _ := tel.Runs(context.Background())
		for _, r := range runs {
			t.Logf("run %s [%s] rev=%s ticket=%s\n--- prompt ---\n%s\n--- output ---\n%s",
				r.ID, r.Scope, r.BoardRevision, r.TicketID, r.Prompt, r.Output)
		}
	})

	newExchange := func(session, goal string, tickets *MemTickets) *ManagerExchange {
		ledger := NewSpawnLedger()
		budget := Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 500000}
		return &ManagerExchange{
			Board: board, Tickets: tickets, Router: router,
			Runtime: &InProcRuntime{Board: board, Router: router,
				Sink: &TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: tel},
			Ledger: ledger, Telemetry: tel,
			Goal: goal, ProjectID: "live-e2e", ManagerSession: session,
			PlanTier: TierFull, WorkerTier: TierMid, VerifyTier: TierFull,
			WorkerBudget: budget,
			PlanTemplate: "{{fragment:routing-guidance}}\n\nYou are the incident manager. " +
				"Following the dispatch policy above EXACTLY, assign this incident to ONE " +
				"specialist and plan exactly one ticket. The ticket title MUST start with the " +
				`assigned specialist's name and a colon, e.g. "web-generalist: ..." or ` +
				`"database-specialist: ...". Incident: {{input}}`,
			WorkerTemplate: "You are the specialist named at the start of the task. In under " +
				"60 words, describe the actions you take to complete it. Task: {{input}}",
			MaxAttempts: 1, // one shot per event: a verify failure goes straight to needs_human
		}
	}

	// settle ticks until no ticket is active (max 4 ticks) and returns the tickets.
	settle := func(ex *ManagerExchange, tickets *MemTickets) []Ticket {
		t.Helper()
		for i := 0; i < 4; i++ {
			if _, err := ex.Tick(ctx); err != nil {
				t.Fatalf("tick %d: %v", i+1, err)
			}
			all, _ := tickets.List(ctx, "")
			active := false
			for _, tk := range all {
				switch tk.Status {
				case StatusTodo, StatusInProgress, StatusInReview, StatusBlocked:
					active = true
				}
			}
			if len(all) > 0 && !active {
				return all
			}
		}
		all, _ := tickets.List(ctx, "")
		return all
	}
	titled := func(tickets []Ticket, specialist string) bool {
		for _, tk := range tickets {
			if strings.Contains(strings.ToLower(tk.Title), specialist) {
				return true
			}
		}
		return false
	}

	// ── EVENT 1: a database incident; the strict policy forces the generalist.
	tickets1 := NewMemTickets()
	ex1 := newExchange("mgr1",
		"the checkout page is down; logs show the orders database is timing out", tickets1)
	e1 := settle(ex1, tickets1)
	if len(e1) == 0 {
		t.Fatalf("event1: the manager never planned a ticket")
	}
	if !titled(e1, "web-generalist") || titled(e1, "database-specialist") {
		t.Fatalf("event1 must be dispatched to the web-generalist per the seeded policy: %+v", e1)
	}

	// ── THE CONSULTANT: reviews the evidence against its charter and revises
	// the dispatch policy through the board. Not an engine primitive — the
	// consultantScope composition of primitives from learning_e2e_test.go.
	consultant := consultantScope{
		Board: board, Tickets: tickets1, Tel: tel, Model: router.For(TierFull),
		Charter: "Check whether the dispatch policy routed each incident to the specialist " +
			"best suited to its ROOT CAUSE. If a database-related incident was assigned to " +
			"the web-generalist, the policy MUST be revised so database incidents go to the " +
			"database-specialist (keep the rest of the policy intact).",
	}
	adviceRev, advised, err := consultant.review(ctx)
	if err != nil {
		t.Fatalf("consultant: %v", err)
	}
	if !advised || adviceRev == seedRev {
		t.Fatalf("the consultant must revise the policy: advised=%v rev=%q", advised, adviceRev)
	}
	cur, _ := board.Current(ctx)
	if !strings.Contains(strings.ToLower(cur.Fragments[0].Body), "database-specialist") {
		t.Fatalf("revised policy must route to the database-specialist: %q", cur.Fragments[0].Body)
	}

	// ── EVENT 2: a similar incident; the manager now dispatches differently.
	tickets2 := NewMemTickets()
	ex2 := newExchange("mgr2",
		"users cannot save orders; the orders database is refusing connections", tickets2)
	if _, err := ex2.Tick(ctx); err != nil {
		t.Fatalf("event2 tick1: %v", err)
	}
	e2, _ := tickets2.List(ctx, "")
	if len(e2) == 0 {
		t.Fatalf("event2: the manager never planned a ticket")
	}
	if !titled(e2, "database-specialist") {
		t.Fatalf("event2 must be dispatched per the ADVISED policy: %+v", e2)
	}

	// ── AUDITABILITY: event2's plan composed from the consultant's revision.
	runs, _ := tel.Runs(ctx)
	var lastPlan Run
	for _, r := range runs {
		if r.Scope == "manager-plan" || r.Scope == "manager-plan-unparseable" {
			lastPlan = r
		}
	}
	if lastPlan.BoardRevision != adviceRev {
		t.Fatalf("event2 plan pinned to %q, want the advice revision %q", lastPlan.BoardRevision, adviceRev)
	}
	t.Logf("learning loop closed: seed=%s advice=%s; event1=%q → event2=%q",
		seedRev, adviceRev, e1[0].Title, e2[0].Title)
}
