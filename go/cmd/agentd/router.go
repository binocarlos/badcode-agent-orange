package main

// router.go — the event router (spec §8.4), and the two sweeps that keep it
// honest: the session-lease reaper (§8.4 step 4) and the daily token budget
// (§8.4 step 6 / §5).
//
// This is the loop that turns the tables into a product. Everything else in the
// product layer is a row somebody wrote; this is the thing that reads those rows
// and starts work.
//
//	poll undelivered events
//	  → match subscriptions (type prefix + envelope equality — §8.3, and NOTHING else)
//	  → record a delivery (the at-least-once idempotency guard)
//	  → hand it to the shared gate, which composes and starts the job
//	drain deliveries that queued behind a busy worker
//	reap sessions whose lease lapsed
//
// # What this file deliberately does NOT do
//
// It does not check capacity. `max_concurrent_jobs`, `max_instances`, worker
// lookup, composition, session start and the delivery's status transitions all
// live in dispatch.go — ONE gate, shared with the scheduler (H1), because §8.4
// step 7 and §8.6 require a schedule firing and a matched event to queue
// identically. A second capacity check here is how those two silently drift.
//
// # The three things that would silently corrupt behaviour
//
//  1. **Depth.** Every event carries `depth`, and a job's events sit one deeper
//     than the event that triggered it. E2 derives that by walking
//     `event_deliveries.session_id → project_events`, so the delivery's
//     `session_id` MUST be stamped before the job's first turn can finish. That
//     is why dispatch.go stamps it through `OnSessionCreated` rather than after
//     `StartJob` returns. Get it wrong and depth silently reads 0 for ever, and
//     §8.4's loop floor — the only runaway protection the spec kept, every other
//     governor having been rejected on purpose — stops existing.
//  2. **The idempotency guard.** `EnsureDelivery` is what makes at-least-once
//     safe: a router that crashed halfway through an event re-polls it and
//     re-creates nothing. So an event is marked delivered only once every
//     matching subscription has a row.
//  3. **The lease.** A job that dies without reporting back holds a
//     `max_instances` slot for ever. The reaper is the release valve — and it
//     keys on the lease, never on the session's status, so a turn a human simply
//     interrupted (which persists, emits nothing, and stays resumable) is never
//     reported as a lost job.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

const (
	// routerPollInterval is how often the loop wakes. Polling is fine at our
	// scale (§8.4 step 2 says so explicitly); LISTEN/NOTIFY is an optimisation
	// nobody needs yet.
	routerPollInterval = 3 * time.Second
	// routerEventBatch bounds one pass, so a storm of events cannot hold the
	// loop for an unbounded time before the drain and the reap get their turn.
	routerEventBatch = 200
	// maxEventDepth is §8.4 step 3's loop floor: the router REFUSES an event
	// deeper than this and logs loudly. It is resource safety, not opinion —
	// runaway prompt design is a prompt problem, but fork bombs are physics.
	maxEventDepth = 8
	// subscriptionRateWindow is the rolling window `max_firings_per_hour` and
	// the `subscription.throttled` notice are both measured over (§8.3, §8.2).
	subscriptionRateWindow = time.Hour
	// leaseReapBatch bounds one reaper pass.
	leaseReapBatch = 100
	// leaseLostText is the `worker.failed` text for a reaped job. The reason
	// field carries the machine-readable "lost" (§8.2); this is what a human, or
	// the worker woken by it, reads.
	leaseLostText = "session lease expired: the job stopped reporting back and was declared lost"
	// staleDeliveryAge is how long a delivery may sit at `running` before the
	// sweep declares it wedged (RD7).
	//
	// It is deliberately far beyond sessionLeaseTTL. A LIVE job renews its lease
	// every minute and is never touched by this sweep, whatever its age — the
	// sweep only closes rows whose session no longer holds a lease, so a long
	// build inside a container is safe. The age is the second belt: it keeps the
	// sweep off the few-second window in which a settling turn has stamped its
	// status and not yet released its lease, and off a claim whose session row
	// is still being created.
	staleDeliveryAge = time.Hour
	// staleDeliveryBatch bounds one sweep pass, like leaseReapBatch.
	staleDeliveryBatch = 100
	// staleDeliveryText is what the swept row records. It says what is known —
	// that nothing ever reported an outcome — and not what is not.
	staleDeliveryText = "no outcome was ever recorded: agentd stopped before this job could be closed, " +
		"and its session holds no lease, so the job is not running any more"
)

