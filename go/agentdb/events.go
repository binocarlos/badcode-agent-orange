package agentdb

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// eventsNow is the clock for the event spine (unix seconds, matching gorm's
// autoCreateTime on the int64 columns). A package-local helper so the event
// tables never disagree with themselves about "now".
func eventsNow() int64 { return time.Now().Unix() }

// ── The event spine (spec §8.1–§8.4) ────────────────────────────────────────
//
// Three append-mostly tables and nothing else:
//
//   project_events   — every trigger that ever entered the project (append-only)
//   subscriptions    — "when an event of this type arrives, start a job for
//                      this worker" (the only routing configuration there is)
//   event_deliveries — one row per (event, subscription) attempt: the
//                      at-least-once idempotency guard AND the job-history
//                      spine the UI renders.
//
// The router (E3) and the internal emitters (E2) are deliberately NOT here:
// this file is only the durable shape they operate on.

// ── Envelope ────────────────────────────────────────────────────────────────

// Event source values — who caused the event (§8.1).
const (
	EventSourceWorker   = "worker"
	EventSourceExternal = "external"
	EventSourceSchedule = "schedule"
	EventSourceCore     = "core"
)

// EventSources is the complete set of legal envelope sources.
var EventSources = []string{
	EventSourceWorker,
	EventSourceExternal,
	EventSourceSchedule,
	EventSourceCore,
}

// Internal event types emitted by core (§8.2). The two the Runner emits are
// named here, alongside the one the ROUTER emits — the remaining two
// (`human.attention.timeout`, `config.changed`) belong to the tracks that
// produce them and name themselves.
const (
	// EventTypeWorkerFinished is emitted when a worker job's query completed and
	// the session went idle. Its text is the full rendered transcript (§8.2).
	EventTypeWorkerFinished = "worker.finished"
	// EventTypeWorkerFailed is emitted when a worker job ended badly. Its text is
	// the error, and the envelope carries a Reason (§8.2).
	EventTypeWorkerFailed = "worker.failed"
	// EventTypeSubscriptionThrottled is emitted by the router when
	// max_firings_per_hour drops deliveries for a subscription (§8.2, §8.3). Its
	// envelope is core's — {source: "core", depth: 0} — and carries neither
	// worker nor session_id, because a throttle is a fact about a subscription,
	// not about anybody's job.
	EventTypeSubscriptionThrottled = "subscription.throttled"
)

// worker.failed reasons (§8.2). The vocabulary is closed: "error" is the
// session itself erroring (the Runner's error path), "lost" is the router's
// lease reaper finding a session whose lease lapsed without the sandbox
// reporting back (§8.4).
const (
	FailureReasonError = "error"
	FailureReasonLost  = "lost"
)

// FailureReasons is the complete set of legal worker.failed reasons.
var FailureReasons = []string{FailureReasonError, FailureReasonLost}

// ValidFailureReason reports whether s is in the worker.failed vocabulary.
func ValidFailureReason(s string) bool {
	for _, v := range FailureReasons {
		if v == s {
			return true
		}
	}
	return false
}

// EventEnvelope is the part of an event that CORE stamps and a sender never
// controls (§8.1). It is stored as jsonb so the router can filter on it with
// plain equality (`envelope->>'worker' = ?`) without a second table.
//
// Every field is always serialised (no `omitempty` on the scalars) except
// Reason, which only worker.failed carries: an envelope filter matching
// `{"interactive": false}` must be able to see the field.
type EventEnvelope struct {
	// Depth is the loop floor (§8.4): triggering job's depth + 1, external = 0.
	Depth int `json:"depth"`
	// Source is one of EventSources.
	Source string `json:"source"`
	// Worker is set when Source == "worker" (also on core events about a
	// specific worker, e.g. human.attention.timeout).
	Worker string `json:"worker"`
	// SessionID is the session that caused the event, when there was one.
	SessionID string `json:"session_id"`
	// Interactive marks events produced by a human chat turn — subscriptions
	// that should not react to chats filter on it.
	Interactive bool `json:"interactive"`
	// AttentionRequested is true when the job called request_human_attention
	// that turn, so reviewers can skip deliberately half-done work.
	AttentionRequested bool `json:"attention_requested"`
	// Reason discriminates worker.failed: "error" | "lost" (§8.2).
	Reason string `json:"reason,omitempty"`
}

