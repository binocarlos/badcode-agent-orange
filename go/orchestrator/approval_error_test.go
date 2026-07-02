package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// seedPendingPostTicket creates a needs_human ticket holding a PendingPost.
func seedPendingPostTicket(t *testing.T, ts TicketStore) string {
	t.Helper()
	ctx := context.Background()
	id, err := ts.Create(ctx, Ticket{Title: "draft a post", Status: StatusInProgress})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := FilePendingPost(ctx, ts, id, Post{Channel: "main", Text: "the draft"}); err != nil {
		t.Fatalf("file pending post: %v", err)
	}
	return id
}

// --- FilePendingPost error paths ---

func TestFilePendingPostUnknownTicket(t *testing.T) {
	err := FilePendingPost(context.Background(), NewMemTickets(), "ghost", Post{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "file pending post ghost") {
		t.Fatalf("unknown ticket: err = %v", err)
	}
}

func TestFilePendingPostUpdateFailure(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	id, _ := ts.Create(ctx, Ticket{Title: "w", Status: StatusInReview})
	flaky := &flakyTickets{TicketStore: ts, updateErr: errBoom}
	if err := FilePendingPost(ctx, flaky, id, Post{Text: "x"}); !errors.Is(err, errBoom) {
		t.Fatalf("update failure: err = %v, want errBoom", err)
	}
}

// --- Approve error paths ---

func TestApproveUnknownTicket(t *testing.T) {
	svc := NewApprovalService(NewMemTickets(), &FakeConnector{}, NewTelemetry())
	if _, err := svc.Approve(context.Background(), "ghost"); err == nil ||
		!strings.Contains(err.Error(), "approve ghost") {
		t.Fatalf("unknown ticket: err = %v", err)
	}
}

func TestApproveCorruptPendingPostNeverPublishes(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	id, _ := ts.Create(ctx, Ticket{Title: "w", Status: StatusNeedsHuman, PendingPost: json.RawMessage(`{corrupt`)})
	conn := &FakeConnector{}
	svc := NewApprovalService(ts, conn, NewTelemetry())
	_, err := svc.Approve(ctx, id)
	if err == nil || !strings.Contains(err.Error(), "decode pending post") {
		t.Fatalf("corrupt pending post: err = %v", err)
	}
	if conn.Calls != 0 {
		t.Fatalf("connector reached despite corrupt post: %d calls", conn.Calls)
	}
}

func TestApproveUpdateFailureSurfacesAfterPublish(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	id := seedPendingPostTicket(t, ts)
	conn := &FakeConnector{Ref: "fake://post/1"}
	svc := NewApprovalService(&flakyTickets{TicketStore: ts, updateErr: errBoom}, conn, NewTelemetry())
	_, err := svc.Approve(ctx, id)
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "persist done") {
		t.Fatalf("persist failure: err = %v", err)
	}
	// The publish DID happen (the connector is the source of truth); the retry
	// is made safe by Post.DedupeKey, not by rolling back the channel.
	if conn.Calls != 1 {
		t.Fatalf("connector calls = %d, want 1", conn.Calls)
	}
	// Ticket unchanged in the store → the operator can retry and DedupeKey
	// prevents a double-post.
	got, _ := ts.Get(ctx, id)
	if got.Status != StatusNeedsHuman || len(got.PendingPost) == 0 {
		t.Fatalf("ticket mutated despite persist failure: %+v", got)
	}
}

func TestApproveTelemetryFailureFailsLoud(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	id := seedPendingPostTicket(t, ts)
	svc := NewApprovalService(ts, &FakeConnector{}, failTelemetry{err: errBoom})
	_, err := svc.Approve(ctx, id)
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "telemetry") {
		t.Fatalf("telemetry failure: err = %v", err)
	}
}

// --- Reject error paths ---

func TestRejectErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown ticket", func(t *testing.T) {
		svc := NewApprovalService(NewMemTickets(), &FakeConnector{}, NewTelemetry())
		if _, err := svc.Reject(ctx, "ghost", "no"); err == nil ||
			!strings.Contains(err.Error(), "reject ghost") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("no pending post", func(t *testing.T) {
		ts := NewMemTickets()
		id, _ := ts.Create(ctx, Ticket{Title: "w", Status: StatusTodo})
		svc := NewApprovalService(ts, &FakeConnector{}, NewTelemetry())
		if _, err := svc.Reject(ctx, id, "no"); err == nil ||
			!strings.Contains(err.Error(), "no pending post") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("needs_human but empty pending post", func(t *testing.T) {
		ts := NewMemTickets()
		id, _ := ts.Create(ctx, Ticket{Title: "w", Status: StatusNeedsHuman}) // escalation, no draft
		svc := NewApprovalService(ts, &FakeConnector{}, NewTelemetry())
		if _, err := svc.Reject(ctx, id, "no"); err == nil ||
			!strings.Contains(err.Error(), "no pending post") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("update failure", func(t *testing.T) {
		ts := NewMemTickets()
		id := seedPendingPostTicket(t, ts)
		svc := NewApprovalService(&flakyTickets{TicketStore: ts, updateErr: errBoom}, &FakeConnector{}, NewTelemetry())
		_, err := svc.Reject(ctx, id, "no")
		if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "persist") {
			t.Fatalf("err = %v", err)
		}
		// Store untouched → pending post survives for a retry.
		got, _ := ts.Get(ctx, id)
		if got.Status != StatusNeedsHuman || len(got.PendingPost) == 0 {
			t.Fatalf("ticket mutated despite persist failure: %+v", got)
		}
	})
}

// --- Answer error paths ---

func TestAnswerUpdateFailure(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()
	id, _ := ts.Create(ctx, Ticket{Title: "w", Status: StatusNeedsHuman}) // escalation, no post
	svc := NewApprovalService(&flakyTickets{TicketStore: ts, updateErr: errBoom}, &FakeConnector{}, NewTelemetry())
	err := svc.Answer(ctx, id, "the answer")
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "persist") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnswerUnknownTicket(t *testing.T) {
	svc := NewApprovalService(NewMemTickets(), &FakeConnector{}, NewTelemetry())
	if err := svc.Answer(context.Background(), "ghost", "hi"); err == nil ||
		!strings.Contains(err.Error(), "answer ghost") {
		t.Fatalf("err = %v", err)
	}
}
