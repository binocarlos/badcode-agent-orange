package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ── The event routes (spec §8.1–§8.5) ───────────────────────────────────────
//
// Four things live here and nothing else:
//
//   POST /agent/events          ingestion — the only way a sender gets an event
//                               into a project. The sender supplies {type, text};
//                               CORE stamps the envelope, always.
//   /agent/subscriptions        CRUD over the routing table.
//   GET  /agent/deliveries      read-only job history (the UI's events view).
//   POST /agent/project-token   mints a long-lived token so a headless poster
//                               (mail forwarder, webhook bridge) can reach the
//                               ingestion route without a browser login (§8.5).
//
// The router that turns deliveries into jobs is E3 and lives in agentd — these
// handlers only write the rows it polls.
//
// Project scoping: every handler derives the project from the authenticated
// Identity's Customer claim ("project" IS the customer claim — a namespacing
// concept, no project table) and passes it to a store method that filters on
// it. A row from another project is never found, so it can be neither read nor
// written. There is no route here that takes a project from the request body.

// EventStore is the slice of agentdb.Store the event routes need. It exists so
// hosts can supply their own implementation and so these handlers can be tested
// without a database; *agentdb.Store satisfies it (asserted below).
type EventStore interface {
	CreateProjectEvent(ctx context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error)
	ListProjectEvents(ctx context.Context, q agentdb.ProjectEventQuery) ([]*agentdb.ProjectEvent, error)

	CreateSubscription(ctx context.Context, sub *agentdb.Subscription, cw agentdb.ConfigWrite) (*agentdb.Subscription, error)
	GetSubscription(ctx context.Context, project, id string) (*agentdb.Subscription, error)
	ListSubscriptions(ctx context.Context, project string) ([]*agentdb.Subscription, error)
	UpdateSubscription(ctx context.Context, sub *agentdb.Subscription, cw agentdb.ConfigWrite) (*agentdb.Subscription, error)
	DeleteSubscription(ctx context.Context, project, id string, cw agentdb.ConfigWrite) error

	ListDeliveries(ctx context.Context, q agentdb.DeliveryQuery) ([]*agentdb.EventDelivery, error)
}

// The concrete store must always satisfy the seam.
var _ EventStore = (*agentdb.Store)(nil)

// ProjectTokenIssuer mints a bearer token scoped to the caller's project, for
// headless posters that cannot do a browser login (§8.5). The host chooses the
// lifetime and the signing key — this package only knows that a token came
// back. Nil in Config disables POST /agent/project-token (501).
type ProjectTokenIssuer func(ctx context.Context, id Identity) (token string, expiresAt int64, err error)

// eventsStore returns the configured event store, or writes 501 and returns
// nil when the host wired no database.
func (h *Handlers) eventsStore(w http.ResponseWriter) EventStore {
	if h.cfg.Events == nil {
		http.Error(w, "events are not configured on this host", http.StatusNotImplemented)
		return nil
	}
	return h.cfg.Events
}

// ── Ingestion ───────────────────────────────────────────────────────────────

// ingestEventBody is everything a sender may say. Any envelope field present in
// the body is ignored on purpose: the envelope is core's, and an external
// poster that could set `depth` or claim `source: "worker"` would defeat both
// the loop floor and every envelope filter.
type ingestEventBody struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// IngestEvent serves POST /agent/events — external ingestion (§8.5). The
// project comes from the JWT, the envelope from core: {source: "external",
// depth: 0}.
func (h *Handlers) IngestEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.eventsStore(w)
	if store == nil {
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
		return
	}
	var body ingestEventBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	body.Type = strings.TrimSpace(body.Type)
	if body.Type == "" {
		http.Error(w, "type is required", http.StatusBadRequest)
		return
	}
	ev, err := store.CreateProjectEvent(r.Context(), &agentdb.ProjectEvent{
		Project: id.Customer,
		Type:    body.Type,
		Text:    body.Text,
		Envelope: agentdb.EventEnvelope{
			Source: agentdb.EventSourceExternal,
			Depth:  0,
		},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, ev)
}

