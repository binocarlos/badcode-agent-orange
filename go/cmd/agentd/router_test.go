package main

// router_test.go — E3's tests (spec §8.2–§8.5).
//
// Everything here is named TestRouter* on purpose: the work plan's validation
// command is `go test ./cmd/agentd/... -run 'TestRouter'`, so a case that does
// not carry the prefix is a case that never runs at the gate.
//
// The fakes extend H1's `fakeDispatchStore` rather than cloning it, so the
// router, the scheduler and the shared gate are all exercised over ONE
// in-memory implementation of the delivery rules. If they ever disagree, they
// disagree here first.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// ── Fakes ───────────────────────────────────────────────────────────────────

// fakeRouterStore is H1's dispatch fake plus everything the router, the lease
// reaper and the token budget read.
type fakeRouterStore struct {
	*fakeDispatchStore

	clock    time.Time
	order    []string // event ids, creation order — the poll's ordering
	subs     []*agentdb.Subscription
	sessions map[string]*agentdb.Session
	tokens   map[string]int64
	tokenErr error
}

func newFakeRouterStore() *fakeRouterStore {
	f := &fakeRouterStore{
		fakeDispatchStore: newFakeDispatchStore(),
		clock:             time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		sessions:          map[string]*agentdb.Session{},
		tokens:            map[string]int64{},
	}
	// A claim stamps started_at from the test clock, exactly as the real store
	// stamps it from the database clock — that is what the stale sweep ages.
	f.claimAt = func() int64 { return f.clock.Unix() }
	return f
}

// The fake must satisfy every seam the production wiring binds, or the tests
// would be exercising a different contract from the one main() uses.
var (
	_ routerStore          = (*fakeRouterStore)(nil)
	_ reaperStore          = (*fakeRouterStore)(nil)
	_ budgetStore          = (*fakeRouterStore)(nil)
	_ dispatchStore        = (*fakeRouterStore)(nil)
	_ leaseStore           = (*fakeRouterStore)(nil)
	_ schedulerStore       = (*fakeRouterStore)(nil)
	_ agentkit.RunnerStore = (*fakeRouterStore)(nil)
)

func (f *fakeRouterStore) now() time.Time                     { return f.clock }
func (f *fakeRouterStore) advance(d time.Duration)            { f.clock = f.clock.Add(d) }
func (f *fakeRouterStore) leaseDeadline() int64               { return f.clock.Add(sessionLeaseTTL).Unix() }
func (f *fakeRouterStore) session(id string) *agentdb.Session { return f.sessions[id] }

// ── project_events ──────────────────────────────────────────────────────────

func (f *fakeRouterStore) CreateProjectEvent(ctx context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error) {
	if ev.OccurredAt == 0 {
		ev.OccurredAt = f.clock.Unix()
	}
	ev.CreatedAt = f.clock.Unix()
	out, err := f.fakeDispatchStore.CreateProjectEvent(ctx, ev)
	if err != nil {
		return nil, err
	}
	f.order = append(f.order, out.ID)
	return out, nil
}

