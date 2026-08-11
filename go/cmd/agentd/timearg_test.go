package main

// timearg_test.go — the four accepted forms of a since/until bound, and the
// refusals. Pure: no database, no clock dependency (parseMSTime takes `now`).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fixedNow is an arbitrary but stable instant, so a relative age asserts an
// exact millisecond rather than a tolerance.
var fixedNow = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

func TestMsTimeArgParseStringForms(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{"rfc3339", "2026-07-18T00:00:00Z", time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{"rfc3339 with offset", "2026-07-18T01:00:00+01:00", time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{"bare millis in quotes", "1752796800000", 1752796800000},
		{"relative seconds", "30s", fixedNow.Add(-30 * time.Second).UnixMilli()},
		{"relative minutes", "90m", fixedNow.Add(-90 * time.Minute).UnixMilli()},
		{"relative hours", "24h", fixedNow.Add(-24 * time.Hour).UnixMilli()},
		{"relative days", "7d", fixedNow.Add(-7 * 24 * time.Hour).UnixMilli()},
		{"surrounding space is tolerated", "  7d  ", fixedNow.Add(-7 * 24 * time.Hour).UnixMilli()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMSTime(tc.in, fixedNow)
			if err != nil {
				t.Fatalf("parseMSTime(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseMSTime(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// A half-understood duration is worse than a refusal, so the grammar is strict
// and every rejection names the accepted forms.
func TestMsTimeArgRejectsAmbiguousForms(t *testing.T) {
	for _, in := range []string{
		"7 days",  // spaces
		"7 d",     // space before unit
		"-7d",     // sign
		"0d",      // zero is not a window
		"d",       // no count
		"7",       // unitless — would be an absurd millisecond value
		"7w",      // unsupported unit
		"7D",      // wrong case
		"last week",
		"",
	} {
		t.Run(in, func(t *testing.T) {
			got, err := parseMSTime(in, fixedNow)
			if err == nil {
				// "7" parses as bare millis by design; assert that explicitly
				// rather than letting it silently pass this table.
				if in == "7" && got == 7 {
					return
				}
				t.Fatalf("parseMSTime(%q) = %d, want an error", in, got)
			}
			if in != "" && !strings.Contains(err.Error(), "relative age") {
				t.Fatalf("refusal for %q does not name the accepted forms: %v", in, err)
			}
		})
	}
}

func TestMsTimeArgUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantSet bool
		want    int64
	}{
		{"absent is unset", `null`, false, 0},
		{"empty string is unset", `""`, false, 0},
		{"blank string is unset", `"   "`, false, 0},
		{"bare number is millis", `1752796800000`, true, 1752796800000},
		{"quoted number is millis", `"1752796800000"`, true, 1752796800000},
		{"rfc3339", `"2026-07-18T00:00:00Z"`, true, time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC).UnixMilli()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got msTimeArg
			if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			if got.Set != tc.wantSet {
				t.Fatalf("Set = %v, want %v", got.Set, tc.wantSet)
			}
			if got.Set && got.MS != tc.want {
				t.Fatalf("MS = %d, want %d", got.MS, tc.want)
			}
		})
	}
}

// The relative form has to work through the JSON path too — that is the one
// every MCP tool actually uses.
func TestMsTimeArgUnmarshalRelativeIsRecent(t *testing.T) {
	before := time.Now().Add(-24 * time.Hour).UnixMilli()
	var got msTimeArg
	if err := json.Unmarshal([]byte(`"24h"`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	after := time.Now().Add(-24 * time.Hour).UnixMilli()
	if !got.Set {
		t.Fatal("relative age did not set the bound")
	}
	if got.MS < before || got.MS > after {
		t.Fatalf("24h ago = %d, want within [%d, %d]", got.MS, before, after)
	}
}

func TestMsTimeArgUnmarshalRejectionNamesTheForms(t *testing.T) {
	var got msTimeArg
	err := json.Unmarshal([]byte(`"last Tuesday"`), &got)
	if err == nil {
		t.Fatal("want an error for an unparseable string")
	}
	for _, want := range []string{"RFC3339", "unix milliseconds", "relative age"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not mention %q: %v", want, err)
		}
	}
}

func TestCheckTimeRange(t *testing.T) {
	set := func(ms int64) msTimeArg { return msTimeArg{MS: ms, Set: true} }

	if err := checkTimeRange(set(200), set(100)); err == nil {
		t.Fatal("an inverted range must be refused")
	} else if !strings.Contains(err.Error(), "matches nothing") {
		t.Fatalf("refusal wording drifted: %v", err)
	}
	if err := checkTimeRange(set(100), set(200)); err != nil {
		t.Fatalf("a valid range must be accepted: %v", err)
	}
	if err := checkTimeRange(set(100), set(100)); err != nil {
		t.Fatalf("an instant range is legal: %v", err)
	}
	// Either bound alone is unbounded on the other side, never an error.
	if err := checkTimeRange(set(200), msTimeArg{}); err != nil {
		t.Fatalf("since alone must be legal: %v", err)
	}
	if err := checkTimeRange(msTimeArg{}, set(100)); err != nil {
		t.Fatalf("until alone must be legal: %v", err)
	}
}

// timeArgSchema must not name a "type": the value may be a string or a number,
// and declaring one would make a model believe the other is illegal.
func TestTimeArgSchemaOmitsType(t *testing.T) {
	s := timeArgSchema("lower")
	if _, ok := s["type"]; ok {
		t.Fatal("timeArgSchema must not declare a type — string and number are both legal")
	}
	desc, _ := s["description"].(string)
	for _, want := range []string{"lower", "RFC3339", "relative age"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("schema description does not mention %q: %q", want, desc)
		}
	}
}
