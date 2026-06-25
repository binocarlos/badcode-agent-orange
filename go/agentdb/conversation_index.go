package agentdb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ConversationIndex is one searchable memory row per session: an LLM summary
// (embedded) plus the full reconstructed transcript (keyword-only via tsvector).
type ConversationIndex struct {
	SessionID      string `json:"session_id" gorm:"primaryKey;type:varchar(36)"`
	Customer       string `json:"customer" gorm:"type:varchar(255);not null;index:idx_aci_customer"`
	Job            string `json:"job" gorm:"type:varchar(255);default:''"`
	UserEmail      string `json:"user_email" gorm:"type:varchar(255);default:''"`
	WorkflowID     string `json:"workflow_id" gorm:"type:varchar(100);default:''"`
	Title          string `json:"title" gorm:"type:varchar(255);default:''"`
	Summary        string `json:"summary" gorm:"type:text;default:''"`
	MessageCount   int    `json:"message_count" gorm:"default:0"`
	LastActivityAt int64  `json:"last_activity_at" gorm:"default:0"`
	IndexedAt      int64  `json:"indexed_at" gorm:"default:0"`
	SourceHash     string `json:"source_hash" gorm:"type:text;default:''"`
	// summary_embedding and transcript_tsv are written via raw SQL (Task 2),
	// not mapped here — GORM has no pgvector/tsvector column types.
}

func (ConversationIndex) TableName() string { return "agent_conversation_index" }

// ConversationIndexMeta holds staleness fields for a session.
type ConversationIndexMeta struct {
	SessionID  string
	IndexedAt  int64
	SourceHash string
}

// ConversationSearchQuery parameterises a hybrid keyword+vector search.
type ConversationSearchQuery struct {
	Customer          string
	Job               string
	Query             string
	QueryEmbedding    []float32
	ExcludeUserEmails []string
	Limit             int
}

// ConversationSearchResult is one row returned by SearchConversations.
type ConversationSearchResult struct {
	SessionID      string  `json:"session_id"`
	Title          string  `json:"title"`
	Summary        string  `json:"summary"`
	UserEmail      string  `json:"user_email"`
	Job            string  `json:"job"`
	LastActivityAt int64   `json:"last_activity_at"`
	Score          float64 `json:"score"`
	MatchType      string  `json:"match_type"`
}

// GetConversationIndexMeta returns the staleness metadata for a session, or
// (nil, nil) when the session has never been indexed.
func (s *Store) GetConversationIndexMeta(ctx context.Context, sessionID string) (*ConversationIndexMeta, error) {
	var row ConversationIndex
	err := s.gdb.WithContext(ctx).Select("session_id", "indexed_at", "source_hash").
		Where("session_id = ?", sessionID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation index meta: %w", err)
	}
	return &ConversationIndexMeta{SessionID: row.SessionID, IndexedAt: row.IndexedAt, SourceHash: row.SourceHash}, nil
}

// ListStaleConversationSessions returns session IDs whose newest query event is
// older than idleBeforeUnix (idle) AND newer than the indexed row's last
// activity (or never indexed). Postgres-only (uses agent_query_events).
func (s *Store) ListStaleConversationSessions(ctx context.Context, idleBeforeUnix int64, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	var ids []string
	sql := `
		SELECT q.session_id
		FROM (
			SELECT session_id, MAX(created_at) AS last_activity
			FROM agent_query_events
			GROUP BY session_id
		) q
		LEFT JOIN agent_conversation_index i ON i.session_id = q.session_id
		WHERE q.last_activity < ?
		  AND (i.session_id IS NULL OR q.last_activity > i.last_activity_at)
		ORDER BY q.last_activity DESC
		LIMIT ?
	`
	if err := s.gdb.WithContext(ctx).Raw(sql, idleBeforeUnix, limit).Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("list stale conversation sessions: %w", err)
	}
	return ids, nil
}

