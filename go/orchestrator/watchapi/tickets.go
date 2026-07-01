package watchapi

import (
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// ListTickets serves GET /api/tickets?status= (default all, per contracts §5/§8).
// status="needs_human" yields the pending approvals + escalations the operator
// acts on (the teacher's desk).
func (h *Handlers) ListTickets(w http.ResponseWriter, r *http.Request) {
	status := orchestrator.TicketStatus(r.URL.Query().Get("status"))
	tickets, err := h.cfg.Tickets.List(r.Context(), status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tickets == nil {
		tickets = []orchestrator.Ticket{}
	}
	writeJSON(w, http.StatusOK, tickets)
}
