package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	// Embedded IANA data, so the DST tick case holds without system zoneinfo.
	_ "time/tzdata"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ── Fakes ───────────────────────────────────────────────────────────────────

// fakeDispatchStore implements BOTH the schedulerStore and dispatchStore seams,
// so a test can drive a firing all the way from "the minute matched" to "the
// gate said queued" with no database.
type fakeDispatchStore struct {
	schedules map[string]*agentdb.Schedule
	workers   map[string]*agentdb.Worker // key: project + "/" + name
	settings  map[string]*agentdb.ProjectSettings
	events    map[string]*agentdb.ProjectEvent
	// deliveries keeps insertion order so FIFO assertions are meaningful.
	deliveries []*agentdb.EventDelivery
	firings    map[string]bool // schedule_id + "@" + occurrence
	// awaitingHuman stands in for the open rows of `attention_requests`, keyed by
	// session id. See dispatch_attention_test.go.
	awaitingHuman map[string]bool

	disabled    map[string]string // schedule id → rationale
	createdRows int
	seq         int
	// claims counts winning ClaimDelivery calls (RD10); claimNow is the
	// started_at a claim stamps, so the stale-delivery sweep can be aged.
	claims int
	// claimAt supplies the started_at a claim stamps, mirroring the real store.
	// The router fake points it at its own clock so a test can age a delivery.
	claimAt func() int64

	// Fault injection over the store seam (RD1). Non-nil makes the read fail the
	// way a database that is up-but-unhappy fails: an opaque error that is NOT
	// one of agentdb's not-found sentinels. Nothing in the product may read one
	// of these as "the row is absent".
	getWorkerErr       error
	getProjectEventErr error
	// failDeliveryWriteFor makes the status write fail for the named statuses,
	// the way a database that is up-but-unhappy fails. RD7's wedge begins
	// exactly there: `settle` only LOGS that error.
	failDeliveryWriteFor map[string]bool
}

func newFakeDispatchStore() *fakeDispatchStore {
	return &fakeDispatchStore{
		schedules: map[string]*agentdb.Schedule{},
		workers:   map[string]*agentdb.Worker{},
		settings:  map[string]*agentdb.ProjectSettings{},
		events:    map[string]*agentdb.ProjectEvent{},
		firings:   map[string]bool{},
		disabled:  map[string]string{},

		awaitingHuman: map[string]bool{},
	}
}

func (f *fakeDispatchStore) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

func (f *fakeDispatchStore) addSchedule(s *agentdb.Schedule) *agentdb.Schedule {
	if s.ID == "" {
		s.ID = f.nextID("sched")
	}
	f.schedules[s.ID] = s
	return s
}

func (f *fakeDispatchStore) addWorker(w *agentdb.Worker) *agentdb.Worker {
	f.workers[w.Project+"/"+w.Name] = w
	return w
}

