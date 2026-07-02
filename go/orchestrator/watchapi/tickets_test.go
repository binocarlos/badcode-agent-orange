package watchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestListTicketsFiltersNeedsHuman(t *testing.T) {
	h, d := newTestHandlers(t)
	ctx := context.Background()
	_, _ = d.tickets.Create(ctx, orchestrator.Ticket{Title: "draft", Status: orchestrator.StatusNeedsHuman})
	_, _ = d.tickets.Create(ctx, orchestrator.Ticket{Title: "wip", Status: orchestrator.StatusInProgress})

	req := httptest.NewRequest("GET", "/api/tickets?status=needs_human", nil)
	rec := httptest.NewRecorder()
	h.ListTickets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var got []orchestrator.Ticket
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Status != orchestrator.StatusNeedsHuman {
		t.Fatalf("filter wrong: %+v", got)
	}

	// no status → all
	rec = httptest.NewRecorder()
	h.ListTickets(rec, httptest.NewRequest("GET", "/api/tickets", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Fatalf("all wrong: %+v", got)
	}
}
