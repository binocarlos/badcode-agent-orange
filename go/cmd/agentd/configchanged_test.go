package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ---------------------------------------------------------------------------
// J3 — the routable `config.changed` event (§15.4, §15.8).
//
// The three properties the work plan names, in the order they would hurt:
//
//   1. a rolled-back transaction emits nothing (a routed event for a change
//      that never happened is a lie the whole system would then act on);
//   2. a retry after a crash does not double-emit (at-least-once needs a guard
//      or it is just "sometimes twice");
//   3. a worker-made change carries the acting session's envelope at depth+1
//      (get this wrong and §8.4's loop floor — our only runaway protection —
//      reads depth 0 for ever).
//
// Plus the text, which is what a subscribed worker actually reads.
// ---------------------------------------------------------------------------

// fakeConfigChangeStore is an in-memory stand-in for the slice of agentdb the
// emitter uses. CreateProjectEvent REFUSES a duplicate id, because that refusal
// is the idempotency guard under test.
type fakeConfigChangeStore struct {
	sessions  map[string]*agentdb.Session
	triggers  map[string]*agentdb.ProjectEvent
	events    map[string]*agentdb.ProjectEvent
	created   []*agentdb.ProjectEvent
	marked    []string
	unemitted []*agentdb.ConfigEvent

	getErr     error
	createErr  error
	sessionErr error
}

func newFakeConfigChangeStore() *fakeConfigChangeStore {
	return &fakeConfigChangeStore{
		sessions: map[string]*agentdb.Session{},
		triggers: map[string]*agentdb.ProjectEvent{},
		events:   map[string]*agentdb.ProjectEvent{},
	}
}

func (f *fakeConfigChangeStore) GetSession(_ context.Context, id string) (*agentdb.Session, error) {
	if f.sessionErr != nil {
		return nil, f.sessionErr
	}
	s, ok := f.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	return s, nil
}

func (f *fakeConfigChangeStore) SessionTriggerEvent(_ context.Context, sessionID string) (*agentdb.ProjectEvent, error) {
	return f.triggers[sessionID], nil
}

func (f *fakeConfigChangeStore) CreateProjectEvent(_ context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if _, dup := f.events[ev.ID]; dup {
		return nil, fmt.Errorf("duplicate key value violates unique constraint (id=%s)", ev.ID)
	}
	f.events[ev.ID] = ev
	f.created = append(f.created, ev)
	return ev, nil
}

func (f *fakeConfigChangeStore) GetProjectEvent(_ context.Context, project, id string) (*agentdb.ProjectEvent, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	ev, ok := f.events[id]
	if !ok || ev.Project != project {
		return nil, agentdb.ErrProjectEventNotFound
	}
	return ev, nil
}

func (f *fakeConfigChangeStore) MarkConfigEventEmitted(_ context.Context, id string) error {
	f.marked = append(f.marked, id)
	return nil
}

func (f *fakeConfigChangeStore) ListUnemittedConfigEvents(_ context.Context, _ int64, _ int) ([]*agentdb.ConfigEvent, error) {
	return f.unemitted, nil
}

// configEvent builds a plausible committed record.
func configEvent(action string, payload agentdb.JSONMap, cw agentdb.ConfigWrite) *agentdb.ConfigEvent {
	return &agentdb.ConfigEvent{
		ID:           "ce-" + action,
		Project:      "acme",
		Seq:          1,
		Action:       action,
		Payload:      payload,
		ActorWorker:  cw.Worker,
		ActorSession: cw.Session,
		Rationale:    cw.Rationale,
		CreatedAt:    time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC).UnixMilli(),
	}
}

func emitterFor(store configChangeStore) *configChangeEmitter {
	return newConfigChangeEmitter(store, func(string, ...any) {})
}

// ── 2. at-least-once, exactly once in practice ──────────────────────────────

