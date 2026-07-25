// Package agentdb — the config log (spec §15, docs/product/09-config-log.md).
//
// # What this file is
//
// `config_events` is the append-only log of every *configuration* mutation in a
// project: workers hired and retuned, prompts rewritten, subscriptions and
// schedules created and deleted, images and skills published. The ordinary
// tables (`workers`, `subscriptions`, `schedules`, `project_settings`,
// `agent_custom_images`, `agent_skills`, …) are **projections** of this log —
// caches of the fold, kept because reading current configuration must be one
// indexed lookup. Nothing on the hot path reads `config_events` (§15.4).
//
// Payload is the **full new state** of the mutated row, never a diff (§15.2):
// folding a log of full states is last-writer-wins per key, with no merge
// algebra. Diffs are a read-time concern (the changelog UI, §15.10).
//
// Deletes append too, carrying the row as it last stood (§15.3): the projection
// row disappears, the record does not.
//
// # ADOPTION RECIPE — read this before writing a configuration mutation
//
// Every store method that mutates project configuration writes its projection
// row and its config-log row in ONE transaction (§15.4). Three steps:
//
//  1. Take a ConfigWrite parameter (the who/why: acting worker, acting session,
//     rationale). Human/API edits pass the zero value. `rationale` is REQUIRED
//     on the two prompt writes (§15.5) and optional everywhere else — the seam
//     enforces that, callers do not.
//
//  2. Do all projection writes inside the WithConfigEvent closure, using the
//     `tx` handed to it — never `s.gdb`. Reads/validation may happen before.
//
//  3. Register the method in ConfigMutations below, naming the actions it may
//     write and the projection tables it touches, and add a probe for it in
//     config_events_test.go. TestMutationsAreLogged discovers configuration
//     mutation methods by reflection and FAILS on any that is neither
//     registered nor explicitly exempted — that is what stops a later track
//     from forgetting. Registering a table also puts it under the write guard
//     (InstallConfigEventGuard), which rejects writes to a projection table
//     made outside a config-event transaction.
//
// The whole recipe, in one method:
//
//	func (s *Store) CreateWorker(ctx context.Context, w *Worker, cw ConfigWrite) (*Worker, error) {
//		if w.Name == "" {
//			return nil, fmt.Errorf("worker name is required") // validate BEFORE either write
//		}
//		_, err := s.WithConfigEvent(ctx, ConfigChange{
//			Project: w.Project,
//			Action:  ActionWorkerCreate,
//			Payload: w, // the FULL new row, never a diff
//			Write:   cw,
//		}, func(tx *gorm.DB) error {
//			return tx.Create(w).Error // on tx, never on s.gdb
//		})
//		if err != nil {
//			return nil, err
//		}
//		return w, nil // then read back and echo (§9)
//	}
//
// Deletes take the same shape: the closure deletes the projection row, the
// event carries the row as it last stood and a `*_delete` action.
//
// Do NOT emit the routable `config.changed` event from here: it is emitted
// AFTER commit, never inside the transaction (§15.4) — that is item J3.
package agentdb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ── The record (§15.2) ──────────────────────────────────────────────────────

