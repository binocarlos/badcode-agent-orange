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
//  1. SKIP MISSED, BUT SAY SO. A tick evaluates the CURRENT minute only. If
//     agentd was down for an hour, those sixty minutes are never RUN — a
//     tweet-writer must not wake to a backlog of stale mornings (§8.6). There is
//     no replay and there is deliberately no bounded catch-up either: see
//     "What the catch-up does and does not do" below.
//
//     What changed with RD11 is that the sixty minutes are no longer INVISIBLE.
//     Each schedule carries a watermark (`last_evaluated`, migration 041), and a
//     tick that finds a gap records the occurrences it did not evaluate as
//     `missed` firings and emits ONE `schedule.missed` event per gap. Before
//     that, an outage left no firing row, no event, no delivery and nothing at
//     all — "my 9am job never ran" was indistinguishable from "you never
//     scheduled a 9am job", and the difference is the entire user question.
//  2. IDEMPOTENT. Each firing claims `(schedule_id, scheduled_for)` before it
//     does anything. A crash/retry, a duplicated tick, or a second agentd
//     re-claims the same occurrence and loses.
//  3. SAME GATE AS THE ROUTER. The firing becomes an ordinary `pending`
//     delivery handed to dispatch.go, so a firing for a worker already at
//     `max_instances` queues instead of starting a second instance (§8.4 step 7)
//     — and the per-project concurrency cap applies to firings too.
//  4. A MISSING WORKER DISABLES THE SCHEDULE, loudly, with the reason in the
//     config log (§8.6). Never a silent retry every minute forever. "Missing"
//     means agentdb.ErrWorkerNotFound and nothing else — a database that cannot
//     answer is not evidence about the worker, and disabling on it would write a
//     durable, permanent, and false reason into the config log (RD1).
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
//
// # What the catch-up does and does not do (RD11 — the decision, on the record)
//
// It does NOT run anything. An agentd that has been down for an hour comes back
// and starts zero backlog jobs; sixty stale instructions arriving at once is
// worse than sixty that did not arrive, and it is worse in a way the operator
// cannot undo — outbound actions (post the tweet, send the mail) do not have an
// undo. §8.6 settled that and this change does not reopen it.
//
// A BOUNDED catch-up ("run the most recent missed occurrence") was considered
// and deliberately rejected, because "most recent" is not a property of the
// clock, it is a property of the work: re-running last night's backup is
// obviously right, re-sending last night's "good morning" is obviously wrong,
// and the schedule row does not say which kind it is. Choosing for the user
// would silently be wrong half the time; the honest move is to tell them what
// they missed and let a human — or a worker subscribed to `schedule.missed` —
// decide. If a future version wants replay, it wants a per-schedule policy
// field, not a global guess.
//
// What it does instead, per schedule, per tick:
//
//   - reads the watermark (`last_evaluated`), the last wall-clock minute any
//     tick evaluated this row;
//   - walks the minutes strictly between the watermark and now, matching the
//     cron, to find occurrences NOBODY evaluated;
//   - claims each one as a `missed` firing (the same UNIQUE index that makes a
//     real firing exactly-once, so two agentds recovering together cannot both
//     report the same minute);
//   - emits ONE `schedule.missed` event naming the count and the range — one,
//     not sixty, because a feed with sixty notices in it is a second outage;
//   - advances the watermark, whether or not anything matched. A tick that
//     found nothing due is evidence too: it is what makes the NEXT gap
//     measurable.
//
// An empty watermark (a schedule created while agentd was down, or one that
// predates migration 041) reports nothing: we do not know what happened before
// we were first looking, and inventing a missed-occurrence report for a
// schedule created five minutes ago would be its own false claim.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
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
	CreateProjectEvent(ctx context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error)
	// RecordFiringJob writes the event, its delivery and the firing's event
	// stamp in ONE transaction (RD11): a kill between the event and the
	// delivery used to leave a `schedule.fired` notice in the feed for a job
	// that never ran, with the occurrence permanently consumed.
	RecordFiringJob(ctx context.Context, firingID string, ev *agentdb.ProjectEvent, d *agentdb.EventDelivery) (*agentdb.ProjectEvent, *agentdb.EventDelivery, error)
	// MarkFiringMissed flips a claimed occurrence that produced nothing.
	MarkFiringMissed(ctx context.Context, firingID string) error
	// NoteScheduleEvaluated advances the per-schedule watermark (RD11).
	NoteScheduleEvaluated(ctx context.Context, project, id, occurrence string) error
	// Session-mode schedules resolve their target by NAME (T9). The name is the
	// only handle the schedule holds, which is the point: an embedding
	// application never has to store a uuid.
	GetSessionByName(ctx context.Context, customer, name string) (*agentdb.Session, error)
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

