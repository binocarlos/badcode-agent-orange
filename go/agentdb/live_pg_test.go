package agentdb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// openLivePG opens a Store against a real Postgres (with pgvector) when
// AGENTKIT_TEST_POSTGRES_URL is set, skipping otherwise — the same env-gated
// pattern as orchestrator/pgstore/migration_pg_test.go. These tests cover the
// Postgres-only SQL that cannot honestly run on sqlite: the numbered
// migrations, jsonb '->>' + '::bigint' casts (GetSessionTokenSummary),
// tsvector search (SearchMessages), and the pgvector/tsvector conversation
// index (UpsertConversationIndex / SearchConversations).
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

func TestLivePG_GetSessionTokenSummary(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()
	sess := newLiveSession(t, s, customer, "u@x.com")

	for i, ev := range []string{
		`[{"type":"query_complete","input_tokens":100,"output_tokens":30}]`,
		`[{"type":"query_complete","input_tokens":7,"output_tokens":3}]`,
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
		t.Fatalf("want 107/33, got %d/%d", sum.InputTokens, sum.OutputTokens)
	}

	// A session with no query events sums to zero via COALESCE, not an error.
	empty := newLiveSession(t, s, customer, "u@x.com")
	sum, err = s.GetSessionTokenSummary(ctx, empty.ID)
	if err != nil || sum.InputTokens != 0 || sum.OutputTokens != 0 {
		t.Fatalf("empty session: %+v err=%v", sum, err)
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

func TestLivePG_ConversationIndexUpsertAndSearch(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()

	sess := newLiveSession(t, s, customer, "alice@x.com")
	emb := make([]float32, 1536)
	emb[0], emb[1] = 0.5, 0.25

	ci := &ConversationIndex{
		SessionID: sess.ID, Customer: customer, Job: "job1", UserEmail: "alice@x.com",
		WorkflowID: "chat", Title: "zebra chat", Summary: "a talk about zebras",
		MessageCount: 2, LastActivityAt: 1000, IndexedAt: 1001, SourceHash: "h1",
	}
	if err := s.UpsertConversationIndex(ctx, ci, emb, "zebras migrate across the plains"); err != nil {
		t.Fatalf("upsert with embedding: %v", err)
	}
	// Conflict path: same session, no embedding this time (NULL), new summary.
	ci.Summary = "updated zebra summary"
	if err := s.UpsertConversationIndex(ctx, ci, nil, "zebras migrate far"); err != nil {
		t.Fatalf("upsert conflict/no-embedding: %v", err)
	}
	meta, err := s.GetConversationIndexMeta(ctx, sess.ID)
	if err != nil || meta == nil || meta.SourceHash != "h1" {
		t.Fatalf("meta after upsert: %+v err=%v", meta, err)
	}

	// Keyword-only search (no query embedding).
	res, err := s.SearchConversations(ctx, &ConversationSearchQuery{
		Customer: customer, Query: "zebras",
	})
	if err != nil {
		t.Fatalf("keyword search: %v", err)
	}
	if len(res) != 1 || res[0].SessionID != sess.ID || res[0].MatchType != "keyword_only" {
		t.Fatalf("keyword search wrong: %+v", res)
	}
	if res[0].Summary != "updated zebra summary" {
		t.Fatalf("conflict upsert did not replace summary: %+v", res[0])
	}

	// Hybrid RRF search with an embedding (embedding was NULLed by the second
	// upsert, so restore it first).
	if err := s.UpsertConversationIndex(ctx, ci, emb, "zebras migrate far"); err != nil {
		t.Fatalf("restore embedding: %v", err)
	}
	res, err = s.SearchConversations(ctx, &ConversationSearchQuery{
		Customer: customer, Query: "zebras", QueryEmbedding: emb,
		ExcludeUserEmails: []string{"Nobody@Else.com"},
	})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(res) != 1 || res[0].SessionID != sess.ID || res[0].MatchType != "keyword+semantic" {
		t.Fatalf("hybrid search wrong: %+v", res)	}

	// Exclusions are case-insensitive here too (lowerAll on the arg side).
	res, err = s.SearchConversations(ctx, &ConversationSearchQuery{
		Customer: customer, Query: "zebras", ExcludeUserEmails: []string{"ALICE@X.COM"},
	})
	if err != nil || len(res) != 0 {
		t.Fatalf("exclusion should remove the only row: %+v err=%v", res, err)
	}
}

// TestLivePG_UpsertConversationIndex_TruncationKeepsValidUTF8 drives the
// transcript through the 900KB tsvector guard with a multibyte character
// straddling the cut point. Postgres rejects invalid UTF-8 outright, so the
// truncation must land on a rune boundary.
func TestLivePG_UpsertConversationIndex_TruncationKeepsValidUTF8(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cust-" + uuid.New().String()
	sess := newLiveSession(t, s, customer, "alice@x.com")

	// 1 ASCII byte + 2-byte runes: byte index 900_000 falls mid-rune.
	transcript := "a" + strings.Repeat("é", 460_000)
	ci := &ConversationIndex{
		SessionID: sess.ID, Customer: customer, Title: "big", Summary: "big transcript",
	}
	if err := s.UpsertConversationIndex(ctx, ci, nil, transcript); err != nil {
		t.Fatalf("upsert with mid-rune truncation boundary: %v", err)
	}
}
