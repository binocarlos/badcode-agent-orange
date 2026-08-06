package main

// scheduler_missed_test.go — RD11: a missed occurrence must stop being
// indistinguishable from one that was never scheduled.
//
// Every test here fails against the pre-RD11 scheduler, and they fail in two
// different directions on purpose:
//
//   - the reporting tests fail because NOTHING was written (no firing row, no
//     event) — the silent-success shape doc 22 exists to kill;
//   - the "zero surprise jobs" assertions fail if a later change ever decides
//     to replay the backlog instead, which is the opposite mistake and the more
//     expensive one (an outbound action has no undo).
//
// So the pair pins the decision, not just the code.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// missedEvents returns the schedule.missed events the fake store holds.
func missedEvents(store *fakeDispatchStore) []*agentdb.ProjectEvent {
	out := []*agentdb.ProjectEvent{}
	for _, ev := range store.events {
		if ev.Type == agentdb.EventTypeScheduleMissed {
			out = append(out, ev)
		}
	}
	return out
}

func eventsOfType(store *fakeDispatchStore, typ string) int {
	n := 0
	for _, ev := range store.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func missedFirings(store *fakeDispatchStore) []*agentdb.ScheduleFiring {
	out := []*agentdb.ScheduleFiring{}
	for _, f := range store.firings {
		if f.Missed {
			out = append(out, f)
		}
	}
	return out
}

// TestSchedulerReportsMissedOccurrences is the headline: four hours of downtime
// spanning three hourly occurrences produce three missed records and ONE event —
// and exactly one job, for the minute we actually came back in.
func TestSchedulerReportsMissedOccurrences(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := agentdb.NewSchedule("acme", "tweet-author", "0 * * * *", "hourly check")
	// The watermark says a tick last looked at this row at 10:30. Everything
	// after that is a gap nobody evaluated.
	sch.LastEvaluated = "2026-07-25T10:30"
	store.addSchedule(sch)

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 14, 0, 5, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// 11:00, 12:00 and 13:00 went by unevaluated.
	missed := missedFirings(store)
	if len(missed) != 3 {
		t.Fatalf("want 3 missed firing rows for 11:00/12:00/13:00, got %d (%v)", len(missed), store.firings)
	}
	for _, f := range missed {
		if f.EventID != "" {
			t.Fatalf("a missed occurrence produced no event: %+v", f)
		}
	}

	// …and the user is told once, not three times.
	evs := missedEvents(store)
	if len(evs) != 1 {
		t.Fatalf("want exactly ONE schedule.missed event for one gap, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Project != "acme" || ev.Envelope.Source != agentdb.EventSourceSchedule || ev.Envelope.Depth != 0 {
		t.Fatalf("schedule.missed must carry the schedule envelope: %+v", ev)
	}
	for _, want := range []string{"3 scheduled occurrence", sch.ID, "0 * * * *", "tweet-author",
		"2026-07-25T11:00", "2026-07-25T13:00", "never replayed", "hourly check"} {
		if !strings.Contains(ev.Text, want) {
			t.Fatalf("schedule.missed text must name %q, got:\n%s", want, ev.Text)
		}
	}

	// The whole point of not replaying: one job, for 14:00 and nothing else.
	if len(starter.jobs) != 1 {
		t.Fatalf("a restart must start ZERO backlog jobs (want 1 job, for the current minute), got %d", len(starter.jobs))
	}
	if n := eventsOfType(store, agentdb.EventTypeScheduleFired); n != 1 {
		t.Fatalf("want exactly one schedule.fired (the current minute), got %d", n)
	}
	if got := store.schedules[sch.ID].LastEvaluated; got != "2026-07-25T14:00" {
		t.Fatalf("the watermark must advance to the evaluated minute, got %q", got)
	}
}

// TestSchedulerFirstSightingReportsNothing: a schedule nobody has ever evaluated
// has no past to report, and inventing one would be its own false claim.
func TestSchedulerFirstSightingReportsNothing(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	// Created hours ago as far as the clock is concerned; never evaluated.
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "0 * * * *", "hourly check"))

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 14, 0, 5, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if evs := missedEvents(store); len(evs) != 0 {
		t.Fatalf("an unevaluated schedule must report nothing missed, got %d: %+v", len(evs), evs)
	}
	if got := store.schedules[sch.ID].LastEvaluated; got != "2026-07-25T14:00" {
		t.Fatalf("the first tick must set the watermark, got %q", got)
	}
}

