package agentdb

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Worker store errors. They are sentinels so callers (notably the httpapi CRUD
// handlers) can map them onto status codes without string-matching or importing
// gorm.
var (
	// ErrWorkerNotFound is returned when no worker matches (project, name).
	ErrWorkerNotFound = errors.New("worker not found")
	// ErrWorkerInvalid wraps every validation failure on a worker row.
	ErrWorkerInvalid = errors.New("invalid worker")
)

// SelectorList is a list of label-selector strings stored as JSONB. Unlike
// JSONArray it is NULL-preserving: a nil list round-trips as SQL NULL rather
// than as the empty array, because "no briefing selectors configured" and "an
// explicitly empty briefing list" are different states (spec 02-workers §6.1:
// `briefing` jsonb, default null).
type SelectorList []string

func (l SelectorList) Value() (driver.Value, error) {
	if l == nil {
		return nil, nil
	}
	b, err := json.Marshal([]string(l))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (l *SelectorList) Scan(value any) error {
	if value == nil {
		*l = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("unsupported type %T for SelectorList", value)
	}
	if len(bytes) == 0 || string(bytes) == "null" {
		*l = nil
		return nil
	}
	var decoded []string
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		return fmt.Errorf("failed to unmarshal SelectorList: %w", err)
	}
	*l = decoded
	return nil
}

// Worker is a configured agent persona in a project (spec 02-workers §6.1).
// Identity is (Project, Name) — the composite primary key; Project is the hard
// tenancy namespace and matches the `customer` claim on the caller's token.
//
// Deliberately absent: model tier, budget, memory namespace, skills. Image,
// MaxInstances and Briefing are plumbing (an environment pointer, a parallelism
// cap, and briefing-section selectors); everything a worker *believes* lives in
// SystemPrompt.
type Worker struct {
	Project      string       `json:"project" gorm:"primaryKey;type:varchar(255)"`
	Name         string       `json:"name" gorm:"primaryKey;type:varchar(255)"`
	Description  string       `json:"description" gorm:"type:text;default:''"`
	SystemPrompt string       `json:"system_prompt" gorm:"type:text;default:''"`
	MCPConfig    JSONMap      `json:"mcp_config" gorm:"column:mcp_config;type:jsonb;default:'{}'"`
	Image        string       `json:"image" gorm:"type:text;default:''"`    // '' | name (latest) | name:version (pinned)
	Briefing     SelectorList `json:"briefing,omitempty" gorm:"type:jsonb"` // label selectors; nil = NULL
	// MaxInstances, Enabled and Frozen carry NO gorm `default` tag on purpose:
	// GORM omits zero-valued fields from the INSERT when a default is declared,
	// which would make `enabled: false` (or `frozen: false`) silently persist as
	// true. The DDL defaults in migrations 021/034 still cover rows written
	// outside GORM; through this store the value is always explicit (see
	// NewWorker / validateWorker).
	MaxInstances int  `json:"max_instances"` // max simultaneously active jobs
	Enabled      bool `json:"enabled"`       // disabled workers ignore subscriptions
	// Frozen is the causal-isolation primitive for measurement instruments
	// (docs/product/10-topology-library.md §3): a frozen worker's configuration
	// cannot be changed by other workers — the core MCP server refuses
	// worker_update / worker_prompt_write against it — only by humans, through
	// the JWT-guarded HTTP API. The store itself does NOT enforce the boundary:
	// "workers may not, people may" is one check at the MCP seam, and putting a
	// second copy here would also block the human path that shares these methods.
	Frozen    bool  `json:"frozen"`
	CreatedAt int64 `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt int64 `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Worker) TableName() string { return "workers" }

// EventTypeWorkerFreezeRefused is the project event the core MCP server emits
// when a worker's tool call is refused because its target is frozen. Named here,
// beside the flag it shadows, exactly as human.attention.timeout is named beside
// the attention tables. Refusals are research signals (playbook C8): an agent
// trying to edit the thing that scores it is the reward-hacking hypothesis of
// AGENTS_RESEARCH.md §2 in its most literal form, so they are counted, never
// swallowed.
const EventTypeWorkerFreezeRefused = "worker.freeze_refused"

// DefaultMaxInstances is the max_instances a worker gets when the caller does
// not choose one (spec 02-workers §6.1).
const DefaultMaxInstances = 1

// NewWorker returns a Worker with the spec's defaults applied — max_instances 1
// and enabled true. Use it rather than a bare &Worker{}: `Enabled` is a plain
// bool, so a zero-valued struct would persist a worker that is switched off.
func NewWorker(project, name string) *Worker {
	return &Worker{
		Project:      project,
		Name:         name,
		MaxInstances: DefaultMaxInstances,
		Enabled:      true,
	}
}

// workerNameRe enforces the kebab-case identity of spec 02-workers §6.1
// (e.g. email-answerer, email-review-consultant).
var workerNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateWorkerName is the identity rule, exported so a caller can refuse a bad
// name BEFORE it starts writing (§9: validate first, never half-write). It is
// the same rule validateWorker applies — one regexp, not two.
func ValidateWorkerName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrWorkerInvalid)
	}
	if !workerNameRe.MatchString(name) {
		return fmt.Errorf("%w: name %q is not kebab-case (lowercase letters, digits and single hyphens)", ErrWorkerInvalid, name)
	}
	return nil
}

