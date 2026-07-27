package main

// configchanged.go — the routable `config.changed` event (spec §15.4, §15.8;
// docs/product/09-config-log.md), work-plan item J3.
//
// # The shape of the thing
//
//	a mutation ──WithConfigEvent──▶ [ projection row + config_events row ]  ONE transaction
//	                                            │ commit
//	                                            ▼
//	                            Store's post-commit hook (agentdb)
//	                                            │
//	                                            ▼
//	                     configChangeEmitter ──▶ project_events{type: config.changed}
//
// Three properties the spec asks for, and where each one lives:
//
//  1. **After commit, never inside the transaction** (§15.4). A routed event
//     must not exist for a change that rolled back. This is structural: the
//     hook is called by `WithConfigEvent` only on the success path, so there is
//     no code here that could get it wrong and no caller that could forget.
//
//  2. **At-least-once with an idempotency guard on the config-event id**
//     (§15.4). The emitted project event's id is DERIVED from the config
//     event's id (a UUIDv5 under a fixed namespace), so a second emission of
//     the same record is the same row: the primary key refuses it. That is the
//     guard. `config_events.emitted_at` is only the watermark that tells the
//     repair sweep which records still need one — losing the watermark costs a
//     duplicate attempt, never a duplicate event.
//
//  3. **The envelope comes from the ACTING SESSION** (§15.8): `source:
//     "worker"` with the worker, its session and the acting job's depth + 1 for
//     a worker-made change, so §8.4's depth floor binds a config→worker→config
//     loop exactly as it binds any other; `source: "external"`, `depth: 0` for
//     a human editing through the UI or the API. Depth is resolved with the
//     same `agentkit.ResolveWorkerJob` the §8.2 emitters use — one stamping of
//     the envelope, not a second that can drift.
//
// # Why a hook rather than a return value
//
// The integration pass noted that the adopted store methods discard the
// committed `*ConfigEvent`, and proposed threading it back out through every
// signature. A hook on the seam is strictly better here: `WithConfigEvent` is
// already the single funnel every configuration mutation passes through
// (§15.4), so hanging emission off it makes "every mutation path produces
// exactly one event" true by construction, for the paths that exist today AND
// for the ones later tracks add. Threading sixteen signatures would instead
// have made the emission one more thing each new mutation can forget — the
// exact failure mode the conformance test exists to prevent — and would have
// rewritten files two other tracks are editing right now.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/google/uuid"
)

// configChangeStore is the narrow slice of *agentdb.Store the emitter needs.
// The first three methods are exactly agentkit.WorkerEventStore, which is what
// resolves the acting session's envelope.
type configChangeStore interface {
	GetSession(ctx context.Context, id string) (*agentdb.Session, error)
	SessionTriggerEvent(ctx context.Context, sessionID string) (*agentdb.ProjectEvent, error)
	CreateProjectEvent(ctx context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error)
	GetProjectEvent(ctx context.Context, project, id string) (*agentdb.ProjectEvent, error)
	MarkConfigEventEmitted(ctx context.Context, id string) error
	ListUnemittedConfigEvents(ctx context.Context, createdBefore int64, limit int) ([]*agentdb.ConfigEvent, error)
}

// configChangedNamespace is the UUIDv5 namespace the emitted event's id is
// derived under. It is a constant of the protocol: change it and every config
// event in the database becomes emittable a second time.
var configChangedNamespace = uuid.NewSHA1(uuid.NameSpaceURL,
	[]byte("https://agent-orange.badcode.dev/events/config.changed"))

// configChangedEventID is the idempotency guard (§15.4): one config-event id
// maps to exactly one project-event id, for ever, on every host.
func configChangedEventID(configEventID string) string {
	return uuid.NewSHA1(configChangedNamespace, []byte(configEventID)).String()
}

// Repair-sweep tuning. The grace window must comfortably exceed the inline
// hook's runtime (two queries and an insert) so the sweep repairs crashes
// rather than racing live mutations.
const (
	configChangeSweepInterval = 30 * time.Second
	configChangeSweepGrace    = 60 * time.Second
	configChangeSweepBatch    = 100
)