// UpsertConversationIndex writes the summary row plus the pgvector embedding and
// the tsvector built from transcriptText. Raw SQL for the vector/tsv columns.
func (s *Store) UpsertConversationIndex(ctx context.Context, ci *ConversationIndex, embedding []float32, transcriptText string) error {
	embStr := float32SliceToConvString(embedding)
	const maxTsvBytes = 900_000
	if len(transcriptText) > maxTsvBytes {
		transcriptText = transcriptText[:maxTsvBytes]
	}
	sql := `
		INSERT INTO agent_conversation_index
			(session_id, customer, job, user_email, workflow_id, title, summary,
			 summary_embedding, transcript_tsv, message_count, last_activity_at, indexed_at, source_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ` + embClause(embedding) + `, to_tsvector('english', ?), ?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE SET
			customer = EXCLUDED.customer,
			job = EXCLUDED.job,
			user_email = EXCLUDED.user_email,
			workflow_id = EXCLUDED.workflow_id,
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			summary_embedding = EXCLUDED.summary_embedding,
			transcript_tsv = EXCLUDED.transcript_tsv,
			message_count = EXCLUDED.message_count,
			last_activity_at = EXCLUDED.last_activity_at,
			indexed_at = EXCLUDED.indexed_at,
			source_hash = EXCLUDED.source_hash
	`
	args := []any{ci.SessionID, ci.Customer, ci.Job, ci.UserEmail, ci.WorkflowID, ci.Title, ci.Summary}
	if len(embedding) > 0 {
		args = append(args, embStr)
	}
	args = append(args, transcriptText, ci.MessageCount, ci.LastActivityAt, ci.IndexedAt, ci.SourceHash)
	if err := s.gdb.WithContext(ctx).Exec(sql, args...).Error; err != nil {
		return fmt.Errorf("upsert conversation index: %w", err)
	}
	return nil
}

// SearchConversations runs hybrid RRF (keyword tsvector + vector cosine) scoped
// to customer, with optional job narrowing and user-email exclusions. Falls back
// to keyword-only when no query embedding is supplied. Postgres-only.
func (s *Store) SearchConversations(ctx context.Context, q *ConversationSearchQuery) ([]*ConversationSearchResult, error) {
	sql, args := buildConversationSearchSQL(q)
	return s.scanConvResults(ctx, sql, args...)
}

