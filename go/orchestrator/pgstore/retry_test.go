package pgstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// TestIsUniqueViolationDetectsRealDriverError grounds the §10c I-3 heuristic
// against a REAL driver error: inserting a duplicate seq must be recognised.
func TestIsUniqueViolationDetectsRealDriverError(t *testing.T) {
	db := newTestDB(t)
	a := agentdb.BoardRevision{ID: "r1", Seq: 1, Status: "applied", Ops: agentdb.JSONArray("[]")}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	dup := agentdb.BoardRevision{ID: "r1-dup", Seq: 1, Status: "applied", Ops: agentdb.JSONArray("[]")}
	err := db.Create(&dup).Error
	if err == nil {
		t.Fatalf("expected a unique violation on duplicate seq")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("real driver unique violation not detected: %v", err)
	}
}

func TestIsUniqueViolationHeuristicStrings(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("UNIQUE constraint failed: board_revisions.seq"), true},                            // sqlite
		{errors.New(`duplicate key value violates unique constraint "idx_board_revisions_seq"`), true}, // postgres
		{fmt.Errorf("insert: %w", errors.New("ERROR: duplicate key value (SQLSTATE 23505)")), true},    // wrapped pgx
		{errors.New("connection refused"), false},
		{errors.New("syntax error at or near INSERT"), false},
	}
	for _, c := range cases {
		if got := isUniqueViolation(c.err); got != c.want {
			t.Fatalf("isUniqueViolation(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestWithSeqRetry unit-tests the bounded retry loop: retry on unique violation
// only, give up after the attempt budget, pass through other errors immediately.
func TestWithSeqRetry(t *testing.T) {
	unique := errors.New("UNIQUE constraint failed: runs.seq")

	// Two collisions then success → nil after 3 calls.
	calls := 0
	err := withSeqRetry(3, func() error {
		calls++
		if calls < 3 {
			return unique
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("retry-to-success: err=%v calls=%d", err, calls)
	}

	// Persistent collision → the violation surfaces after exactly `attempts` calls.
	calls = 0
	err = withSeqRetry(3, func() error { calls++; return unique })
	if err == nil || calls != 3 {
		t.Fatalf("bounded retry: err=%v calls=%d, want error after 3", err, calls)
	}

	// A non-unique error is NOT retried.
	calls = 0
	boom := errors.New("connection reset")
	err = withSeqRetry(3, func() error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("non-unique error retried: err=%v calls=%d", err, calls)
	}
}

// TestPgBoardAppendRetriesPastInjectedCollision simulates a concurrent writer:
// a once-only gorm create-hook claims the seq PgBoard.Append derived, so the
// first insert collides on the unique seq index; the retry re-reads MAX inside
// a fresh transaction and must succeed. (The thief lives in the SAME rolled-back
// transaction, so the retried append lands on the same seq — the pin is that
// Append survives the violation at all; without the retry loop it errors.)
func TestPgBoardAppendRetriesPastInjectedCollision(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	b := NewPgBoard(db)
	if _, err := b.Append(ctx, agentdb.Changeset{Author: "h", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "g", "x")}}); err != nil {
		t.Fatalf("append r1: %v", err)
	}

	injected := false
	if err := db.Callback().Create().Before("gorm:create").Register("test:steal_seq", func(tx *gorm.DB) {
		if injected {
			return // fire once (and don't recurse on the thief's own insert)
		}
		if _, ok := tx.Statement.Dest.(*agentdb.BoardRevision); !ok {
			return
		}
		injected = true
		thief := agentdb.BoardRevision{ID: "thief", Seq: 2, Status: "applied", Ops: agentdb.JSONArray("[]")}
		if err := tx.Session(&gorm.Session{NewDB: true}).Create(&thief).Error; err != nil {
			t.Errorf("inject thief: %v", err)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	id, err := b.Append(ctx, agentdb.Changeset{Author: "h", Message: "after-thief",
		Ops: []agentdb.Op{fragOp(agentdb.OpUpdate, "g", "y")}})
	if err != nil {
		t.Fatalf("append past collision: %v (retry loop missing?)", err)
	}
	if !injected {
		t.Fatalf("collision was never injected — test proves nothing")
	}
	if id != "r2" {
		t.Fatalf("retried append id = %q, want r2", id)
	}
	if head, _ := b.Head(ctx); head != id {
		t.Fatalf("head = %q, want %q", head, id)
	}
}
