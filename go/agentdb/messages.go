package agentdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/google/uuid"
)

// lowerAll lowercases every element, matching LOWER(...) column predicates in
// the exclusion filters (SearchMessages, ListSessions).
func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = strings.ToLower(v)
	}
	return out
}

func (s *Store) CreateMessages(ctx context.Context, messages []*Message) error {
	if len(messages) == 0 {
		return nil
	}
	for _, msg := range messages {
		if msg.ID == "" {
			msg.ID = uuid.New().String()
		}
		if msg.SessionID == "" {
			return fmt.Errorf("session_id is required for all messages")
		}
	}
	if err := s.gdb.WithContext(ctx).CreateInBatches(messages, 100).Error; err != nil {
		return fmt.Errorf("failed to create agent messages: %w", err)
	}
	return nil
}

func (s *Store) ListMessages(ctx context.Context, query *MessageQuery) ([]*Message, int64, error) {
	var messages []*Message
	var total int64

	db := s.gdb.WithContext(ctx).Model(&Message{})

	if query.SessionID != "" {
		db = db.Where("session_id = ?", query.SessionID)
	}
	if query.PhaseNode != "" {
		db = db.Where("phase_node = ?", query.PhaseNode)
	}

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count agent messages: %w", err)
	}

	limit := query.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}

	if err := db.Order("sequence_num ASC").Limit(limit).Offset(offset).Find(&messages).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list agent messages: %w", err)
	}
	if messages == nil {
		messages = []*Message{}
	}
	return messages, total, nil
}

func (s *Store) GetMessageCount(ctx context.Context, sessionID string) (int64, error) {
	var count int64
	if err := s.gdb.WithContext(ctx).Model(&Message{}).Where("session_id = ?", sessionID).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("failed to count agent messages: %w", err)
	}
	return count, nil
}

func (s *Store) DeleteMessagesForSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if err := s.gdb.WithContext(ctx).Where("session_id = ?", sessionID).Delete(&Message{}).Error; err != nil {
		return fmt.Errorf("failed to delete agent messages: %w", err)
	}
	return nil
}

// SearchMessages is a listing, so it obeys the soft-delete filter (migration
// 041): a deleted session's messages survive in the table but must not surface
// here, or search would hand the user back a conversation the UI told them was
// gone — and hand them a session id every by-id route now 404s.
func (s *Store) SearchMessages(ctx context.Context, query *MessageSearchQuery) ([]*MessageSearchResult, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	sqlQuery := `
		SELECT
			m.session_id,
			s.title AS session_title,
			s.user_email,
			m.role,
			CASE WHEN length(m.content) > 500 THEN substring(m.content, 1, 500) ELSE m.content END AS content,
			m.created_at,
			s.job,
			s.workflow_id,
			ts_rank_cd(m.content_tsv, plainto_tsquery('english', ?)) AS rank
		FROM agent_messages m
		JOIN agent_sessions s ON m.session_id = s.id
		WHERE s.deleted_at = 0
			AND s.customer = ?
			AND m.content_tsv @@ plainto_tsquery('english', ?)`

	args := []any{query.Query, query.Customer, query.Query}

	if query.UserEmail != "" {
		sqlQuery += " AND s.user_email = ?"
		args = append(args, query.UserEmail)
	}
	if query.Job != "" {
		sqlQuery += " AND s.job = ?"
		args = append(args, query.Job)
	}
	if query.Role != "" {
		sqlQuery += " AND m.role = ?"
		args = append(args, query.Role)
	}
	if len(query.ExcludeUserEmails) > 0 {
		// Lowercase the argument side to match LOWER(s.user_email).
		sqlQuery += " AND LOWER(s.user_email) NOT IN (?)"
		args = append(args, lowerAll(query.ExcludeUserEmails))
	}

	sqlQuery += " ORDER BY rank DESC LIMIT ?"
	args = append(args, limit)

	var results []*MessageSearchResult
	if err := s.gdb.WithContext(ctx).Raw(sqlQuery, args...).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to search agent messages: %w", err)
	}
	if results == nil {
		results = []*MessageSearchResult{}
	}
	return results, nil
}

func (s *Store) UpsertQueryEvents(ctx context.Context, qe *QueryEvents) error {
	if qe.ID == "" {
		qe.ID = uuid.New().String()
	}
	if qe.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if qe.QueryID == "" {
		return fmt.Errorf("query_id is required")
	}
	if qe.CreatedAt == 0 {
		qe.CreatedAt = time.Now().Unix()
	}

	// `ordinal` comes from the database, never from the caller: it is the
	// transcript's total order (migration 038) and `created_at` above is only
	// second-resolution, so ties there used to replay in arbitrary order.
	//
	// The conflict branch deliberately does NOT touch `ordinal`. A re-persist of
	// the same (session, query) — the pipeline rewriting a turn it already
	// stored — must keep the position the turn had when it first appeared;
	// re-numbering it would move a finished turn to the end of the transcript.
	// On Postgres nextval is still consumed on that path (it is evaluated before
	// the conflict is detected), which leaves gaps in the sequence. Gaps are
	// harmless: only the relative order within a session is ever read.
	result := s.gdb.WithContext(ctx).Exec(`
		INSERT INTO agent_query_events (id, session_id, query_id, events, search_text, created_at, ordinal)
		VALUES (?, ?, ?, ?, ?, ?, `+s.nextQueryOrdinalSQL()+`)
		ON CONFLICT (session_id, query_id) DO UPDATE SET
			events = EXCLUDED.events,
			search_text = EXCLUDED.search_text
	`, qe.ID, qe.SessionID, qe.QueryID, qe.Events, qe.SearchText, qe.CreatedAt)

	if result.Error != nil {
		return fmt.Errorf("failed to upsert agent query events: %w", result.Error)
	}
	return nil
}

