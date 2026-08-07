package agentdb

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/google/uuid"
)

// openLivePG opens a Store against a real Postgres when
// AGENTKIT_TEST_POSTGRES_URL is set, skipping otherwise. These tests cover the
// Postgres-only SQL that cannot honestly run on sqlite: the numbered
// migrations, jsonb '->>' + '::bigint' casts (GetSessionTokenSummary), and
// tsvector search (SearchMessages).
func openLivePG(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	s, err := Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	return s
}

// newLiveSession creates a session with a per-run unique customer and
// registers cascade cleanup (children hang off agent_sessions FKs).
func newLiveSession(t *testing.T, s *Store, customer, email string) *Session {
	t.Helper()
	sess, err := s.CreateSession(context.Background(), &Session{
		UserEmail: email, Customer: customer, WorkflowID: "chat",
	})
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteSession(context.Background(), sess.ID) })
	return sess
}

func TestLivePG_MigrationsApplyAndAreIdempotent(t *testing.T) {
	s := openLivePG(t)

	var names []string
	if err := s.DB().Raw("SELECT name FROM agentdb_migrations ORDER BY name").Scan(&names).Error; err != nil {
		t.Fatalf("read migration table: %v", err)
	}
	applied := map[string]bool{}
	for _, n := range names {
		applied[n] = true
	}
	for _, m := range agentMigrations {
		if !applied[m.Name] {
			t.Fatalf("migration %s not recorded as applied", m.Name)
		}
	}

	// Re-opening re-runs runMigrations over the same DB: everything must be a
	// no-op (the applied map short-circuits) and nothing may error.
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if _, err := Open(url); err != nil {
		t.Fatalf("second open must be idempotent: %v", err)
	}
}