// TestConfigChangedEventIsIdempotent is the crash-retry case: the emitter is
// called twice for the same committed record — once by the hook, once by the
// repair sweep that could not know the first attempt had landed — and only one
// event exists afterwards.
func TestConfigChangedEventIsIdempotent(t *testing.T) {
	store := newFakeConfigChangeStore()
	e := emitterFor(store)
	ev := configEvent(agentdb.ActionWorkerCreate, agentdb.JSONMap{"name": "tweet-author"},
		agentdb.ConfigWrite{Worker: "manager", Session: "s-1"})

	for i := 0; i < 3; i++ {
		if err := e.Emit(context.Background(), ev); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if len(store.created) != 1 {
		t.Fatalf("expected exactly one config.changed event after three emits, got %d", len(store.created))
	}
	if len(store.marked) != 3 {
		t.Fatalf("every emit must stamp the watermark (it is idempotent), got %d stamps", len(store.marked))
	}
}

// TestConfigChangedEventIDIsDerivedFromTheConfigEvent pins WHY the retry is
// safe: the event id is a function of the config-event id, so a second
// emission is the same row rather than a second one. A random id here would
// make the idempotency guard a coincidence.
func TestConfigChangedEventIDIsDerivedFromTheConfigEvent(t *testing.T) {
	a := configChangedEventID("ce-41")
	if a != configChangedEventID("ce-41") {
		t.Fatal("derivation is not deterministic — the idempotency guard would not hold across restarts")
	}
	if a == configChangedEventID("ce-42") {
		t.Fatal("two config events derived the same event id — one change would silence another")
	}
	if len(a) != 36 {
		t.Fatalf("derived id %q is not a uuid", a)
	}
}

// TestConfigChangedEventSurvivesALostWatermark covers the other half of the
// crash window: the event landed but the process died before stamping
// emitted_at, so the sweep re-emits. The insert must be refused and the record
// stamped, not reported as a failure.
func TestConfigChangedEventSurvivesALostWatermark(t *testing.T) {
	store := newFakeConfigChangeStore()
	e := emitterFor(store)
	ev := configEvent(agentdb.ActionScheduleUpdate,
		agentdb.JSONMap{"id": "sch-7", "cron": "0 9 * * 1-5", "worker": "tweet-author"},
		agentdb.ConfigWrite{})

	if err := e.Emit(context.Background(), ev); err != nil {
		t.Fatalf("first emit: %v", err)
	}
	// Simulate "the row exists but nobody remembers writing it": the sweep hands
	// the same record back because emitted_at is still 0.
	store.unemitted = []*agentdb.ConfigEvent{ev}
	n, err := e.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweep repaired %d, want 1", n)
	}
	if len(store.created) != 1 {
		t.Fatalf("the sweep double-emitted: %d events", len(store.created))
	}
}

// TestConfigChangedEventReportsRealFailures makes sure the idempotency guard is
// not swallowing genuine trouble: an unreadable event table must surface, so
// the record stays unstamped and the sweep tries again.
func TestConfigChangedEventReportsRealFailures(t *testing.T) {
	store := newFakeConfigChangeStore()
	store.getErr = errors.New("connection refused")
	e := emitterFor(store)

	err := e.Emit(context.Background(), configEvent(agentdb.ActionWorkerCreate,
		agentdb.JSONMap{"name": "x"}, agentdb.ConfigWrite{}))
	if err == nil {
		t.Fatal("a database failure was reported as a successful emission")
	}
	if len(store.marked) != 0 {
		t.Fatal("a failed emission stamped the watermark — the sweep would never retry it")
	}
}

// ── 3. the envelope comes from the acting session ───────────────────────────

