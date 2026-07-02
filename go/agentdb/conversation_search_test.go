package agentdb

import (
	"strings"
	"testing"
)

// Guards two exclusion-predicate bugs: (1) an empty exclude list must omit the
// predicate entirely (an empty exclusion list is invalid SQL / used to render as
// ALL(NULL) and filter every row); (2) a non-empty list must use NOT IN (?) —
// GORM expands []string args into value lists, never Postgres arrays, so the
// former `<> ALL(?)` failed on live Postgres with "malformed array literal"
// (see TestLivePG_ConversationIndexUpsertAndSearch).
func TestBuildConversationSearchSQL_ExcludePredicateForm(t *testing.T) {
	sql, args := buildConversationSearchSQL(&ConversationSearchQuery{Customer: "c", Query: "mango"})
	if strings.Contains(sql, "NOT IN") || strings.Contains(sql, "ALL(") {
		t.Fatalf("empty exclude must not emit an exclusion predicate:\n%s", sql)
	}
	for _, a := range args {
		if _, isSlice := a.([]string); isSlice {
			t.Fatalf("empty exclude must not pass a slice arg, got %#v", args)
		}
	}

	sqlEx, _ := buildConversationSearchSQL(&ConversationSearchQuery{Customer: "c", Query: "mango", ExcludeUserEmails: []string{"bot@x.com"}})
	if !strings.Contains(sqlEx, "LOWER(user_email) NOT IN (?)") {
		t.Fatalf("non-empty exclude must emit the NOT IN (?) predicate:\n%s", sqlEx)
	}
	if strings.Contains(sqlEx, "ALL(") {
		t.Fatalf("ALL(?) cannot be bound by GORM placeholder expansion:\n%s", sqlEx)
	}
}

// Every positional placeholder must have exactly one bound arg, across all four
// branches (keyword-only / hybrid x with / without exclude). A mismatch is the
// class of bug that silently breaks the query.
func TestBuildConversationSearchSQL_PlaceholderArgParity(t *testing.T) {
	cases := map[string]*ConversationSearchQuery{
		"keyword-only, no exclude":  {Customer: "c", Query: "mango"},
		"keyword-only, exclude":     {Customer: "c", Query: "mango", ExcludeUserEmails: []string{"a@x.com"}},
		"hybrid, no exclude":        {Customer: "c", Query: "mango", QueryEmbedding: []float32{0.1, 0.2}},
		"hybrid, exclude":           {Customer: "c", Query: "mango", QueryEmbedding: []float32{0.1, 0.2}, ExcludeUserEmails: []string{"a@x.com"}},
	}
	for name, q := range cases {
		sql, args := buildConversationSearchSQL(q)
		if got := strings.Count(sql, "?"); got != len(args) {
			t.Fatalf("%s: %d placeholders but %d args", name, got, len(args))
		}
	}
}