func (f *fakeDispatchStore) ListEnabledSchedules(context.Context) ([]*agentdb.Schedule, error) {
	out := []*agentdb.Schedule{}
	for _, s := range f.schedules {
		if s.Enabled {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeDispatchStore) GetWorker(_ context.Context, project, name string) (*agentdb.Worker, error) {
	if f.getWorkerErr != nil {
		return nil, f.getWorkerErr
	}
	if w, ok := f.workers[project+"/"+name]; ok {
		return w, nil
	}
	return nil, fmt.Errorf("%w: %s/%s", agentdb.ErrWorkerNotFound, project, name)
}

func (f *fakeDispatchStore) DisableSchedule(_ context.Context, project, id string, cw agentdb.ConfigWrite) (*agentdb.Schedule, error) {
	s, ok := f.schedules[id]
	if !ok || s.Project != project {
		return nil, fmt.Errorf("%w: %s", agentdb.ErrScheduleNotFound, id)
	}
	s.Enabled = false
	// The real store spends the streak with the disable, so a re-enable gets a
	// fresh budget rather than retiring on its next firing.
	s.ProvisionFailures = 0
	s.LastProvisionError = ""
	f.disabled[id] = cw.Rationale
	return s, nil
}

func (f *fakeDispatchStore) NoteScheduleProvisionFailure(_ context.Context, project, id, reason string) (int, error) {
	s, ok := f.schedules[id]
	if !ok || s.Project != project {
		return 0, fmt.Errorf("%w: %s", agentdb.ErrScheduleNotFound, id)
	}
	s.ProvisionFailures++
	s.LastProvisionError = reason
	return s.ProvisionFailures, nil
}

func (f *fakeDispatchStore) ClearScheduleProvisionFailures(_ context.Context, project, id string) error {
	s, ok := f.schedules[id]
	if !ok || s.Project != project {
		return fmt.Errorf("%w: %s", agentdb.ErrScheduleNotFound, id)
	}
	s.ProvisionFailures = 0
	s.LastProvisionError = ""
	return nil
}

func (f *fakeDispatchStore) ClaimFiring(_ context.Context, fi *agentdb.ScheduleFiring) (*agentdb.ScheduleFiring, bool, error) {
	key := fi.ScheduleID + "@" + fi.ScheduledFor
	if f.firings[key] {
		return &agentdb.ScheduleFiring{ID: "existing", ScheduleID: fi.ScheduleID, ScheduledFor: fi.ScheduledFor}, false, nil
	}
	f.firings[key] = true
	if fi.ID == "" {
		fi.ID = f.nextID("firing")
	}
	return fi, true, nil
}

func (f *fakeDispatchStore) StampFiringEvent(context.Context, string, string) error { return nil }

func (f *fakeDispatchStore) CreateProjectEvent(_ context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error) {
	if ev.ID == "" {
		ev.ID = f.nextID("ev")
	}
	f.events[ev.ID] = ev
	return ev, nil
}

func (f *fakeDispatchStore) GetProjectEvent(_ context.Context, project, id string) (*agentdb.ProjectEvent, error) {
	if f.getProjectEventErr != nil {
		return nil, f.getProjectEventErr
	}
	ev, ok := f.events[id]
	if !ok || ev.Project != project {
		// The sentinel the real store returns — the fake used to invent a bare
		// error, which is exactly the kind of writer/reader mismatch that let an
		// unclassified read look correct (RD1).
		return nil, fmt.Errorf("%w: %s", agentdb.ErrProjectEventNotFound, id)
	}
	return ev, nil
}

func (f *fakeDispatchStore) EnsureDelivery(_ context.Context, d *agentdb.EventDelivery) (*agentdb.EventDelivery, bool, error) {
	for _, existing := range f.deliveries {
		if existing.EventID == d.EventID && existing.SubscriptionID == d.SubscriptionID {
			return existing, false, nil
		}
	}
	if d.ID == "" {
		d.ID = f.nextID("del")
	}
	if d.Status == "" {
		d.Status = agentdb.DeliveryPending
	}
	f.createdRows++
	d.CreatedAt = int64(f.createdRows) // insertion order == FIFO order
	f.deliveries = append(f.deliveries, d)
	return d, true, nil
}

func (f *fakeDispatchStore) GetProjectSettings(_ context.Context, project string) (*agentdb.ProjectSettings, error) {
	if ps, ok := f.settings[project]; ok {
		return ps, nil
	}
	return agentdb.DefaultProjectSettings(project), nil
}

func (f *fakeDispatchStore) CountActiveDeliveries(_ context.Context, project string) (int64, error) {
	var n int64
	for _, d := range f.deliveries {
		if d.Project == project && d.Status == agentdb.DeliveryRunning {
			n++
		}
	}
	return n, nil
}

func (f *fakeDispatchStore) CountActiveDeliveriesForWorker(_ context.Context, project, worker string) (int64, error) {
	var n int64
	for _, d := range f.deliveries {
		if d.Project == project && d.Worker == worker && d.Status == agentdb.DeliveryRunning {
			n++
		}
	}
	return n, nil
}

func (f *fakeDispatchStore) ListPendingDeliveries(_ context.Context, project, worker string, _ int) ([]*agentdb.EventDelivery, error) {
	out := []*agentdb.EventDelivery{}
	for _, d := range f.deliveries {
		if d.Project != project || d.Status != agentdb.DeliveryPending {
			continue
		}
		if worker != "" && d.Worker != worker {
			continue
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func (f *fakeDispatchStore) UpdateDeliveryStatus(_ context.Context, project, id string, u agentdb.DeliveryStatusUpdate) (*agentdb.EventDelivery, error) {
	if f.failDeliveryWriteFor[u.Status] {
		return nil, fmt.Errorf("the delivery status write failed")
	}
	for _, d := range f.deliveries {
		if d.ID == id && d.Project == project {
			d.Status = u.Status
			if u.SessionID != "" {
				d.SessionID = u.SessionID
			}
			// Mirrors the real store (agentdb.UpdateDeliveryStatus): a reason is
			// written when given, kept when omitted, and cleared the moment the
			// row stops being failed.
			if u.FailureReason != "" {
				d.FailureReason = u.FailureReason
			} else if u.Status != agentdb.DeliveryFailed {
				d.FailureReason = ""
			}
			return d, nil
		}
	}
	return nil, fmt.Errorf("delivery not found")
}

// ClaimDelivery mirrors the real store's conditional UPDATE: pending→running or
// ErrDeliveryNotPending, never a read-then-write. `claims` counts every call
// that won, which is what the double-dispatch tests assert on.
func (f *fakeDispatchStore) ClaimDelivery(_ context.Context, project, id string) (*agentdb.EventDelivery, error) {
	for _, d := range f.deliveries {
		if d.ID != id || d.Project != project {
			continue
		}
		if d.Status != agentdb.DeliveryPending {
			return nil, agentdb.ErrDeliveryNotPending
		}
		d.Status = agentdb.DeliveryRunning
		if d.StartedAt == 0 && f.claimAt != nil {
			d.StartedAt = f.claimAt()
		}
		f.claims++
		return d, nil
	}
	return nil, agentdb.ErrDeliveryNotPending
}

// recordingStarter is a sessionStarter that records what it was asked to run.
type recordingStarter struct {
	jobs []startJobInput
	err  error
	n    int
}

func (r *recordingStarter) StartJob(_ context.Context, in startJobInput) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	r.jobs = append(r.jobs, in)
	r.n++
	return fmt.Sprintf("sess-%d", r.n), nil
}

// newTestScheduler wires a scheduler and the real gate over the fake store, with
// the clock pinned to `now` in UTC.
func newTestScheduler(t *testing.T, store *fakeDispatchStore, starter sessionStarter, now time.Time) (*scheduler, *dispatcher) {
	t.Helper()
	gate := newDispatcher(dispatcherConfig{
		Store:        store,
		Starter:      starter,
		DefaultImage: "agentkit-example:dev",
		Logf:         func(string, ...any) {},
	})
	s := newScheduler(schedulerConfig{
		Store:      store,
		Dispatcher: gate,
		Location:   time.UTC,
		Now:        func() time.Time { return now },
		Logf:       func(string, ...any) {},
	})
	return s, gate
}

// ── Firing ──────────────────────────────────────────────────────────────────

// TestSchedulerFiresDueScheduleThroughComposition proves the §8.6 path end to
// end: a matching minute becomes a `schedule.fired` event whose text is the
// schedule's input, composed through the ordinary path and started as a job.
func TestSchedulerFiresDueScheduleThroughComposition(t *testing.T) {
	store := newFakeDispatchStore()
	worker := agentdb.NewWorker("acme", "tweet-author")
	worker.SystemPrompt = "you write tweets"
	store.addWorker(worker)
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "0 10 * * *", "write the morning tweet"))

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 10, 0, 30, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(starter.jobs) != 1 {
		t.Fatalf("expected one job started, got %d", len(starter.jobs))
	}
	job := starter.jobs[0]
	if job.Project != "acme" || job.Worker.Name != "tweet-author" {
		t.Fatalf("job identity: %+v", job)
	}
	if job.Event.Type != agentdb.EventTypeScheduleFired {
		t.Fatalf("event type: want %q, got %q", agentdb.EventTypeScheduleFired, job.Event.Type)
	}
	// §8.6: the schedule does not only say WHEN, it says WHAT — the input is the
	// event text, and it reaches the model as the first message.
	if job.Event.Text != "write the morning tweet" {
		t.Fatalf("event text must be the schedule's input, got %q", job.Event.Text)
	}
	if job.Event.Envelope.Source != agentdb.EventSourceSchedule || job.Event.Envelope.Depth != 0 {
		t.Fatalf("envelope must be {source: schedule, depth: 0}: %+v", job.Event.Envelope)
	}
	if !strings.Contains(job.Job.FirstMessage, "write the morning tweet") ||
		!strings.Contains(job.Job.FirstMessage, agentkit.EventTextBeginMarker) {
		t.Fatalf("the firing must flow through the identical composition path: %q", job.Job.FirstMessage)
	}
	if !strings.Contains(job.Job.SystemPrompt, "you write tweets") {
		t.Fatalf("composed prompt missing the worker prompt: %q", job.Job.SystemPrompt)
	}

	// The delivery is running and carries the schedule on both id columns.
	if len(store.deliveries) != 1 {
		t.Fatalf("expected one delivery, got %d", len(store.deliveries))
	}
	d := store.deliveries[0]
	if d.Status != agentdb.DeliveryRunning || d.SessionID == "" {
		t.Fatalf("delivery should be running with a session: %+v", d)
	}
	if d.Worker != "tweet-author" || d.ScheduleID != sch.ID || d.SubscriptionID != sch.ID {
		t.Fatalf("delivery must carry the worker and the schedule id: %+v", d)
	}
}

// TestSchedulerFiresEachOccurrenceOnce covers both halves of idempotency: a
// repeated tick inside the same minute does nothing, and a re-claimed occurrence
// (a crash/retry, or a second agentd) does nothing either.
func TestSchedulerFiresEachOccurrenceOnce(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "0 10 * * *", "tweet"))

	starter := &recordingStarter{}
	minute := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	s, _ := newTestScheduler(t, store, starter, minute)

	for i := 0; i < 3; i++ {
		if err := s.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("three ticks in one minute must fire once, got %d", len(starter.jobs))
	}

	// A fresh scheduler (a restarted process) hitting the same minute is stopped
	// by the firing table, not by in-process memory.
	s2, _ := newTestScheduler(t, store, starter, minute.Add(30*time.Second))
	if err := s2.Tick(context.Background()); err != nil {
		t.Fatalf("restarted tick: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("a restarted scheduler must not re-fire a claimed occurrence, got %d", len(starter.jobs))
	}
}

// TestSchedulerSkipsMissedFirings is §8.6's headline behaviour: minutes that
// passed while agentd was down are gone, not replayed.
func TestSchedulerSkipsMissedFirings(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	// Every hour on the hour: four occurrences pass while we are "down".
	store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "0 * * * *", "hourly check"))

	starter := &recordingStarter{}
	// Come back up at 14:00, four hours after the last tick would have run.
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 14, 0, 5, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("only the current minute may fire; a backlog of stale mornings must be skipped (got %d)", len(starter.jobs))
	}
	if len(store.firings) != 1 {
		t.Fatalf("missed occurrences must not be claimed retroactively: %v", store.firings)
	}
}