// Value implements driver.Valuer — the envelope is stored as a jsonb object
// with exactly the field names above (the type has no MarshalJSON, so the
// struct tags are the wire format).
func (e EventEnvelope) Value() (driver.Value, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner.
func (e *EventEnvelope) Scan(value any) error {
	if value == nil {
		*e = EventEnvelope{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported type %T for EventEnvelope", value)
	}
	if len(raw) == 0 {
		*e = EventEnvelope{}
		return nil
	}
	var decoded EventEnvelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("failed to unmarshal EventEnvelope: %w", err)
	}
	*e = decoded
	return nil
}

// ── Rows ────────────────────────────────────────────────────────────────────

// ProjectEvent is one trigger: a name and a text payload, plus the core-stamped
// envelope (§8.1). Append-only — there is no update and no delete method, and
// that absence is the whole enforcement mechanism.
type ProjectEvent struct {
	ID         string        `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Project    string        `json:"project" gorm:"type:varchar(255);not null;index:idx_project_events_project"`
	Type       string        `json:"type" gorm:"type:varchar(255);not null;index:idx_project_events_type"`
	Text       string        `json:"text" gorm:"type:text"`
	Envelope   EventEnvelope `json:"envelope" gorm:"type:jsonb"`
	OccurredAt int64         `json:"occurred_at"`
	CreatedAt  int64         `json:"created_at" gorm:"autoCreateTime"`
	// Delivered is the router's watermark (§8.4 step 1): events land false and
	// the router flips them once every matching subscription has a delivery row.
	Delivered bool `json:"delivered" gorm:"index:idx_project_events_undelivered,priority:1"`
}

func (ProjectEvent) TableName() string { return "project_events" }

// Subscription is the whole of routing configuration (§8.3): event type
// (exact or trailing-`*`), an optional equality filter on envelope fields, and
// the worker to start a job for.
type Subscription struct {
	ID        string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Project   string `json:"project" gorm:"type:varchar(255);not null;index:idx_subscriptions_project"`
	EventType string `json:"event_type" gorm:"type:varchar(255);not null"`
	// Filter is an equality match on envelope fields, e.g.
	// {"worker":"email-answerer"}. Anything smarter belongs in the reacting
	// worker's prompt.
	Filter JSONMap `json:"filter" gorm:"type:jsonb"`
	Worker string  `json:"worker" gorm:"type:varchar(255);not null"`
	// MaxFiringsPerHour is 0 = unlimited (§8.3). Excess deliveries are recorded
	// rate_limited, with one subscription.throttled event per rolling hour.
	MaxFiringsPerHour int `json:"max_firings_per_hour"`
	// Enabled carries no gorm default on purpose: GORM omits zero-valued
	// fields that have one, which would make a disabled subscription silently
	// come back enabled. The HTTP layer defaults an absent field to true.
	Enabled   bool  `json:"enabled"`
	CreatedAt int64 `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt int64 `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Subscription) TableName() string { return "subscriptions" }

// Delivery status vocabulary (§8.4). This list is EXACTLY the legal set — a
// test pins it. Note there is deliberately no "dropped": the per-subscription
// concurrency mode that would have produced it was removed 2026-07-25 in
// favour of the worker-level max_instances gate, and deliveries for a worker at
// capacity simply stay pending.
const (
	DeliveryPending       = "pending"
	DeliveryRunning       = "running"
	DeliveryOK            = "ok"
	DeliveryFailed        = "failed"
	DeliveryAwaitingHuman = "awaiting_human"
	DeliveryRateLimited   = "rate_limited"
)

// DeliveryStatuses is the complete, ordered delivery-status vocabulary.
var DeliveryStatuses = []string{
	DeliveryPending,
	DeliveryRunning,
	DeliveryOK,
	DeliveryFailed,
	DeliveryAwaitingHuman,
	DeliveryRateLimited,
}

// ValidDeliveryStatus reports whether s is in the vocabulary.
func ValidDeliveryStatus(s string) bool {
	for _, v := range DeliveryStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// isTerminalDeliveryStatus reports whether a status ends the attempt (and so
// stamps ended_at).
func isTerminalDeliveryStatus(s string) bool {
	switch s {
	case DeliveryOK, DeliveryFailed, DeliveryRateLimited:
		return true
	}
	return false
}

// EventDelivery is one (event, subscription) attempt: the at-least-once
// idempotency guard and the job-history row the UI renders (§8.4 step 2).
// (event_id, subscription_id) is unique.
type EventDelivery struct {
	ID             string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Project        string `json:"project" gorm:"type:varchar(255);not null;index:idx_event_deliveries_project"`
	EventID        string `json:"event_id" gorm:"type:varchar(36);not null;uniqueIndex:idx_event_deliveries_pair,priority:1"`
	SubscriptionID string `json:"subscription_id" gorm:"type:varchar(36);not null;uniqueIndex:idx_event_deliveries_pair,priority:2"`
	SessionID      string `json:"session_id" gorm:"type:varchar(36)"`
	// Worker is denormalised from the subscription (or the schedule) at dispatch
	// time. It is what the shared dispatch gate counts and queues on (§8.4 step
	// 7): a schedule firing has no subscription row to join through, and the gate
	// must behave identically for both paths. Added by migration 024.
	Worker string `json:"worker" gorm:"type:varchar(255);index:idx_ed_worker"`
	// ScheduleID names the schedule a firing came from; empty for event-matched
	// deliveries. Schedule-fired rows carry the same id in SubscriptionID so the
	// (event_id, subscription_id) idempotency index covers both paths unchanged.
	ScheduleID string `json:"schedule_id" gorm:"type:varchar(36)"`
	// Status is one of DeliveryStatuses.
	Status    string `json:"status" gorm:"type:varchar(30);not null"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"`
	CreatedAt int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

func (EventDelivery) TableName() string { return "event_deliveries" }

// ── Queries ─────────────────────────────────────────────────────────────────

// ProjectEventQuery filters the append-only event log. Project is mandatory at
// the store surface: there is no cross-project read.
type ProjectEventQuery struct {
	Project string
	Type    string // exact type, optional
	Limit   int
	Offset  int
}

// DeliveryQuery filters the delivery log. Project is mandatory.
type DeliveryQuery struct {
	Project        string
	EventID        string
	SubscriptionID string
	// SessionID finds the delivery a session is running — the reverse of the
	// depth walk, and what the lease reaper needs to close the job history of a
	// session nobody reported back on (§8.4 step 4).
	SessionID string
	Status    string
	Limit     int
	Offset    int
}

const defaultEventLimit = 100

func clampLimit(n int) int {
	if n <= 0 {
		return defaultEventLimit
	}
	if n > 1000 {
		return 1000
	}
	return n
}

// ── project_events ──────────────────────────────────────────────────────────

// CreateProjectEvent appends an event. The caller (ingestion handler or an
// internal emitter) owns the envelope — the store only checks it is coherent.
func (s *Store) CreateProjectEvent(ctx context.Context, ev *ProjectEvent) (*ProjectEvent, error) {
	if ev == nil {
		return nil, fmt.Errorf("project event is required")
	}
	if strings.TrimSpace(ev.Project) == "" {
		return nil, fmt.Errorf("project is required")
	}
	if strings.TrimSpace(ev.Type) == "" {
		return nil, fmt.Errorf("event type is required")
	}
	if ev.Envelope.Source == "" {
		return nil, fmt.Errorf("envelope source is required (one of %s)", strings.Join(EventSources, "|"))
	}
	if !validEventSource(ev.Envelope.Source) {
		return nil, fmt.Errorf("invalid envelope source %q (want one of %s)",
			ev.Envelope.Source, strings.Join(EventSources, "|"))
	}
	if ev.Envelope.Depth < 0 {
		return nil, fmt.Errorf("envelope depth must not be negative")
	}
	if ev.ID == "" {
		ev.ID = uuid.New().String()
	}
	if ev.OccurredAt == 0 {
		ev.OccurredAt = eventsNow()
	}
	if err := s.gdb.WithContext(ctx).Create(ev).Error; err != nil {
		return nil, fmt.Errorf("failed to create project event: %w", err)
	}
	return ev, nil
}

func validEventSource(src string) bool {
	for _, v := range EventSources {
		if v == src {
			return true
		}
	}
	return false
}

// GetProjectEvent reads one event. A row belonging to another project looks
// like a missing row — the only project-isolation answer a caller ever gets.
func (s *Store) GetProjectEvent(ctx context.Context, project, id string) (*ProjectEvent, error) {
	if project == "" || id == "" {
		return nil, fmt.Errorf("project and id are required")
	}
	var ev ProjectEvent
	err := s.gdb.WithContext(ctx).Where("project = ? AND id = ?", project, id).First(&ev).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("project event not found")
		}
		return nil, fmt.Errorf("failed to get project event: %w", err)
	}
	return &ev, nil
}

