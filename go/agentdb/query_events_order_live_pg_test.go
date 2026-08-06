package agentdb

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/google/uuid"
)

// The transcript-ordering tests for RD16 / migration 038.
//
// They are live-Postgres only, and they have to be: the ordinal comes from a
// Postgres SEQUENCE and the defect being fixed is *what order the database
// returns rows in*, which no in-memory fake can reproduce honestly.

// turnText marshals a one-envelope turn whose text names the turn, so a replay
// can be compared as a plain []string.
func turnText(t *testing.T, s string) JSONArray {
	t.Helper()
	raw, err := json.Marshal([]events.Envelope{{Type: "assistant_text", Data: map[string]any{"text": s}}})
	if err != nil {
		t.Fatalf("marshal turn: %v", err)
	}
	return JSONArray(raw)
}

// replayTexts reads a session back through the production reader and returns
// each turn's text, in the order the reader produced.
func replayTexts(t *testing.T, s *Store, sessionID string) []string {
	t.Helper()
	rows, err := s.ListQueryEvents(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListQueryEvents: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var batch []events.Envelope
		if err := json.Unmarshal([]byte(r.Events), &batch); err != nil {
			t.Fatalf("decode row: %v", err)
		}
		for _, e := range batch {
			out = append(out, fmt.Sprint(e.Data["text"]))
		}
	}
	return out
}

// TestLivePG_QueryEventsReplayInWriteOrderWithinOneSecond is RD16 itself: every
// turn below is stamped with the SAME created_at (which is what
// time.Now().Unix() does to turns written inside one second), and one of them is
// re-persisted afterwards — the pipeline rewriting a turn it has already stored.
//
// Under the old `ORDER BY created_at ASC` there was no tie-break at all, so
// Postgres returned heap order, and an UPDATE moves a row to the end of the
// heap: the re-persisted turn replayed LAST, after turns the user watched arrive
// after it. That is the coin toss, in the shape that reliably lands tails.
func TestLivePG_QueryEventsReplayInWriteOrderWithinOneSecond(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "cust-"+uuid.New().String(), "u@x.com")

	const sameSecond = int64(1750000000)
	write := func(queryID, text string) {
		t.Helper()
		qe := &QueryEvents{
			SessionID: sess.ID,
			QueryID:   queryID,
			Events:    turnText(t, text),
			CreatedAt: sameSecond,
		}
		if err := s.UpsertQueryEvents(ctx, qe); err != nil {
			t.Fatalf("upsert %s: %v", queryID, err)
		}
	}

	write("q1", "one")
	write("q2", "two")
	write("q3", "three")
	write("q4", "four")

	// Every ordinal is distinct and increasing: the point of a sequence over a
	// MAX()+1 read-then-write is that it cannot tie.
	stored, err := s.ListQueryEvents(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ListQueryEvents: %v", err)
	}
	if len(stored) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(stored))
	}
	for i, r := range stored {
		if r.Ordinal <= 0 {
			t.Fatalf("row %s has non-positive ordinal %d", r.QueryID, r.Ordinal)
		}
		if i > 0 && r.Ordinal <= stored[i-1].Ordinal {
			t.Fatalf("ordinals must strictly increase, got %d then %d", stored[i-1].Ordinal, r.Ordinal)
		}
	}

	// The turn is re-persisted (same session+query), as the pipeline does when
	// it rewrites a turn it already stored. Its position must not move.
	write("q2", "two (revised)")

	got := replayTexts(t, s, sess.ID)
	want := []string{"one", "two (revised)", "three", "four"}
	if len(got) != len(want) {
		t.Fatalf("replay length: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replay out of order:\n got %v\nwant %v", got, want)
		}
	}

	// A second read must agree with the first — a replay that disagrees with
	// itself is the same defect wearing a different hat.
	if again := replayTexts(t, s, sess.ID); fmt.Sprint(again) != fmt.Sprint(got) {
		t.Fatalf("two replays disagree: %v vs %v", got, again)
	}
}

