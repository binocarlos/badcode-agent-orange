package httpapi

// The config-log read route (spec §15.10, docs/product/09-config-log.md).
//
//	GET /agent/config-events
//	  query: ?action=<exact|prefix*>&actor_worker=<name>&since=<ms>&until=<ms>
//	         &limit=<n>&before_seq=<seq>
//	  auth : the ordinary session JWT; the project comes from the Customer claim,
//	         never from the query (P5) — same posture as GET /agent/events.
//	  200  : {"config_events": [ConfigEvent, …]}   // newest-first
//
// Read-only, and deliberately so: a config event exists only as the shadow of a
// real configuration mutation (§15.4), so there is no POST here and never will
// be. Writing one directly would be forging history.
//
// Ordering is by `seq`, not by `created_at` (J2): `created_at` is a millisecond
// wall clock and the id is a random uuid, so two writes inside one millisecond
// have no total order. `seq` is allocated inside the config-event transaction,
// so seq order IS commit order. That is why the page cursor is `before_seq` and
// not a timestamp — `since`/`until` still filter on the clock, because a human
// asking "what changed on Tuesday" means the clock.

import (
	"context"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ConfigLogStore is the slice of agentdb.Store this route needs. It exists so a
// host can supply its own implementation and so the handler can be tested
// without a database; *agentdb.Store satisfies it.
type ConfigLogStore interface {
	ListConfigEvents(ctx context.Context, q agentdb.ConfigEventQuery) ([]*agentdb.ConfigEvent, error)
}

// The concrete store must always satisfy the seam.
var _ ConfigLogStore = (*agentdb.Store)(nil)

// ListConfigEvents serves GET /agent/config-events — the changelog's read path.
func (h *Handlers) ListConfigEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if h.cfg.ConfigLog == nil {
		http.Error(w, "the config log is not configured on this host", http.StatusNotImplemented)
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	events, err := h.cfg.ConfigLog.ListConfigEvents(r.Context(), agentdb.ConfigEventQuery{
		Project:     id.Customer,
		Action:      q.Get("action"),
		ActorWorker: q.Get("actor_worker"),
		Since:       queryInt64(r, "since"),
		Until:       queryInt64(r, "until"),
		BeforeSeq:   queryInt64(r, "before_seq"),
		Limit:       queryInt(r, "limit", 0),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if events == nil {
		events = []*agentdb.ConfigEvent{}
	}
	writeJSON(w, map[string]any{"config_events": events})
}