// ListProjectEvents returns the project's events newest-first.
func (s *Store) ListProjectEvents(ctx context.Context, q ProjectEventQuery) ([]*ProjectEvent, error) {
	if q.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	db := s.gdb.WithContext(ctx).Model(&ProjectEvent{}).Where("project = ?", q.Project)
	if q.Type != "" {
		db = db.Where("type = ?", q.Type)
	}
	out := []*ProjectEvent{}
	if err := db.Order("occurred_at DESC, id DESC").
		Limit(clampLimit(q.Limit)).Offset(q.Offset).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list project events: %w", err)
	}
	return out, nil
}

// ListUndeliveredProjectEvents returns undelivered events oldest-first across
// every project — the router's poll (§8.4 step 2). It is the one deliberately
// unscoped read in this file: the router is core, not a tenant.
func (s *Store) ListUndeliveredProjectEvents(ctx context.Context, limit int) ([]*ProjectEvent, error) {
	out := []*ProjectEvent{}
	if err := s.gdb.WithContext(ctx).Model(&ProjectEvent{}).
		Where("delivered = ?", false).
		Order("occurred_at ASC, id ASC").
		Limit(clampLimit(limit)).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list undelivered project events: %w", err)
	}
	return out, nil
}

// MarkProjectEventDelivered flips the router watermark. It is the only mutation
// of an otherwise append-only table, and it touches no sender-visible field.
func (s *Store) MarkProjectEventDelivered(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("project event id is required")
	}
	res := s.gdb.WithContext(ctx).Model(&ProjectEvent{}).
		Where("id = ?", id).Update("delivered", true)
	if res.Error != nil {
		return fmt.Errorf("failed to mark project event delivered: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("project event not found")
	}
	return nil
}

