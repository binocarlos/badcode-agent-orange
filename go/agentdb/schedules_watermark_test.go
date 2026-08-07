package agentdb

// schedules_watermark_test.go — the store half of RD11: the per-schedule
// watermark, the `missed` mark on a firing, and the ONE transaction that stops a
// `schedule.fired` event existing for a job that was never written.

import (
	"context"
	"testing"
)

func TestNoteScheduleEvaluatedNeverRewinds(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()
	sch, err := s.CreateSchedule(ctx, NewSchedule("acme", "tweet-author", "0 10 * * *", "tweet"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sch.LastEvaluated != "" {
		t.Fatalf("a new schedule has never been evaluated, got %q", sch.LastEvaluated)
	}

	if err := s.NoteScheduleEvaluated(ctx, "acme", sch.ID, "2026-08-06T09:30"); err != nil {
		t.Fatalf("note: %v", err)
	}
	got, err := s.GetSchedule(ctx, "acme", sch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastEvaluated != "2026-08-06T09:30" {
		t.Fatalf("watermark not stored, got %q", got.LastEvaluated)
	}
	// The write must not look like an edit: the config log is the record of
	// decisions, and nobody decided this.
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("the watermark must append no config event (want only the create), got %d", len(evs))
	}

	// A lagging second agentd must not drag the watermark backwards and make the
	// same minutes look missed twice.
	if err := s.NoteScheduleEvaluated(ctx, "acme", sch.ID, "2026-08-06T09:00"); err != nil {
		t.Fatalf("rewind attempt returned an error: %v", err)
	}
	got, err = s.GetSchedule(ctx, "acme", sch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastEvaluated != "2026-08-06T09:30" {
		t.Fatalf("the watermark moved backwards to %q", got.LastEvaluated)
	}

	// Forwards still works.
	if err := s.NoteScheduleEvaluated(ctx, "acme", sch.ID, "2026-08-06T09:31"); err != nil {
		t.Fatalf("note: %v", err)
	}
	got, _ = s.GetSchedule(ctx, "acme", sch.ID)
	if got.LastEvaluated != "2026-08-06T09:31" {
		t.Fatalf("the watermark did not advance, got %q", got.LastEvaluated)
	}
}

func TestClaimFiringCarriesTheMissedMark(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()

	f, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: "sched-1", Project: "acme", ScheduledFor: "2026-08-06T09:00", Missed: true,
	})
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	rows, err := s.ListFirings(ctx, "acme", "sched-1", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || !rows[0].Missed || rows[0].EventID != "" {
		t.Fatalf("a missed occurrence must persist as missed with no event: %+v", rows)
	}

	// A peer catching up on the same outage claims nothing and is handed the row
	// it lost to — including its `missed` mark, which is what stops a second
	// announcement of the same minute.
	again, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: "sched-1", Project: "acme", ScheduledFor: "2026-08-06T09:00", Missed: true,
	})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatalf("an occurrence may only be claimed once")
	}
	if again.ID != f.ID || !again.Missed {
		t.Fatalf("the loser must be handed the stored row: %+v", again)
	}

	// And a firing that DID happen can be marked missed after the fact — the
	// claimed-but-produced-nothing remnant.
	live, _, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: "sched-1", Project: "acme", ScheduledFor: "2026-08-06T10:00",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.MarkFiringMissed(ctx, live.ID); err != nil {
		t.Fatalf("mark missed: %v", err)
	}
	rows, _ = s.ListFirings(ctx, "acme", "sched-1", 10)
	for _, r := range rows {
		if r.ID == live.ID && !r.Missed {
			t.Fatalf("MarkFiringMissed did not stick: %+v", r)
		}
	}
	if err := s.MarkFiringMissed(ctx, "no-such-firing"); err == nil {
		t.Fatalf("marking a firing that does not exist must not report success")
	}
}

// TestRecordFiringJobIsAtomic is RD11's narrow window, at the store: the event,
// the delivery and the firing's event stamp are one transaction, so the user's
// feed can never hold a `schedule.fired` for a job that was never written.
func TestRecordFiringJobIsAtomic(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()

	firing, _, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: "sched-1", Project: "acme", ScheduledFor: "2026-08-06T09:00",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	ev, d, err := s.RecordFiringJob(ctx, firing.ID,
		&ProjectEvent{Project: "acme", Type: EventTypeScheduleFired, Text: "tweet",
			Envelope: EventEnvelope{Source: EventSourceSchedule}},
		&EventDelivery{Project: "acme", SubscriptionID: "sched-1", ScheduleID: "sched-1", Worker: "tweet-author"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if d.EventID != ev.ID {
		t.Fatalf("the delivery must point at the event it was written with: %q vs %q", d.EventID, ev.ID)
	}
	if d.Status != DeliveryPending {
		t.Fatalf("a firing's delivery starts pending, got %q", d.Status)
	}
	rows, _ := s.ListFirings(ctx, "acme", "sched-1", 10)
	if len(rows) != 1 || rows[0].EventID != ev.ID {
		t.Fatalf("the firing must be stamped inside the same transaction: %+v", rows)
	}

	// Now the failure. A second occurrence, whose delivery collides on the
	// primary key — standing in for any write that fails half way through.
	second, _, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: "sched-1", Project: "acme", ScheduledFor: "2026-08-06T10:00",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	before, err := s.ListProjectEvents(ctx, ProjectEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	_, _, err = s.RecordFiringJob(ctx, second.ID,
		&ProjectEvent{Project: "acme", Type: EventTypeScheduleFired, Text: "tweet",
			Envelope: EventEnvelope{Source: EventSourceSchedule}},
		&EventDelivery{ID: d.ID, Project: "acme", SubscriptionID: "sched-1", ScheduleID: "sched-1", Worker: "tweet-author"})
	if err == nil {
		t.Fatalf("a colliding delivery must fail the whole write")
	}
	after, err := s.ListProjectEvents(ctx, ProjectEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("a failed job write must leave NO event behind: %d → %d", len(before), len(after))
	}
	rows, _ = s.ListFirings(ctx, "acme", "sched-1", 10)
	for _, r := range rows {
		if r.ID == second.ID && r.EventID != "" {
			t.Fatalf("a rolled-back write must not stamp the firing: %+v", r)
		}
	}
}
