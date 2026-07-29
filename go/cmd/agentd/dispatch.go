package main

// dispatch.go — THE gated dispatch point (spec §8.4 steps 3, 5 and 7).
//
// # Why this file exists at all
//
// Two things in agentd turn a delivery row into a running job: the router (E3,
// event-matched deliveries) and the scheduler (H1, schedule firings). §8.4 step
// 7 and §8.6 both require them to gate IDENTICALLY — a firing for a worker
// already at `max_instances` must queue as `pending`, exactly as a matched event
// would, and be dispatched FIFO when an instance frees. Two implementations of
// that rule would drift the first time one of them was fixed.
//
// So the gate is one type here, and both loops call it:
//
//	dispatcher.Dispatch(ctx, delivery)   — try to start ONE pending delivery
//	dispatcher.DrainPending(ctx, proj)   — start queued deliveries FIFO
//
// # What E3 (the router) must call
//
// The router owns matching (event type + envelope filter), the idempotency guard
// (`EnsureDelivery`), rate limiting and the depth floor. Once it has a `pending`
// EventDelivery row — with `Worker` filled in from the matching subscription —
// it hands that row to `Dispatch` and does NOT check capacity itself. On every
// poll it also calls `DrainPending(project)` so deliveries that queued earlier
// get their turn as instances free. Everything below the "capacity" line
// (worker lookup, project cap, per-worker cap, ComposeJob, session start,
// delivery status) is this file's job, for both callers.
//
// Deliberately NOT gated here: interactive chat. §8.4 step 5 exempts interactive
// jobs from `max_concurrent_jobs`, and interactive sessions are created straight
// through httpapi's CreateSession — they never become deliveries, so they cannot
// be queued behind background load.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/google/uuid"
)

// dispatchStore is the narrow slice of *agentdb.Store the gate needs. It is an
// interface so the gating rules are unit-testable with no database.
type dispatchStore interface {
	GetWorker(ctx context.Context, project, name string) (*agentdb.Worker, error)
	GetProjectSettings(ctx context.Context, project string) (*agentdb.ProjectSettings, error)
	GetProjectEvent(ctx context.Context, project, id string) (*agentdb.ProjectEvent, error)
	CountActiveDeliveries(ctx context.Context, project string) (int64, error)
	CountActiveDeliveriesForWorker(ctx context.Context, project, worker string) (int64, error)
	ListPendingDeliveries(ctx context.Context, project, worker string, limit int) ([]*agentdb.EventDelivery, error)
	UpdateDeliveryStatus(ctx context.Context, project, id string, u agentdb.DeliveryStatusUpdate) (*agentdb.EventDelivery, error)
	// SessionAwaitsHuman answers §8.4's `awaiting_human`: did this job end its
	// turn with a `request_human_attention` still open?
	SessionAwaitsHuman(ctx context.Context, project, sessionID string) (bool, error)
}

// The concrete store must always satisfy the seam.
var _ dispatchStore = (*agentdb.Store)(nil)

// startJobInput is a composed job ready to run.
type startJobInput struct {
	Project string
	Worker  *agentdb.Worker
	Event   *agentdb.ProjectEvent
	Job     *agentkit.ComposedJob

	// OnSessionCreated is called with the new session id AS SOON AS the session
	// row exists and BEFORE the first message is sent. That ordering is
	// load-bearing twice over:
	//
	//  1. Depth (§8.4). E2 derives a job's depth by walking
	//     event_deliveries.session_id → project_events. The first turn emits
	//     worker.finished/worker.failed while it is still running, so a
	//     session_id stamped only after the turn returns would make every event
	//     a job emits read as depth 0 — silently disabling the loop floor, which
	//     is the only runaway protection the spec kept.
	//  2. Capacity (§8.4 steps 3 and 7). A delivery that only becomes `running`
	//     once its turn ends is invisible to the concurrency counts for exactly
	//     as long as it is actually consuming a slot.
	//
	// Returning an error aborts the job before the model is ever called.
	// Optional: a starter with no hook simply does not call it.
	OnSessionCreated func(ctx context.Context, sessionID string) error
	// OnSessionEnded is called once the turn has settled, with whatever
	// SendMessage returned. It is where the delivery reaches a terminal status
	// and the session lease is released.
	OnSessionEnded func(ctx context.Context, sessionID string, err error)
}

