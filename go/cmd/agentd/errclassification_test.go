package main

// errclassification_test.go — RD1 (docs/product/22-readiness.md §5).
//
// The defect these pin: a store read that fails for ANY reason was being read as
// "the row is gone", and that reading was then made DURABLE — a schedule
// disabled forever with a config-log reason naming a worker that exists, or a
// delivery marked `failed` after the router had already consumed its trigger.
//
// The shape of every test here is the same, and it is the house fault-injection
// shape (see agentdb/topology_apply.go's rollback tests): drive the real code
// over the store seam, make one read fail with an OPAQUE error — deliberately
// not one of agentdb's not-found sentinels, because that is what a database
// which is up but unhappy actually returns — and assert that nothing durable
// was written. Each transient case is paired with its genuinely-absent twin, so
// the fix cannot be "stop disabling", which would silently delete the §8.6
// behaviour the transient case is protecting.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// errDatabaseUnhappy is what fault injection returns: not wrapped around any
// sentinel, so `errors.Is(err, agentdb.ErrWorkerNotFound)` is false and the only
// way to treat it as "absent" is to not classify at all.
var errDatabaseUnhappy = errors.New("read tcp 10.0.0.5:5432: connection reset by peer")

// TestSchedulerDoesNotDisableOnTransientWorkerReadError is RD1's headline.
//
// The check runs BEFORE ClaimFiring, so it is upstream of the
// ScheduleMaxProvisionFailures streak: there is no five-strikes safeguard here,
// one blip during the due second was enough. A user's daily job stopped
// happening forever, and the config log told whoever investigated that the
// worker no longer existed.
func TestSchedulerDoesNotDisableOnTransientWorkerReadError(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "0 10 * * *", "tweet"))
	store.getWorkerErr = errDatabaseUnhappy

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("a per-schedule read failure must not fail the whole tick: %v", err)
	}

	if !store.schedules[sch.ID].Enabled {
		t.Fatalf("a transient read error disabled the schedule — the worker is right there")
	}
	if reason, ok := store.disabled[sch.ID]; ok {
		t.Fatalf("a durable config event was written for a database blip: %q", reason)
	}
	if store.schedules[sch.ID].ProvisionFailures != 0 {
		t.Fatalf("a database blip is not evidence about the schedule; the streak must not grow")
	}
	// The occurrence must survive too: the next tick is a retry, and a burnt
	// occurrence would make the retry a no-op.
	if len(store.firings) != 0 {
		t.Fatalf("an unread worker must not burn the occurrence: %v", store.firings)
	}
	if len(starter.jobs) != 0 || len(store.deliveries) != 0 {
		t.Fatalf("nothing may be started when the worker could not be read")
	}

	// …and the next tick, with the database well again, fires normally. This is
	// the whole point of not disabling: the schedule is still there to retry.
	store.getWorkerErr = nil
	s.lastMinute = "" // a new tick, same minute
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick after recovery: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("the schedule must fire once the database answers again, started %d", len(starter.jobs))
	}
}

