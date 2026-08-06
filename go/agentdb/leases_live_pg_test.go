package agentdb

import (
	"context"
	"errors"
	"sync"
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
	// Releasing one REPORTS that there was nothing to release. The state we
	// wanted is already true, so this is not a failure — but it is not silence
	// either, and the difference is load-bearing: the reaper treats a
	// successful release as its exclusive claim on a dead session before it
	// emits `worker.failed{lost}`. When the release swallowed a missing row and
	// ignored RowsAffected, two reapers could both "claim" it and both emit.
	if err := s.ReleaseSessionLease(ctx, "no-such-session"); !errors.Is(err, ErrSessionLeaseNotHeld) {
		t.Fatalf("releasing an unknown session must report ErrSessionLeaseNotHeld, got %v", err)
	}
	// And the same for a session that exists but holds nothing: it was just
	// released above.
	if err := s.ReleaseSessionLease(ctx, sess.ID); !errors.Is(err, ErrSessionLeaseNotHeld) {
		t.Fatalf("a second release of the same lease must not also report success, got %v", err)
	}
}

// TestLivePG_ReleaseSessionLeaseHasExactlyOneWinner is the reaper's
// at-most-once justification, tested rather than asserted in a comment: when
// several sweeps race over one lapsed lease, exactly one may win the right to
// emit `worker.failed{reason:lost}`. A duplicated "your worker died" wakes
// every subscriber twice about a job that died once.
func TestLivePG_ReleaseSessionLeaseHasExactlyOneWinner(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "cust-"+uuid.New().String(), "u@x.com")
	if err := s.RenewSessionLease(ctx, sess.ID, 1_000); err != nil {
		t.Fatalf("take the lease: %v", err)
	}

	// Cap the pool. Every live test in this package opens a store and never
	// closes it, so the shared test Postgres is close to `max_connections`
	// already; a dozen simultaneous connections tips it into "sorry, too many
	// clients" and the race never happens. Four connections still race — the
	// contention that matters is on the ROW, not on the pool.
	if sqlDB, err := s.DB().DB(); err == nil {
		sqlDB.SetMaxOpenConns(4)
	}

	const racers = 12
	start := make(chan struct{})
	var mu sync.Mutex
	won, lost, other := 0, 0, []error{}
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := s.ReleaseSessionLease(ctx, sess.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrSessionLeaseNotHeld):
				lost++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if won != 1 || lost != racers-1 {
		t.Fatalf("exactly one caller may claim a lapsed lease — got %d winners and %d losers, "+
			"which means that many reapers would each emit worker.failed{lost}", won, lost)
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

	// The seeded rows are the REAL stored envelope shape — see
	// capturedQueryEventsRow in live_pg_test.go for where it was captured from.
	// Before TOK1 this test seeded an invented flat snake_case shape and passed
	// against a reader that summed 0 in production.
	// RD2 widened the seeded shape again: input is three separately-billed
	// components, so the rows below carry a cache write and a cache read as a
	// real cached turn does.
	writeUsage := func(sessionID, queryID string, uncached, cacheCreation, cacheRead, out int, createdAt int64) {
		t.Helper()
		if err := s.DB().WithContext(ctx).Exec(`
			INSERT INTO agent_query_events (id, session_id, query_id, events, search_text, created_at)
			VALUES (?, ?, ?, ?::jsonb, '', ?)`,
			uuid.New().String(), sessionID, queryID,
			capturedQueryEventsRow(uncached, cacheCreation, cacheRead, out), createdAt,
		).Error; err != nil {
			t.Fatalf("insert usage: %v", err)
		}
	}

	writeUsage(mine.ID, "q-old", 1000, 0, 0, 1000, 100)  // before the window
	writeUsage(mine.ID, "q-1", 100, 400, 0, 20, 5000)    // in: 520
	writeUsage(mine.ID, "q-2", 7, 0, 1500, 3, 6000)      // in: 1510
	writeUsage(theirs.ID, "q-x", 9999, 0, 0, 9999, 5500) // another project

	total, err := s.CountProjectTokensSince(ctx, customer, 1000)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2030 {
		t.Fatalf("want 2030 tokens in the window, got %d "+
			"(130 means the cache components are not being counted — the gate is under-reading spend)", total)
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