// TestSchedulerDisablesScheduleWithMissingWorker: §8.6 — disabled and logged,
// never silently retried forever. The reason lands in the config log.
func TestSchedulerDisablesScheduleWithMissingWorker(t *testing.T) {
	store := newFakeDispatchStore()
	sch := store.addSchedule(agentdb.NewSchedule("acme", "retired-worker", "0 10 * * *", "tweet"))

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if store.schedules[sch.ID].Enabled {
		t.Fatalf("a schedule whose worker is gone must be disabled")
	}
	if !strings.Contains(store.disabled[sch.ID], "retired-worker") {
		t.Fatalf("the disable must record why: %q", store.disabled[sch.ID])
	}
	if len(starter.jobs) != 0 || len(store.deliveries) != 0 {
		t.Fatalf("nothing may be started for a missing worker")
	}
	// The occurrence is NOT consumed: re-hiring the worker and re-enabling the
	// schedule must be able to fire that minute.
	if len(store.firings) != 0 {
		t.Fatalf("a missing worker must not burn the occurrence: %v", store.firings)
	}
}

// TestSchedulerDisablesUnparseableCron covers the row written around the store.
func TestSchedulerDisablesUnparseableCron(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := store.addSchedule(&agentdb.Schedule{
		ID: "sched-bad", Project: "acme", Worker: "tweet-author", Cron: "not a cron", Input: "x", Enabled: true,
	})

	s, _ := newTestScheduler(t, store, &recordingStarter{}, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if store.schedules[sch.ID].Enabled {
		t.Fatalf("an unparseable cron must disable the schedule rather than be re-parsed every minute")
	}
}

// TestSchedulerDSTRepeatedHourFiresOnce is the tick-level DST case: the wall
// clock 01:30 comes round twice on the fall-back night and the job runs once.
func TestSchedulerDSTRepeatedHourFiresOnce(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "30 1 * * *", "write the morning tweet"))

	starter := &recordingStarter{}
	// 2026-10-25: 01:30 BST, then an hour later 01:30 GMT — two instants, one
	// wall clock.
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)  // 01:30 BST
	second := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC) // 01:30 GMT
	if a, b := first.In(london).Format("15:04"), second.In(london).Format("15:04"); a != "01:30" || b != "01:30" {
		t.Fatalf("fixture wrong: %s / %s", a, b)
	}

	for _, instant := range []time.Time{first, second} {
		s := newScheduler(schedulerConfig{
			Store: store, Location: london,
			Dispatcher: newDispatcher(dispatcherConfig{
				Store: store, Starter: starter, Logf: func(string, ...any) {},
			}),
			Now:  func() time.Time { return instant },
			Logf: func(string, ...any) {},
		})
		if err := s.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("the repeated wall-clock hour must fire exactly once, got %d", len(starter.jobs))
	}
}

