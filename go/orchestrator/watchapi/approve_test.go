package watchapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doPath(h *Handlers, method, target string) *httptest.ResponseRecorder {
	// route through the Mux so {id} PathValue is populated
	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestApproveCallsApprover(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := doPath(h, "POST", "/api/tickets/t1/approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var out struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Ref != "at://did/post/1" {
		t.Fatalf("ref = %q", out.Ref)
	}
	if len(d.approver.calls) != 1 || d.approver.calls[0] != "t1" {
		t.Fatalf("approver not called with t1: %+v", d.approver.calls)
	}
}

func TestApproveSurfacesPublishError(t *testing.T) {
	h, d := newTestHandlers(t)
	d.approver.err = errors.New("channel refused")
	rec := doPath(h, "POST", "/api/tickets/t1/approve")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d want 502", rec.Code)
	}
}