// sessionStarter turns a composed job into a running session and returns its id.
// The seam keeps the Runner (and Docker) out of the gate's tests.
type sessionStarter interface {
	StartJob(ctx context.Context, in startJobInput) (sessionID string, err error)
}

// dispatchOutcome is what one Dispatch call did.
type dispatchOutcome string

const (
	// dispatchStarted: a session was created and the delivery is `running`.
	dispatchStarted dispatchOutcome = "started"
	// dispatchQueued: capacity was full, so the delivery stays `pending` and will
	// be retried FIFO by DrainPending (§8.4 step 7).
	dispatchQueued dispatchOutcome = "queued"
	// dispatchSkipped: nothing to do (the delivery was not pending any more —
	// another agentd got there first).
	dispatchSkipped dispatchOutcome = "skipped"
	// dispatchFailed: the job cannot run at all (worker gone or disabled,
	// composition refused). The delivery is marked `failed`, loudly.
	dispatchFailed dispatchOutcome = "failed"
)

// dispatcher is the shared gate. One instance is created in main() and given to
// both the scheduler and (when E3 lands) the router.
type dispatcher struct {
	store   dispatchStore
	starter sessionStarter

	// Composition inputs (§6.2). CoreMCP is the engine's own tool servers
	// (memory tools today — E4/I2 add theirs to the same server); DefaultImage is
	// the global Policy.BaseImage; Images resolves a worker's §13 image pointer
	// (imageresolver.go — the SAME object the Runner holds, so a job and a chat
	// with one worker cannot launch from different environments), nil only on
	// the SQLite fallback where there is no catalogue; Memories is C4's
	// briefing read seam (nil ⇒ no briefing sections, which is the SQLite
	// fallback where memory is unavailable by decision).
	coreMCP      agentdb.MCPServers
	defaultImage string
	images       agentkit.ImageResolver
	memories     agentkit.BriefingMemorySource

	// budget is the §8.4 step 6 / §5 daily-token check. nil = unmetered.
	budget budgetGate

	logf func(format string, v ...any)
}

// budgetGate answers §8.4 step 6: may a NON-INTERACTIVE job be created for this
// project right now? A false answer leaves the delivery `pending`, which is
// exactly what "matching event deliveries queue as pending and are delivered
// after midnight" means (§8.4 step 6).
//
// It is an interface so the gate stays testable without a token ledger, and so
// there is one budget check serving both the router and the scheduler — the
// same reason the capacity checks live here and nowhere else.
type budgetGate interface {
	Allow(ctx context.Context, project string, settings *agentdb.ProjectSettings) (bool, error)
}

// dispatcherConfig is what main() supplies.
type dispatcherConfig struct {
	Store        dispatchStore
	Starter      sessionStarter
	CoreMCP      agentdb.MCPServers
	DefaultImage string
	Images       agentkit.ImageResolver
	Memories     agentkit.BriefingMemorySource
	Budget       budgetGate
	Logf         func(format string, v ...any)
}

func newDispatcher(cfg dispatcherConfig) *dispatcher {
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	return &dispatcher{
		store:        cfg.Store,
		starter:      cfg.Starter,
		coreMCP:      cfg.CoreMCP,
		defaultImage: cfg.DefaultImage,
		images:       cfg.Images,
		memories:     cfg.Memories,
		budget:       cfg.Budget,
		logf:         logf,
	}
}

// Dispatch tries to start one pending delivery. It is the ONLY place either loop
// decides whether a job may run now.
//
// The order of the checks is the order of §8.4: is there still work to do, does
// the worker exist, is the project at its cap, is the worker at its cap, and
// only then compose and start.
func (d *dispatcher) Dispatch(ctx context.Context, delivery *agentdb.EventDelivery) (dispatchOutcome, error) {
	outcome, _, err := d.DispatchWithReason(ctx, delivery)
	return outcome, err
}