// ConfigEvent is one record in the append-only configuration log.
//
// CreatedAt is unix **milliseconds**, deliberately finer than the seconds used
// by the older agentdb tables: the fold (§15.6) orders by (created_at, id), and
// second resolution would leave same-second writes ordered by a random uuid.
type ConfigEvent struct {
	ID           string  `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Project      string  `json:"project" gorm:"type:text;not null;index:idx_config_events_project"`
	ActorWorker  string  `json:"actor_worker" gorm:"type:text;default:''"`
	ActorSession string  `json:"actor_session" gorm:"type:text;default:''"`
	Action       string  `json:"action" gorm:"type:text;not null"`
	Payload      JSONMap `json:"payload" gorm:"type:jsonb;default:'{}'"`
	Rationale    string  `json:"rationale" gorm:"type:text;default:''"`
	CreatedAt    int64   `json:"created_at" gorm:"not null;default:0"`
}

func (ConfigEvent) TableName() string { return "config_events" }

// The closed action vocabulary of §15.3. Nothing outside this list may be
// logged; adding a verb is a spec change, not an implementation detail.
const (
	ActionWorkerCreate       = "worker_create"
	ActionWorkerUpdate       = "worker_update"
	ActionWorkerEnable       = "worker_enable"
	ActionWorkerDisable      = "worker_disable"
	ActionWorkerPromptWrite  = "worker_prompt_write"
	ActionProjectPromptWrite = "project_prompt_write"
	ActionProjectSettingsPut = "project_settings_put"
	ActionSubscriptionCreate = "subscription_create"
	ActionSubscriptionUpdate = "subscription_update"
	ActionSubscriptionDelete = "subscription_delete"
	ActionScheduleCreate     = "schedule_create"
	ActionScheduleUpdate     = "schedule_update"
	ActionScheduleDelete     = "schedule_delete"
	ActionImageCreate        = "image_create"
	ActionSkillCreate        = "skill_create"
)

// ConfigActions is the complete §15.3 vocabulary, in spec order.
var ConfigActions = []string{
	ActionWorkerCreate,
	ActionWorkerUpdate,
	ActionWorkerEnable,
	ActionWorkerDisable,
	ActionWorkerPromptWrite,
	ActionProjectPromptWrite,
	ActionProjectSettingsPut,
	ActionSubscriptionCreate,
	ActionSubscriptionUpdate,
	ActionSubscriptionDelete,
	ActionScheduleCreate,
	ActionScheduleUpdate,
	ActionScheduleDelete,
	ActionImageCreate,
	ActionSkillCreate,
}

// rationaleRequired lists the actions whose rationale may not be empty (§15.5).
// Prompt rewrites are the self-improvement loop, and the *why* is the one thing
// not recoverable from the text. Core validates non-empty and nothing else —
// judging whether a rationale is good is a reviewing worker's job (P1).
var rationaleRequired = map[string]bool{
	ActionWorkerPromptWrite:  true,
	ActionProjectPromptWrite: true,
}

func isConfigAction(action string) bool {
	for _, a := range ConfigActions {
		if a == action {
			return true
		}
	}
	return false
}

// ── The seam (§15.4) ────────────────────────────────────────────────────────

// ConfigWrite is the who/why every configuration-mutation store method takes.
// Worker and Session name the acting worker and the session it acted from
// (§15.2) — both empty for human/UI/API edits, whose identity is the login
// audit's business, not the config log's. Rationale is the commit-message why.
type ConfigWrite struct {
	Worker    string `json:"worker,omitempty"`
	Session   string `json:"session,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

// ConfigChange describes one configuration mutation for the log: what changed,
// in which project, and the full new state of the changed row.
type ConfigChange struct {
	// Project is the hard namespace (P5) — required, never inferred.
	Project string
	// Action is one of the §15.3 vocabulary constants.
	Action string
	// Payload is the FULL new state of the mutated record — a struct, a map, or
	// a JSONMap; it must marshal to a JSON object. Never a diff. For a delete,
	// the row as it last stood.
	Payload any
	// Write carries the actor and the rationale.
	Write ConfigWrite
}

// validate applies the shape rules of §15.2/§15.3/§15.5 before anything is
// written. Malformed input fails before either row lands (§15.4).
func (c ConfigChange) validate() error {
	if strings.TrimSpace(c.Project) == "" {
		return fmt.Errorf("agentdb: config event requires a project (P5: the namespace is never inferred)")
	}
	if !isConfigAction(c.Action) {
		return fmt.Errorf("agentdb: %q is not a config-log action; the §15.3 vocabulary is closed: %s",
			c.Action, strings.Join(ConfigActions, ", "))
	}
	if rationaleRequired[c.Action] && strings.TrimSpace(c.Write.Rationale) == "" {
		return fmt.Errorf("agentdb: action %q requires a non-empty rationale (§15.5)", c.Action)
	}
	if c.Payload == nil {
		return fmt.Errorf("agentdb: config event %q requires a payload — the full new state, never a diff (§15.2)", c.Action)
	}
	return nil
}

// configPayload marshals a full-state payload into the jsonb column. It must be
// a JSON object: the fold (§15.6) keys on entity, and a bare scalar carries no
// state to project.
func configPayload(v any) (JSONMap, error) {
	if m, ok := v.(JSONMap); ok {
		if m == nil {
			return nil, fmt.Errorf("agentdb: config event payload is nil")
		}
		return m, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("agentdb: marshal config payload: %w", err)
	}
	var m JSONMap
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("agentdb: config payload must be a JSON object (full new state, §15.2): %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("agentdb: config event payload is nil")
	}
	return m, nil
}

// configTxKey marks a context as being inside a config-event transaction. The
// write guard (InstallConfigEventGuard) looks for it.
type configTxKey struct{}

// WithConfigEvent is THE seam: it runs fn's projection writes and appends the
// config-log record in ONE transaction (§15.4). Either both land or neither
// does — there is no window in which the projection says one thing and the log
// says another, and no reconciliation job to write.
//
// fn MUST perform its writes on the supplied tx (not on s.gdb): only writes on
// tx are inside the transaction, and only they satisfy the write guard.
//
// The returned ConfigEvent is the committed record — its ID is what J3 quotes
// in the `config.changed` text and what a restore rationale names (§15.7).
// Emission of `config.changed` happens AFTER this call returns, never inside
// (§15.4); that is J3.
func (s *Store) WithConfigEvent(ctx context.Context, c ConfigChange, fn func(tx *gorm.DB) error) (*ConfigEvent, error) {
	if fn == nil {
		return nil, fmt.Errorf("agentdb: WithConfigEvent requires a mutation function")
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	payload, err := configPayload(c.Payload)
	if err != nil {
		return nil, err
	}

	ev := &ConfigEvent{
		ID:           uuid.New().String(),
		Project:      c.Project,
		ActorWorker:  c.Write.Worker,
		ActorSession: c.Write.Session,
		Action:       c.Action,
		Payload:      payload,
		Rationale:    c.Write.Rationale,
		CreatedAt:    time.Now().UnixMilli(),
	}

	txCtx := context.WithValue(ctx, configTxKey{}, true)
	if err := s.gdb.WithContext(txCtx).Transaction(func(tx *gorm.DB) error {
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Create(ev).Error
	}); err != nil {
		return nil, err
	}
	return ev, nil
}

// ── Reading the log (history/audit only — never the hot path) ───────────────

// ConfigEventQuery filters the log. Project is REQUIRED: a caller physically
// cannot read another project's history (P5, §15.9). Filtering is equality plus
// a time range and a trailing-`*` action prefix — deliberately the same
// austerity as subscription filters and label selectors.
type ConfigEventQuery struct {
	Project     string // required
	Action      string // exact, or a trailing-`*` prefix such as "worker_*"
	ActorWorker string
	Since       int64 // inclusive, unix ms; 0 = unbounded
	Until       int64 // inclusive, unix ms; 0 = unbounded
	Limit       int   // 0 = no limit
}

// ListConfigEvents returns matching records newest first. This is history,
// replay and audit only: no runtime path reads the log (§15.4).
func (s *Store) ListConfigEvents(ctx context.Context, q ConfigEventQuery) ([]*ConfigEvent, error) {
	if strings.TrimSpace(q.Project) == "" {
		return nil, fmt.Errorf("agentdb: ListConfigEvents requires a project (P5)")
	}
	db := s.gdb.WithContext(ctx).Model(&ConfigEvent{}).Where("project = ?", q.Project)
	if q.Action != "" {
		if strings.HasSuffix(q.Action, "*") {
			db = db.Where("action LIKE ?", strings.TrimSuffix(q.Action, "*")+"%")
		} else {
			db = db.Where("action = ?", q.Action)
		}
	}
	if q.ActorWorker != "" {
		db = db.Where("actor_worker = ?", q.ActorWorker)
	}
	if q.Since > 0 {
		db = db.Where("created_at >= ?", q.Since)
	}
	if q.Until > 0 {
		db = db.Where("created_at <= ?", q.Until)
	}
	if q.Limit > 0 {
		db = db.Limit(q.Limit)
	}
	var out []*ConfigEvent
	if err := db.Order("created_at DESC, id DESC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("agentdb: list config events: %w", err)
	}
	if out == nil {
		out = []*ConfigEvent{}
	}
	return out, nil
}

// ── Conformance: the registry that stops a later track from forgetting ──────

// ConfigMutation registers one *Store method as a configuration mutation.
//
// TestMutationsAreLogged enumerates the store's methods by reflection and fails
// on any that looks like a configuration mutation but appears in neither this
// registry nor ConfigMutationExempt. Adding a mutation method therefore forces
// an explicit, reviewable decision.
type ConfigMutation struct {
	// Method is the exact *Store method name.
	Method string
	// Actions are the §15.3 actions the method may write. Exactly one is
	// written per call.
	Actions []string
	// Tables are the projection tables the method writes. Listing a table here
	// also puts it under the write guard.
	Tables []string
}

// ConfigMutations is the registry. APPEND to it when you add a configuration
// mutation method (see the adoption recipe at the top of this file).
//
// Tracks B1/C1/E1/E4/H1/I2/I3 add their entries as they land:
//
//	{Method: "PutProjectSettings", Actions: []string{ActionProjectSettingsPut}, Tables: []string{"project_settings"}},
//	{Method: "CreateWorker",       Actions: []string{ActionWorkerCreate},       Tables: []string{"workers"}},
//	{Method: "DeleteSubscription", Actions: []string{ActionSubscriptionDelete}, Tables: []string{"subscriptions"}},
//	…
var ConfigMutations = []ConfigMutation{
	{
		Method:  "UpsertSkill",
		Actions: []string{ActionSkillCreate},
		Tables:  []string{"agent_skills"},
	},
	{
		Method:  "UpsertCustomImage",
		Actions: []string{ActionImageCreate},
		Tables:  []string{"agent_custom_images"},
	},
	{
		// The §13 catalogue write (I1). Publishing an environment is part of the
		// organisation's history, so burning a version appends `image_create`
		// with the burning worker/session as the actor (§13.4).
		Method:  "CreateCustomImage",
		Actions: []string{ActionImageCreate},
		Tables:  []string{"agent_custom_images"},
	},
}

// ConfigMutationExempt lists methods that the reflection sweep flags but that
// deliberately write no config event, each with the reason. An exemption is a
// decision on the record: the conformance test pins this map, so growing it is
// a deliberate edit, never a silent omission.
var ConfigMutationExempt = map[string]string{
	"SetWorkerBinding": "runtime session↔container binding (execenv placement), not project configuration; " +
		"the §6.1 workers table is C1's and its methods must register",
	"ClearWorkerBinding": "runtime session↔container binding (execenv placement), not project configuration",
	"SetSkillVisibility": "legacy host-side catalogue moderation (pre-§14 visibility model); " +
		"§14's project-scoped skills have no visibility concept",
	"DeleteSkill": "legacy catalogue GC; §15.3's closed vocabulary has no skill_delete because skills are " +
		"append-only at the tool surface (§14.2). I3 replaces this catalogue",
	"DeleteCustomImage": "legacy catalogue GC; §15.3's closed vocabulary has no image_delete because images are " +
		"append-only at the tool surface (§13). I1 replaces this catalogue",
	"MarkCustomImageReaped": "storage GC, not curation: the snapshot_ttl_days reaper (§5, B4) deleted the bytes " +
		"and stamps the catalogue row so resolution fails loudly instead of pointing at nothing (§13.7). " +
		"No agent decided it and §15.3's closed vocabulary has no verb for it. Like DeleteCustomImage it " +
		"writes a guarded table outside the seam, so the reaper must not run with the write guard installed",
}

// configGuardedTables is the set of projection tables under the write guard,
// derived from the registry so that registering a mutation guards its table.
func configGuardedTables() map[string]bool {
	out := map[string]bool{}
	for _, m := range ConfigMutations {
		for _, t := range m.Tables {
			out[t] = true
		}
	}
	return out
}

// ConfigGuardedTables returns the guarded projection tables, sorted.
func ConfigGuardedTables() []string {
	set := configGuardedTables()
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// InstallConfigEventGuard installs callbacks that reject any INSERT/UPDATE/
// DELETE against a registered projection table made outside a config-event
// transaction. It is the behavioural half of the conformance test: reflection
// catches an unregistered method, the guard catches a registered one that
// forgot the seam.
//
// It is opt-in (tests install it) rather than always-on: a hard runtime failure
// on every unadopted write would take the process down instead of the build.
func InstallConfigEventGuard(gdb *gorm.DB) error {
	if err := gdb.Callback().Create().Before("gorm:create").Register("agentdb:config_guard_create", configGuardCallback); err != nil {
		return err
	}
	if err := gdb.Callback().Update().Before("gorm:update").Register("agentdb:config_guard_update", configGuardCallback); err != nil {
		return err
	}
	if err := gdb.Callback().Delete().Before("gorm:delete").Register("agentdb:config_guard_delete", configGuardCallback); err != nil {
		return err
	}
	return nil
}

func configGuardCallback(db *gorm.DB) {
	table := db.Statement.Table
	if table == "" && db.Statement.Schema != nil {
		table = db.Statement.Schema.Table
	}
	if !configGuardedTables()[table] {
		return
	}
	if db.Statement.Context != nil && db.Statement.Context.Value(configTxKey{}) != nil {
		return
	}
	_ = db.AddError(fmt.Errorf(
		"agentdb: write to projection table %q outside a config-event transaction — "+
			"configuration mutations must go through Store.WithConfigEvent (§15.4; see the adoption recipe in config_events.go)",
		table))
}
