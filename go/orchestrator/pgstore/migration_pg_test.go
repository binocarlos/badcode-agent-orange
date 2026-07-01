package pgstore

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// TestPostgresMigrationsRoundTrip exercises the real SQL migrations (020-024)
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
	})
	if err != nil {
		t.Fatalf("ticket create: %v", err)
	}
	if _, err := tickets.Get(ctx, id); err != nil {
		t.Fatalf("ticket get: %v", err)
	}

	tel := NewPgTelemetry(db)
	got, err := tel.Record(ctx, orchestrator.Run{Scope: "manager", BoardRevision: rev, Output: "o"})
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
}