// TestConfigChangedEventCarriesTheActingSessionEnvelope is the depth case: a
// worker whose job was triggered by an event at depth 2 changes configuration,
// and the change is announced at depth 3 — exactly where that job's
// `worker.finished` would sit, so §8.4's floor binds a config-reaction loop
// like any other.
func TestConfigChangedEventCarriesTheActingSessionEnvelope(t *testing.T) {
	store := newFakeConfigChangeStore()
	store.sessions["s-1043"] = &agentdb.Session{ID: "s-1043", Customer: "acme", Worker: "marketing-manager"}
	store.triggers["s-1043"] = &agentdb.ProjectEvent{
		ID: "ev-9", Project: "acme", Envelope: agentdb.EventEnvelope{Depth: 2, Source: agentdb.EventSourceWorker},
	}
	e := emitterFor(store)

	err := e.Emit(context.Background(), configEvent(agentdb.ActionWorkerPromptWrite,
		agentdb.JSONMap{"name": "tweet-author"},
		agentdb.ConfigWrite{
			Worker: "marketing-manager", Session: "s-1043",
			Rationale: "the Thursday thread experiment underperformed",
		}))
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("want one event, got %d", len(store.created))
	}
	env := store.created[0].Envelope
	if env.Source != agentdb.EventSourceWorker {
		t.Fatalf("source: want %q, got %q", agentdb.EventSourceWorker, env.Source)
	}
	if env.Worker != "marketing-manager" || env.SessionID != "s-1043" {
		t.Fatalf("actor not carried into the envelope: %+v", env)
	}
	if env.Depth != 3 {
		t.Fatalf("depth: want the acting job's depth + 1 = 3, got %d — the §8.4 loop floor is not binding", env.Depth)
	}
	if env.Interactive {
		t.Fatal("a job started by a delivery is not interactive")
	}
	if store.created[0].Type != agentdb.EventTypeConfigChanged {
		t.Fatalf("type: want %q, got %q", agentdb.EventTypeConfigChanged, store.created[0].Type)
	}
}

// TestConfigChangedEventFromAHumanIsExternal: a UI or API edit leaves both
// actor columns empty (§15.2), so it enters exactly like an ingested event.
func TestConfigChangedEventFromAHumanIsExternal(t *testing.T) {
	store := newFakeConfigChangeStore()
	e := emitterFor(store)

	if err := e.Emit(context.Background(), configEvent(agentdb.ActionProjectSettingsPut,
		agentdb.JSONMap{"project": "acme"}, agentdb.ConfigWrite{})); err != nil {
		t.Fatalf("emit: %v", err)
	}
	env := store.created[0].Envelope
	if env.Source != agentdb.EventSourceExternal || env.Depth != 0 {
		t.Fatalf("human edit: want {external, depth 0}, got %+v", env)
	}
	if env.Worker != "" {
		t.Fatalf("human edit carried a worker: %+v", env)
	}
}

// TestConfigChangedEventFromAnInteractiveJob: a human chatting to a worker is
// still a worker making the change, and an interactive job has no triggering
// event — depth 0, interactive true, exactly as worker.finished would stamp it.
func TestConfigChangedEventFromAnInteractiveJob(t *testing.T) {
	store := newFakeConfigChangeStore()
	store.sessions["s-2"] = &agentdb.Session{ID: "s-2", Customer: "acme", Worker: "email-reviewer"}
	e := emitterFor(store)

	if err := e.Emit(context.Background(), configEvent(agentdb.ActionWorkerDisable,
		agentdb.JSONMap{"name": "spammer"},
		agentdb.ConfigWrite{Worker: "email-reviewer", Session: "s-2"})); err != nil {
		t.Fatalf("emit: %v", err)
	}
	env := store.created[0].Envelope
	if env.Source != agentdb.EventSourceWorker || env.Worker != "email-reviewer" {
		t.Fatalf("want a worker envelope, got %+v", env)
	}
	if env.Depth != 0 || !env.Interactive {
		t.Fatalf("an interactive job is depth 0 and interactive: %+v", env)
	}
}

// TestConfigChangedEventWithAnUnreadableSessionStillEmits: losing the session
// row must cost precision, never the event. Depth 1 rather than 0, because a
// worker made this and calling it external would disarm the loop floor.
func TestConfigChangedEventWithAnUnreadableSessionStillEmits(t *testing.T) {
	store := newFakeConfigChangeStore()
	store.sessionErr = errors.New("session table unavailable")
	e := emitterFor(store)

	if err := e.Emit(context.Background(), configEvent(agentdb.ActionWorkerUpdate,
		agentdb.JSONMap{"name": "tweet-author"},
		agentdb.ConfigWrite{Worker: "manager", Session: "s-gone"})); err != nil {
		t.Fatalf("emit: %v", err)
	}
	env := store.created[0].Envelope
	if env.Source != agentdb.EventSourceWorker || env.Depth != 1 {
		t.Fatalf("want {worker, depth 1} when the session is unreadable, got %+v", env)
	}
}

