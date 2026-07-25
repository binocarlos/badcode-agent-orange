package agentdb

import (
	"context"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

// The Postgres-only half of E3's store surface (spec §8.4 steps 4 and 6): the
// lease sweep's partial-index predicate, and the daily token counter, whose
// jsonb `->>` + `::bigint` casts sqlite cannot answer at all. Skipped unless
// AGENTKIT_TEST_POSTGRES_URL is set.

func TestLivePG_RouterSweepIndexes028(t *testing.T) {
	s := openLivePG(t)
	for _, index := range []string{"idx_agent_sessions_lease", "idx_event_deliveries_project_status"} {
		var n int64
		if err := s.DB().Raw("SELECT count(*) FROM pg_indexes WHERE indexname = ?", index).Scan(&n).Error; err != nil {
			t.Fatalf("read pg_indexes: %v", err)
		}
		if n != 1 {
			t.Fatalf("migration 028 must create %s", index)
		}
	}
}

func TestLivePG_SessionLeaseLifecycle(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()
	sess := newLiveSession(t, s, customer, "u@x.com")

	// A fresh session holds no lease and is never in the reaper's view.
	expired, err := s.ListExpiredLeaseSessions(ctx, 1_000_000_000_000, 0)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if containsSession(expired, sess.ID) {
		t.Fatalf("a session with no lease must never be reaped — that is what keeps an interrupted, " +
			"resumable turn out of the reaper's hands")
	}

	// Take a lease in the past: now visible.
	if err := s.RenewSessionLease(ctx, sess.ID, 1000); err != nil {
		t.Fatalf("renew: %v", err)
	}
	expired, err = s.ListExpiredLeaseSessions(ctx, 2000, 0)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if !containsSession(expired, sess.ID) {
		t.Fatalf("a lapsed lease must be visible to the reaper")
	}

	// Renew it into the future: no longer expired.
	if err := s.RenewSessionLease(ctx, sess.ID, 9_000_000_000); err != nil {
		t.Fatalf("renew: %v", err)
	}
	expired, err = s.ListExpiredLeaseSessions(ctx, 2000, 0)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if containsSession(expired, sess.ID) {
		t.Fatalf("a renewed lease is not expired")
	}

	// Release: back to invisible whatever the clock says.
	if err := s.ReleaseSessionLease(ctx, sess.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LeaseExpiresAt != SessionLeaseUnset {
		t.Fatalf("release must zero the lease, got %d", got.LeaseExpiresAt)
	}
	expired, err = s.ListExpiredLeaseSessions(ctx, 9_999_999_999, 0)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if containsSession(expired, sess.ID) {
		t.Fatalf("a released lease is never reaped")
	}

	// Renewing a vanished session is an error, not a silent no-op: the caller
	// would otherwise believe it still holds a lease it does not.
	if err := s.RenewSessionLease(ctx, "no-such-session", 1234); err == nil {
		t.Fatalf("renewing an unknown session must fail")
	}
	// Releasing one is fine — the state we wanted is already true.
	if err := s.ReleaseSessionLease(ctx, "no-such-session"); err != nil {
		t.Fatalf("releasing an unknown session must not error: %v", err)
	}
}

// TestLivePG_RenewingALeaseDoesNotTouchUpdatedAt: the idle-archive loop reads
// `updated_at` to decide what is idle, so a per-minute lease renewal must not
// make every running session look freshly touched.
func TestLivePG_RenewingALeaseDoesNotTouchUpdatedAt(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "cust-"+uuid.New().String(), "u@x.com")

	before, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := s.RenewSessionLease(ctx, sess.ID, 5_000_000_000); err != nil {
		t.Fatalf("renew: %v", err)
	}
	after, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("a lease renewal must not bump updated_at (%d → %d)", before.UpdatedAt, after.UpdatedAt)
	}
	if after.LeaseExpiresAt != 5_000_000_000 {
		t.Fatalf("the lease did not stick: %d", after.LeaseExpiresAt)
	}
}

