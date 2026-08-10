package main

// timearg.go — the one time-bound argument type, shared by every MCP tool that
// takes a `since`/`until` pair.
//
// It moved here from mcp_config_log.go when memory_search grew the same pair
// (design/2026-08-10-memory-query-and-injection-hardening.md, T1). Both tools
// now accept the identical four forms, which is the point: a model that learned
// "7d" on config_history should not discover that memory_search wants unix
// milliseconds.
//
// The four accepted forms, and why each one is here:
//
//	"2026-07-18T00:00:00Z"   RFC3339 — a model reasoning about a date writes one
//	1752796800000            unix milliseconds — what a previous result handed back
//	"1752796800000"          the same number in quotes, a common model habit
//	"7d" / "24h" / "90m"     a relative AGE: that long before now
//
// The relative form is the one worth explaining. Every caller of this type is
// asking a question about a window ending roughly now — "what changed since my
// last pass", "outcomes over the last week" — and the alternative is making the
// model compute an epoch millisecond from a date it reasoned about. That
// arithmetic is exactly where a model silently produces a number 1000x off,
// and this codebase has the footgun to prove it: memories and config events are
// stamped in MILLISECONDS while the event spine is in SECONDS. A duration
// string cannot be wrong by a factor of a thousand.
//
// UNITS: everything here returns milliseconds, because both stores that consume
// it (config_events.created_at, memories.created_at) are milliseconds.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// msTimeArg is an optional time bound in unix milliseconds. Set distinguishes
// "absent" from "the epoch", which matters because zero is a legal instant and
// also the zero value.
type msTimeArg struct {
	MS  int64
	Set bool
}

// relativeAgeRe matches the relative form: a positive count and one unit.
// Deliberately strict — no spaces, no plural, no sign, no zero-padding beyond
// what a number allows — because a half-understood duration is worse than a
// refusal that names the accepted forms.
var relativeAgeRe = regexp.MustCompile(`^([1-9][0-9]*)([smhd])$`)

// timeArgFormsHelp is the one description of the accepted forms, used in error
// messages and in every tool's schema so the two can never drift.
const timeArgFormsHelp = `an RFC3339 timestamp ("2026-07-18T00:00:00Z"), unix milliseconds, or a relative age ("7d", "24h", "90m", "30s")`

// parseMSTime parses the string form of a time bound. It is separate from
// UnmarshalJSON because MCP tools receive JSON while HTTP routes receive bare
// query-string values, and both must accept exactly the same four forms — the
// alternative is an HTTP handler inventing a quoting hack to reuse the JSON
// path. now is passed in rather than read, so the relative form is testable.
func parseMSTime(s string, now time.Time) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time bound: want %s", timeArgFormsHelp)
	}
	if m := relativeAgeRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not %s", s, timeArgFormsHelp)
		}
		var unit time.Duration
		switch m[2] {
		case "s":
			unit = time.Second
		case "m":
			unit = time.Minute
		case "h":
			unit = time.Hour
		case "d":
			unit = 24 * time.Hour
		}
		return now.Add(-time.Duration(n) * unit).UnixMilli(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli(), nil
	}
	// A bare number in quotes is a common model habit; accept it rather than
	// failing a query over its punctuation.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	return 0, fmt.Errorf("%q is not %s", s, timeArgFormsHelp)
}

// UnmarshalJSON accepts a JSON string in any of the string forms, or a bare
// JSON number as raw milliseconds. null and "" mean absent, not zero.
func (m *msTimeArg) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		if strings.TrimSpace(str) == "" {
			return nil
		}
		ms, err := parseMSTime(str, time.Now())
		if err != nil {
			return err
		}
		m.MS, m.Set = ms, true
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("expected %s, got %s", timeArgFormsHelp, s)
	}
	m.MS, m.Set = int64(n), true
	return nil
}

// timeArgSchema is the schema entry for a since/until argument. It deliberately
// omits "type": the value may be a string or a number, and naming one would
// make a model believe the other is illegal.
func timeArgSchema(bound string) map[string]any {
	return map[string]any{
		"description": "Inclusive " + bound + " bound: " + timeArgFormsHelp + ".",
	}
}

// checkTimeRange refuses a window that matches nothing by construction. Shared
// so every tool refuses it in the same words.
func checkTimeRange(since, until msTimeArg) error {
	if since.Set && until.Set && since.MS > until.MS {
		return fmt.Errorf("since (%d) is after until (%d) — that range matches nothing", since.MS, until.MS)
	}
	return nil
}
