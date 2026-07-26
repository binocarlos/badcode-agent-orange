package agentdb

// attention.go — the durable half of `request_human_attention`
// (spec §9, docs/product/05-management-tools.md; §8.2 for the timeout event).
//
// The tool itself is "deliberately almost nothing": post `{message,
// session_url}` to the project's attention channel, stamp the session, echo the
// permalink, end the turn. Two things nevertheless have to survive a process
// restart, and they are what this file stores:
//
//	agent_sessions.attention_requested  — the stamp §9 puts on the session, and
//	                                      the flag §8.2 copies onto the
//	                                      `worker.finished` envelope so reviewers
//	                                      can skip deliberately half-done work.
//	attention_requests                  — one row per call, carrying the optional
//	                                      `expires_in` deadline. The sweep reads
//	                                      it, and a request that lapses unanswered
//	                                      becomes a `human.attention.timeout`
//	                                      event (§8.2) so the *worker's prompt*
//	                                      decides the fallback.
//
// This is runtime state, not project configuration: §15.3's closed vocabulary
// has no verb for it and nothing here is a setting, so — like event_deliveries —
// it writes no config event (§15.3 rule 3).
//
// What is NOT here, on purpose: any approval state machine, draft queue or
// pending-items projection. The thread itself is the review surface (§9); a
// request is answered by a human simply typing the next message.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EventTypeHumanAttentionTimeout is emitted by the attention sweep when a
// request made with `expires_in` lapses unanswered (§8.2). Its envelope is
// {source: "core", depth: 0} plus the worker and session of the paused job.
const EventTypeHumanAttentionTimeout = "human.attention.timeout"

// ErrAttentionRequestNotFound is returned when no request matches.
var ErrAttentionRequestNotFound = errors.New("attention request not found")

// AttentionRequest is one `request_human_attention` call (§9).
//
// AnsweredAt and TimedOutAt are terminal and mutually exclusive: a request is
// open while both are 0. ExpiresAt 0 means "no deadline" — the majority case,
// since `expires_in` is optional and a request without one simply waits.
type AttentionRequest struct {
	ID        string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Project   string `json:"project" gorm:"type:varchar(255);not null;index:idx_attention_requests_project"`
	SessionID string `json:"session_id" gorm:"type:varchar(36);not null;index:idx_attention_requests_session"`
	Worker    string `json:"worker" gorm:"type:varchar(255)"`
	// Message is what the worker asked the human for, verbatim.
	Message string `json:"message" gorm:"type:text"`
	// SessionURL is the permalink minted at request time (§9, F3). Stored rather
	// than recomputed so a later change of AGENTKIT_PUBLIC_BASE_URL cannot
	// rewrite history.
	SessionURL string `json:"session_url" gorm:"type:text"`
	// Channel records where the notification actually went: "webhook" when a
	// channel was configured and posted to, "none" for the log-only fallback.
	Channel string `json:"channel" gorm:"type:varchar(30)"`
	// Delivered is false when the channel was unset (log-only) or the post
	// failed — the tool still succeeds either way (§9), but the record says so.
	Delivered  bool  `json:"delivered"`
	ExpiresAt  int64 `json:"expires_at"` // unix seconds; 0 = never expires
	CreatedAt  int64 `json:"created_at" gorm:"autoCreateTime"`
	AnsweredAt int64 `json:"answered_at"`
	TimedOutAt int64 `json:"timed_out_at"`
}

func (AttentionRequest) TableName() string { return "attention_requests" }

// CreateAttentionRequest records one call and stamps the session in the same
// breath, so a crash can never leave a stamped session with no record or a
// record with an unstamped session.
func (s *Store) CreateAttentionRequest(ctx context.Context, req *AttentionRequest) (*AttentionRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("attention request is required")
	}
	if strings.TrimSpace(req.Project) == "" {
		return nil, fmt.Errorf("project is required")
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, fmt.Errorf("message is required (a human needs to know what you need)")
	}
	if req.ExpiresAt < 0 {
		return nil, fmt.Errorf("expires_at must not be negative")
	}
	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	if err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(req).Error; err != nil {
			return err
		}
		return tx.Model(&Session{}).
			Where("id = ? AND customer = ?", req.SessionID, req.Project).
			Update("attention_requested", true).Error
	}); err != nil {
		return nil, fmt.Errorf("failed to create attention request: %w", err)
	}
	return req, nil
}

// GetAttentionRequest reads one request within a project. Another project's row
// looks like a missing row.
func (s *Store) GetAttentionRequest(ctx context.Context, project, id string) (*AttentionRequest, error) {
	if project == "" || id == "" {
		return nil, fmt.Errorf("project and id are required")
	}
	var req AttentionRequest
	err := s.gdb.WithContext(ctx).Where("project = ? AND id = ?", project, id).First(&req).Error
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s", ErrAttentionRequestNotFound, project, id)
		}
		return nil, fmt.Errorf("failed to get attention request: %w", err)
	}
	return &req, nil
}

// ListOpenAttentionRequests returns the project's unresolved requests,
// newest-first.
func (s *Store) ListOpenAttentionRequests(ctx context.Context, project string) ([]*AttentionRequest, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	out := []*AttentionRequest{}
	if err := s.gdb.WithContext(ctx).Model(&AttentionRequest{}).
		Where("project = ? AND answered_at = 0 AND timed_out_at = 0", project).
		Order("created_at DESC, id DESC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list attention requests: %w", err)
	}
	return out, nil
}

