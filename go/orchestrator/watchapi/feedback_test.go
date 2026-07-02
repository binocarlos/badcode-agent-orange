package watchapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeedbackAppliesAndReturnsRevision(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := httptest.NewRecorder()
	body := `{"target_ref":"fragment:routing-guidance","note":"be more clever"}`
	h.Feedback(rec, httptest.NewRequest("POST", "/api/feedback", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var out struct {
		Revision string `json:"revision"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Revision != "r2" {
		t.Fatalf("revision = %q", out.Revision)
	}
	if len(d.feedback.got) != 1 || d.feedback.got[0].TargetRef != "fragment:routing-guidance" {
		t.Fatalf("feedback not forwarded: %+v", d.feedback.got)
	}
}

func TestFeedbackRejectsMissingFields(t *testing.T) {
	h, _ := newTestHandlers(t)
	for _, body := range []string{`{"note":"x"}`, `{"target_ref":"fragment:r"}`, `{}`} {
		rec := httptest.NewRecorder()
		h.Feedback(rec, httptest.NewRequest("POST", "/api/feedback", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d want 400", body, rec.Code)
		}
	}
}
