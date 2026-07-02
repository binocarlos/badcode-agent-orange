package watchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestRunsRendersTelemetry(t *testing.T) {
	h, d := newTestHandlers(t)
	ctx := context.Background()
	_, _ = d.tel.Record(ctx, orchestrator.Run{Scope: "manager", BoardRevision: "r1", Output: "dumb plan"})
	_, _ = d.tel.Record(ctx, orchestrator.Run{Scope: "manager", BoardRevision: "r2", Output: "clever plan",
		TicketID: "t1", SessionID: "sess-1"})

	rec := httptest.NewRecorder()
	h.Runs(rec, httptest.NewRequest("GET", "/api/runs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var runs []RunDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &runs)
	if len(runs) != 2 || runs[0].BoardRev != "r1" || runs[1].Output != "clever plan" {
		t.Fatalf("runs wrong: %+v", runs)
	}
	if runs[0].ID != "run1" || runs[0].Seq != 1 {
		t.Fatalf("run identity wrong: %+v", runs[0])
	}
	// §10c §C / I-6: attribution reaches the wire.
	if runs[1].TicketID != "t1" || runs[1].SessionID != "sess-1" {
		t.Fatalf("run attribution missing from DTO: %+v", runs[1])
	}
}
