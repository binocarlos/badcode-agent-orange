package agentdb

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The Postgres-only half of the event spine: migration 023's real DDL, the
// CHECK constraint that makes the delivery-status vocabulary a database
// invariant rather than a Go convention, the unique index behind the
// at-least-once guard, and jsonb envelope filtering — none of which sqlite can
// honestly answer. Skipped unless AGENTKIT_TEST_POSTGRES_URL is set.

// liveProject returns a per-run unique project id and registers cleanup of
// every row this test wrote under it.
func liveProject(t *testing.T, s *Store) string {
	t.Helper()
	project := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		ctx := context.Background()
		_ = s.DB().WithContext(ctx).Exec("DELETE FROM event_deliveries WHERE project = ?", project).Error
		_ = s.DB().WithContext(ctx).Exec("DELETE FROM subscriptions WHERE project = ?", project).Error
		_ = s.DB().WithContext(ctx).Exec("DELETE FROM project_events WHERE project = ?", project).Error
	})
	return project
}

func TestLivePG_EventsSchema023(t *testing.T) {
	s := openLivePG(t)

	// Every column the plan named must exist, with the removed ones absent.
	tests := []struct {
		table   string
		want    []string
		absent  []string
		comment string
	}{
		{
			table: "project_events",
			want:  []string{"id", "project", "type", "text", "envelope", "occurred_at", "created_at", "delivered"},
		},
		{
			table:  "subscriptions",
			want:   []string{"id", "project", "event_type", "filter", "worker", "max_firings_per_hour", "enabled", "created_at", "updated_at"},
			absent: []string{"concurrency"}, // removed 2026-07-25 (superseded by worker.max_instances)
		},
		{
			table: "event_deliveries",
			want:  []string{"id", "project", "event_id", "subscription_id", "session_id", "status", "started_at", "ended_at", "created_at", "updated_at"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			var cols []string
			if err := s.DB().Raw(
				"SELECT column_name FROM information_schema.columns WHERE table_name = ?", tc.table,
			).Scan(&cols).Error; err != nil {
				t.Fatalf("read columns: %v", err)
			}
			have := map[string]bool{}
			for _, c := range cols {
				have[c] = true
			}
			for _, c := range tc.want {
				if !have[c] {
					t.Fatalf("%s is missing column %q (have %v)", tc.table, c, cols)
				}
			}
			for _, c := range tc.absent {
				if have[c] {
					t.Fatalf("%s must NOT have column %q — it was explicitly removed from the plan", tc.table, c)
				}
			}
		})
	}
}

// TestLivePG_DeliveriesStatusCheckConstraint proves the six-value vocabulary is
// enforced by the database, not merely by the Go validator: a raw INSERT of
// `dropped` (the status the walkthrough removed) must be rejected.
func TestLivePG_DeliveriesStatusCheckConstraint(t *testing.T) {
	s := openLivePG(t)
	project := liveProject(t, s)
	ctx := context.Background()

	insert := func(status string) error {
		return s.DB().WithContext(ctx).Exec(`
			INSERT INTO event_deliveries (id, project, event_id, subscription_id, status)
			VALUES (?, ?, ?, ?, ?)`,
			uuid.New().String(), project, uuid.New().String(), uuid.New().String(), status).Error
	}

	for _, ok := range DeliveryStatuses {
		if err := insert(ok); err != nil {
			t.Fatalf("status %q must be accepted by the CHECK constraint: %v", ok, err)
		}
	}
	for _, bad := range []string{"dropped", "queued", "OK"} {
		if err := insert(bad); err == nil {
			t.Fatalf("status %q must be rejected by the CHECK constraint", bad)
		}
	}
}

