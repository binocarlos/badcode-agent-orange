package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
)

func TestInProcRuntimeDraftsAndSinksToInReview(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("role-writer", "You are a witty writer."))

	tickets := NewMemTickets()
	id, _ := tickets.Create(ctx, Ticket{Title: "draft", Objective: "write it", Status: StatusInProgress})

	ledger := NewSpawnLedger()
	ledger.RegisterRoot("mgr", Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 10000})

	tel := NewTelemetry()
	rt := &InProcRuntime{
		Board:     board,
		Router:    ScriptedRouter{TierMid: &ScriptedModel{Default: "a witty draft post"}},
		Sink:      &TicketResultSink{Tickets: tickets},
		Ledger:    ledger,
		Telemetry: tel,
	}
	scope := Scope{
		Name: "post-writer", Template: "{{fragment:role-writer}}\nTask: {{input}}", Input: "launch post",
		Tier: TierMid, Budget: Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 10000},
		Parent: "mgr", TicketID: id,
	}

	sid, err := rt.Spawn(ctx, scope)
	if err != nil || sid != "s1" {
		t.Fatalf("spawn: id=%q err=%v", sid, err)
	}
	// The result landed on the ticket as In-Review (fire-and-forget → sink).
	got, _ := tickets.Get(ctx, id)
	if got.Status != StatusInReview {
		t.Fatalf("ticket status = %q, want in_review", got.Status)
	}
	var r Result
	if err := json.Unmarshal(got.Result, &r); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if r.Output != "a witty draft post" || r.Status != ResultDone || r.TicketID != id {
		t.Fatalf("result wrong: %+v", r)
	}
	runs, err := tel.Runs(ctx)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 recorded run, got %d", len(runs))
	}
}

func TestInProcRuntimeEscalationSinksToNeedsHuman(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("role-writer", "writer"))
	tickets := NewMemTickets()
	id, _ := tickets.Create(ctx, Ticket{Status: StatusInProgress})
	ledger := NewSpawnLedger()
	ledger.RegisterRoot("mgr", Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 10000})

	rt := &InProcRuntime{
		Board:     board,
		Router:    ScriptedRouter{TierMid: &ScriptedModel{Default: "ESCALATE: what tone should I use?"}},
		Sink:      &TicketResultSink{Tickets: tickets},
		Ledger:    ledger,
		Telemetry: NewTelemetry(),
	}
	if _, err := rt.Spawn(ctx, Scope{Template: "{{fragment:role-writer}}", Input: "x",
		Tier: TierMid, Budget: Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 10000}, Parent: "mgr", TicketID: id}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	got, _ := tickets.Get(ctx, id)
	if got.Status != StatusNeedsHuman {
		t.Fatalf("escalation status = %q, want needs_human", got.Status)
	}
	var r Result
	_ = json.Unmarshal(got.Result, &r)
	if r.Status != ResultEscalated || r.Output != "what tone should I use?" {
		t.Fatalf("escalation result wrong: %+v", r)
	}
}

func TestInProcRuntimeRefusesOnFloor(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("role-writer", "writer"))
	tickets := NewMemTickets()
	id, _ := tickets.Create(ctx, Ticket{Status: StatusInProgress})
	ledger := NewSpawnLedger()
	ledger.RegisterRoot("mgr", Budget{MaxDepth: 0, MaxSpawns: 5, TreeTokens: 10000}) // MaxDepth 0 → any worker refused

	rt := &InProcRuntime{Board: board, Router: ScriptedRouter{}, Sink: &TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: NewTelemetry()}
	if _, err := rt.Spawn(ctx, Scope{Template: "{{fragment:role-writer}}", Tier: TierMid,
		Budget: Budget{MaxDepth: 0, MaxSpawns: 5, TreeTokens: 10000}, Parent: "mgr", TicketID: id}); err == nil {
		t.Fatalf("expected floor refusal")
	}
	// Refusal must NOT have touched the ticket (the exchange decides fail-loud handling).
	if got, _ := tickets.Get(ctx, id); got.Status != StatusInProgress {
		t.Fatalf("refused spawn mutated ticket: %s", got.Status)
	}
}
