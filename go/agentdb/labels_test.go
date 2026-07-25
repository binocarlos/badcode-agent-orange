package agentdb

import (
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Label validation (§7.1: ≤63 chars, ≤32 labels, no controlled vocabulary).
// ---------------------------------------------------------------------------

func TestMemoriesLabelValidation(t *testing.T) {
	long := strings.Repeat("a", 64)
	ok63 := strings.Repeat("a", 63)

	tests := []struct {
		name    string
		labels  map[string]string
		wantErr string // substring; "" = must pass
	}{
		{name: "nil is fine", labels: nil},
		{name: "empty is fine", labels: map[string]string{}},
		{name: "typical set", labels: map[string]string{
			"kind": "conversation-summary", "worker": "email-answerer", "thread": "cust-4711",
		}},
		{name: "dots and underscores", labels: map[string]string{"app.kubernetes_io": "v1.2.3"}},
		{name: "empty value allowed", labels: map[string]string{"archived": ""}},
		{name: "63 chars is the edge", labels: map[string]string{ok63: ok63}},
		{name: "key too long", labels: map[string]string{long: "v"}, wantErr: "max 63"},
		{name: "value too long", labels: map[string]string{"k": long}, wantErr: "max 63"},
		{name: "empty key", labels: map[string]string{"": "v"}, wantErr: "must not be empty"},
		{name: "key with space", labels: map[string]string{"my key": "v"}, wantErr: "invalid"},
		{name: "key with comma breaks selectors", labels: map[string]string{"a,b": "v"}, wantErr: "invalid"},
		{name: "value with equals breaks selectors", labels: map[string]string{"k": "a=b"}, wantErr: "invalid"},
		{name: "value with paren breaks selectors", labels: map[string]string{"k": "a(b)"}, wantErr: "invalid"},
		{name: "key may not start with dash", labels: map[string]string{"-k": "v"}, wantErr: "invalid"},
		{name: "key may not end with dot", labels: map[string]string{"k.": "v"}, wantErr: "invalid"},
		{name: "no slash prefix segment", labels: map[string]string{"acme.io/kind": "v"}, wantErr: "invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLabels(tc.labels)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}

	// Cardinality: 32 is allowed, 33 is not.
	at32 := map[string]string{}
	for i := 0; i < MaxLabelsPerObject; i++ {
		at32["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	if len(at32) != MaxLabelsPerObject {
		t.Fatalf("test bug: built %d labels", len(at32))
	}
	if err := ValidateLabels(at32); err != nil {
		t.Fatalf("32 labels must be valid: %v", err)
	}
	at32["overflow"] = "v"
	if err := ValidateLabels(at32); err == nil || !strings.Contains(err.Error(), "too many labels") {
		t.Fatalf("33 labels must fail, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Selector grammar (§7.2).
// ---------------------------------------------------------------------------

func TestSelectorParseGrammar(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []LabelRequirement
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"equality", "worker=email-answerer", []LabelRequirement{
			{Key: "worker", Op: LabelOpEquals, Values: []string{"email-answerer"}}}},
		{"double equals is equality", "worker==email-answerer", []LabelRequirement{
			{Key: "worker", Op: LabelOpEquals, Values: []string{"email-answerer"}}}},
		{"inequality", "kind!=raw-transcript", []LabelRequirement{
			{Key: "kind", Op: LabelOpNotEquals, Values: []string{"raw-transcript"}}}},
		{"empty value", "archived=", []LabelRequirement{
			{Key: "archived", Op: LabelOpEquals, Values: []string{""}}}},
		{"in set", "kind in (summary, lesson)", []LabelRequirement{
			{Key: "kind", Op: LabelOpIn, Values: []string{"summary", "lesson"}}}},
		{"in set tight", "kind in(summary,lesson)", []LabelRequirement{
			{Key: "kind", Op: LabelOpIn, Values: []string{"summary", "lesson"}}}},
		{"notin set", "thread notin (spam)", []LabelRequirement{
			{Key: "thread", Op: LabelOpNotIn, Values: []string{"spam"}}}},
		{"exists keyword", "exists thread", []LabelRequirement{
			{Key: "thread", Op: LabelOpExists}}},
		{"bare key is exists", "thread", []LabelRequirement{
			{Key: "thread", Op: LabelOpExists}}},
		{"not exists", "!archived", []LabelRequirement{
			{Key: "archived", Op: LabelOpNotExists}}},
		{"conjunction", "worker=email-answerer,kind=summary", []LabelRequirement{
			{Key: "worker", Op: LabelOpEquals, Values: []string{"email-answerer"}},
			{Key: "kind", Op: LabelOpEquals, Values: []string{"summary"}}}},
		{"conjunction with set (commas inside parens are not separators)",
			"kind in (summary, lesson),worker=x,!archived", []LabelRequirement{
				{Key: "kind", Op: LabelOpIn, Values: []string{"summary", "lesson"}},
				{Key: "worker", Op: LabelOpEquals, Values: []string{"x"}},
				{Key: "archived", Op: LabelOpNotExists}}},
		{"loose whitespace", "  worker = x ,  kind != y  ", []LabelRequirement{
			{Key: "worker", Op: LabelOpEquals, Values: []string{"x"}},
			{Key: "kind", Op: LabelOpNotEquals, Values: []string{"y"}}}},
		{"trailing comma is not a term", "worker=x,", []LabelRequirement{
			{Key: "worker", Op: LabelOpEquals, Values: []string{"x"}}}},
		{"repeated key is allowed (ANDed)", "kind!=a,kind!=b", []LabelRequirement{
			{Key: "kind", Op: LabelOpNotEquals, Values: []string{"a"}},
			{Key: "kind", Op: LabelOpNotEquals, Values: []string{"b"}}}},
		{"key literally named exists", "exists=1", []LabelRequirement{
			{Key: "exists", Op: LabelOpEquals, Values: []string{"1"}}}},
		{"key with the exists prefix but no space", "existsential", []LabelRequirement{
			{Key: "existsential", Op: LabelOpExists}}},
		{"bare 'exists' is itself a key", "exists", []LabelRequirement{
			{Key: "exists", Op: LabelOpExists}}},
		{"name convention selector", "name=label-registry", []LabelRequirement{
			{Key: "name", Op: LabelOpEquals, Values: []string{"label-registry"}}}},
		{"briefing default selector", "kind=rolling-summary, worker=archivist", []LabelRequirement{
			{Key: "kind", Op: LabelOpEquals, Values: []string{"rolling-summary"}},
			{Key: "worker", Op: LabelOpEquals, Values: []string{"archivist"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := ParseLabelSelector(tc.in)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.in, err)
			}
			if !reflect.DeepEqual(sel.Requirements, tc.want) {
				t.Fatalf("parse %q:\n got %+v\nwant %+v", tc.in, sel.Requirements, tc.want)
			}
		})
	}
}

func TestSelectorParseErrors(t *testing.T) {
	tests := []struct{ name, in, wantErr string }{
		{"unbalanced open paren", "kind in (a", "unbalanced"},
		{"unbalanced close paren", "kind in a)", "unbalanced"},
		{"empty set", "kind in ()", "empty value in set"},
		{"empty value in set", "kind in (a,,b)", "empty value in set"},
		{"bad key", "my key", "invalid"},
		{"bad value", "kind=a b", "invalid"},
		{"bang with no key", "!", "must not be empty"},
		{"equals with no key", "=v", "must not be empty"},
		{"exists with an invalid key", "exists my key", "invalid"},
		{"no OR support", "kind=a|kind=b", "invalid"},
		{"key too long", strings.Repeat("k", 64) + "=v", "max 63"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseLabelSelector(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("parse %q: want error containing %q, got %v", tc.in, tc.wantErr, err)
			}
		})
	}
}

// The canonical rendering must re-parse to the same thing — I1 stores selector
// strings, so parse ∘ String has to be a fixed point.
func TestSelectorStringRoundTrip(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"worker=x", "worker=x"},
		{"worker==x", "worker=x"},
		{"kind!=y", "kind!=y"},
		{"kind in (a, b)", "kind in (a,b)"},
		{"kind notin (a)", "kind notin (a)"},
		{"exists thread", "thread"},
		{"thread", "thread"},
		{"!archived", "!archived"},
		{" a = 1 , b in ( 2 , 3 ) , !c ", "a=1,b in (2,3),!c"},
	}
	for _, tc := range tests {
		sel, err := ParseLabelSelector(tc.in)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.in, err)
		}
		if got := sel.String(); got != tc.want {
			t.Fatalf("String(%q) = %q, want %q", tc.in, got, tc.want)
		}
		again, err := ParseLabelSelector(sel.String())
		if err != nil {
			t.Fatalf("reparse %q: %v", sel.String(), err)
		}
		if !reflect.DeepEqual(again.Requirements, sel.Requirements) {
			t.Fatalf("round trip changed %q: %+v vs %+v", tc.in, again.Requirements, sel.Requirements)
		}
	}
}