// ListExpiredAttentionRequests returns every open request whose deadline has
// passed, across every project — the sweep's poll. Deliberately unscoped for the
// same reason as the scheduler's poll: the sweep is core, not a tenant.
//
// Requests with no deadline (expires_at = 0) are never returned: `expires_in` is
// optional, and a request without one waits indefinitely by design (§9).
func (s *Store) ListExpiredAttentionRequests(ctx context.Context, now int64, limit int) ([]*AttentionRequest, error) {
	out := []*AttentionRequest{}
	if err := s.gdb.WithContext(ctx).Model(&AttentionRequest{}).
		Where("expires_at > 0 AND expires_at <= ? AND answered_at = 0 AND timed_out_at = 0", now).
		Order("expires_at ASC, id ASC").Limit(clampLimit(limit)).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("failed to list expired attention requests: %w", err)
	}
	return out, nil
}

// resolveAttention closes a request and clears the session stamp in one
// transaction. column is answered_at or timed_out_at.
func (s *Store) resolveAttention(ctx context.Context, id, column string, at int64) error {
	if id == "" {
		return fmt.Errorf("attention request id is required")
	}
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var req AttentionRequest
		if err := tx.Where("id = ?", id).First(&req).Error; err != nil {
			if isNotFound(err) {
				return fmt.Errorf("%w: %s", ErrAttentionRequestNotFound, id)
			}
			return err
		}
		if req.AnsweredAt != 0 || req.TimedOutAt != 0 {
			// Already resolved: the sweep is at-least-once, so a second pass over
			// the same row must be a no-op rather than a second timeout event.
			return nil
		}
		if err := tx.Model(&AttentionRequest{}).Where("id = ?", id).Update(column, at).Error; err != nil {
			return err
		}
		// The stamp is per-request: with this one closed the session is no longer
		// waiting on a human, unless another open request says otherwise.
		var open int64
		if err := tx.Model(&AttentionRequest{}).
			Where("session_id = ? AND id <> ? AND answered_at = 0 AND timed_out_at = 0", req.SessionID, id).
			Count(&open).Error; err != nil {
			return err
		}
		if open > 0 {
			return nil
		}
		return tx.Model(&Session{}).Where("id = ?", req.SessionID).
			Update("attention_requested", false).Error
	})
}

// MarkAttentionAnswered closes a request because a human replied. Idempotent:
// re-resolving an already-resolved request changes nothing.
func (s *Store) MarkAttentionAnswered(ctx context.Context, id string, at int64) error {
	if err := s.resolveAttention(ctx, id, "answered_at", at); err != nil {
		return fmt.Errorf("failed to mark attention answered: %w", err)
	}
	return nil
}

// MarkAttentionTimedOut closes a request because its deadline passed unanswered.
// Idempotent, which is what stops the sweep emitting a second
// `human.attention.timeout` for the same request after a crash.
func (s *Store) MarkAttentionTimedOut(ctx context.Context, id string, at int64) error {
	if err := s.resolveAttention(ctx, id, "timed_out_at", at); err != nil {
		return fmt.Errorf("failed to mark attention timed out: %w", err)
	}
	return nil
}

// CountUserMessagesSince counts the human turns a session received at or after
// `since` (unix seconds). It is how the sweep tells "answered" from "lapsed":
// §9 has no approval state machine, so a reply IS the answer — whatever the
// human typed.
func (s *Store) CountUserMessagesSince(ctx context.Context, sessionID string, since int64) (int64, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("session id is required")
	}
	var n int64
	if err := s.gdb.WithContext(ctx).Model(&Message{}).
		Where("session_id = ? AND role = ? AND created_at >= ?", sessionID, "user", since).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("failed to count user messages: %w", err)
	}
	return n, nil
}

// SessionAwaitsHuman reports whether a session still has an open attention
// request — i.e. whether a human owes it an answer.
//
// This is DELIBERATELY not `agent_sessions.attention_requested`. That column is
// the per-TURN carrier §8.2 copies onto the `worker.finished` envelope and the
// emitter clears the moment it has copied it, so by the time a job settles it
// says nothing about whether anyone replied. The open rows in
// `attention_requests` are the durable fact, and they are what the dispatch gate
// parks a delivery at `awaiting_human` on (§8.4).
//
// No approval state: "open" means created and neither answered nor timed out,
// and a human simply typing the next message is what closes it (§9).
func (s *Store) SessionAwaitsHuman(ctx context.Context, project, sessionID string) (bool, error) {
	if project == "" || sessionID == "" {
		return false, fmt.Errorf("project and session id are required")
	}
	var n int64
	if err := s.gdb.WithContext(ctx).Model(&AttentionRequest{}).
		Where("project = ? AND session_id = ? AND answered_at = 0 AND timed_out_at = 0", project, sessionID).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("failed to count open attention requests: %w", err)
	}
	return n > 0, nil
}

// SetSessionAttentionRequested writes the session stamp directly. The tool path
// does not need it (CreateAttentionRequest stamps transactionally) — it exists
// for E2's emitter, which clears the per-turn flag after copying it onto the
// `worker.finished` envelope (§8.2).
func (s *Store) SetSessionAttentionRequested(ctx context.Context, sessionID string, requested bool) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	res := s.gdb.WithContext(ctx).Model(&Session{}).
		Where("id = ?", sessionID).Update("attention_requested", requested)
	if res.Error != nil {
		return fmt.Errorf("failed to stamp session attention: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("session not found")
	}
	return nil
}
