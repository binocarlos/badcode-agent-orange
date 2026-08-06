package agentdb

// leases.go — session leases and the daily token counter (spec §8.4 step 4 and
// §8.4 step 6 / §5).
//
// # Why a lease exists
//
// A job runs inside a container agentd does not supervise. If agentd is killed
// mid-turn, or the container dies without the sandbox ever reporting back,
// nothing else in the system learns the job ended: the delivery stays
// `running`, the worker's `max_instances` slot stays taken, and §8.4's capacity
// gates deadlock quietly. The lease is the deadline that makes that
// recoverable — whoever runs a job holds it and renews it while the sandbox
// streams, and the router's reaper turns a lapsed lease into a `worker.failed`
// with `reason:"lost"`.
//
// # Why the reaper keys on the LEASE and not on the session status
//
// A turn a human interrupted (browser reload, stop button) persists its
// transcript, deliberately emits no outcome event, and is perfectly resumable —
// the session row sits at `status:"running"` legitimately, which is exactly what
// `ensureRunning` needs. Reaping on status would report those as lost jobs and
// wake every subscriber with a lie. So the lease is taken only by the job runner
// and released whenever the turn settles at all (completed, errored, or
// cancelled). A held-and-lapsed lease therefore means precisely one thing:
// nobody is running this any more and nobody ever reported why.
//
// These are runtime state on `agent_sessions`, not configuration: they write no
// config event (§15.3 rule 3) and the table is not under the config-write guard.

import (
	"context"
	"errors"
	"fmt"
)

// SessionLeaseUnset is the "no lease held" value of `lease_expires_at`.
const SessionLeaseUnset int64 = 0

// RenewSessionLease extends a session's lease to `until` (unix seconds).
//
// Written with UpdateColumn so a renewal never disturbs `updated_at`: bumping it
// every minute would keep every running session looking freshly touched to the
// idle-archive loop, which reads exactly that column to decide what is idle.
func (s *Store) RenewSessionLease(ctx context.Context, sessionID string, until int64) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	if until < 0 {
		return fmt.Errorf("lease deadline must not be negative")
	}
	res := s.gdb.WithContext(ctx).Model(&Session{}).
		Where("id = ?", sessionID).
		UpdateColumn("lease_expires_at", until)
	if res.Error != nil {
		return fmt.Errorf("failed to renew session lease: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("session not found")
	}
	return nil
}

// ErrSessionLeaseNotHeld is returned by ReleaseSessionLease when there was no
// lease to drop — the session holds none, or the row is gone. The state the
// caller wanted is already true, so it is not a failure; it is the answer to a
// different question: "was it MINE to release?"
var ErrSessionLeaseNotHeld = errors.New("session holds no lease")

// ReleaseSessionLease drops a session's lease, if this caller is the one that
// still finds it held. Called whenever a turn settles — including a cancelled
// one, which is what keeps a resumable interrupted turn out of the reaper's
// hands.
//
// The release is CONDITIONAL on the lease still being held, and reports
// ErrSessionLeaseNotHeld when it was not. That is load-bearing, not tidiness:
// the reaper (`router.go`, `leaseReaper.reapOne`) uses a successful release as
// its claim on a dead session and only then emits `worker.failed{reason:lost}`,
// justifying that as at-most-once. An unconditional UPDATE that also swallowed
// a missing row made that justification false — two reapers, or a reaper and a
// settling turn, could both "succeed" and both emit, waking every subscriber
// twice about one dead job. With the condition, exactly one caller can win.
func (s *Store) ReleaseSessionLease(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	res := s.gdb.WithContext(ctx).Model(&Session{}).
		Where("id = ? AND lease_expires_at > ?", sessionID, SessionLeaseUnset).
		UpdateColumn("lease_expires_at", SessionLeaseUnset)
	if res.Error != nil {
		return fmt.Errorf("failed to release session lease: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrSessionLeaseNotHeld
	}
	return nil
}

// ListExpiredLeaseSessions returns sessions that HOLD a lease whose deadline has
// passed — the reaper's input (§8.4 step 4). Sessions with no lease are never
// returned, whatever their status.
//
// Oldest deadline first, so a backlog after a long agentd outage is worked in
// the order the jobs actually died.
func (s *Store) ListExpiredLeaseSessions(ctx context.Context, now int64, limit int) ([]*Session, error) {
	out := []*Session{}
	if err := s.gdb.WithContext(ctx).Model(&Session{}).
		Where("lease_expires_at > ? AND lease_expires_at < ?", SessionLeaseUnset, now).
		Order("lease_expires_at ASC, id ASC").
		Limit(clampLimit(limit)).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list expired-lease sessions: %w", err)
	}
	return out, nil
}

// CountProjectTokensSince sums the tokens a project has spent since `since`
// (unix seconds) — what §5's `daily_tokens_soft`/`daily_tokens_hard` are
// measured against, and what §8.4 step 6 checks before creating a
// non-interactive job.
//
// It reuses the same per-envelope accounting GetSessionTokenSummary reads,
// joined to sessions for the project. The jsonb shape both read is captured and
// explained in token_usage.go — do not change the path here without re-reading
// a real stored envelope. Postgres-only SQL (jsonb operators plus LATERAL),
// like the summary it mirrors — on a store without those the budget simply
// cannot be evaluated, and the router treats that as "do not stop the world"
// (see the router's budget gate).
func (s *Store) CountProjectTokensSince(ctx context.Context, project string, since int64) (int64, error) {
	if project == "" {
		return 0, fmt.Errorf("project is required")
	}
	var row struct{ Total int64 }
	err := s.gdb.WithContext(ctx).Raw(`
		SELECT COALESCE(SUM(`+usageInputSQL+` + `+usageOutputSQL+`), 0) AS total
		FROM agent_query_events AS qe
		JOIN agent_sessions AS sess ON sess.id = qe.session_id
		`+usageEnvelopes("qe")+`
		WHERE sess.customer = ? AND qe.created_at >= ?`, project, since).
		Scan(&row).Error
	if err != nil {
		return 0, fmt.Errorf("failed to count project tokens: %w", err)
	}
	return row.Total, nil
}