func (f *fakeRouterStore) ListUndeliveredProjectEvents(_ context.Context, limit int) ([]*agentdb.ProjectEvent, error) {
	out := []*agentdb.ProjectEvent{}
	for _, id := range f.order {
		if ev := f.events[id]; ev != nil && !ev.Delivered {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRouterStore) MarkProjectEventDelivered(_ context.Context, id string) error {
	ev, ok := f.events[id]
	if !ok {
		return fmt.Errorf("project event not found")
	}
	ev.Delivered = true
	return nil
}

// ── subscriptions ───────────────────────────────────────────────────────────

func (f *fakeRouterStore) addSubscription(sub *agentdb.Subscription) *agentdb.Subscription {
	if sub.ID == "" {
		sub.ID = f.nextID("sub")
	}
	f.subs = append(f.subs, sub)
	return sub
}

func (f *fakeRouterStore) ListEnabledSubscriptions(_ context.Context, project string) ([]*agentdb.Subscription, error) {
	out := []*agentdb.Subscription{}
	for _, s := range f.subs {
		if s.Project == project && s.Enabled {
			out = append(out, s)
		}
	}
	return out, nil
}

// ── event_deliveries ────────────────────────────────────────────────────────

// EnsureDelivery re-stamps created_at from the fake clock: the rate-limit window
// is measured in unix seconds, and the base fake's monotonic counter would make
// every delivery look like it happened in 1970.
func (f *fakeRouterStore) EnsureDelivery(ctx context.Context, d *agentdb.EventDelivery) (*agentdb.EventDelivery, bool, error) {
	out, created, err := f.fakeDispatchStore.EnsureDelivery(ctx, d)
	if err == nil && created {
		out.CreatedAt = f.clock.Unix()
	}
	return out, created, err
}

func (f *fakeRouterStore) CountSubscriptionFiringsSince(_ context.Context, subscriptionID string, since int64) (int64, error) {
	var n int64
	for _, d := range f.deliveries {
		if d.SubscriptionID == subscriptionID && d.CreatedAt >= since && d.Status != agentdb.DeliveryRateLimited {
			n++
		}
	}
	return n, nil
}

func (f *fakeRouterStore) CountRateLimitedDeliveriesSince(_ context.Context, subscriptionID string, since int64) (int64, error) {
	var n int64
	for _, d := range f.deliveries {
		if d.SubscriptionID == subscriptionID && d.CreatedAt >= since && d.Status == agentdb.DeliveryRateLimited {
			n++
		}
	}
	return n, nil
}

func (f *fakeRouterStore) ListProjectsWithPendingDeliveries(context.Context) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, d := range f.deliveries {
		if d.Status == agentdb.DeliveryPending && !seen[d.Project] {
			seen[d.Project] = true
			out = append(out, d.Project)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeRouterStore) ListDeliveries(_ context.Context, q agentdb.DeliveryQuery) ([]*agentdb.EventDelivery, error) {
	out := []*agentdb.EventDelivery{}
	for _, d := range f.deliveries {
		switch {
		case q.Project != "" && d.Project != q.Project,
			q.SessionID != "" && d.SessionID != q.SessionID,
			q.EventID != "" && d.EventID != q.EventID,
			q.SubscriptionID != "" && d.SubscriptionID != q.SubscriptionID,
			q.Status != "" && d.Status != q.Status:
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeRouterStore) deliveryFor(sessionID string) *agentdb.EventDelivery {
	for _, d := range f.deliveries {
		if d.SessionID == sessionID {
			return d
		}
	}
	return nil
}

// ── sessions + leases ───────────────────────────────────────────────────────

func (f *fakeRouterStore) GetSession(_ context.Context, id string) (*agentdb.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	return s, nil
}

func (f *fakeRouterStore) UpdateSession(_ context.Context, s *agentdb.Session) (*agentdb.Session, error) {
	f.sessions[s.ID] = s
	return s, nil
}

// SessionTriggerEvent mirrors the real walk exactly: the session's earliest
// delivery names the event that caused the job. This is the E2 mechanism the
// depth test exercises.
func (f *fakeRouterStore) SessionTriggerEvent(_ context.Context, sessionID string) (*agentdb.ProjectEvent, error) {
	var earliest *agentdb.EventDelivery
	for _, d := range f.deliveries {
		if d.SessionID != sessionID {
			continue
		}
		if earliest == nil || d.CreatedAt < earliest.CreatedAt {
			earliest = d
		}
	}
	if earliest == nil {
		return nil, nil
	}
	return f.events[earliest.EventID], nil
}

func (f *fakeRouterStore) ListExpiredLeaseSessions(_ context.Context, now int64, limit int) ([]*agentdb.Session, error) {
	out := []*agentdb.Session{}
	for _, s := range f.sessions {
		if s.LeaseExpiresAt > agentdb.SessionLeaseUnset && s.LeaseExpiresAt < now {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LeaseExpiresAt != out[j].LeaseExpiresAt {
			return out[i].LeaseExpiresAt < out[j].LeaseExpiresAt
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRouterStore) RenewSessionLease(_ context.Context, sessionID string, until int64) error {
	s, ok := f.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.LeaseExpiresAt = until
	return nil
}

// ReleaseSessionLease mirrors the real store's compare-and-set: only a caller
// that finds the lease HELD may release it, and everyone else is told so. The
// reaper's at-most-once claim rests on exactly this (agentdb/leases.go).
func (f *fakeRouterStore) ReleaseSessionLease(_ context.Context, sessionID string) error {
	s, ok := f.sessions[sessionID]
	if !ok || s.LeaseExpiresAt <= agentdb.SessionLeaseUnset {
		return agentdb.ErrSessionLeaseNotHeld
	}
	s.LeaseExpiresAt = agentdb.SessionLeaseUnset
	return nil
}

// ListStaleRunningDeliveries is the stale-delivery sweep's input (RD7).
func (f *fakeRouterStore) ListStaleRunningDeliveries(_ context.Context, startedBefore int64, limit int) ([]*agentdb.EventDelivery, error) {
	out := []*agentdb.EventDelivery{}
	for _, d := range f.deliveries {
		// Mirrors the real query exactly, including its refusal to touch a
		// running row with no started_at — a shape the sweep does not
		// understand must not be closed by it.
		if d.Status != agentdb.DeliveryRunning || d.StartedAt <= 0 || d.StartedAt >= startedBefore {
			continue
		}
		out = append(out, d)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ── budget ledger ───────────────────────────────────────────────────────────

func (f *fakeRouterStore) CountProjectTokensSince(_ context.Context, project string, _ int64) (int64, error) {
	if f.tokenErr != nil {
		return 0, f.tokenErr
	}
	return f.tokens[project], nil
}

// ── the rest of agentkit.RunnerStore (unused by these tests) ────────────────

func (f *fakeRouterStore) PersistQueryEventsFlat(context.Context, string, string, []events.Envelope, string) error {
	return nil
}
func (f *fakeRouterStore) ListQueryEventsFlat(context.Context, string) ([]events.Envelope, error) {
	return nil, nil
}
func (f *fakeRouterStore) GetSnapshotHandle(context.Context, string) (imageregistry.Handle, bool, error) {
	return imageregistry.Handle{}, false, nil
}
func (f *fakeRouterStore) SetSnapshotHandle(context.Context, string, imageregistry.Handle) error {
	return nil
}
func (f *fakeRouterStore) GetWorkerBinding(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakeRouterStore) SetWorkerBinding(context.Context, string, string) error { return nil }
func (f *fakeRouterStore) ClearWorkerBinding(context.Context, string) error       { return nil }

// ── the job starter fake ────────────────────────────────────────────────────

// fakeJobStarter reproduces runnerSessionStarter's ORDERING, which is the part
// that matters: the session row and its lease exist, the delivery is told the
// session id, and only then does the turn run. A starter that stamped the
// delivery afterwards would pass every capacity test and silently break depth.
type fakeJobStarter struct {
	store *fakeRouterStore
	n     int
	jobs  []startJobInput
	ids   []string
	err   error
	// hold leaves the job running (no OnSessionEnded), so capacity tests have
	// something actually occupying a slot.
	hold bool
	// endErr is what the settled turn reports — a turn that ran and then blew up,
	// as opposed to `err`, which is a job that never started.
	endErr error
	// duringTurn runs while the job is live — where a test emits the events a
	// real worker would emit.
	duringTurn func(sessionID string)
}

func (s *fakeJobStarter) StartJob(ctx context.Context, in startJobInput) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.n++
	id := fmt.Sprintf("sess-%d", s.n)
	s.jobs = append(s.jobs, in)
	s.ids = append(s.ids, id)

	s.store.sessions[id] = &agentdb.Session{
		ID:             id,
		Customer:       in.Project,
		WorkflowID:     "agent",
		Status:         "running",
		Worker:         in.Worker.Name,
		ComposedPrompt: in.Job.SystemPrompt,
		LeaseExpiresAt: s.store.leaseDeadline(),
	}
	if in.OnSessionCreated != nil {
		if err := in.OnSessionCreated(ctx, id); err != nil {
			return "", err
		}
	}
	if s.duringTurn != nil {
		s.duringTurn(id)
	}
	if s.hold {
		return id, nil
	}
	s.store.sessions[id].LeaseExpiresAt = agentdb.SessionLeaseUnset
	if in.OnSessionEnded != nil {
		_ = in.OnSessionEnded(ctx, id, s.endErr)
	}
	return id, nil
}

// ── wiring helpers ──────────────────────────────────────────────────────────

func quietf(string, ...any) {}

func newTestRouter(store *fakeRouterStore, starter sessionStarter, tweak ...func(*dispatcherConfig)) (*router, *dispatcher) {
	cfg := dispatcherConfig{
		Store:        store,
		Starter:      starter,
		DefaultImage: "agentkit-example:dev",
		Logf:         quietf,
	}
	for _, t := range tweak {
		t(&cfg)
	}
	gate := newDispatcher(cfg)
	rt := newRouter(routerConfig{
		Store:      store,
		Dispatcher: gate,
		Reaper:     &leaseReaper{store: store, batch: leaseReapBatch, now: store.now, logf: quietf},
		Now:        store.now,
		Logf:       quietf,
	})
	return rt, gate
}

func seedWorker(store *fakeRouterStore, project, name string, maxInstances int) *agentdb.Worker {
	w := agentdb.NewWorker(project, name)
	w.SystemPrompt = "you are " + name
	w.MaxInstances = maxInstances
	store.addWorker(w)
	return w
}

func postEvent(t *testing.T, store *fakeRouterStore, project, eventType, text string, env agentdb.EventEnvelope) *agentdb.ProjectEvent {
	t.Helper()
	if env.Source == "" {
		env.Source = agentdb.EventSourceExternal
	}
	ev, err := store.CreateProjectEvent(context.Background(), &agentdb.ProjectEvent{
		Project: project, Type: eventType, Text: text, Envelope: env,
	})
	if err != nil {
		t.Fatalf("post event: %v", err)
	}
	return ev
}

func statusesFor(store *fakeRouterStore, status string) []*agentdb.EventDelivery {
	out := []*agentdb.EventDelivery{}
	for _, d := range store.deliveries {
		if d.Status == status {
			out = append(out, d)
		}
	}
	return out
}

// ── Matching (§8.3) ─────────────────────────────────────────────────────────

func TestRouterSubscriptionMatching(t *testing.T) {
	base := agentdb.EventEnvelope{
		Depth: 1, Source: agentdb.EventSourceWorker, Worker: "email-answerer",
		SessionID: "s1", Interactive: false,
	}
	cases := []struct {
		name      string
		eventType string
		filter    agentdb.JSONMap
		event     *agentdb.ProjectEvent
		want      bool
	}{
		{"exact type matches", "email.received", nil,
			&agentdb.ProjectEvent{Type: "email.received", Envelope: base}, true},
		{"exact type does not prefix-match", "email", nil,
			&agentdb.ProjectEvent{Type: "email.received", Envelope: base}, false},
		{"trailing star is a prefix", "email.*", nil,
			&agentdb.ProjectEvent{Type: "email.received", Envelope: base}, true},
		{"trailing star does not match another namespace", "email.*", nil,
			&agentdb.ProjectEvent{Type: "worker.finished", Envelope: base}, false},
		{"envelope equality on worker", "worker.finished", agentdb.JSONMap{"worker": "email-answerer"},
			&agentdb.ProjectEvent{Type: "worker.finished", Envelope: base}, true},
		{"envelope equality rejects another worker", "worker.finished", agentdb.JSONMap{"worker": "archivist"},
			&agentdb.ProjectEvent{Type: "worker.finished", Envelope: base}, false},
		{"booleans compare as text", "worker.finished", agentdb.JSONMap{"interactive": false},
			&agentdb.ProjectEvent{Type: "worker.finished", Envelope: base}, true},
		{"stringified booleans compare the same", "worker.finished", agentdb.JSONMap{"interactive": "false"},
			&agentdb.ProjectEvent{Type: "worker.finished", Envelope: base}, true},
		{"numbers do not acquire float notation", "worker.finished", agentdb.JSONMap{"depth": 1},
			&agentdb.ProjectEvent{Type: "worker.finished", Envelope: base}, true},
		{"a filter on an absent field never matches", "worker.finished", agentdb.JSONMap{"reason": "lost"},
			&agentdb.ProjectEvent{Type: "worker.finished", Envelope: base}, false},
		// The envelope worker here is deliberately NOT "w" — every subscription
		// in this table is worker "w", and since self-delivery suppression
		// landed an event stamped "w" would be refused before the filter is
		// ever consulted, testing the guard instead of the `reason` field this
		// case exists for. (It said "w" until 2026-08-08 and started failing the
		// moment the guard arrived, which is the guard working.)
		{"reason matches on worker.failed", "worker.failed", agentdb.JSONMap{"reason": "lost"},
			&agentdb.ProjectEvent{Type: "worker.failed", Envelope: agentdb.EventEnvelope{
				Source: agentdb.EventSourceWorker, Worker: "lost-worker", SessionID: "s", Reason: agentdb.FailureReasonLost,
			}}, true},
		{"every filter key must match", "worker.finished",
			agentdb.JSONMap{"worker": "email-answerer", "interactive": true},
			&agentdb.ProjectEvent{Type: "worker.finished", Envelope: base}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := &agentdb.Subscription{EventType: tc.eventType, Filter: tc.filter, Worker: "w", Enabled: true}
			if got := subscriptionMatches(sub, tc.event); got != tc.want {
				t.Fatalf("match: want %v, got %v", tc.want, got)
			}
		})
	}
}

// TestSubscriptionMatchesSuppressesSelfDelivery pins the rule that a worker is
// never woken by its own completion.
//
// It lives here rather than in any one topology because that is the point of the
// change: `topology/supervisor.go` hand-rolled a validator refusing `worker.*`
// event types for exactly this reason, and `architect-archivist` carried a
// `filter interactive=true` for exactly this reason — two workarounds for a rule
// that belongs to the spine. Subscription filters are equality-only, so "every
// worker.finished EXCEPT my own" cannot be expressed as a filter by anyone.
//
// The external case is the one that would hurt if this were written carelessly.
// External events carry no worker and a subscription's worker is never empty, so
// a guard without the emptiness check would compare "" against every subscriber,
// match nothing, and silently kill the spine's main path.
func TestSubscriptionMatchesSuppressesSelfDelivery(t *testing.T) {
	finishedBy := func(worker string) *agentdb.ProjectEvent {
		return &agentdb.ProjectEvent{Type: "worker.finished", Envelope: agentdb.EventEnvelope{
			Depth: 1, Source: agentdb.EventSourceWorker, Worker: worker, SessionID: "s1",
		}}
	}
	external := &agentdb.ProjectEvent{Type: "email.received", Envelope: agentdb.EventEnvelope{
		Depth: 0, Source: agentdb.EventSourceExternal,
	}}

	cases := []struct {
		name      string
		subWorker string
		eventType string
		event     *agentdb.ProjectEvent
		want      bool
		why       string
	}{
		{
			name: "a worker is not woken by its own completion", subWorker: "archivist",
			eventType: "worker.finished", event: finishedBy("archivist"), want: false,
			why: "the archivist would archive its own archiving until the depth floor cut it off",
		},
		{
			name: "another worker's completion still wakes it", subWorker: "archivist",
			eventType: "worker.finished", event: finishedBy("email-answerer"), want: true,
			why: "suppressing self-delivery must not suppress the subscription's whole purpose",
		},
		{
			name: "an external event still wakes every subscriber", subWorker: "triage",
			eventType: "email.received", event: external, want: true,
			why: "external events carry no worker; comparing \"\" to the subscriber must not match",
		},
		{
			name: "the wildcard path is guarded too", subWorker: "archivist",
			eventType: "worker.*", event: finishedBy("archivist"), want: false,
			why: "a `worker.*` subscription is the easiest way to write the self-loop by accident",
		},
		{
			// The guard is on the ENVELOPE's worker, not on the event type, so it
			// covers worker.failed as well as worker.finished. That is wanted: a
			// worker woken by its own failure is a retry spin, and the retry it
			// implements would be the one nobody designed.
			name: "a worker is not woken by its own failure either", subWorker: "flaky",
			eventType: "worker.failed", event: &agentdb.ProjectEvent{
				Type: "worker.failed", Envelope: agentdb.EventEnvelope{
					Source: agentdb.EventSourceWorker, Worker: "flaky", SessionID: "s1",
					Reason: agentdb.FailureReasonLost,
				},
			}, want: false,
			why: "self-delivery of a failure is a retry loop nobody asked for",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := &agentdb.Subscription{EventType: tc.eventType, Worker: tc.subWorker, Enabled: true}
			if got := subscriptionMatches(sub, tc.event); got != tc.want {
				t.Fatalf("match = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// ── The happy path ──────────────────────────────────────────────────────────

// TestRouterRoutesMatchingEventIntoAJob is §8.4 steps 1–2 end to end: an event
// lands, the matching subscription becomes a delivery, and the delivery becomes
// a composed job whose first message is the event text.
func TestRouterRoutesMatchingEventIntoAJob(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "email-answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.*", Worker: "email-answerer", Enabled: true,
	})
	ev := postEvent(t, store, "acme", "email.received", "From: a customer", agentdb.EventEnvelope{})
	// A subscription in another project must not see it.
	store.addSubscription(&agentdb.Subscription{
		Project: "other", EventType: "email.*", Worker: "email-answerer", Enabled: true,
	})
	// A disabled one must not either.
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.*", Worker: "email-answerer", Enabled: false,
	})

	starter := &fakeJobStarter{store: store}
	rt, _ := newTestRouter(store, starter)
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(starter.jobs) != 1 {
		t.Fatalf("expected exactly one job, got %d", len(starter.jobs))
	}
	job := starter.jobs[0]
	if job.Project != "acme" || job.Worker.Name != "email-answerer" {
		t.Fatalf("job identity: %+v", job)
	}
	if !strings.Contains(job.Job.FirstMessage, "From: a customer") {
		t.Fatalf("the event text must be the job's first message, got:\n%s", job.Job.FirstMessage)
	}
	if len(store.deliveries) != 1 {
		t.Fatalf("expected one delivery, got %d", len(store.deliveries))
	}
	d := store.deliveries[0]
	if d.Worker != "email-answerer" {
		t.Fatalf("the delivery must carry the worker for the gate to count on: %+v", d)
	}
	if d.SessionID == "" {
		t.Fatalf("the delivery must be stamped with its session id — depth depends on it")
	}
	if d.Status != agentdb.DeliveryOK {
		t.Fatalf("a settled job's delivery must be terminal, got %q", d.Status)
	}
	if !store.events[ev.ID].Delivered {
		t.Fatalf("the event must be marked delivered once every match has a row")
	}
}

// TestRouterIsIdempotentAcrossPolls is the at-least-once guarantee (§8.4 step
// 2): re-polling an event that was already routed must produce no second job.
func TestRouterIsIdempotentAcrossPolls(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 4)
	sub := store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	ev := postEvent(t, store, "acme", "email.received", "hello", agentdb.EventEnvelope{})

	starter := &fakeJobStarter{store: store}
	rt, _ := newTestRouter(store, starter)
	ctx := context.Background()
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}

	// Simulate a crash between creating the delivery and marking the event
	// delivered: the watermark is lost, the delivery is not.
	store.events[ev.ID].Delivered = false
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if len(store.deliveries) != 1 {
		t.Fatalf("EnsureDelivery must keep (event, subscription) unique: %d rows", len(store.deliveries))
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("a replayed event must not start a second job: %d jobs", len(starter.jobs))
	}
	if store.deliveries[0].SubscriptionID != sub.ID {
		t.Fatalf("delivery is not keyed on the subscription: %+v", store.deliveries[0])
	}
}

// ── Depth: the §8.4 loop floor ──────────────────────────────────────────────

// TestRouterDepthIncrementsAcrossAWorkerChain is the E2 constraint made
// executable. A human's event is depth 0; the job it starts emits at depth 1;
// the job THAT starts emits at depth 2. All of it hangs off the delivery's
// session_id, which is why this test would fail the moment the router stopped
// stamping it (depth would pin to 0 for ever and the loop floor would be dead
// code).
func TestRouterDepthIncrementsAcrossAWorkerChain(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 4)
	seedWorker(store, "acme", "reviewer", 4)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: agentdb.EventTypeWorkerFinished,
		Filter: agentdb.JSONMap{"worker": "answerer"}, Worker: "reviewer", Enabled: true,
	})

	ctx := context.Background()
	starter := &fakeJobStarter{store: store}
	// While each job runs, emit what the Runner would emit for it — through the
	// SAME emitter, so this test pins the production envelope, not a copy.
	starter.duringTurn = func(sessionID string) {
		job, ok, err := agentkit.ResolveWorkerJob(ctx, store, sessionID)
		if err != nil || !ok {
			t.Fatalf("resolve worker job %s: ok=%v err=%v", sessionID, ok, err)
		}
		if _, err := agentkit.EmitWorkerFinished(ctx, store, job, "a transcript", false); err != nil {
			t.Fatalf("emit worker.finished: %v", err)
		}
	}
	rt, _ := newTestRouter(store, starter)

	postEvent(t, store, "acme", "email.received", "a customer wrote in", agentdb.EventEnvelope{Depth: 0})
	// Poll twice: the first routes the human's event, the second routes the
	// worker.finished the first job emitted.
	for i := 0; i < 2; i++ {
		store.advance(time.Second)
		if err := rt.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	if len(starter.jobs) != 2 {
		t.Fatalf("expected the chain answerer → reviewer (2 jobs), got %d", len(starter.jobs))
	}
	depths := map[string]int{}
	for _, id := range store.order {
		ev := store.events[id]
		if ev.Type == agentdb.EventTypeWorkerFinished {
			depths[ev.Envelope.Worker] = ev.Envelope.Depth
		}
	}
	if depths["answerer"] != 1 {
		t.Fatalf("the answerer's worker.finished must sit at depth 1 (external 0 + 1), got %d", depths["answerer"])
	}
	if depths["reviewer"] != 2 {
		t.Fatalf("the reviewer's worker.finished must sit at depth 2 — if this reads 0 the delivery "+
			"lost its session_id and the §8.4 loop floor is dead. got %d", depths["reviewer"])
	}
}

// TestRouterRefusesEventsPastTheDepthFloor pins §8.4 step 3: depth > 8 starts
// nothing, and the event is retired rather than re-examined for ever.
func TestRouterRefusesEventsPastTheDepthFloor(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 4)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "loop.tick", Worker: "answerer", Enabled: true,
	})
	// Emitted by "upstream", not by "answerer". Both events said "answerer" until
	// 2026-08-08 — the same name as the subscriber — so once the router began
	// suppressing self-delivery this test measured that guard instead of the
	// depth floor and reported zero jobs. The depth floor and the self-delivery
	// guard are independent rules and each needs a fixture that isolates it.
	atFloor := postEvent(t, store, "acme", "loop.tick", "still fine", agentdb.EventEnvelope{
		Depth: maxEventDepth, Source: agentdb.EventSourceWorker, Worker: "upstream", SessionID: "s",
	})
	pastFloor := postEvent(t, store, "acme", "loop.tick", "runaway", agentdb.EventEnvelope{
		Depth: maxEventDepth + 1, Source: agentdb.EventSourceWorker, Worker: "upstream", SessionID: "s",
	})

	starter := &fakeJobStarter{store: store}
	rt, _ := newTestRouter(store, starter)
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(starter.jobs) != 1 {
		t.Fatalf("depth %d must run and depth %d must not: got %d jobs",
			maxEventDepth, maxEventDepth+1, len(starter.jobs))
	}
	if len(store.deliveries) != 1 || store.deliveries[0].EventID != atFloor.ID {
		t.Fatalf("only the at-floor event may produce a delivery: %+v", store.deliveries)
	}
	if !store.events[pastFloor.ID].Delivered {
		t.Fatalf("a refused event must be retired, not re-polled every three seconds")
	}
}

// ── Capacity: the three required cases ──────────────────────────────────────

// TestRouterAtCapacityDeliveryStaysPending — required case 1.
func TestRouterAtCapacityDeliveryStaysPending(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 1) // max_instances = 1
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})

	starter := &fakeJobStarter{store: store, hold: true} // jobs never finish
	rt, _ := newTestRouter(store, starter)
	ctx := context.Background()

	postEvent(t, store, "acme", "email.received", "first", agentdb.EventEnvelope{})
	store.advance(time.Second)
	postEvent(t, store, "acme", "email.received", "second", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(starter.jobs) != 1 {
		t.Fatalf("a worker at max_instances=1 must run exactly one job, got %d", len(starter.jobs))
	}
	if got := len(statusesFor(store, agentdb.DeliveryRunning)); got != 1 {
		t.Fatalf("expected 1 running delivery, got %d", got)
	}
	pending := statusesFor(store, agentdb.DeliveryPending)
	if len(pending) != 1 {
		t.Fatalf("the excess delivery must STAY pending (never dropped), got %d pending", len(pending))
	}
	if pending[0].SessionID != "" {
		t.Fatalf("a queued delivery has no session yet: %+v", pending[0])
	}
}