// DispatchWithReason is Dispatch plus the human reason behind a `failed`
// outcome, which is otherwise only ever logged (§8.4's delivery tuple has no
// reason column). The scheduler needs it: a schedule disabled for repeatedly
// failing to provision must record WHY, and "the port pool is exhausted" is the
// difference between a five-minute fix and a day of misdiagnosis.
//
// It is a second method rather than a wider Dispatch signature on purpose. The
// router treats a non-nil error as "the delivery is still ours, the drain will
// retry it"; folding the reason into that error would turn every unstartable
// job into a router-level failure. One gate, two readings, no drift.
func (d *dispatcher) DispatchWithReason(ctx context.Context, delivery *agentdb.EventDelivery) (dispatchOutcome, string, error) {
	if delivery == nil {
		return dispatchSkipped, "", fmt.Errorf("dispatch: delivery is required")
	}
	if delivery.Status != agentdb.DeliveryPending {
		// Already claimed by another pass or another process. At-least-once
		// delivery means a duplicate attempt must be a no-op, not a second job.
		return dispatchSkipped, "", nil
	}
	if delivery.Worker == "" {
		reason := d.fail(ctx, delivery, "delivery carries no worker")
		return dispatchFailed, reason, fmt.Errorf("dispatch: delivery %s carries no worker", delivery.ID)
	}

	worker, err := d.store.GetWorker(ctx, delivery.Project, delivery.Worker)
	switch {
	case errors.Is(err, agentdb.ErrWorkerNotFound):
		// A worker that has been retired cannot run: fail the delivery loudly
		// rather than retrying it every poll forever. (For a SCHEDULE firing the
		// scheduler additionally disables the schedule — §8.6.)
		return dispatchFailed, d.fail(ctx, delivery, fmt.Sprintf("worker %q no longer exists", delivery.Worker)), nil
	case err != nil:
		// The database could not answer. That says nothing about the worker, and
		// `failed` is terminal: the router has already consumed the trigger, so a
		// delivery failed here is a lost trigger with no path back. Leave it
		// `pending` and report the error — the router's own comment says it, and
		// every other store call in this function already does this (RD1).
		return dispatchSkipped, "", fmt.Errorf("dispatch: read worker %q: %w", delivery.Worker, err)
	}
	if !worker.Enabled {
		return dispatchFailed, d.fail(ctx, delivery, fmt.Sprintf("worker %q is disabled", worker.Name)), nil
	}

	settings, err := d.store.GetProjectSettings(ctx, delivery.Project)
	if err != nil {
		return dispatchSkipped, "", fmt.Errorf("dispatch: project settings: %w", err)
	}

	// §8.4 step 6 — the daily token budget (§5). H1 left this slot so there is
	// ONE budget check on both dispatch paths. Everything that reaches here is
	// non-interactive by construction: interactive chat never becomes a
	// delivery (see the file header), which is also §8.4 step 5's exemption.
	//
	// A budget the gate cannot evaluate does not stop the world: the failure is
	// logged loudly and the job runs. Spending slightly over a soft ceiling
	// because Postgres hiccuped is a smaller harm than a project whose whole
	// workforce silently stops.
	if d.budget != nil {
		allowed, err := d.budget.Allow(ctx, delivery.Project, settings)
		if err != nil {
			d.logf("[dispatch] %s: budget check failed, allowing the job: %v", delivery.Project, err)
		} else if !allowed {
			return dispatchQueued, "", nil
		}
	}

	// §8.4 step 3 — the per-project concurrency cap, shared by router and
	// scheduler.
	active, err := d.store.CountActiveDeliveries(ctx, delivery.Project)
	if err != nil {
		return dispatchSkipped, "", fmt.Errorf("dispatch: count active: %w", err)
	}
	if settings.MaxConcurrentJobs > 0 && active >= int64(settings.MaxConcurrentJobs) {
		return dispatchQueued, "", nil
	}

	// §8.4 step 7 — the per-worker instance gate. Excess deliveries stay pending
	// and are dispatched FIFO as instances free; this applies identically to
	// schedule firings, which is the whole point of one shared gate.
	max := worker.MaxInstances
	if max <= 0 {
		max = agentdb.DefaultMaxInstances
	}
	running, err := d.store.CountActiveDeliveriesForWorker(ctx, delivery.Project, worker.Name)
	if err != nil {
		return dispatchSkipped, "", fmt.Errorf("dispatch: count worker instances: %w", err)
	}
	if running >= int64(max) {
		return dispatchQueued, "", nil
	}

	// Compose (§6.2) — the identical path for every trigger.
	var event *agentdb.ProjectEvent
	if delivery.EventID != "" {
		// Same classification rule as the worker read above: only a genuinely
		// absent event row makes this delivery unrunnable forever.
		event, err = d.store.GetProjectEvent(ctx, delivery.Project, delivery.EventID)
		switch {
		case errors.Is(err, agentdb.ErrProjectEventNotFound):
			return dispatchFailed, d.fail(ctx, delivery, fmt.Sprintf("event %s no longer exists", delivery.EventID)), nil
		case err != nil:
			return dispatchSkipped, "", fmt.Errorf("dispatch: read event %s: %w", delivery.EventID, err)
		}
	}
	job, err := agentkit.ComposeJob(ctx, agentkit.ComposeJobInput{
		Project:  delivery.Project,
		Worker:   worker,
		Settings: settings,
		Event:    event,
		CoreMCP:  d.coreMCP,
		// §6.2 step 2.4 — the rolling summary and each `briefing` selector.
		// BuildBriefingSections returns no error by design: a worker with a
		// stale briefing works, one that cannot start does not (C4).
		Briefing:      agentkit.BuildBriefingSections(ctx, d.memories, delivery.Project, worker, settings),
		DefaultImage:  d.defaultImage,
		ImageResolver: d.images,
	})
	if err != nil {
		// Composition refuses loudly (an unresolvable image pointer, a malformed
		// stored MCP config). That is a job failure, never a silent fallback.
		return dispatchFailed, d.fail(ctx, delivery, fmt.Sprintf("compose: %v", err)), nil
	}

	if d.starter == nil {
		return dispatchSkipped, "", fmt.Errorf("dispatch: no session starter configured")
	}
	project, deliveryID := delivery.Project, delivery.ID
	stamped := false
	sessionID, err := d.starter.StartJob(ctx, startJobInput{
		Project: delivery.Project,
		Worker:  worker,
		Event:   event,
		Job:     job,
		OnSessionCreated: func(ctx context.Context, sessionID string) error {
			stamped = true
			_, err := d.store.UpdateDeliveryStatus(ctx, project, deliveryID, agentdb.DeliveryStatusUpdate{
				Status:    agentdb.DeliveryRunning,
				SessionID: sessionID,
			})
			return err
		},
		OnSessionEnded: func(ctx context.Context, sessionID string, runErr error) {
			status := agentdb.DeliveryOK
			if runErr != nil {
				status = agentdb.DeliveryFailed
				d.logf("[dispatch] delivery %s (%s/%s) session %s ended badly: %v",
					deliveryID, project, worker.Name, sessionID, runErr)
			} else if awaits, err := d.store.SessionAwaitsHuman(ctx, project, sessionID); err != nil {
				// Unknowable ⇒ close it normally. A delivery wrongly left parked
				// would misreport the job for ever; wrongly closed it only loses a
				// status nuance — the human still has the link the webhook carried,
				// and the session itself is untouched either way.
				d.logf("[dispatch] delivery %s: could not check for an open attention request: %v",
					deliveryID, err)
			} else if awaits {
				// §8.4: the job asked for a human and ended its turn. That is a
				// PAUSE, not a completion — UpdateDeliveryStatus leaves ended_at
				// unset for `awaiting_human` (E1). No approval machinery follows
				// (§9): the human clicks the permalink, types the next message, and
				// the thread carries on. Note this frees the worker's instance slot,
				// which is deliberate — see CountActiveDeliveriesForWorker.
				status = agentdb.DeliveryAwaitingHuman
				d.logf("[dispatch] delivery %s (%s/%s) session %s is awaiting a human",
					deliveryID, project, worker.Name, sessionID)
			}
			if _, err := d.store.UpdateDeliveryStatus(ctx, project, deliveryID, agentdb.DeliveryStatusUpdate{
				Status:    status,
				SessionID: sessionID,
			}); err != nil {
				// A delivery stuck at `running` holds a max_instances slot for
				// ever, so this is loud: the lease reaper is the backstop.
				d.logf("[dispatch] delivery %s: could not close as %s: %v", deliveryID, status, err)
			}
		},
	})
	if err != nil {
		return dispatchFailed, d.fail(ctx, delivery, fmt.Sprintf("start job: %v", err)), nil
	}

	// A starter that ignores OnSessionCreated still gets its delivery stamped —
	// but only then. Re-stamping unconditionally would race an already-settled
	// turn back from `ok` to `running` and strand the slot.
	if !stamped {
		if _, err := d.store.UpdateDeliveryStatus(ctx, delivery.Project, delivery.ID, agentdb.DeliveryStatusUpdate{
			Status:    agentdb.DeliveryRunning,
			SessionID: sessionID,
		}); err != nil {
			return dispatchStarted, "", fmt.Errorf("dispatch: mark running: %w", err)
		}
	}
	return dispatchStarted, "", nil
}