func TestLivePG_CountProjectTokensSince(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()
	other := "cust-" + uuid.New().String()
	mine := newLiveSession(t, s, customer, "u@x.com")
	theirs := newLiveSession(t, s, other, "u@x.com")

	writeUsage := func(sessionID, queryID string, in, out int64, createdAt int64) {
		t.Helper()
		if err := s.DB().WithContext(ctx).Exec(`
			INSERT INTO agent_query_events (id, session_id, query_id, events, search_text, created_at)
			VALUES (?, ?, ?, ?::jsonb, '', ?)`,
			uuid.New().String(), sessionID, queryID,
			`[{"input_tokens":`+itoa(in)+`,"output_tokens":`+itoa(out)+`}]`, createdAt,
		).Error; err != nil {
			t.Fatalf("insert usage: %v", err)
		}
	}

	writeUsage(mine.ID, "q-old", 1000, 1000, 100)  // before the window
	writeUsage(mine.ID, "q-1", 100, 20, 5000)      // in
	writeUsage(mine.ID, "q-2", 7, 3, 6000)         // in
	writeUsage(theirs.ID, "q-x", 9999, 9999, 5500) // another project

	total, err := s.CountProjectTokensSince(ctx, customer, 1000)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 130 {
		t.Fatalf("want 130 tokens in the window, got %d", total)
	}

	// Project isolation is absolute: another customer's spend is invisible.
	theirTotal, err := s.CountProjectTokensSince(ctx, other, 1000)
	if err != nil {
		t.Fatalf("count other: %v", err)
	}
	if theirTotal != 19998 {
		t.Fatalf("want 19998 for the other project, got %d", theirTotal)
	}

	// A project that has never spent anything reads 0, not an error — an
	// unmetered project must not fail the gate.
	empty, err := s.CountProjectTokensSince(ctx, "proj-"+uuid.New().String(), 0)
	if err != nil {
		t.Fatalf("count empty: %v", err)
	}
	if empty != 0 {
		t.Fatalf("want 0 for a project with no usage, got %d", empty)
	}
}

func TestLivePG_RouterDeliveryPollHelpers(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveProject(t, s)

	ev, err := s.CreateProjectEvent(ctx, &ProjectEvent{
		Project: project, Type: "email.received", Text: "hi",
		Envelope: EventEnvelope{Source: EventSourceExternal},
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	// One pending, one rate_limited.
	if _, _, err := s.EnsureDelivery(ctx, &EventDelivery{
		Project: project, EventID: ev.ID, SubscriptionID: "sub-a", Worker: "answerer", Status: DeliveryPending,
	}); err != nil {
		t.Fatalf("ensure pending: %v", err)
	}
	limited, _, err := s.EnsureDelivery(ctx, &EventDelivery{
		Project: project, EventID: ev.ID, SubscriptionID: "sub-b", Worker: "answerer", Status: DeliveryRateLimited,
	})
	if err != nil {
		t.Fatalf("ensure rate_limited: %v", err)
	}

	projects, err := s.ListProjectsWithPendingDeliveries(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if !containsString(projects, project) {
		t.Fatalf("a project with a pending delivery must be in the drain list")
	}

	n, err := s.CountRateLimitedDeliveriesSince(ctx, "sub-b", limited.CreatedAt)
	if err != nil {
		t.Fatalf("count rate limited: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 rate-limited delivery for sub-b, got %d", n)
	}
	// The pending one does not count as a refusal.
	if n, err := s.CountRateLimitedDeliveriesSince(ctx, "sub-a", 0); err != nil || n != 0 {
		t.Fatalf("sub-a has no refusals: n=%d err=%v", n, err)
	}
	// …but it does count as a firing (§8.3: every status except rate_limited).
	if n, err := s.CountSubscriptionFiringsSince(ctx, "sub-a", 0); err != nil || n != 1 {
		t.Fatalf("sub-a has one firing: n=%d err=%v", n, err)
	}
	if n, err := s.CountSubscriptionFiringsSince(ctx, "sub-b", 0); err != nil || n != 0 {
		t.Fatalf("a refused delivery must not consume a firing: n=%d err=%v", n, err)
	}

	// The reaper's reverse lookup: deliveries by session.
	if _, err := s.UpdateDeliveryStatus(ctx, project, limited.ID, DeliveryStatusUpdate{
		Status: DeliveryRunning, SessionID: "sess-42",
	}); err != nil {
		t.Fatalf("stamp session: %v", err)
	}
	found, err := s.ListDeliveries(ctx, DeliveryQuery{Project: project, SessionID: "sess-42", Status: DeliveryRunning})
	if err != nil {
		t.Fatalf("list by session: %v", err)
	}
	if len(found) != 1 || found[0].ID != limited.ID {
		t.Fatalf("SessionID filter must find exactly the running delivery: %+v", found)
	}
}

func containsSession(sessions []*Session, id string) bool {
	for _, s := range sessions {
		if s.ID == id {
			return true
		}
	}
	return false
}

func containsString(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
