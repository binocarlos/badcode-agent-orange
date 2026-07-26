package main

// dispatch_attention_test.go — §8.4: a job that asked for a human PARKS.
//
// `request_human_attention` used to post the webhook, record the request, and
// then let the job run to completion like any other, so the delivery reached
// `ok` with an `ended_at` and `worker.finished` claimed nothing was pending.
// These cases pin the three facts that fix has to hold together: the delivery
// parks at `awaiting_human`, a failure still beats a pause, and a parked
// delivery does NOT sit on the worker's `max_instances` slot.

import (
	"context"
	"errors"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

var (
	errFakeNoProjectOrSession = errors.New("project and session id are required")
	errFakeNoSession          = errors.New("session not found")
	errFakeTurnBlewUp         = errors.New("the model provider returned 503")
)

// ── the fakes' half of `attention_requests` ─────────────────────────────────

// SessionAwaitsHuman is the fake's *agentdb.Store.SessionAwaitsHuman: has the
// session an open request? The fake keys on session id only — every test here
// is single-project, and the real query is project-scoped by SQL.
func (f *fakeDispatchStore) SessionAwaitsHuman(_ context.Context, project, sessionID string) (bool, error) {
	if project == "" || sessionID == "" {
		return false, errFakeNoProjectOrSession
	}
	return f.awaitingHuman[sessionID], nil
}

// requestAttention is what the tool does mid-turn: record an open request (and,
// as CreateAttentionRequest does transactionally, stamp the session).
func (f *fakeRouterStore) requestAttention(sessionID string) {
	f.awaitingHuman[sessionID] = true
	if sess := f.sessions[sessionID]; sess != nil {
		sess.AttentionRequested = true
	}
}

// SetSessionAttentionRequested completes agentkit.WorkerEventStore on the fake:
// the §8.2 emitter clears the per-turn stamp once it has copied it.
func (f *fakeRouterStore) SetSessionAttentionRequested(_ context.Context, sessionID string, requested bool) error {
	sess, ok := f.sessions[sessionID]
	if !ok {
		return errFakeNoSession
	}
	sess.AttentionRequested = requested
	return nil
}

// ── the defect ──────────────────────────────────────────────────────────────

// TestJobRequestingAttentionParksTheDelivery is the fix: a turn that called
// `request_human_attention` leaves the delivery at `awaiting_human`, and E1's
// UpdateDeliveryStatus deliberately treats that as a pause (no ended_at — the
// timestamp half is pinned in agentdb's TestDeliveriesLifecycleTimestamps).
func TestJobRequestingAttentionParksTheDelivery(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "tweet-author", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "tweet.due", Worker: "tweet-author", Enabled: true,
	})
	postEvent(t, store, "acme", "tweet.due", "time for a tweet", agentdb.EventEnvelope{})

	starter := &fakeJobStarter{store: store}
	// The worker drafts the tweet and asks for sign-off (§8.8.3 staged autonomy).
	starter.duringTurn = func(sessionID string) { store.requestAttention(sessionID) }
	rt, _ := newTestRouter(store, starter)
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(store.deliveries) != 1 {
		t.Fatalf("expected one delivery, got %d", len(store.deliveries))
	}
	d := store.deliveries[0]
	if d.Status != agentdb.DeliveryAwaitingHuman {
		t.Fatalf("status = %q, want %q — the job asked for a human, it is not finished",
			d.Status, agentdb.DeliveryAwaitingHuman)
	}
	if d.SessionID == "" {
		t.Fatalf("a parked delivery must still name its session — it IS the review surface")
	}
}

// TestJobNotRequestingAttentionStillCloses is the control: the pause must be
// reachable only by asking for it.
func TestJobNotRequestingAttentionStillCloses(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "tweet-author", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "tweet.due", Worker: "tweet-author", Enabled: true,
	})
	postEvent(t, store, "acme", "tweet.due", "time for a tweet", agentdb.EventEnvelope{})

	rt, _ := newTestRouter(store, &fakeJobStarter{store: store})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := store.deliveries[0].Status; got != agentdb.DeliveryOK {
		t.Fatalf("status = %q, want %q", got, agentdb.DeliveryOK)
	}
}

// TestFailureBeatsAwaitingHuman: a job that asked for a human and then errored
// is a FAILED job, not a paused one. `failed` is terminal and stamps ended_at,
// which is what stops a crashed job masquerading as one politely waiting.
func TestFailureBeatsAwaitingHuman(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "tweet-author", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "tweet.due", Worker: "tweet-author", Enabled: true,
	})
	postEvent(t, store, "acme", "tweet.due", "time for a tweet", agentdb.EventEnvelope{})

	starter := &fakeJobStarter{store: store, endErr: errFakeTurnBlewUp}
	starter.duringTurn = func(sessionID string) { store.requestAttention(sessionID) }
	rt, _ := newTestRouter(store, starter)
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := store.deliveries[0].Status; got != agentdb.DeliveryFailed {
		t.Fatalf("status = %q, want %q", got, agentdb.DeliveryFailed)
	}
}

// TestAwaitingHumanHoldsNoInstanceSlot is the deadlock question, and the single
// most likely way to turn this fix into an outage: a worker with
// `max_instances: 1` whose one delivery is parked awaiting a human must still
// be able to run again. It can, because §8.4's capacity counts are `status =
// running` only (agentdb.CountActiveDeliveriesForWorker) — a pause is not an
// occupied slot. The alternative (a parked job holding its instance) would mean
// a human who never replies retires the worker for ever, which no spec section
// asks for and §9 explicitly rejects the machinery for.
func TestAwaitingHumanHoldsNoInstanceSlot(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "tweet-author", 1) // max_instances: 1
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "tweet.due", Worker: "tweet-author", Enabled: true,
	})
	postEvent(t, store, "acme", "tweet.due", "first tweet", agentdb.EventEnvelope{})

	starter := &fakeJobStarter{store: store}
	starter.duringTurn = func(sessionID string) { store.requestAttention(sessionID) }
	rt, gate := newTestRouter(store, starter)
	ctx := context.Background()
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if got := store.deliveries[0].Status; got != agentdb.DeliveryAwaitingHuman {
		t.Fatalf("setup: first delivery is %q, want %q", got, agentdb.DeliveryAwaitingHuman)
	}

	// The parked delivery must not be counted as an instance in flight.
	n, err := store.CountActiveDeliveriesForWorker(ctx, "acme", "tweet-author")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("a parked delivery counts as %d active instances, want 0 — the worker would deadlock", n)
	}
	if full, err := gate.projectAtCapacity(ctx, "acme"); err != nil || full {
		t.Fatalf("a parked delivery must not hold a max_concurrent_jobs slot either (full=%v err=%v)", full, err)
	}

	// So the next event runs, rather than queueing behind a human who may never
	// answer.
	postEvent(t, store, "acme", "tweet.due", "second tweet", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(starter.jobs) != 2 {
		t.Fatalf("the worker started %d jobs, want 2 — a parked job must not block it", len(starter.jobs))
	}
	if got := store.deliveries[1].Status; got == agentdb.DeliveryPending {
		t.Fatalf("the second delivery queued behind the parked one")
	}
}
