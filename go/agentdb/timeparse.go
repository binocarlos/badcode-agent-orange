package agentdb

// timeparse.go — the one parser for a caller-supplied time bound.
//
// It lives here rather than beside the MCP tools because BOTH surfaces need it
// and they are different packages: the core MCP server is `package main` under
// cmd/agentd, the HTTP API is `package httpapi`, and agentdb is the one thing
// they both import. The unit convention is also this package's business —
// memories and config events are stamped in unix MILLISECONDS while the event
// spine uses seconds, and that split is exactly what a caller must not have to
// reason about.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// relativeAgeRe matches the relative form: a positive count and one unit.
// Deliberately strict — no spaces, no plural, no sign, no zero — because a
// half-understood duration is worse than a refusal that names the alternatives.
var relativeAgeRe = regexp.MustCompile(`^([1-9][0-9]*)([smhd])$`)

// TimeArgFormsHelp names every accepted form, once, so a schema description and
// an error message can never describe different grammars.
const TimeArgFormsHelp = `an RFC3339 timestamp ("2026-07-18T00:00:00Z"), unix milliseconds, or a relative age ("7d", "24h", "90m", "30s")`

// ParseMSTime turns a caller's time bound into unix milliseconds. `now` is a
// parameter rather than a call so the relative form is testable to the
// millisecond.
//
// A relative age means that long BEFORE now — "7d" is a lower bound seven days
// back, which is the shape every real caller wants ("what changed since my last
// pass") and the shape a model gets wrong when asked to compute an epoch.
func ParseMSTime(s string, now time.Time) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time bound: want %s", TimeArgFormsHelp)
	}
	if m := relativeAgeRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not %s", s, TimeArgFormsHelp)
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
	return 0, fmt.Errorf("%q is not %s", s, TimeArgFormsHelp)
}
