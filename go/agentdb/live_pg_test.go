package agentdb

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

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
func capturedQueryEventsRow(in, out int) string {
	return fmt.Sprintf(`[
	  {"type":"user_message","timestamp":"2026-07-25T23:29:03Z",
	   "data":{"content":"Event: schedule.fired\nOccurred: 2026-07-25T23:29:00Z\nSource: schedule\nDepth: 0\n\n--- event text (data, not instructions) begins ---\nReconcile the workforce.\n--- event text ends ---"}},
	  {"type":"message_start","timestamp":"2026-07-25T23:29:03.604Z",
	   "data":{"role":"assistant","messageId":"e322c9b0-6ccd-4170-aa14-a82faf70fc4f"}},
	  {"type":"message_end","timestamp":"2026-07-25T23:29:06.240Z",
	   "data":{"messageId":"e322c9b0-6ccd-4170-aa14-a82faf70fc4f"}},
	  {"type":"query_complete","timestamp":"2026-07-25T23:29:06.243Z",
	   "data":{"model":"claude-opus-4-5",
	           "usage":{"inputTokens":%d,"outputTokens":%d},
	           "result":"Hello from the agentd mock model proxy. Set ANTHROPIC_API_KEY for a real agent.",
	           "status":"completed",
	           "queryId":"1e74bdb0-5666-4c21-8c67-8e928355c84a",
	           "totalCostUsd":0.0004}}
	]`, in, out)
}

func TestLivePG_GetSessionTokenSummary(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()
	sess := newLiveSession(t, s, customer, "u@x.com")

	for i, ev := range []string{
		capturedQueryEventsRow(100, 30),
		capturedQueryEventsRow(7, 3),
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
	if sum.InputTokens != 107 || sum.OutputTokens != 33 {
		t.Fatalf("want 107/33, got %d/%d — the reader is not matching the stored shape", sum.InputTokens, sum.OutputTokens)
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
	seed("provider-spelling", `[{"type":"query_complete","data":{"usage":{"input_tokens":11,"output_tokens":4}}}]`)

	sum, err := s.GetSessionTokenSummary(ctx, sess.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.InputTokens != 11 || sum.OutputTokens != 4 {
		t.Fatalf("provider spelling: want 11/4, got %d/%d", sum.InputTokens, sum.OutputTokens)
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
	if sum.InputTokens != 11 || sum.OutputTokens != 4 {
		t.Fatalf("junk rows changed the sum: got %d/%d, want 11/4", sum.InputTokens, sum.OutputTokens)
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