// ── Stores ──────────────────────────────────────────────────────────────────

// routerStore is the narrow slice of *agentdb.Store the loop needs, so matching,
// rate limiting and the depth floor are all testable with no database.
type routerStore interface {
	ListUndeliveredProjectEvents(ctx context.Context, limit int) ([]*agentdb.ProjectEvent, error)
	MarkProjectEventDelivered(ctx context.Context, id string) error
	ListEnabledSubscriptions(ctx context.Context, project string) ([]*agentdb.Subscription, error)
	EnsureDelivery(ctx context.Context, d *agentdb.EventDelivery) (*agentdb.EventDelivery, bool, error)
	CountSubscriptionFiringsSince(ctx context.Context, subscriptionID string, since int64) (int64, error)
	CountRateLimitedDeliveriesSince(ctx context.Context, subscriptionID string, since int64) (int64, error)
	ListProjectsWithPendingDeliveries(ctx context.Context) ([]string, error)
	CreateProjectEvent(ctx context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error)
}

var _ routerStore = (*agentdb.Store)(nil)

// reaperStore is what the lease sweep needs. It embeds agentkit's
// WorkerEventStore so the reaper emits `worker.failed` through the Runner's own
// emitter — one stamping of the §8.2 envelope, never two that can disagree.
type reaperStore interface {
	agentkit.WorkerEventStore
	ListExpiredLeaseSessions(ctx context.Context, now int64, limit int) ([]*agentdb.Session, error)
	ReleaseSessionLease(ctx context.Context, sessionID string) error
	UpdateSession(ctx context.Context, session *agentdb.Session) (*agentdb.Session, error)
	ListDeliveries(ctx context.Context, q agentdb.DeliveryQuery) ([]*agentdb.EventDelivery, error)
	UpdateDeliveryStatus(ctx context.Context, project, id string, u agentdb.DeliveryStatusUpdate) (*agentdb.EventDelivery, error)
	// The stale-delivery sweep (RD7). GetSession is how it tells a wedged row
	// from a live job: a live job holds a lease, a wedged one cannot.
	ListStaleRunningDeliveries(ctx context.Context, startedBefore int64, limit int) ([]*agentdb.EventDelivery, error)
	GetSession(ctx context.Context, sessionID string) (*agentdb.Session, error)
}

var _ reaperStore = (*agentdb.Store)(nil)

// ── The router ──────────────────────────────────────────────────────────────

type router struct {
	store      routerStore
	dispatcher jobDispatcher
	reaper     *leaseReaper

	maxDepth int
	batch    int
	now      func() time.Time
	logf     func(format string, v ...any)
}

type routerConfig struct {
	Store      routerStore
	Dispatcher jobDispatcher
	// Reaper is the §8.4 step 4 sweep. Optional: nil simply means no session is
	// ever declared lost.
	Reaper *leaseReaper
	// MaxDepth overrides the §8.4 loop floor. Tests set it low; nothing else
	// should touch it.
	MaxDepth int
	Batch    int
	Now      func() time.Time
	Logf     func(format string, v ...any)
}

