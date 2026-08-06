package agentdb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newEventStore returns a Store backed by a temp-file sqlite DB with the three
// event-spine tables created via AutoMigrate (the production Postgres
// migrations cannot run on sqlite — those are covered by the live-PG suite).
func newEventStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "events_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&ProjectEvent{}, &Subscription{}, &EventDelivery{}, &ConfigEvent{}); err != nil {
		t.Fatalf("automigrate event tables: %v", err)
	}
	return &Store{gdb: db}
}

func externalEnvelope() EventEnvelope {
	return EventEnvelope{Source: EventSourceExternal, Depth: 0}
}

// seedEvent appends an event, failing the test on error.
func seedEvent(t *testing.T, s *Store, project, typ, text string) *ProjectEvent {
	t.Helper()
	ev, err := s.CreateProjectEvent(context.Background(), &ProjectEvent{
		Project: project, Type: typ, Text: text, Envelope: externalEnvelope(),
	})
	if err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return ev
}

// seedSubscription creates an enabled subscription, failing the test on error.
func seedSubscription(t *testing.T, s *Store, project, eventType, worker string) *Subscription {
	t.Helper()
	sub, err := s.CreateSubscription(context.Background(), &Subscription{
		Project: project, EventType: eventType, Worker: worker, Enabled: true,
	}, ConfigWrite{})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return sub
}

// ── The vocabulary (the plan's explicit requirement) ────────────────────────

// TestDeliveriesStatusVocabularyIsExact pins the delivery-status vocabulary to
// EXACTLY the six values of §8.4. It is deliberately brittle: `dropped` was
// removed on 2026-07-25 along with the per-subscription concurrency mode that
// would have produced it, and nothing may quietly add a seventh status.
func TestDeliveriesStatusVocabularyIsExact(t *testing.T) {
	want := []string{"pending", "running", "ok", "failed", "awaiting_human", "rate_limited"}
	if len(DeliveryStatuses) != len(want) {
		t.Fatalf("vocabulary size: want %d (%v), got %d (%v)",
			len(want), want, len(DeliveryStatuses), DeliveryStatuses)
	}
	for i, v := range want {
		if DeliveryStatuses[i] != v {
			t.Fatalf("vocabulary[%d]: want %q, got %q (full: %v)", i, v, DeliveryStatuses[i], DeliveryStatuses)
		}
	}
	// The constants and the list cannot drift apart.
	for _, c := range []string{
		DeliveryPending, DeliveryRunning, DeliveryOK,
		DeliveryFailed, DeliveryAwaitingHuman, DeliveryRateLimited,
	} {
		if !ValidDeliveryStatus(c) {
			t.Fatalf("constant %q is not in DeliveryStatuses", c)
		}
	}
	// Everything else is refused — notably the removed `dropped`.
	for _, bad := range []string{"dropped", "queued", "success", "OK", "", "Pending"} {
		if ValidDeliveryStatus(bad) {
			t.Fatalf("%q must not be a legal delivery status", bad)
		}
	}
}

// ── project_events ──────────────────────────────────────────────────────────

