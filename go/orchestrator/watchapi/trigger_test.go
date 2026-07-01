package watchapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTriggerFiresExchange(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := httptest.NewRecorder()
	h.Trigger(rec, httptest.NewRequest("POST", "/api/trigger", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d want 202", rec.Code)
	}
	if d.trigger.n != 1 {
		t.Fatalf("trigger not fired: %d", d.trigger.n)
	}
}

func TestTriggerSurfacesError(t *testing.T) {
	h, d := newTestHandlers(t)
	d.trigger.err = errors.New("dispatch halted: spend ceiling")
	rec := httptest.NewRecorder()
	h.Trigger(rec, httptest.NewRequest("POST", "/api/trigger", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d want 500", rec.Code)
	}
}