// ListEvents serves GET /agent/events — the project's event log, newest-first,
// optionally filtered by ?type=. Read-only observability for the events view.
func (h *Handlers) ListEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.eventsStore(w)
	if store == nil {
		return
	}
	events, err := store.ListProjectEvents(r.Context(), agentdb.ProjectEventQuery{
		Project: id.Customer,
		Type:    r.URL.Query().Get("type"),
		Limit:   queryInt(r, "limit", 0),
		Offset:  queryInt(r, "offset", 0),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

// ── Subscriptions ───────────────────────────────────────────────────────────

// subscriptionBody is the wire shape. Enabled is a pointer so an absent field
// means "the default" (true on create, unchanged on update) rather than false.
// Project is never accepted from the body — it comes from the token.
type subscriptionBody struct {
	EventType         string         `json:"event_type"`
	Filter            map[string]any `json:"filter"`
	Worker            string         `json:"worker"`
	MaxFiringsPerHour *int           `json:"max_firings_per_hour"`
	Enabled           *bool          `json:"enabled"`
}

// Subscriptions serves GET (list) and POST (create) on /agent/subscriptions.
func (h *Handlers) Subscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listSubscriptions(w, r)
	case http.MethodPost:
		h.createSubscription(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Subscription serves GET/PUT/DELETE on /agent/subscriptions/{id}.
func (h *Handlers) Subscription(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getSubscription(w, r)
	case http.MethodPut, http.MethodPatch:
		h.updateSubscription(w, r)
	case http.MethodDelete:
		h.deleteSubscription(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handlers) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.eventsStore(w)
	if store == nil {
		return
	}
	subs, err := store.ListSubscriptions(r.Context(), id.Customer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"subscriptions": subs})
}

func (h *Handlers) createSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.eventsStore(w)
	if store == nil {
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
		return
	}
	var body subscriptionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	sub := &agentdb.Subscription{
		Project:   id.Customer,
		EventType: strings.TrimSpace(body.EventType),
		Filter:    agentdb.JSONMap(body.Filter),
		Worker:    strings.TrimSpace(body.Worker),
		Enabled:   true, // a subscription you just created is live unless you say otherwise
	}
	if body.Enabled != nil {
		sub.Enabled = *body.Enabled
	}
	if body.MaxFiringsPerHour != nil {
		sub.MaxFiringsPerHour = *body.MaxFiringsPerHour
	}
	created, err := store.CreateSubscription(r.Context(), sub, humanEdit())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, created)
}

func (h *Handlers) getSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.eventsStore(w)
	if store == nil {
		return
	}
	sub, err := store.GetSubscription(r.Context(), id.Customer, r.PathValue("id"))
	if err != nil {
		http.Error(w, "subscription not found", http.StatusNotFound)
		return
	}
	writeJSON(w, sub)
}

func (h *Handlers) updateSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.eventsStore(w)
	if store == nil {
		return
	}
	// Read the current row through the project filter first: this is what makes
	// a cross-project PUT a 404 instead of a write.
	existing, err := store.GetSubscription(r.Context(), id.Customer, r.PathValue("id"))
	if err != nil {
		http.Error(w, "subscription not found", http.StatusNotFound)
		return
	}
	var body subscriptionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if v := strings.TrimSpace(body.EventType); v != "" {
		existing.EventType = v
	}
	if body.Filter != nil {
		existing.Filter = agentdb.JSONMap(body.Filter)
	}
	if v := strings.TrimSpace(body.Worker); v != "" {
		existing.Worker = v
	}
	if body.MaxFiringsPerHour != nil {
		existing.MaxFiringsPerHour = *body.MaxFiringsPerHour
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	updated, err := store.UpdateSubscription(r.Context(), existing, humanEdit())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, updated)
}

func (h *Handlers) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.eventsStore(w)
	if store == nil {
		return
	}
	if err := store.DeleteSubscription(r.Context(), id.Customer, r.PathValue("id"), humanEdit()); err != nil {
		http.Error(w, "subscription not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}

// ── Deliveries ──────────────────────────────────────────────────────────────

// ListDeliveries serves GET /agent/deliveries — the job-history spine
// (§8.4 step 2), newest-first, filterable by ?event_id / ?subscription_id /
// ?status. Read-only: statuses are written by the router, never by a client.
func (h *Handlers) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.eventsStore(w)
	if store == nil {
		return
	}
	rows, err := store.ListDeliveries(r.Context(), agentdb.DeliveryQuery{
		Project:        id.Customer,
		EventID:        r.URL.Query().Get("event_id"),
		SubscriptionID: r.URL.Query().Get("subscription_id"),
		Status:         r.URL.Query().Get("status"),
		Limit:          queryInt(r, "limit", 0),
		Offset:         queryInt(r, "offset", 0),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"deliveries": rows})
}

// ── Project token ───────────────────────────────────────────────────────────

// ProjectToken serves POST /agent/project-token: exchanges the caller's ordinary
// session credential for a long-lived token scoped to the same project, so a
// mail forwarder or webhook bridge can POST /agent/events unattended (§8.5).
//
// It grants nothing new — the minted token carries exactly the project the
// caller already holds — so the only authorization needed is the one the auth
// middleware already performed.
func (h *Handlers) ProjectToken(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if h.cfg.ProjectTokenIssuer == nil {
		http.Error(w, "project tokens are not configured on this host", http.StatusNotImplemented)
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
		return
	}
	token, expiresAt, err := h.cfg.ProjectTokenIssuer(r.Context(), id)
	if err != nil {
		http.Error(w, "token generation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"project":    id.Customer,
		"token":      token,
		"expires_at": expiresAt,
	})
}

// queryInt reads a non-negative integer query parameter, falling back to def.
// queryInt64 reads a non-negative int64 query parameter (unix-millisecond
// timestamps and sequence cursors overflow int on a 32-bit build). Absent,
// malformed or negative → 0, which every store query reads as "unbounded".
func queryInt64(r *http.Request, key string) int64 {
	v, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func queryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil && v >= 0 {
		return v
	}
	return def
}
