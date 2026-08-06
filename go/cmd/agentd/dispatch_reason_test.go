package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// RD20: the dispatcher has always KNOWN why a job failed and only ever logged
// it, so the newcomer's answer to "why did my worker fail?" was `docker compose
// logs agentd`. These two tests pin the two failure paths writing the reason
// onto the delivery row, which is what the API serves and the UI reads.

// TestFailedProvisionRecordsTheReasonOnTheDelivery — the job never started.
func TestFailedProvisionRecordsTheReasonOnTheDelivery(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	starter := &recordingStarter{err: fmt.Errorf("create session: host port pool is exhausted")}
	tickMinutes(t, store, starter, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC), 1)

	d := onlyDelivery(t, store)
	if d.Status != agentdb.DeliveryFailed {
		t.Fatalf("status = %q, want failed", d.Status)
	}
	if !strings.Contains(d.FailureReason, "host port pool is exhausted") {
		t.Fatalf("the delivery must carry WHY it failed, got %q — without it the only "+
			"copy of the reason is agentd's stdout", d.FailureReason)
	}
}

// TestFailedJobRecordsTheReasonOnTheDelivery — provisioning worked, the turn
// did not. A different code path (OnSessionEnded), the same user question.
func TestFailedJobRecordsTheReasonOnTheDelivery(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	tickMinutes(t, store, &failingJobStarter{}, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC), 1)

	d := onlyDelivery(t, store)
	if d.Status != agentdb.DeliveryFailed {
		t.Fatalf("status = %q, want failed", d.Status)
	}
	if !strings.Contains(d.FailureReason, "the model refused") {
		t.Fatalf("a job that failed mid-turn must record why, got %q", d.FailureReason)
	}
}

// TestSuccessfulJobCarriesNoReason is the other half: a green row must never
// carry a red explanation.
func TestSuccessfulJobCarriesNoReason(t *testing.T) {
	store := newFakeDispatchStore()
	store.addWorker(agentdb.NewWorker("acme", "tweet-author"))
	store.addSchedule(agentdb.NewSchedule("acme", "tweet-author", "* * * * *", "tweet"))

	tickMinutes(t, store, &recordingStarter{}, time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC), 1)

	if d := onlyDelivery(t, store); d.FailureReason != "" {
		t.Fatalf("a delivery that did not fail must carry no reason, got %q", d.FailureReason)
	}
}

func onlyDelivery(t *testing.T, store *fakeDispatchStore) *agentdb.EventDelivery {
	t.Helper()
	if len(store.deliveries) != 1 {
		t.Fatalf("want exactly one delivery, got %d", len(store.deliveries))
	}
	return store.deliveries[0]
}
