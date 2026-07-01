package pgstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// PgTelemetry is a Postgres-backed orchestrator.Telemetry (append-only run log).
// The §5 interface is ctx+error (contracts §10b E-1), so writes fail loud: a DB
// error surfaces rather than being logged and dropped.
type PgTelemetry struct {
	db *gorm.DB
	mu sync.Mutex // serialize Record for monotonic seq (single-writer v1)
}

// NewPgTelemetry returns a Telemetry over db (runs table migrated).
func NewPgTelemetry(db *gorm.DB) *PgTelemetry { return &PgTelemetry{db: db} }

// Record appends a run, assigning its 1-based Seq and "run{seq}" id, and returns it.
func (t *PgTelemetry) Record(ctx context.Context, r orchestrator.Run) (orchestrator.Run, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	err := t.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&agentdb.TelemetryRun{}).
			Select("COALESCE(MAX(seq),0)").Scan(&maxSeq).Error; err != nil {
			return err
		}
		seq := maxSeq + 1
		r.Seq = int(seq)
		r.ID = fmt.Sprintf("run%d", seq)
		row := agentdb.TelemetryRun{
			ID: r.ID, Seq: seq, Scope: r.Scope, BoardRevision: r.BoardRevision,
			Prompt: r.Prompt, Output: r.Output, CreatedAt: time.Now().Unix(),
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return orchestrator.Run{}, fmt.Errorf("pgtelemetry: record: %w", err)
	}
	return r, nil
}

// Runs returns all recorded runs in seq order.
func (t *PgTelemetry) Runs(ctx context.Context) ([]orchestrator.Run, error) {
	var rows []agentdb.TelemetryRun
	if err := t.db.WithContext(ctx).Order("seq asc").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("pgtelemetry: runs: %w", err)
	}
	out := make([]orchestrator.Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, orchestrator.Run{
			ID: r.ID, Seq: int(r.Seq), Scope: r.Scope, BoardRevision: r.BoardRevision,
			Prompt: r.Prompt, Output: r.Output,
		})
	}
	return out, nil
}

var _ orchestrator.Telemetry = (*PgTelemetry)(nil)
