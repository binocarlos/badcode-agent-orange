package httpapi

// The attention-request read route (operator console design B1, spec §9).
//
//	GET /agent/attention-requests
//	  query: ?state=open|all  (default open — answered_at = 0 AND timed_out_at = 0)
//	         ?limit=<n>
//	  auth : the ordinary session JWT; the project comes from the Customer claim,
//	         never from the query (P5) — same posture as GET /agent/config-events.
//	  200  : {"attention_requests": [AttentionRequest, …]}   // newest-first
//
// Read-only. A request is answered by a human typing the next message in the
// thread (§9) and timed out by the sweep — there is no write here, because
// there is no approval state machine to drive.

import (
	"context"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// AttentionStore is the slice of agentdb.Store this route needs. It exists so a
// host can supply its own implementation and so the handler can be tested
// without a database; *agentdb.Store satisfies it.
type AttentionStore interface {
	ListAttentionRequests(ctx context.Context, q agentdb.AttentionRequestQuery) ([]*agentdb.AttentionRequest, error)
}

// The concrete store must always satisfy the seam.
var _ AttentionStore = (*agentdb.Store)(nil)

// ListAttentionRequests serves GET /agent/attention-requests — the Desk's Asks
// stack, carrying the message the worker wrote.
func (h *Handlers) ListAttentionRequests(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if h.cfg.Attention == nil {
		http.Error(w, "attention requests are not configured on this host", http.StatusNotImplemented)
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
		return
	}
	// Anything other than the two known words is the default rather than a 400:
	// a widened read is opt-in, so an unrecognised state can only ever narrow.
	all := r.URL.Query().Get("state") == "all"
	reqs, err := h.cfg.Attention.ListAttentionRequests(r.Context(), agentdb.AttentionRequestQuery{
		Project:         id.Customer,
		IncludeResolved: all,
		Limit:           queryInt(r, "limit", 0),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if reqs == nil {
		reqs = []*agentdb.AttentionRequest{}
	}
	writeJSON(w, map[string]any{"attention_requests": reqs})
}