// SessionTriggerEvent returns the event that triggered the job running in
// sessionID, or (nil, nil) when there is none.
//
// The link is the delivery row: the router stamps `session_id` on the delivery
// when it dispatches a matched event, so a session's earliest delivery names the
// event that caused it. That event's depth is what the emitters (§8.2) add one
// to — the loop floor of §8.4 — and its absence is exactly the "a human started
// this" case, which is depth 0.
//
// A read, not a mutation: it touches neither the event log nor the config log.
func (s *Store) SessionTriggerEvent(ctx context.Context, sessionID string) (*ProjectEvent, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id is required")
	}
	var d EventDelivery
	err := s.gdb.WithContext(ctx).Model(&EventDelivery{}).
		Where("session_id = ?", sessionID).
		Order("created_at ASC, id ASC").First(&d).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to look up session trigger delivery: %w", err)
	}
	var ev ProjectEvent
	err = s.gdb.WithContext(ctx).Model(&ProjectEvent{}).
		Where("id = ?", d.EventID).First(&ev).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// A delivery pointing at a vanished event is not a reason to fail the
			// job's outcome event — treat it as "no trigger" (depth 0).
			return nil, nil
		}
		return nil, fmt.Errorf("failed to load session trigger event: %w", err)
	}
	return &ev, nil
}