// TestSchedulerWatermarkAdvancesWithNothingDue: a tick that matched nothing is
// still evidence — it is what makes the NEXT gap measurable.
func TestSchedulerWatermarkAdvancesWithNothingDue(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := agentdb.NewSchedule("acme", "tweet-author", "0 10 * * *", "morning tweet")
	sch.LastEvaluated = "2026-07-25T09:04"
	store.addSchedule(sch)

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 9, 5, 0, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(starter.jobs) != 0 || len(missedEvents(store)) != 0 || len(store.firings) != 0 {
		t.Fatalf("nothing was due: want no job, no event, no firing — got %d/%d/%d",
			len(starter.jobs), len(missedEvents(store)), len(store.firings))
	}
	if got := store.schedules[sch.ID].LastEvaluated; got != "2026-07-25T09:05" {
		t.Fatalf("the watermark must advance even when nothing matched, got %q", got)
	}
}

// TestSchedulerCapsMissedRecords: a `* * * * *` schedule and a long outage must
// not turn one tick into ten thousand INSERTs — but the COUNT must stay true.
func TestSchedulerCapsMissedRecords(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "noisy"))
	sch := agentdb.NewSchedule("acme", "noisy", "* * * * *", "every minute")
	// Thirty days behind: well past the catch-up window.
	sch.LastEvaluated = "2026-06-25T14:00"
	store.addSchedule(sch)

	starter := &recordingStarter{}
	now := time.Date(2026, 7, 25, 14, 0, 5, 0, time.UTC)
	s, _ := newTestScheduler(t, store, starter, now)
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if got := len(missedFirings(store)); got != scheduleMaxMissedRecorded {
		t.Fatalf("want the cap (%d) missed rows, got %d", scheduleMaxMissedRecorded, got)
	}
	evs := missedEvents(store)
	if len(evs) != 1 {
		t.Fatalf("want one schedule.missed event, got %d", len(evs))
	}
	// Every minute of the seven-day window, and the event says so — the cap
	// shortens the evidence, never the count.
	wantTotal := int(scheduleCatchUpWindow / time.Minute)
	if !strings.Contains(evs[0].Text, fmt.Sprintf("%d scheduled occurrence", wantTotal)) {
		t.Fatalf("the event must report the true total (%d), got:\n%s", wantTotal, evs[0].Text)
	}
	for _, want := range []string{"60 most recent", "not included in the count"} {
		if !strings.Contains(evs[0].Text, want) {
			t.Fatalf("a truncated report must say so (%q), got:\n%s", want, evs[0].Text)
		}
	}
	if len(starter.jobs) != 1 {
		t.Fatalf("still zero backlog jobs: want 1 (this minute), got %d", len(starter.jobs))
	}
}