// validateWorker checks tenancy + identity and applies the max_instances
// default. It mutates w so the row written and the row echoed back agree.
func validateWorker(w *Worker) error {
	if w == nil {
		return fmt.Errorf("%w: worker is required", ErrWorkerInvalid)
	}
	if w.Project == "" {
		return fmt.Errorf("%w: project is required", ErrWorkerInvalid)
	}
	if err := ValidateWorkerName(w.Name); err != nil {
		return err
	}
	if w.MaxInstances == 0 {
		w.MaxInstances = DefaultMaxInstances
	}
	if w.MaxInstances < 0 {
		return fmt.Errorf("%w: max_instances must be >= 1, got %d", ErrWorkerInvalid, w.MaxInstances)
	}
	for i, sel := range w.Briefing {
		if strings.TrimSpace(sel) == "" {
			return fmt.Errorf("%w: briefing selector %d is empty", ErrWorkerInvalid, i)
		}
	}
	return nil
}

// workerConfigEqual compares everything a worker carries EXCEPT the two
// toggles (`enabled`, `frozen`) — and except the identity and timestamp
// columns, which an upsert never changes. It is what distinguishes "the
// operator flipped a switch" from "the operator rewrote the worker and
// happened to flip a switch too".
func workerConfigEqual(a, b *Worker) bool {
	if a.Description != b.Description || a.SystemPrompt != b.SystemPrompt ||
		a.Image != b.Image || a.MaxInstances != b.MaxInstances {
		return false
	}
	return jsonValueEqual(a.MCPConfig, b.MCPConfig) && jsonValueEqual(a.Briefing, b.Briefing)
}

// jsonValueEqual compares two JSON-serialisable column values structurally.
// Marshalling rather than reflect.DeepEqual keeps nil and empty distinguishable
// (a nil Briefing is SQL NULL, an empty one is `[]`) without a type switch.
func jsonValueEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

// workerUpsertAction picks the most specific §15.3 action the write represents:
// a new row is a create; a write that only flips `enabled` is an enable or a
// disable; a write that only flips `frozen` is a freeze or an unfreeze;
// anything else — including flipping both toggles at once — is an update.
//
// Deliberately never worker_prompt_write: that action requires a rationale
// (§15.5) and belongs to the dedicated prompt-write path (H1). A whole-object
// PUT that carries a new system_prompt is an update.
func workerUpsertAction(existing, next *Worker) string {
	if existing == nil {
		return ActionWorkerCreate
	}
	if workerConfigEqual(existing, next) {
		enabledFlip := existing.Enabled != next.Enabled
		frozenFlip := existing.Frozen != next.Frozen
		switch {
		case enabledFlip && !frozenFlip:
			if next.Enabled {
				return ActionWorkerEnable
			}
			return ActionWorkerDisable
		case frozenFlip && !enabledFlip:
			if next.Frozen {
				return ActionWorkerFreeze
			}
			return ActionWorkerUnfreeze
		}
	}
	return ActionWorkerUpdate
}

