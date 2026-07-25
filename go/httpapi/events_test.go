package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fakeEventStore is an in-memory stand-in for *agentdb.Store (whose real
// migrations need Postgres). It reproduces the one behaviour these handlers
// depend on for safety: every read and write is filtered by project, and a row
// belonging to another project is simply not found. The real store proves the
// same contract against sqlite and Postgres in agentdb/events*_test.go.
type fakeEventStore struct {
	events []*agentdb.ProjectEvent
	subs   []*agentdb.Subscription
	dels   []*agentdb.EventDelivery

	createErr error
	nextID    int

	// Recorded arguments, so tests can assert what the handler passed down.
	lastEvent      *agentdb.ProjectEvent
	lastEventQuery agentdb.ProjectEventQuery
	lastDelQuery   agentdb.DeliveryQuery
	lastSubProject string
}

func newFakeEventStore() *fakeEventStore { return &fakeEventStore{} }

func (f *fakeEventStore) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func (f *fakeEventStore) CreateProjectEvent(_ context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error) {
	f.lastEvent = ev
	if f.createErr != nil {
		return nil, f.createErr
	}
	if ev.Project == "" || ev.Type == "" {
		return nil, errors.New("project and type are required")
	}
	stored := *ev
	stored.ID = f.id("ev")
	stored.OccurredAt = 1700000000
	f.events = append(f.events, &stored)
	return &stored, nil
}

func (f *fakeEventStore) ListProjectEvents(_ context.Context, q agentdb.ProjectEventQuery) ([]*agentdb.ProjectEvent, error) {
	f.lastEventQuery = q
	if q.Project == "" {
		return nil, errors.New("project is required")
	}
	out := []*agentdb.ProjectEvent{}
	for _, ev := range f.events {
		if ev.Project != q.Project {
			continue
		}
		if q.Type != "" && ev.Type != q.Type {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

func (f *fakeEventStore) CreateSubscription(_ context.Context, sub *agentdb.Subscription) (*agentdb.Subscription, error) {
	if sub.Project == "" || sub.EventType == "" || sub.Worker == "" {
		return nil, errors.New("project, event_type and worker are required")
	}
	stored := *sub
	stored.ID = f.id("sub")
	f.subs = append(f.subs, &stored)
	return &stored, nil
}

func (f *fakeEventStore) GetSubscription(_ context.Context, project, id string) (*agentdb.Subscription, error) {
	for _, s := range f.subs {
		if s.ID == id && s.Project == project {
			cp := *s
			return &cp, nil
		}
	}
	return nil, errors.New("subscription not found")
}

func (f *fakeEventStore) ListSubscriptions(_ context.Context, project string) ([]*agentdb.Subscription, error) {
	f.lastSubProject = project
	out := []*agentdb.Subscription{}
	for _, s := range f.subs {
		if s.Project == project {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeEventStore) UpdateSubscription(_ context.Context, sub *agentdb.Subscription) (*agentdb.Subscription, error) {
	for i, s := range f.subs {
		if s.ID == sub.ID && s.Project == sub.Project {
			cp := *sub
			f.subs[i] = &cp
			return &cp, nil
		}
	}
	return nil, errors.New("subscription not found")
}

func (f *fakeEventStore) DeleteSubscription(_ context.Context, project, id string) error {
	for i, s := range f.subs {
		if s.ID == id && s.Project == project {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			return nil
		}
	}
	return errors.New("subscription not found")
}

func (f *fakeEventStore) ListDeliveries(_ context.Context, q agentdb.DeliveryQuery) ([]*agentdb.EventDelivery, error) {
	f.lastDelQuery = q
	out := []*agentdb.EventDelivery{}
	for _, d := range f.dels {
		if d.Project != q.Project {
			continue
		}
		if q.Status != "" && d.Status != q.Status {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// newEventHandlers builds the handler set with the event seam wired.
func newEventHandlers(t *testing.T, store EventStore, identity IdentityFunc) *Handlers {
	t.Helper()
	return newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: identity,
		Events:   store,
	})
}

// do routes a request through the real Mux, so route patterns and method
// dispatch are exercised rather than the handler funcs in isolation.
func do(h *Handlers, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, r)
	return rec
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode body %s: %v", rec.Body, err)
	}
}

// ── Ingestion ───────────────────────────────────────────────────────────────

// TestEventsIngestStampsEnvelope is the security-relevant half of ingestion: a
// sender supplies only {type, text}, and core stamps source=external, depth=0.
// A poster that could set depth or claim source=worker would defeat both the
// §8.4 loop floor and every envelope filter.
func TestEventsIngestStampsEnvelope(t *testing.T) {
	store := newFakeEventStore()
	h := newEventHandlers(t, store, identityFor("acme"))

	rec := do(h, http.MethodPost, "/agent/events",
		`{"type":"email.received","text":"From: bob\nSubject: hi",
		  "envelope":{"depth":7,"source":"worker","worker":"impostor"},
		  "project":"other-co"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}

	got := store.lastEvent
	if got.Project != "acme" {
		t.Fatalf("project must come from the token, not the body: %q", got.Project)
	}
	if got.Type != "email.received" || !strings.Contains(got.Text, "Subject: hi") {
		t.Fatalf("type/text not carried through: %+v", got)
	}
	if got.Envelope.Source != agentdb.EventSourceExternal {
		t.Fatalf("source must be stamped external, got %q", got.Envelope.Source)
	}
	if got.Envelope.Depth != 0 {
		t.Fatalf("depth must be stamped 0, got %d (a sender-set depth defeats the loop floor)", got.Envelope.Depth)
	}
	if got.Envelope.Worker != "" || got.Envelope.SessionID != "" {
		t.Fatalf("a sender must not be able to claim a worker identity: %+v", got.Envelope)
	}

	var out agentdb.ProjectEvent
	decodeInto(t, rec, &out)
	if out.ID == "" {
		t.Fatalf("response must carry the stored id: %s", rec.Body)
	}
}

func TestEventsIngestValidation(t *testing.T) {
	tests := []struct {
		name     string
		project  string
		body     string
		wantCode int
	}{
		{"missing type", "acme", `{"text":"hello"}`, http.StatusBadRequest},
		{"blank type", "acme", `{"type":"   ","text":"hello"}`, http.StatusBadRequest},
		{"malformed json", "acme", `{"type":`, http.StatusBadRequest},
		{"no project in token", "", `{"type":"email.received"}`, http.StatusForbidden},
		{"text may be empty", "acme", `{"type":"ping"}`, http.StatusCreated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newEventHandlers(t, newFakeEventStore(), identityFor(tc.project))
			if tc.project == "" {
				h = newEventHandlers(t, newFakeEventStore(), func(*http.Request) (Identity, error) {
					return Identity{UserEmail: "u@x.com"}, nil
				})
			}
			rec := do(h, http.MethodPost, "/agent/events", tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("want %d, got %d (%s)", tc.wantCode, rec.Code, rec.Body)
			}
		})
	}
}

// TestEventsRoutesNeedAStore proves the routes degrade honestly rather than
// panicking when a host wires no database.
func TestEventsRoutesNeedAStore(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{}, Identity: identityFor("acme"),
	})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/agent/events"},
		{http.MethodGet, "/agent/events"},
		{http.MethodGet, "/agent/subscriptions"},
		{http.MethodPost, "/agent/subscriptions"},
		{http.MethodGet, "/agent/subscriptions/sub-1"},
		{http.MethodDelete, "/agent/subscriptions/sub-1"},
		{http.MethodGet, "/agent/deliveries"},
	} {
		rec := do(h, tc.method, tc.path, `{}`)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s: want 501, got %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestEventsList(t *testing.T) {
	store := newFakeEventStore()
	h := newEventHandlers(t, store, identityFor("acme"))
	do(h, http.MethodPost, "/agent/events", `{"type":"email.received","text":"a"}`)
	do(h, http.MethodPost, "/agent/events", `{"type":"schedule.fired","text":"b"}`)

	rec := do(h, http.MethodGet, "/agent/events?type=schedule.fired&limit=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var out struct {
		Events []*agentdb.ProjectEvent `json:"events"`
	}
	decodeInto(t, rec, &out)
	if len(out.Events) != 1 || out.Events[0].Type != "schedule.fired" {
		t.Fatalf("type filter not applied: %s", rec.Body)
	}
	if store.lastEventQuery.Project != "acme" || store.lastEventQuery.Limit != 5 {
		t.Fatalf("query not built from token+params: %+v", store.lastEventQuery)
	}
}

// ── Subscriptions ───────────────────────────────────────────────────────────

func TestSubscriptionsCRUDOverHTTP(t *testing.T) {
	store := newFakeEventStore()
	h := newEventHandlers(t, store, identityFor("acme"))

	// Create. Enabled is absent → live; max_firings_per_hour absent → 0.
	rec := do(h, http.MethodPost, "/agent/subscriptions",
		`{"event_type":"worker.finished","worker":"email-reviewer",
		  "filter":{"worker":"email-answerer"},"project":"other-co"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d body=%s", rec.Code, rec.Body)
	}
	var created agentdb.Subscription
	decodeInto(t, rec, &created)
	if created.Project != "acme" {
		t.Fatalf("project must come from the token, not the body: %q", created.Project)
	}
	if !created.Enabled {
		t.Fatalf("a new subscription is live unless the body says otherwise")
	}
	if created.MaxFiringsPerHour != 0 {
		t.Fatalf("max_firings_per_hour must default to 0 = unlimited, got %d", created.MaxFiringsPerHour)
	}
	if created.Filter["worker"] != "email-answerer" {
		t.Fatalf("filter not carried: %+v", created.Filter)
	}

	// Get.
	rec = do(h, http.MethodGet, "/agent/subscriptions/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status %d body=%s", rec.Code, rec.Body)
	}

	// Update: a partial body touches only what it names.
	rec = do(h, http.MethodPut, "/agent/subscriptions/"+created.ID,
		`{"max_firings_per_hour":12,"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status %d body=%s", rec.Code, rec.Body)
	}
	var updated agentdb.Subscription
	decodeInto(t, rec, &updated)
	if updated.MaxFiringsPerHour != 12 {
		t.Fatalf("cap not applied: %+v", updated)
	}
	if updated.Enabled {
		t.Fatalf("explicit enabled:false must disable — a *bool body field, not a bare bool")
	}
	if updated.Worker != "email-reviewer" || updated.EventType != "worker.finished" {
		t.Fatalf("untouched fields must survive a partial update: %+v", updated)
	}

	// Explicit zero must be writable back (0 = unlimited is a real value).
	rec = do(h, http.MethodPut, "/agent/subscriptions/"+created.ID, `{"max_firings_per_hour":0}`)
	decodeInto(t, rec, &updated)
	if updated.MaxFiringsPerHour != 0 {
		t.Fatalf("0 = unlimited must be writable, got %d", updated.MaxFiringsPerHour)
	}

	// List.
	rec = do(h, http.MethodGet, "/agent/subscriptions", "")
	var list struct {
		Subscriptions []*agentdb.Subscription `json:"subscriptions"`
	}
	decodeInto(t, rec, &list)
	if len(list.Subscriptions) != 1 {
		t.Fatalf("list: %s", rec.Body)
	}

	// Delete, then delete again.
	if rec = do(h, http.MethodDelete, "/agent/subscriptions/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: status %d body=%s", rec.Code, rec.Body)
	}
	if rec = do(h, http.MethodDelete, "/agent/subscriptions/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: want 404, got %d", rec.Code)
	}
}

func TestSubscriptionsBadInput(t *testing.T) {
	store := newFakeEventStore()
	h := newEventHandlers(t, store, identityFor("acme"))

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
	}{
		{"create without worker", http.MethodPost, "/agent/subscriptions", `{"event_type":"email.*"}`, http.StatusBadRequest},
		{"create without event_type", http.MethodPost, "/agent/subscriptions", `{"worker":"w"}`, http.StatusBadRequest},
		{"create malformed", http.MethodPost, "/agent/subscriptions", `{`, http.StatusBadRequest},
		{"get missing", http.MethodGet, "/agent/subscriptions/nope", "", http.StatusNotFound},
		{"update missing", http.MethodPut, "/agent/subscriptions/nope", `{"worker":"w"}`, http.StatusNotFound},
		{"unsupported method", http.MethodPatch, "/agent/subscriptions", `{}`, http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, tc.method, tc.path, tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("want %d, got %d (%s)", tc.wantCode, rec.Code, rec.Body)
			}
		})
	}
}

// TestSubscriptionsProjectIsolationOverHTTP is the §12 negative test at the
// HTTP boundary: a token for one project can neither read, list, update nor
// delete another project's subscription, and the other project's row is
// untouched afterwards.
func TestSubscriptionsProjectIsolationOverHTTP(t *testing.T) {
	store := newFakeEventStore()
	acme := newEventHandlers(t, store, identityFor("acme"))
	other := newEventHandlers(t, store, identityFor("other-co"))

	rec := do(other, http.MethodPost, "/agent/subscriptions",
		`{"event_type":"email.received","worker":"their-answerer"}`)
	var theirs agentdb.Subscription
	decodeInto(t, rec, &theirs)
	if theirs.ID == "" {
		t.Fatalf("seed failed: %s", rec.Body)
	}

	for _, tc := range []struct{ name, method, body string }{
		{"get", http.MethodGet, ""},
		{"update", http.MethodPut, `{"worker":"hijacked"}`},
		{"delete", http.MethodDelete, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(acme, tc.method, "/agent/subscriptions/"+theirs.ID, tc.body)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("cross-project %s: want 404, got %d (%s)", tc.name, rec.Code, rec.Body)
			}
		})
	}

	// The row survived untouched.
	rec = do(other, http.MethodGet, "/agent/subscriptions/"+theirs.ID, "")
	var still agentdb.Subscription
	decodeInto(t, rec, &still)
	if rec.Code != http.StatusOK || still.Worker != "their-answerer" {
		t.Fatalf("other project's subscription was disturbed: %d %s", rec.Code, rec.Body)
	}

	// And listing shows acme nothing.
	rec = do(acme, http.MethodGet, "/agent/subscriptions", "")
	var list struct {
		Subscriptions []*agentdb.Subscription `json:"subscriptions"`
	}
	decodeInto(t, rec, &list)
	if len(list.Subscriptions) != 0 {
		t.Fatalf("acme must see none of other-co's subscriptions: %s", rec.Body)
	}
}

// TestEventsProjectIsolationOverHTTP proves the ingestion and read routes are
// scoped by the token too — the project is never taken from the request.
func TestEventsProjectIsolationOverHTTP(t *testing.T) {
	store := newFakeEventStore()
	acme := newEventHandlers(t, store, identityFor("acme"))
	other := newEventHandlers(t, store, identityFor("other-co"))

	do(other, http.MethodPost, "/agent/events", `{"type":"email.received","text":"theirs"}`)

	rec := do(acme, http.MethodGet, "/agent/events", "")
	var out struct {
		Events []*agentdb.ProjectEvent `json:"events"`
	}
	decodeInto(t, rec, &out)
	if len(out.Events) != 0 {
		t.Fatalf("acme must not see other-co's events: %s", rec.Body)
	}

	// Even an explicit project in the body cannot redirect the write.
	do(acme, http.MethodPost, "/agent/events", `{"type":"x","text":"mine","project":"other-co"}`)
	if store.lastEvent.Project != "acme" {
		t.Fatalf("body project overrode the token: %q", store.lastEvent.Project)
	}
}

// ── Deliveries ──────────────────────────────────────────────────────────────

func TestDeliveriesListOverHTTP(t *testing.T) {
	store := newFakeEventStore()
	store.dels = []*agentdb.EventDelivery{
		{ID: "d1", Project: "acme", EventID: "ev-1", SubscriptionID: "sub-1", Status: agentdb.DeliveryOK},
		{ID: "d2", Project: "acme", EventID: "ev-2", SubscriptionID: "sub-1", Status: agentdb.DeliveryPending},
		{ID: "d3", Project: "other-co", EventID: "ev-3", SubscriptionID: "sub-9", Status: agentdb.DeliveryOK},
	}
	h := newEventHandlers(t, store, identityFor("acme"))

	rec := do(h, http.MethodGet, "/agent/deliveries", "")
	var all struct {
		Deliveries []*agentdb.EventDelivery `json:"deliveries"`
	}
	decodeInto(t, rec, &all)
	if len(all.Deliveries) != 2 {
		t.Fatalf("project scoping: want 2, got %d (%s)", len(all.Deliveries), rec.Body)
	}

	rec = do(h, http.MethodGet, "/agent/deliveries?status=pending&subscription_id=sub-1&event_id=ev-2", "")
	var filtered struct {
		Deliveries []*agentdb.EventDelivery `json:"deliveries"`
	}
	decodeInto(t, rec, &filtered)
	if len(filtered.Deliveries) != 1 || filtered.Deliveries[0].ID != "d2" {
		t.Fatalf("filters not passed down: %s", rec.Body)
	}
	if store.lastDelQuery.Project != "acme" ||
		store.lastDelQuery.Status != agentdb.DeliveryPending ||
		store.lastDelQuery.SubscriptionID != "sub-1" ||
		store.lastDelQuery.EventID != "ev-2" {
		t.Fatalf("delivery query not built from token+params: %+v", store.lastDelQuery)
	}
}

// ── Project token ───────────────────────────────────────────────────────────

// TestEventsProjectTokenMinting covers the headless-poster path of §8.5: a
// token scoped to exactly the caller's project, and a 501 when the host wired
// no issuer.
func TestEventsProjectTokenMinting(t *testing.T) {
	t.Run("no issuer configured", func(t *testing.T) {
		h := newEventHandlers(t, newFakeEventStore(), identityFor("acme"))
		if rec := do(h, http.MethodPost, "/agent/project-token", `{}`); rec.Code != http.StatusNotImplemented {
			t.Fatalf("want 501, got %d", rec.Code)
		}
	})

	t.Run("mints for the caller's project only", func(t *testing.T) {
		var sawIdentity Identity
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{}, Identity: identityFor("acme"),
			Events: newFakeEventStore(),
			ProjectTokenIssuer: func(_ context.Context, id Identity) (string, int64, error) {
				sawIdentity = id
				return "tok-for-" + id.Customer, 1799999999, nil
			},
		})
		rec := do(h, http.MethodPost, "/agent/project-token", `{"project":"other-co"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body=%s", rec.Code, rec.Body)
		}
		var out struct {
			Project   string `json:"project"`
			Token     string `json:"token"`
			ExpiresAt int64  `json:"expires_at"`
		}
		decodeInto(t, rec, &out)
		if sawIdentity.Customer != "acme" {
			t.Fatalf("issuer must be handed the token's project, got %q", sawIdentity.Customer)
		}
		if out.Project != "acme" || out.Token != "tok-for-acme" {
			t.Fatalf("a body-supplied project must not widen the grant: %s", rec.Body)
		}
		if out.ExpiresAt != 1799999999 {
			t.Fatalf("expiry not surfaced: %s", rec.Body)
		}
	})

	t.Run("issuer failure is a 500", func(t *testing.T) {
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{}, Identity: identityFor("acme"),
			ProjectTokenIssuer: func(context.Context, Identity) (string, int64, error) {
				return "", 0, errors.New("keyring down")
			},
		})
		if rec := do(h, http.MethodPost, "/agent/project-token", `{}`); rec.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", rec.Code)
		}
	})

	t.Run("no project in token", func(t *testing.T) {
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{},
			Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "u@x.com"}, nil },
			ProjectTokenIssuer: func(context.Context, Identity) (string, int64, error) {
				t.Fatal("issuer must not be called without a project")
				return "", 0, nil
			},
		})
		if rec := do(h, http.MethodPost, "/agent/project-token", `{}`); rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
	})
}

// TestEventsUnauthorizedIsRefused proves every event route goes through the
// host's identity extractor first.
func TestEventsUnauthorizedIsRefused(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{}, errors.New("nope") },
		Events:   newFakeEventStore(),
		ProjectTokenIssuer: func(context.Context, Identity) (string, int64, error) {
			t.Fatal("issuer reached without authentication")
			return "", 0, nil
		},
	})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/agent/events"},
		{http.MethodGet, "/agent/events"},
		{http.MethodGet, "/agent/subscriptions"},
		{http.MethodPost, "/agent/subscriptions"},
		{http.MethodGet, "/agent/subscriptions/s1"},
		{http.MethodPut, "/agent/subscriptions/s1"},
		{http.MethodDelete, "/agent/subscriptions/s1"},
		{http.MethodGet, "/agent/deliveries"},
		{http.MethodPost, "/agent/project-token"},
	} {
		rec := do(h, tc.method, tc.path, `{}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: want 401, got %d", tc.method, tc.path, rec.Code)
		}
	}
}