// TestRouterFIFOOrderOnRelease — required case 2. Queued deliveries are
// dispatched oldest-first as instances free, one per release.
func TestRouterFIFOOrderOnRelease(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})

	starter := &fakeJobStarter{store: store, hold: true}
	rt, _ := newTestRouter(store, starter)
	ctx := context.Background()

	texts := []string{"first", "second", "third"}
	for _, text := range texts {
		postEvent(t, store, "acme", "email.received", text, agentdb.EventEnvelope{})
		store.advance(time.Second)
	}
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("expected 1 running, 2 queued; got %d jobs", len(starter.jobs))
	}

	// Free the instance and poll again, twice — each release must admit exactly
	// the OLDEST queued delivery.
	for i := 1; i < len(texts); i++ {
		running := statusesFor(store, agentdb.DeliveryRunning)
		if len(running) != 1 {
			t.Fatalf("round %d: expected 1 running, got %d", i, len(running))
		}
		running[0].Status = agentdb.DeliveryOK
		store.advance(time.Second)
		if err := rt.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if len(starter.jobs) != i+1 {
			t.Fatalf("round %d: expected %d jobs started, got %d", i, i+1, len(starter.jobs))
		}
	}

	var order []string
	for _, job := range starter.jobs {
		order = append(order, job.Event.Text)
	}
	want := strings.Join(texts, ",")
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("queued deliveries must dispatch FIFO.\nwant %s\ngot  %s", want, got)
	}
}