// ── subscriptions ───────────────────────────────────────────────────────────

// validateSubscription enforces the §8.3 shape: an exact or trailing-`*` event
// type, a worker, and a non-negative firing cap. No other patterns exist.
func validateSubscription(sub *Subscription) error {
	if sub == nil {
		return fmt.Errorf("subscription is required")
	}
	if strings.TrimSpace(sub.Project) == "" {
		return fmt.Errorf("project is required")
	}
	if strings.TrimSpace(sub.EventType) == "" {
		return fmt.Errorf("event_type is required")
	}
	if sub.EventType != strings.TrimSpace(sub.EventType) {
		return fmt.Errorf("event_type must not have surrounding whitespace")
	}
	if star := strings.Index(sub.EventType, "*"); star >= 0 && star != len(sub.EventType)-1 {
		return fmt.Errorf("event_type %q: `*` is only legal as a trailing wildcard", sub.EventType)
	}
	if sub.EventType == "*" {
		return fmt.Errorf("event_type `*` (match everything) is not a supported pattern")
	}
	if strings.TrimSpace(sub.Worker) == "" {
		return fmt.Errorf("worker is required")
	}
	if sub.MaxFiringsPerHour < 0 {
		return fmt.Errorf("max_firings_per_hour must not be negative (0 = unlimited)")
	}
	return nil
}

// CreateSubscription stores a new subscription. Enabled is taken literally —
// callers that want the "new subscriptions are live" default set it themselves
// (the HTTP layer does).
//
// Routing configuration is configuration: the write appends a
// `subscription_create` event in the same transaction (§15.3/§15.4). cw is the
// who/why; a human/API edit passes the zero value.
func (s *Store) CreateSubscription(ctx context.Context, sub *Subscription, cw ConfigWrite) (*Subscription, error) {
	if err := validateSubscription(sub); err != nil {
		return nil, err
	}
	if sub.ID == "" {
		sub.ID = uuid.New().String()
	}
	if sub.Filter == nil {
		sub.Filter = JSONMap{}
	}
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: sub.Project,
		Action:  ActionSubscriptionCreate,
		Payload: sub,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		return tx.Create(sub).Error
	}); err != nil {
		return nil, fmt.Errorf("failed to create subscription: %w", err)
	}
	return sub, nil
}

// GetSubscription reads one subscription within a project. Another project's
// row looks like a missing row.
func (s *Store) GetSubscription(ctx context.Context, project, id string) (*Subscription, error) {
	if project == "" || id == "" {
		return nil, fmt.Errorf("project and id are required")
	}
	var sub Subscription
	err := s.gdb.WithContext(ctx).Where("project = ? AND id = ?", project, id).First(&sub).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("subscription not found")
		}
		return nil, fmt.Errorf("failed to get subscription: %w", err)
	}
	return &sub, nil
}

// ListSubscriptions returns a project's subscriptions, newest-first.
func (s *Store) ListSubscriptions(ctx context.Context, project string) ([]*Subscription, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	out := []*Subscription{}
	if err := s.gdb.WithContext(ctx).Model(&Subscription{}).
		Where("project = ?", project).
		Order("created_at DESC, id DESC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}
	return out, nil
}

// ListEnabledSubscriptions returns a project's live subscriptions — what the
// router matches an arriving event against.
func (s *Store) ListEnabledSubscriptions(ctx context.Context, project string) ([]*Subscription, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	out := []*Subscription{}
	if err := s.gdb.WithContext(ctx).Model(&Subscription{}).
		Where("project = ? AND enabled = ?", project, true).
		Order("created_at ASC, id ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list enabled subscriptions: %w", err)
	}
	return out, nil
}

