package main

// scheduler.go — cron inside agentd (spec §8.6).
//
// A loop, not a daemon: it lives in the same process as the router because we
// deleted the last standalone daemon on purpose. Each tick it evaluates ONE
// wall-clock minute in the stack-local zone, fires the enabled schedules that
// match it, and drains whatever queued behind a busy worker.
//
// The four rules that make it boring, in the order they bite:
//
//  1. SKIP MISSED. A tick evaluates the CURRENT minute only. If agentd was down
//     for an hour, those sixty minutes are never evaluated — a tweet-writer must
//     not wake to a backlog of stale mornings (§8.6). There is no catch-up
//     window, no replay, and the "Deferred" list says there never will be.
//  2. IDEMPOTENT. Each firing claims `(schedule_id, scheduled_for)` before it
//     does anything. A crash/retry, a duplicated tick, or a second agentd
//     re-claims the same occurrence and loses.
//  3. SAME GATE AS THE ROUTER. The firing becomes an ordinary `pending`
//     delivery handed to dispatch.go, so a firing for a worker already at
//     `max_instances` queues instead of starting a second instance (§8.4 step 7)
//     — and the per-project concurrency cap applies to firings too.
//  4. A MISSING WORKER DISABLES THE SCHEDULE, loudly, with the reason in the
//     config log (§8.6). Never a silent retry every minute forever.
//  5. AND NEITHER DOES ANYTHING ELSE THAT CANNOT POSSIBLY SUCCEED. Rule 4 was
//     written for one cause; the harm is the pattern. 53 abandoned `* * * * *`
//     rows, none of which could provision, held every host port between them
//     and made the whole stack unable to start anything until a human deleted
//     them. So a schedule whose firings repeatedly fail TO START A JOB is
//     disabled too, after ScheduleMaxProvisionFailures consecutive attempts,
//     by the same call and into the same log.
//
//     The line that rule must not cross: a job that RAN and failed is a
//     legitimate outcome and resets nothing and counts nothing here. A worker
//     whose jobs keep failing is exactly what §8.7's self-improvement loop
//     exists to repair, and retiring its schedule would silence the loop. The
//     distinction is structural, not a judgement call: the scheduler only ever
//     sees whether the GATE started a session, and the turn itself runs
//     detached long after Dispatch has returned (see dispatch.go).
//
// Timezone: the whole stack evaluates in one location (TZ on agentd, default
// UTC — §8.6). DST needs no special case because occurrences are keyed on the
// wall clock: see agentdb.ScheduleFiring.

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// schedulerStore is the narrow slice of *agentdb.Store the loop needs, so the
// tick logic is testable with no database.
type schedulerStore interface {
	ListEnabledSchedules(ctx context.Context) ([]*agentdb.Schedule, error)
	GetWorker(ctx context.Context, project, name string) (*agentdb.Worker, error)
	DisableSchedule(ctx context.Context, project, id string, cw agentdb.ConfigWrite) (*agentdb.Schedule, error)
	// The consecutive-provision-failure streak. Runtime state, deliberately not
	// a config event — see the note above these methods in agentdb/schedules.go.
	NoteScheduleProvisionFailure(ctx context.Context, project, id, reason string) (int, error)
	ClearScheduleProvisionFailures(ctx context.Context, project, id string) error
	ClaimFiring(ctx context.Context, f *agentdb.ScheduleFiring) (*agentdb.ScheduleFiring, bool, error)
	StampFiringEvent(ctx context.Context, firingID, eventID string) error
	CreateProjectEvent(ctx context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error)
	EnsureDelivery(ctx context.Context, d *agentdb.EventDelivery) (*agentdb.EventDelivery, bool, error)
}

var _ schedulerStore = (*agentdb.Store)(nil)

// jobDispatcher is the shared gated dispatch point (dispatch.go). The scheduler
// holds it as an interface for exactly one reason: so its tests can assert "this
// firing was handed to the gate" without a Runner. The router (E3) uses the same
// concrete *dispatcher.
type jobDispatcher interface {
	// Dispatch is what the router calls: it wants the outcome and nothing else.
	Dispatch(ctx context.Context, delivery *agentdb.EventDelivery) (dispatchOutcome, error)
	// DispatchWithReason is what the SCHEDULER calls: a schedule about to be
	// retired for never starting must record WHY, and the reason is otherwise
	// only ever logged (§8.4's delivery tuple has no reason column).
	DispatchWithReason(ctx context.Context, delivery *agentdb.EventDelivery) (dispatchOutcome, string, error)
	DrainPending(ctx context.Context, project string) (int, error)
}

// schedulerPollInterval is how often the loop wakes. It is deliberately shorter
// than a minute: the tick evaluates whichever minute it is in and remembers it,
// so waking often absorbs scheduling jitter WITHOUT replaying anything — a
// minute is evaluated at most once and never retroactively.
const schedulerPollInterval = 10 * time.Second