// TestRouterMaxInstancesRespectedForScheduleAndEventAlike — required case 3.
// The gate must behave identically whichever loop presents the delivery, in
// BOTH orders: a schedule firing must queue behind a routed event, and a routed
// event must queue behind a schedule firing.
func TestRouterMaxInstancesRespectedForScheduleAndEventAlike(t *testing.T) {
	newRig := func() (*fakeRouterStore, *fakeJobStarter, *router, *scheduler) {
		store := newFakeRouterStore()
		seedWorker(store, "acme", "tweet-author", 1)
		store.addSubscription(&agentdb.Subscription{
			Project: "acme", EventType: "email.received", Worker: "tweet-author", Enabled: true,
		})
		store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "0 12 * * *", "write the morning tweet"))
		starter := &fakeJobStarter{store: store, hold: true}
		rt, gate := newTestRouter(store, starter)
		sched := newScheduler(schedulerConfig{
			Store: store, Dispatcher: gate, Location: time.UTC, Now: store.now, Logf: quietf,
		})
		return store, starter, rt, sched
	}
	ctx := context.Background()

	assertOneRunningOnePending := func(t *testing.T, store *fakeRouterStore, starter *fakeJobStarter) {
		t.Helper()
		if len(starter.jobs) != 1 {
			t.Fatalf("max_instances=1 must admit exactly one job whatever fired it, got %d", len(starter.jobs))
		}
		if got := len(statusesFor(store, agentdb.DeliveryRunning)); got != 1 {
			t.Fatalf("expected 1 running delivery, got %d", got)
		}
		queued := statusesFor(store, agentdb.DeliveryPending)
		if len(queued) != 1 {
			t.Fatalf("the loser must queue as pending, got %d pending", len(queued))
		}
		if queued[0].Worker != "tweet-author" {
			t.Fatalf("a queued delivery must carry the worker the gate counts on: %+v", queued[0])
		}
	}

	t.Run("event first, schedule queues", func(t *testing.T) {
		store, starter, rt, sched := newRig()
		postEvent(t, store, "acme", "email.received", "a customer wrote in", agentdb.EventEnvelope{})
		if err := rt.Tick(ctx); err != nil {
			t.Fatalf("router tick: %v", err)
		}
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("scheduler tick: %v", err)
		}
		assertOneRunningOnePending(t, store, starter)
		queued := statusesFor(store, agentdb.DeliveryPending)[0]
		if queued.ScheduleID == "" {
			t.Fatalf("the queued row should be the schedule firing: %+v", queued)
		}
	})

	t.Run("schedule first, event queues", func(t *testing.T) {
		store, starter, rt, sched := newRig()
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("scheduler tick: %v", err)
		}
		postEvent(t, store, "acme", "email.received", "a customer wrote in", agentdb.EventEnvelope{})
		if err := rt.Tick(ctx); err != nil {
			t.Fatalf("router tick: %v", err)
		}
		assertOneRunningOnePending(t, store, starter)
		queued := statusesFor(store, agentdb.DeliveryPending)[0]
		if queued.ScheduleID != "" {
			t.Fatalf("the queued row should be the routed event: %+v", queued)
		}
	})
}

