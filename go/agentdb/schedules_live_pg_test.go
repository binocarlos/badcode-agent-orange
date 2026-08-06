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
		_ = s.PurgeConfigEvents(ctx, project)
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

// TestSchedulesLivePGProvisionFailures is migration 031 against the real DDL:
// the two streak columns exist, their SQL DEFAULTs apply to a row inserted
// AROUND the store (the convention is DEFAULTs in the migration, never a gorm
// `default:` tag), the increment is done in SQL so two agentds cannot lose one,
// and a reset back to zero is actually writable.
func TestSchedulesLivePGProvisionFailures(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveScheduleProject(t, s)

	// A row written around the store gets the column DEFAULTs.
	id := uuid.New().String()
	if err := s.DB().WithContext(ctx).Exec(
		`INSERT INTO schedules (id, project, worker, cron, input, enabled, created_at, updated_at)
		 VALUES (?, ?, 'tweeter', '* * * * *', 'tweet', true, 0, 0)`, id, project).Error; err != nil {
		t.Fatalf("raw insert (are the 031 DEFAULTs missing?): %v", err)
	}
	sch, err := s.GetSchedule(ctx, project, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if sch.ProvisionFailures != 0 || sch.LastProvisionError != "" {
		t.Fatalf("031 defaults not applied: %+v", sch)
	}

	for want := 1; want <= ScheduleMaxProvisionFailures; want++ {
		n, err := s.NoteScheduleProvisionFailure(ctx, project, id, "port pool exhausted")
		if err != nil {
			t.Fatalf("note %d: %v", want, err)
		}
		if n != want {
			t.Fatalf("streak = %d, want %d (the increment must be SQL, not read-modify-write)", n, want)
		}
	}
	if err := s.ClearScheduleProvisionFailures(ctx, project, id); err != nil {
		t.Fatalf("clear: %v", err)
	}
	sch, _ = s.GetSchedule(ctx, project, id)
	if sch.ProvisionFailures != 0 || sch.LastProvisionError != "" {
		t.Fatalf("reset to zero did not persist (a gorm `default:` tag would do exactly this): %+v", sch)
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

// TestSessionAwaitsHumanLivePG runs the read the §8.4 dispatch gate parks a
// delivery on against real Postgres — the sqlite cases prove the logic, this
// proves the query. It is the one thing standing between "the job asked for a
// human" and the delivery being closed `ok` as if the job were done.
func TestSessionAwaitsHumanLivePG(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveScheduleProject(t, s)
	session := uuid.New().String() // session_id is varchar(36) — a bare uuid, no prefix

	awaits, err := s.SessionAwaitsHuman(ctx, project, session)
	if err != nil || awaits {
		t.Fatalf("a session with no request awaits nobody: %v err=%v", awaits, err)
	}

	req, err := s.CreateAttentionRequest(ctx, &AttentionRequest{
		Project: project, SessionID: session, Worker: "tweet-author",
		Message: "sign off on this draft", Channel: "webhook", Delivered: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if awaits, err = s.SessionAwaitsHuman(ctx, project, session); err != nil || !awaits {
		t.Fatalf("an open request must be visible: %v err=%v", awaits, err)
	}
	// Another project's read must not see it (the query is tenant-scoped in SQL).
	if awaits, err = s.SessionAwaitsHuman(ctx, project+"-other", session); err != nil || awaits {
		t.Fatalf("cross-project read leaked: %v err=%v", awaits, err)
	}

	if err := s.MarkAttentionTimedOut(ctx, req.ID, 4000); err != nil {
		t.Fatalf("time out: %v", err)
	}
	if awaits, err = s.SessionAwaitsHuman(ctx, project, session); err != nil || awaits {
		t.Fatalf("a resolved request is not outstanding: %v err=%v", awaits, err)
	}
}

// TestSchedulesLivePGTargetSession is migration 036's live half. The sqlite
// tests prove the XOR in Go and nothing about the column: only Postgres runs
// the ALTER, and only Postgres can tell us that the pre-036 rows this deployment
// already holds came through it with a usable value rather than a NULL that
// GORM would scan into a string and fail on.
func TestSchedulesLivePGTargetSession(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveScheduleProject(t, s)

	// A worker-mode row written around the store — i.e. exactly the shape every
	// schedule in the database had before 036 — must read back with an empty
	// target, not an error.
	legacyID := uuid.New().String()
	if err := s.DB().WithContext(ctx).Exec(
		`INSERT INTO schedules (id, project, worker, cron, input, enabled, created_at, updated_at)
		 VALUES (?, ?, 'tweeter', '* * * * *', 'tweet', true, 0, 0)`, legacyID, project).Error; err != nil {
		t.Fatalf("raw insert (is the 036 DEFAULT missing?): %v", err)
	}
	legacy, err := s.GetSchedule(ctx, project, legacyID)
	if err != nil {
		t.Fatalf("get the pre-036 row: %v", err)
	}
	if legacy.TargetSession != "" {
		t.Fatalf("a worker schedule must have no target session: %+v", legacy)
	}

	created, err := s.CreateSchedule(ctx,
		NewSessionSchedule(project, "hypothesis-a", "0 7 * * *", "research and update"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create session schedule: %v", err)
	}
	got, err := s.GetSchedule(ctx, project, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TargetSession != "hypothesis-a" || got.Worker != "" {
		t.Fatalf("session mode did not round-trip through Postgres: %+v", got)
	}

	// ListEnabledSchedules is what the scheduler polls, and it is the read that
	// has to carry the target — a firing that lost it would silently become a
	// targetless worker schedule.
	enabled, err := s.ListEnabledSchedules(ctx)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	found := false
	for _, sch := range enabled {
		if sch.ID == created.ID {
			found = true
			if sch.TargetSession != "hypothesis-a" {
				t.Fatalf("the scheduler's own read lost the target: %+v", sch)
			}
		}
	}
	if !found {
		t.Fatalf("the session schedule is not in the scheduler's poll")
	}
}

// TestSchedulesLivePGWatermark is migration 041 against the real DDL (RD11).
//
// It has to be a live test rather than a sqlite one for the same reason the
// provision-failure case above does: the sqlite schema comes from AutoMigrate
// and would happily invent the columns whether or not the migration exists. The
// DEFAULTs are the other half — a row written AROUND the store must read back as
// "never evaluated" and "not missed", never as NULL.
func TestSchedulesLivePGWatermark(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveScheduleProject(t, s)

	id := uuid.New().String()
	if err := s.DB().WithContext(ctx).Exec(
		`INSERT INTO schedules (id, project, worker, cron, input, enabled, created_at, updated_at)
		 VALUES (?, ?, 'tweeter', '0 * * * *', 'tweet', true, 0, 0)`, id, project).Error; err != nil {
		t.Fatalf("raw insert (is migration 041's DEFAULT missing?): %v", err)
	}
	sch, err := s.GetSchedule(ctx, project, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if sch.LastEvaluated != "" {
		t.Fatalf("a row written around the store must read as never evaluated, got %q", sch.LastEvaluated)
	}

	if err := s.NoteScheduleEvaluated(ctx, project, id, "2026-08-06T09:30"); err != nil {
		t.Fatalf("note: %v", err)
	}
	// Backwards is refused by the WHERE clause, not by the caller.
	if err := s.NoteScheduleEvaluated(ctx, project, id, "2026-08-06T09:00"); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	sch, _ = s.GetSchedule(ctx, project, id)
	if sch.LastEvaluated != "2026-08-06T09:30" {
		t.Fatalf("watermark = %q, want it held at 09:30", sch.LastEvaluated)
	}
	// The watermark is not an edit: `updated_at` must not move (UpdateColumns,
	// not Updates — gorm would otherwise stamp it every single minute).
	if sch.UpdatedAt != 0 {
		t.Fatalf("the watermark write must not touch updated_at, got %d", sch.UpdatedAt)
	}

	// The `missed` mark, through the real UNIQUE index.
	f, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: id, Project: project, ScheduledFor: "2026-08-06T09:00", Missed: true,
	})
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	rows, err := s.ListFirings(ctx, project, id, 10)
	if err != nil {
		t.Fatalf("list firings: %v", err)
	}
	if len(rows) != 1 || !rows[0].Missed || rows[0].ID != f.ID {
		t.Fatalf("the missed mark must survive a round trip: %+v", rows)
	}
}
