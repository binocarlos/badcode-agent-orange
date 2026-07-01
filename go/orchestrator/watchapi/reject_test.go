package watchapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