func TestEventsCreateValidation(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		in      *ProjectEvent
		wantErr string
	}{
		{"nil event", nil, "project event is required"},
		{"no project", &ProjectEvent{Type: "email.received", Envelope: externalEnvelope()}, "project is required"},
		{"blank project", &ProjectEvent{Project: "  ", Type: "x", Envelope: externalEnvelope()}, "project is required"},
		{"no type", &ProjectEvent{Project: "acme", Envelope: externalEnvelope()}, "event type is required"},
		{"no source", &ProjectEvent{Project: "acme", Type: "x"}, "envelope source is required"},
		{"bad source", &ProjectEvent{Project: "acme", Type: "x", Envelope: EventEnvelope{Source: "hacker"}}, "invalid envelope source"},
		{"negative depth", &ProjectEvent{Project: "acme", Type: "x", Envelope: EventEnvelope{Source: EventSourceCore, Depth: -1}}, "depth must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateProjectEvent(ctx, tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestEventsEnvelopeRoundTrip proves the core-stamped envelope survives the
// jsonb column intact — including the two booleans that subscriptions filter
// on, which must be present in the stored JSON even when false.
func TestEventsEnvelopeRoundTrip(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	in := EventEnvelope{
		Depth:              3,
		Source:             EventSourceWorker,
		Worker:             "email-answerer",
		SessionID:          "sess-1",
		Interactive:        false,
		AttentionRequested: true,
		Reason:             "",
	}
	created, err := s.CreateProjectEvent(ctx, &ProjectEvent{
		Project: "acme", Type: "worker.finished", Text: "transcript", Envelope: in,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.OccurredAt == 0 {
		t.Fatalf("store must stamp id and occurred_at: %+v", created)
	}
	if created.Delivered {
		t.Fatalf("a fresh event must land undelivered (§8.4 step 1)")
	}

	got, err := s.GetProjectEvent(ctx, "acme", created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Envelope != in {
		t.Fatalf("envelope round-trip: want %+v, got %+v", in, got.Envelope)
	}

	// The serialised form keeps the filterable booleans explicit.
	raw, err := in.Value()
	if err != nil {
		t.Fatalf("value: %v", err)
	}
	for _, key := range []string{`"interactive"`, `"attention_requested"`, `"depth"`, `"worker"`} {
		if !strings.Contains(raw.(string), key) {
			t.Fatalf("serialised envelope %s must contain %s", raw, key)
		}
	}
	// reason is the one omitempty field: absent unless worker.failed set it.
	if strings.Contains(raw.(string), `"reason"`) {
		t.Fatalf("empty reason must not be serialised: %s", raw)
	}
}

func TestEventsListAndUndeliveredFlow(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	a := seedEvent(t, s, "acme", "email.received", "first")
	b := seedEvent(t, s, "acme", "email.received", "second")
	c := seedEvent(t, s, "acme", "schedule.fired", "write the tweet")

	all, err := s.ListProjectEvents(ctx, ProjectEventQuery{Project: "acme"})
	if err != nil || len(all) != 3 {
		t.Fatalf("list: got %d err=%v", len(all), err)
	}

	typed, err := s.ListProjectEvents(ctx, ProjectEventQuery{Project: "acme", Type: "schedule.fired"})
	if err != nil || len(typed) != 1 || typed[0].ID != c.ID {
		t.Fatalf("type filter: %+v err=%v", typed, err)
	}

	// The router sees all three, oldest-first.
	und, err := s.ListUndeliveredProjectEvents(ctx, 10)
	if err != nil || len(und) != 3 {
		t.Fatalf("undelivered: got %d err=%v", len(und), err)
	}

	if err := s.MarkProjectEventDelivered(ctx, a.ID); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	und, err = s.ListUndeliveredProjectEvents(ctx, 10)
	if err != nil || len(und) != 2 {
		t.Fatalf("after mark: got %d err=%v", len(und), err)
	}
	for _, ev := range und {
		if ev.ID == a.ID {
			t.Fatalf("delivered event still in the undelivered poll")
		}
	}
	_ = b

	if err := s.MarkProjectEventDelivered(ctx, "no-such-event"); err == nil {
		t.Fatalf("marking a missing event must fail loudly")
	}
}

// TestEventsProjectIsolation is the §12 negative test for the event log: one
// project can neither read nor enumerate another's events.
func TestEventsProjectIsolation(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	mine := seedEvent(t, s, "acme", "email.received", "mine")
	theirs := seedEvent(t, s, "other-co", "email.received", "theirs")

	if _, err := s.GetProjectEvent(ctx, "acme", theirs.ID); err == nil {
		t.Fatalf("acme must not read other-co's event")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("cross-project read must look like not-found, got %v", err)
	}
	if _, err := s.GetProjectEvent(ctx, "other-co", mine.ID); err == nil {
		t.Fatalf("other-co must not read acme's event")
	}

	list, err := s.ListProjectEvents(ctx, ProjectEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("list leaked across projects: %+v", list)
	}
	if _, err := s.ListProjectEvents(ctx, ProjectEventQuery{}); err == nil {
		t.Fatalf("an unscoped list must be refused, not answered")
	}
}

// ── subscriptions ───────────────────────────────────────────────────────────

func TestSubscriptionsValidation(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		in      *Subscription
		wantErr string
	}{
		{"no project", &Subscription{EventType: "email.*", Worker: "w"}, "project is required"},
		{"no event type", &Subscription{Project: "acme", Worker: "w"}, "event_type is required"},
		{"no worker", &Subscription{Project: "acme", EventType: "email.*"}, "worker is required"},
		{"interior star", &Subscription{Project: "acme", EventType: "em*il.received", Worker: "w"}, "trailing wildcard"},
		{"leading star", &Subscription{Project: "acme", EventType: "*.received", Worker: "w"}, "trailing wildcard"},
		{"match everything", &Subscription{Project: "acme", EventType: "*", Worker: "w"}, "not a supported pattern"},
		{"negative cap", &Subscription{Project: "acme", EventType: "email.received", Worker: "w", MaxFiringsPerHour: -1}, "must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.CreateSubscription(ctx, tc.in, ConfigWrite{}); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}

	// The two legal patterns are exact and trailing-`*`.
	for _, ok := range []string{"email.received", "email.*", "worker.finished"} {
		if _, err := s.CreateSubscription(ctx, &Subscription{
			Project: "acme", EventType: ok, Worker: "w", Enabled: true,
		}, ConfigWrite{}); err != nil {
			t.Fatalf("event_type %q must be legal: %v", ok, err)
		}
	}
}

// TestSubscriptionsCRUD covers the full lifecycle including the two fields the
// landscape fold added: max_firings_per_hour (0 = unlimited) and enabled.
func TestSubscriptionsCRUD(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	sub, err := s.CreateSubscription(ctx, &Subscription{
		Project:   "acme",
		EventType: "worker.finished",
		Filter:    JSONMap{"worker": "email-answerer"},
		Worker:    "email-reviewer",
		Enabled:   true,
	}, ConfigWrite{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sub.ID == "" {
		t.Fatalf("store must allocate an id")
	}
	if sub.MaxFiringsPerHour != 0 {
		t.Fatalf("max_firings_per_hour defaults to 0 = unlimited, got %d", sub.MaxFiringsPerHour)
	}

	got, err := s.GetSubscription(ctx, "acme", sub.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Filter["worker"] != "email-answerer" {
		t.Fatalf("filter round-trip: %+v", got.Filter)
	}
	if !got.Enabled {
		t.Fatalf("enabled must round-trip true")
	}

	// Rate limit + disable, both through Update.
	got.MaxFiringsPerHour = 12
	got.Enabled = false
	updated, err := s.UpdateSubscription(ctx, got, ConfigWrite{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.MaxFiringsPerHour != 12 {
		t.Fatalf("max_firings_per_hour: want 12, got %d", updated.MaxFiringsPerHour)
	}
	// The GORM `default` trap: a disabled subscription must STAY disabled.
	reread, err := s.GetSubscription(ctx, "acme", sub.ID)
	if err != nil || reread.Enabled {
		t.Fatalf("disabled subscription came back enabled: %+v err=%v", reread, err)
	}

	// Only enabled subscriptions are what the router matches against.
	live := seedSubscription(t, s, "acme", "email.received", "email-answerer")
	enabled, err := s.ListEnabledSubscriptions(ctx, "acme")
	if err != nil || len(enabled) != 1 || enabled[0].ID != live.ID {
		t.Fatalf("enabled list: %+v err=%v", enabled, err)
	}
	all, err := s.ListSubscriptions(ctx, "acme")
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: got %d err=%v", len(all), err)
	}

	if err := s.DeleteSubscription(ctx, "acme", sub.ID, ConfigWrite{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteSubscription(ctx, "acme", sub.ID, ConfigWrite{}); err == nil {
		t.Fatalf("deleting a missing subscription must fail loudly")
	}
}

// TestSubscriptionsProjectIsolation is the §12 negative test for the routing
// table: a scoped caller can neither read, list, update, nor delete across
// projects — and a cross-project delete must not silently succeed.
func TestSubscriptionsProjectIsolation(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	mine := seedSubscription(t, s, "acme", "email.received", "answerer")
	theirs := seedSubscription(t, s, "other-co", "email.received", "their-answerer")

	if _, err := s.GetSubscription(ctx, "acme", theirs.ID); err == nil {
		t.Fatalf("cross-project get must fail")
	}
	list, err := s.ListSubscriptions(ctx, "acme")
	if err != nil || len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("list leaked across projects: %+v err=%v", list, err)
	}

	// Update: claiming another project's row id under my own project must not
	// touch the other project's row.
	spoof := *theirs
	spoof.Project = "acme"
	spoof.Worker = "hijacked"
	if _, err := s.UpdateSubscription(ctx, &spoof, ConfigWrite{}); err == nil {
		t.Fatalf("cross-project update must fail")
	}
	stillTheirs, err := s.GetSubscription(ctx, "other-co", theirs.ID)
	if err != nil || stillTheirs.Worker != "their-answerer" {
		t.Fatalf("other project's row was modified: %+v err=%v", stillTheirs, err)
	}

	if err := s.DeleteSubscription(ctx, "acme", theirs.ID, ConfigWrite{}); err == nil {
		t.Fatalf("cross-project delete must fail")
	}
	if _, err := s.GetSubscription(ctx, "other-co", theirs.ID); err != nil {
		t.Fatalf("cross-project delete removed the row anyway: %v", err)
	}
}

// ── event_deliveries ────────────────────────────────────────────────────────

// TestDeliveriesIdempotency proves the at-least-once guard: a router that
// retries the same (event, subscription) pair gets the stored row back rather
// than starting a second job.
func TestDeliveriesIdempotency(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	ev := seedEvent(t, s, "acme", "email.received", "hello")
	sub := seedSubscription(t, s, "acme", "email.received", "email-answerer")

	first, created, err := s.EnsureDelivery(ctx, &EventDelivery{
		Project: "acme", EventID: ev.ID, SubscriptionID: sub.ID,
	})
	if err != nil || !created {
		t.Fatalf("first ensure: created=%v err=%v", created, err)
	}
	if first.Status != DeliveryPending {
		t.Fatalf("a new delivery starts pending, got %q", first.Status)
	}

	// Advance it, then retry: the retry must find the row as it is, not reset it.
	if _, err := s.UpdateDeliveryStatus(ctx, "acme", first.ID, DeliveryStatusUpdate{
		Status: DeliveryRunning, SessionID: "sess-9",
	}); err != nil {
		t.Fatalf("to running: %v", err)
	}
	again, created, err := s.EnsureDelivery(ctx, &EventDelivery{
		Project: "acme", EventID: ev.ID, SubscriptionID: sub.ID,
	})
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if created {
		t.Fatalf("a repeat (event, subscription) must NOT create a second delivery")
	}
	if again.ID != first.ID || again.Status != DeliveryRunning || again.SessionID != "sess-9" {
		t.Fatalf("retry must return the stored row untouched: %+v", again)
	}

	rows, err := s.ListDeliveries(ctx, DeliveryQuery{Project: "acme", EventID: ev.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("exactly one delivery must exist, got %d err=%v", len(rows), err)
	}
}

func TestDeliveriesValidation(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		in      *EventDelivery
		wantErr string
	}{
		{"nil", nil, "delivery is required"},
		{"no project", &EventDelivery{EventID: "e", SubscriptionID: "s"}, "project is required"},
		{"no event", &EventDelivery{Project: "acme", SubscriptionID: "s"}, "event_id is required"},
		{"no subscription", &EventDelivery{Project: "acme", EventID: "e"}, "subscription_id is required"},
		{"bad status", &EventDelivery{Project: "acme", EventID: "e", SubscriptionID: "s", Status: "dropped"}, "invalid delivery status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.EnsureDelivery(ctx, tc.in); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}

	// A query filtered on a status outside the vocabulary is a caller bug, not
	// an empty result set.
	if _, err := s.ListDeliveries(ctx, DeliveryQuery{Project: "acme", Status: "dropped"}); err == nil {
		t.Fatalf("listing by an illegal status must be refused")
	}
}

// TestDeliveriesLifecycleTimestamps pins the started_at/ended_at semantics:
// started_at on the first move to running, ended_at only on a terminal status,
// and awaiting_human explicitly NOT terminal (a paused job is not a finished
// one).
func TestDeliveriesLifecycleTimestamps(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	ev := seedEvent(t, s, "acme", "email.received", "hello")

	newDelivery := func(t *testing.T, subWorker string) *EventDelivery {
		t.Helper()
		sub := seedSubscription(t, s, "acme", "email.received", subWorker)
		d, _, err := s.EnsureDelivery(ctx, &EventDelivery{
			Project: "acme", EventID: ev.ID, SubscriptionID: sub.ID,
		})
		if err != nil {
			t.Fatalf("ensure: %v", err)
		}
		if d.StartedAt != 0 || d.EndedAt != 0 {
			t.Fatalf("pending delivery must carry no timestamps: %+v", d)
		}
		return d
	}

	tests := []struct {
		name        string
		terminal    string
		wantEndedAt bool
	}{
		{"ok is terminal", DeliveryOK, true},
		{"failed is terminal", DeliveryFailed, true},
		{"rate_limited is terminal", DeliveryRateLimited, true},
		{"awaiting_human is a pause", DeliveryAwaitingHuman, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDelivery(t, "w-"+tc.terminal)
			running, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID,
				DeliveryStatusUpdate{Status: DeliveryRunning, SessionID: "sess-1"})
			if err != nil {
				t.Fatalf("running: %v", err)
			}
			if running.StartedAt == 0 {
				t.Fatalf("running must stamp started_at")
			}
			if running.EndedAt != 0 {
				t.Fatalf("running must not stamp ended_at")
			}
			if running.SessionID != "sess-1" {
				t.Fatalf("dispatch must record the session: %+v", running)
			}

			done, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID, DeliveryStatusUpdate{Status: tc.terminal})
			if err != nil {
				t.Fatalf("%s: %v", tc.terminal, err)
			}
			if done.StartedAt != running.StartedAt {
				t.Fatalf("started_at must not move once set")
			}
			if tc.wantEndedAt && done.EndedAt == 0 {
				t.Fatalf("%s must stamp ended_at", tc.terminal)
			}
			if !tc.wantEndedAt && done.EndedAt != 0 {
				t.Fatalf("%s must leave ended_at unset (it is a pause, not an end)", tc.terminal)
			}
			// The session link survives a status change that omits it.
			if done.SessionID != "sess-1" {
				t.Fatalf("session link lost on transition: %+v", done)
			}
		})
	}

	// Every legal status is accepted; nothing else is.
	d := newDelivery(t, "vocab-probe")
	for _, st := range DeliveryStatuses {
		if _, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID, DeliveryStatusUpdate{Status: st}); err != nil {
			t.Fatalf("status %q must be accepted: %v", st, err)
		}
	}
	if _, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID, DeliveryStatusUpdate{Status: "dropped"}); err == nil {
		t.Fatalf("`dropped` must be refused — it is not in the vocabulary")
	}
}

// TestDeliveryFailureReason pins RD20's column: the reason dispatch already
// knows must survive the write and come back out of the store, an omitted
// reason must not erase a recorded one, and a delivery that stops being failed
// must not keep carrying a red explanation on a green row.
func TestDeliveryFailureReason(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	ev := seedEvent(t, s, "acme", "email.received", "hello")
	sub := seedSubscription(t, s, "acme", "email.received", "answerer")
	d, _, err := s.EnsureDelivery(ctx, &EventDelivery{
		Project: "acme", EventID: ev.ID, SubscriptionID: sub.ID,
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if d.FailureReason != "" {
		t.Fatalf("a pending delivery must carry no reason: %+v", d)
	}

	const reason = "start job: host port pool is exhausted"
	failed, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID,
		DeliveryStatusUpdate{Status: DeliveryFailed, FailureReason: reason})
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if failed.FailureReason != reason {
		t.Fatalf("failure_reason = %q, want %q", failed.FailureReason, reason)
	}

	// Read back through the list surface the API serves — a reason that only
	// exists on the returned struct would never reach a browser.
	rows, err := s.ListDeliveries(ctx, DeliveryQuery{Project: "acme", Status: DeliveryFailed})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].FailureReason != reason {
		t.Fatalf("listed rows lost the reason: %+v", rows)
	}

	// A later failed transition that carries no reason keeps the one recorded.
	again, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID, DeliveryStatusUpdate{Status: DeliveryFailed})
	if err != nil {
		t.Fatalf("re-fail: %v", err)
	}
	if again.FailureReason != reason {
		t.Fatalf("an omitted reason must not erase one: %q", again.FailureReason)
	}

	// Not failed any more ⇒ no reason.
	ok, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID, DeliveryStatusUpdate{Status: DeliveryOK})
	if err != nil {
		t.Fatalf("ok: %v", err)
	}
	if ok.FailureReason != "" {
		t.Fatalf("a non-failed delivery must carry no reason: %q", ok.FailureReason)
	}
}

// TestDeliveriesFiringCount covers what max_firings_per_hour is measured
// against: deliveries that consumed a firing, excluding the rate_limited rows
// that record refusals.
func TestDeliveriesFiringCount(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	sub := seedSubscription(t, s, "acme", "email.received", "answerer")

	for i, status := range []string{DeliveryOK, DeliveryRunning, DeliveryRateLimited} {
		ev := seedEvent(t, s, "acme", "email.received", "e")
		d, _, err := s.EnsureDelivery(ctx, &EventDelivery{
			Project: "acme", EventID: ev.ID, SubscriptionID: sub.ID,
		})
		if err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
		if _, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID, DeliveryStatusUpdate{Status: status}); err != nil {
			t.Fatalf("status %d: %v", i, err)
		}
	}

	n, err := s.CountSubscriptionFiringsSince(ctx, sub.ID, 0)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 consumed firings (rate_limited excluded), got %d", n)
	}

	// A window that starts in the future counts nothing.
	n, err = s.CountSubscriptionFiringsSince(ctx, sub.ID, eventsNow()+3600)
	if err != nil || n != 0 {
		t.Fatalf("future window: got %d err=%v", n, err)
	}
	if _, err := s.CountSubscriptionFiringsSince(ctx, "", 0); err == nil {
		t.Fatalf("counting without a subscription id must be refused")
	}
}

// TestDeliveriesProjectIsolation is the §12 negative test for the delivery log.
func TestDeliveriesProjectIsolation(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()

	theirEvent := seedEvent(t, s, "other-co", "email.received", "theirs")
	theirSub := seedSubscription(t, s, "other-co", "email.received", "their-worker")
	theirs, _, err := s.EnsureDelivery(ctx, &EventDelivery{
		Project: "other-co", EventID: theirEvent.ID, SubscriptionID: theirSub.ID,
	})
	if err != nil {
		t.Fatalf("seed delivery: %v", err)
	}

	rows, err := s.ListDeliveries(ctx, DeliveryQuery{Project: "acme"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("acme must see none of other-co's deliveries: %+v err=%v", rows, err)
	}
	if _, err := s.UpdateDeliveryStatus(ctx, "acme", theirs.ID,
		DeliveryStatusUpdate{Status: DeliveryFailed}); err == nil {
		t.Fatalf("cross-project status write must fail")
	}
	after, err := s.ListDeliveries(ctx, DeliveryQuery{Project: "other-co"})
	if err != nil || len(after) != 1 || after[0].Status != DeliveryPending {
		t.Fatalf("other project's delivery was modified: %+v err=%v", after, err)
	}
	if _, err := s.ListDeliveries(ctx, DeliveryQuery{}); err == nil {
		t.Fatalf("an unscoped delivery list must be refused")
	}
}

// TestAwaitingHumanHoldsNoCapacitySlot is the deadlock guard for §8.4's pause.
// A delivery parked at `awaiting_human` must not count against
// `max_concurrent_jobs` or `max_instances` — otherwise a worker with
// `max_instances: 1` whose job is waiting on a human who never replies would
// never run again, and parking the job would be an outage rather than a fix.
// "Active" means `running`, and only `running`.
func TestAwaitingHumanHoldsNoCapacitySlot(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	ev := seedEvent(t, s, "acme", "tweet.due", "time for a tweet")
	sub := seedSubscription(t, s, "acme", "tweet.due", "tweet-author")
	d, _, err := s.EnsureDelivery(ctx, &EventDelivery{
		Project: "acme", EventID: ev.ID, SubscriptionID: sub.ID, Worker: "tweet-author",
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID,
		DeliveryStatusUpdate{Status: DeliveryRunning, SessionID: "sess-1"}); err != nil {
		t.Fatalf("running: %v", err)
	}
	// While it runs it holds a slot — the control for the assertions below.
	if n, err := s.CountActiveDeliveriesForWorker(ctx, "acme", "tweet-author"); err != nil || n != 1 {
		t.Fatalf("a running delivery must hold a slot: %d err=%v", n, err)
	}

	parked, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID,
		DeliveryStatusUpdate{Status: DeliveryAwaitingHuman})
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if parked.EndedAt != 0 {
		t.Fatalf("a paused delivery is not an ended one: %+v", parked)
	}
	if n, err := s.CountActiveDeliveries(ctx, "acme"); err != nil || n != 0 {
		t.Fatalf("a parked delivery must not hold a max_concurrent_jobs slot: %d err=%v", n, err)
	}
	if n, err := s.CountActiveDeliveriesForWorker(ctx, "acme", "tweet-author"); err != nil || n != 0 {
		t.Fatalf("a parked delivery must not hold a max_instances slot: %d err=%v", n, err)
	}
	// Nor is it queued work: it has already run, and re-dispatching it would
	// start a second session for the same event.
	pending, err := s.ListPendingDeliveries(ctx, "acme", "", 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("a parked delivery must not re-enter the pending queue: %+v err=%v", pending, err)
	}
}
