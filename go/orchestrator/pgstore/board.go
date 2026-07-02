package pgstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// PgBoard is a Postgres-backed agentdb.BoardStore: an append-only changeset log
// (board_revisions) folded on read, with a single-row head pointer (board_head).
// Revision ids are a deterministic "r{seq}" counter for parity with MemBoard.
type PgBoard struct {
	db *gorm.DB
	mu sync.Mutex // serialize Append within this process (monotonic seq, fewer retries)
}

// NewPgBoard returns a BoardStore over db. db must have the board tables migrated
// (agentdb migration 020/021 in prod; AutoMigrate in tests).
func NewPgBoard(db *gorm.DB) *PgBoard { return &PgBoard{db: db} }

// Append records a changeset as the next revision and moves head to it. §10c
// I-4: a changeset carrying any non-prompt_fragment op is rejected fail-loud.
// §10c I-3: seq is MAX(seq)+1 read inside the transaction; a unique-violation
// (concurrent writer) re-reads MAX and retries, bounded — cross-process safe.
func (b *PgBoard) Append(ctx context.Context, cs agentdb.Changeset) (string, error) {
	if err := agentdb.RequireFragmentOps(cs.Ops); err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var id string
	err := withSeqRetry(seqInsertAttempts, func() error {
		return b.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var maxSeq int64
			if err := tx.Model(&agentdb.BoardRevision{}).
				Select("COALESCE(MAX(seq),0)").Scan(&maxSeq).Error; err != nil {
				return fmt.Errorf("pgboard: max seq: %w", err)
			}
			seq := maxSeq + 1
			id = fmt.Sprintf("r%d", seq)
			rev := agentdb.BoardRevision{
				ID: id, ParentID: cs.ParentID, Seq: seq, Status: "applied",
				Author: cs.Author, Message: cs.Message, Ops: agentdb.OpsToJSON(cs.Ops),
				CreatedAt: time.Now().Unix(),
			}
			if err := tx.Create(&rev).Error; err != nil {
				return fmt.Errorf("pgboard: insert revision: %w", err)
			}
			head := agentdb.BoardHead{Singleton: true, RevisionID: id}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "singleton"}},
				DoUpdates: clause.AssignmentColumns([]string{"revision_id"}),
			}).Create(&head).Error; err != nil {
				return fmt.Errorf("pgboard: upsert head: %w", err)
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Current folds the whole log (through head) into the live board state.
func (b *PgBoard) Current(ctx context.Context) (agentdb.Board, error) {
	head, err := b.Head(ctx)
	if err != nil {
		return agentdb.Board{}, err
	}
	return b.AsOf(ctx, head)
}

// AsOf folds the log in seq order up to and including revisionID, via the one
// shared fold (agentdb.FoldFragments — §10c I-4).
func (b *PgBoard) AsOf(ctx context.Context, revisionID string) (agentdb.Board, error) {
	var revs []agentdb.BoardRevision
	if err := b.db.WithContext(ctx).Order("seq asc").Find(&revs).Error; err != nil {
		return agentdb.Board{}, fmt.Errorf("pgboard: load revisions: %w", err)
	}
	return agentdb.FoldFragments(revs, revisionID)
}

// Head returns the currently-live applied revision id.
func (b *PgBoard) Head(ctx context.Context) (string, error) {
	var head agentdb.BoardHead
	err := b.db.WithContext(ctx).First(&head).Error
	if err != nil {
		return "", fmt.Errorf("pgboard: board empty: %w", err)
	}
	return head.RevisionID, nil
}

// Revisions returns the append-only log in ascending seq order (the story timeline).
func (b *PgBoard) Revisions(ctx context.Context) ([]agentdb.BoardRevision, error) {
	var revs []agentdb.BoardRevision
	if err := b.db.WithContext(ctx).Order("seq asc").Find(&revs).Error; err != nil {
		return nil, fmt.Errorf("pgboard: revisions: %w", err)
	}
	return revs, nil
}

// compile-time assertion that PgBoard satisfies the seam.
var _ agentdb.BoardStore = (*PgBoard)(nil)