// sessionMessenger is the Runner seam a SESSION-mode schedule needs, and
// deliberately nothing else. Two methods, both read-or-message: the scheduler
// must not be able to create, snapshot, restore-by-hand or destroy a session,
// and holding the whole *Runner here would put all of that one keystroke away
// from a loop that runs unattended every ten seconds.
//
// It is also what makes the tests above possible without a container runtime.
// agentkit.Runner satisfies it (asserted below).
type sessionMessenger interface {
	// SendMessage runs one turn. Its first act is ensureRunning
	// (go/runner.go:857) → restoreToWorker, which materialises the snapshot AND
	// rehydrates the conversation, so "wake an archived session" needs no code
	// here at all.
	SendMessage(ctx context.Context, ref agentkit.SessionRef, msg agentkit.SendMessageRequest, w agentkit.Writer) error
	// Status is the busy check. SendMessage does NOT refuse a session with a
	// turn already in flight, so asking is the only way to skip rather than
	// stack up behind it.
	Status(ctx context.Context, ref agentkit.SessionRef) (*agentkit.SessionStatus, error)
}

var _ sessionMessenger = (agentkit.Runner)(nil)

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
	// sessions is the session-mode seam (T9). nil on a deployment with no
	// Runner wired, where session schedules simply cannot fire — they are
	// refused loudly per tick rather than silently doing nothing.
	sessions sessionMessenger
	loc      *time.Location
	now      func() time.Time
	// spawn runs a turn off the tick goroutine. A turn takes as long as a model
	// takes; running it inline would stall every other schedule in the stack
	// behind it. Injectable so the tests can make it synchronous — the same
	// trick dispatch.go's runnerSessionStarter uses.
	spawn func(func())
	logf  func(format string, v ...any)

	// lastMinute is the wall-clock minute already evaluated. It is what makes a
	// tick idempotent in-process; the firing table makes it idempotent across
	// processes.
	lastMinute string
}

