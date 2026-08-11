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
	"strings"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// msTimeArg is an optional time bound in unix milliseconds. Set distinguishes
// "absent" from "the epoch", which matters because zero is a legal instant and
// also the zero value.
type msTimeArg struct {
	MS  int64
	Set bool
}

// The grammar itself lives in agentdb.ParseMSTime, because the HTTP API needs
// the identical one and is a different package. What stays here is the JSON
// wrapper: MCP tools receive JSON values, HTTP receives bare query strings.

const timeArgFormsHelp = agentdb.TimeArgFormsHelp

func parseMSTime(s string, now time.Time) (int64, error) { return agentdb.ParseMSTime(s, now) }

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
