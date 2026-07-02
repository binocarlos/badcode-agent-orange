// Package pgstore holds the Postgres (gorm) implementations of the Slice-A data
// seams: agentdb.BoardStore (PgBoard), orchestrator.TicketStore (PgTicketStore),
// and orchestrator.Telemetry (PgTelemetry). It imports agentdb (row models +
// Board types) and orchestrator (domain types + interfaces); agentdb never
// imports orchestrator, so there is no cycle. Fast tests run against sqlite via
// AutoMigrate (the agentdb/board_test.go pattern); production wires *gorm.DB from
// agentdb.Store.DB().
package pgstore

import "strings"

// seqInsertAttempts bounds the MAX(seq)+1 retry loop (§10c I-3).
const seqInsertAttempts = 3

// isUniqueViolation reports whether err looks like a unique-index/constraint
// violation, portably across the two drivers the stores run on. Heuristic
// (documented per §10c I-3) — a case-insensitive substring match on the error
// text, deliberately avoiding driver-specific error types in the store:
//
//	sqlite (glebarez):  "UNIQUE constraint failed: board_revisions.seq"     → "unique"
//	postgres (pgx):     `duplicate key value violates unique constraint
//	                     "idx_board_revisions_seq" (SQLSTATE 23505)`        → "unique" / "duplicate key" / "23505"
//
// A false positive only costs a harmless bounded retry of an idempotent
// read-MAX-and-insert transaction; a false negative surfaces the raw error.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "23505")
}

// withSeqRetry runs op up to attempts times, retrying ONLY on a unique
// violation (a concurrent writer claimed our MAX(seq)+1; op re-reads MAX inside
// its own transaction each attempt — §10c I-3). Any other error, or exhausting
// the attempt budget, surfaces immediately.
func withSeqRetry(attempts int, op func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		err = op()
		if err == nil || !isUniqueViolation(err) {
			return err
		}
	}
	return err
}