// DrainPending dispatches a project's queued deliveries oldest-first, stopping
// at the first one the gate queues again. Router and scheduler both call it each
// poll; that is how "excess deliveries are dispatched FIFO as instances free"
// actually happens (§8.4 step 7).
//
// Stopping early only when the PROJECT is at capacity would starve a worker with
// free instances behind one that is full, so a per-worker queue does not stop
// the drain: the loop keeps going and lets the gate answer per delivery. It does
// stop as soon as the project-wide cap is reached, because nothing can start
// then.
func (d *dispatcher) DrainPending(ctx context.Context, project string) (int, error) {
	pending, err := d.store.ListPendingDeliveries(ctx, project, "", 0)
	if err != nil {
		return 0, err
	}
	started := 0
	blocked := map[string]bool{}
	for _, delivery := range pending {
		if blocked[delivery.Worker] {
			// This worker already answered "at capacity" this pass: skip its
			// remaining deliveries so FIFO order within the worker is preserved.
			continue
		}
		outcome, err := d.Dispatch(ctx, delivery)
		if err != nil {
			d.logf("[scheduler] drain %s/%s: %v", project, delivery.ID, err)
		}
		switch outcome {
		case dispatchStarted:
			started++
		case dispatchQueued:
			blocked[delivery.Worker] = true
			// A project-wide block shows up as every worker blocking in turn; a
			// cheap extra probe stops the loop early in the common case.
			if full, err := d.projectAtCapacity(ctx, project); err == nil && full {
				return started, nil
			}
		}
	}
	return started, nil
}

