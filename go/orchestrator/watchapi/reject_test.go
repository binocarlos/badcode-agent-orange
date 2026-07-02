package watchapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestRejectWithNoteAppliesFeedback(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/t1/reject", strings.NewReader(`{"note":"too boring, be witty"}`))
	req.SetPathValue("id", "t1")
	h.Reject(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	if len(d.rejecter.calls) != 1 || d.rejecter.calls[0].ID != "t1" {
		t.Fatalf("rejecter not called with t1: %+v", d.rejecter.calls)
	}
	if len(d.feedback.got) != 1 || d.feedback.got[0].TargetRef != "ticket:t1" ||
		!strings.Contains(d.feedback.got[0].Note, "witty") {
		t.Fatalf("feedback not applied: %+v", d.feedback.got)
	}
}

func TestRejectWithoutNoteSkipsFeedback(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/t1/reject", strings.NewReader(`{}`))
	req.SetPathValue("id", "t1")
	h.Reject(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(d.feedback.got) != 0 {
		t.Fatalf("feedback should be skipped with no note")
	}
}

// §10c I-7: the note is applied BEFORE the reject, so a feedback failure leaves
// the pending post INTACT (fully retryable — the note is never lost against a
// consumed pending post). With the fakes: feedback errors → 500 AND the
// Rejecter is never called.
func TestRejectFeedbackFailureIs500AndDoesNotReject(t *testing.T) {
	h, d := newTestHandlers(t)
	d.feedback.err = errors.New("board down")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/t1/reject", strings.NewReader(`{"note":"be witty"}`))
	req.SetPathValue("id", "t1")
	h.Reject(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if len(d.rejecter.calls) != 0 {
		t.Fatalf("Reject ran despite feedback failure — note ordering wrong: %+v", d.rejecter.calls)
	}
}

// nopConnector satisfies orchestrator.Connector for wiring a REAL
// ApprovalService into the handler (it is never reached by Reject).
type nopConnector struct{}

func (nopConnector) Publish(context.Context, orchestrator.Post) (string, error) {
	return "at://ref/1", nil
}

// End-to-end I-7 pin over the REAL ApprovalService: a feedback failure leaves
// the ticket needs_human with its pending post intact (retryable), and a
// successful reject-with-note on a FRESH board seeds the routing-guidance
// fragment (the first lesson) and clears the post.
func TestRejectOrderingWithRealApprovalService(t *testing.T) {
	ctx := context.Background()
	newDeps := func(fb orchestrator.FeedbackApplier) (Config, orchestrator.TicketStore, *orchestrator.MemBoard, string) {
		board := orchestrator.NewMemBoard()
		tickets := orchestrator.NewMemTickets()
		tel := orchestrator.NewTelemetry()
		id, _ := tickets.Create(ctx, orchestrator.Ticket{Title: "draft", Status: orchestrator.StatusTodo})
		tk, _ := tickets.Get(ctx, id)
		tk.Status = orchestrator.StatusNeedsHuman
		tk.PendingPost = json.RawMessage(`{"channel":"bsky","text":"hi"}`)
		if err := tickets.Update(ctx, tk); err != nil {
			t.Fatalf("seed pending post: %v", err)
		}
		svc := orchestrator.NewApprovalService(tickets, nopConnector{}, tel)
		cfg := Config{Board: board, Revisions: board, Tickets: tickets, Telemetry: tel,
			Approver: svc, Rejecter: svc, Feedback: fb, Trigger: &fakeTrigger{}}
		return cfg, tickets, board, id
	}

	// Case 1: feedback fails → 500, pending post INTACT.
	cfg, tickets, _, id := newDeps(&fakeFeedback{err: errors.New("board down")})
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/"+id+"/reject", strings.NewReader(`{"note":"be witty"}`))
	req.SetPathValue("id", id)
	h.Reject(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	tk, _ := tickets.Get(ctx, id)
	if tk.Status != orchestrator.StatusNeedsHuman || len(tk.PendingPost) == 0 {
		t.Fatalf("pending post not intact after feedback failure: %+v", tk)
	}

	// Case 2: fresh board (zero revisions), real feedback applier → 200, the
	// routing-guidance fragment is SEEDED with the note, and the post is cleared.
	board2 := orchestrator.NewMemBoard()
	real := orchestrator.HumanFeedbackApplier{Board: board2, Reviser: &orchestrator.ScriptedModel{Default: "unused"}}
	cfg2, tickets2, _, id2 := newDeps(real)
	cfg2.Board = board2
	cfg2.Revisions = board2
	h2, err := New(cfg2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/tickets/"+id2+"/reject", strings.NewReader(`{"note":"always mention the demo"}`))
	req2.SetPathValue("id", id2)
	h2.Reject(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec2.Code, rec2.Body)
	}
	cur, err := board2.Current(ctx)
	if err != nil {
		t.Fatalf("fresh board reject did not seed a revision: %v", err)
	}
	if len(cur.Fragments) != 1 || cur.Fragments[0].ID != "routing-guidance" ||
		cur.Fragments[0].Body != "always mention the demo" {
		t.Fatalf("first lesson not seeded: %+v", cur.Fragments)
	}
	tk2, _ := tickets2.Get(ctx, id2)
	if tk2.Status != orchestrator.StatusTodo || len(tk2.PendingPost) != 0 {
		t.Fatalf("reject did not clear the post after feedback: %+v", tk2)
	}
	var body map[string]string
	_ = json.Unmarshal(rec2.Body.Bytes(), &body)
	if body["status"] != "rejected" || body["revision"] != "r1" {
		t.Fatalf("response wrong: %v", body)
	}
}

func TestRejectUnknownTicket404(t *testing.T) {
	h, d := newTestHandlers(t)
	d.rejecter.err = errors.New("reject nope: unknown id")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/nope/reject", strings.NewReader(`{}`))
	req.SetPathValue("id", "nope")
	h.Reject(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d want 404", rec.Code)
	}
}
