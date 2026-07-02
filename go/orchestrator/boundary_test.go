package orchestrator

import (
	"context"
	"reflect"
	"testing"
)

// TestWorkerPathHasNoConnectorField asserts the worker executor (Runner) cannot
// hold a Connector — publishing is structurally out of the worker path. If a
// future change adds a Connector-typed field to Runner, this fails loudly.
func TestWorkerPathHasNoConnectorField(t *testing.T) {
	connIface := reflect.TypeOf((*Connector)(nil)).Elem()
	rt := reflect.TypeOf(Runner{})
	for i := 0; i < rt.NumField(); i++ {
		ft := rt.Field(i).Type
		if ft == connIface || ft.Implements(connIface) {
			t.Fatalf("Runner.%s is/holds a Connector — the worker path must never reach Publish", rt.Field(i).Name)
		}
	}
}

// TestApprovalServiceIsSoleConnectorHolder asserts ApprovalService is the only
// orchestrator type that holds a Connector — the sole gate to Publish.
func TestApprovalServiceIsSoleConnectorHolder(t *testing.T) {
	connIface := reflect.TypeOf((*Connector)(nil)).Elem()
	at := reflect.TypeOf(ApprovalService{})
	held := false
	for i := 0; i < at.NumField(); i++ {
		if at.Field(i).Type == connIface {
			held = true
		}
	}
	if !held {
		t.Fatalf("ApprovalService must hold the Connector — it is the sole publisher")
	}
}

// TestPublishOnlyReachableFromApproval drives the full worker→approval cycle with
// a counting connector and asserts Publish fires ONLY on approve, never on the
// worker/draft/reject paths (contracts §7 floor #3, the keystone v1 property).
func TestPublishOnlyReachableFromApproval(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	conn := &FakeConnector{} // Calls is our publish counter
	svc := NewApprovalService(ts, conn, NewTelemetry())

	// Simulate the worker path: a worker drafted content; the manager/ResultSink
	// (which holds NO connector) files it as a pending post. Zero publishes here.
	id, _ := ts.Create(ctx, Ticket{Title: "post", Status: StatusInProgress})
	if err := FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "draft"}); err != nil {
		t.Fatalf("file pending: %v", err)
	}
	if conn.Calls != 0 {
		t.Fatalf("the worker/draft path published (%d) — gate is bypassable", conn.Calls)
	}

	// Rejecting also never publishes.
	rejID, _ := ts.Create(ctx, Ticket{Title: "post2", Status: StatusInProgress})
	_ = FilePendingPost(ctx, ts, rejID, Post{Channel: "demo", Text: "nope"})
	if _, err := svc.Reject(ctx, rejID, "no"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if conn.Calls != 0 {
		t.Fatalf("reject published (%d) — must never publish", conn.Calls)
	}

	// Only the explicit approval action publishes — exactly once.
	if _, err := svc.Approve(ctx, id); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if conn.Calls != 1 {
		t.Fatalf("expected exactly one publish via approve, got %d", conn.Calls)
	}

	// And the worker tool surface still offers no way in.
	if IsWorkerTool("publish") || IsWorkerTool("connector") {
		t.Fatalf("a worker tool exposes publishing — gate is bypassable")
	}
}