// TestRouterProjectConcurrencyCapQueuesAcrossWorkers pins §8.4 step 3: the
// per-project cap is orthogonal to max_instances and blocks even a completely
// idle worker.
func TestRouterProjectConcurrencyCapQueuesAcrossWorkers(t *testing.T) {
	store := newFakeRouterStore()
	settings := agentdb.DefaultProjectSettings("acme")
	settings.MaxConcurrentJobs = 1
	store.settings["acme"] = settings
	seedWorker(store, "acme", "answerer", 5)
	seedWorker(store, "acme", "archivist", 5)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "archivist", Enabled: true,
	})

	starter := &fakeJobStarter{store: store, hold: true}
	rt, _ := newTestRouter(store, starter)
	postEvent(t, store, "acme", "email.received", "one email, two subscribers", agentdb.EventEnvelope{})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(starter.jobs) != 1 {
		t.Fatalf("max_concurrent_jobs=1 must admit one job across all workers, got %d", len(starter.jobs))
	}
	if got := len(statusesFor(store, agentdb.DeliveryPending)); got != 1 {
		t.Fatalf("the second subscriber's delivery must queue, got %d pending", got)
	}
}

// ── Rate limiting (§8.3, §8.2) ──────────────────────────────────────────────

func TestRouterRateLimitsSubscriptionAndAnnouncesItOnce(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 5)
	sub := store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer",
		MaxFiringsPerHour: 2, Enabled: true,
	})

	starter := &fakeJobStarter{store: store}
	rt, _ := newTestRouter(store, starter)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		postEvent(t, store, "acme", "email.received", fmt.Sprintf("email %d", i), agentdb.EventEnvelope{})
		store.advance(time.Minute)
		if err := rt.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	if len(starter.jobs) != 2 {
		t.Fatalf("max_firings_per_hour=2 must start exactly two jobs, got %d", len(starter.jobs))
	}
	limited := statusesFor(store, agentdb.DeliveryRateLimited)
	if len(limited) != 3 {
		t.Fatalf("the other three must be recorded rate_limited, got %d", len(limited))
	}
	for _, d := range limited {
		if d.SubscriptionID != sub.ID {
			t.Fatalf("a rate-limited row must still name its subscription: %+v", d)
		}
	}

	var throttled []*agentdb.ProjectEvent
	for _, id := range store.order {
		if ev := store.events[id]; ev.Type == agentdb.EventTypeSubscriptionThrottled {
			throttled = append(throttled, ev)
		}
	}
	if len(throttled) != 1 {
		t.Fatalf("§8.2 allows at most one %s per subscription per rolling hour, got %d",
			agentdb.EventTypeSubscriptionThrottled, len(throttled))
	}
	env := throttled[0].Envelope
	if env.Source != agentdb.EventSourceCore || env.Depth != 0 {
		t.Fatalf("throttled envelope must be core's {source:core, depth:0}: %+v", env)
	}
	if env.Worker != "" || env.SessionID != "" {
		t.Fatalf("§8.2: the throttled envelope carries neither worker nor session_id: %+v", env)
	}
	if !strings.Contains(throttled[0].Text, sub.ID) {
		t.Fatalf("the throttle notice must name the subscription:\n%s", throttled[0].Text)
	}
}

