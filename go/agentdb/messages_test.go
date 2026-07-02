package agentdb

import (
	"context"
	"fmt"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/events"
)

func TestCreateMessages(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	// Empty batch is a no-op.
	if err := s.CreateMessages(ctx, nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}

	// Missing session_id rejects the whole batch.
	err := s.CreateMessages(ctx, []*Message{
		{SessionID: "s1", Role: "user", Content: "ok"},
		{Role: "user", Content: "no session"},
	})
	if err == nil {
		t.Fatalf("expected error for missing session_id")
	}

	msgs := []*Message{
		{SessionID: "s1", Role: "user", Content: "hi", SequenceNum: 1},
		{SessionID: "s1", Role: "assistant", Content: "hello", SequenceNum: 2, ToolInput: JSONMap{"cmd": "ls"}},
	}
	if err := s.CreateMessages(ctx, msgs); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i, m := range msgs {
		if m.ID == "" {
			t.Fatalf("message %d: expected generated ID", i)
		}
	}
	count, err := s.GetMessageCount(ctx, "s1")
	if err != nil || count != 2 {
		t.Fatalf("expected 2 messages, got %d (err %v)", count, err)
	}
}

func TestListMessages_FiltersOrderingPagination(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	// Insert out of order to prove sequence_num ASC ordering.
	if err := s.CreateMessages(ctx, []*Message{
		{SessionID: "s1", Role: "assistant", Content: "third", SequenceNum: 3, PhaseNode: "plan"},
		{SessionID: "s1", Role: "user", Content: "first", SequenceNum: 1, PhaseNode: "intro"},
		{SessionID: "s1", Role: "assistant", Content: "second", SequenceNum: 2, PhaseNode: "intro"},
		{SessionID: "s2", Role: "user", Content: "other session", SequenceNum: 1},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, total, err := s.ListMessages(ctx, &MessageQuery{SessionID: "s1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("expected 3/3, got total=%d rows=%d", total, len(rows))
	}
	if rows[0].Content != "first" || rows[1].Content != "second" || rows[2].Content != "third" {
		t.Fatalf("wrong order: %q %q %q", rows[0].Content, rows[1].Content, rows[2].Content)
	}

	// PhaseNode filter composes with session filter.
	rows, total, err = s.ListMessages(ctx, &MessageQuery{SessionID: "s1", PhaseNode: "intro"})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("phase filter: total=%d rows=%d err=%v", total, len(rows), err)
	}

	// Pagination: total stays the full count; negative offset is clamped to 0.
	rows, total, err = s.ListMessages(ctx, &MessageQuery{SessionID: "s1", Limit: 1, Offset: 1})
	if err != nil || total != 3 || len(rows) != 1 || rows[0].Content != "second" {
		t.Fatalf("pagination: total=%d rows=%v err=%v", total, rows, err)
	}
	rows, _, err = s.ListMessages(ctx, &MessageQuery{SessionID: "s1", Limit: 1, Offset: -5})
	if err != nil || len(rows) != 1 || rows[0].Content != "first" {
		t.Fatalf("negative offset: rows=%v err=%v", rows, err)
	}

	// No matches → empty non-nil slice.
	rows, total, err = s.ListMessages(ctx, &MessageQuery{SessionID: "nope"})
	if err != nil || rows == nil || len(rows) != 0 || total != 0 {
		t.Fatalf("no-match: rows=%#v total=%d err=%v", rows, total, err)
	}
}

func TestListMessages_LimitDefaultsAndCap(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	batch := make([]*Message, 1001)
	for i := range batch {
		batch[i] = &Message{SessionID: "big", Role: "user", Content: fmt.Sprintf("m%d", i), SequenceNum: i}
	}
	if err := s.CreateMessages(ctx, batch); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Limit <= 0 defaults to 200.
	rows, total, err := s.ListMessages(ctx, &MessageQuery{SessionID: "big"})
	if err != nil || len(rows) != 200 || total != 1001 {
		t.Fatalf("default limit: rows=%d total=%d err=%v", len(rows), total, err)
	}
	// Limit above 1000 is capped at 1000.
	rows, total, err = s.ListMessages(ctx, &MessageQuery{SessionID: "big", Limit: 5000})
	if err != nil || len(rows) != 1000 || total != 1001 {
		t.Fatalf("capped limit: rows=%d total=%d err=%v", len(rows), total, err)
	}
}

func TestDeleteMessagesForSession(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	if err := s.DeleteMessagesForSession(ctx, ""); err == nil {
		t.Fatalf("expected error for empty session_id")
	}

	if err := s.CreateMessages(ctx, []*Message{
		{SessionID: "s1", Role: "user", Content: "a"},
		{SessionID: "s2", Role: "user", Content: "b"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.DeleteMessagesForSession(ctx, "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := s.GetMessageCount(ctx, "s1"); n != 0 {
		t.Fatalf("s1 messages not deleted: %d", n)
	}
	if n, _ := s.GetMessageCount(ctx, "s2"); n != 1 {
		t.Fatalf("s2 messages must be untouched: %d", n)
	}
}

func TestUpsertQueryEvents(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	if err := s.UpsertQueryEvents(ctx, &QueryEvents{QueryID: "q1"}); err == nil {
		t.Fatalf("expected error for missing session_id")
	}
	if err := s.UpsertQueryEvents(ctx, &QueryEvents{SessionID: "s1"}); err == nil {
		t.Fatalf("expected error for missing query_id")
	}

	first := &QueryEvents{SessionID: "s1", QueryID: "q1", Events: JSONArray(`[{"type":"text_delta"}]`), SearchText: "one", CreatedAt: 111}
	if err := s.UpsertQueryEvents(ctx, first); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if first.ID == "" {
		t.Fatalf("expected generated ID")
	}

	// Conflict on (session_id, query_id) replaces events + search_text but
	// keeps the original row (id, created_at).
	second := &QueryEvents{SessionID: "s1", QueryID: "q1", Events: JSONArray(`[{"type":"query_complete"}]`), SearchText: "two", CreatedAt: 222}
	if err := s.UpsertQueryEvents(ctx, second); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := s.ListQueryEvents(ctx, "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", len(rows))
	}
	if rows[0].ID != first.ID || rows[0].CreatedAt != 111 {
		t.Fatalf("upsert must keep original id/created_at, got %+v", rows[0])
	}
	if string(rows[0].Events) != `[{"type":"query_complete"}]` || rows[0].SearchText != "two" {
		t.Fatalf("upsert did not replace payload: %+v", rows[0])
	}

	// CreatedAt defaults to now when unset.
	third := &QueryEvents{SessionID: "s1", QueryID: "q2", Events: JSONArray(`[]`)}
	if err := s.UpsertQueryEvents(ctx, third); err != nil {
		t.Fatalf("insert q2: %v", err)
	}
	if third.CreatedAt == 0 {
		t.Fatalf("expected CreatedAt to be defaulted")
	}
}

func TestListQueryEvents_OrderAndIsolation(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	seed := []*QueryEvents{
		{SessionID: "s1", QueryID: "q2", Events: JSONArray(`[2]`), CreatedAt: 200},
		{SessionID: "s1", QueryID: "q1", Events: JSONArray(`[1]`), CreatedAt: 100},
		{SessionID: "other", QueryID: "q1", Events: JSONArray(`[9]`), CreatedAt: 50},
	}
	for _, qe := range seed {
		if err := s.UpsertQueryEvents(ctx, qe); err != nil {
			t.Fatalf("seed %s/%s: %v", qe.SessionID, qe.QueryID, err)
		}
	}

	rows, err := s.ListQueryEvents(ctx, "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 || rows[0].QueryID != "q1" || rows[1].QueryID != "q2" {
		t.Fatalf("expected [q1 q2] by created_at ASC, got %+v", rows)
	}

	empty, err := s.ListQueryEvents(ctx, "none")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v err=%v", empty, err)
	}
}

func TestQueryEventsFlat_RoundTrip(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	batch1 := []events.Envelope{
		{Type: events.Type("user_message"), Data: map[string]any{"text": "hi"}},
		{Type: events.Type("query_complete"), Data: map[string]any{}},
	}
	batch2 := []events.Envelope{
		{Type: events.Type("user_message"), Data: map[string]any{"text": "again"}},
	}
	if err := s.PersistQueryEventsFlat(ctx, "s1", "q1", batch1, "hi"); err != nil {
		t.Fatalf("persist q1: %v", err)
	}
	if err := s.PersistQueryEventsFlat(ctx, "s1", "q2", batch2, "again"); err != nil {
		t.Fatalf("persist q2: %v", err)
	}
	// Re-persisting a query replaces its batch, not appends.
	if err := s.PersistQueryEventsFlat(ctx, "s1", "q1", batch1, "hi"); err != nil {
		t.Fatalf("re-persist q1: %v", err)
	}

	got, err := s.ListQueryEventsFlat(ctx, "s1")
	if err != nil {
		t.Fatalf("list flat: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events (2 + 1), got %d", len(got))
	}
	if got[0].Data["text"] != "hi" || got[2].Data["text"] != "again" {
		t.Fatalf("flat order/content wrong: %+v", got)
	}
}

func TestListQueryEventsFlat_SkipsEmptyAndRejectsCorrupt(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	// A row whose events JSON is empty is skipped, not an error.
	if err := s.gdb.WithContext(ctx).Create(&QueryEvents{
		ID: "e1", SessionID: "s1", QueryID: "q1", Events: JSONArray(""), CreatedAt: 1,
	}).Error; err != nil {
		t.Fatalf("seed empty: %v", err)
	}
	got, err := s.ListQueryEventsFlat(ctx, "s1")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty events row: got=%v err=%v", got, err)
	}

	// A corrupt row surfaces a decode error naming the query.
	if err := s.gdb.WithContext(ctx).Create(&QueryEvents{
		ID: "e2", SessionID: "s1", QueryID: "q-bad", Events: JSONArray("{corrupt"), CreatedAt: 2,
	}).Error; err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	if _, err := s.ListQueryEventsFlat(ctx, "s1"); err == nil {
		t.Fatalf("expected decode error for corrupt events row")
	}
}
