package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// Slice-D tests reuse Slice C's MemTickets (contracts §5 in-memory TicketStore
// double) rather than a second fakeTicketStore — one canonical double per seam.

func seedDraftTicket(t *testing.T, ts *MemTickets) string {
	t.Helper()
	id, err := ts.Create(context.Background(), Ticket{Title: "draft a post", Status: StatusInProgress})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return id
}

func TestApproveIsTheSolePublisher(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	conn := &FakeConnector{}
	svc := NewApprovalService(ts, conn, NewTelemetry())

	id := seedDraftTicket(t, ts)

	// A worker draft becomes a PendingPost on a Needs-Human ticket. No publish yet.
	if err := FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "ship it"}); err != nil {
		t.Fatalf("file pending: %v", err)
	}
	pending, _ := ts.Get(ctx, id)
	if pending.Status != StatusNeedsHuman || len(pending.PendingPost) == 0 {
		t.Fatalf("draft did not become a pending post: %+v", pending)
	}
	if conn.Calls != 0 {
		t.Fatalf("filing a pending post must NOT publish (calls=%d)", conn.Calls)
	}

	// Approve publishes exactly once and moves the ticket to Done.
	ref, err := svc.Approve(ctx, id)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if conn.Calls != 1 || len(conn.Published) != 1 || conn.Published[0].Text != "ship it" {
		t.Fatalf("approve must publish exactly once: calls=%d published=%+v", conn.Calls, conn.Published)
	}
	if ref == "" {
		t.Fatalf("approve must return the channel ref")
	}
	// E-5: the ticket id is set as the publish idempotency key.
	if conn.Published[0].DedupeKey != id {
		t.Fatalf("approve must set Post.DedupeKey to the ticket id: got %q want %q", conn.Published[0].DedupeKey, id)
	}
	done, _ := ts.Get(ctx, id)
	if done.Status != StatusDone || len(done.PendingPost) != 0 {
		t.Fatalf("approved ticket must be Done with no pending post: %+v", done)
	}
	// E-4: the channel's returned ref is persisted on the ticket as attribution.
	if done.PublishedRef != ref {
		t.Fatalf("approved ticket must persist PublishedRef=%q, got %q", ref, done.PublishedRef)
	}

	// A second approve is a no-op publish (guards double-post).
	if _, err := svc.Approve(ctx, id); err == nil {
		t.Fatalf("re-approving a published ticket must error")
	}
	if conn.Calls != 1 {
		t.Fatalf("double-approve must NOT publish again (calls=%d)", conn.Calls)
	}
}

func TestRejectEmitsFeedbackAndDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	conn := &FakeConnector{}
	svc := NewApprovalService(ts, conn, NewTelemetry())

	id := seedDraftTicket(t, ts)
	if err := FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "meh draft"}); err != nil {
		t.Fatalf("file pending: %v", err)
	}

	fb, err := svc.Reject(ctx, id, "too salesy — be wittier")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if conn.Calls != 0 {
		t.Fatalf("reject must NEVER publish (calls=%d)", conn.Calls)
	}
	if fb.TargetRef != "ticket:"+id || fb.Note != "too salesy — be wittier" {
		t.Fatalf("reject must emit HumanFeedback targeting the ticket: %+v", fb)
	}
	after, _ := ts.Get(ctx, id)
	if after.Status != StatusTodo || len(after.PendingPost) != 0 {
		t.Fatalf("rejected ticket must clear the pending post and return to Todo: %+v", after)
	}

	// An empty note is allowed (contracts §8 note is optional).
	id2 := seedDraftTicket(t, ts)
	_ = FilePendingPost(ctx, ts, id2, Post{Channel: "demo", Text: "x"})
	if fb2, err := svc.Reject(ctx, id2, ""); err != nil || fb2.Note != "" {
		t.Fatalf("empty-note reject: fb=%+v err=%v", fb2, err)
	}
}

func TestApproveKeepsTicketPendingOnConnectorFailure(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	boom := errors.New("channel 503")
	svc := NewApprovalService(ts, &FakeConnector{Err: boom}, NewTelemetry())

	id := seedDraftTicket(t, ts)
	_ = FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "retry me"})

	if _, err := svc.Approve(ctx, id); !errors.Is(err, boom) {
		t.Fatalf("expected connector error, got %v", err)
	}
	stuck, _ := ts.Get(ctx, id)
	if stuck.Status != StatusNeedsHuman || len(stuck.PendingPost) == 0 {
		t.Fatalf("failed publish must leave the ticket Needs-Human for retry: %+v", stuck)
	}

	// Sanity: the pending post round-trips (json.RawMessage decode).
	var p Post
	if err := json.Unmarshal(stuck.PendingPost, &p); err != nil || p.Text != "retry me" {
		t.Fatalf("pending post did not round-trip: %+v err=%v", p, err)
	}
}