func TestRouterRateLimitWindowRollsOver(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 5)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer",
		MaxFiringsPerHour: 1, Enabled: true,
	})
	starter := &fakeJobStarter{store: store}
	rt, _ := newTestRouter(store, starter)
	ctx := context.Background()

	postEvent(t, store, "acme", "email.received", "first", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	// Same hour: refused.
	store.advance(30 * time.Minute)
	postEvent(t, store, "acme", "email.received", "second", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("the second event is inside the window and must be refused, got %d jobs", len(starter.jobs))
	}
	// The window has rolled past the first firing: allowed again.
	store.advance(2 * time.Hour)
	postEvent(t, store, "acme", "email.received", "third", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if len(starter.jobs) != 2 {
		t.Fatalf("once the rolling hour has passed the subscription fires again, got %d jobs", len(starter.jobs))
	}
}

// ── The daily token budget (§8.4 step 6, §5) ────────────────────────────────

func TestRouterHardTokenBudgetQueuesJobsUntilMidnight(t *testing.T) {
	store := newFakeRouterStore()
	settings := agentdb.DefaultProjectSettings("acme")
	settings.DailyTokensHard = 1000
	store.settings["acme"] = settings
	store.tokens["acme"] = 1500 // already over
	seedWorker(store, "acme", "answerer", 5)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})

	starter := &fakeJobStarter{store: store}
	budget := newTokenBudget(tokenBudgetConfig{Store: store, Location: time.UTC, Now: store.now, Logf: quietf})
	rt, _ := newTestRouter(store, starter, func(c *dispatcherConfig) { c.Budget = budget })
	ctx := context.Background()

	postEvent(t, store, "acme", "email.received", "over budget", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 0 {
		t.Fatalf("a hard-budget-stopped project creates no non-interactive jobs, got %d", len(starter.jobs))
	}
	if got := len(statusesFor(store, agentdb.DeliveryPending)); got != 1 {
		t.Fatalf("the delivery must QUEUE (delivered after midnight), not be dropped: %d pending", got)
	}

	// Midnight resets the counter; the queued delivery goes out on the next drain.
	store.tokens["acme"] = 0
	store.advance(13 * time.Hour)
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick after reset: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("the queued delivery must go out once the budget resets, got %d jobs", len(starter.jobs))
	}
}

func TestRouterSoftTokenBudgetNotifiesOncePerDay(t *testing.T) {
	store := newFakeRouterStore()
	settings := agentdb.DefaultProjectSettings("acme")
	settings.DailyTokensSoft = 100
	store.settings["acme"] = settings
	store.tokens["acme"] = 500
	seedWorker(store, "acme", "answerer", 5)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})

	var notices int
	starter := &fakeJobStarter{store: store}
	budget := newTokenBudget(tokenBudgetConfig{
		Store: store, Location: time.UTC, Now: store.now, Logf: quietf,
		Notify: func(context.Context, string, *agentdb.ProjectSettings, int64) { notices++ },
	})
	rt, _ := newTestRouter(store, starter, func(c *dispatcherConfig) { c.Budget = budget })
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		postEvent(t, store, "acme", "email.received", fmt.Sprintf("email %d", i), agentdb.EventEnvelope{})
		store.advance(time.Minute)
		if err := rt.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(starter.jobs) != 3 {
		t.Fatalf("the soft tier stops nothing — expected 3 jobs, got %d", len(starter.jobs))
	}
	if notices != 1 {
		t.Fatalf("§5: exactly one soft-budget notification per day, got %d", notices)
	}

	// A new day re-arms it.
	store.advance(24 * time.Hour)
	postEvent(t, store, "acme", "email.received", "tomorrow", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick tomorrow: %v", err)
	}
	if notices != 2 {
		t.Fatalf("a new day gets its own notification, got %d", notices)
	}
}

func TestRouterUnreadableBudgetDoesNotStopTheProject(t *testing.T) {
	store := newFakeRouterStore()
	settings := agentdb.DefaultProjectSettings("acme")
	settings.DailyTokensHard = 10
	store.settings["acme"] = settings
	store.tokenErr = fmt.Errorf("boom")
	seedWorker(store, "acme", "answerer", 5)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})

	starter := &fakeJobStarter{store: store}
	budget := newTokenBudget(tokenBudgetConfig{Store: store, Location: time.UTC, Now: store.now, Logf: quietf})
	rt, _ := newTestRouter(store, starter, func(c *dispatcherConfig) { c.Budget = budget })
	postEvent(t, store, "acme", "email.received", "hello", agentdb.EventEnvelope{})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("a budget that cannot be evaluated must not silently stop a project's workforce")
	}
}

// TestRouterInteractiveJobsBypassTheCaps pins §8.4 step 5 the way it is actually
// implemented: interactive chat never becomes a delivery, so it is not visible
// to any of the gate's counts and cannot be queued behind background load — even
// with the project both at its concurrency cap and hard-budget-stopped.
func TestRouterInteractiveJobsBypassTheCaps(t *testing.T) {
	store := newFakeRouterStore()
	settings := agentdb.DefaultProjectSettings("acme")
	settings.MaxConcurrentJobs = 1
	settings.DailyTokensHard = 1
	store.settings["acme"] = settings
	store.tokens["acme"] = 10_000
	seedWorker(store, "acme", "answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})

	starter := &fakeJobStarter{store: store, hold: true}
	budget := newTokenBudget(tokenBudgetConfig{Store: store, Location: time.UTC, Now: store.now, Logf: quietf})
	rt, _ := newTestRouter(store, starter, func(c *dispatcherConfig) { c.Budget = budget })
	ctx := context.Background()

	postEvent(t, store, "acme", "email.received", "background work", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 0 {
		t.Fatalf("the background job must be stopped by the hard budget, got %d", len(starter.jobs))
	}

	// A human opens a chat with the same worker, the way httpapi does it:
	// straight to a session row, with no delivery anywhere.
	chat := &agentdb.Session{ID: "chat-1", Customer: "acme", WorkflowID: "agent", Status: "running", Worker: "answerer"}
	if _, err := store.UpdateSession(ctx, chat); err != nil {
		t.Fatalf("create chat session: %v", err)
	}
	if d := store.deliveryFor("chat-1"); d != nil {
		t.Fatalf("an interactive session must produce no delivery row: %+v", d)
	}
	active, err := store.CountActiveDeliveriesForWorker(ctx, "acme", "answerer")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if active != 0 {
		t.Fatalf("interactive chat must be invisible to the max_instances count, got %d", active)
	}
	job, ok, err := agentkit.ResolveWorkerJob(ctx, store, "chat-1")
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if !job.Interactive || job.Depth != 0 {
		t.Fatalf("a session with no delivery is interactive at depth 0: %+v", job)
	}
}

// ── The lease reaper (§8.4 step 4) ──────────────────────────────────────────

func TestRouterLeaseReaperDeclaresLostJobsFailed(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	starter := &fakeJobStarter{store: store, hold: true}
	rt, _ := newTestRouter(store, starter)
	ctx := context.Background()

	postEvent(t, store, "acme", "email.received", "a customer wrote in", agentdb.EventEnvelope{Depth: 0})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	sessionID := starter.ids[0]
	if store.session(sessionID).LeaseExpiresAt == agentdb.SessionLeaseUnset {
		t.Fatalf("a started job must hold a lease")
	}

	// The container dies without ever reporting back.
	store.advance(sessionLeaseTTL + time.Minute)
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick after the lease lapsed: %v", err)
	}

	sess := store.session(sessionID)
	if sess.LeaseExpiresAt != agentdb.SessionLeaseUnset {
		t.Fatalf("the reaper must release the lease so it fires once, got %d", sess.LeaseExpiresAt)
	}
	if sess.Status != "error" {
		t.Fatalf("§8.4: an expired-lease session is marked failed, got %q", sess.Status)
	}
	d := store.deliveryFor(sessionID)
	if d == nil || d.Status != agentdb.DeliveryFailed {
		t.Fatalf("the dead job's delivery must be closed or it holds a max_instances slot for ever: %+v", d)
	}

	var failed *agentdb.ProjectEvent
	for _, id := range store.order {
		if ev := store.events[id]; ev.Type == agentdb.EventTypeWorkerFailed {
			failed = ev
		}
	}
	if failed == nil {
		t.Fatalf("the reaper must emit %s", agentdb.EventTypeWorkerFailed)
	}
	if failed.Envelope.Reason != agentdb.FailureReasonLost {
		t.Fatalf("reason must be %q, got %q", agentdb.FailureReasonLost, failed.Envelope.Reason)
	}
	if failed.Envelope.Worker != "answerer" || failed.Envelope.SessionID != sessionID {
		t.Fatalf("the envelope must name the dead job: %+v", failed.Envelope)
	}
	if failed.Envelope.Depth != 1 {
		t.Fatalf("a lost job's event sits one deeper than its trigger (0+1), got %d — the reaper must "+
			"reuse the Runner's emitter so depth cannot drift", failed.Envelope.Depth)
	}
	if failed.Envelope.Interactive {
		t.Fatalf("a routed job is not interactive: %+v", failed.Envelope)
	}

	// The freed slot is usable straight away.
	store.advance(time.Second)
	postEvent(t, store, "acme", "email.received", "the next one", agentdb.EventEnvelope{})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick after reaping: %v", err)
	}
	if len(starter.jobs) != 2 {
		t.Fatalf("reaping must free the worker's instance, got %d jobs", len(starter.jobs))
	}
}