// TestSchedulerStillDisablesForAGenuinelyMissingWorker is the twin: §8.6's
// behaviour must survive the fix. Without this, "never disable" would pass the
// test above and silently drop the rule that stops 53 abandoned `* * * * *` rows
// from pinning every host port.
func TestSchedulerStillDisablesForAGenuinelyMissingWorker(t *testing.T) {
	store := newFakeDispatchStore()
	sch := store.addSchedule(agentdb.NewSchedule("acme", "retired-worker", "0 10 * * *", "tweet"))
	// No worker seeded → the fake returns the real store's ErrWorkerNotFound.

	s, _ := newTestScheduler(t, store, &recordingStarter{}, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if store.schedules[sch.ID].Enabled {
		t.Fatalf("a schedule whose worker is genuinely gone must still be disabled (§8.6)")
	}
	if reason := store.disabled[sch.ID]; reason == "" {
		t.Fatalf("the disable must still record why")
	}
}

// TestDispatchQueuesRatherThanFailsOnTransientWorkerReadError — the sibling.
//
// `failed` is terminal and the router has already consumed the trigger by the
// time the gate runs, so a delivery failed here is a lost trigger with no path
// back to the user's work. The right shape is the one every other store read in
// DispatchWithReason already uses: leave it `pending`, return the error, let the
// drain retry.
func TestDispatchQueuesRatherThanFailsOnTransientWorkerReadError(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	starter := &recordingStarter{}
	gate := newDispatcher(dispatcherConfig{Store: store, Starter: starter, Logf: func(string, ...any) {}})

	ev, _ := store.CreateProjectEvent(context.Background(), &agentdb.ProjectEvent{
		Project: "acme", Type: agentdb.EventTypeScheduleFired, Text: "tweet",
		Envelope: agentdb.EventEnvelope{Source: agentdb.EventSourceSchedule},
	})
	d, _, _ := store.EnsureDelivery(context.Background(), &agentdb.EventDelivery{
		Project: "acme", EventID: ev.ID, SubscriptionID: "sub-1",
		Worker: "tweet-author", Status: agentdb.DeliveryPending,
	})

	store.getWorkerErr = errDatabaseUnhappy
	outcome, err := gate.Dispatch(context.Background(), d)
	if err == nil {
		t.Fatalf("a store failure must be reported to the caller, not swallowed")
	}
	if outcome == dispatchFailed {
		t.Fatalf("a database blip permanently failed a delivery whose trigger is already spent")
	}
	if d.Status != agentdb.DeliveryPending {
		t.Fatalf("the delivery must stay pending so the drain retries it, got %q", d.Status)
	}

	// The drain is the retry, and it works once the database does.
	store.getWorkerErr = nil
	started, err := gate.DrainPending(context.Background(), "acme")
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if started != 1 || len(starter.jobs) != 1 {
		t.Fatalf("the retained delivery must start on the next drain, started=%d jobs=%d", started, len(starter.jobs))
	}
}

// TestDispatchStillFailsForAGenuinelyMissingWorker — the twin, so "never fail"
// cannot pass.
func TestDispatchStillFailsForAGenuinelyMissingWorker(t *testing.T) {
	store := newFakeDispatchStore()
	gate := newDispatcher(dispatcherConfig{Store: store, Starter: &recordingStarter{}, Logf: func(string, ...any) {}})

	d, _, _ := store.EnsureDelivery(context.Background(), &agentdb.EventDelivery{
		Project: "acme", EventID: "", SubscriptionID: "sub-1",
		Worker: "retired-worker", Status: agentdb.DeliveryPending,
	})
	outcome, err := gate.Dispatch(context.Background(), d)
	if err != nil {
		t.Fatalf("a retired worker is a delivery outcome, not a gate error: %v", err)
	}
	if outcome != dispatchFailed || d.Status != agentdb.DeliveryFailed {
		t.Fatalf("want a failed delivery for a worker that is genuinely gone, got %s/%s", outcome, d.Status)
	}
}

// TestDispatchQueuesRatherThanFailsOnTransientEventReadError — the third site,
// found by surveying for the same shape. Sixty lines below the worker read, the
// triggering event was read with the same unclassified `if err != nil` and the
// same durable `failed`.
func TestDispatchQueuesRatherThanFailsOnTransientEventReadError(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	gate := newDispatcher(dispatcherConfig{Store: store, Starter: &recordingStarter{}, Logf: func(string, ...any) {}})

	ev, _ := store.CreateProjectEvent(context.Background(), &agentdb.ProjectEvent{
		Project: "acme", Type: agentdb.EventTypeScheduleFired, Text: "tweet",
		Envelope: agentdb.EventEnvelope{Source: agentdb.EventSourceSchedule},
	})
	d, _, _ := store.EnsureDelivery(context.Background(), &agentdb.EventDelivery{
		Project: "acme", EventID: ev.ID, SubscriptionID: "sub-1",
		Worker: "tweet-author", Status: agentdb.DeliveryPending,
	})

	store.getProjectEventErr = errDatabaseUnhappy
	outcome, err := gate.Dispatch(context.Background(), d)
	if err == nil {
		t.Fatalf("a store failure must be reported to the caller, not swallowed")
	}
	if outcome == dispatchFailed || d.Status != agentdb.DeliveryPending {
		t.Fatalf("an unreadable event permanently failed the delivery: outcome=%s status=%s", outcome, d.Status)
	}
}

// TestDispatchStillFailsForAGenuinelyMissingEvent — the twin.
func TestDispatchStillFailsForAGenuinelyMissingEvent(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	gate := newDispatcher(dispatcherConfig{Store: store, Starter: &recordingStarter{}, Logf: func(string, ...any) {}})

	d, _, _ := store.EnsureDelivery(context.Background(), &agentdb.EventDelivery{
		Project: "acme", EventID: "ev-that-never-existed", SubscriptionID: "sub-1",
		Worker: "tweet-author", Status: agentdb.DeliveryPending,
	})
	outcome, err := gate.Dispatch(context.Background(), d)
	if err != nil {
		t.Fatalf("an absent event is a delivery outcome, not a gate error: %v", err)
	}
	if outcome != dispatchFailed || d.Status != agentdb.DeliveryFailed {
		t.Fatalf("want a failed delivery for an event that is genuinely gone, got %s/%s", outcome, d.Status)
	}
}