// scheduler is the loop. Construct with newScheduler and drive with Run (or
// call Tick directly, which is what the tests do).
type scheduler struct {
	store      schedulerStore
	dispatcher jobDispatcher
	loc        *time.Location
	now        func() time.Time
	logf       func(format string, v ...any)

	// lastMinute is the wall-clock minute already evaluated. It is what makes a
	// tick idempotent in-process; the firing table makes it idempotent across
	// processes.
	lastMinute string
}

type schedulerConfig struct {
	Store      schedulerStore
	Dispatcher jobDispatcher
	// Location is the stack-local zone (§8.6). nil → time.Local, which honours
	// the TZ environment variable and defaults to UTC in the shipped image.
	Location *time.Location
	Now      func() time.Time
	Logf     func(format string, v ...any)
}

func newScheduler(cfg schedulerConfig) *scheduler {
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
	return &scheduler{store: cfg.Store, dispatcher: cfg.Dispatcher, loc: loc, now: now, logf: logf}
}

// Run drives the loop until ctx is cancelled.
func (s *scheduler) Run(ctx context.Context) {
	t := time.NewTicker(schedulerPollInterval)
	defer t.Stop()
	s.logf("[scheduler] running (zone=%s, poll=%s)", s.loc, schedulerPollInterval)
	for {
		select {
		case <-ctx.Done():
			s.logf("[scheduler] stopped")
			return
		case <-t.C:
			if err := s.Tick(ctx); err != nil {
				s.logf("[scheduler] tick: %v", err)
			}
		}
	}
}

// Tick evaluates the current wall-clock minute exactly once and then drains
// whatever is queued. Calling it twice within a minute does nothing the second
// time; calling it after a gap evaluates only the minute it is in — the missed
// ones are gone, by design (§8.6).
func (s *scheduler) Tick(ctx context.Context) error {
	minute := s.now().In(s.loc).Truncate(time.Minute)
	key := agentdb.OccurrenceKey(minute)
	if key == s.lastMinute {
		return nil
	}
	s.lastMinute = key

	schedules, err := s.store.ListEnabledSchedules(ctx)
	if err != nil {
		return err
	}
	projects := map[string]bool{}
	for _, sch := range schedules {
		projects[sch.Project] = true
		expr, err := agentdb.ParseCron(sch.Cron)
		if err != nil {
			// The store validates on write, so this means the row was written
			// around the store. Disable rather than re-parse it every minute.
			s.logf("[scheduler] schedule %s/%s has an unparseable cron %q: disabling (%v)",
				sch.Project, sch.ID, sch.Cron, err)
			s.disable(ctx, sch, "cron expression is unparseable: "+err.Error())
			continue
		}
		if !expr.Matches(minute) {
			continue
		}
		if err := s.fire(ctx, sch, minute); err != nil {
			s.logf("[scheduler] schedule %s/%s: %v", sch.Project, sch.ID, err)
		}
	}

	// Deliveries that queued behind a busy worker (this tick or an earlier one)
	// get their turn here — the same drain the router performs, through the same
	// gate (§8.4 step 7).
	for project := range projects {
		if _, err := s.dispatcher.DrainPending(ctx, project); err != nil {
			s.logf("[scheduler] drain %s: %v", project, err)
		}
	}
	return nil
}