// UpsertWorker creates or replaces the worker row identified by
// (project, name). Replace, not patch: every column on w is written, so callers
// doing partial updates must read-modify-write (see GetWorker). Returns the
// stored row read back.
//
// The write appends one config event in the same transaction (§15.4), carrying
// the whole worker row as its payload and the most specific action of
// worker_create / worker_update / worker_enable / worker_disable. cw is the
// who/why; a human/API edit passes the zero value.
func (s *Store) UpsertWorker(ctx context.Context, w *Worker, cw ConfigWrite) (*Worker, error) {
	if err := validateWorker(w); err != nil {
		return nil, err
	}

	var existing Worker
	err := s.gdb.WithContext(ctx).Model(&Worker{}).
		Where("project = ? AND name = ?", w.Project, w.Name).
		First(&existing).Error
	if err == nil {
		action := workerUpsertAction(&existing, w)
		existing.Description = w.Description
		existing.SystemPrompt = w.SystemPrompt
		existing.MCPConfig = w.MCPConfig
		existing.Image = w.Image
		existing.MaxInstances = w.MaxInstances
		existing.Briefing = w.Briefing
		existing.Enabled = w.Enabled
		existing.Frozen = w.Frozen
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: existing.Project,
			Action:  action,
			Payload: &existing,
			Write:   cw,
		}, func(tx *gorm.DB) error {
			return tx.Save(&existing).Error
		}); err != nil {
			return nil, fmt.Errorf("failed to update worker: %w", err)
		}
		return &existing, nil
	}
	if !isNotFound(err) {
		return nil, fmt.Errorf("failed to look up worker: %w", err)
	}

	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: w.Project,
		Action:  ActionWorkerCreate,
		Payload: w,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		return tx.Create(w).Error
	}); err != nil {
		return nil, fmt.Errorf("failed to create worker: %w", err)
	}
	return w, nil
}

// SetWorkerPrompt replaces a worker's system prompt wholesale (P4) and appends
// a `worker_prompt_write` config event carrying the whole worker row (§15.3).
// It is the ONLY path that writes that action.
//
// Why a narrow write rather than a read-modify-write through UpsertWorker:
//
//   - UpsertWorker deliberately never writes `worker_prompt_write` — that action
//     requires a rationale (§15.5), and a PUT that happens to carry a different
//     prompt is an ordinary update.
//   - A whole-object save would let a prompt rewrite clobber a description or an
//     image pointer edited a moment earlier. The one mutation that must have no
//     side effects is the one a worker performs on ANOTHER worker (§8.7).
//
// The superseded prompt is returned alongside the stored row because §9 requires
// the caller to put it in the automatic `kind=prompt-revision` memory. Reading it
// here — inside the same call that replaced it — is the only way to be certain
// the text recorded as "previous" is the text this write actually superseded.
//
// rationale is REQUIRED: the seam refuses this action without one (§15.5), so a
// caller that forgets writes neither row.
func (s *Store) SetWorkerPrompt(ctx context.Context, project, name, prompt string, cw ConfigWrite) (*Worker, string, error) {
	if project == "" {
		return nil, "", fmt.Errorf("%w: project is required", ErrWorkerInvalid)
	}
	if name == "" {
		return nil, "", fmt.Errorf("%w: name is required", ErrWorkerInvalid)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, "", fmt.Errorf("%w: system_prompt must not be blank (§9 validates a non-empty prompt)", ErrWorkerInvalid)
	}
	existing, err := s.GetWorker(ctx, project, name)
	if err != nil {
		return nil, "", err
	}
	previous := existing.SystemPrompt

	// The payload is the whole row as it will stand (§15.2/§15.3) so the fold and
	// the changelog see the same worker the projection table holds — including
	// updated_at, which the narrow UPDATE below sets explicitly rather than
	// leaving to gorm's autoUpdateTime.
	now := time.Now().Unix()
	next := *existing
	next.SystemPrompt = prompt
	next.UpdatedAt = now

	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: project,
		Action:  ActionWorkerPromptWrite,
		Payload: &next,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		res := tx.Model(&Worker{}).
			Where("project = ? AND name = ?", project, name).
			Updates(map[string]any{"system_prompt": prompt, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Lost a race with a concurrent delete: roll back rather than log a
			// rewrite of a worker that no longer exists.
			return fmt.Errorf("%w: %s/%s", ErrWorkerNotFound, project, name)
		}
		return nil
	}); err != nil {
		if errors.Is(err, ErrWorkerNotFound) {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("failed to write worker prompt: %w", err)
	}

	// §9 read-back: what the database holds is what the caller is shown, never
	// the struct that was handed to it.
	stored, err := s.GetWorker(ctx, project, name)
	if err != nil {
		return nil, previous, err
	}
	return stored, previous, nil
}

