// Command approvalgate demonstrates the v1 publish-approval floor: a draft becomes
// a Needs-Human pending post; approving it publishes exactly once via the fake
// connector; rejecting it emits a note and never publishes. Offline, deterministic.
package main

import (
	"context"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func main() {
	ctx := context.Background()
	ts := orchestrator.NewMemTickets() // Slice C's in-memory TicketStore double
	conn := &orchestrator.FakeConnector{}
	svc := orchestrator.NewApprovalService(ts, conn, orchestrator.NewTelemetry())

	// 1) Approve path.
	good, _ := ts.Create(ctx, orchestrator.Ticket{Title: "launch tweet", Status: orchestrator.StatusInProgress})
	must(orchestrator.FilePendingPost(ctx, ts, good, orchestrator.Post{Channel: "demo", Text: "We shipped v1 🚀"}))
	fmt.Printf("filed pending post on %s (publishes so far: %d)\n", good, conn.Calls)
	ref, err := svc.Approve(ctx, good)
	if err != nil {
		panic(err)
	}
	fmt.Printf("APPROVED %s → published ref=%s (publishes so far: %d)\n", good, ref, conn.Calls)

	// 2) Reject path.
	bad, _ := ts.Create(ctx, orchestrator.Ticket{Title: "salesy post", Status: orchestrator.StatusInProgress})
	must(orchestrator.FilePendingPost(ctx, ts, bad, orchestrator.Post{Channel: "demo", Text: "BUY NOW!!!"}))
	fb, err := svc.Reject(ctx, bad, "too salesy — be witty, not shouty")
	if err != nil {
		panic(err)
	}
	fmt.Printf("REJECTED %s → feedback{%s: %q} (publishes so far: %d)\n", bad, fb.TargetRef, fb.Note, conn.Calls)

	fmt.Printf("\ngate held: %d post(s) published, only via approve.\n", conn.Calls)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
