package agentdb

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
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
	// MaxInstances and Enabled carry NO gorm `default` tag on purpose: GORM omits
	// zero-valued fields from the INSERT when a default is declared, which would
	// make `enabled: false` silently persist as true. The DDL defaults in
	// migration 021 still cover rows written outside GORM; through this store the
	// value is always explicit (see NewWorker / validateWorker).
	MaxInstances int   `json:"max_instances"` // max simultaneously active jobs
	Enabled      bool  `json:"enabled"`       // disabled workers ignore subscriptions
	CreatedAt    int64 `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt    int64 `json:"updated_at" gorm:"autoUpdateTime"`
}

func (Worker) TableName() string { return "workers" }

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

// validateWorker checks tenancy + identity and applies the max_instances
// default. It mutates w so the row written and the row echoed back agree.
func validateWorker(w *Worker) error {
	if w == nil {
		return fmt.Errorf("%w: worker is required", ErrWorkerInvalid)
	}
	if w.Project == "" {
		return fmt.Errorf("%w: project is required", ErrWorkerInvalid)
	}
	if w.Name == "" {
		return fmt.Errorf("%w: name is required", ErrWorkerInvalid)
	}
	if !workerNameRe.MatchString(w.Name) {
		return fmt.Errorf("%w: name %q is not kebab-case (lowercase letters, digits and single hyphens)", ErrWorkerInvalid, w.Name)
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

// UpsertWorker creates or replaces the worker row identified by
// (project, name). Replace, not patch: every column on w is written, so callers
// doing partial updates must read-modify-write (see GetWorker). Returns the
// stored row read back.
func (s *Store) UpsertWorker(ctx context.Context, w *Worker) (*Worker, error) {
	if err := validateWorker(w); err != nil {
		return nil, err
	}

	var existing Worker
	err := s.gdb.WithContext(ctx).Model(&Worker{}).
		Where("project = ? AND name = ?", w.Project, w.Name).
		First(&existing).Error
	if err == nil {
		existing.Description = w.Description
		existing.SystemPrompt = w.SystemPrompt
		existing.MCPConfig = w.MCPConfig
		existing.Image = w.Image
		existing.MaxInstances = w.MaxInstances
		existing.Briefing = w.Briefing
		existing.Enabled = w.Enabled
		if err := s.gdb.WithContext(ctx).Save(&existing).Error; err != nil {
			return nil, fmt.Errorf("failed to update worker: %w", err)
		}
		return &existing, nil
	}
	if !isNotFound(err) {
		return nil, fmt.Errorf("failed to look up worker: %w", err)
	}

	if err := s.gdb.WithContext(ctx).Create(w).Error; err != nil {
		return nil, fmt.Errorf("failed to create worker: %w", err)
	}
	return w, nil
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
func (s *Store) DeleteWorker(ctx context.Context, project, name string) error {
	if project == "" {
		return fmt.Errorf("%w: project is required", ErrWorkerInvalid)
	}
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrWorkerInvalid)
	}
	res := s.gdb.WithContext(ctx).
		Where("project = ? AND name = ?", project, name).
		Delete(&Worker{})
	if res.Error != nil {
		return fmt.Errorf("failed to delete worker: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: %s/%s", ErrWorkerNotFound, project, name)
	}
	return nil
}