// UpdateSubscription overwrites the mutable fields of an existing row. The
// project on sub is the authorization boundary: a row owned by another project
// is never found, so it is never written.
func (s *Store) UpdateSubscription(ctx context.Context, sub *Subscription, cw ConfigWrite) (*Subscription, error) {
	if sub == nil || sub.ID == "" {
		return nil, fmt.Errorf("subscription id is required")
	}
	if err := validateSubscription(sub); err != nil {
		return nil, err
	}
	existing, err := s.GetSubscription(ctx, sub.Project, sub.ID)
	if err != nil {
		return nil, err
	}
	existing.EventType = sub.EventType
	existing.Filter = sub.Filter
	if existing.Filter == nil {
		existing.Filter = JSONMap{}
	}
	existing.Worker = sub.Worker
	existing.MaxFiringsPerHour = sub.MaxFiringsPerHour
	existing.Enabled = sub.Enabled
	// One action whether or not the write flips `enabled`: unlike workers,
	// §15.3 gives subscriptions no enable/disable verbs — a disabled
	// subscription is an ordinary field change on the routing row.
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: existing.Project,
		Action:  ActionSubscriptionUpdate,
		Payload: existing,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		return tx.Save(existing).Error
	}); err != nil {
		return nil, fmt.Errorf("failed to update subscription: %w", err)
	}
	return existing, nil
}

// DeleteSubscription removes a project's subscription. Deleting another
// project's row is a not-found, never a silent success.
//
// The delete appends too (§15.3 rule 2), carrying the subscription as it last
// stood — which is what makes "restore the subscription we deleted on Tuesday"
// a lookup (§15.7). The row is read first so there is a final state to carry.
func (s *Store) DeleteSubscription(ctx context.Context, project, id string, cw ConfigWrite) error {
	if project == "" || id == "" {
		return fmt.Errorf("project and id are required")
	}
	existing, err := s.GetSubscription(ctx, project, id)
	if err != nil {
		return err
	}
	vanished := false
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: project,
		Action:  ActionSubscriptionDelete,
		Payload: existing,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		res := tx.Where("project = ? AND id = ?", project, id).Delete(&Subscription{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Lost a race with a concurrent delete: roll back rather than log a
			// deletion this call did not perform.
			vanished = true
			return fmt.Errorf("subscription not found")
		}
		return nil
	}); err != nil {
		if vanished {
			return fmt.Errorf("subscription not found")
		}
		return fmt.Errorf("failed to delete subscription: %w", err)
	}
	return nil
}

// ── event_deliveries ────────────────────────────────────────────────────────

// EnsureDelivery is the at-least-once idempotency guard (§8.4 step 2): the
// first call for an (event_id, subscription_id) pair creates the row and
// returns created=true; every later call returns the stored row untouched with
// created=false. A crashed router that retries therefore cannot double-deliver.
func (s *Store) EnsureDelivery(ctx context.Context, d *EventDelivery) (*EventDelivery, bool, error) {
	if d == nil {
		return nil, false, fmt.Errorf("delivery is required")
	}
	if strings.TrimSpace(d.Project) == "" {
		return nil, false, fmt.Errorf("project is required")
	}
	if d.EventID == "" {
		return nil, false, fmt.Errorf("event_id is required")
	}
	if d.SubscriptionID == "" {
		return nil, false, fmt.Errorf("subscription_id is required")
	}
	if d.Status == "" {
		d.Status = DeliveryPending
	}
	if !ValidDeliveryStatus(d.Status) {
		return nil, false, fmt.Errorf("invalid delivery status %q (want one of %s)",
			d.Status, strings.Join(DeliveryStatuses, "|"))
	}
	if existing, err := s.findDeliveryPair(ctx, d.EventID, d.SubscriptionID); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, fmt.Errorf("failed to look up delivery: %w", err)
	}
	if d.ID == "" {
		d.ID = uuid.New().String()
	}
	if err := s.gdb.WithContext(ctx).Create(d).Error; err != nil {
		// Lost the race against a concurrent router: the unique index fired.
		// Re-read rather than surfacing a driver-specific duplicate-key error.
		if existing, lookupErr := s.findDeliveryPair(ctx, d.EventID, d.SubscriptionID); lookupErr == nil {
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("failed to create delivery: %w", err)
	}
	return d, true, nil
}

