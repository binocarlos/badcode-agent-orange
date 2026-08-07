package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ---------------------------------------------------------------------------
// RD7 — a job that wedges at `running` forever, eating two capacity slots.
//
// The wedge had one cause and one aggravator:
//
//   - `settle` released the session lease BEFORE stamping the delivery's
//     terminal status. `ListExpiredLeaseSessions` filters on
//     `lease_expires_at > 0`, so anything that went wrong in that window left a
//     `running` delivery the lease reaper could never see;
//   - nothing else swept deliveries by age, so nothing could see it either.
//
// These tests pin both halves: the ordering, and the sweep that catches what
// survives it.
// ---------------------------------------------------------------------------

// TestSettleStampsTheStatusBeforeReleasingTheLease is the ordering itself. The
// delivery write fails — the case whose error `settle` only logs — and the
// question is what state that leaves behind. Under the old order: `running`
// with no lease, invisible to everything, forever. Under the new one: the
// lease is still held, so the reaper collects it.
func TestSettleStampsTheStatusBeforeReleasingTheLease(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	ctx := context.Background()

	// The turn runs fine; closing the row is what fails.
	store.failDeliveryWriteFor = map[string]bool{agentdb.DeliveryOK: true}

	runner := &stubRunner{}
	starter := newRunnerSessionStarter(runner, store).withLeases(store)
	starter.now = store.now
	starter.logf = quietf
	starter.run = func(fn func()) { fn() }
	starter.newID = func() string { return "job-1" }

	rt, _ := newTestRouter(store, starter)
	postEvent(t, store, "acme", "email.received", "a customer wrote in", agentdb.EventEnvelope{Depth: 0})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	sess := store.session("job-1")
	if sess == nil {
		t.Fatalf("the session row must exist")
	}
	if sess.LeaseExpiresAt == agentdb.SessionLeaseUnset {
		t.Fatalf("a turn whose terminal status could not be written must STILL hold its lease — " +
			"releasing it first is what made the wedged delivery invisible to the reaper for ever (RD7)")
	}

	// And the promise that buys: the reaper really does find it.
	store.advance(sessionLeaseTTL + time.Minute)
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick after the lease lapsed: %v", err)
	}
	store.failDeliveryWriteFor = nil // the database recovers
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick after recovery: %v", err)
	}
	d := store.deliveryFor("job-1")
	if d == nil || d.Status == agentdb.DeliveryRunning {
		t.Fatalf("the wedged delivery must eventually be closed and its slot returned, got %+v", d)
	}
}

// TestStaleRunningDeliveriesAreSwept is the backstop for everything the
// ordering cannot save: agentd killed between the two writes, an old row from
// before the fix, a claim whose session never got created. A delivery running
// for an hour whose session holds no lease is not running.
func TestStaleRunningDeliveriesAreSwept(t *testing.T) {
	store := newFakeRouterStore()
	ctx := context.Background()

	// The wedge, constructed directly: a `running` delivery, an old
	// started_at, and a session with no lease (released, then nothing).
	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "wedged", Customer: "acme", WorkflowID: "agent", Status: "running",
		Worker: "answerer", LeaseExpiresAt: agentdb.SessionLeaseUnset,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	store.deliveries = append(store.deliveries, &agentdb.EventDelivery{
		ID: "del-wedged", Project: "acme", EventID: "ev-1", SubscriptionID: "sub-1",
		Worker: "answerer", SessionID: "wedged", Status: agentdb.DeliveryRunning,
		StartedAt: store.clock.Add(-2 * staleDeliveryAge).Unix(),
	})

	rt, _ := newTestRouter(store, &fakeJobStarter{store: store})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	d := store.deliveries[0]
	if d.Status != agentdb.DeliveryFailed {
		t.Fatalf("a delivery stuck at running for %s with no lease must be closed — until it is, it holds "+
			"a max_instances AND a max_concurrent_jobs slot for the life of the project (RD7). got %q",
			2*staleDeliveryAge, d.Status)
	}
	if d.FailureReason == "" {
		t.Fatalf("the closed row must say why it was closed, or the operator learns nothing")
	}
}

// TestStaleSweepLeavesLiveJobsAlone is the other half, and the more important
// one: a sweep that can close a running job is a worse defect than the one it
// fixes. Two shapes must survive it — a job still holding a lease (however
// long it has been running) and a young delivery.
func TestStaleSweepLeavesLiveJobsAlone(t *testing.T) {
	store := newFakeRouterStore()
	ctx := context.Background()

	// (a) A very long job that is demonstrably alive: it renews its lease.
	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "long", Customer: "acme", WorkflowID: "agent", Status: "running",
		Worker: "builder", LeaseExpiresAt: store.leaseDeadline(),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	store.deliveries = append(store.deliveries, &agentdb.EventDelivery{
		ID: "del-long", Project: "acme", EventID: "ev-1", SubscriptionID: "sub-1",
		Worker: "builder", SessionID: "long", Status: agentdb.DeliveryRunning,
		StartedAt: store.clock.Add(-8 * staleDeliveryAge).Unix(),
	})
	// (b) A job that started moments ago and holds no lease yet.
	store.deliveries = append(store.deliveries, &agentdb.EventDelivery{
		ID: "del-young", Project: "acme", EventID: "ev-2", SubscriptionID: "sub-2",
		Worker: "builder", SessionID: "", Status: agentdb.DeliveryRunning,
		StartedAt: store.clock.Add(-time.Second).Unix(),
	})

	rt, _ := newTestRouter(store, &fakeJobStarter{store: store})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	for _, d := range store.deliveries {
		if d.Status != agentdb.DeliveryRunning {
			t.Fatalf("delivery %s was swept and it is alive — a sweep that closes running jobs reports "+
				"failures that did not happen. got %q", d.ID, d.Status)
		}
	}
}

// TestSettleLeaseAlreadyReleasedIsNotAnError: with ReleaseSessionLease now a
// compare-and-set, a turn that settles AFTER the reaper already declared it
// lost gets ErrSessionLeaseNotHeld back. That is information, not a failure,
// and it must not stop the settle path doing its job.
func TestSettleLeaseAlreadyReleasedIsNotAnError(t *testing.T) {
	store := newFakeRouterStore()
	ctx := context.Background()
	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "gone", Customer: "acme", WorkflowID: "agent", Status: "running",
		Worker: "answerer", LeaseExpiresAt: agentdb.SessionLeaseUnset,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := store.ReleaseSessionLease(ctx, "gone"); !errors.Is(err, agentdb.ErrSessionLeaseNotHeld) {
		t.Fatalf("releasing a lease nobody holds must report it, got %v", err)
	}

	starter := newRunnerSessionStarter(&stubRunner{}, store).withLeases(store)
	starter.now = store.now
	starter.logf = quietf
	ended := false
	starter.settle(ctx, "gone", nil, func(context.Context, string, error) error { ended = true; return nil })
	if !ended {
		t.Fatalf("settle must still close the delivery when the lease was already released")
	}
}
