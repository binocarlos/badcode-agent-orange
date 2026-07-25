package agentdb

import (
	"context"
	"testing"
)

// TestSessionTriggerEvent covers the lookup the §8.2 internal emitters use to
// find the depth their event sits one level below: the delivery row links a
// session back to the event that caused it, and its absence means a human
// started the job (depth 0).
func TestSessionTriggerEvent(t *testing.T) {
	ctx := context.Background()

	t.Run("resolves the event through the delivery", func(t *testing.T) {
		s := newEventStore(t)
		ev, err := s.CreateProjectEvent(ctx, &ProjectEvent{
			Project: "acme", Type: "email.received", Text: "hello",
			Envelope: EventEnvelope{Source: EventSourceExternal, Depth: 2},
		})
		if err != nil {
			t.Fatalf("create event: %v", err)
		}
		sub := seedSubscription(t, s, "acme", "email.*", "email-answerer")
		d, _, err := s.EnsureDelivery(ctx, &EventDelivery{
			Project: "acme", EventID: ev.ID, SubscriptionID: sub.ID,
		})
		if err != nil {
			t.Fatalf("ensure delivery: %v", err)
		}
		if _, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID, DeliveryStatusUpdate{
			Status: DeliveryRunning, SessionID: "sess-1",
		}); err != nil {
			t.Fatalf("update delivery: %v", err)
		}

		got, err := s.SessionTriggerEvent(ctx, "sess-1")
		if err != nil {
			t.Fatalf("SessionTriggerEvent: %v", err)
		}
		if got == nil {
			t.Fatal("no trigger event found for a dispatched session")
		}
		if got.ID != ev.ID {
			t.Errorf("event id = %q, want %q", got.ID, ev.ID)
		}
		if got.Envelope.Depth != 2 {
			t.Errorf("depth = %d, want 2 (emitters add one to this)", got.Envelope.Depth)
		}
	})

	t.Run("a session with no delivery has no trigger", func(t *testing.T) {
		s := newEventStore(t)
		got, err := s.SessionTriggerEvent(ctx, "sess-human")
		if err != nil {
			t.Fatalf("SessionTriggerEvent: %v", err)
		}
		if got != nil {
			t.Errorf("got %#v, want nil — a human-started job is depth 0", got)
		}
	})

	t.Run("a delivery pointing at a vanished event is not an error", func(t *testing.T) {
		s := newEventStore(t)
		sub := seedSubscription(t, s, "acme", "email.*", "email-answerer")
		d, _, err := s.EnsureDelivery(ctx, &EventDelivery{
			Project: "acme", EventID: "gone", SubscriptionID: sub.ID,
		})
		if err != nil {
			t.Fatalf("ensure delivery: %v", err)
		}
		if _, err := s.UpdateDeliveryStatus(ctx, "acme", d.ID, DeliveryStatusUpdate{
			Status: DeliveryRunning, SessionID: "sess-2",
		}); err != nil {
			t.Fatalf("update delivery: %v", err)
		}
		got, err := s.SessionTriggerEvent(ctx, "sess-2")
		if err != nil {
			t.Fatalf("SessionTriggerEvent: %v", err)
		}
		if got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})

	t.Run("an empty session id is refused", func(t *testing.T) {
		s := newEventStore(t)
		if _, err := s.SessionTriggerEvent(ctx, "  "); err == nil {
			t.Fatal("expected an error for a blank session id")
		}
	})
}

// TestFailureReasonVocabulary pins the closed worker.failed reason set (§8.2).
func TestFailureReasonVocabulary(t *testing.T) {
	want := []string{"error", "lost"}
	if len(FailureReasons) != len(want) {
		t.Fatalf("FailureReasons = %v, want %v", FailureReasons, want)
	}
	for i, v := range want {
		if FailureReasons[i] != v {
			t.Fatalf("FailureReasons = %v, want %v", FailureReasons, want)
		}
	}
	for _, v := range want {
		if !ValidFailureReason(v) {
			t.Errorf("ValidFailureReason(%q) = false", v)
		}
	}
	if ValidFailureReason("dropped") {
		t.Error("ValidFailureReason(\"dropped\") = true; the vocabulary is closed")
	}
	if EventTypeWorkerFinished != "worker.finished" || EventTypeWorkerFailed != "worker.failed" {
		t.Errorf("internal event type names drifted: %q / %q", EventTypeWorkerFinished, EventTypeWorkerFailed)
	}
}