func (d *dispatcher) projectAtCapacity(ctx context.Context, project string) (bool, error) {
	settings, err := d.store.GetProjectSettings(ctx, project)
	if err != nil {
		return false, err
	}
	if settings.MaxConcurrentJobs <= 0 {
		return false, nil
	}
	active, err := d.store.CountActiveDeliveries(ctx, project)
	if err != nil {
		return false, err
	}
	return active >= int64(settings.MaxConcurrentJobs), nil
}

// fail marks a delivery failed and logs why. §8.4's delivery tuple records no
// reason column (an E1 finding), so the log is where the reason lives until one
// is added.
// It echoes the reason back so DispatchWithReason can hand it to a caller that
// has to record it (the scheduler) without a second formatting of the same text.
func (d *dispatcher) fail(ctx context.Context, delivery *agentdb.EventDelivery, reason string) string {
	d.logf("[dispatch] delivery %s (%s/%s) failed: %s", delivery.ID, delivery.Project, delivery.Worker, reason)
	if _, err := d.store.UpdateDeliveryStatus(ctx, delivery.Project, delivery.ID, agentdb.DeliveryStatusUpdate{
		Status: agentdb.DeliveryFailed,
	}); err != nil {
		d.logf("[dispatch] delivery %s: could not mark failed: %v", delivery.ID, err)
	}
	return reason
}

// ── The real session starter ────────────────────────────────────────────────

// runnerSessionStarter creates the session row, provisions it through the
// Runner, and sends the composed first message. It is the production
// sessionStarter; E3 reuses it rather than growing a second one.
//
// # Why the turn runs in a goroutine
//
// `SendMessage` blocks for the whole model turn. Running it inline would make
// every capacity rule in §8.4 a fiction: with the loop parked on one turn, a
// project could never reach `max_concurrent_jobs`, a worker could never reach
// `max_instances`, and one slow job would stall all routing behind it. So the
// gate's decision stays synchronous (its checks are three cheap counts) and only
// the turn itself is detached. The delivery is stamped `running` BEFORE the
// goroutine starts, so the counts the gate reads are true the whole time.
type runnerSessionStarter struct {
	runner agentkit.Runner
	store  agentkit.RunnerStore
	// leases is the §8.4 step 4 lease surface. Optional: with no lease store a
	// job simply holds no lease and is never reaped (the SQLite fallback).
	leases leaseStore
	// newID mints session ids; overridable in tests.
	newID func() string
	// run executes the detached turn. Swapped in tests so a job can be driven
	// synchronously; nil means "go".
	run  func(func())
	now  func() time.Time
	logf func(format string, v ...any)
}