type schedulerConfig struct {
	Store      schedulerStore
	Dispatcher jobDispatcher
	// Sessions is the Runner, narrowed (see sessionMessenger). Optional: without
	// it only worker schedules can fire.
	Sessions sessionMessenger
	// Location is the stack-local zone (§8.6). nil → time.Local, which honours
	// the TZ environment variable and defaults to UTC in the shipped image.
	Location *time.Location
	Now      func() time.Time
	Spawn    func(func())
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
	spawn := cfg.Spawn
	if spawn == nil {
		spawn = func(fn func()) { go fn() }
	}
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	return &scheduler{
		store:      cfg.Store,
		dispatcher: cfg.Dispatcher,
		sessions:   cfg.Sessions,
		loc:        loc,
		now:        now,
		spawn:      spawn,
		logf:       logf,
	}
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
		// Before this minute, and always — the gap is chronologically earlier
		// than the firing, and a schedule that does NOT match this minute still
		// has to report the ones it missed while nobody was looking.
		s.catchUp(ctx, sch, expr, minute)
		if expr.Matches(minute) {
			if err := s.fire(ctx, sch, minute); err != nil {
				s.logf("[scheduler] schedule %s/%s: %v", sch.Project, sch.ID, err)
			}
		}
		// Unconditional, and after the firing: this minute has now been
		// evaluated whatever came of it. A tick that errored is not retried
		// (nothing ever retried a minute), so leaving the watermark behind would
		// only make the NEXT tick report this minute as missed — a false report
		// about a minute we did look at.
		if err := s.store.NoteScheduleEvaluated(ctx, sch.Project, sch.ID, key); err != nil {
			s.logf("[scheduler] schedule %s/%s: could not advance the watermark to %s: %v",
				sch.Project, sch.ID, key, err)
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

// fire turns one due schedule into one job, idempotently — or, in session mode,
// into one message to an existing session.
func (s *scheduler) fire(ctx context.Context, sch *agentdb.Schedule, minute time.Time) error {
	// The two modes are mutually exclusive by store validation
	// (agentdb.validateSchedule), so this is a total branch and not a
	// precedence rule.
	if sch.TargetSession != "" {
		return s.fireSession(ctx, sch, minute)
	}

	// §8.6: a due schedule whose worker no longer exists is disabled and logged.
	// Checked BEFORE claiming the occurrence so re-enabling the schedule after
	// re-hiring the worker does not find its minute already spent.
	//
	// Only ErrWorkerNotFound means "no longer exists". Any other error is the
	// database failing to answer, and MUST NOT be read as an absent worker: this
	// check runs before ClaimFiring, so it is upstream of the
	// ScheduleMaxProvisionFailures streak and one blip during the due second
	// would disable the schedule permanently — with a config-log reason naming a
	// worker that is sitting right there. Log and return; the next tick retries.
	worker, err := s.store.GetWorker(ctx, sch.Project, sch.Worker)
	switch {
	case errors.Is(err, agentdb.ErrWorkerNotFound):
		s.logf("[scheduler] schedule %s/%s targets worker %q which no longer exists: disabling",
			sch.Project, sch.ID, sch.Worker)
		s.disable(ctx, sch, "worker "+sch.Worker+" no longer exists")
		return nil
	case err != nil:
		return fmt.Errorf("read worker %q: %w", sch.Worker, err)
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
	//
	// The event, the delivery and the firing's event stamp land in ONE
	// transaction (RD11). They used to be three sequential writes, and a kill in
	// between left a `schedule.fired` event in the user's feed for a job that
	// never ran and never would — the occurrence was already spent, so no later
	// tick would try again. Now the occurrence either produced all of it or none
	// of it, and "none of it" is a state with a name: the firing row is claimed
	// with no event id, which is exactly what noteFiringProducedNothing below
	// and the catch-up sweep report as missed.
	//
	// A firing is an ordinary delivery. It carries the schedule id in BOTH
	// subscription_id and schedule_id: the former keeps the existing
	// (event_id, subscription_id) idempotency index working for schedule-fired
	// rows too, the latter says the id names a schedule and not a subscription.
	_, delivery, err := s.store.RecordFiringJob(ctx, firing.ID,
		&agentdb.ProjectEvent{
			Project:    sch.Project,
			Type:       agentdb.EventTypeScheduleFired,
			Text:       sch.Input,
			OccurredAt: minute.Unix(),
			Envelope: agentdb.EventEnvelope{
				Source: agentdb.EventSourceSchedule,
				Depth:  0,
			},
		},
		&agentdb.EventDelivery{
			Project:        sch.Project,
			SubscriptionID: sch.ID,
			ScheduleID:     sch.ID,
			Worker:         worker.Name,
			Status:         agentdb.DeliveryPending,
		})
	if err != nil {
		// The occurrence is spent and produced nothing. Say so now, while the
		// process is alive to say it — the catch-up sweep would only find this
		// row if a later gap happened to span it.
		s.noteFiringProducedNothing(ctx, sch, firing, minute, err)
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

// fireSession is the other mode (T9 of
// design/2026-08-06-embeddable-agent-orange.md): the firing does not start a
// job, it sends the schedule's Input to an EXISTING named session as its next
// message. Same conversation, woken on a cron.
//
// What this deliberately does NOT do is the whole worker path: no
// `schedule.fired` event, no delivery row, no composition, no capacity gate.
// There is nothing for the gate to decide — the session already exists, holds
// its own container, and can run exactly one turn at a time, which the busy
// check below enforces directly.
//
// WHAT A RESUMED SESSION DOES AND DOES NOT PICK UP, because it is easy to get
// backwards and the answer differs by session kind:
//
//   - A CHAT session (empty composed_prompt — everything created through
//     POST /agent/session) re-resolves its system prompt from the live provider
//     on EVERY turn (go/runner.go:1914-1941), so it does pick up edits to the
//     project prompt and to its worker's prompt between firings.
//   - A DISPATCHED JOB session persisted its ComposedPrompt at creation and
//     runs that same text forever.
//   - NEITHER refreshes its MCP tool set: the tools are fixed when the
//     container is provisioned, and a restore rebuilds the same container.
//   - NEITHER gains a briefing: briefings are built at composition time only,
//     so a chat session never has one at all.
//
// That last pair is why current state reaches a long-lived session through the
// memory tools at message time rather than through its prompt.
func (s *scheduler) fireSession(ctx context.Context, sch *agentdb.Schedule, minute time.Time) error {
	if s.sessions == nil {
		// No Runner wired (nothing in the shipped agentd, but the seam is
		// optional). Loud and per-tick rather than a silent no-op: a schedule
		// that looks enabled and never fires is the worst of both.
		return fmt.Errorf("schedule targets session %q but this deployment has no session runner wired",
			sch.TargetSession)
	}

	// Resolved BEFORE the occurrence is claimed, exactly as the worker branch
	// reads its worker first: a schedule disabled for a missing target must be
	// able to fire that same minute once the target is back and the schedule is
	// re-enabled.
	//
	// And, as there, only the not-found sentinel counts as "gone". An opaque
	// read error is the database failing to answer, and disabling on it would
	// write a permanent, false reason into the config log (RD1).
	sess, err := s.store.GetSessionByName(ctx, sch.Project, sch.TargetSession)
	switch {
	case errors.Is(err, agentdb.ErrSessionNotFound):
		s.logf("[scheduler] schedule %s/%s targets session %q which no longer exists: disabling",
			sch.Project, sch.ID, sch.TargetSession)
		s.disable(ctx, sch, "session "+sch.TargetSession+" no longer exists")
		return nil
	case err != nil:
		return fmt.Errorf("read session %q: %w", sch.TargetSession, err)
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
		return nil
	}
	// No StampFiringEvent: session mode produces no event, so the firing row's
	// event_id stays empty. It is still the idempotency record — the claim is
	// what stops a second agentd sending the same instruction twice.

	// The busy check. SendMessage does not refuse a session with a turn already
	// in flight, so without this a slow daily research turn would have tomorrow's
	// instruction posted on top of it. Skipped and NOT queued, deliberately: the
	// occurrence is spent, and a stale "good morning" delivered at 11am is worse
	// than one not delivered at all (§8.6's skip-missed posture, applied to the
	// session rather than to the clock).
	st, err := s.sessions.Status(ctx, agentkit.SessionRef{SessionID: sess.ID})
	if err != nil {
		// Cannot tell whether it is busy. Do not guess in the direction that
		// posts a second turn; count it, so a session that can never be
		// inspected retires the schedule rather than skipping forever in silence.
		s.noteProvisionFailure(ctx, sch, fmt.Sprintf("could not read the status of session %q: %v",
			sch.TargetSession, err))
		return nil
	}
	if st.ActiveQueryID != "" {
		s.logf("[scheduler] %s/%s fired %s → skipped: session %q is already running query %s "+
			"(firing %s recorded; nothing is queued)",
			sch.Project, sch.ID, agentdb.OccurrenceKey(minute), sch.TargetSession, st.ActiveQueryID, firing.ID)
		// Not a provision failure: a busy session is one that is working.
		return nil
	}

	s.logf("[scheduler] %s/%s fired %s → sending to session %q (%s)",
		sch.Project, sch.ID, agentdb.OccurrenceKey(minute), sch.TargetSession, sess.ID)

	// Detached, for the same reason dispatch.go detaches a job's first message:
	// a turn takes as long as the model takes, and the tick that started it must
	// not be able to cancel it or be blocked by it.
	turnCtx := context.WithoutCancel(ctx)
	s.spawn(func() {
		// io.Discard, not a lease writer: leases belong to dispatched jobs
		// (§8.4 step 4) and there is no delivery row here to reap.
		err := s.sessions.SendMessage(turnCtx, agentkit.SessionRef{SessionID: sess.ID}, agentkit.SendMessageRequest{
			Content:  sch.Input,
			Customer: sch.Project,
		}, io.Discard)
		if err != nil {
			// This is rule 5's case, not "a job ran and failed": SendMessage
			// returns an error when the turn could not be DELIVERED — the
			// snapshot would not restore, no host port was free, the sandbox
			// never answered. What the model then does with the message comes
			// back as events, not as an error here. So it counts, and a session
			// that can never be woken retires its schedule instead of retrying
			// every minute forever.
			s.logf("[scheduler] schedule %s/%s: could not deliver to session %q: %v",
				sch.Project, sch.ID, sch.TargetSession, err)
			s.noteProvisionFailure(turnCtx, sch, fmt.Sprintf("could not deliver to session %q: %v",
				sch.TargetSession, err))
			return
		}
		s.clearProvisionFailures(turnCtx, sch)
	})
	return nil
}

// ── The catch-up (RD11) ─────────────────────────────────────────────────────

const (
	// scheduleCatchUpWindow bounds how far back ONE catch-up looks. A week is
	// well past the point where a missed occurrence is still actionable, and the
	// bound is what stops a schedule that has been enabled-but-unevaluated for a
	// year from walking half a million minutes on the first tick after an
	// upgrade. Beyond it the report says so rather than pretending the older
	// occurrences did not exist.
	scheduleCatchUpWindow = 7 * 24 * time.Hour
	// scheduleMaxMissedRecorded caps the firing rows a single gap may write, so
	// a `* * * * *` schedule and a long outage cannot turn one tick into ten
	// thousand INSERTs. The MOST RECENT occurrences are the ones kept — they are
	// the ones an operator might still act on — and the event always names the
	// true total, so the cap shortens the evidence and never the count.
	scheduleMaxMissedRecorded = 60
)

// catchUp reports the occurrences of one schedule that nobody evaluated, and
// runs none of them. See the header note for why running them was rejected.
func (s *scheduler) catchUp(ctx context.Context, sch *agentdb.Schedule, expr *agentdb.CronExpr, minute time.Time) {
	watermark := strings.TrimSpace(sch.LastEvaluated)
	if watermark == "" {
		// Never evaluated: a schedule created while agentd was down, or one that
		// predates migration 041. We have no evidence about the past and must
		// not invent any — this tick becomes the first watermark.
		return
	}
	last, err := agentdb.ParseOccurrence(watermark, s.loc)
	if err != nil {
		s.logf("[scheduler] schedule %s/%s: unreadable watermark %q (%v) — treating this tick as the first",
			sch.Project, sch.ID, watermark, err)
		return
	}
	start := last.Add(time.Minute)
	if !start.Before(minute) {
		// The usual case by far: the previous tick was a minute ago.
		return
	}
	truncated := false
	if minute.Sub(start) > scheduleCatchUpWindow {
		start = minute.Add(-scheduleCatchUpWindow)
		truncated = true
	}

	// Walk the gap. `recent` keeps at most scheduleMaxMissedRecorded of them
	// while `total` counts them all: the difference between what we can afford
	// to write down and what actually happened.
	total := 0
	recent := make([]time.Time, 0, scheduleMaxMissedRecorded)
	for t := start; t.Before(minute); t = t.Add(time.Minute) {
		if !expr.Matches(t) {
			continue
		}
		total++
		if len(recent) == scheduleMaxMissedRecorded {
			recent = recent[1:]
		}
		recent = append(recent, t)
	}
	if total == 0 {
		s.logf("[scheduler] schedule %s/%s: %s..%s went unevaluated but nothing was due in it",
			sch.Project, sch.ID, watermark, agentdb.OccurrenceKey(minute))
		return
	}

	// Claiming is what makes the REPORT exactly-once, exactly as it makes a
	// firing exactly-once: two agentds coming back from the same outage cannot
	// both announce the same missed minute.
	newly := make([]string, 0, len(recent))
	for _, t := range recent {
		key := agentdb.OccurrenceKey(t)
		row, claimed, err := s.store.ClaimFiring(ctx, &agentdb.ScheduleFiring{
			ScheduleID:   sch.ID,
			Project:      sch.Project,
			ScheduledFor: key,
			Missed:       true,
		})
		if err != nil {
			s.logf("[scheduler] schedule %s/%s: could not record missed occurrence %s: %v",
				sch.Project, sch.ID, key, err)
			continue
		}
		if claimed {
			newly = append(newly, key)
			continue
		}
		// Somebody already owns this occurrence. Three ways that happens, and
		// only one of them is news:
		//   - it is already recorded as missed (a peer got there first);
		//   - it fired properly (worker mode: it has an event id; session mode:
		//     the row itself is the record, because that mode writes no event);
		//   - worker mode, claimed, NO event id — the process died between
		//     claiming the occurrence and writing the job. That is RD11's narrow
		//     window, and it is the one remnant that was invisible before.
		if row.Missed || sch.TargetSession != "" || row.EventID != "" {
			continue
		}
		if err := s.store.MarkFiringMissed(ctx, row.ID); err != nil {
			s.logf("[scheduler] schedule %s/%s: could not mark occurrence %s missed: %v",
				sch.Project, sch.ID, key, err)
			continue
		}
		newly = append(newly, key)
	}
	if len(newly) == 0 {
		// Every occurrence in the gap was already accounted for. Staying silent
		// here is what stops a second agentd double-announcing an outage.
		return
	}
	s.reportMissed(ctx, sch, newly, total, minute, s.missedNote(watermark, total, len(newly), truncated))
}

// missedNote is the human sentence that says how complete the report is. It is
// separate because the honest answer differs: usually "all of them", sometimes
// "the most recent N of M", and after a very long outage "and older ones we did
// not enumerate".
func (s *scheduler) missedNote(watermark string, total, recorded int, truncated bool) string {
	note := ""
	if recorded < total {
		note = fmt.Sprintf(" The %d most recent are recorded individually; the rest are counted but not listed.", recorded)
	}
	if truncated {
		note += fmt.Sprintf(" This schedule was last evaluated at %s, which is more than %d days ago —"+
			" occurrences older than that window are not included in the count.",
			watermark, int(scheduleCatchUpWindow/(24*time.Hour)))
	}
	return note
}

// reportMissed appends ONE `schedule.missed` event for a run of occurrences that
// did not happen. One event and not one per occurrence: sixty notices in a feed
// is a second outage, and the count is the part a person acts on.
func (s *scheduler) reportMissed(ctx context.Context, sch *agentdb.Schedule, keys []string, total int, minute time.Time, note string) {
	target := "worker " + sch.Worker
	if sch.TargetSession != "" {
		target = "session " + sch.TargetSession
	}
	span := keys[0]
	if len(keys) > 1 {
		span = keys[0] + " … " + keys[len(keys)-1]
	}
	text := fmt.Sprintf(
		"%d scheduled occurrence(s) of schedule %s (cron `%s`, %s) were missed: %s. "+
			"Nothing ran for them — a missed occurrence is recorded, never replayed, so no backlog of "+
			"stale work starts at once.%s The instruction they would have delivered was: %s",
		total, sch.ID, sch.Cron, target, span, note, strings.TrimSpace(sch.Input))
	if _, err := s.store.CreateProjectEvent(ctx, &agentdb.ProjectEvent{
		Project:    sch.Project,
		Type:       agentdb.EventTypeScheduleMissed,
		Text:       text,
		OccurredAt: minute.Unix(),
		Envelope: agentdb.EventEnvelope{
			Source: agentdb.EventSourceSchedule,
			Depth:  0,
		},
	}); err != nil {
		// The log is the fallback record. Not a provision failure: nothing about
		// this says the schedule cannot start a job.
		s.logf("[scheduler] schedule %s/%s: MISSED %d occurrence(s) (%s) and could not append the "+
			"schedule.missed event: %v", sch.Project, sch.ID, total, span, err)
		return
	}
	s.logf("[scheduler] schedule %s/%s: MISSED %d occurrence(s) (%s) — recorded, not replayed",
		sch.Project, sch.ID, total, span)
}

// noteFiringProducedNothing is the online half of the same honesty: the
// occurrence was claimed, the transactional job write failed, and the occurrence
// is now spent with nothing to show for it. Reported here rather than left for a
// future gap, because a gap only covers minutes nobody evaluated and this one
// was evaluated — by us, badly.
func (s *scheduler) noteFiringProducedNothing(ctx context.Context, sch *agentdb.Schedule, firing *agentdb.ScheduleFiring, minute time.Time, cause error) {
	if err := s.store.MarkFiringMissed(ctx, firing.ID); err != nil {
		s.logf("[scheduler] schedule %s/%s: could not mark firing %s missed: %v",
			sch.Project, sch.ID, firing.ID, err)
	}
	s.reportMissed(ctx, sch, []string{agentdb.OccurrenceKey(minute)}, 1, minute,
		fmt.Sprintf(" The occurrence was claimed but the job could not be written: %v.", cause))
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
