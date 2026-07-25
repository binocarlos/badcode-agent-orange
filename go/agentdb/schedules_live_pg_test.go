package agentdb

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The Postgres-only half of migration 024: the real DDL, the UNIQUE index that
// makes a firing idempotent against a *concurrent* second scheduler (sqlite's
// AutoMigrate index is a weaker promise), and the columns the shared dispatch
// gate counts on. Skipped unless AGENTKIT_TEST_POSTGRES_URL is set.

// liveScheduleProject returns a per-run unique project id and cleans up every
// row this test wrote under it.
func liveScheduleProject(t *testing.T, s *Store) string {
	t.Helper()
	project := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		ctx := context.Background()
		_ = s.DB().WithContext(ctx).Exec("DELETE FROM schedule_firings WHERE project = ?", project).Error
		_ = s.DB().WithContext(ctx).Exec("DELETE FROM schedules WHERE project = ?", project).Error
		_ = s.DB().WithContext(ctx).Exec("DELETE FROM attention_requests WHERE project = ?", project).Error
		_ = s.DB().WithContext(ctx).Exec("DELETE FROM config_events WHERE project = ?", project).Error
	})
	return project
}

func TestSchedulesLivePGSchema024(t *testing.T) {
	s := openLivePG(t)

	tests := []struct {
		table string
		want  []string
	}{
		{"schedules", []string{"id", "project", "worker", "cron", "input", "enabled", "created_at", "updated_at"}},
		{"schedule_firings", []string{"id", "schedule_id", "scheduled_for", "project", "event_id", "fired_at"}},
		{"attention_requests", []string{
			"id", "project", "session_id", "worker", "message", "session_url", "channel",
			"delivered", "expires_at", "created_at", "answered_at", "timed_out_at",
		}},
		// The gate's denormalised columns (the E1 decision: a `worker` column, not
		// a synthetic subscription per schedule).
		{"event_deliveries", []string{"worker", "schedule_id"}},
		// The §9 stamp §8.2 copies onto the worker.finished envelope.
		{"agent_sessions", []string{"attention_requested"}},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			var cols []string
			if err := s.DB().Raw(
				`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, tc.table,
			).Scan(&cols).Error; err != nil {
				t.Fatalf("read columns: %v", err)
			}
			have := map[string]bool{}
			for _, c := range cols {
				have[c] = true
			}
			for _, c := range tc.want {
				if !have[c] {
					t.Fatalf("%s.%s missing (have %v)", tc.table, c, cols)
				}
			}
		})
	}
}

// TestSchedulesLivePGOccurrenceKeyIsUnique proves the idempotency guard is a
// DATABASE invariant, not a Go convention: a second insert of the same
// occurrence is refused by the index, which is what makes two agentd processes
// (or one that crashed mid-tick) unable to double-fire.
func TestSchedulesLivePGOccurrenceKeyIsUnique(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveScheduleProject(t, s)

	sch, err := s.CreateSchedule(ctx, NewSchedule(project, "tweet-author", "0 10 * * *", "write the morning tweet"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	const key = "2026-07-25T10:00"
	if _, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: sch.ID, Project: project, ScheduledFor: key,
	}); err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}

	// Raw insert bypassing ClaimFiring's read-then-write: only the index can stop
	// this, and it must.
	err = s.DB().WithContext(ctx).Exec(
		`INSERT INTO schedule_firings (id, schedule_id, scheduled_for, project, event_id, fired_at)
		 VALUES (?, ?, ?, ?, '', 0)`,
		uuid.New().String(), sch.ID, key, project).Error
	if err == nil {
		t.Fatalf("the unique occurrence index did not fire — a crash/retry could double-fire (§8.6)")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "duplicate") &&
		!strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("unexpected error from the occurrence index: %v", err)
	}

	// And the store's own path reports it as "already claimed", not as an error.
	if _, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: sch.ID, Project: project, ScheduledFor: key,
	}); err != nil || claimed {
		t.Fatalf("retry: want claimed=false err=nil, got claimed=%v err=%v", claimed, err)
	}
}

// TestSchedulesLivePGConfigLogDualWrite proves the schedule mutations land in
// the config log on the real database, in the same transaction (§15.4).
func TestSchedulesLivePGConfigLogDualWrite(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveScheduleProject(t, s)

	sch, err := s.CreateSchedule(ctx, NewSchedule(project, "marketing-manager", "0 9 * * MON", "reconcile the workforce"),
		ConfigWrite{Worker: "marketing-manager", Session: "s-1", Rationale: "weekly critique"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.DisableSchedule(ctx, project, sch.ID, ConfigWrite{Rationale: "worker no longer exists"}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project, Action: "schedule_*"})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected create + update, got %d", len(evs))
	}
	if evs[0].Action != ActionScheduleUpdate || evs[0].Rationale != "worker no longer exists" {
		t.Fatalf("disable did not log its reason: %+v", evs[0])
	}
	if evs[1].Action != ActionScheduleCreate || evs[1].ActorWorker != "marketing-manager" {
		t.Fatalf("create actor not recorded: %+v", evs[1])
	}
	// jsonb, really: the server can index into the payload.
	var worker string
	if err := s.DB().Raw(
		"SELECT payload->>'worker' FROM config_events WHERE id = ?", evs[1].ID,
	).Scan(&worker).Error; err != nil {
		t.Fatalf("jsonb query: %v", err)
	}
	if worker != "marketing-manager" {
		t.Fatalf("payload jsonb extraction: got %q", worker)
	}
}
