package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// PgTicketStore is a Postgres-backed orchestrator.TicketStore. Ungated work
// state; not versioned.
type PgTicketStore struct{ db *gorm.DB }

// NewPgTicketStore returns a TicketStore over db (tickets table migrated).
func NewPgTicketStore(db *gorm.DB) *PgTicketStore { return &PgTicketStore{db: db} }

func toRow(t orchestrator.Ticket) agentdb.Ticket {
	dep, _ := json.Marshal(t.DependsOn)
	row := agentdb.Ticket{
		ID: t.ID, ProjectID: t.ProjectID, Title: t.Title, Objective: t.Objective,
		Acceptance: t.Acceptance, Status: string(t.Status),
		Scope: rawToJSON(t.Scope, "{}"), Result: rawToJSON(t.Result, "{}"),
		PendingPost: rawToJSON(t.PendingPost, "{}"), PublishedRef: t.PublishedRef,
		DependsOn: agentdb.JSONArray(dep),
		Parent:    t.Parent, Attempts: t.Attempts, BoardRev: t.BoardRev,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	return row
}

func fromRow(r agentdb.Ticket) orchestrator.Ticket {
	var dep []string
	_ = json.Unmarshal(jsonArrayBytes(r.DependsOn), &dep)
	return orchestrator.Ticket{
		ID: r.ID, ProjectID: r.ProjectID, Title: r.Title, Objective: r.Objective,
		Acceptance: r.Acceptance, Status: orchestrator.TicketStatus(r.Status),
		Scope: json.RawMessage(jsonArrayBytes(r.Scope)), Result: json.RawMessage(jsonArrayBytes(r.Result)),
		PendingPost: json.RawMessage(jsonArrayBytes(r.PendingPost)), PublishedRef: r.PublishedRef,
		DependsOn: dep,
		Parent:    r.Parent, Attempts: r.Attempts, BoardRev: r.BoardRev,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func rawToJSON(raw json.RawMessage, empty string) agentdb.JSONArray {
	if len(raw) == 0 {
		return agentdb.JSONArray(empty)
	}
	return agentdb.JSONArray(raw)
}

// Create inserts a ticket, allocating an id and timestamps if unset.
func (s *PgTicketStore) Create(ctx context.Context, t orchestrator.Ticket) (string, error) {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	now := time.Now().Unix()
	if t.CreatedAt == 0 {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	if t.Status == "" {
		t.Status = orchestrator.StatusBacklog
	}
	row := toRow(t)
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return "", fmt.Errorf("pgticket: create %q: %w", t.ID, err)
	}
	return t.ID, nil
}

// Update overwrites a ticket's mutable fields.
func (s *PgTicketStore) Update(ctx context.Context, t orchestrator.Ticket) error {
	if t.ID == "" {
		return fmt.Errorf("pgticket: update requires an id")
	}
	t.UpdatedAt = time.Now().Unix()
	row := toRow(t)
	if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
		return fmt.Errorf("pgticket: update %q: %w", t.ID, err)
	}
	return nil
}

// Get returns a ticket by id.
func (s *PgTicketStore) Get(ctx context.Context, id string) (orchestrator.Ticket, error) {
	var row agentdb.Ticket
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return orchestrator.Ticket{}, fmt.Errorf("pgticket: get %q: %w", id, err)
	}
	return fromRow(row), nil
}

// List returns tickets filtered by status ("" = all), ordered by creation.
func (s *PgTicketStore) List(ctx context.Context, status orchestrator.TicketStatus) ([]orchestrator.Ticket, error) {
	q := s.db.WithContext(ctx).Model(&agentdb.Ticket{}).Order("created_at asc, id asc")
	if status != "" {
		q = q.Where("status = ?", string(status))
	}
	var rows []agentdb.Ticket
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("pgticket: list %q: %w", status, err)
	}
	out := make([]orchestrator.Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}

var _ orchestrator.TicketStore = (*PgTicketStore)(nil)
