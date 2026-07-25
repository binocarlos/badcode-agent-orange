package agentdb

// schedules.go — cron as a core primitive (spec §8.6,
// docs/product/04-events-and-schedules.md).
//
// Two tables and one parser live here:
//
//	schedules        — {project, id, worker, cron, input, enabled, updated_at}.
//	                   Configuration: every mutation goes through the config-log
//	                   seam (§15.3/§15.4), so retuning a workforce is recorded.
//	schedule_firings — one row per (schedule, occurrence). The UNIQUE index on
//	                   (schedule_id, scheduled_for) is the idempotency guard of
//	                   §8.6: a crash/retry cannot double-fire an occurrence.
//	                   Runtime state, NOT configuration — like event_deliveries
//	                   it stays out of the config log (§15.3 rule 3), which is
//	                   also why its methods are deliberately named without the
//	                   word "Schedule": the conformance classifier in
//	                   config_events_test.go reads that noun as configuration.
//
// The `input` column is the design's centre of gravity: a schedule does not
// only say *when* a worker runs, it says *what it is told* each time. Two rows
// pointing at one worker ("10:00 → write the morning tweet", "17:00 → write the
// evening tweet") are two different instructions, not two copies of a job.
//
// The cron parser lives here rather than in the scheduler so that WRITE-TIME
// validation (§9: "parseable cron") and FIRING-TIME matching can never disagree
// about what an expression means.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EventTypeScheduleFired is the event a due schedule produces (§8.6). Its text
// is the schedule's `input` and its envelope is {source: "schedule", depth: 0},
// so a firing flows through the identical composition path (§6.2) as every
// other trigger.
const EventTypeScheduleFired = "schedule.fired"

// Schedule store errors. Sentinels so the HTTP layer maps them to status codes
// without string-matching.
var (
	// ErrScheduleNotFound is returned when no schedule matches (project, id).
	ErrScheduleNotFound = errors.New("schedule not found")
	// ErrScheduleInvalid wraps every validation failure on a schedule row.
	ErrScheduleInvalid = errors.New("invalid schedule")
)