// TestLivePG_DeliveriesUniquePair proves the (event_id, subscription_id) unique
// index exists: two routers racing on the same pair cannot both insert, and
// EnsureDelivery converts the loser's duplicate-key error into the stored row.
func TestLivePG_DeliveriesUniquePair(t *testing.T) {
	s := openLivePG(t)
	project := liveProject(t, s)
	ctx := context.Background()

	eventID, subID := uuid.New().String(), uuid.New().String()
	raw := func() error {
		return s.DB().WithContext(ctx).Exec(`
			INSERT INTO event_deliveries (id, project, event_id, subscription_id, status)
			VALUES (?, ?, ?, ?, 'pending')`,
			uuid.New().String(), project, eventID, subID).Error
	}
	if err := raw(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := raw(); err == nil {
		t.Fatalf("a second row for the same (event_id, subscription_id) must violate the unique index")
	}

	// EnsureDelivery, meeting the row it did not create, returns it.
	got, created, err := s.EnsureDelivery(ctx, &EventDelivery{
		Project: project, EventID: eventID, SubscriptionID: subID,
	})
	if err != nil || created {
		t.Fatalf("ensure over an existing pair: created=%v err=%v", created, err)
	}
	if got.EventID != eventID || got.SubscriptionID != subID {
		t.Fatalf("ensure returned the wrong row: %+v", got)
	}
}

// TestLivePG_EventEnvelopeJSONB proves the envelope really is jsonb — the
// router's envelope filter (§8.3) is an equality match on its fields, and it
// must be expressible as SQL against the stored column.
func TestLivePG_EventEnvelopeJSONB(t *testing.T) {
	s := openLivePG(t)
	project := liveProject(t, s)
	ctx := context.Background()

	mk := func(worker string, interactive bool) {
		if _, err := s.CreateProjectEvent(ctx, &ProjectEvent{
			Project: project, Type: "worker.finished", Text: "transcript",
			Envelope: EventEnvelope{
				Depth: 1, Source: EventSourceWorker, Worker: worker,
				SessionID: "sess-" + worker, Interactive: interactive,
			},
		}); err != nil {
			t.Fatalf("create (%s): %v", worker, err)
		}
	}
	mk("email-answerer", false)
	mk("email-answerer", true)
	mk("tweet-writer", false)

	var n int64
	if err := s.DB().WithContext(ctx).Model(&ProjectEvent{}).
		Where("project = ? AND envelope->>'worker' = ?", project, "email-answerer").
		Count(&n).Error; err != nil {
		t.Fatalf("envelope worker filter: %v", err)
	}
	if n != 2 {
		t.Fatalf("envelope->>'worker' filter: want 2, got %d", n)
	}

	// The chat-suppressing filter of §8.2 only works if `interactive` is
	// serialised even when false.
	if err := s.DB().WithContext(ctx).Model(&ProjectEvent{}).
		Where("project = ? AND envelope->>'worker' = ? AND envelope->>'interactive' = 'false'",
			project, "email-answerer").
		Count(&n).Error; err != nil {
		t.Fatalf("envelope interactive filter: %v", err)
	}
	if n != 1 {
		t.Fatalf("envelope->>'interactive'='false' filter: want 1, got %d", n)
	}

	// And the typed read path agrees with the SQL one.
	got, err := s.ListProjectEvents(ctx, ProjectEventQuery{Project: project})
	if err != nil || len(got) != 3 {
		t.Fatalf("list: got %d err=%v", len(got), err)
	}
	for _, ev := range got {
		if ev.Envelope.Source != EventSourceWorker || ev.Envelope.Depth != 1 {
			t.Fatalf("envelope did not round-trip through jsonb: %+v", ev.Envelope)
		}
	}
}

// TestLivePG_SubscriptionsRateLimitColumn covers the landscape-fold column on
// real Postgres, including the "0 = unlimited" default and the honest
// round-trip of a disabled subscription (the SQL default is TRUE).
func TestLivePG_SubscriptionsRateLimitColumn(t *testing.T) {
	s := openLivePG(t)
	project := liveProject(t, s)
	ctx := context.Background()

	sub, err := s.CreateSubscription(ctx, &Subscription{
		Project: project, EventType: "email.*", Worker: "answerer", Enabled: false,
	}, ConfigWrite{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sub.MaxFiringsPerHour != 0 {
		t.Fatalf("default cap must be 0 = unlimited, got %d", sub.MaxFiringsPerHour)
	}
	got, err := s.GetSubscription(ctx, project, sub.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Enabled {
		t.Fatalf("a subscription created disabled must stay disabled (the SQL default is TRUE)")
	}

	got.MaxFiringsPerHour = 6
	got.Enabled = true
	got.Filter = JSONMap{"worker": "answerer"}
	if _, err := s.UpdateSubscription(ctx, got, ConfigWrite{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	reread, err := s.GetSubscription(ctx, project, sub.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if reread.MaxFiringsPerHour != 6 || !reread.Enabled || reread.Filter["worker"] != "answerer" {
		t.Fatalf("round-trip: %+v", reread)
	}

	// A negative cap never reaches the column.
	reread.MaxFiringsPerHour = -1
	if _, err := s.UpdateSubscription(ctx, reread, ConfigWrite{}); err == nil ||
		!strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("negative cap must be refused, got %v", err)
	}
}
