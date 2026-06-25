package agentdb

import (
	"strings"
	"testing"
)

// Guards the bug where an empty exclude list produced `... <> ALL(?)` bound to an
// empty array, which Postgres/GORM renders as `ALL(NULL)` — NULL, not true — so
// every conversation search returned zero rows.
func TestBuildConversationSearchSQL_OmitsExcludeWhenEmpty(t *testing.T) {
	sql, args := buildConversationSearchSQL(&ConversationSearchQuery{Customer: "c", Query: "mango"})
	if strings.Contains(sql, "ALL(") {
		t.Fatalf("empty exclude must not emit ALL(?) (renders as ALL(NULL) and filters everything):\n%s", sql)
	}
	for _, a := range args {
		if _, isSlice := a.([]string); isSlice {
			t.Fatalf("empty exclude must not pass a slice arg, got %#v", args)
		}
	}

	sqlEx, _ := buildConversationSearchSQL(&ConversationSearchQuery{Customer: "c", Query: "mango", ExcludeUserEmails: []string{"bot@x.com"}})
	if !strings.Contains(sqlEx, "ALL(") {
		t.Fatalf("non-empty exclude must emit the ALL(?) predicate")
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
