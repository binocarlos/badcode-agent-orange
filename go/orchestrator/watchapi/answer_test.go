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

func TestAnswerCallsTheAnswerer(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/t1/answer", strings.NewReader(`{"text":"use a playful tone"}`))
	req.SetPathValue("id", "t1")
	h.Answer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	if len(d.answerer.calls) != 1 || d.answerer.calls[0].ID != "t1" || d.answerer.calls[0].Text != "use a playful tone" {
		t.Fatalf("answerer not called: %+v", d.answerer.calls)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "answered" {
		t.Fatalf("response wrong: %v", body)
	}
}

func TestAnswerEmptyTextIs400(t *testing.T) {
	h, d := newTestHandlers(t)
	for _, payload := range []string{`{}`, `{"text":"  "}`, ``} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/tickets/t1/answer", strings.NewReader(payload))
		req.SetPathValue("id", "t1")
		h.Answer(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("payload %q: status %d, want 400", payload, rec.Code)
		}
	}
	if len(d.answerer.calls) != 0 {
		t.Fatalf("empty text must not reach the Answerer: %+v", d.answerer.calls)
	}
}

func TestAnswerUnknownOrInvalidStateIs404(t *testing.T) {
	h, d := newTestHandlers(t)
	d.answerer.err = errors.New("answer nope: unknown id")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/nope/answer", strings.NewReader(`{"text":"hi"}`))
	req.SetPathValue("id", "nope")
	h.Answer(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// The route is mounted and, wired to the REAL ApprovalService, answering an
// escalated ticket re-queues it with the answer in AttemptNotes — while a ticket
// holding a PendingPost refuses (approve/reject is the only way past a draft).
func TestAnswerRouteWithRealApprovalService(t *testing.T) {
	ctx := context.Background()
	board := orchestrator.NewMemBoard()
	tickets := orchestrator.NewMemTickets()
	tel := orchestrator.NewTelemetry()
	svc := orchestrator.NewApprovalService(tickets, nopConnector{}, tel)

	escalated, _ := tickets.Create(ctx, orchestrator.Ticket{
		Title: "stuck", Status: orchestrator.StatusNeedsHuman,
		Result: json.RawMessage(`{"Output":"what tone?","Status":"escalated"}`),
	})
	pending, _ := tickets.Create(ctx, orchestrator.Ticket{
		Title: "drafted", Status: orchestrator.StatusNeedsHuman,
		PendingPost: json.RawMessage(`{"channel":"bsky","text":"hi"}`),
	})

	cfg := Config{Board: board, Revisions: board, Tickets: tickets, Telemetry: tel,
		Approver: svc, Rejecter: svc, Answerer: svc, Feedback: &fakeFeedback{rev: "r1"}, Trigger: &fakeTrigger{}}
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := newAuthServer(t, h)
	defer srv.Close()

	postJSON(t, srv, "/api/tickets/"+escalated+"/answer", `{"text":"playful tone"}`, nil)
	tk, _ := tickets.Get(ctx, escalated)
	if tk.Status != orchestrator.StatusTodo || len(tk.AttemptNotes) != 1 || tk.AttemptNotes[0] != "playful tone" {
		t.Fatalf("answer did not re-queue with the note: %+v", tk)
	}

	// A pending-post ticket refuses Answer (404 invalid-state).
	resp, err := http.Post(srv.URL+"/api/tickets/"+pending+"/answer", "application/json",
		strings.NewReader(`{"text":"guidance"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("answer on a pending post: status %d, want 404", resp.StatusCode)
	}
	tk2, _ := tickets.Get(ctx, pending)
	if tk2.Status != orchestrator.StatusNeedsHuman || len(tk2.PendingPost) == 0 {
		t.Fatalf("failed answer must leave the pending post intact: %+v", tk2)
	}
}
