package agentdb

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// Embed the IANA database so the DST cases below hold on a machine with no
	// system zoneinfo (a scratch container, most CI images).
	_ "time/tzdata"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newScheduleStore returns a sqlite Store with the schedule tables, the config
// log (every schedule mutation writes one) and the event spine the firing path
// touches.
func newScheduleStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "schedules_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Schedule{}, &ScheduleFiring{}, &ConfigEvent{},
		&ProjectEvent{}, &Subscription{}, &EventDelivery{}, &Worker{}); err != nil {
		t.Fatalf("automigrate schedule tables: %v", err)
	}
	return &Store{gdb: db}
}

// ── The provision-failure streak (§8.6) ─────────────────────────────────────

// TestScheduleProvisionFailureStreak covers the counter's whole contract in one
// place: it counts up, it persists with its reason, a reset zeroes it, the
// counter writes stay OUT of the config log, and switching the schedule off
// spends the streak (so a re-enable starts from zero rather than retiring on its
// very next firing) while the DISABLE itself is logged with its reason.
func TestScheduleProvisionFailureStreak(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()

	sch, err := s.CreateSchedule(ctx, NewSchedule("acme", "tweeter", "* * * * *", "tweet"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sch.ProvisionFailures != 0 {
		t.Fatalf("a new schedule starts with no streak, got %d", sch.ProvisionFailures)
	}

	for want := 1; want <= 3; want++ {
		n, err := s.NoteScheduleProvisionFailure(ctx, "acme", sch.ID, "port pool exhausted")
		if err != nil {
			t.Fatalf("note %d: %v", want, err)
		}
		if n != want {
			t.Fatalf("streak = %d, want %d", n, want)
		}
	}
	got, err := s.GetSchedule(ctx, "acme", sch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProvisionFailures != 3 || got.LastProvisionError != "port pool exhausted" {
		t.Fatalf("streak not persisted with its reason: %+v", got)
	}

	if err := s.ClearScheduleProvisionFailures(ctx, "acme", sch.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = s.GetSchedule(ctx, "acme", sch.ID)
	if got.ProvisionFailures != 0 || got.LastProvisionError != "" {
		t.Fatalf("clear left state behind: %+v", got)
	}

	// The counter is runtime state: neither write appends to the config log
	// (§15.3 rule 3). Only the create is in there so far.
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected only the create in the config log, got %d: %+v", len(evs), evs)
	}

	if _, err := s.NoteScheduleProvisionFailure(ctx, "acme", sch.ID, "still exhausted"); err != nil {
		t.Fatalf("note: %v", err)
	}
	if _, err := s.DisableSchedule(ctx, "acme", sch.ID, ConfigWrite{Rationale: "5 consecutive firings"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _ = s.GetSchedule(ctx, "acme", sch.ID)
	if got.Enabled {
		t.Fatalf("schedule should be disabled")
	}
	if got.ProvisionFailures != 0 {
		t.Fatalf("the disable must spend the streak, got %d", got.ProvisionFailures)
	}
	// …and the decision IS logged, carrying the reason.
	evs, _ = s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if len(evs) != 2 || evs[0].Action != ActionScheduleUpdate || !strings.Contains(evs[0].Rationale, "5 consecutive") {
		t.Fatalf("the disable must append one config event carrying the reason: %+v", evs)
	}
}

// TestScheduleProvisionFailureUnknownRow: a row that vanished mid-streak is a
// not-found, never a silent success that counts into nothing.
func TestScheduleProvisionFailureUnknownRow(t *testing.T) {
	s := newScheduleStore(t)
	if _, err := s.NoteScheduleProvisionFailure(context.Background(), "acme", "nope", "x"); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("want ErrScheduleNotFound, got %v", err)
	}
}

// ── The cron parser: acceptance ─────────────────────────────────────────────

func TestSchedulesCronParseRejects(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr string
	}{
		{"empty", "", "empty"},
		{"four fields", "0 10 * *", "want exactly 5 fields"},
		{"six fields", "0 0 10 * * *", "want exactly 5 fields"},
		{"nickname", "@daily", "nicknames"},
		{"minute out of range", "60 * * * *", "out of range"},
		{"hour out of range", "0 24 * * *", "out of range"},
		{"day zero", "0 0 0 * *", "out of range"},
		{"month thirteen", "0 0 1 13 *", "out of range"},
		{"dow eight", "0 0 * * 8", "out of range"},
		{"inverted range", "0 10-2 * * *", "inverted"},
		{"zero step", "*/0 * * * *", "positive integer"},
		{"garbage step", "*/x * * * *", "positive integer"},
		{"not a number", "abc * * * *", "is not a number"},
		{"bad month name", "0 0 * smarch *", "is not a number or a month name"},
		{"empty list element", "0,,5 * * * *", "empty list element"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCron(tc.expr)
			if err == nil {
				t.Fatalf("expected %q to be refused", tc.expr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// ── The cron parser: matching ───────────────────────────────────────────────

// mustParse is the table tests' terse constructor.
func mustParse(t *testing.T, expr string) *CronExpr {
	t.Helper()
	c, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	return c
}

// at builds a UTC time; the location only matters for the DST tests below.
func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func TestSchedulesCronMatching(t *testing.T) {
	// 2026-07-25 is a Saturday (weekday 6); 2026-07-27 is a Monday.
	tests := []struct {
		name string
		expr string
		when time.Time
		want bool
	}{
		{"every minute matches", "* * * * *", at(2026, 7, 25, 13, 37), true},
		{"exact minute hit", "0 10 * * *", at(2026, 7, 25, 10, 0), true},
		{"exact minute miss by a minute", "0 10 * * *", at(2026, 7, 25, 10, 1), false},
		{"exact minute miss by an hour", "0 10 * * *", at(2026, 7, 25, 11, 0), false},
		{"list of minutes hit", "0,30 * * * *", at(2026, 7, 25, 4, 30), true},
		{"list of minutes miss", "0,30 * * * *", at(2026, 7, 25, 4, 15), false},
		{"range hit", "0 9-17 * * *", at(2026, 7, 25, 17, 0), true},
		{"range miss (exclusive of 18)", "0 9-17 * * *", at(2026, 7, 25, 18, 0), false},
		{"step from star", "*/15 * * * *", at(2026, 7, 25, 4, 45), true},
		{"step from star miss", "*/15 * * * *", at(2026, 7, 25, 4, 46), false},
		{"step within a range", "0 9-17/4 * * *", at(2026, 7, 25, 13, 0), true},
		{"step within a range miss", "0 9-17/4 * * *", at(2026, 7, 25, 14, 0), false},
		{"open-ended step", "0 8/6 * * *", at(2026, 7, 25, 20, 0), true},
		{"day of month hit", "0 0 25 * *", at(2026, 7, 25, 0, 0), true},
		{"day of month miss", "0 0 25 * *", at(2026, 7, 26, 0, 0), false},
		{"month name hit", "0 0 1 JUL *", at(2026, 7, 1, 0, 0), true},
		{"month name miss", "0 0 1 JUL *", at(2026, 8, 1, 0, 0), false},
		{"day name hit", "0 9 * * MON", at(2026, 7, 27, 9, 0), true},
		{"day name miss", "0 9 * * MON", at(2026, 7, 28, 9, 0), false},
		{"sunday as 0", "0 9 * * 0", at(2026, 7, 26, 9, 0), true},
		{"sunday as 7", "0 9 * * 7", at(2026, 7, 26, 9, 0), true},
		{"weekday range hit", "0 9 * * 1-5", at(2026, 7, 27, 9, 0), true},
		{"weekday range miss on saturday", "0 9 * * 1-5", at(2026, 7, 25, 9, 0), false},

		// The classic-cron union rule: dom AND dow both restricted means EITHER
		// may match. 2026-07-27 is a Monday; the 1st of July 2026 is a Wednesday.
		{"dom or dow: dow leg", "0 0 1 * MON", at(2026, 7, 27, 0, 0), true},
		{"dom or dow: dom leg", "0 0 1 * MON", at(2026, 7, 1, 0, 0), true},
		{"dom or dow: neither", "0 0 1 * MON", at(2026, 7, 2, 0, 0), false},
		// …but with only one restricted it is an ordinary conjunct.
		{"dom only", "0 0 1 * *", at(2026, 7, 2, 0, 0), false},
		{"dow only on a wednesday", "0 0 * * WED", at(2026, 7, 1, 0, 0), true},

		{"seconds are ignored", "0 10 * * *", at(2026, 7, 25, 10, 0).Add(42 * time.Second), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustParse(t, tc.expr).Matches(tc.when); got != tc.want {
				t.Fatalf("%q.Matches(%s) = %v, want %v", tc.expr, tc.when.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

// ── DST / timezone edges ────────────────────────────────────────────────────
//
// The scheduler evaluates one wall-clock minute per tick in the stack-local
// zone, and keys each occurrence on that wall clock (ScheduleFiring.
// ScheduledFor). Those two decisions together are the whole DST story, and
// these cases pin it:
//
//   - spring forward: in Europe/London the clocks go 00:59 → 02:00, so the whole
//     01:xx wall-clock hour does not exist and a daily 01:30 job simply does not
//     run that day. No catch-up (§8.6 skips missed firings by design).
//   - fall back: the same 01:xx hour occurs twice. 01:30 MATCHES twice — but both
//     occurrences share one occurrence key, so ClaimFiring lets exactly one
//     through and the tweet is written once.

func TestSchedulesCronDSTSpringForward(t *testing.T) {
	// Europe/London: 2026-03-29 01:00 GMT → 02:00 BST. 01:30 never happens.
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	expr := mustParse(t, "30 1 * * *")

	// Walk every real minute of the transition day and count matches. Iterating
	// with Add on an absolute instant is exactly what the scheduler's clock does,
	// so the skipped hour is skipped for the same reason in the test and in prod.
	start := time.Date(2026, 3, 29, 0, 0, 0, 0, london)
	matches := 0
	keys := map[string]int{}
	for i := 0; i < 24*60; i++ {
		ts := start.Add(time.Duration(i) * time.Minute)
		if ts.Day() != 29 {
			break
		}
		if expr.Matches(ts) {
			matches++
			keys[OccurrenceKey(ts)]++
		}
	}
	if matches != 0 {
		t.Fatalf("01:30 does not exist on the spring-forward day; got %d match(es): %v", matches, keys)
	}

	// The day before and the day after are ordinary: one firing each.
	for _, day := range []int{28, 30} {
		ts := time.Date(2026, 3, day, 1, 30, 0, 0, london)
		if !expr.Matches(ts) {
			t.Fatalf("2026-03-%02d 01:30 should fire", day)
		}
	}
}

func TestSchedulesCronDSTFallBackFiresOnce(t *testing.T) {
	// Europe/London: 2026-10-25 02:00 BST → 01:00 GMT. 01:30 happens twice.
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	expr := mustParse(t, "30 1 * * *")

	start := time.Date(2026, 10, 25, 0, 0, 0, 0, london)
	var matched []time.Time
	for i := 0; i < 26*60; i++ {
		ts := start.Add(time.Duration(i) * time.Minute)
		if ts.Day() != 25 {
			break
		}
		if expr.Matches(ts) {
			matched = append(matched, ts)
		}
	}
	if len(matched) != 2 {
		t.Fatalf("expected the repeated wall-clock hour to match twice, got %d: %v", len(matched), matched)
	}
	// Two matches, ONE occurrence: the wall-clock key is what dedupes them.
	if a, b := OccurrenceKey(matched[0]), OccurrenceKey(matched[1]); a != b {
		t.Fatalf("the repeated hour must share one occurrence key, got %q and %q", a, b)
	}
	// And the store enforces it.
	s := newScheduleStore(t)
	ctx := context.Background()
	sch, err := s.CreateSchedule(ctx, NewSchedule("acme", "tweet-author", "30 1 * * *", "write the morning tweet"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed := 0
	for _, ts := range matched {
		if _, ok, err := s.ClaimFiring(ctx, &ScheduleFiring{
			ScheduleID: sch.ID, Project: "acme", ScheduledFor: OccurrenceKey(ts),
		}); err != nil {
			t.Fatalf("claim: %v", err)
		} else if ok {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("the repeated hour must fire exactly once, claimed %d", claimed)
	}
}

func TestSchedulesCronTimezoneShiftsTheWallClock(t *testing.T) {
	// The same instant is a different wall-clock minute in two zones — which is
	// why the scheduler evaluates in the stack-local zone and nothing else.
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	expr := mustParse(t, "0 10 * * *")
	instant := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	if !expr.Matches(instant) {
		t.Fatalf("10:00 UTC should fire for a UTC stack")
	}
	if expr.Matches(instant.In(tokyo)) {
		t.Fatalf("10:00 UTC is 19:00 in Tokyo and must not fire a 10:00 job there")
	}
	if !expr.Matches(time.Date(2026, 7, 25, 10, 0, 0, 0, tokyo)) {
		t.Fatalf("10:00 Tokyo should fire for a Tokyo stack")
	}
	// Occurrence keys follow the wall clock, so two zones never collide on one
	// key for the same instant.
	if OccurrenceKey(instant) == OccurrenceKey(instant.In(tokyo)) {
		t.Fatalf("occurrence keys must be wall-clock, not instant")
	}
}

// ── CRUD ────────────────────────────────────────────────────────────────────

func TestSchedulesCRUD(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()

	morning, err := s.CreateSchedule(ctx, NewSchedule("acme", "tweet-author", "0 10 * * *", "write the morning tweet"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if morning.ID == "" {
		t.Fatalf("create must allocate an id")
	}
	if !morning.Enabled {
		t.Fatalf("NewSchedule must default enabled=true")
	}

	// §8.6: two rows targeting one worker, differing only in when and WHAT.
	evening, err := s.CreateSchedule(ctx, NewSchedule("acme", "tweet-author", "0 17 * * *", "write the evening tweet"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create evening: %v", err)
	}

	got, err := s.GetSchedule(ctx, "acme", morning.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Input != "write the morning tweet" || got.Cron != "0 10 * * *" || got.Worker != "tweet-author" {
		t.Fatalf("round-trip: %+v", got)
	}

	list, err := s.ListSchedules(ctx, "acme")
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %d rows, err=%v", len(list), err)
	}

	// Update is a whole-object replace.
	evening.Cron = "30 18 * * *"
	evening.Input = "write the evening tweet, then check replies"
	updated, err := s.UpdateSchedule(ctx, evening, ConfigWrite{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Cron != "30 18 * * *" || !strings.Contains(updated.Input, "check replies") {
		t.Fatalf("update did not stick: %+v", updated)
	}

	// Disable is the §8.6 missing-worker path; it is idempotent.
	disabled, err := s.DisableSchedule(ctx, "acme", morning.ID, ConfigWrite{Rationale: "worker tweet-author no longer exists"})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.Enabled {
		t.Fatalf("disable did not stick")
	}
	if _, err := s.DisableSchedule(ctx, "acme", morning.ID, ConfigWrite{}); err != nil {
		t.Fatalf("disabling twice must be a no-op: %v", err)
	}

	// Only the live one is polled.
	enabled, err := s.ListEnabledSchedules(ctx)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ID != evening.ID {
		t.Fatalf("expected only the enabled schedule, got %+v", enabled)
	}

	if err := s.DeleteSchedule(ctx, "acme", morning.ID, ConfigWrite{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetSchedule(ctx, "acme", morning.ID); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("want ErrScheduleNotFound, got %v", err)
	}
	if err := s.DeleteSchedule(ctx, "acme", morning.ID, ConfigWrite{}); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("deleting twice: want ErrScheduleNotFound, got %v", err)
	}
}

func TestSchedulesValidation(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		sch     *Schedule
		wantErr string
	}{
		{"no project", &Schedule{Worker: "w", Cron: "* * * * *"}, "project is required"},
		{"no worker", &Schedule{Project: "acme", Cron: "* * * * *"}, "worker is required"},
		{"no cron", &Schedule{Project: "acme", Worker: "w"}, "cron is required"},
		{"unparseable cron", &Schedule{Project: "acme", Worker: "w", Cron: "0 99 * * *"}, "out of range"},
		{"nickname cron", &Schedule{Project: "acme", Worker: "w", Cron: "@hourly"}, "nicknames"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateSchedule(ctx, tc.sch, ConfigWrite{})
			if err == nil {
				t.Fatalf("expected a refusal")
			}
			if !errors.Is(err, ErrScheduleInvalid) {
				t.Fatalf("want ErrScheduleInvalid, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q in %v", tc.wantErr, err)
			}
			// Nothing half-written (§9): no row, and no config event either.
			rows, _ := s.ListSchedules(ctx, "acme")
			if len(rows) != 0 {
				t.Fatalf("a refused create still wrote a row: %+v", rows)
			}
			evs, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
			if len(evs) != 0 {
				t.Fatalf("a refused create still wrote a config event: %+v", evs)
			}
		})
	}
}

// TestSchedulesProjectIsolation is the §12 negative test: a scoped caller can
// neither read nor write across projects.
func TestSchedulesProjectIsolation(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()

	mine, err := s.CreateSchedule(ctx, NewSchedule("acme", "w", "0 10 * * *", "mine"), ConfigWrite{})
	if err != nil {
		t.Fatalf("seed acme: %v", err)
	}
	theirs, err := s.CreateSchedule(ctx, NewSchedule("globex", "w", "0 10 * * *", "theirs"), ConfigWrite{})
	if err != nil {
		t.Fatalf("seed globex: %v", err)
	}

	// Read across: not found, never forbidden and never the row.
	if _, err := s.GetSchedule(ctx, "acme", theirs.ID); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("cross-project read: want not-found, got %v", err)
	}
	list, err := s.ListSchedules(ctx, "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("list leaked another project's rows: %+v", list)
	}

	// Write across: the update finds nothing, so it writes nothing.
	if _, err := s.UpdateSchedule(ctx, &Schedule{
		ID: theirs.ID, Project: "acme", Worker: "w", Cron: "0 0 * * *", Input: "hijacked",
	}, ConfigWrite{}); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("cross-project update: want not-found, got %v", err)
	}
	if _, err := s.DisableSchedule(ctx, "acme", theirs.ID, ConfigWrite{}); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("cross-project disable: want not-found, got %v", err)
	}
	if err := s.DeleteSchedule(ctx, "acme", theirs.ID, ConfigWrite{}); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("cross-project delete: want not-found, got %v", err)
	}
	still, err := s.GetSchedule(ctx, "globex", theirs.ID)
	if err != nil {
		t.Fatalf("the other project's row must be untouched: %v", err)
	}
	if still.Input != "theirs" || !still.Enabled {
		t.Fatalf("cross-project write leaked through: %+v", still)
	}

	// The scheduler's unscoped poll is the one deliberate exception — it is core,
	// not a tenant — and it sees both.
	all, err := s.ListEnabledSchedules(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("scheduler poll: %d rows, err=%v", len(all), err)
	}
}

// TestSchedulesMutationsAppendConfigEvents pins the §15 adoption: every write
// lands in the log with the acting worker and the full new state.
func TestSchedulesMutationsAppendConfigEvents(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()
	actor := ConfigWrite{Worker: "marketing-manager", Session: "s-42", Rationale: "the strategy says daily"}

	sch, err := s.CreateSchedule(ctx, NewSchedule("acme", "tweet-author", "0 10 * * *", "write the morning tweet"), actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sch.Cron = "0 11 * * *"
	if _, err := s.UpdateSchedule(ctx, sch, actor); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.DeleteSchedule(ctx, "acme", sch.ID, actor); err != nil {
		t.Fatalf("delete: %v", err)
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme", Action: "schedule_*"})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("expected three schedule config events, got %d", len(evs))
	}
	var actions []string
	for _, ev := range evs {
		actions = append(actions, ev.Action)
		if ev.ActorWorker != "marketing-manager" || ev.ActorSession != "s-42" {
			t.Fatalf("actor not threaded through: %+v", ev)
		}
		// Full new state, never a diff (§15.2) — the delete carries the row as it
		// last stood, which is what makes restoring it a lookup.
		if ev.Payload["worker"] != "tweet-author" || ev.Payload["input"] != "write the morning tweet" {
			t.Fatalf("payload is not the full row: %v", ev.Payload)
		}
	}
	want := []string{ActionScheduleDelete, ActionScheduleUpdate, ActionScheduleCreate} // newest first
	for i, a := range want {
		if actions[i] != a {
			t.Fatalf("actions newest-first: want %v, got %v", want, actions)
		}
	}
}

// ── Firings (the §8.6 idempotency guard) ────────────────────────────────────

func TestSchedulesFiringIsIdempotent(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()
	sch, err := s.CreateSchedule(ctx, NewSchedule("acme", "tweet-author", "0 10 * * *", "tweet"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	key := OccurrenceKey(at(2026, 7, 25, 10, 0))
	first, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{ScheduleID: sch.ID, Project: "acme", ScheduledFor: key})
	if err != nil || !claimed {
		t.Fatalf("first claim must win: claimed=%v err=%v", claimed, err)
	}
	if first.FiredAt == 0 {
		t.Fatalf("claim must stamp fired_at")
	}

	// Every retry loses — a crashed scheduler cannot double-fire an occurrence.
	for i := 0; i < 3; i++ {
		again, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{ScheduleID: sch.ID, Project: "acme", ScheduledFor: key})
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if claimed {
			t.Fatalf("retry %d claimed the same occurrence twice", i)
		}
		if again.ID != first.ID {
			t.Fatalf("retry returned a different row: %s vs %s", again.ID, first.ID)
		}
	}

	// A different minute is a different occurrence.
	if _, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: sch.ID, Project: "acme", ScheduledFor: OccurrenceKey(at(2026, 7, 26, 10, 0)),
	}); err != nil || !claimed {
		t.Fatalf("next day must be claimable: claimed=%v err=%v", claimed, err)
	}
	// …and so is the same minute for a different schedule.
	other, err := s.CreateSchedule(ctx, NewSchedule("acme", "tweet-author", "0 10 * * *", "second tweet"), ConfigWrite{})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if _, claimed, err := s.ClaimFiring(ctx, &ScheduleFiring{
		ScheduleID: other.ID, Project: "acme", ScheduledFor: key,
	}); err != nil || !claimed {
		t.Fatalf("a second schedule must claim the same minute: claimed=%v err=%v", claimed, err)
	}

	if err := s.StampFiringEvent(ctx, first.ID, "ev-1"); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	firings, err := s.ListFirings(ctx, "acme", sch.ID, 0)
	if err != nil {
		t.Fatalf("list firings: %v", err)
	}
	if len(firings) != 2 {
		t.Fatalf("expected two occurrences for this schedule, got %d", len(firings))
	}
	if firings[1].EventID != "ev-1" && firings[0].EventID != "ev-1" {
		t.Fatalf("stamped event id did not persist: %+v", firings)
	}
}

func TestSchedulesFiringValidation(t *testing.T) {
	s := newScheduleStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		f    *ScheduleFiring
	}{
		{"nil", nil},
		{"no schedule id", &ScheduleFiring{ScheduledFor: "2026-07-25T10:00"}},
		{"no occurrence", &ScheduleFiring{ScheduleID: "s1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.ClaimFiring(ctx, tc.f); err == nil {
				t.Fatalf("expected a refusal")
			}
		})
	}
}
