package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentApprovePublishesExactlyOnce fires N simultaneous Approves for
// ONE needs_human ticket holding ONE PendingPost. The publish-approval gate's
// exactly-once guarantee must hold under concurrency: the Connector is invoked
// once, one caller gets the ref, and every other caller errors "no pending
// post". Run with -race. Before the ApprovalService serialization fix all N
// callers read the same pending post and all N publish (Calls > 1).
func TestConcurrentApprovePublishesExactlyOnce(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	id, err := ts.Create(ctx, Ticket{Title: "draft a post", Status: StatusInProgress})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	post, _ := json.Marshal(Post{Channel: "main", Text: "the draft"})
	tk, _ := ts.Get(ctx, id)
	tk.Status = StatusNeedsHuman
	tk.PendingPost = post
	if err := ts.Update(ctx, tk); err != nil {
		t.Fatalf("update: %v", err)
	}

	conn := &FakeConnector{Ref: "fake://post/1"}
	svc := NewApprovalService(ts, conn, NewTelemetry())

	const n = 8
	start := make(chan struct{})
	refs := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			refs[i], errs[i] = svc.Approve(ctx, id)
		}(i)
	}
	close(start)
	wg.Wait()

	if conn.Calls != 1 {
		t.Fatalf("double-publish: Connector.Publish called %d times, want exactly 1", conn.Calls)
	}
	successes := 0
	for i := 0; i < n; i++ {
		switch {
		case errs[i] == nil:
			successes++
			if refs[i] != "fake://post/1" {
				t.Fatalf("winner got ref %q", refs[i])
			}
		case !strings.Contains(errs[i].Error(), "no pending post"):
			t.Fatalf("loser %d got unexpected error: %v", i, errs[i])
		}
	}
	if successes != 1 {
		t.Fatalf("want exactly 1 successful approve, got %d", successes)
	}

	got, _ := ts.Get(ctx, id)
	if got.Status != StatusDone || len(got.PendingPost) != 0 || got.PublishedRef != "fake://post/1" {
		t.Fatalf("ticket after concurrent approve: %+v", got)
	}
}