// GetWorker fetches one worker by (project, name). The project predicate is
// mandatory and is the only tenancy boundary — there is no code path that
// returns a worker without it.
func (s *Store) GetWorker(ctx context.Context, project, name string) (*Worker, error) {
	if project == "" {
		return nil, fmt.Errorf("%w: project is required", ErrWorkerInvalid)
	}
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrWorkerInvalid)
	}
	var w Worker
	if err := s.gdb.WithContext(ctx).
		Where("project = ? AND name = ?", project, name).
		First(&w).Error; err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s", ErrWorkerNotFound, project, name)
		}
		return nil, fmt.Errorf("failed to get worker: %w", err)
	}
	return &w, nil
}

// ListWorkers returns every worker in a project, name-ordered.
func (s *Store) ListWorkers(ctx context.Context, project string) ([]*Worker, error) {
	if project == "" {
		return nil, fmt.Errorf("%w: project is required", ErrWorkerInvalid)
	}
	var workers []*Worker
	if err := s.gdb.WithContext(ctx).Model(&Worker{}).
		Where("project = ?", project).
		Order("name ASC").
		Find(&workers).Error; err != nil {
		return nil, fmt.Errorf("failed to list workers: %w", err)
	}
	if workers == nil {
		workers = []*Worker{}
	}
	return workers, nil
}

// DeleteWorker removes a worker row. Sessions the worker already ran keep their
// `worker` value and their `composed_prompt`: history is not rewritten.
//
// The delete appends too (§15.3 rule 2): the event carries the worker as it
// last stood, so restoring a retired worker is a lookup rather than an
// archaeology project. The row is read before the transaction precisely so
// there is a final state to carry.
func (s *Store) DeleteWorker(ctx context.Context, project, name string, cw ConfigWrite) error {
	if project == "" {
		return fmt.Errorf("%w: project is required", ErrWorkerInvalid)
	}
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrWorkerInvalid)
	}
	existing, err := s.GetWorker(ctx, project, name)
	if err != nil {
		return err
	}
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: project,
		Action:  ActionWorkerDelete,
		Payload: existing,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		res := tx.Where("project = ? AND name = ?", project, name).Delete(&Worker{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Lost a race with a concurrent delete: roll the whole thing back
			// rather than log a deletion this call did not perform.
			return fmt.Errorf("%w: %s/%s", ErrWorkerNotFound, project, name)
		}
		return nil
	}); err != nil {
		if errors.Is(err, ErrWorkerNotFound) {
			return err
		}
		return fmt.Errorf("failed to delete worker: %w", err)
	}
	return nil
}