func (s *Store) findDeliveryPair(ctx context.Context, eventID, subscriptionID string) (*EventDelivery, error) {
	var d EventDelivery
	err := s.gdb.WithContext(ctx).
		Where("event_id = ? AND subscription_id = ?", eventID, subscriptionID).
		First(&d).Error
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// DeliveryStatusUpdate is the transition applied by UpdateDeliveryStatus.
// SessionID is written when non-empty (the router learns it at dispatch).
type DeliveryStatusUpdate struct {
	Status    string
	SessionID string
}

// UpdateDeliveryStatus moves a delivery through the vocabulary and stamps the
// lifecycle timestamps: started_at on the first transition to running,
// ended_at on any terminal status. An unknown status is refused loudly — the
// vocabulary is closed.
func (s *Store) UpdateDeliveryStatus(ctx context.Context, project, id string, u DeliveryStatusUpdate) (*EventDelivery, error) {
	if project == "" || id == "" {
		return nil, fmt.Errorf("project and id are required")
	}
	if !ValidDeliveryStatus(u.Status) {
		return nil, fmt.Errorf("invalid delivery status %q (want one of %s)",
			u.Status, strings.Join(DeliveryStatuses, "|"))
	}
	var d EventDelivery
	err := s.gdb.WithContext(ctx).Where("project = ? AND id = ?", project, id).First(&d).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("delivery not found")
		}
		return nil, fmt.Errorf("failed to get delivery: %w", err)
	}
	d.Status = u.Status
	if u.SessionID != "" {
		d.SessionID = u.SessionID
	}
	now := eventsNow()
	if u.Status == DeliveryRunning && d.StartedAt == 0 {
		d.StartedAt = now
	}
	if isTerminalDeliveryStatus(u.Status) && d.EndedAt == 0 {
		d.EndedAt = now
	}
	if u.Status == DeliveryAwaitingHuman {
		// Awaiting a human is a pause, not an end: ended_at stays unset so the
		// UI shows an open-ended duration rather than a finished job.
		d.EndedAt = 0
	}
	if err := s.gdb.WithContext(ctx).Save(&d).Error; err != nil {
		return nil, fmt.Errorf("failed to update delivery: %w", err)
	}
	return &d, nil
}

// ListDeliveries returns a project's deliveries newest-first.
func (s *Store) ListDeliveries(ctx context.Context, q DeliveryQuery) ([]*EventDelivery, error) {
	if q.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if q.Status != "" && !ValidDeliveryStatus(q.Status) {
		return nil, fmt.Errorf("invalid delivery status %q (want one of %s)",
			q.Status, strings.Join(DeliveryStatuses, "|"))
	}
	db := s.gdb.WithContext(ctx).Model(&EventDelivery{}).Where("project = ?", q.Project)
	if q.EventID != "" {
		db = db.Where("event_id = ?", q.EventID)
	}
	if q.SubscriptionID != "" {
		db = db.Where("subscription_id = ?", q.SubscriptionID)
	}
	if q.SessionID != "" {
		db = db.Where("session_id = ?", q.SessionID)
	}
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	out := []*EventDelivery{}
	if err := db.Order("created_at DESC, id DESC").
		Limit(clampLimit(q.Limit)).Offset(q.Offset).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list deliveries: %w", err)
	}
	return out, nil
}

// ── The shared dispatch gate's reads (§8.4 steps 3 and 7) ───────────────────
//
// These three helpers are what the ONE gated dispatch point in agentd
// (cmd/agentd/dispatch.go) counts and queues on. Router and scheduler share
// them, which is what makes "a firing for a worker already at max_instances
// queues as pending" true for both paths rather than for whichever one
// remembered to check.
//
// "Active" means status = running: a delivery that has started a session and has
// not finished. pending deliveries are the queue, terminal ones are history.

// CountActiveDeliveries counts a project's in-flight jobs — what the per-project
// max_concurrent_jobs cap (§5, §8.4 step 3) is measured against.
func (s *Store) CountActiveDeliveries(ctx context.Context, project string) (int64, error) {
	if project == "" {
		return 0, fmt.Errorf("project is required")
	}
	var n int64
	if err := s.gdb.WithContext(ctx).Model(&EventDelivery{}).
		Where("project = ? AND status = ?", project, DeliveryRunning).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("failed to count active deliveries: %w", err)
	}
	return n, nil
}