// Schedule is one cron entry (§8.6). Identity is the uuid; `project` is the
// hard tenancy namespace and matches the `customer` claim on the caller's token.
//
// Deliberately no gorm `default:` tags: GORM substitutes a declared default for
// a zero value on write, which would make `enabled: false` silently persist as
// true. The column DEFAULTs live in migration 024 for rows written outside this
// store; NewSchedule/validateSchedule own the in-Go defaulting.
type Schedule struct {
	ID      string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Project string `json:"project" gorm:"type:varchar(255);not null;index:idx_schedules_project"`
	// Worker is the worker a firing starts a job for. A due schedule whose
	// worker no longer exists is disabled and logged (§8.6) — never retried
	// forever — which is the scheduler's job, not this store's.
	Worker string `json:"worker" gorm:"type:varchar(255);not null"`
	// Cron is a standard 5-field expression, evaluated in the stack-local time
	// zone (TZ on agentd, default UTC). Validated on write: an unparseable
	// expression is refused, never stored to fail silently at 03:00.
	Cron string `json:"cron" gorm:"type:varchar(255);not null"`
	// Input is the instruction this trigger delivers — it becomes the event text.
	Input     string `json:"input" gorm:"type:text"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Schedule) TableName() string { return "schedules" }

// NewSchedule returns a Schedule with the spec's default applied (enabled). Use
// it rather than a bare &Schedule{}: `Enabled` is a plain bool, so a zero-valued
// struct would persist a schedule that never fires.
func NewSchedule(project, worker, cron, input string) *Schedule {
	return &Schedule{
		Project: project,
		Worker:  worker,
		Cron:    cron,
		Input:   input,
		Enabled: true,
	}
}

// ScheduleFiring records one occurrence of one schedule. The (ScheduleID,
// ScheduledFor) pair is UNIQUE — claiming it is what makes firing idempotent.
//
// ScheduledFor is the LOCAL WALL-CLOCK minute ("2026-11-01T01:30"), not a unix
// timestamp, and that is a DST decision on the record: when the clocks go back,
// 01:30 happens twice in one evening, and a tweet-writer must write one morning
// tweet, not two. Keying the occurrence on wall-clock time collapses the
// repeated hour to a single firing. The spring-forward gap needs no special
// case: 02:30 never occurs as a wall clock, so it simply never fires.
type ScheduleFiring struct {
	ID           string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	ScheduleID   string `json:"schedule_id" gorm:"type:varchar(36);not null;uniqueIndex:idx_schedule_firings_occurrence,priority:1"`
	ScheduledFor string `json:"scheduled_for" gorm:"type:varchar(20);not null;uniqueIndex:idx_schedule_firings_occurrence,priority:2"`
	Project      string `json:"project" gorm:"type:varchar(255);not null;index:idx_schedule_firings_project"`
	// EventID is the `schedule.fired` event this occurrence produced, stamped
	// after the event lands. Empty means the process died between claiming the
	// occurrence and creating the event: the occurrence is consumed and that
	// firing is skipped, which is exactly §8.6's skip-missed posture — a stale
	// morning is never replayed.
	EventID string `json:"event_id" gorm:"type:varchar(36)"`
	FiredAt int64  `json:"fired_at"`
}

func (ScheduleFiring) TableName() string { return "schedule_firings" }

// OccurrenceKey renders a time as the wall-clock occurrence key. The location of
// t is the schedule's evaluation zone (agentd's TZ).
func OccurrenceKey(t time.Time) string { return t.Format("2006-01-02T15:04") }

// ── Validation ──────────────────────────────────────────────────────────────

// validateSchedule enforces the §8.6 shape and the §9 "parseable cron" rule. It
// mutates s only to trim, so the row written and the row echoed back agree.
func validateSchedule(s *Schedule) error {
	if s == nil {
		return fmt.Errorf("%w: schedule is required", ErrScheduleInvalid)
	}
	if strings.TrimSpace(s.Project) == "" {
		return fmt.Errorf("%w: project is required", ErrScheduleInvalid)
	}
	s.Worker = strings.TrimSpace(s.Worker)
	if s.Worker == "" {
		return fmt.Errorf("%w: worker is required", ErrScheduleInvalid)
	}
	s.Cron = strings.TrimSpace(s.Cron)
	if s.Cron == "" {
		return fmt.Errorf("%w: cron is required", ErrScheduleInvalid)
	}
	if _, err := ParseCron(s.Cron); err != nil {
		return fmt.Errorf("%w: %w", ErrScheduleInvalid, err)
	}
	return nil
}

// ── CRUD (configuration — every write appends a config event) ───────────────

// CreateSchedule stores a new schedule and returns it read back (§9).
//
// Schedules are configuration: the write appends a `schedule_create` record in
// the same transaction (§15.3/§15.4). cw is the who/why — a human/API edit
// passes the zero value, a worker acting through `schedule_create` supplies
// itself.
func (s *Store) CreateSchedule(ctx context.Context, sch *Schedule, cw ConfigWrite) (*Schedule, error) {
	if err := validateSchedule(sch); err != nil {
		return nil, err
	}
	if sch.ID == "" {
		sch.ID = uuid.New().String()
	}
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: sch.Project,
		Action:  ActionScheduleCreate,
		Payload: sch,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		return tx.Create(sch).Error
	}); err != nil {
		return nil, fmt.Errorf("failed to create schedule: %w", err)
	}
	return sch, nil
}

// GetSchedule reads one schedule within a project. Another project's row looks
// like a missing row — the only project-isolation answer a caller ever gets.
func (s *Store) GetSchedule(ctx context.Context, project, id string) (*Schedule, error) {
	if project == "" || id == "" {
		return nil, fmt.Errorf("%w: project and id are required", ErrScheduleInvalid)
	}
	var sch Schedule
	err := s.gdb.WithContext(ctx).Where("project = ? AND id = ?", project, id).First(&sch).Error
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s", ErrScheduleNotFound, project, id)
		}
		return nil, fmt.Errorf("failed to get schedule: %w", err)
	}
	return &sch, nil
}

// ListSchedules returns a project's schedules, newest-first.
func (s *Store) ListSchedules(ctx context.Context, project string) ([]*Schedule, error) {
	if project == "" {
		return nil, fmt.Errorf("%w: project is required", ErrScheduleInvalid)
	}
	out := []*Schedule{}
	if err := s.gdb.WithContext(ctx).Model(&Schedule{}).
		Where("project = ?", project).
		Order("created_at DESC, id DESC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list schedules: %w", err)
	}
	return out, nil
}

// ListEnabledSchedules returns every live schedule across every project — the
// scheduler's minute poll (§8.6). Like ListUndeliveredProjectEvents it is
// deliberately unscoped: the scheduler is core, not a tenant.
func (s *Store) ListEnabledSchedules(ctx context.Context) ([]*Schedule, error) {
	out := []*Schedule{}
	if err := s.gdb.WithContext(ctx).Model(&Schedule{}).
		Where("enabled = ?", true).
		Order("project ASC, created_at ASC, id ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list enabled schedules: %w", err)
	}
	return out, nil
}

// UpdateSchedule overwrites the mutable fields of an existing row. The project
// on sch is the authorization boundary: a row owned by another project is never
// found, so it is never written.
func (s *Store) UpdateSchedule(ctx context.Context, sch *Schedule, cw ConfigWrite) (*Schedule, error) {
	if sch == nil || sch.ID == "" {
		return nil, fmt.Errorf("%w: schedule id is required", ErrScheduleInvalid)
	}
	if err := validateSchedule(sch); err != nil {
		return nil, err
	}
	existing, err := s.GetSchedule(ctx, sch.Project, sch.ID)
	if err != nil {
		return nil, err
	}
	existing.Worker = sch.Worker
	existing.Cron = sch.Cron
	existing.Input = sch.Input
	existing.Enabled = sch.Enabled
	// One action whether or not the write flips `enabled`: §15.3 gives schedules
	// no enable/disable verbs — a paused schedule is an ordinary field change.
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: existing.Project,
		Action:  ActionScheduleUpdate,
		Payload: existing,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		return tx.Save(existing).Error
	}); err != nil {
		return nil, fmt.Errorf("failed to update schedule: %w", err)
	}
	return existing, nil
}

// DisableSchedule switches a schedule off, recording why in the config log's
// rationale. It is the §8.6 answer to "a due schedule whose worker no longer
// exists": disabled and logged, never silently retried forever.
//
// It is a narrow write rather than a read-modify-write through UpdateSchedule so
// the scheduler cannot clobber a concurrent edit of the cron or the input while
// switching the row off. Disabling an already-disabled schedule is a no-op and
// appends nothing: the log records decisions, not repeated observations.
func (s *Store) DisableSchedule(ctx context.Context, project, id string, cw ConfigWrite) (*Schedule, error) {
	existing, err := s.GetSchedule(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if !existing.Enabled {
		return existing, nil
	}
	existing.Enabled = false
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: existing.Project,
		Action:  ActionScheduleUpdate,
		Payload: existing,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		return tx.Save(existing).Error
	}); err != nil {
		return nil, fmt.Errorf("failed to disable schedule: %w", err)
	}
	return existing, nil
}

// DeleteSchedule removes a project's schedule. Deleting another project's row is
// a not-found, never a silent success.
//
// The delete appends too (§15.3 rule 2), carrying the schedule as it last stood,
// which is what makes "put back the schedule we deleted on Tuesday" a lookup.
func (s *Store) DeleteSchedule(ctx context.Context, project, id string, cw ConfigWrite) error {
	existing, err := s.GetSchedule(ctx, project, id)
	if err != nil {
		return err
	}
	vanished := false
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: project,
		Action:  ActionScheduleDelete,
		Payload: existing,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		res := tx.Where("project = ? AND id = ?", project, id).Delete(&Schedule{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Lost a race with a concurrent delete: roll back rather than log a
			// deletion this call did not perform.
			vanished = true
			return fmt.Errorf("%w: %s/%s", ErrScheduleNotFound, project, id)
		}
		return nil
	}); err != nil {
		if vanished || errors.Is(err, ErrScheduleNotFound) {
			return fmt.Errorf("%w: %s/%s", ErrScheduleNotFound, project, id)
		}
		return fmt.Errorf("failed to delete schedule: %w", err)
	}
	return nil
}

// ── Firings (runtime state — the idempotency guard) ─────────────────────────

// ClaimFiring claims one occurrence of one schedule. The first caller for a
// (schedule_id, scheduled_for) pair gets claimed=true and owns the firing; every
// later caller — a retry after a crash, a second agentd, a duplicated tick —
// gets claimed=false and must do nothing (§8.6).
//
// Claim-before-fire is deliberate: if the process dies between the claim and the
// event, the occurrence is consumed and that firing is skipped. Skipping a
// missed firing is already the documented semantics; double-firing is not.
func (s *Store) ClaimFiring(ctx context.Context, f *ScheduleFiring) (*ScheduleFiring, bool, error) {
	if f == nil {
		return nil, false, fmt.Errorf("firing is required")
	}
	if f.ScheduleID == "" {
		return nil, false, fmt.Errorf("schedule_id is required")
	}
	if f.ScheduledFor == "" {
		return nil, false, fmt.Errorf("scheduled_for is required")
	}
	if existing, err := s.findFiring(ctx, f.ScheduleID, f.ScheduledFor); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, fmt.Errorf("failed to look up firing: %w", err)
	}
	if f.ID == "" {
		f.ID = uuid.New().String()
	}
	if f.FiredAt == 0 {
		f.FiredAt = eventsNow()
	}
	if err := s.gdb.WithContext(ctx).Create(f).Error; err != nil {
		// Lost the race against a concurrent scheduler: the unique index fired.
		// Re-read rather than surfacing a driver-specific duplicate-key error.
		if existing, lookupErr := s.findFiring(ctx, f.ScheduleID, f.ScheduledFor); lookupErr == nil {
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("failed to claim firing: %w", err)
	}
	return f, true, nil
}

func (s *Store) findFiring(ctx context.Context, scheduleID, scheduledFor string) (*ScheduleFiring, error) {
	var f ScheduleFiring
	if err := s.gdb.WithContext(ctx).
		Where("schedule_id = ? AND scheduled_for = ?", scheduleID, scheduledFor).
		First(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

// StampFiringEvent records which `schedule.fired` event an occurrence produced.
func (s *Store) StampFiringEvent(ctx context.Context, firingID, eventID string) error {
	if firingID == "" || eventID == "" {
		return fmt.Errorf("firing id and event id are required")
	}
	res := s.gdb.WithContext(ctx).Model(&ScheduleFiring{}).
		Where("id = ?", firingID).Update("event_id", eventID)
	if res.Error != nil {
		return fmt.Errorf("failed to stamp firing event: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("firing not found")
	}
	return nil
}

// ListFirings returns a schedule's recorded occurrences, newest-first. History
// and debugging only; nothing on the firing path reads it.
func (s *Store) ListFirings(ctx context.Context, project, scheduleID string, limit int) ([]*ScheduleFiring, error) {
	if project == "" || scheduleID == "" {
		return nil, fmt.Errorf("project and schedule_id are required")
	}
	out := []*ScheduleFiring{}
	if err := s.gdb.WithContext(ctx).Model(&ScheduleFiring{}).
		Where("project = ? AND schedule_id = ?", project, scheduleID).
		Order("scheduled_for DESC").Limit(clampLimit(limit)).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list firings: %w", err)
	}
	return out, nil
}

// ── The cron parser (§8.6: "standard 5-field cron expression") ──────────────

// CronExpr is a parsed 5-field cron expression: minute, hour, day-of-month,
// month, day-of-week. Each field is a bitmask, so matching a minute is five bit
// tests and no allocation.
//
// Supported syntax is deliberately the classic one and nothing more:
//
//	"*"                   every value
//	"a"                   one value
//	"a-b"                 an inclusive range
//	"*/n", "a-b/n", "a/n" a step over a range
//	"a,b,c"               a list of any of the above
//	"JAN".."DEC"          three-letter month names
//	"SUN".."SAT"          three-letter day names
//
// Day-of-week accepts 0-7 with both 0 and 7 meaning Sunday. Nicknames (@daily
// and friends) are refused: the spec says five fields, and quietly accepting a
// second syntax is how "every minute" happens by accident.
type CronExpr struct {
	raw    string
	minute uint64 // bits 0..59
	hour   uint64 // bits 0..23
	dom    uint64 // bits 1..31
	month  uint64 // bits 1..12
	dow    uint64 // bits 0..6 (Sunday = 0)

	// domRestricted/dowRestricted implement the one genuinely surprising rule of
	// classic cron: when BOTH day-of-month and day-of-week are restricted, the
	// entry fires when EITHER matches (a union, not an intersection). When only
	// one is restricted it is an ordinary conjunct.
	domRestricted bool
	dowRestricted bool
}

// String returns the expression as written.
func (c *CronExpr) String() string { return c.raw }

type cronField struct {
	name  string
	min   int
	max   int
	names map[string]int
}

var (
	monthNames = map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	dowNames = map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}

	cronFields = []cronField{
		{name: "minute", min: 0, max: 59},
		{name: "hour", min: 0, max: 23},
		{name: "day-of-month", min: 1, max: 31},
		{name: "month", min: 1, max: 12, names: monthNames},
		{name: "day-of-week", min: 0, max: 7, names: dowNames},
	}
)

// ParseCron parses a standard 5-field cron expression. Whitespace between
// fields may be any run of spaces or tabs.
func ParseCron(expr string) (*CronExpr, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return nil, fmt.Errorf("cron expression is empty (want 5 fields: minute hour day-of-month month day-of-week)")
	}
	if strings.HasPrefix(raw, "@") {
		return nil, fmt.Errorf("cron %q: nicknames like @daily are not supported — write the 5 fields "+
			"(e.g. `0 0 * * *` for daily at midnight)", raw)
	}
	parts := strings.Fields(raw)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron %q: want exactly 5 fields (minute hour day-of-month month day-of-week), got %d",
			raw, len(parts))
	}
	c := &CronExpr{raw: raw}
	masks := make([]uint64, 5)
	for i, part := range parts {
		mask, err := parseCronField(part, cronFields[i])
		if err != nil {
			return nil, fmt.Errorf("cron %q: %s field: %w", raw, cronFields[i].name, err)
		}
		masks[i] = mask
	}
	c.minute, c.hour, c.dom, c.month = masks[0], masks[1], masks[2], masks[3]
	// Normalise day-of-week: bit 7 (the second spelling of Sunday) folds onto 0.
	c.dow = masks[4]
	if c.dow&(1<<7) != 0 {
		c.dow = (c.dow &^ (1 << 7)) | 1
	}
	c.domRestricted = parts[2] != "*"
	c.dowRestricted = parts[4] != "*"
	return c, nil
}

// parseCronField turns one comma-separated field into a bitmask.
func parseCronField(field string, f cronField) (uint64, error) {
	var mask uint64
	for _, item := range strings.Split(field, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return 0, fmt.Errorf("empty list element in %q", field)
		}
		m, err := parseCronItem(item, f)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	if mask == 0 {
		return 0, fmt.Errorf("%q matches nothing", field)
	}
	return mask, nil
}

func parseCronItem(item string, f cronField) (uint64, error) {
	step := 1
	spec := item
	if slash := strings.Index(item, "/"); slash >= 0 {
		spec = item[:slash]
		stepStr := item[slash+1:]
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("step %q in %q must be a positive integer", stepStr, item)
		}
		step = n
	}

	var lo, hi int
	switch {
	case spec == "*":
		lo, hi = f.min, f.max
	case strings.Contains(spec, "-"):
		bounds := strings.SplitN(spec, "-", 2)
		var err error
		if lo, err = cronValue(bounds[0], f); err != nil {
			return 0, err
		}
		if hi, err = cronValue(bounds[1], f); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("range %q is inverted (%d > %d)", spec, lo, hi)
		}
	default:
		v, err := cronValue(spec, f)
		if err != nil {
			return 0, err
		}
		lo = v
		// `a/n` (no upper bound) is the common extension meaning "from a to the
		// end of the field, every n"; a bare `a` is the single value.
		if step > 1 {
			hi = f.max
		} else {
			hi = v
		}
	}

	var mask uint64
	for v := lo; v <= hi; v += step {
		mask |= 1 << uint(v)
	}
	return mask, nil
}

func cronValue(s string, f cronField) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	if f.names != nil {
		if v, ok := f.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number%s", s, nameHint(f))
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("%d is out of range (%d-%d)", v, f.min, f.max)
	}
	return v, nil
}

func nameHint(f cronField) string {
	if f.names == nil {
		return ""
	}
	if f.name == "month" {
		return " or a month name (JAN-DEC)"
	}
	return " or a day name (SUN-SAT)"
}

// Matches reports whether t — interpreted in its own location, which the
// scheduler sets to the stack-local zone — is a minute this expression fires on.
// Seconds and sub-second parts of t are ignored.
func (c *CronExpr) Matches(t time.Time) bool {
	if c.minute&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if c.hour&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if c.month&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	domHit := c.dom&(1<<uint(t.Day())) != 0
	dowHit := c.dow&(1<<uint(int(t.Weekday()))) != 0
	switch {
	case c.domRestricted && c.dowRestricted:
		// Classic cron: restricted on both means EITHER may match.
		return domHit || dowHit
	case c.domRestricted:
		return domHit
	case c.dowRestricted:
		return dowHit
	default:
		return true
	}
}
