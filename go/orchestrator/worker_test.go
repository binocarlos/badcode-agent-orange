package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// failRouter/failModel: a deterministic always-erroring Model double for the
// spawn-error / spend-halt paths (every tier resolves to the same failure).
type failRouter struct{ err error }

func (f failRouter) For(ModelTier) Model { return failModel{err: f.err} }

type failModel struct{ err error }

func (m failModel) Run(context.Context, string) (string, Usage, error) {
	return "", Usage{}, m.err
}

var errBoom = errors.New("model boom")

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
	// §10c §A: TokensUsed is the model's reported usage total (ScriptedModel's
	// deterministic pseudo-usage: len(prompt)/4 + len(reply)/4), not a char count.
	prompt := "You are a witty writer.\nTask: launch post"
	wantTokens := int64(len(prompt)/4) + int64(len("a witty draft post")/4)
	if r.TokensUsed != wantTokens {
		t.Fatalf("TokensUsed = %d, want %d (usage.Total())", r.TokensUsed, wantTokens)
	}
	runs, err := tel.Runs(ctx)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 recorded run, got %d", len(runs))
	}
	// §10c §C: the worker run is attributed to its ticket + session.
	if runs[0].TicketID != id || runs[0].SessionID != sid {
		t.Fatalf("run attribution = ticket %q session %q, want %q / %q",
			runs[0].TicketID, runs[0].SessionID, id, sid)
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

// §10c §F: a completed worker frees its parent's in-flight slot — with
// MaxSpawns=1 two SEQUENTIAL spawns both succeed (capacity is in-flight, not
// lifetime), and a model failure also releases (any terminal outcome frees).
func TestInProcRuntimeReleasesSlotOnCompletion(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("role-writer", "writer"))
	tickets := NewMemTickets()
	budget := Budget{MaxDepth: 3, MaxSpawns: 1, TreeTokens: 10000}
	ledger := NewSpawnLedger()
	ledger.RegisterRoot("mgr", budget)

	rt := &InProcRuntime{Board: board,
		Router: ScriptedRouter{TierMid: &ScriptedModel{Default: "a draft"}},
		Sink:   &TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: NewTelemetry()}

	for i := 0; i < 2; i++ { // two sequential workers under MaxSpawns=1
		id, _ := tickets.Create(ctx, Ticket{Status: StatusInProgress})
		if _, err := rt.Spawn(ctx, Scope{Template: "{{fragment:role-writer}}", Input: "x",
			Tier: TierMid, Budget: budget, Parent: "mgr", TicketID: id}); err != nil {
			t.Fatalf("sequential spawn %d must succeed after release: %v", i+1, err)
		}
	}

	// A failing model call is also terminal → the slot still frees.
	failing := &InProcRuntime{Board: board,
		Router: failRouter{err: errBoom},
		Sink:   &TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: NewTelemetry()}
	id, _ := tickets.Create(ctx, Ticket{Status: StatusInProgress})
	if _, err := failing.Spawn(ctx, Scope{Template: "{{fragment:role-writer}}", Input: "x",
		Tier: TierMid, Budget: budget, Parent: "mgr", TicketID: id}); err == nil {
		t.Fatalf("expected model failure")
	}
	id2, _ := tickets.Create(ctx, Ticket{Status: StatusInProgress})
	if _, err := rt.Spawn(ctx, Scope{Template: "{{fragment:role-writer}}", Input: "x",
		Tier: TierMid, Budget: budget, Parent: "mgr", TicketID: id2}); err != nil {
		t.Fatalf("spawn after failed worker must succeed (slot released): %v", err)
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