// configChangeEmitter turns committed config-log records into routable events.
type configChangeEmitter struct {
	store configChangeStore
	logf  func(string, ...any)
}

func newConfigChangeEmitter(store configChangeStore, logf func(string, ...any)) *configChangeEmitter {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &configChangeEmitter{store: store, logf: logf}
}

// Hook is the agentdb post-commit seam. It swallows errors on purpose: the
// projection row and the log row are already committed, so failing the caller
// now would report a change that did happen as one that did not. A failed
// emission stays unstamped and the sweep picks it up.
func (e *configChangeEmitter) Hook() agentdb.ConfigEventHook {
	return func(ctx context.Context, ev *agentdb.ConfigEvent) {
		if err := e.Emit(ctx, ev); err != nil {
			e.logf("[agentd] config.changed emit failed for config event %s (%s): %v — "+
				"the change IS committed; the repair sweep will retry", ev.ID, ev.Action, err)
		}
	}
}

// Emit appends the `config.changed` event for one committed config record and
// stamps its watermark. It is idempotent: calling it twice for the same record
// produces one event, whether the second call is a retry after a crash, a
// concurrent sweep, or both.
func (e *configChangeEmitter) Emit(ctx context.Context, ev *agentdb.ConfigEvent) error {
	if ev == nil {
		return fmt.Errorf("config.changed: nil config event")
	}
	if strings.TrimSpace(ev.ID) == "" || strings.TrimSpace(ev.Project) == "" {
		return fmt.Errorf("config.changed: config event needs an id and a project")
	}

	eventID := configChangedEventID(ev.ID)

	// Guard, first pass: has this record already been announced? A plain lookup
	// so the answer is the same on every backend rather than a duplicate-key
	// string match, which differs between Postgres and sqlite. Note the
	// distinction the sentinel buys: "not announced yet" means append, any other
	// error means try again later — announcing twice would be the worse answer.
	switch existing, err := e.store.GetProjectEvent(ctx, ev.Project, eventID); {
	case err == nil && existing != nil:
		return e.store.MarkConfigEventEmitted(ctx, ev.ID)
	case err != nil && !errors.Is(err, agentdb.ErrProjectEventNotFound):
		return fmt.Errorf("config.changed: check for an existing event: %w", err)
	}

	envelope := e.envelope(ctx, ev)
	if _, err := e.store.CreateProjectEvent(ctx, &agentdb.ProjectEvent{
		ID:      eventID, // derived, NOT random — that is the guard
		Project: ev.Project,
		Type:    agentdb.EventTypeConfigChanged,
		Text:    describeConfigChange(ev),
		// OccurredAt is the event spine's clock: SECONDS, while
		// config_events.created_at is milliseconds. Converting is not optional —
		// handing the spine a millisecond value dates the event to the year
		// 57000 and every "what changed this week" query stops working.
		OccurredAt: ev.CreatedAt / 1000,
		Envelope:   envelope,
	}); err != nil {
		// Guard, second pass: two emitters raced and the other one won (or the
		// process died between the insert and the watermark last time). The
		// derived id means the loser's row IS the winner's row, so this is a
		// success, not a failure.
		if other, getErr := e.store.GetProjectEvent(ctx, ev.Project, eventID); getErr == nil && other != nil {
			return e.store.MarkConfigEventEmitted(ctx, ev.ID)
		}
		return fmt.Errorf("config.changed: append event for config event %s: %w", ev.ID, err)
	}
	return e.store.MarkConfigEventEmitted(ctx, ev.ID)
}