// CountActiveDeliveriesForWorker counts one worker's in-flight jobs — what
// `max_instances` (§6.1, §8.4 step 7) is measured against.
func (s *Store) CountActiveDeliveriesForWorker(ctx context.Context, project, worker string) (int64, error) {
	if project == "" || worker == "" {
		return 0, fmt.Errorf("project and worker are required")
	}
	var n int64
	if err := s.gdb.WithContext(ctx).Model(&EventDelivery{}).
		Where("project = ? AND worker = ? AND status = ?", project, worker, DeliveryRunning).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("failed to count active deliveries for worker: %w", err)
	}
	return n, nil
}

// ListPendingDeliveries returns a project's queued deliveries OLDEST FIRST —
// the FIFO order §8.4 step 7 requires when instances free up. Scoped to one
// worker when worker is non-empty.
func (s *Store) ListPendingDeliveries(ctx context.Context, project, worker string, limit int) ([]*EventDelivery, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	db := s.gdb.WithContext(ctx).Model(&EventDelivery{}).
		Where("project = ? AND status = ?", project, DeliveryPending)
	if worker != "" {
		db = db.Where("worker = ?", worker)
	}
	out := []*EventDelivery{}
	if err := db.Order("created_at ASC, id ASC").
		Limit(clampLimit(limit)).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list pending deliveries: %w", err)
	}
	return out, nil
}

// ListProjectsWithPendingDeliveries returns the distinct projects that have at
// least one queued delivery — the router's drain list (§8.4 step 7).
//
// Deliberately unscoped, like ListUndeliveredProjectEvents: the router is core,
// not a tenant, and a project whose deliveries queued behind a busy worker must
// get its turn even when no new event arrives for it.
func (s *Store) ListProjectsWithPendingDeliveries(ctx context.Context) ([]string, error) {
	out := []string{}
	if err := s.gdb.WithContext(ctx).Model(&EventDelivery{}).
		Where("status = ?", DeliveryPending).
		Distinct().Order("project ASC").
		Pluck("project", &out).Error; err != nil {
		return nil, fmt.Errorf("failed to list projects with pending deliveries: %w", err)
	}
	return out, nil
}

// CountRateLimitedDeliveriesSince counts the deliveries a subscription had
// REFUSED at or after `since` (unix seconds).
//
// This is the record §8.2's "at most one `subscription.throttled` per
// subscription per rolling-60-minute window" is derived from: the router emits
// the event only when this returns 0 for the last hour, so a subscription that
// is being throttled continuously says so once rather than once per event.
func (s *Store) CountRateLimitedDeliveriesSince(ctx context.Context, subscriptionID string, since int64) (int64, error) {
	if subscriptionID == "" {
		return 0, fmt.Errorf("subscription_id is required")
	}
	var n int64
	if err := s.gdb.WithContext(ctx).Model(&EventDelivery{}).
		Where("subscription_id = ? AND created_at >= ? AND status = ?",
			subscriptionID, since, DeliveryRateLimited).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("failed to count rate-limited deliveries: %w", err)
	}
	return n, nil
}

// CountSubscriptionFiringsSince counts deliveries created for a subscription at
// or after `since` (unix seconds) that actually consumed a firing — i.e. every
// status except rate_limited, which is the record of a firing that was refused.
// This is what max_firings_per_hour is measured against (§8.3).
func (s *Store) CountSubscriptionFiringsSince(ctx context.Context, subscriptionID string, since int64) (int64, error) {
	if subscriptionID == "" {
		return 0, fmt.Errorf("subscription_id is required")
	}
	var n int64
	if err := s.gdb.WithContext(ctx).Model(&EventDelivery{}).
		Where("subscription_id = ? AND created_at >= ? AND status <> ?",
			subscriptionID, since, DeliveryRateLimited).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("failed to count subscription firings: %w", err)
	}
	return n, nil
}