// TestRouterLeaseReaperLeavesResumableSessionsAlone is the interrupted-turn
// constraint: a turn a human cancelled persists, emits nothing, and stays
// `running` and resumable. It holds NO lease, so the reaper must not report it
// lost — which is exactly why the sweep keys on the lease and never on status.
func TestRouterLeaseReaperLeavesResumableSessionsAlone(t *testing.T) {
	store := newFakeRouterStore()
	ctx := context.Background()
	// A worker session whose turn was interrupted: still `running`, lease
	// released when the turn settled.
	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "interrupted", Customer: "acme", WorkflowID: "agent",
		Status: "running", Worker: "answerer", LeaseExpiresAt: agentdb.SessionLeaseUnset,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	starter := &fakeJobStarter{store: store}
	rt, _ := newTestRouter(store, starter)

	store.advance(48 * time.Hour)
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if got := store.session("interrupted").Status; got != "running" {
		t.Fatalf("an interrupted-but-resumable session must be left alone, status is now %q", got)
	}
	for _, id := range store.order {
		if store.events[id].Type == agentdb.EventTypeWorkerFailed {
			t.Fatalf("a cancelled turn must not be double-reported as a lost job")
		}
	}
}

// ── The production starter (dispatch.go) ────────────────────────────────────

// stubRunner is agentkit.Runner with only the two methods a job start uses.
type stubRunner struct {
	created  []agentkit.CreateSessionRequest
	onSend   func(sessionID string, w agentkit.Writer) error
	sendCall int
}

func (r *stubRunner) CreateSession(_ context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
	r.created = append(r.created, req)
	return &agentkit.SessionHandle{}, nil
}

func (r *stubRunner) SendMessage(_ context.Context, ref agentkit.SessionRef, _ agentkit.SendMessageRequest, w agentkit.Writer) error {
	r.sendCall++
	if r.onSend != nil {
		return r.onSend(ref.SessionID, w)
	}
	return nil
}

func (r *stubRunner) Stream(context.Context, agentkit.SessionRef, agentkit.StreamOptions, agentkit.Writer) error {
	return nil
}
func (r *stubRunner) Stop(context.Context, agentkit.SessionRef) error { return nil }
func (r *stubRunner) Resume(context.Context, agentkit.SessionRef) (*agentkit.SessionHandle, error) {
	return nil, nil
}
func (r *stubRunner) Destroy(context.Context, agentkit.SessionRef) error { return nil }
func (r *stubRunner) Snapshot(context.Context, agentkit.SessionRef) (imageregistry.Handle, error) {
	return imageregistry.Handle{}, nil
}
func (r *stubRunner) WriteWorkspaceFile(context.Context, agentkit.SessionRef, string, []byte) error {
	return nil
}
func (r *stubRunner) Status(context.Context, agentkit.SessionRef) (*agentkit.SessionStatus, error) {
	return nil, nil
}
func (r *stubRunner) RunningSessions(context.Context) (map[string]bool, error) { return nil, nil }
func (r *stubRunner) Start(context.Context) error                              { return nil }
func (r *stubRunner) Close() error                                             { return nil }