// buildConversationSearchSQL constructs the search SQL and its positional args.
// Extracted so the placeholder/arg pairing and the conditional user-exclusion
// predicate are unit-testable without Postgres (see conversation_search_test.go).
func buildConversationSearchSQL(q *ConversationSearchQuery) (string, []any) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	exclude := lowerAll(q.ExcludeUserEmails)
	// Optional user-exclusion predicate. Omitted when empty: passing an empty slice
	// to `ALL(?)` renders as `ALL(NULL)`, which is NULL (not true) and silently
	// filters out every row.
	excl := ""
	if len(exclude) > 0 {
		excl = " AND LOWER(user_email) <> ALL(?)"
	}

	if len(q.QueryEmbedding) == 0 {
		sql := `
			SELECT session_id, title, summary, user_email, job, last_activity_at,
				ts_rank_cd(transcript_tsv, plainto_tsquery('english', ?)) AS score,
				'keyword_only' AS match_type
			FROM agent_conversation_index
			WHERE customer = ?
			  AND transcript_tsv @@ plainto_tsquery('english', ?)` + excl + `
			  AND (? = '' OR job = ?)
			ORDER BY score DESC
			LIMIT ?`
		args := []any{q.Query, q.Customer, q.Query}
		if len(exclude) > 0 {
			args = append(args, exclude)
		}
		args = append(args, q.Job, q.Job, limit)
		return sql, args
	}

	embStr := float32SliceToConvString(q.QueryEmbedding)
	sql := `
		WITH keyword AS (
			SELECT session_id, ROW_NUMBER() OVER (ORDER BY ts_rank_cd(transcript_tsv, qq) DESC) AS rn
			FROM agent_conversation_index, plainto_tsquery('english', ?) AS qq
			WHERE customer = ? AND transcript_tsv @@ qq` + excl + `
			  AND (? = '' OR job = ?)
			ORDER BY ts_rank_cd(transcript_tsv, qq) DESC LIMIT 20
		),
		semantic AS (
			SELECT session_id, ROW_NUMBER() OVER (ORDER BY summary_embedding <=> ?::vector ASC) AS rn
			FROM agent_conversation_index
			WHERE customer = ? AND summary_embedding IS NOT NULL` + excl + `
			  AND (? = '' OR job = ?)
			ORDER BY summary_embedding <=> ?::vector LIMIT 20
		),
		rrf AS (
			SELECT COALESCE(k.session_id, s.session_id) AS session_id,
				COALESCE(1.0/(60+k.rn),0) + COALESCE(1.0/(60+s.rn),0) AS score,
				CASE WHEN k.session_id IS NOT NULL AND s.session_id IS NOT NULL THEN 'keyword+semantic'
					 WHEN k.session_id IS NOT NULL THEN 'keyword_only' ELSE 'semantic_only' END AS match_type
			FROM keyword k FULL OUTER JOIN semantic s ON k.session_id = s.session_id
		)
		SELECT i.session_id, i.title, i.summary, i.user_email, i.job, i.last_activity_at,
			rrf.score, rrf.match_type
		FROM rrf JOIN agent_conversation_index i ON i.session_id = rrf.session_id
		ORDER BY rrf.score DESC LIMIT ?`
	args := []any{q.Query, q.Customer}
	if len(exclude) > 0 {
		args = append(args, exclude)
	}
	args = append(args, q.Job, q.Job, embStr, q.Customer)
	if len(exclude) > 0 {
		args = append(args, exclude)
	}
	args = append(args, q.Job, q.Job, embStr, limit)
	return sql, args
}

// ListConversations returns recent indexed conversations for a customer (most
// recent first), optionally narrowed by job. Skips empty marker rows. Used by
// `pt chats list` for browsing without a search term.
func (s *Store) ListConversations(ctx context.Context, customer, job string, limit int) ([]*ConversationSearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	sql := `
		SELECT session_id, title, summary, user_email, job, last_activity_at, 0 AS score, 'list' AS match_type
		FROM agent_conversation_index
		WHERE customer = ? AND summary <> '' AND (? = '' OR job = ?)
		ORDER BY last_activity_at DESC
		LIMIT ?`
	return s.scanConvResults(ctx, sql, customer, job, job, limit)
}

func (s *Store) scanConvResults(ctx context.Context, sql string, args ...any) ([]*ConversationSearchResult, error) {
	rows, err := s.gdb.WithContext(ctx).Raw(sql, args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("search conversations: %w", err)
	}
	defer rows.Close()
	var out []*ConversationSearchResult
	for rows.Next() {
		var r ConversationSearchResult
		if err := rows.Scan(&r.SessionID, &r.Title, &r.Summary, &r.UserEmail, &r.Job, &r.LastActivityAt, &r.Score, &r.MatchType); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	if out == nil {
		out = []*ConversationSearchResult{}
	}
	return out, nil
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func float32SliceToConvString(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", f)
	}
	b.WriteByte(']')
	return b.String()
}

// embClause returns the SQL fragment for the embedding column in the INSERT:
// a bound "?::vector" when present, or literal NULL when absent.
// ListAllSessionIDs returns session IDs (most recent first), for backfill.
func (s *Store) ListAllSessionIDs(ctx context.Context, limit int) ([]string, error) {
	var ids []string
	db := s.gdb.WithContext(ctx).Model(&Session{}).Order("created_at DESC")
	if limit > 0 {
		db = db.Limit(limit)
	}
	if err := db.Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("list all session ids: %w", err)
	}
	return ids, nil
}

func embClause(embedding []float32) string {
	if len(embedding) > 0 {
		return "?::vector"
	}
	return "NULL"
}