// ── the text a subscribed worker actually reads (§15.8) ─────────────────────

func TestConfigChangedEventTextNamesTheActorTheChangeAndTheRationale(t *testing.T) {
	ev := configEvent(agentdb.ActionWorkerPromptWrite,
		agentdb.JSONMap{"name": "tweet-author"},
		agentdb.ConfigWrite{
			Worker: "marketing-manager", Session: "s-1043",
			Rationale: "going back to single posts",
		})
	text := describeConfigChange(ev)

	for _, want := range []string{
		"marketing-manager",
		"rewrote the system prompt of worker \"tweet-author\"",
		"Rationale: going back to single posts",
		ev.ID,
		"worker:tweet-author",
		"config_history",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config.changed text is missing %q:\n%s", want, text)
		}
	}
}

func TestConfigChangedEventTextCoversEveryAction(t *testing.T) {
	// Every verb in the closed vocabulary must render as a sentence — a new one
	// arriving with no phrase would announce "made a … change" to every
	// subscriber, which is the sort of thing nobody notices for a year.
	payloads := map[string]agentdb.JSONMap{
		agentdb.ActionWorkerCreate:       {"name": "w"},
		agentdb.ActionWorkerUpdate:       {"name": "w"},
		agentdb.ActionWorkerEnable:       {"name": "w"},
		agentdb.ActionWorkerDisable:      {"name": "w"},
		agentdb.ActionWorkerFreeze:       {"name": "w"},
		agentdb.ActionWorkerUnfreeze:     {"name": "w"},
		agentdb.ActionWorkerDelete:       {"name": "w"},
		agentdb.ActionWorkerPromptWrite:  {"name": "w"},
		agentdb.ActionProjectPromptWrite: {"system_prompt": "hi"},
		agentdb.ActionProjectSettingsPut: {"project": "acme"},
		agentdb.ActionSubscriptionCreate: {"id": "sub-1", "event_type": "email.received", "worker": "answerer"},
		agentdb.ActionSubscriptionUpdate: {"id": "sub-1", "event_type": "email.*", "worker": "answerer"},
		agentdb.ActionSubscriptionDelete: {"id": "sub-1", "event_type": "email.*", "worker": "answerer"},
		agentdb.ActionScheduleCreate:     {"id": "sch-1", "cron": "0 9 * * *", "worker": "tweeter"},
		agentdb.ActionScheduleUpdate:     {"id": "sch-1", "cron": "0 10 * * *", "worker": "tweeter"},
		agentdb.ActionScheduleDelete:     {"id": "sch-1", "cron": "0 10 * * *", "worker": "tweeter"},
		agentdb.ActionImageCreate:        {"name": "toolbox", "version": float64(2)},
		agentdb.ActionSkillCreate:        {"name": "graph-gen"},
		agentdb.ActionTopologyApply:      {"topology": "solo@v1", "answers": map[string]any{"cadence": "daily"}},
	}
	for _, action := range agentdb.ConfigActions {
		payload, ok := payloads[action]
		if !ok {
			t.Fatalf("no fixture for action %q — add one when you add a verb", action)
		}
		phrase := configChangePhrase(configEvent(action, payload, agentdb.ConfigWrite{}))
		if phrase == "" || strings.Contains(phrase, "made a ") {
			t.Fatalf("action %q has no human phrase: %q", action, phrase)
		}
	}
}

// TestConfigChangedEventUsesTheEventSpineClock guards the ms/seconds trap the
// config log is full of: config_events.created_at is MILLISECONDS and the event
// spine is SECONDS. Handing the spine a millisecond value dates the change
// thousands of years into the future and quietly breaks every time filter.
func TestConfigChangedEventUsesTheEventSpineClock(t *testing.T) {
	store := newFakeConfigChangeStore()
	e := emitterFor(store)
	ev := configEvent(agentdb.ActionWorkerCreate, agentdb.JSONMap{"name": "w"}, agentdb.ConfigWrite{})

	if err := e.Emit(context.Background(), ev); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if got, want := store.created[0].OccurredAt, ev.CreatedAt/1000; got != want {
		t.Fatalf("occurred_at: want %d seconds, got %d", want, got)
	}
}