// ── The shared gate (§8.4 steps 3 and 7) ────────────────────────────────────

// TestSchedulerGateQueuesAtMaxInstances is the walkthrough requirement: a firing
// for a worker already at max_instances queues as `pending` instead of starting
// a second instance.
func TestSchedulerGateQueuesAtMaxInstances(t *testing.T) {
	store := newFakeDispatchStore()
	worker := agentdb.NewWorker("acme", "tweet-author") // max_instances = 1
	store.addWorker(worker)
	store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	starter := &recordingStarter{}
	base := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	// Minute one starts a job; minute two finds the worker busy.
	for i := 0; i < 2; i++ {
		s, _ := newTestScheduler(t, store, starter, base.Add(time.Duration(i)*time.Minute))
		if err := s.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("max_instances=1 must not start a second instance, got %d jobs", len(starter.jobs))
	}
	if len(store.deliveries) != 2 {
		t.Fatalf("both firings must be recorded as deliveries, got %d", len(store.deliveries))
	}
	if store.deliveries[1].Status != agentdb.DeliveryPending {
		t.Fatalf("the second firing must QUEUE as pending, got %q", store.deliveries[1].Status)
	}

	// When the instance frees, the drain dispatches it — FIFO, through the same
	// gate, with no new firing involved.
	store.deliveries[0].Status = agentdb.DeliveryOK
	gate := newDispatcher(dispatcherConfig{Store: store, Starter: starter, Logf: func(string, ...any) {}})
	started, err := gate.DrainPending(context.Background(), "acme")
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if started != 1 || len(starter.jobs) != 2 {
		t.Fatalf("the queued firing must dispatch when an instance frees: started=%d jobs=%d", started, len(starter.jobs))
	}
	if store.deliveries[1].Status != agentdb.DeliveryRunning {
		t.Fatalf("drained delivery should be running: %+v", store.deliveries[1])
	}
}