// TestSchedulerReportsClaimedOccurrenceThatProducedNothing is RD11's NARROW
// WINDOW seen from a later tick: an occurrence claimed by a process that died
// before it could write the job. The row exists, so nothing will ever retry it —
// and before this change nothing said so either.
func TestSchedulerReportsClaimedOccurrenceThatProducedNothing(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := agentdb.NewSchedule("acme", "tweet-author", "0 * * * *", "hourly check")
	sch.LastEvaluated = "2026-07-25T10:30"
	store.addSchedule(sch)
	// The remnant: 11:00 claimed, no event id, not marked missed.
	store.firings[sch.ID+"@2026-07-25T11:00"] = &agentdb.ScheduleFiring{
		ID: "firing-orphan", ScheduleID: sch.ID, Project: "acme", ScheduledFor: "2026-07-25T11:00",
	}

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 12, 0, 5, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if !store.firings[sch.ID+"@2026-07-25T11:00"].Missed {
		t.Fatalf("a claimed occurrence with no event must be marked missed")
	}
	evs := missedEvents(store)
	if len(evs) != 1 {
		t.Fatalf("want one schedule.missed event, got %d", len(evs))
	}
	if !strings.Contains(evs[0].Text, "2026-07-25T11:00") {
		t.Fatalf("the report must name the orphaned occurrence, got:\n%s", evs[0].Text)
	}
	// And it is reported ONCE: a second tick over a fresh gap must not re-announce it.
	store.events = map[string]*agentdb.ProjectEvent{}
	s2, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 13, 0, 5, 0, time.UTC))
	if err := s2.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	for _, ev := range missedEvents(store) {
		if strings.Contains(ev.Text, "2026-07-25T11:00") {
			t.Fatalf("a missed occurrence must be announced once, not every tick:\n%s", ev.Text)
		}
	}
}

// TestSchedulerFiringJobFailureLeavesNoFalseNotice is the same window seen from
// INSIDE the failing tick, and it is the half doc 22 named: a kill between the
// event and the delivery left a `schedule.fired` in the user's feed for a job
// that never ran. The write is now one transaction, so the feed gets the truth
// instead — `schedule.missed`.
func TestSchedulerFiringJobFailureLeavesNoFalseNotice(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "0 * * * *", "hourly check"))
	store.failFiringJob = fmt.Errorf("the database went away mid-write")

	starter := &recordingStarter{}
	s, _ := newTestScheduler(t, store, starter, time.Date(2026, 7, 25, 14, 0, 5, 0, time.UTC))
	// The tick reports the firing error; that is not the assertion.
	_ = s.Tick(context.Background())

	if n := eventsOfType(store, agentdb.EventTypeScheduleFired); n != 0 {
		t.Fatalf("no schedule.fired may survive a failed job write, got %d", n)
	}
	if len(store.deliveries) != 0 {
		t.Fatalf("no delivery may exist for a job that was never written, got %d", len(store.deliveries))
	}
	if len(missedFirings(store)) != 1 {
		t.Fatalf("the spent occurrence must be marked missed: %v", store.firings)
	}
	evs := missedEvents(store)
	if len(evs) != 1 {
		t.Fatalf("want one schedule.missed event, got %d", len(evs))
	}
	if !strings.Contains(evs[0].Text, "the database went away mid-write") {
		t.Fatalf("the report must carry the cause, got:\n%s", evs[0].Text)
	}
	_ = sch
}

// TestSchedulerReportsMissedSessionOccurrences: session-mode schedules (T9) get
// the same honesty. Nothing is sent — a stale "good morning" at 11am is worse
// than none — but the occurrences are recorded and announced.
func TestSchedulerReportsMissedSessionOccurrences(t *testing.T) {
	store := newFakeDispatchStore()
	store.addSession("acme", "morning-desk")
	sch := agentdb.NewSessionSchedule("acme", "morning-desk", "0 * * * *", "good morning")
	sch.LastEvaluated = "2026-07-25T10:30"
	store.addSchedule(sch)

	msgr := &recordingMessenger{}
	s := newTestSessionScheduler(t, store, msgr, time.Date(2026, 7, 25, 13, 0, 5, 0, time.UTC))
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if got := len(missedFirings(store)); got != 2 {
		t.Fatalf("want 2 missed occurrences (11:00, 12:00), got %d (%v)", got, store.firings)
	}
	evs := missedEvents(store)
	if len(evs) != 1 {
		t.Fatalf("want one schedule.missed event, got %d", len(evs))
	}
	if !strings.Contains(evs[0].Text, "session morning-desk") {
		t.Fatalf("the report must name the session target, got:\n%s", evs[0].Text)
	}
	// One message, for the minute we are actually in — never a backlog of them.
	if len(msgr.sends) != 1 {
		t.Fatalf("want exactly one message (13:00), got %d", len(msgr.sends))
	}
}
