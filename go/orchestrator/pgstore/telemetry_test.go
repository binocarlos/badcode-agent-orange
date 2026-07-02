package pgstore

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// TestPgTelemetryRecordsInOrder mirrors orchestrator.TestTelemetryRecordsInOrder.
func TestPgTelemetryRecordsInOrder(t *testing.T) {
	ctx := context.Background()
	tel := NewPgTelemetry(newTestDB(t))
	a, err := tel.Record(ctx, orchestrator.Run{Scope: "manager", BoardRevision: "r1", Output: "dumb plan"})
	if err != nil {
		t.Fatalf("record a: %v", err)
	}
	b, err := tel.Record(ctx, orchestrator.Run{Scope: "manager", BoardRevision: "r2", Output: "clever plan"})
	if err != nil {
		t.Fatalf("record b: %v", err)
	}
	if a.ID != "run1" || a.Seq != 1 || b.ID != "run2" || b.Seq != 2 {
		t.Fatalf("ids/seq wrong: %+v %+v", a, b)
	}
	runs, err := tel.Runs(ctx)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 2 || runs[0].BoardRevision != "r1" || runs[1].BoardRevision != "r2" {
		t.Fatalf("runs wrong: %+v", runs)
	}
	if runs[0].Output != "dumb plan" || runs[1].Output != "clever plan" {
		t.Fatalf("outputs not persisted: %+v", runs)
	}
}

// §10c I-6 / §C: TicketID/SessionID persist and come back — runs are joinable
// to the ticket and worker session they served.
func TestPgTelemetryAttributionRoundTrip(t *testing.T) {
	ctx := context.Background()
	tel := NewPgTelemetry(newTestDB(t))
	rec, err := tel.Record(ctx, orchestrator.Run{
		Scope: "worker", BoardRevision: "r1", Prompt: "p", Output: "draft",
		TicketID: "t42", SessionID: "sess-9",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if rec.TicketID != "t42" || rec.SessionID != "sess-9" {
		t.Fatalf("returned run lost attribution: %+v", rec)
	}
	runs, err := tel.Runs(ctx)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 1 || runs[0].TicketID != "t42" || runs[0].SessionID != "sess-9" {
		t.Fatalf("attribution not persisted: %+v", runs)
	}
}

var _ orchestrator.Telemetry = (*PgTelemetry)(nil)