// TestSchedulerGateRespectsMaxInstancesAboveOne proves the cap is the number,
// not a boolean.
func TestSchedulerGateRespectsMaxInstancesAboveOne(t *testing.T) {
	store := newFakeDispatchStore()
	worker := agentdb.NewWorker("acme", "tweet-author")
	worker.MaxInstances = 2
	store.addWorker(worker)
	store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	starter := &recordingStarter{}
	base := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		s, _ := newTestScheduler(t, store, starter, base.Add(time.Duration(i)*time.Minute))
		if err := s.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(starter.jobs) != 2 {
		t.Fatalf("max_instances=2 must run two and queue the third, got %d", len(starter.jobs))
	}
	if store.deliveries[2].Status != agentdb.DeliveryPending {
		t.Fatalf("the third firing must queue: %+v", store.deliveries[2])
	}
}

// TestSchedulerGateRespectsProjectConcurrencyCap: the per-project cap (§8.4
// step 3) applies to firings, and is shared with the router by construction.
func TestSchedulerGateRespectsProjectConcurrencyCap(t *testing.T) {
	store := newFakeDispatchStore()
	settings := agentdb.DefaultProjectSettings("acme")
	settings.MaxConcurrentJobs = 1
	store.settings["acme"] = settings
	for _, name := range []string{"tweet-author", "image-maker"} {
		w := agentdb.NewWorker("acme", name)
		w.MaxInstances = 5 // room at the worker level; the project cap is the binding one
		store.addWorker(w)
		store.addSchedule(agentdb.NewSchedule("acme", name, "* * * * *", "do the thing"))
	}

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("max_concurrent_jobs=1 must start exactly one job, got %d", len(starter.jobs))
	}
	pending := 0
	for _, d := range store.deliveries {
		if d.Status == agentdb.DeliveryPending {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("the other firing must queue as pending, got %d pending", pending)
	}
}