// envelope stamps the §15.8 envelope from the acting session.
//
// A worker-made change sits one level deeper than the job that made it, which
// is what `ResolveWorkerJob` already computes for `worker.finished` — reused
// rather than recomputed so the two can never disagree about the depth floor.
func (e *configChangeEmitter) envelope(ctx context.Context, ev *agentdb.ConfigEvent) agentdb.EventEnvelope {
	// Human/UI/API edits leave both actor columns empty (§15.2): nobody's job
	// made this, so it enters the system exactly like an ingested event.
	if strings.TrimSpace(ev.ActorSession) == "" && strings.TrimSpace(ev.ActorWorker) == "" {
		return agentdb.EventEnvelope{Source: agentdb.EventSourceExternal, Depth: 0}
	}

	if ev.ActorSession != "" {
		job, ok, err := agentkit.ResolveWorkerJob(ctx, e.store, ev.ActorSession)
		switch {
		case err != nil:
			// The acting session is unreadable (deleted, or the database
			// hiccuped). Emitting nothing would lose the change; emitting depth 0
			// would hand a worker-made change the external depth and quietly
			// disarm §8.4's loop floor for whatever reacts to it. Depth 1 is the
			// honest floor: a worker made this, so it is not external.
			e.logf("[agentd] config.changed: could not resolve session %s for config event %s (%v) — "+
				"stamping depth 1; the §8.4 loop floor still binds, one level shallower than the truth",
				ev.ActorSession, ev.ID, err)
			return agentdb.EventEnvelope{
				Source:    agentdb.EventSourceWorker,
				Worker:    ev.ActorWorker,
				SessionID: ev.ActorSession,
				Depth:     1,
			}
		case ok:
			return agentdb.EventEnvelope{
				Source:      agentdb.EventSourceWorker,
				Worker:      job.Worker,
				SessionID:   ev.ActorSession,
				Depth:       job.Depth,
				Interactive: job.Interactive,
			}
		}
		// ok == false: a real session with no worker on it — a human chatting to
		// a plain session, using the management tools by hand. That is an
		// external edit that happens to have a conversation attached, so the
		// session is kept as provenance and the source stays external.
		if ev.ActorWorker == "" {
			return agentdb.EventEnvelope{
				Source:      agentdb.EventSourceExternal,
				SessionID:   ev.ActorSession,
				Depth:       0,
				Interactive: true,
			}
		}
	}

	// A worker with no session: nothing to resolve a depth from, so it is the
	// first link in whatever chain follows.
	return agentdb.EventEnvelope{
		Source:    agentdb.EventSourceWorker,
		Worker:    ev.ActorWorker,
		SessionID: ev.ActorSession,
		Depth:     0,
	}
}

// ---------------------------------------------------------------------------
// The text
//
// §8.1's raw-text discipline: what a worker reads is prose, not a schema it
// could come to depend on. So the text says who did what, in what to whom, and
// why — and quotes the config-event id so a reader can fetch the full new state
// with config_history.
// ---------------------------------------------------------------------------