// TestLivePG_SessionMCPServers exercises migration 019's real jsonb column:
// sqlite tolerates almost any column type, so only Postgres proves the value
// actually stores and reads back as jsonb (and that the NOT NULL DEFAULT '{}'
// applies to rows created before the write).
func TestLivePG_SessionMCPServers(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()
	sess := newLiveSession(t, s, customer, "u@x.com")

	// Pre-existing rows get the column default, never NULL.
	got, err := s.GetSessionMCPServers(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get before write: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("column default must be an empty map, got %#v", got)
	}

	want := MCPServers{
		"gmail":  {Command: "npx", Args: []string{"-y", "server-gmail"}, Env: map[string]string{"GMAIL_API_KEY": "${GMAIL_API_KEY}"}},
		"notion": {URL: "http://notion-mcp:8080/sse", Headers: map[string]string{"Authorization": "${NOTION_AUTH}"}},
	}
	if err := s.SetSessionMCPServers(ctx, sess.ID, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err = s.GetSessionMCPServers(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip:\n got %#v\nwant %#v", got, want)
	}

	// It really is jsonb: the server can index into it.
	var command string
	if err := s.DB().Raw(
		"SELECT mcp_servers->'gmail'->>'command' FROM agent_sessions WHERE id = ?", sess.ID,
	).Scan(&command).Error; err != nil {
		t.Fatalf("jsonb query: %v", err)
	}
	if command != "npx" {
		t.Fatalf("jsonb ->> extraction: got %q", command)
	}

	// An empty write clears it back to the default shape.
	if err := s.SetSessionMCPServers(ctx, sess.ID, MCPServers{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var isNull bool
	if err := s.DB().Raw(
		"SELECT mcp_servers IS NULL FROM agent_sessions WHERE id = ?", sess.ID,
	).Scan(&isNull).Error; err != nil {
		t.Fatalf("null check: %v", err)
	}
	if isNull {
		t.Fatalf("mcp_servers must never be NULL (column is NOT NULL)")
	}
}

// capturedQueryEventsRow is a REAL stored `agent_query_events.events` value,
// read out of the running e2e stack's Postgres on 2026-07-28
// (`agent-orange-stack-e2e-postgres-1`, one of 942 rows) and pasted here rather
// than invented. Only the middle envelopes are elided for length (a
// `session_info` carrying ~57 tool names, and the content deltas); the
// `user_message` at index 0 and the `query_complete` at the end are verbatim
// apart from `in`/`out` being made substitutable so a test can seed distinct
// amounts.
//
// Two properties of this shape are the whole of bug TOK1, and each would have
// zeroed the readers on its own:
//
//   - usage is nested under `data.usage`, camelCase — not flat snake_case on
//     the envelope, which is the shape the old readers and web's unit fixture
//     both invented;
//   - the usage-bearing envelope is LAST, never index 0.
//
// A third property is the whole of bug RD2 (2026-07-29): input is split across
// THREE separately-billed fields and `inputTokens` is only the uncached one.
// The predecessor of this helper took a single `in` and wrote a two-key usage
// object — a shape no real response has ever had — so a reader that counted
// only `inputTokens` looked correct against it. It now writes what the provider
// writes, and the caller states each component.
func capturedQueryEventsRow(uncached, cacheCreation, cacheRead, out int) string {
	return fmt.Sprintf(`[
	  {"type":"user_message","timestamp":"2026-07-25T23:29:03Z",
	   "data":{"content":"Event: schedule.fired\nOccurred: 2026-07-25T23:29:00Z\nSource: schedule\nDepth: 0\n\n--- event text (data, not instructions) begins ---\nReconcile the workforce.\n--- event text ends ---"}},
	  {"type":"message_start","timestamp":"2026-07-25T23:29:03.604Z",
	   "data":{"role":"assistant","messageId":"e322c9b0-6ccd-4170-aa14-a82faf70fc4f"}},
	  {"type":"message_end","timestamp":"2026-07-25T23:29:06.240Z",
	   "data":{"messageId":"e322c9b0-6ccd-4170-aa14-a82faf70fc4f"}},
	  {"type":"query_complete","timestamp":"2026-07-25T23:29:06.243Z",
	   "data":{"model":"claude-opus-4-5",
	           "usage":{"inputTokens":%d,"outputTokens":%d,
	                    "cacheCreationInputTokens":%d,"cacheReadInputTokens":%d},
	           "result":"Hello from the agentd mock model proxy. Set ANTHROPIC_API_KEY for a real agent.",
	           "status":"completed",
	           "queryId":"1e74bdb0-5666-4c21-8c67-8e928355c84a",
	           "totalCostUsd":0.0004}}
	]`, uncached, out, cacheCreation, cacheRead)
}

func TestLivePG_GetSessionTokenSummary(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()
	sess := newLiveSession(t, s, customer, "u@x.com")

	// Two turns of the shape a composed job actually produces: a small uncached
	// remainder, a cache write on the first turn, and a large cache read on the
	// second. Input totals 100+400+0 + 7+0+1500 = 2007; a reader counting only
	// `inputTokens` would report 107 — 5% of the bill.
	for i, ev := range []string{
		capturedQueryEventsRow(100, 400, 0, 30),
		capturedQueryEventsRow(7, 0, 1500, 3),
	} {
		if err := s.UpsertQueryEvents(ctx, &QueryEvents{
			SessionID: sess.ID, QueryID: fmt.Sprintf("q%d", i), Events: JSONArray(ev),
		}); err != nil {
			t.Fatalf("seed qe %d: %v", i, err)
		}
	}

	sum, err := s.GetSessionTokenSummary(ctx, sess.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.InputTokens != 2007 || sum.OutputTokens != 33 {
		t.Fatalf("want 2007/33, got %d/%d — the reader is not matching the stored shape "+
			"(107 means it is counting only the uncached inputTokens and ignoring the cache components)",
			sum.InputTokens, sum.OutputTokens)
	}

	// A session with no query events sums to zero via COALESCE, not an error.
	empty := newLiveSession(t, s, customer, "u@x.com")
	sum, err = s.GetSessionTokenSummary(ctx, empty.ID)
	if err != nil || sum.InputTokens != 0 || sum.OutputTokens != 0 {
		t.Fatalf("empty session: %+v err=%v", sum, err)
	}
}

// TestLivePG_TokenSummaryToleratesTheProviderSpelling: the camelCase keys are
// produced by ONE line of ONE pluggable harness converting the Anthropic wire
// format; a harness forwarding its provider's usage object verbatim would spell
// them snake_case. Both are read, so swapping harnesses cannot silently re-zero
// the ledger. The invented flat-on-the-envelope shape is NOT read — it is
// asserted here to still sum to zero, so nobody "restores" it by accident.
func TestLivePG_TokenSummaryToleratesTheProviderSpelling(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "cust-"+uuid.New().String(), "u@x.com")

	seed := func(queryID, ev string) {
		t.Helper()
		if err := s.UpsertQueryEvents(ctx, &QueryEvents{
			SessionID: sess.ID, QueryID: queryID, Events: JSONArray(ev),
		}); err != nil {
			t.Fatalf("seed %s: %v", queryID, err)
		}
	}
	seed("provider-spelling", `[{"type":"query_complete","data":{"usage":`+
		`{"input_tokens":11,"output_tokens":4,`+
		`"cache_creation_input_tokens":30,"cache_read_input_tokens":900}}}]`)

	sum, err := s.GetSessionTokenSummary(ctx, sess.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.InputTokens != 941 || sum.OutputTokens != 4 {
		t.Fatalf("provider spelling: want 941/4, got %d/%d "+
			"(11 means the snake_case cache components are not being read)",
			sum.InputTokens, sum.OutputTokens)
	}

	// The shape TOK1 was about: never written by anything, deliberately unread.
	seed("invented-flat", `[{"type":"query_complete","input_tokens":9999,"output_tokens":9999}]`)
	// …and a row whose events are not even an array must not blow up the query
	// (jsonb_array_elements raises on a non-array; the reader guards it).
	seed("not-an-array", `{"type":"query_complete"}`)

	sum, err = s.GetSessionTokenSummary(ctx, sess.ID)
	if err != nil {
		t.Fatalf("summary after junk rows: %v", err)
	}
	if sum.InputTokens != 941 || sum.OutputTokens != 4 {
		t.Fatalf("junk rows changed the sum: got %d/%d, want 941/4", sum.InputTokens, sum.OutputTokens)
	}
}

// TestLivePG_TokenSummaryReadsHistoricalTwoKeyEnvelopes: every envelope stored
// before 2026-07-29 carries only `inputTokens`/`outputTokens`, because that is
// all the harness forwarded. Widening the reader to sum the cache components
// must not make those rows unreadable — the added terms COALESCE to 0, so a
// historical row reads exactly the number it always did.
//
// This is the backward-compatibility half of RD2. It is a live-Postgres test
// because the whole mechanism is jsonb COALESCE over absent keys, which no
// in-memory fake reproduces.
func TestLivePG_TokenSummaryReadsHistoricalTwoKeyEnvelopes(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "cust-"+uuid.New().String(), "u@x.com")

	seed := func(queryID, ev string) {
		t.Helper()
		if err := s.UpsertQueryEvents(ctx, &QueryEvents{
			SessionID: sess.ID, QueryID: queryID, Events: JSONArray(ev),
		}); err != nil {
			t.Fatalf("seed %s: %v", queryID, err)
		}
	}

	// A pre-RD2 row: the two-key shape, camelCase, exactly as it sits in the
	// live database today.
	seed("historical", `[{"type":"query_complete","data":{"usage":{"inputTokens":40,"outputTokens":12}}}]`)

	sum, err := s.GetSessionTokenSummary(ctx, sess.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.InputTokens != 40 || sum.OutputTokens != 12 {
		t.Fatalf("historical row became unreadable: got %d/%d, want 40/12", sum.InputTokens, sum.OutputTokens)
	}

	// A post-RD2 row alongside it: both are summed, mixed generations and all.
	seed("current", `[{"type":"query_complete","data":{"usage":`+
		`{"inputTokens":5,"outputTokens":8,"cacheCreationInputTokens":0,"cacheReadInputTokens":600}}}]`)

	sum, err = s.GetSessionTokenSummary(ctx, sess.ID)
	if err != nil {
		t.Fatalf("summary (mixed): %v", err)
	}
	if sum.InputTokens != 645 || sum.OutputTokens != 20 {
		t.Fatalf("mixed generations: got %d/%d, want 645/20", sum.InputTokens, sum.OutputTokens)
	}
}

func TestLivePG_SearchMessages(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()

	alice := newLiveSession(t, s, customer, "alice@x.com")
	alice.Title = "quarterly zebra report"
	if _, err := s.UpdateSession(ctx, alice); err != nil {
		t.Fatalf("title: %v", err)
	}
	bob := newLiveSession(t, s, customer, "Bob@X.com") // mixed-case on purpose
	other := newLiveSession(t, s, "cust-"+uuid.New().String(), "eve@other.com")

	if err := s.CreateMessages(ctx, []*Message{
		{SessionID: alice.ID, Role: "user", Content: "tell me about the zebra migration", SequenceNum: 1},
		{SessionID: alice.ID, Role: "assistant", Content: "zebras migrate across the Serengeti", SequenceNum: 2},
		{SessionID: bob.ID, Role: "user", Content: "zebra stripes and how they work", SequenceNum: 1},
		{SessionID: other.ID, Role: "user", Content: "zebra data for another customer", SequenceNum: 1},
	}); err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	// Customer scoping: only this customer's rows, ranked, both sessions found.
	res, err := s.SearchMessages(ctx, &MessageSearchQuery{Customer: customer, Query: "zebra"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("expected 3 in-customer hits, got %d: %+v", len(res), res)
	}
	for _, r := range res {
		if r.UserEmail == "eve@other.com" {
			t.Fatalf("cross-customer leak: %+v", r)
		}
		if r.Rank <= 0 {
			t.Fatalf("expected positive rank, got %+v", r)
		}
	}

	// Role + user filters compose.
	res, err = s.SearchMessages(ctx, &MessageSearchQuery{Customer: customer, Query: "zebra", Role: "assistant"})
	if err != nil || len(res) != 1 || res[0].Role != "assistant" {
		t.Fatalf("role filter: %+v err=%v", res, err)
	}
	res, err = s.SearchMessages(ctx, &MessageSearchQuery{Customer: customer, Query: "zebra", UserEmail: "alice@x.com"})
	if err != nil || len(res) != 2 {
		t.Fatalf("user filter: %+v err=%v", res, err)
	}

	// ExcludeUserEmails is case-insensitive: the SQL lowercases the column
	// side, so mixed-case exclusion input must still exclude Bob@X.com.
	res, err = s.SearchMessages(ctx, &MessageSearchQuery{
		Customer: customer, Query: "zebra", ExcludeUserEmails: []string{"Bob@X.com"},
	})
	if err != nil {
		t.Fatalf("exclude search: %v", err)
	}
	for _, r := range res {
		if strings.EqualFold(r.UserEmail, "bob@x.com") {
			t.Fatalf("excluded user leaked through mixed-case exclusion: %+v", r)
		}
	}
	if len(res) != 2 {
		t.Fatalf("expected alice's 2 rows after excluding bob, got %d", len(res))
	}

	// No hits → empty non-nil slice.
	res, err = s.SearchMessages(ctx, &MessageSearchQuery{Customer: customer, Query: "xylophonectomy"})
	if err != nil || res == nil || len(res) != 0 {
		t.Fatalf("no-hit search: %#v err=%v", res, err)
	}
}

// TestLivePG_ListQueryEventsFlatForQuery: the read half of the reconnect merge
// (D2 / doc 22 RD6). A reconnect that re-attaches to a turn nobody in the
// process owns any more has to APPEND to that turn's row, so it must first read
// exactly that turn back — not the whole session, which would splice one turn's
// tail onto another turn's words.
func TestLivePG_ListQueryEventsFlatForQuery(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "cust-"+uuid.New().String(), "u@x.com")

	first := []events.Envelope{
		{Type: events.UserMessage, Data: map[string]any{"content": "first question"}},
		{Type: events.ContentDelta, Data: map[string]any{"delta": "first answer"}},
	}
	second := []events.Envelope{
		{Type: events.UserMessage, Data: map[string]any{"content": "second question"}},
	}
	if err := s.PersistQueryEventsFlat(ctx, sess.ID, "q1", first, "first"); err != nil {
		t.Fatalf("persist q1: %v", err)
	}
	if err := s.PersistQueryEventsFlat(ctx, sess.ID, "q2", second, "second"); err != nil {
		t.Fatalf("persist q2: %v", err)
	}

	got, err := s.ListQueryEventsFlatForQuery(ctx, sess.ID, "q1")
	if err != nil {
		t.Fatalf("read q1: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("q1 read back %d events, want 2 (a read that leaks q2 would splice two turns together): %+v", len(got), got)
	}
	if c, _ := got[0].Data["content"].(string); c != "first question" {
		t.Fatalf("q1[0] = %+v, want the first question", got[0])
	}

	// A turn nothing has written yet is empty, not an error — the ordinary case
	// on the FIRST reconnect to a turn.
	got, err = s.ListQueryEventsFlatForQuery(ctx, sess.ID, "q-never-written")
	if err != nil || len(got) != 0 {
		t.Fatalf("unwritten turn: got %d events, err %v — want (0, nil)", len(got), err)
	}
}
