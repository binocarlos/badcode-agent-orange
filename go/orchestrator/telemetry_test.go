package orchestrator

import (
	"context"
	"testing"
)

func TestTelemetryRecordsInOrder(t *testing.T) {
	ctx := context.Background()
	tel := NewTelemetry()
	a, err := tel.Record(ctx, Run{Scope: "manager", BoardRevision: "r1", Output: "dumb plan"})
	if err != nil {
		t.Fatalf("record a: %v", err)
	}
	b, err := tel.Record(ctx, Run{Scope: "manager", BoardRevision: "r2", Output: "clever plan"})
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
}