// TestSchedulerGateFailsDeliveryForDisabledWorker: a disabled worker is a job
// failure, not an infinitely retried pending row.
func TestSchedulerGateFailsDeliveryForDisabledWorker(t *testing.T) {
	store := newFakeDispatchStore()
	worker := agentdb.NewWorker("acme", "tweet-author")
	worker.Enabled = false
	store.addWorker(worker)

	gate := newDispatcher(dispatcherConfig{Store: store, Starter: &recordingStarter{}, Logf: func(string, ...any) {}})
	ev, _ := store.CreateProjectEvent(context.Background(), &agentdb.ProjectEvent{
		Project: "acme", Type: agentdb.EventTypeScheduleFired, Text: "tweet",
		Envelope: agentdb.EventEnvelope{Source: agentdb.EventSourceSchedule},
	})
	d, _, _ := store.EnsureDelivery(context.Background(), &agentdb.EventDelivery{
		Project: "acme", EventID: ev.ID, SubscriptionID: "sched-1", ScheduleID: "sched-1",
		Worker: "tweet-author", Status: agentdb.DeliveryPending,
	})

	outcome, err := gate.Dispatch(context.Background(), d)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if outcome != dispatchFailed || d.Status != agentdb.DeliveryFailed {
		t.Fatalf("want a failed delivery, got outcome=%s status=%s", outcome, d.Status)
	}
}

// TestSchedulerGateIsIdempotentPerDelivery: a delivery that is no longer pending
// is skipped, so a duplicated drain cannot start a second session for it.
func TestSchedulerGateIsIdempotentPerDelivery(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	starter := &recordingStarter{}
	gate := newDispatcher(dispatcherConfig{Store: store, Starter: starter, Logf: func(string, ...any) {}})

	ev, _ := store.CreateProjectEvent(context.Background(), &agentdb.ProjectEvent{
		Project: "acme", Type: agentdb.EventTypeScheduleFired, Text: "tweet",
		Envelope: agentdb.EventEnvelope{Source: agentdb.EventSourceSchedule},
	})
	d, _, _ := store.EnsureDelivery(context.Background(), &agentdb.EventDelivery{
		Project: "acme", EventID: ev.ID, SubscriptionID: "s1", Worker: "tweet-author", Status: agentdb.DeliveryPending,
	})
	if out, err := gate.Dispatch(context.Background(), d); err != nil || out != dispatchStarted {
		t.Fatalf("first dispatch: %s %v", out, err)
	}
	if out, err := gate.Dispatch(context.Background(), d); err != nil || out != dispatchSkipped {
		t.Fatalf("second dispatch must be a no-op: %s %v", out, err)
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("a duplicated dispatch started %d jobs", len(starter.jobs))
	}
}