// TestRouterStartJobStampsTheDeliveryBeforeTheTurn is the depth guarantee at the
// level that actually ships: the PRODUCTION starter must have stamped the
// delivery with its session id, and taken the lease, before the model is ever
// called — because the job's own worker.finished is emitted from inside that
// call. It also pins the lifecycle: the lease is renewed as the sandbox streams
// and released when the turn settles, and the delivery ends terminal.
func TestRouterStartJobStampsTheDeliveryBeforeTheTurn(t *testing.T) {
	store := newFakeRouterStore()
	worker := seedWorker(store, "acme", "answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	ctx := context.Background()

	var depthDuringTurn int
	var leaseDuringTurn int64
	runner := &stubRunner{}
	runner.onSend = func(sessionID string, w agentkit.Writer) error {
		// This is where the Runner emits worker.finished, so this is the moment
		// the depth walk has to work.
		job, ok, err := agentkit.ResolveWorkerJob(ctx, store, sessionID)
		if err != nil || !ok {
			t.Fatalf("resolve during the turn: ok=%v err=%v", ok, err)
		}
		depthDuringTurn = job.Depth
		// Streaming renews the lease (§8.4 step 4).
		store.advance(2 * sessionLeaseRenewInterval)
		if _, err := w.Write([]byte("data: {}\n\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		leaseDuringTurn = store.session(sessionID).LeaseExpiresAt
		return nil
	}

	starter := newRunnerSessionStarter(runner, store).withLeases(store)
	starter.now = store.now
	starter.logf = quietf
	starter.run = func(fn func()) { fn() } // synchronous, so the test is deterministic
	ids := 0
	starter.newID = func() string { ids++; return fmt.Sprintf("job-%d", ids) }

	rt, _ := newTestRouter(store, starter)
	postEvent(t, store, "acme", "email.received", "a customer wrote in", agentdb.EventEnvelope{Depth: 0})
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if runner.sendCall != 1 {
		t.Fatalf("the composed first message must be sent exactly once, got %d", runner.sendCall)
	}
	if depthDuringTurn != 1 {
		t.Fatalf("during the turn the job must already resolve to depth 1 — if this is 0 the delivery "+
			"was stamped too late and every event the job emits would claim depth 0. got %d", depthDuringTurn)
	}
	if leaseDuringTurn <= store.clock.Add(-sessionLeaseTTL).Unix() {
		t.Fatalf("the lease must be renewed while the sandbox streams, got %d", leaseDuringTurn)
	}
	sess := store.session("job-1")
	if sess == nil {
		t.Fatalf("the session row must be persisted before provisioning")
	}
	if sess.Worker != worker.Name || sess.ComposedPrompt == "" {
		t.Fatalf("the row must carry the worker and the composed prompt: %+v", sess)
	}
	if sess.LeaseExpiresAt != agentdb.SessionLeaseUnset {
		t.Fatalf("a settled turn releases its lease, got %d", sess.LeaseExpiresAt)
	}
	if len(runner.created) != 1 || runner.created[0].Worker != worker.Name {
		t.Fatalf("CreateSession must carry the worker: %+v", runner.created)
	}
	d := store.deliveryFor("job-1")
	if d == nil {
		t.Fatalf("the delivery must be stamped with the session id")
	}
	if d.Status != agentdb.DeliveryOK {
		t.Fatalf("a clean turn closes the delivery ok, got %q", d.Status)
	}
}

// TestRouterStartJobFailureFailsTheDelivery: a turn that errors closes the
// delivery `failed` and drops the lease, so the reaper never double-reports it.
func TestRouterStartJobFailureFailsTheDelivery(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	runner := &stubRunner{onSend: func(string, agentkit.Writer) error { return fmt.Errorf("the sandbox exploded") }}
	starter := newRunnerSessionStarter(runner, store).withLeases(store)
	starter.now = store.now
	starter.logf = quietf
	starter.run = func(fn func()) { fn() }
	starter.newID = func() string { return "job-x" }

	rt, _ := newTestRouter(store, starter)
	postEvent(t, store, "acme", "email.received", "hello", agentdb.EventEnvelope{})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	d := store.deliveryFor("job-x")
	if d == nil || d.Status != agentdb.DeliveryFailed {
		t.Fatalf("a failed turn must close the delivery failed: %+v", d)
	}
	if store.session("job-x").LeaseExpiresAt != agentdb.SessionLeaseUnset {
		t.Fatalf("a failed turn still releases its lease")
	}
}

// TestRouterComposesWithCoreToolsAndBriefing proves the last mile D3 and C4
// each left open: a routed job is actually told about the core MCP server, and
// its briefing sections are injected.
func TestRouterComposesWithCoreToolsAndBriefing(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	starter := &fakeJobStarter{store: store}
	rt, _ := newTestRouter(store, starter, func(c *dispatcherConfig) {
		c.CoreMCP = coreMCPServers("http://172.17.0.1:8099")
		c.Memories = fakeBriefingSource{
			agentkit.RollingSummarySelector("answerer"): "you have answered 40 emails this week",
		}
	})
	postEvent(t, store, "acme", "email.received", "hello", agentdb.EventEnvelope{})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("expected one job, got %d", len(starter.jobs))
	}
	job := starter.jobs[0].Job
	if _, ok := job.MCPServers[coreMCPServerName]; !ok {
		t.Fatalf("a routed job must be told about the core tool server, got %v", job.MCPServers)
	}
	if !strings.Contains(job.SystemPrompt, "you have answered 40 emails this week") {
		t.Fatalf("the briefing section must reach the composed prompt:\n%s", job.SystemPrompt)
	}
}

// fakeBriefingSource is selector → content.
type fakeBriefingSource map[string]string

func (f fakeBriefingSource) NewestMemory(_ context.Context, _ string, selector string) (*agentdb.Memory, error) {
	content, ok := f[selector]
	if !ok {
		return nil, nil
	}
	return &agentdb.Memory{Content: content}, nil
}

// ── Sundry ──────────────────────────────────────────────────────────────────

// TestRouterDrainsQueuedDeliveriesWithNoNewEvents: a project whose deliveries
// queued behind a busy worker gets its turn even when nothing new arrives.
func TestRouterDrainsQueuedDeliveriesWithNoNewEvents(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 1)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	starter := &fakeJobStarter{store: store, hold: true}
	rt, _ := newTestRouter(store, starter)
	ctx := context.Background()

	for _, text := range []string{"first", "second"} {
		postEvent(t, store, "acme", "email.received", text, agentdb.EventEnvelope{})
		store.advance(time.Second)
	}
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	statusesFor(store, agentdb.DeliveryRunning)[0].Status = agentdb.DeliveryOK

	// No new events at all — only the drain can start the queued delivery.
	if err := rt.Tick(ctx); err != nil {
		t.Fatalf("drain tick: %v", err)
	}
	if len(starter.jobs) != 2 {
		t.Fatalf("the queued delivery must be drained with no new event, got %d jobs", len(starter.jobs))
	}
}

// TestRouterFailsDeliveryForARetiredWorker: the gate refuses loudly rather than
// retrying a vanished worker every poll for ever.
func TestRouterFailsDeliveryForARetiredWorker(t *testing.T) {
	store := newFakeRouterStore()
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "ghost", Enabled: true,
	})
	starter := &fakeJobStarter{store: store}
	rt, _ := newTestRouter(store, starter)
	postEvent(t, store, "acme", "email.received", "hello", agentdb.EventEnvelope{})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 0 {
		t.Fatalf("a delivery for a worker that does not exist must not start a job")
	}
	if got := len(statusesFor(store, agentdb.DeliveryFailed)); got != 1 {
		t.Fatalf("expected the delivery to be failed, got %d failed rows", got)
	}
}

// TestRouterLogsWhenNothingMatched pins RD19's router half: an event that wakes
// nobody is still delivered (fanning out to nothing is legal) but it must not be
// byte-identical to a healthy fan-out. Without the log line, "my worker didn't
// wake up" has no observable signal anywhere in the system.
func TestRouterLogsWhenNothingMatched(t *testing.T) {
	store := newFakeRouterStore()
	seedWorker(store, "acme", "answerer", 4)
	store.addSubscription(&agentdb.Subscription{
		Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
	})
	// A typo in the event type: nothing subscribes to `email.recieved`.
	orphan := postEvent(t, store, "acme", "email.recieved", "hello?", agentdb.EventEnvelope{})

	var lines []string
	starter := &fakeJobStarter{store: store}
	gate := newDispatcher(dispatcherConfig{
		Store: store, Starter: starter, DefaultImage: "agentkit-example:dev", Logf: quietf,
	})
	rt := newRouter(routerConfig{
		Store: store, Dispatcher: gate, Now: store.now,
		Logf: func(format string, v ...any) { lines = append(lines, fmt.Sprintf(format, v...)) },
	})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(starter.jobs) != 0 || len(store.deliveries) != 0 {
		t.Fatalf("a zero-match event must start nothing: %d jobs, %d deliveries",
			len(starter.jobs), len(store.deliveries))
	}
	if !store.events[orphan.ID].Delivered {
		t.Fatalf("a zero-match event must still be marked delivered — it is legal, not an error")
	}
	var found string
	for _, l := range lines {
		if strings.Contains(l, "matched NO subscription") {
			found = l
		}
	}
	if found == "" {
		t.Fatalf("a zero-match fan-out must log: got %q", lines)
	}
	for _, want := range []string{orphan.ID, "acme", "email.recieved", "1 enabled subscription"} {
		if !strings.Contains(found, want) {
			t.Fatalf("the zero-match line must name %q — an operator has to know what arrived and how "+
				"many subscriptions were asked. got %q", want, found)
		}
	}

	// The mirror image: a matching event logs nothing of the sort.
	lines = nil
	postEvent(t, store, "acme", "email.received", "real one", agentdb.EventEnvelope{})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	for _, l := range lines {
		if strings.Contains(l, "matched NO subscription") {
			t.Fatalf("a healthy fan-out must not claim it matched nothing: %q", l)
		}
	}
}
