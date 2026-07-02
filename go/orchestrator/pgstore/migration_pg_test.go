package pgstore

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// TestPostgresMigrationsRoundTrip exercises the real SQL migrations (020-025)
// against a live Postgres. Set AGENTKIT_TEST_POSTGRES_URL to run it, e.g.
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://user:pass@localhost:5432/agentorange?sslmode=disable
func TestPostgresMigrationsRoundTrip(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres migration test")
	}
	ctx := context.Background()
	store, err := agentdb.Open(url) // runs numbered migrations incl. 022-024
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db := store.DB()

	board := NewPgBoard(db)
	rev, err := board.Append(ctx, agentdb.Changeset{Author: "it", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be clever.")}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	cur, err := board.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if cur.Revision != rev || len(cur.Fragments) == 0 {
		t.Fatalf("fold wrong on postgres: %+v", cur)
	}

	tickets := NewPgTicketStore(db)
	id, err := tickets.Create(ctx, orchestrator.Ticket{
		Title: "it", Status: orchestrator.StatusTodo, Scope: json.RawMessage(`{"name":"w"}`),
		Disposition: orchestrator.DispositionPublish, AttemptNotes: []string{"n1"},
	})
	if err != nil {
		t.Fatalf("ticket create: %v", err)
	}
	tk, err := tickets.Get(ctx, id)
	if err != nil {
		t.Fatalf("ticket get: %v", err)
	}
	// §10c I-6 (migration 025): the remediation columns round-trip on real Postgres.
	if tk.Disposition != orchestrator.DispositionPublish || len(tk.AttemptNotes) != 1 {
		t.Fatalf("025 ticket columns wrong: %+v", tk)
	}
	// §10c I-1: PendingPost was never set — it must come back empty on Postgres too.
	if len(tk.PendingPost) != 0 {
		t.Fatalf("pending post not empty on round-trip: %q", tk.PendingPost)
	}

	tel := NewPgTelemetry(db)
	got, err := tel.Record(ctx, orchestrator.Run{
		Scope: "manager", BoardRevision: rev, Output: "o", TicketID: id, SessionID: "sess-it",
	})
	if err != nil {
		t.Fatalf("telemetry record: %v", err)
	}
	if got.ID == "" {
		t.Fatalf("telemetry record produced no id")
	}
	runs, err := tel.Runs(ctx)
	if err != nil {
		t.Fatalf("telemetry runs: %v", err)
	}
	if len(runs) == 0 {
		t.Fatalf("telemetry runs empty after record")
	}
	last := runs[len(runs)-1]
	if last.TicketID != id || last.SessionID != "sess-it" {
		t.Fatalf("025 run attribution wrong: %+v", last)
	}
}