// leaseStore is the narrow slice of *agentdb.Store the lease needs.
type leaseStore interface {
	RenewSessionLease(ctx context.Context, sessionID string, until int64) error
	ReleaseSessionLease(ctx context.Context, sessionID string) error
}

var _ leaseStore = (*agentdb.Store)(nil)

const (
	// sessionLeaseTTL is how long a lease outlives its last renewal.
	//
	// Deliberately generous. A job renews while the sandbox streams, but a long
	// silent step inside the container (a build, a big test run) produces no
	// stream traffic at all, and reaping a live job would wake every subscriber
	// with a `worker.failed` that is simply false. The failures the reaper
	// actually exists for — agentd killed mid-turn, container gone — are
	// permanent, so noticing them fifteen minutes late costs nothing.
	sessionLeaseTTL = 15 * time.Minute
	// sessionLeaseRenewInterval throttles renewals: one UPDATE a minute per
	// running job, not one per streamed token.
	sessionLeaseRenewInterval = time.Minute
)

func newRunnerSessionStarter(runner agentkit.Runner, store agentkit.RunnerStore) *runnerSessionStarter {
	return &runnerSessionStarter{runner: runner, store: store, logf: log.Printf}
}

// withLeases returns the starter with the §8.4 lease surface bound.
func (r *runnerSessionStarter) withLeases(leases leaseStore) *runnerSessionStarter {
	r.leases = leases
	return r
}

func (r *runnerSessionStarter) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *runnerSessionStarter) StartJob(ctx context.Context, in startJobInput) (string, error) {
	if in.Worker == nil || in.Job == nil {
		return "", fmt.Errorf("start job: worker and composed job are required")
	}
	sessionID := ""
	if r.newID != nil {
		sessionID = r.newID()
	} else {
		sessionID = newSessionID()
	}

	// The Runner's contract: the host persists the row BEFORE provisioning. The
	// worker and the composed prompt land here, at composition time, so every
	// transcript is tied to the exact prompt that produced it (§6.2, §6.5) —
	// and `composed_prompt` is what each of this session's turns is actually run
	// with (runner.turnSystemPrompt).
	// The lease is taken in the same write: from this moment on, a crash leaves
	// something the reaper can find (§8.4 step 4).
	//
	// `persona` is deliberately LEFT EMPTY on a routed job. It is the key
	// sessioncontext.go resolves a worker's prompt/image/MCP layer from, and a
	// composed job has already had all three composed and pinned. Setting it
	// would give the session two worker identities that can disagree — `worker`
	// (what the job IS, what the core MCP server attributes writes to) and
	// `persona` (a live re-resolution of config that may have changed since
	// dispatch). One mechanism: `worker` names the worker, `composed_prompt`
	// carries its prompt, and neither is re-derived while the job runs.
	if _, err := r.store.UpdateSession(ctx, &agentdb.Session{
		ID:             sessionID,
		Customer:       in.Project,
		WorkflowID:     "agent",
		Status:         "creating",
		Worker:         in.Worker.Name,
		ComposedPrompt: in.Job.SystemPrompt,
		LeaseExpiresAt: r.clock().Add(sessionLeaseTTL).Unix(),
	}); err != nil {
		return "", fmt.Errorf("persist session row: %w", err)
	}

	if _, err := r.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID:    sessionID,
		Customer:     in.Project,
		SystemPrompt: in.Job.SystemPrompt,
		Image:        in.Job.Image,
		MCPServers:   in.Job.MCPServers,
		Worker:       in.Worker.Name,
	}); err != nil {
		r.abandon(ctx, sessionID)
		return "", fmt.Errorf("create session: %w", err)
	}
	if sess, err := r.store.GetSession(ctx, sessionID); err == nil && sess != nil {
		sess.Status = "running"
		_, _ = r.store.UpdateSession(ctx, sess)
	}

	// The delivery learns its session id here — before the first message, so the
	// depth walk and the concurrency counts both see this job while it runs.
	if in.OnSessionCreated != nil {
		if err := in.OnSessionCreated(ctx, sessionID); err != nil {
			r.abandon(ctx, sessionID)
			return "", fmt.Errorf("claim delivery: %w", err)
		}
	}

	// A job with no event (a bare "run this worker") has nothing to say and
	// settles immediately rather than sitting idle holding a slot.
	if in.Job.FirstMessage == "" {
		r.settle(ctx, sessionID, nil, in.OnSessionEnded)
		return sessionID, nil
	}

	// Detach: the poll that dispatched this job must not be able to cancel the
	// turn, and the turn outlives the request that started it.
	turnCtx := context.WithoutCancel(ctx)
	project := in.Project
	onEnded := in.OnSessionEnded
	r.spawn(func() {
		err := r.runner.SendMessage(turnCtx, agentkit.SessionRef{SessionID: sessionID}, agentkit.SendMessageRequest{
			Content:  in.Job.FirstMessage,
			Customer: project,
		}, r.leaseWriter(turnCtx, sessionID))
		if err != nil {
			err = fmt.Errorf("send first message: %w", err)
		}
		r.settle(turnCtx, sessionID, err, onEnded)
	})
	return sessionID, nil
}