// fire turns one due schedule into one job, idempotently.
func (s *scheduler) fire(ctx context.Context, sch *agentdb.Schedule, minute time.Time) error {
	// §8.6: a due schedule whose worker no longer exists is disabled and logged.
	// Checked BEFORE claiming the occurrence so re-enabling the schedule after
	// re-hiring the worker does not find its minute already spent.
	worker, err := s.store.GetWorker(ctx, sch.Project, sch.Worker)
	if err != nil {
		s.logf("[scheduler] schedule %s/%s targets worker %q which no longer exists: disabling",
			sch.Project, sch.ID, sch.Worker)
		s.disable(ctx, sch, "worker "+sch.Worker+" no longer exists")
		return nil
	}

	firing, claimed, err := s.store.ClaimFiring(ctx, &agentdb.ScheduleFiring{
		ScheduleID:   sch.ID,
		Project:      sch.Project,
		ScheduledFor: agentdb.OccurrenceKey(minute),
	})
	if err != nil {
		return err
	}
	if !claimed {
		// Someone already fired this occurrence. Nothing to do — that is the
		// whole guarantee (§8.6).
		return nil
	}

	// The instruction the trigger delivers becomes the event text (§8.6). The
	// envelope is core's: {source: "schedule", depth: 0}.
	event, err := s.store.CreateProjectEvent(ctx, &agentdb.ProjectEvent{
		Project:    sch.Project,
		Type:       agentdb.EventTypeScheduleFired,
		Text:       sch.Input,
		OccurredAt: minute.Unix(),
		Envelope: agentdb.EventEnvelope{
			Source: agentdb.EventSourceSchedule,
			Depth:  0,
		},
	})
	if err != nil {
		return err
	}
	if err := s.store.StampFiringEvent(ctx, firing.ID, event.ID); err != nil {
		s.logf("[scheduler] firing %s: could not stamp event id: %v", firing.ID, err)
	}

	// A firing is an ordinary delivery. It carries the schedule id in BOTH
	// subscription_id and schedule_id: the former keeps the existing
	// (event_id, subscription_id) idempotency index working for schedule-fired
	// rows too, the latter says the id names a schedule and not a subscription.
	delivery, _, err := s.store.EnsureDelivery(ctx, &agentdb.EventDelivery{
		Project:        sch.Project,
		EventID:        event.ID,
		SubscriptionID: sch.ID,
		ScheduleID:     sch.ID,
		Worker:         worker.Name,
		Status:         agentdb.DeliveryPending,
	})
	if err != nil {
		return err
	}

	// …and it goes through the SAME gate as an event-matched delivery, so a
	// worker already at max_instances queues rather than doubling up.
	outcome, reason, err := s.dispatcher.DispatchWithReason(ctx, delivery)
	if err != nil {
		// An infrastructure error from the gate (a failed count, unreadable
		// settings). Deliberately NOT counted: it says the database hiccuped,
		// not that this schedule is broken, and the streak must only ever be
		// grown by evidence about the schedule itself.
		return err
	}
	s.logf("[scheduler] %s/%s fired %s → %s", sch.Project, sch.ID, agentdb.OccurrenceKey(minute), outcome)
	switch outcome {
	case dispatchStarted:
		// A session exists. That — and only that — is this schedule working.
		// The turn runs detached from here, so whether the JOB succeeds or
		// fails is invisible to the streak by construction (§8.7).
		s.clearProvisionFailures(ctx, sch)
	case dispatchFailed:
		// The gate could not start a job at all: the worker is gone or
		// disabled, composition refused, or the session would not provision.
		s.noteProvisionFailure(ctx, sch, reason)
	case dispatchQueued, dispatchSkipped:
		// Queued is the capacity gate working as designed and skipped is
		// somebody else's firing — neither is a failure, and neither is proof
		// of health. Leave the streak alone.
	}
	return nil
}

// noteProvisionFailure grows the streak and, at the ceiling, retires the
// schedule the way §8.6 retires one whose worker is gone.
func (s *scheduler) noteProvisionFailure(ctx context.Context, sch *agentdb.Schedule, reason string) {
	if reason == "" {
		reason = "the job could not be started"
	}
	n, err := s.store.NoteScheduleProvisionFailure(ctx, sch.Project, sch.ID, reason)
	if err != nil {
		s.logf("[scheduler] schedule %s/%s: could not record a provision failure: %v",
			sch.Project, sch.ID, err)
		return
	}
	if n < agentdb.ScheduleMaxProvisionFailures {
		// Loud on the way up, not only at the end: an operator watching the log
		// sees the streak building and can act before anything is switched off.
		s.logf("[scheduler] schedule %s/%s could not provision a job (%d/%d consecutive): %s",
			sch.Project, sch.ID, n, agentdb.ScheduleMaxProvisionFailures, reason)
		return
	}
	s.logf("[scheduler] schedule %s/%s failed to provision %d times running: DISABLING it so it stops "+
		"retrying every %s and starving its neighbours. Last reason: %s",
		sch.Project, sch.ID, n, sch.Cron, reason)
	s.disable(ctx, sch, fmt.Sprintf("%d consecutive firings could not start a job; last reason: %s", n, reason))
}

// clearProvisionFailures resets the streak, and only writes when there is one to
// reset — the healthy path is by far the common one and must stay a pure read.
func (s *scheduler) clearProvisionFailures(ctx context.Context, sch *agentdb.Schedule) {
	if sch.ProvisionFailures == 0 {
		return
	}
	if err := s.store.ClearScheduleProvisionFailures(ctx, sch.Project, sch.ID); err != nil {
		s.logf("[scheduler] schedule %s/%s: could not clear the provision-failure streak: %v",
			sch.Project, sch.ID, err)
		return
	}
	s.logf("[scheduler] schedule %s/%s started a job again — provision-failure streak reset (was %d)",
		sch.Project, sch.ID, sch.ProvisionFailures)
}

// disable switches a schedule off, recording the reason in the config log.
func (s *scheduler) disable(ctx context.Context, sch *agentdb.Schedule, reason string) {
	if _, err := s.store.DisableSchedule(ctx, sch.Project, sch.ID, agentdb.ConfigWrite{
		Rationale: "disabled by the scheduler: " + reason,
	}); err != nil {
		s.logf("[scheduler] could not disable schedule %s/%s: %v", sch.Project, sch.ID, err)
	}
}
