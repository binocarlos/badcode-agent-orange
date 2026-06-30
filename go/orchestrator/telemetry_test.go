package orchestrator

import "testing"

func TestTelemetryRecordsInOrder(t *testing.T) {
	tel := NewTelemetry()
	a := tel.Record(Run{Scope: "manager", BoardRevision: "r1", Output: "dumb plan"})
	b := tel.Record(Run{Scope: "manager", BoardRevision: "r2", Output: "clever plan"})
	if a.ID != "run1" || a.Seq != 1 || b.ID != "run2" || b.Seq != 2 {
		t.Fatalf("ids/seq wrong: %+v %+v", a, b)
	}
	runs := tel.Runs()
	if len(runs) != 2 || runs[0].BoardRevision != "r1" || runs[1].BoardRevision != "r2" {
		t.Fatalf("runs wrong: %+v", runs)
	}
}
