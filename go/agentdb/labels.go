package agentdb

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Labels
//
// Flat map[string]string metadata, Kubernetes-style, stored as jsonb. This file
// is deliberately NOT memory-specific: the same validator, selector parser and
// jsonb translator serve any labeled table (memories today, the named/versioned
// image catalogue next). Nothing here knows what a memory is.
// ---------------------------------------------------------------------------

// Label limits (K8s limits — familiar, and enough; spec §7.1).
const (
	MaxLabelKeyLen     = 63
	MaxLabelValueLen   = 63
	MaxLabelsPerObject = 32
)

// labelKeyRe / labelValueRe are the Kubernetes label charset (minus the
// optional DNS prefix segment: keys here are a single name segment, no "/").
// The charset is not decoration — it is what makes labels round-trip through
// the selector grammar unambiguously (no commas, parens, "=", "!" or spaces).
var (
	labelKeyRe   = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	labelValueRe = labelKeyRe
	// sqlIdentRe guards the column name handed to the jsonb translator so a
	// caller can never smuggle SQL through it.
	sqlIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
)

// LabelSet is a flat map[string]string persisted as jsonb.
type LabelSet map[string]string

func (l LabelSet) Value() (driver.Value, error) {
	if l == nil {
		return "{}", nil
	}
	b, err := json.Marshal(map[string]string(l))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (l *LabelSet) Scan(value any) error {
	if value == nil {
		*l = LabelSet{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported type %T for LabelSet", value)
	}
	decoded := map[string]string{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("failed to unmarshal LabelSet: %w", err)
	}
	*l = decoded
	return nil
}

// ValidateLabelKey checks a single label key.
func ValidateLabelKey(k string) error {
	if k == "" {
		return fmt.Errorf("label key must not be empty")
	}
	if len(k) > MaxLabelKeyLen {
		return fmt.Errorf("label key %q is %d chars, max %d", k, len(k), MaxLabelKeyLen)
	}
	if !labelKeyRe.MatchString(k) {
		return fmt.Errorf("label key %q is invalid: must be alphanumeric, optionally containing '-', '_' or '.', and start and end alphanumeric", k)
	}
	return nil
}

// ValidateLabelValue checks a single label value. The empty value is legal
// (K8s allows it) — everything else follows the key charset.
func ValidateLabelValue(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > MaxLabelValueLen {
		return fmt.Errorf("label value %q is %d chars, max %d", v, len(v), MaxLabelValueLen)
	}
	if !labelValueRe.MatchString(v) {
		return fmt.Errorf("label value %q is invalid: must be alphanumeric, optionally containing '-', '_' or '.', and start and end alphanumeric", v)
	}
	return nil
}

// ValidateLabels checks a whole label set: cardinality, keys and values.
// Nil / empty is valid — labels are optional.
func ValidateLabels(labels map[string]string) error {
	if len(labels) > MaxLabelsPerObject {
		return fmt.Errorf("too many labels: %d, max %d", len(labels), MaxLabelsPerObject)
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error for a given bad set
	for _, k := range keys {
		if err := ValidateLabelKey(k); err != nil {
			return err
		}
		if err := ValidateLabelValue(labels[k]); err != nil {
			return fmt.Errorf("label %q: %w", k, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Selectors (spec §7.2 — Kubernetes selector semantics, conjunction only)
// ---------------------------------------------------------------------------

// LabelOperator is the comparison a single requirement performs.
type LabelOperator string

const (
	LabelOpEquals    LabelOperator = "="
	LabelOpNotEquals LabelOperator = "!="
	LabelOpIn        LabelOperator = "in"
	LabelOpNotIn     LabelOperator = "notin"
	LabelOpExists    LabelOperator = "exists"
	LabelOpNotExists LabelOperator = "!"
)

// LabelRequirement is one term of a selector.
type LabelRequirement struct {
	Key    string
	Op     LabelOperator
	Values []string // one for =/!=, one-or-more for in/notin, none for exists/!
}

// LabelSelector is a conjunction (AND) of requirements. There is deliberately
// no OR and no nesting: a caller that needs OR runs two searches (§7.2).
type LabelSelector struct {
	Requirements []LabelRequirement
}

// Empty reports whether the selector filters nothing.
func (sel *LabelSelector) Empty() bool { return sel == nil || len(sel.Requirements) == 0 }

// ParseLabelSelector parses Kubernetes-style selector text. The empty string
// (or whitespace) parses to an empty selector that matches everything.
//
//	worker=email-answerer,kind!=raw-transcript
//	kind in (summary, lesson)
//	thread notin (spam)
//	exists thread          (or the bare form: thread)
//	!archived
func ParseLabelSelector(s string) (*LabelSelector, error) {
	sel := &LabelSelector{}
	terms, err := splitTerms(s)
	if err != nil {
		return nil, err
	}
	for _, term := range terms {
		req, err := parseRequirement(term)
		if err != nil {
			return nil, err
		}
		sel.Requirements = append(sel.Requirements, req)
	}
	return sel, nil
}

// splitTerms splits on top-level commas — commas inside a set's parentheses
// belong to the set, not to the conjunction.
func splitTerms(s string) ([]string, error) {
	var (
		terms []string
		buf   strings.Builder
		depth int
	)
	flush := func() {
		t := strings.TrimSpace(buf.String())
		buf.Reset()
		if t != "" {
			terms = append(terms, t)
		}
	}
	for _, r := range s {
		switch r {
		case '(':
			depth++
			buf.WriteRune(r)
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("selector %q: unbalanced ')'", s)
			}
			buf.WriteRune(r)
		case ',':
			if depth == 0 {
				flush()
				continue
			}
			buf.WriteRune(r)
		default:
			buf.WriteRune(r)
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("selector %q: unbalanced '('", s)
	}
	flush()
	return terms, nil
}

var setTermRe = regexp.MustCompile(`^(\S+)\s+(in|notin)\s*\((.*)\)$`)

func parseRequirement(term string) (LabelRequirement, error) {
	var zero LabelRequirement

	// !key — must not exist.
	if strings.HasPrefix(term, "!") {
		key := strings.TrimSpace(strings.TrimPrefix(term, "!"))
		if err := ValidateLabelKey(key); err != nil {
			return zero, fmt.Errorf("selector term %q: %w", term, err)
		}
		return LabelRequirement{Key: key, Op: LabelOpNotExists}, nil
	}

	// exists key — must exist (the spec's spelling of the bare-key form).
	if rest, ok := cutWord(term, "exists"); ok {
		key := strings.TrimSpace(rest)
		if err := ValidateLabelKey(key); err != nil {
			return zero, fmt.Errorf("selector term %q: %w", term, err)
		}
		return LabelRequirement{Key: key, Op: LabelOpExists}, nil
	}

	// key in (a, b) / key notin (a)
	if m := setTermRe.FindStringSubmatch(term); m != nil {
		key := m[1]
		if err := ValidateLabelKey(key); err != nil {
			return zero, fmt.Errorf("selector term %q: %w", term, err)
		}
		op := LabelOpIn
		if m[2] == "notin" {
			op = LabelOpNotIn
		}
		var values []string
		for _, raw := range strings.Split(m[3], ",") {
			v := strings.TrimSpace(raw)
			if v == "" {
				return zero, fmt.Errorf("selector term %q: empty value in set", term)
			}
			if err := ValidateLabelValue(v); err != nil {
				return zero, fmt.Errorf("selector term %q: %w", term, err)
			}
			values = append(values, v)
		}
		if len(values) == 0 {
			return zero, fmt.Errorf("selector term %q: %s requires at least one value", term, m[2])
		}
		return LabelRequirement{Key: key, Op: op, Values: values}, nil
	}

	// key!=value / key==value / key=value
	for _, op := range []struct {
		token string
		op    LabelOperator
	}{{"!=", LabelOpNotEquals}, {"==", LabelOpEquals}, {"=", LabelOpEquals}} {
		if i := strings.Index(term, op.token); i >= 0 {
			key := strings.TrimSpace(term[:i])
			val := strings.TrimSpace(term[i+len(op.token):])
			if err := ValidateLabelKey(key); err != nil {
				return zero, fmt.Errorf("selector term %q: %w", term, err)
			}
			if err := ValidateLabelValue(val); err != nil {
				return zero, fmt.Errorf("selector term %q: %w", term, err)
			}
			return LabelRequirement{Key: key, Op: op.op, Values: []string{val}}, nil
		}
	}

	// Bare key — must exist.
	if err := ValidateLabelKey(term); err != nil {
		return zero, fmt.Errorf("selector term %q is not a valid requirement: %w", term, err)
	}
	return LabelRequirement{Key: term, Op: LabelOpExists}, nil
}

// cutWord splits "word rest" when term starts with the given word followed by
// whitespace. Used for the "exists key" spelling.
func cutWord(term, word string) (string, bool) {
	if !strings.HasPrefix(term, word) {
		return "", false
	}
	rest := term[len(word):]
	if rest == "" || !isSpace(rest[0]) {
		return "", false
	}
	return rest, true
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// String renders the selector back to canonical text (parse ∘ String is a
// fixed point).
func (sel *LabelSelector) String() string {
	if sel.Empty() {
		return ""
	}
	parts := make([]string, 0, len(sel.Requirements))
	for _, r := range sel.Requirements {
		switch r.Op {
		case LabelOpExists:
			parts = append(parts, r.Key)
		case LabelOpNotExists:
			parts = append(parts, "!"+r.Key)
		case LabelOpIn, LabelOpNotIn:
			parts = append(parts, fmt.Sprintf("%s %s (%s)", r.Key, r.Op, strings.Join(r.Values, ",")))
		default:
			parts = append(parts, r.Key+string(r.Op)+r.Values[0])
		}
	}
	return strings.Join(parts, ",")
}

// Matches evaluates the selector in memory. Kubernetes semantics: a "!=" or
// "notin" requirement is satisfied by a missing key.
func (sel *LabelSelector) Matches(labels map[string]string) bool {
	if sel.Empty() {
		return true
	}
	for _, r := range sel.Requirements {
		v, ok := labels[r.Key]
		switch r.Op {
		case LabelOpExists:
			if !ok {
				return false
			}
		case LabelOpNotExists:
			if ok {
				return false
			}
		case LabelOpEquals:
			if !ok || v != r.Values[0] {
				return false
			}
		case LabelOpNotEquals:
			if ok && v == r.Values[0] {
				return false
			}
		case LabelOpIn:
			if !ok || !contains(r.Values, v) {
				return false
			}
		case LabelOpNotIn:
			if ok && contains(r.Values, v) {
				return false
			}
		}
	}
	return true
}

func contains(vals []string, v string) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

// SQL translates the selector to a conjunctive Postgres predicate over a jsonb
// column, with `?` placeholders and the matching argument list (GORM style).
// An empty selector yields ("", nil, nil) — the caller simply omits it.
//
// Containment (`@>`) is used for equality and set membership so the GIN index
// on the column can serve them; existence goes through jsonb_exists() rather
// than the `?` operator, which would collide with GORM's placeholder.
func (sel *LabelSelector) SQL(column string) (string, []any, error) {
	if !sqlIdentRe.MatchString(column) {
		return "", nil, fmt.Errorf("invalid jsonb column name %q", column)
	}
	if sel.Empty() {
		return "", nil, nil
	}
	var (
		clauses []string
		args    []any
	)
	for _, r := range sel.Requirements {
		switch r.Op {
		case LabelOpExists:
			clauses = append(clauses, fmt.Sprintf("jsonb_exists(%s, ?)", column))
			args = append(args, r.Key)
		case LabelOpNotExists:
			clauses = append(clauses, fmt.Sprintf("NOT jsonb_exists(%s, ?)", column))
			args = append(args, r.Key)
		case LabelOpEquals:
			clauses = append(clauses, fmt.Sprintf("%s @> ?::jsonb", column))
			args = append(args, labelJSON(r.Key, r.Values[0]))
		case LabelOpNotEquals:
			clauses = append(clauses, fmt.Sprintf("NOT (%s @> ?::jsonb)", column))
			args = append(args, labelJSON(r.Key, r.Values[0]))
		case LabelOpIn, LabelOpNotIn:
			ors := make([]string, 0, len(r.Values))
			for _, v := range r.Values {
				ors = append(ors, fmt.Sprintf("%s @> ?::jsonb", column))
				args = append(args, labelJSON(r.Key, v))
			}
			joined := "(" + strings.Join(ors, " OR ") + ")"
			if r.Op == LabelOpNotIn {
				joined = "NOT " + joined
			}
			clauses = append(clauses, joined)
		default:
			return "", nil, fmt.Errorf("unknown label operator %q", r.Op)
		}
	}
	return strings.Join(clauses, " AND "), args, nil
}

// LabelSelectorSQL parses selector text and translates it in one call — the
// convenient form for stores that keep the raw selector string.
func LabelSelectorSQL(selector, column string) (string, []any, error) {
	sel, err := ParseLabelSelector(selector)
	if err != nil {
		return "", nil, err
	}
	return sel.SQL(column)
}

func labelJSON(k, v string) string {
	b, _ := json.Marshal(map[string]string{k: v}) // keys/values are validated strings
	return string(b)
}