func (r *runnerSessionStarter) spawn(fn func()) {
	if r.run != nil {
		r.run(fn)
		return
	}
	go fn()
}

// settle closes out a turn: the lease goes first, so the reaper can never
// double-report a job whose outcome is already known, and a turn a human
// cancelled (which emits no event at all, by E2's decision) is simply a job
// that released its lease without failing.
func (r *runnerSessionStarter) settle(ctx context.Context, sessionID string, err error, onEnded func(context.Context, string, error)) {
	r.releaseLease(ctx, sessionID)
	if onEnded != nil {
		onEnded(ctx, sessionID, err)
	}
}

// abandon undoes a half-started job: the lease is dropped so the reaper does not
// later report a session that never ran as lost, and the row is marked errored.
func (r *runnerSessionStarter) abandon(ctx context.Context, sessionID string) {
	r.releaseLease(ctx, sessionID)
	if sess, err := r.store.GetSession(ctx, sessionID); err == nil && sess != nil {
		sess.Status = "error"
		sess.LeaseExpiresAt = agentdb.SessionLeaseUnset
		_, _ = r.store.UpdateSession(ctx, sess)
	}
}

func (r *runnerSessionStarter) releaseLease(ctx context.Context, sessionID string) {
	if r.leases == nil {
		return
	}
	if err := r.leases.ReleaseSessionLease(ctx, sessionID); err != nil {
		r.logf("[dispatch] session %s: could not release lease: %v", sessionID, err)
	}
}

// leaseWriter is the sink SendMessage streams into: agentd discards the bytes
// (the pipeline persists the turn) but every flush is proof the sandbox is
// alive, which is exactly what §8.4 step 4 renews the lease on.
func (r *runnerSessionStarter) leaseWriter(ctx context.Context, sessionID string) io.Writer {
	if r.leases == nil {
		return io.Discard
	}
	return &leaseRenewingWriter{
		ctx:       ctx,
		leases:    r.leases,
		sessionID: sessionID,
		now:       r.clock,
		logf:      r.logf,
	}
}

type leaseRenewingWriter struct {
	ctx       context.Context
	leases    leaseStore
	sessionID string
	now       func() time.Time
	last      time.Time
	logf      func(format string, v ...any)
}

func (w *leaseRenewingWriter) Write(p []byte) (int, error) {
	now := w.now()
	if now.Sub(w.last) >= sessionLeaseRenewInterval {
		w.last = now
		if err := w.leases.RenewSessionLease(w.ctx, w.sessionID, now.Add(sessionLeaseTTL).Unix()); err != nil {
			// Not fatal to the turn: the worst case is the reaper deciding a live
			// job is lost, which is why the TTL is generous.
			w.logf("[dispatch] session %s: could not renew lease: %v", w.sessionID, err)
		}
	}
	return len(p), nil
}

// newSessionID mints a session id. A job session must be indistinguishable from
// a chat session everywhere downstream — same table, same UI, same permalink —
// so it is just a uuid.
func newSessionID() string { return uuid.New().String() }