// TestLivePG_QueryEventsMixedPreAndPostMigrationRows pins the backfill story.
//
// A pre-038 row is a row with ordinal 0 (the column's DDL default) carrying only
// a second-resolution created_at. Two claims are tested, in this order:
//
//  1. Before any backfill reaches them, zero-ordinal rows replay FIRST and
//     stably, ahead of every row the sequence has numbered. That is the mixed
//     pre/post claim, and it is what protects a database where 038's UPDATE
//     raced a concurrent insert.
//  2. Migration 038's own backfill SQL — re-run verbatim from agentMigrations,
//     which is safe because every statement in it is idempotent — numbers those
//     legacy rows in (created_at, id) order, and a row written afterwards still
//     outranks them.
//
// What is deliberately NOT claimed: that two legacy rows written inside the same
// second come back in true write order. The database never recorded it. The
// backfill freezes the arbitrary tie-break once, in a column, instead of the
// reader re-drawing it on every query — stable, not correct, and stable is the
// most that is recoverable.
func TestLivePG_QueryEventsMixedPreAndPostMigrationRows(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "cust-"+uuid.New().String(), "u@x.com")

	// Two "legacy" rows: inserted without the ordinal column, exactly as a
	// pre-038 agentd did, so they land on the DDL default of 0.
	legacy := func(queryID, text string, createdAt int64) {
		t.Helper()
		if err := s.DB().WithContext(ctx).Exec(`
			INSERT INTO agent_query_events (id, session_id, query_id, events, search_text, created_at)
			VALUES (?, ?, ?, ?::jsonb, '', ?)`,
			uuid.New().String(), sess.ID, queryID, string(turnText(t, text)), createdAt,
		).Error; err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
	}
	legacy("old1", "old one", 1750000000)
	legacy("old2", "old two", 1750000001)

	// And one row written by today's code.
	if err := s.UpsertQueryEvents(ctx, &QueryEvents{
		SessionID: sess.ID, QueryID: "new1", Events: turnText(t, "new one"),
		CreatedAt: 1750000000, // same second as the FIRST legacy row, deliberately
	}); err != nil {
		t.Fatalf("upsert new row: %v", err)
	}

	want := []string{"old one", "old two", "new one"}
	got := replayTexts(t, s, sess.ID)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("mixed pre/post replay:\n got %v\nwant %v", got, want)
	}

	// Now re-run the shipped backfill and re-check. Ordinals must become
	// positive for the legacy rows, in created_at order, and the order the user
	// sees must not change.
	var sql string
	for _, m := range agentMigrations {
		if m.Name == "038_agent_query_events_ordinal" {
			sql = m.SQL
		}
	}
	if sql == "" {
		t.Fatal("migration 038 not found by name — did it get renumbered?")
	}
	if err := s.DB().WithContext(ctx).Exec(sql).Error; err != nil {
		t.Fatalf("re-run migration 038: %v", err)
	}

	rows, err := s.ListQueryEvents(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ListQueryEvents after backfill: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Ordinal <= 0 {
			t.Fatalf("row %s still has ordinal %d after backfill", r.QueryID, r.Ordinal)
		}
	}
	if got := replayTexts(t, s, sess.ID); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("backfill changed the replay order:\n got %v\nwant %v", got, want)
	}

	// A row written after the backfill still sorts last: setval left the
	// sequence above every backfilled value.
	if err := s.UpsertQueryEvents(ctx, &QueryEvents{
		SessionID: sess.ID, QueryID: "new2", Events: turnText(t, "new two"),
		CreatedAt: 1750000000, // still the same second as the oldest row
	}); err != nil {
		t.Fatalf("upsert post-backfill row: %v", err)
	}
	want = append(want, "new two")
	if got := replayTexts(t, s, sess.ID); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("post-backfill row did not sort last:\n got %v\nwant %v", got, want)
	}
}
