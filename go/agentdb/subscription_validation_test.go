package agentdb

// RD19, write-time half: a subscription that can never fire must be refused
// where the user is looking, not discovered (if ever) in agentd's log.

import (
	"context"
	"strings"
	"testing"
)

// TestSubscriptionFilterKeysAreValidatedAtWriteTime pins the filter half.
// `envelopeFilterMatches` returns false for any key the envelope does not
// carry, so a mistyped filter key does not fail — it silently matches nothing
// for ever. The legal set is knowable at write time.
func TestSubscriptionFilterKeysAreValidatedAtWriteTime(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	seedWorkerRow(t, s, "acme", "reviewer")

	_, err := s.CreateSubscription(ctx, &Subscription{
		Project: "acme", EventType: "worker.finished", Worker: "reviewer", Enabled: true,
		Filter: JSONMap{"wroker": "answerer"},
	}, ConfigWrite{})
	if err == nil {
		t.Fatalf("a filter key the envelope cannot carry must be refused at write time — " +
			"accepted, it matches nothing for ever and no job ever runs")
	}
	// Cron-validator diagnostics: name the offending field, give the legal set.
	if !strings.Contains(err.Error(), `"wroker"`) {
		t.Fatalf("the error must name the offending key: %v", err)
	}
	for _, legal := range []string{"worker", "source", "depth", "interactive", "session_id"} {
		if !strings.Contains(err.Error(), legal) {
			t.Fatalf("the error must list the legal key %q: %v", legal, err)
		}
	}

	// Every legal key is accepted, and the legal set is the envelope's own wire
	// keys — derived from the struct tags, so it cannot drift from what the
	// router actually filters against.
	full := JSONMap{}
	for _, k := range EnvelopeFilterKeys() {
		full[k] = ""
	}
	for _, want := range []string{"depth", "source", "worker", "session_id", "interactive",
		"attention_requested", "reason"} {
		if _, ok := full[want]; !ok {
			t.Fatalf("EnvelopeFilterKeys lost %q — the legal set has drifted from EventEnvelope", want)
		}
	}
	if _, err := s.CreateSubscription(ctx, &Subscription{
		Project: "acme", EventType: "worker.finished", Worker: "reviewer", Enabled: true, Filter: full,
	}, ConfigWrite{}); err != nil {
		t.Fatalf("every envelope field must be a legal filter key: %v", err)
	}

	// An update is a write too: the same guard applies.
	sub := seedSubscription(t, s, "acme", "email.received", "reviewer")
	sub.Filter = JSONMap{"nonsense": "x"}
	if _, err := s.UpdateSubscription(ctx, sub, ConfigWrite{}); err == nil {
		t.Fatalf("UpdateSubscription must refuse an unknown filter key too")
	}
}

// TestSubscriptionWorkerMustExistAtWriteTime pins the worker half: a
// subscription naming a worker that does not exist delivers to nobody.
func TestSubscriptionWorkerMustExistAtWriteTime(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	seedWorkerRow(t, s, "acme", "email-reviewer")

	_, err := s.CreateSubscription(ctx, &Subscription{
		Project: "acme", EventType: "email.received", Worker: "email-reveiwer", Enabled: true,
	}, ConfigWrite{})
	if err == nil {
		t.Fatalf("a subscription naming a worker that does not exist must be refused at write time")
	}
	if !strings.Contains(err.Error(), `"email-reveiwer"`) {
		t.Fatalf("the error must name the worker that was asked for: %v", err)
	}
	if !strings.Contains(err.Error(), "email-reviewer") {
		t.Fatalf("the error must list the workers the project does have — that is the diagnostic: %v", err)
	}

	// The tenancy boundary is part of the check: another project's worker of the
	// same name does not satisfy it.
	seedWorkerRow(t, s, "globex", "shared-name")
	if _, err := s.CreateSubscription(ctx, &Subscription{
		Project: "acme", EventType: "email.received", Worker: "shared-name", Enabled: true,
	}, ConfigWrite{}); err == nil {
		t.Fatalf("another project's worker must not satisfy the existence check")
	}

	// And the happy path still works, through create and update.
	sub, err := s.CreateSubscription(ctx, &Subscription{
		Project: "acme", EventType: "email.received", Worker: "email-reviewer", Enabled: true,
	}, ConfigWrite{})
	if err != nil {
		t.Fatalf("a subscription naming a real worker must be accepted: %v", err)
	}
	sub.Worker = "email-reveiwer"
	if _, err := s.UpdateSubscription(ctx, sub, ConfigWrite{}); err == nil {
		t.Fatalf("UpdateSubscription must refuse a worker that does not exist too")
	}
}