func newRouter(cfg routerConfig) *router {
	maxDepth := cfg.MaxDepth
	if maxDepth <= 0 {
		maxDepth = maxEventDepth
	}
	batch := cfg.Batch
	if batch <= 0 {
		batch = routerEventBatch
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	return &router{
		store:      cfg.Store,
		dispatcher: cfg.Dispatcher,
		reaper:     cfg.Reaper,
		maxDepth:   maxDepth,
		batch:      batch,
		now:        now,
		logf:       logf,
	}
}

// Run drives the loop until ctx is cancelled.
func (r *router) Run(ctx context.Context) {
	t := time.NewTicker(routerPollInterval)
	defer t.Stop()
	r.logf("[router] running (poll=%s, depth floor=%d)", routerPollInterval, r.maxDepth)
	for {
		select {
		case <-ctx.Done():
			r.logf("[router] stopped")
			return
		case <-t.C:
			if err := r.Tick(ctx); err != nil {
				r.logf("[router] tick: %v", err)
			}
		}
	}
}

// Tick is one pass: reap, route, drain.
//
// Reaping comes first so a slot freed by a dead job is available to the events
// this very pass is about to route. Draining comes last for the mirror-image
// reason: whatever this pass queued gets its chance the moment capacity exists.
func (r *router) Tick(ctx context.Context) error {
	if r.reaper != nil {
		if err := r.reaper.Reap(ctx); err != nil {
			r.logf("[router] lease reap: %v", err)
		}
	}
	if err := r.route(ctx); err != nil {
		return err
	}
	r.drain(ctx)
	return nil
}

// route turns undelivered events into deliveries.
func (r *router) route(ctx context.Context) error {
	events, err := r.store.ListUndeliveredProjectEvents(ctx, r.batch)
	if err != nil {
		return fmt.Errorf("poll undelivered events: %w", err)
	}
	for _, ev := range events {
		if err := r.routeEvent(ctx, ev); err != nil {
			// Leave the event undelivered: the next pass replays it, and
			// EnsureDelivery makes every row it already created a no-op.
			r.logf("[router] event %s (%s %s): %v", ev.ID, ev.Project, ev.Type, err)
		}
	}
	return nil
}

func (r *router) routeEvent(ctx context.Context, ev *agentdb.ProjectEvent) error {
	// §8.4 step 3 — the loop floor. Refused loudly and marked delivered: an
	// event nobody may act on must not be re-examined every three seconds for
	// the rest of the deployment's life.
	if ev.Envelope.Depth > r.maxDepth {
		r.logf("[router] REFUSING event %s (%s %s from worker %q): depth %d exceeds the loop floor of %d — "+
			"no job will be created. Something is triggering itself; look at the subscriptions for this chain.",
			ev.ID, ev.Project, ev.Type, ev.Envelope.Worker, ev.Envelope.Depth, r.maxDepth)
		return r.store.MarkProjectEventDelivered(ctx, ev.ID)
	}

	subs, err := r.store.ListEnabledSubscriptions(ctx, ev.Project)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	matched := 0
	for _, sub := range subs {
		if !subscriptionMatches(sub, ev) {
			continue
		}
		matched++
		if err := r.deliver(ctx, sub, ev); err != nil {
			return fmt.Errorf("subscription %s: %w", sub.ID, err)
		}
	}

	// RD19 — a fan-out to nobody is LEGAL, but it must not be byte-identical to
	// a healthy one. Without this line "my worker didn't wake up" has no
	// observable signal anywhere: the event is simply marked delivered and the
	// three-second poll moves on. Not an error, not a failed event — one line
	// naming what arrived and how many subscriptions were asked.
	if matched == 0 {
		r.logf("[router] event %s (%s %s) matched NO subscription — %d enabled subscription(s) considered; "+
			"no job will run. If a worker was meant to wake, check the event type against the project's subscriptions.",
			ev.ID, ev.Project, ev.Type, len(subs))
	}

	// Only once EVERY match has a row: the delivered watermark is the promise
	// that no subscription was skipped.
	return r.store.MarkProjectEventDelivered(ctx, ev.ID)
}

// deliver records one (event, subscription) delivery and hands it to the gate.
func (r *router) deliver(ctx context.Context, sub *agentdb.Subscription, ev *agentdb.ProjectEvent) error {
	if sub.MaxFiringsPerHour > 0 {
		limited, err := r.rateLimited(ctx, sub)
		if err != nil {
			return err
		}
		if limited {
			return r.recordRateLimited(ctx, sub, ev)
		}
	}

	delivery, created, err := r.store.EnsureDelivery(ctx, &agentdb.EventDelivery{
		Project:        ev.Project,
		EventID:        ev.ID,
		SubscriptionID: sub.ID,
		// Denormalised from the subscription: the gate counts and queues on it,
		// and a schedule firing (which has no subscription) fills the same
		// column. One shape for both dispatch paths (H1).
		Worker: sub.Worker,
		Status: agentdb.DeliveryPending,
	})
	if err != nil {
		return fmt.Errorf("ensure delivery: %w", err)
	}
	if !created {
		// Already recorded by an earlier pass — at-least-once means a retry is a
		// no-op, never a second job.
		return nil
	}

	outcome, err := r.dispatcher.Dispatch(ctx, delivery)
	if err != nil {
		// The row exists and is `pending` (or already marked failed by the
		// gate); the drain will pick it up. Never lose the delivery over a
		// transient dispatch error.
		return fmt.Errorf("dispatch: %w", err)
	}
	r.logf("[router] %s %s → %s (%s) = %s", ev.Project, ev.Type, sub.Worker, sub.ID, outcome)
	return nil
}

// rateLimited reports whether this subscription has spent its hourly firings
// (§8.3). `max_firings_per_hour` of 0 is unlimited and never reaches here.
func (r *router) rateLimited(ctx context.Context, sub *agentdb.Subscription) (bool, error) {
	since := r.now().Add(-subscriptionRateWindow).Unix()
	fired, err := r.store.CountSubscriptionFiringsSince(ctx, sub.ID, since)
	if err != nil {
		return false, fmt.Errorf("count firings: %w", err)
	}
	return fired >= int64(sub.MaxFiringsPerHour), nil
}

// recordRateLimited writes the refused delivery and, at most once per rolling
// hour, the `subscription.throttled` event (§8.2).
//
// "At most one per rolling-60-minute window" is derived from the refusals
// themselves rather than from a counter in memory, so the guarantee survives an
// agentd restart. The consequence is deliberate: a subscription throttled
// CONTINUOUSLY says so once and then stays quiet, because the point of the rule
// is to stop core shouting once per dropped event.
func (r *router) recordRateLimited(ctx context.Context, sub *agentdb.Subscription, ev *agentdb.ProjectEvent) error {
	since := r.now().Add(-subscriptionRateWindow).Unix()
	announced, err := r.store.CountRateLimitedDeliveriesSince(ctx, sub.ID, since)
	if err != nil {
		return fmt.Errorf("count rate-limited deliveries: %w", err)
	}
	_, created, err := r.store.EnsureDelivery(ctx, &agentdb.EventDelivery{
		Project:        ev.Project,
		EventID:        ev.ID,
		SubscriptionID: sub.ID,
		Worker:         sub.Worker,
		Status:         agentdb.DeliveryRateLimited,
	})
	if err != nil {
		return fmt.Errorf("ensure rate-limited delivery: %w", err)
	}
	if !created || announced > 0 {
		return nil
	}
	if _, err := r.store.CreateProjectEvent(ctx, &agentdb.ProjectEvent{
		Project:    sub.Project,
		Type:       agentdb.EventTypeSubscriptionThrottled,
		Text:       throttledText(sub),
		OccurredAt: r.now().Unix(),
		Envelope: agentdb.EventEnvelope{
			// §8.2: core's own envelope, carrying neither worker nor session_id.
			// A throttle is a fact about a subscription, not about anyone's job.
			Source: agentdb.EventSourceCore,
			Depth:  0,
		},
	}); err != nil {
		return fmt.Errorf("emit %s: %w", agentdb.EventTypeSubscriptionThrottled, err)
	}
	r.logf("[router] subscription %s (%s → %s) is over %d firings/hour — emitted %s",
		sub.ID, sub.EventType, sub.Worker, sub.MaxFiringsPerHour, agentdb.EventTypeSubscriptionThrottled)
	return nil
}

func throttledText(sub *agentdb.Subscription) string {
	return fmt.Sprintf(
		"Subscription %s (%s → worker %q) has hit its limit of %d firings per hour. "+
			"Further matching events are being recorded as rate_limited and will not start jobs until the hour rolls over.",
		sub.ID, sub.EventType, sub.Worker, sub.MaxFiringsPerHour)
}

// drain gives queued deliveries their turn, through the same gate (§8.4 step 7).
func (r *router) drain(ctx context.Context) {
	projects, err := r.store.ListProjectsWithPendingDeliveries(ctx)
	if err != nil {
		r.logf("[router] list projects with pending deliveries: %v", err)
		return
	}
	for _, project := range projects {
		if _, err := r.dispatcher.DrainPending(ctx, project); err != nil {
			r.logf("[router] drain %s: %v", project, err)
		}
	}
}

// ── Matching (§8.3) ─────────────────────────────────────────────────────────
//
// Two predicates and no more: an exact-or-trailing-`*` event type, and equality
// on envelope fields. Anything smarter belongs in the reacting worker's prompt
// ("if this doesn't concern you, finish immediately"). Growing a third pattern
// here means growing it in `web/src/events.ts` too, which is a second
// implementation of the same rule — see the F1 note in the Discovered Issues
// Log before adding one.

func subscriptionMatches(sub *agentdb.Subscription, ev *agentdb.ProjectEvent) bool {
	if sub == nil || ev == nil {
		return false
	}
	if !eventTypeMatches(sub.EventType, ev.Type) {
		return false
	}
	return envelopeFilterMatches(sub.Filter, ev.Envelope)
}

// eventTypeMatches is §8.3's whole pattern language: exact, or a trailing `*`
// prefix. `*` alone is refused at write time, so it never reaches here.
func eventTypeMatches(pattern, eventType string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(eventType, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == eventType
}

// envelopeFilterMatches is equality on envelope fields, compared as text — the
// same semantics the UI's matcher documents, so a subscription tested in the
// events view behaves identically here. A filter naming a field the envelope
// does not carry (`reason` on anything but worker.failed) never matches.
func envelopeFilterMatches(filter agentdb.JSONMap, envelope agentdb.EventEnvelope) bool {
	if len(filter) == 0 {
		return true
	}
	fields, err := envelopeFields(envelope)
	if err != nil {
		// The envelope is core-stamped and always marshals; if it somehow does
		// not, refusing to match is the safe answer — no job is better than the
		// wrong job.
		return false
	}
	for key, want := range filter {
		have, ok := fields[key]
		if !ok {
			return false
		}
		if scalarText(have) != scalarText(want) {
			return false
		}
	}
	return true
}

// envelopeFields renders the envelope as the jsonb object the filter is written
// against — via its own struct tags, so the filter keys are exactly the wire
// keys and cannot drift from what is stored.
func envelopeFields(envelope agentdb.EventEnvelope) (map[string]any, error) {
	b, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// scalarText stringifies a jsonb scalar the way Postgres's `->>` would, so
// {"interactive": false} and {"interactive": "false"} both match a
// non-interactive event and a depth of 7 never reads as "7e+00".
func scalarText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// ── The lease reaper (§8.4 step 4) ──────────────────────────────────────────

// leaseReaper turns a lapsed session lease into a `worker.failed` with
// `reason:"lost"`, so a dead container can no longer strand a job in limbo for
// ever.
//
// It emits through agentkit.EmitWorkerFailed — the Runner's own emitter —
// because a second stamping of the §8.2 envelope would eventually disagree with
// the first about depth, source or interactivity, and depth is load-bearing.
type leaseReaper struct {
	store reaperStore
	batch int
	now   func() time.Time
	logf  func(format string, v ...any)
}

func newLeaseReaper(store reaperStore) *leaseReaper {
	return &leaseReaper{store: store, batch: leaseReapBatch, now: time.Now, logf: log.Printf}
}

// Reap declares every session holding a lapsed lease lost.
//
// The lease is released BEFORE the event is emitted. That is at-most-once on
// purpose, the same trade the attention sweep makes: emitting first and crashing
// would wake every subscriber twice for one dead job, and a duplicated "your
// worker died" is worse than a missed one, which nothing downstream can tell
// apart from the job simply never having run.
//
// That argument only works because the release is a COMPARE-AND-SET: it is
// conditional on the lease still being held and reports ErrSessionLeaseNotHeld
// otherwise (agentdb/leases.go). It used to be an unconditional UPDATE that
// also reported a missing row as success, which meant two reapers could both
// "claim" the same dead session and both emit — the exact duplicate this
// paragraph claims to prevent.
func (p *leaseReaper) Reap(ctx context.Context) error {
	lost, err := p.store.ListExpiredLeaseSessions(ctx, p.now().Unix(), p.batch)
	if err != nil {
		return fmt.Errorf("list expired leases: %w", err)
	}
	for _, sess := range lost {
		p.reapOne(ctx, sess)
	}
	return p.sweepStaleDeliveries(ctx)
}

// sweepStaleDeliveries closes deliveries wedged at `running` (RD7).
//
// The lease reaper above cannot reach these, and that is not a tuning problem:
// its input is `lease_expires_at > 0`, so a delivery whose session released its
// lease and then failed to reach a terminal status is invisible to it for ever
// — while still counting against `max_instances` and `max_concurrent_jobs`.
// Before this sweep, nothing in the system swept deliveries by age, so the slot
// was gone until somebody edited the database by hand.
//
// The safety rule is "no lease, no life": a delivery is only closed when its
// session holds no lease (or has no session at all — a claim that crashed
// before provisioning). A running job renews its lease every minute, so this
// sweep can never touch one, however long it takes.
func (p *leaseReaper) sweepStaleDeliveries(ctx context.Context) error {
	cutoff := p.now().Add(-staleDeliveryAge).Unix()
	stale, err := p.store.ListStaleRunningDeliveries(ctx, cutoff, staleDeliveryBatch)
	if err != nil {
		return fmt.Errorf("list stale running deliveries: %w", err)
	}
	for _, d := range stale {
		if d.SessionID != "" {
			sess, err := p.store.GetSession(ctx, d.SessionID)
			if err != nil {
				// Could not tell whether the job is alive. Say so and leave the
				// row for the next pass: closing a live job would be the same
				// class of lie in the other direction.
				p.logf("[router] stale delivery %s: could not read session %s: %v", d.ID, d.SessionID, err)
				continue
			}
			if sess != nil && sess.LeaseExpiresAt > agentdb.SessionLeaseUnset {
				continue // still held: a live job, or one the lease reaper owns.
			}
		}
		if _, err := p.store.UpdateDeliveryStatus(ctx, d.Project, d.ID, agentdb.DeliveryStatusUpdate{
			Status:        agentdb.DeliveryFailed,
			FailureReason: staleDeliveryText,
		}); err != nil {
			p.logf("[router] stale delivery %s: could not close it: %v", d.ID, err)
			continue
		}
		p.logf("[router] delivery %s (%s/%s, session %q) was stuck at running since %d with no lease — "+
			"closed as failed and its capacity slot released",
			d.ID, d.Project, d.Worker, d.SessionID, d.StartedAt)
	}
	return nil
}

func (p *leaseReaper) reapOne(ctx context.Context, sess *agentdb.Session) {
	// Resolve the envelope facts first, while the delivery row still points at
	// this session: that walk is where the job's depth comes from.
	job, isWorkerJob, resolveErr := agentkit.ResolveWorkerJob(ctx, p.store, sess.ID)

	if err := p.store.ReleaseSessionLease(ctx, sess.ID); err != nil {
		// Could not claim it: leave it for the next pass rather than emitting an
		// event a second reaper might also emit.
		p.logf("[router] lease reap %s: could not release the lease: %v", sess.ID, err)
		return
	}

	// §8.4: "a reaper marks expired-lease sessions failed". The container is
	// gone, so the row should not read as live work — but note this is only ever
	// reached for a session that HELD a lease, which an interrupted-but-resumable
	// turn never does.
	sess.Status = "error"
	sess.LeaseExpiresAt = agentdb.SessionLeaseUnset
	if _, err := p.store.UpdateSession(ctx, sess); err != nil {
		p.logf("[router] lease reap %s: could not mark the session failed: %v", sess.ID, err)
	}

	// Free the slot the dead job is holding.
	p.failDelivery(ctx, sess)

	if resolveErr != nil {
		p.logf("[router] lease reap %s: %v", sess.ID, resolveErr)
		return
	}
	if !isWorkerJob {
		// A plain session never takes a lease, so this is belt and braces: §8.2
		// fires only for worker jobs.
		return
	}
	if _, err := agentkit.EmitWorkerFailed(ctx, p.store, job, agentdb.FailureReasonLost, leaseLostText); err != nil {
		p.logf("[router] lease reap %s: could not emit %s: %v", sess.ID, agentdb.EventTypeWorkerFailed, err)
		return
	}
	p.logf("[router] session %s (worker %q, project %s) lost its lease — marked failed and emitted %s{reason:%q}",
		sess.ID, sess.Worker, sess.Customer, agentdb.EventTypeWorkerFailed, agentdb.FailureReasonLost)
}

// failDelivery closes the job-history row of a lost session. Without it the
// delivery stays `running` and holds one of the worker's `max_instances` slots
// for ever — the exact deadlock the lease exists to break.
func (p *leaseReaper) failDelivery(ctx context.Context, sess *agentdb.Session) {
	deliveries, err := p.store.ListDeliveries(ctx, agentdb.DeliveryQuery{
		Project:   sess.Customer,
		SessionID: sess.ID,
		Status:    agentdb.DeliveryRunning,
	})
	if err != nil {
		p.logf("[router] lease reap %s: could not read the delivery: %v", sess.ID, err)
		return
	}
	for _, d := range deliveries {
		if _, err := p.store.UpdateDeliveryStatus(ctx, d.Project, d.ID, agentdb.DeliveryStatusUpdate{
			Status: agentdb.DeliveryFailed,
			// The same sentence the `worker.failed` event carries, so the job
			// row and the event agree about why (RD20).
			FailureReason: leaseLostText,
		}); err != nil {
			p.logf("[router] lease reap %s: could not fail delivery %s: %v", sess.ID, d.ID, err)
		}
	}
}

// ── The daily token budget (§8.4 step 6, §5) ────────────────────────────────

// budgetStore is the token ledger the budget reads.
type budgetStore interface {
	CountProjectTokensSince(ctx context.Context, project string, since int64) (int64, error)
}

var _ budgetStore = (*agentdb.Store)(nil)

// tokenBudget implements §5's two tiers, per project, per day:
//
//   - crossing `daily_tokens_soft` sends exactly one attention-channel
//     notification per day — a heads-up; nothing stops;
//   - crossing `daily_tokens_hard` creates no non-interactive jobs until
//     midnight (stack-local), when both counters reset. Matching deliveries
//     queue as `pending` and go out afterwards (§8.4 step 6).
//
// Both tiers exempt interactive chat, and get that for free: interactive
// sessions never become deliveries, so they never reach the gate this plugs
// into. A blown budget can never lock a human out of talking to their workers.
type tokenBudget struct {
	store budgetStore
	// notify delivers the soft-tier heads-up. Swapped in tests.
	notify func(ctx context.Context, project string, settings *agentdb.ProjectSettings, used int64)
	loc    *time.Location
	now    func() time.Time
	logf   func(format string, v ...any)

	mu sync.Mutex
	// softNotified is project → the day already announced. In memory on
	// purpose: "exactly one per day" is a courtesy, and the alternative is a
	// table whose only reader is a log line.
	softNotified map[string]string
	// hardStopped is project → the day we last logged the stop, so a
	// hard-budget-stopped project does not log once per poll per delivery.
	hardStopped map[string]string
}

type tokenBudgetConfig struct {
	Store  budgetStore
	Notify func(ctx context.Context, project string, settings *agentdb.ProjectSettings, used int64)
	// Location is the stack-local zone that decides when midnight is (§5).
	// nil → time.Local, which honours TZ and defaults to UTC in the image.
	Location *time.Location
	Now      func() time.Time
	Logf     func(format string, v ...any)
}

func newTokenBudget(cfg tokenBudgetConfig) *tokenBudget {
	loc := cfg.Location
	if loc == nil {
		loc = time.Local
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	return &tokenBudget{
		store:        cfg.Store,
		notify:       cfg.Notify,
		loc:          loc,
		now:          now,
		logf:         logf,
		softNotified: map[string]string{},
		hardStopped:  map[string]string{},
	}
}

// Allow answers the gate's §8.4 step 6 question.
func (b *tokenBudget) Allow(ctx context.Context, project string, settings *agentdb.ProjectSettings) (bool, error) {
	if settings == nil {
		return true, nil
	}
	// 0 = off on both tiers (§5). Unmetered is the default, and it costs
	// nothing: no ledger query at all.
	if settings.DailyTokensSoft <= 0 && settings.DailyTokensHard <= 0 {
		return true, nil
	}
	day := b.now().In(b.loc)
	used, err := b.store.CountProjectTokensSince(ctx, project, startOfDay(day).Unix())
	if err != nil {
		return true, fmt.Errorf("daily token total: %w", err)
	}
	dayKey := day.Format("2006-01-02")

	if settings.DailyTokensSoft > 0 && used >= settings.DailyTokensSoft {
		b.notifySoftOnce(ctx, project, dayKey, settings, used)
	}
	if settings.DailyTokensHard > 0 && used >= settings.DailyTokensHard {
		b.logHardStopOnce(project, dayKey, settings, used)
		return false, nil
	}
	return true, nil
}

func (b *tokenBudget) notifySoftOnce(ctx context.Context, project, dayKey string, settings *agentdb.ProjectSettings, used int64) {
	b.mu.Lock()
	already := b.softNotified[project] == dayKey
	if !already {
		b.softNotified[project] = dayKey
	}
	b.mu.Unlock()
	if already {
		return
	}
	b.logf("[router] %s crossed its soft daily token budget (%d used, soft %d)", project, used, settings.DailyTokensSoft)
	if b.notify != nil {
		b.notify(ctx, project, settings, used)
	}
}

func (b *tokenBudget) logHardStopOnce(project, dayKey string, settings *agentdb.ProjectSettings, used int64) {
	b.mu.Lock()
	already := b.hardStopped[project] == dayKey
	if !already {
		b.hardStopped[project] = dayKey
	}
	b.mu.Unlock()
	if already {
		return
	}
	b.logf("[router] %s is HARD-BUDGET-STOPPED (%d used, hard %d): non-interactive jobs queue as pending "+
		"until midnight (%s). Interactive chat is unaffected.", project, used, settings.DailyTokensHard, b.loc)
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// softBudgetNotifier posts the soft-tier heads-up to the project's
// `attention_channel` (§5). It reuses H2's channel parsing and webhook POST
// rather than growing a second notification path; `session_url` is empty because
// a budget notice is about the project, not about anybody's session.
func softBudgetNotifier(env func(string) string, logf func(format string, v ...any)) func(context.Context, string, *agentdb.ProjectSettings, int64) {
	return func(ctx context.Context, project string, settings *agentdb.ProjectSettings, used int64) {
		ch, err := parseAttentionChannel(settings.AttentionChannel)
		if err != nil {
			logf("[router] %s: soft budget notice undeliverable: %v", project, err)
			return
		}
		if !ch.configured() {
			// The documented fallback everywhere else in §9: log only.
			return
		}
		headers, err := ch.resolveHeaders(env)
		if err != nil {
			logf("[router] %s: soft budget notice undeliverable: %v", project, err)
			return
		}
		message := fmt.Sprintf(
			"Project %q has used %d tokens today, crossing its soft daily budget of %d. Nothing has stopped; "+
				"the hard budget is what pauses non-interactive jobs.", project, used, settings.DailyTokensSoft)
		if err := postAttentionWebhook(ctx, ch, headers, attentionPayload{Message: message}); err != nil {
			logf("[router] %s: soft budget notice delivery failed: %v", project, err)
		}
	}
}
