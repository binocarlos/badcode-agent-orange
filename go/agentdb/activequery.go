package agentdb

// activequery.go — the in-flight turn's two ids, written down (D5; doc 22
// RD6/RD24).
//
// # Why this is persisted at all
//
// A turn runs in a container agentd does not supervise. Kill agentd mid-turn
// and the sandbox carries on: it finishes the answer and buffers it in RAM
// because no stream is attached. The restarted agentd can re-adopt the
// container, but it has forgotten two things it needs to go and collect that
// answer:
//
//	active_query_id         the runner's `q-<session>-<n>` — the key
//	                        agent_query_events rows are written under, and the
//	                        id a browser is handed by GET /status and sends back
//	                        as ?queryId= on /reconnect.
//	active_sandbox_query_id the uuid the in-image agent minted for the SAME
//	                        turn. Its replay buffer is keyed `sessionId:uuid`
//	                        (sandbox/src/services/stream-service.ts), so this is
//	                        the only id that can attach to it.
//
// Two ids, one turn: the runner's is canonical for STORAGE, the sandbox's is
// canonical for TRANSPORT, and this row is the only place the two are ever
// joined. Without it a reconnect either attaches to nothing (wrong key at the
// sandbox) or writes a second row (wrong key in the database) — and a turn
// split across two rows is worse than one lost, because it reads as two.
//
// These are runtime state on `agent_sessions`, exactly like lease_expires_at:
// they write no config event (§15.3 rule 3) and the table is not under the
// config-write guard.

import (
	"context"
	"fmt"
)

// SetActiveQuery records the in-flight turn's ids. sandboxQueryID may be empty:
// the runner knows its own id before it dispatches, and only learns the
// sandbox's from the `connected` frame a moment later, so this is called twice
// per turn.
//
// Written with UpdateColumns so it never disturbs `updated_at`: the idle-archive
// loop reads exactly that column to decide what is idle, and a turn stamping it
// would be indistinguishable from a human typing.
func (s *Store) SetActiveQuery(ctx context.Context, sessionID, queryID, sandboxQueryID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	if queryID == "" {
		return fmt.Errorf("query id is required")
	}
	res := s.gdb.WithContext(ctx).Model(&Session{}).
		Where("id = ?", sessionID).
		UpdateColumns(map[string]any{
			"active_query_id":         queryID,
			"active_sandbox_query_id": sandboxQueryID,
		})
	if res.Error != nil {
		return fmt.Errorf("failed to set active query: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("session not found")
	}
	return nil
}

// GetActiveQuery returns the in-flight turn's (runner id, sandbox id). Both
// empty means no turn is recorded as running. A missing session row is not an
// error — the answer to "what is running" is "nothing".
func (s *Store) GetActiveQuery(ctx context.Context, sessionID string) (string, string, error) {
	if sessionID == "" {
		return "", "", fmt.Errorf("session id is required")
	}
	var row Session
	err := s.gdb.WithContext(ctx).Model(&Session{}).
		Select("active_query_id", "active_sandbox_query_id").
		Where("id = ?", sessionID).
		First(&row).Error
	if err != nil {
		if isNotFound(err) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("failed to get active query: %w", err)
	}
	return row.ActiveQueryID, row.ActiveSandboxQueryID, nil
}

// ClearActiveQuery drops the record of the in-flight turn, but ONLY if the turn
// being cleared is still the one recorded.
//
// The condition is load-bearing rather than tidy. A turn that a browser reload
// interrupted keeps running inside the sandbox, and the next turn on the same
// session starts while the old goroutine is still unwinding; an unconditional
// clear from the loser would erase the live turn's ids and make it
// unreconnectable. Clearing someone else's turn is not an error — the state the
// caller wanted (this turn is not the active one) is already true.
func (s *Store) ClearActiveQuery(ctx context.Context, sessionID, queryID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	if queryID == "" {
		return fmt.Errorf("query id is required")
	}
	res := s.gdb.WithContext(ctx).Model(&Session{}).
		Where("id = ? AND active_query_id = ?", sessionID, queryID).
		UpdateColumns(map[string]any{
			"active_query_id":         "",
			"active_sandbox_query_id": "",
		})
	if res.Error != nil {
		return fmt.Errorf("failed to clear active query: %w", res.Error)
	}
	return nil
}
