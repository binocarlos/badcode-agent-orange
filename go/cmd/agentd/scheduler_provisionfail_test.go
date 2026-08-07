package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// tickMinutes drives one scheduler per minute (a fresh scheduler each time is
// exactly what a `* * * * *` schedule sees: one evaluated minute per tick).
func tickMinutes(t *testing.T, store *fakeDispatchStore, starter sessionStarter, from time.Time, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		s, _ := newTestScheduler(t, store, starter, from.Add(time.Duration(i)*time.Minute))
		if err := s.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
}

// TestScheduleThatCannotProvisionIsEventuallyDisabled is the schedule storm: 53
// abandoned `* * * * *` rows, every firing failing to provision, retrying for
// ever and pinning the host's port pool at 100. §8.6 already disables a schedule
// whose worker is gone; a schedule that can NEVER provision is the same class of
// problem and must stop by itself.
func TestScheduleThatCannotProvisionIsEventuallyDisabled(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	starter := &recordingStarter{err: fmt.Errorf("create session: provision: port pool exhausted")}
	tickMinutes(t, store, starter, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC), 20)

	if store.schedules[sch.ID].Enabled {
		t.Fatalf("a schedule that failed to provision 20 minutes running is still enabled — "+
			"it will keep firing for ever (deliveries created: %d)", len(store.deliveries))
	}
	t.Logf("disabled with rationale: %q", store.disabled[sch.ID])
	if !strings.Contains(store.disabled[sch.ID], "provision") {
		t.Errorf("the reason must be recorded, got %q", store.disabled[sch.ID])
	}
}

// TestScheduleWhoseJOBFails is the line that must not be crossed: a worker whose
// JOBS keep failing is exactly what §8.7's self-improvement loop exists to fix.
// Provisioning succeeded; the schedule must stay enabled.
func TestScheduleWhoseJOBFailsStaysEnabled(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	// The session starts every time; the TURN then fails, which is a legitimate
	// job outcome recorded on the delivery.
	starter := &failingJobStarter{}
	tickMinutes(t, store, starter, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC), 20)

	if !store.schedules[sch.ID].Enabled {
		t.Fatalf("a schedule whose JOBS fail was retired — that silences the very loop "+
			"§8.7 uses to fix a bad worker (rationale: %q)", store.disabled[sch.ID])
	}
}

// TestProvisionFailureStreakResetsOnASuccess pins the word CONSECUTIVE: a
// schedule that fails, recovers, and fails again must never accumulate its way
// to a disable across an intervening success.
func TestProvisionFailureStreakResetsOnASuccess(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	broken := &recordingStarter{err: fmt.Errorf("provision: port pool exhausted")}
	working := &recordingStarter{}
	base := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	// Four failures — one short of the ceiling.
	tickMinutes(t, store, broken, base, agentdb.ScheduleMaxProvisionFailures-1)
	if got := store.schedules[sch.ID].ProvisionFailures; got != agentdb.ScheduleMaxProvisionFailures-1 {
		t.Fatalf("streak = %d, want %d", got, agentdb.ScheduleMaxProvisionFailures-1)
	}
	// One success clears it.
	tickMinutes(t, store, working, base.Add(10*time.Minute), 1)
	if got := store.schedules[sch.ID].ProvisionFailures; got != 0 {
		t.Fatalf("a successful start must reset the streak, got %d", got)
	}
	// Four more failures must therefore still not be enough.
	tickMinutes(t, store, broken, base.Add(20*time.Minute), agentdb.ScheduleMaxProvisionFailures-1)
	if !store.schedules[sch.ID].Enabled {
		t.Fatalf("failures either side of a success were counted together — the streak is not consecutive")
	}
}

// TestQueuedFiringIsNotAProvisionFailure: the capacity gate saying "wait" is the
// system working (§8.4 step 7), not a schedule that cannot succeed. Counting it
// would retire the busiest workers first.
func TestQueuedFiringIsNotAProvisionFailure(t *testing.T) {
	store := newFakeDispatchStore()
	worker := agentdb.NewWorker("acme", "tweet-author")
	worker.MaxInstances = 1
	store.addWorker(worker)
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))
	// One delivery already occupying the worker's only instance.
	store.deliveries = append(store.deliveries, &agentdb.EventDelivery{
		ID: "busy", Project: "acme", Worker: "tweet-author", Status: agentdb.DeliveryRunning,
	})

	tickMinutes(t, store, &recordingStarter{}, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC), 20)

	if !store.schedules[sch.ID].Enabled {
		t.Fatalf("a schedule queued behind a busy worker was disabled: %q", store.disabled[sch.ID])
	}
	if got := store.schedules[sch.ID].ProvisionFailures; got != 0 {
		t.Fatalf("queued firings were counted as provision failures: %d", got)
	}
}

// TestDisableSpendsTheStreak: after an auto-disable, a human who re-enables the
// schedule must get a full budget back, not a row that retires on its next
// firing.
func TestDisableSpendsTheStreak(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	sch := store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	broken := &recordingStarter{err: fmt.Errorf("provision: port pool exhausted")}
	tickMinutes(t, store, broken, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC), 10)

	row := store.schedules[sch.ID]
	if row.Enabled {
		t.Fatalf("expected the schedule to be disabled")
	}
	if row.ProvisionFailures != 0 {
		t.Fatalf("the streak survived the disable (%d) — a re-enable would retire immediately", row.ProvisionFailures)
	}
}

// failingJobStarter provisions fine and then fails the turn.
type failingJobStarter struct{ n int }

func (f *failingJobStarter) StartJob(ctx context.Context, in startJobInput) (string, error) {
	f.n++
	sid := fmt.Sprintf("sess-%d", f.n)
	if in.OnSessionCreated != nil {
		if err := in.OnSessionCreated(ctx, sid); err != nil {
			return "", err
		}
	}
	if in.OnSessionEnded != nil {
		_ = in.OnSessionEnded(ctx, sid, fmt.Errorf("the model refused"))
	}
	return sid, nil
}
