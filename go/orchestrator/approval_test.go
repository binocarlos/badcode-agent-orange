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

// §10c §D: Reject routes through Transition — it increments Attempts, appends the
// note to AttemptNotes, and at the cap goes needs_human instead of todo (the
// Reject attempts-bypass is fixed).
func TestRejectIncrementsAttemptsAndCapsAtNeedsHuman(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	svc := NewApprovalService(ts, &FakeConnector{}, NewTelemetry())

	id := seedDraftTicket(t, ts)
	_ = FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "draft v1"})
	if _, err := svc.Reject(ctx, id, "too flat"); err != nil {
		t.Fatalf("reject 1: %v", err)
	}
	after1, _ := ts.Get(ctx, id)
	if after1.Status != StatusTodo || after1.Attempts != 1 ||
		len(after1.AttemptNotes) != 1 || after1.AttemptNotes[0] != "too flat" {
		t.Fatalf("reject must count the attempt and keep the note: %+v", after1)
	}

	// Second reject reaches DefaultMaxAttempts(2) → needs_human (fail-loud, no loop).
	_ = FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "draft v2"})
	if _, err := svc.Reject(ctx, id, "still flat"); err != nil {
		t.Fatalf("reject 2: %v", err)
	}
	after2, _ := ts.Get(ctx, id)
	if after2.Status != StatusNeedsHuman || after2.Attempts != 2 || len(after2.AttemptNotes) != 2 {
		t.Fatalf("reject at cap must go needs_human: %+v", after2)
	}
	if len(after2.PendingPost) != 0 {
		t.Fatalf("reject must still clear the pending post at the cap: %+v", after2)
	}
}

// §10c §E: Answer resumes an escalated (needs_human, no PendingPost) ticket —
// the text lands in AttemptNotes, the stale Result clears, the ticket re-enters
// the queue WITHOUT burning an attempt.
func TestAnswerResumesEscalatedTicket(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	svc := NewApprovalService(ts, &FakeConnector{}, NewTelemetry())

	res, _ := json.Marshal(Result{Output: "what tone should I use?", Status: ResultEscalated})
	id, _ := ts.Create(ctx, Ticket{Title: "draft", Status: StatusNeedsHuman, Result: res, Attempts: 1})

	if err := svc.Answer(ctx, id, "use a playful tone"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	got, _ := ts.Get(ctx, id)
	if got.Status != StatusTodo {
		t.Fatalf("answered ticket = %s, want todo", got.Status)
	}
	if got.Attempts != 1 {
		t.Fatalf("answer must NOT increment attempts: %d", got.Attempts)
	}
	if len(got.AttemptNotes) != 1 || got.AttemptNotes[0] != "use a playful tone" {
		t.Fatalf("answer text must land in AttemptNotes: %v", got.AttemptNotes)
	}
	if len(got.Result) != 0 {
		t.Fatalf("answer must clear the stale escalation Result: %s", got.Result)
	}
}

func TestAnswerRejectsInvalidStates(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	svc := NewApprovalService(ts, &FakeConnector{}, NewTelemetry())

	// A needs_human ticket WITH a pending post: answer must refuse and direct the
	// operator to approve/reject (answering a pending post is NOT reject-with-guidance).
	id := seedDraftTicket(t, ts)
	_ = FilePendingPost(ctx, ts, id, Post{Channel: "demo", Text: "draft"})
	if err := svc.Answer(ctx, id, "some guidance"); err == nil {
		t.Fatalf("answer on a pending post must error")
	}
	if tk, _ := ts.Get(ctx, id); tk.Status != StatusNeedsHuman || len(tk.PendingPost) == 0 {
		t.Fatalf("failed answer must leave the pending post intact: %+v", tk)
	}

	// Not needs_human → error.
	todoID, _ := ts.Create(ctx, Ticket{Status: StatusTodo})
	if err := svc.Answer(ctx, todoID, "hi"); err == nil {
		t.Fatalf("answer on a todo ticket must error")
	}
	// Unknown ticket → error.
	if err := svc.Answer(ctx, "nope", "hi"); err == nil {
		t.Fatalf("answer on unknown ticket must error")
	}
	// Empty text → error (an answer IS the text).
	escID, _ := ts.Create(ctx, Ticket{Status: StatusNeedsHuman})
	if err := svc.Answer(ctx, escID, "  "); err == nil {
		t.Fatalf("empty answer must error")
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
