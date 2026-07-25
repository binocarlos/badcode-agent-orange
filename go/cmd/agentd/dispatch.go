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
	"fmt"
	"io"
	"log"

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
}

// The concrete store must always satisfy the seam.
var _ dispatchStore = (*agentdb.Store)(nil)

// startJobInput is a composed job ready to run.
type startJobInput struct {
	Project string
	Worker  *agentdb.Worker
	Event   *agentdb.ProjectEvent
	Job     *agentkit.ComposedJob
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
	// (memory/management/image tools — D3, E4, I2 fill it); DefaultImage is the
	// global Policy.BaseImage; Images resolves a worker's image pointer and stays
	// nil until I4 binds the §13 catalogue's Resolve. Briefing sections are C4's
	// and are not injected yet.
	coreMCP      agentdb.MCPServers
	defaultImage string
	images       agentkit.ImageResolver

	logf func(format string, v ...any)
}

// dispatcherConfig is what main() supplies.
type dispatcherConfig struct {
	Store        dispatchStore
	Starter      sessionStarter
	CoreMCP      agentdb.MCPServers
	DefaultImage string
	Images       agentkit.ImageResolver
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
	if delivery == nil {
		return dispatchSkipped, fmt.Errorf("dispatch: delivery is required")
	}
	if delivery.Status != agentdb.DeliveryPending {
		// Already claimed by another pass or another process. At-least-once
		// delivery means a duplicate attempt must be a no-op, not a second job.
		return dispatchSkipped, nil
	}
	if delivery.Worker == "" {
		d.fail(ctx, delivery, "delivery carries no worker")
		return dispatchFailed, fmt.Errorf("dispatch: delivery %s carries no worker", delivery.ID)
	}

	worker, err := d.store.GetWorker(ctx, delivery.Project, delivery.Worker)
	if err != nil {
		// A worker that has been retired cannot run: fail the delivery loudly
		// rather than retrying it every poll forever. (For a SCHEDULE firing the
		// scheduler additionally disables the schedule — §8.6.)
		d.fail(ctx, delivery, fmt.Sprintf("worker %q: %v", delivery.Worker, err))
		return dispatchFailed, nil
	}
	if !worker.Enabled {
		d.fail(ctx, delivery, fmt.Sprintf("worker %q is disabled", worker.Name))
		return dispatchFailed, nil
	}

	settings, err := d.store.GetProjectSettings(ctx, delivery.Project)
	if err != nil {
		return dispatchSkipped, fmt.Errorf("dispatch: project settings: %w", err)
	}

	// §8.4 step 3 — the per-project concurrency cap, shared by router and
	// scheduler. (E3 adds the §8.4 step 6 daily-token budget check right here:
	// one more reason to leave the delivery `pending`, in this one place.)
	active, err := d.store.CountActiveDeliveries(ctx, delivery.Project)
	if err != nil {
		return dispatchSkipped, fmt.Errorf("dispatch: count active: %w", err)
	}
	if settings.MaxConcurrentJobs > 0 && active >= int64(settings.MaxConcurrentJobs) {
		return dispatchQueued, nil
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
		return dispatchSkipped, fmt.Errorf("dispatch: count worker instances: %w", err)
	}
	if running >= int64(max) {
		return dispatchQueued, nil
	}

	// Compose (§6.2) — the identical path for every trigger.
	var event *agentdb.ProjectEvent
	if delivery.EventID != "" {
		event, err = d.store.GetProjectEvent(ctx, delivery.Project, delivery.EventID)
		if err != nil {
			d.fail(ctx, delivery, fmt.Sprintf("event %s: %v", delivery.EventID, err))
			return dispatchFailed, nil
		}
	}
	job, err := agentkit.ComposeJob(ctx, agentkit.ComposeJobInput{
		Project:       delivery.Project,
		Worker:        worker,
		Settings:      settings,
		Event:         event,
		CoreMCP:       d.coreMCP,
		DefaultImage:  d.defaultImage,
		ImageResolver: d.images,
	})
	if err != nil {
		// Composition refuses loudly (an unresolvable image pointer, a malformed
		// stored MCP config). That is a job failure, never a silent fallback.
		d.fail(ctx, delivery, fmt.Sprintf("compose: %v", err))
		return dispatchFailed, nil
	}

	if d.starter == nil {
		return dispatchSkipped, fmt.Errorf("dispatch: no session starter configured")
	}
	sessionID, err := d.starter.StartJob(ctx, startJobInput{
		Project: delivery.Project,
		Worker:  worker,
		Event:   event,
		Job:     job,
	})
	if err != nil {
		d.fail(ctx, delivery, fmt.Sprintf("start job: %v", err))
		return dispatchFailed, nil
	}

	if _, err := d.store.UpdateDeliveryStatus(ctx, delivery.Project, delivery.ID, agentdb.DeliveryStatusUpdate{
		Status:    agentdb.DeliveryRunning,
		SessionID: sessionID,
	}); err != nil {
		return dispatchStarted, fmt.Errorf("dispatch: mark running: %w", err)
	}
	return dispatchStarted, nil
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
func (d *dispatcher) fail(ctx context.Context, delivery *agentdb.EventDelivery, reason string) {
	d.logf("[dispatch] delivery %s (%s/%s) failed: %s", delivery.ID, delivery.Project, delivery.Worker, reason)
	if _, err := d.store.UpdateDeliveryStatus(ctx, delivery.Project, delivery.ID, agentdb.DeliveryStatusUpdate{
		Status: agentdb.DeliveryFailed,
	}); err != nil {
		d.logf("[dispatch] delivery %s: could not mark failed: %v", delivery.ID, err)
	}
}

// ── The real session starter ────────────────────────────────────────────────

// runnerSessionStarter creates the session row, provisions it through the
// Runner, and sends the composed first message. It is the production
// sessionStarter; E3 should reuse it rather than growing a second one.
type runnerSessionStarter struct {
	runner agentkit.Runner
	store  agentkit.RunnerStore
	// newID mints session ids; overridable in tests.
	newID func() string
	logf  func(format string, v ...any)
}

func newRunnerSessionStarter(runner agentkit.Runner, store agentkit.RunnerStore) *runnerSessionStarter {
	return &runnerSessionStarter{runner: runner, store: store, logf: log.Printf}
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
	// transcript is tied to the exact prompt that produced it (§6.2, §6.5).
	if _, err := r.store.UpdateSession(ctx, &agentdb.Session{
		ID:             sessionID,
		Customer:       in.Project,
		WorkflowID:     "agent",
		Status:         "creating",
		Worker:         in.Worker.Name,
		ComposedPrompt: in.Job.SystemPrompt,
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
		if sess, getErr := r.store.GetSession(ctx, sessionID); getErr == nil && sess != nil {
			sess.Status = "error"
			_, _ = r.store.UpdateSession(ctx, sess)
		}
		return "", fmt.Errorf("create session: %w", err)
	}
	if sess, err := r.store.GetSession(ctx, sessionID); err == nil && sess != nil {
		sess.Status = "running"
		_, _ = r.store.UpdateSession(ctx, sess)
	}

	// The rendered event is the job's first user message (§6.2 step 4). A job
	// with no event (a bare "run this worker") simply starts idle.
	if in.Job.FirstMessage != "" {
		if err := r.runner.SendMessage(ctx, agentkit.SessionRef{SessionID: sessionID}, agentkit.SendMessageRequest{
			Content:  in.Job.FirstMessage,
			Customer: in.Project,
		}, io.Discard); err != nil {
			// The session exists and the delivery is running; a failed first turn
			// is the job failing, which E2's worker.failed emitter reports.
			return sessionID, fmt.Errorf("send first message: %w", err)
		}
	}
	return sessionID, nil
}

// newSessionID mints a session id. A job session must be indistinguishable from
// a chat session everywhere downstream — same table, same UI, same permalink —
// so it is just a uuid.
func newSessionID() string { return uuid.New().String() }
