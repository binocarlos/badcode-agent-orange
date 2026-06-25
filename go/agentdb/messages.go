package agentdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/google/uuid"
)

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
		WHERE s.customer = ?
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
		sqlQuery += " AND LOWER(s.user_email) NOT IN (?)"
		args = append(args, query.ExcludeUserEmails)
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

	result := s.gdb.WithContext(ctx).Exec(`
		INSERT INTO agent_query_events (id, session_id, query_id, events, search_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (session_id, query_id) DO UPDATE SET
			events = EXCLUDED.events,
			search_text = EXCLUDED.search_text
	`, qe.ID, qe.SessionID, qe.QueryID, qe.Events, qe.SearchText, qe.CreatedAt)

	if result.Error != nil {
		return fmt.Errorf("failed to upsert agent query events: %w", result.Error)
	}
	return nil
}

func (s *Store) ListQueryEvents(ctx context.Context, sessionID string) ([]*QueryEvents, error) {
	var qevents []*QueryEvents
	if err := s.gdb.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at ASC").
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