func describeConfigChange(ev *agentdb.ConfigEvent) string {
	actor := "A human"
	if ev.ActorWorker != "" {
		actor = ev.ActorWorker
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s.", actor, configChangePhrase(ev))
	if r := strings.TrimSpace(ev.Rationale); r != "" {
		fmt.Fprintf(&b, "\nRationale: %s", r)
	}
	fmt.Fprintf(&b, "\nConfig event: %s (action %s", ev.ID, ev.Action)
	if ref, err := agentdb.EntityRefFor(ev); err == nil {
		fmt.Fprintf(&b, ", entity %s", ref.String())
	}
	b.WriteString(").")
	b.WriteString("\nRead the full new state with config_history.")
	return b.String()
}

// configChangePhrase is the verb half — deliberately past tense and specific,
// because "config was updated" tells a reacting worker nothing it can act on.
func configChangePhrase(ev *agentdb.ConfigEvent) string {
	name, _ := ev.PayloadString("name")
	switch ev.Action {
	case agentdb.ActionWorkerCreate:
		return fmt.Sprintf("hired worker %q", name)
	case agentdb.ActionWorkerUpdate:
		return fmt.Sprintf("changed the configuration of worker %q", name)
	case agentdb.ActionWorkerEnable:
		return fmt.Sprintf("enabled worker %q", name)
	case agentdb.ActionWorkerDisable:
		return fmt.Sprintf("disabled worker %q", name)
	case agentdb.ActionWorkerFreeze:
		return fmt.Sprintf("froze worker %q — its configuration can now only be changed by a human", name)
	case agentdb.ActionWorkerUnfreeze:
		return fmt.Sprintf("unfroze worker %q — other workers may change its configuration again", name)
	case agentdb.ActionWorkerDelete:
		return fmt.Sprintf("retired worker %q", name)
	case agentdb.ActionWorkerPromptWrite:
		return fmt.Sprintf("rewrote the system prompt of worker %q", name)
	case agentdb.ActionProjectPromptWrite:
		return "rewrote the project system prompt"
	case agentdb.ActionProjectSettingsPut:
		return "changed the project settings"
	case agentdb.ActionSubscriptionCreate:
		return "created a subscription " + subscriptionPhrase(ev)
	case agentdb.ActionSubscriptionUpdate:
		return "changed a subscription " + subscriptionPhrase(ev)
	case agentdb.ActionSubscriptionDelete:
		return "deleted a subscription " + subscriptionPhrase(ev)
	case agentdb.ActionScheduleCreate:
		return "created a schedule " + schedulePhrase(ev)
	case agentdb.ActionScheduleUpdate:
		return "retuned a schedule " + schedulePhrase(ev)
	case agentdb.ActionScheduleDelete:
		return "deleted a schedule " + schedulePhrase(ev)
	case agentdb.ActionImageCreate:
		if ref, err := agentdb.EntityRefFor(ev); err == nil {
			return fmt.Sprintf("published image %q", ref.Key)
		}
		return fmt.Sprintf("published image %q", name)
	case agentdb.ActionSkillCreate:
		return fmt.Sprintf("published skill %q", name)
	case agentdb.ActionTopologyApply:
		// The bracket record of one T2 apply. Its payload keys on "topology",
		// not "name"; the rows it created each announced themselves already.
		ref, _ := ev.PayloadString("topology")
		return fmt.Sprintf("applied topology %s — the workers, subscriptions and schedules it created were each recorded in their own config events", orUnset(ref))
	default:
		// Unreachable while the §15.3 vocabulary is closed and validated at the
		// seam — but a new verb must degrade to something readable rather than
		// to an empty sentence.
		return "made a " + ev.Action + " change"
	}
}

func subscriptionPhrase(ev *agentdb.ConfigEvent) string {
	eventType, _ := ev.PayloadString("event_type")
	worker, _ := ev.PayloadString("worker")
	if eventType == "" && worker == "" {
		return "(no routing recorded)"
	}
	return fmt.Sprintf("(%s → %s)", orUnset(eventType), orUnset(worker))
}

func schedulePhrase(ev *agentdb.ConfigEvent) string {
	cron, _ := ev.PayloadString("cron")
	worker, _ := ev.PayloadString("worker")
	if cron == "" && worker == "" {
		return "(no timing recorded)"
	}
	return fmt.Sprintf("(%s → %s)", orUnset(cron), orUnset(worker))
}

func orUnset(s string) string {
	if strings.TrimSpace(s) == "" {
		return "?"
	}
	return s
}

// ---------------------------------------------------------------------------
// The repair sweep
// ---------------------------------------------------------------------------

// Run repairs emissions lost to a crash between commit and append (§15.4's
// at-least-once). It is the reason `emitted_at` exists: without a sweep,
// "at-least-once" would really be "usually once", and a `config.changed`
// subscriber would silently miss whatever was decided during a restart.
func (e *configChangeEmitter) Run(ctx context.Context) {
	ticker := time.NewTicker(configChangeSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := e.Sweep(ctx); err != nil {
				e.logf("[agentd] config.changed sweep: %v", err)
			} else if n > 0 {
				e.logf("[agentd] config.changed sweep repaired %d unannounced change(s)", n)
			}
		}
	}
}

// Sweep emits every unannounced record older than the grace window, returning
// how many it repaired.
func (e *configChangeEmitter) Sweep(ctx context.Context) (int, error) {
	before := time.Now().Add(-configChangeSweepGrace).UnixMilli()
	pending, err := e.store.ListUnemittedConfigEvents(ctx, before, configChangeSweepBatch)
	if err != nil {
		return 0, err
	}
	repaired := 0
	for _, ev := range pending {
		if err := e.Emit(ctx, ev); err != nil {
			// One bad record must not stop the queue behind it.
			e.logf("[agentd] config.changed sweep: config event %s: %v", ev.ID, err)
			continue
		}
		repaired++
	}
	return repaired, nil
}