// nextQueryOrdinalSQL is the expression that stamps a new transcript row's
// ordinal.
//
// On Postgres — the production store, and the only one where two writers can
// race — it is a SEQUENCE. A `MAX(ordinal)+1` subquery would be a read-then-
// write: two concurrent inserts read the same maximum and tie, which is RD16
// again in a narrower window. nextval is atomic. That the counter is global
// rather than per-session does not matter: a subsequence of a monotonic
// sequence is monotonic, and nothing reads the value except as an ORDER BY key
// within one session.
//
// sqlite has no sequences and no concurrent writers (the dev/test store is a
// single-process file), so it gets the MAX+1 subquery. It is exactly as ordered
// as the Postgres path for one writer, which is the only case that store has.
func (s *Store) nextQueryOrdinalSQL() string {
	if s.gdb.Dialector != nil && s.gdb.Dialector.Name() == "postgres" {
		return "nextval('agent_query_events_ordinal_seq')"
	}
	return "(SELECT COALESCE(MAX(ordinal), 0) + 1 FROM agent_query_events)"
}

// ListQueryEvents returns a session's stored turns in transcript order.
//
// The order is `ordinal` (migration 038): a sequence value stamped at insert,
// total and tie-free, so two turns written inside the same second replay in
// write order. `created_at, id` remain as tie-breaks for rows whose ordinal is
// 0 — rows written before migration 038 that the backfill did not reach, and
// rows some other writer inserted without the column. Those sort first, which is
// correct: every backfilled and every new row carries a positive ordinal, so a
// zero can only be older than or contemporaneous with the rest.
func (s *Store) ListQueryEvents(ctx context.Context, sessionID string) ([]*QueryEvents, error) {
	var qevents []*QueryEvents
	if err := s.gdb.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("ordinal ASC, created_at ASC, id ASC").
		Find(&qevents).Error; err != nil {
		return nil, fmt.Errorf("failed to list agent query events: %w", err)
	}
	if qevents == nil {
		qevents = []*QueryEvents{}
	}
	return qevents, nil
}

// PersistQueryEventsFlat upserts a flat []events.Envelope for (sessionID, queryID).
func (s *Store) PersistQueryEventsFlat(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error {
	raw, err := json.Marshal(evs)
	if err != nil {
		return err
	}
	return s.UpsertQueryEvents(ctx, &QueryEvents{
		SessionID:  sessionID,
		QueryID:    queryID,
		Events:     JSONArray(raw),
		SearchText: searchText,
	})
}

// ListQueryEventsFlatForQuery returns the events already persisted for ONE turn.
// It is the read half of the reconnect merge (see events.Splice): a stream that
// re-attaches to a turn nobody in this process owns any more must append to what
// the previous generation wrote, not replace it.
//
// Absent row -> (nil, nil): a turn nothing has persisted yet is not an error.
func (s *Store) ListQueryEventsFlatForQuery(ctx context.Context, sessionID, queryID string) ([]events.Envelope, error) {
	var rows []*QueryEvents
	if err := s.gdb.WithContext(ctx).
		Where("session_id = ? AND query_id = ?", sessionID, queryID).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list agent query events for query: %w", err)
	}
	var out []events.Envelope
	for _, r := range rows {
		if len(r.Events) == 0 {
			continue
		}
		var batch []events.Envelope
		if err := json.Unmarshal([]byte(r.Events), &batch); err != nil {
			return nil, fmt.Errorf("agentdb: decode query events for %s/%s: %w", sessionID, r.QueryID, err)
		}
		out = append(out, batch...)
	}
	return out, nil
}

// ListQueryEventsFlat returns all events for a session as a flat []events.Envelope.
func (s *Store) ListQueryEventsFlat(ctx context.Context, sessionID string) ([]events.Envelope, error) {
	rows, err := s.ListQueryEvents(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var out []events.Envelope
	for _, r := range rows {
		if len(r.Events) == 0 {
			continue
		}
		var batch []events.Envelope
		if err := json.Unmarshal([]byte(r.Events), &batch); err != nil {
			return nil, fmt.Errorf("agentdb: decode query events for %s/%s: %w", sessionID, r.QueryID, err)
		}
		out = append(out, batch...)
	}
	return out, nil
}