// Matches is the in-memory twin of the SQL translation; the live tests prove
// the two agree on real rows.
func TestSelectorMatches(t *testing.T) {
	labels := map[string]string{"kind": "summary", "worker": "archivist", "thread": "cust-4711"}
	tests := []struct {
		sel  string
		want bool
	}{
		{"", true},
		{"kind=summary", true},
		{"kind=lesson", false},
		{"kind!=lesson", true},
		{"kind!=summary", false},
		{"archived!=true", true}, // K8s: a missing key satisfies !=
		{"kind in (summary, lesson)", true},
		{"kind in (lesson)", false},
		{"kind notin (lesson)", true},
		{"kind notin (summary)", false},
		{"archived notin (true)", true}, // missing key satisfies notin
		{"thread", true},
		{"exists thread", true},
		{"exists archived", false},
		{"!archived", true},
		{"!thread", false},
		{"kind=summary,worker=archivist,!archived", true},
		{"kind=summary,worker=other", false},
	}
	for _, tc := range tests {
		sel, err := ParseLabelSelector(tc.sel)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.sel, err)
		}
		if got := sel.Matches(labels); got != tc.want {
			t.Fatalf("Matches(%q) = %v, want %v", tc.sel, got, tc.want)
		}
	}
}

// The jsonb translation is pinned exactly: it is reused verbatim by the image
// catalogue (I1), and the index-friendly `@>` shape is a deliberate choice.
func TestSelectorSQLTranslation(t *testing.T) {
	tests := []struct {
		name     string
		sel      string
		wantSQL  string
		wantArgs []any
	}{
		{"empty", "", "", nil},
		{"equals", "kind=summary", "labels @> ?::jsonb", []any{`{"kind":"summary"}`}},
		{"not equals", "kind!=summary", "NOT (labels @> ?::jsonb)", []any{`{"kind":"summary"}`}},
		{"in", "kind in (a,b)", "(labels @> ?::jsonb OR labels @> ?::jsonb)",
			[]any{`{"kind":"a"}`, `{"kind":"b"}`}},
		{"notin", "kind notin (a,b)", "NOT (labels @> ?::jsonb OR labels @> ?::jsonb)",
			[]any{`{"kind":"a"}`, `{"kind":"b"}`}},
		// jsonb_exists(), not the `?` operator, which would collide with the
		// GORM/database-sql placeholder.
		{"exists", "exists thread", "jsonb_exists(labels, ?)", []any{"thread"}},
		{"not exists", "!archived", "NOT jsonb_exists(labels, ?)", []any{"archived"}},
		{"conjunction", "kind=summary,!archived",
			"labels @> ?::jsonb AND NOT jsonb_exists(labels, ?)",
			[]any{`{"kind":"summary"}`, "archived"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := LabelSelectorSQL(tc.sel, "labels")
			if err != nil {
				t.Fatalf("translate %q: %v", tc.sel, err)
			}
			if sql != tc.wantSQL {
				t.Fatalf("SQL(%q) = %q, want %q", tc.sel, sql, tc.wantSQL)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Fatalf("args(%q) = %#v, want %#v", tc.sel, args, tc.wantArgs)
			}
		})
	}

	// Column names are qualified-identifier only — no SQL smuggling.
	if _, _, err := LabelSelectorSQL("kind=a", "labels; DROP TABLE memories"); err == nil {
		t.Fatalf("expected an invalid-column error")
	}
	sql, _, err := LabelSelectorSQL("kind=a", "m.labels")
	if err != nil || sql != "m.labels @> ?::jsonb" {
		t.Fatalf("qualified column: %q err=%v", sql, err)
	}

	// A bad selector propagates its parse error rather than translating.
	if _, _, err := LabelSelectorSQL("kind in (", "labels"); err == nil {
		t.Fatalf("expected a parse error through LabelSelectorSQL")
	}
}

func TestSelectorLabelSetScanValue(t *testing.T) {
	var l LabelSet
	if err := l.Scan(nil); err != nil || len(l) != 0 {
		t.Fatalf("nil scan: %v %v", l, err)
	}
	if err := l.Scan([]byte(`{"a":"b"}`)); err != nil || l["a"] != "b" {
		t.Fatalf("bytes scan: %v %v", l, err)
	}
	if err := l.Scan(`{"c":"d"}`); err != nil || l["c"] != "d" {
		t.Fatalf("string scan: %v %v", l, err)
	}
	if err := l.Scan(42); err == nil {
		t.Fatalf("expected an unsupported-type error")
	}
	v, err := LabelSet(nil).Value()
	if err != nil || v != "{}" {
		t.Fatalf("nil Value: %v %v", v, err)
	}
	v, err = LabelSet{"a": "b"}.Value()
	if err != nil || v != `{"a":"b"}` {
		t.Fatalf("Value: %v %v", v, err)
	}
}
