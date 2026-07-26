package main

import (
	"strings"
	"testing"
)

// TestResolvePortRange_DefaultUnchanged pins the promise the seam was added
// under: a deployment that sets neither variable gets exactly the pool agentd
// hardcoded before it existed (30001-30100, 100 concurrent sessions).
func TestResolvePortRange_DefaultUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"neither set", nil},
		{"both empty (compose ${VAR:-})", map[string]string{
			portRangeStartVar: "", portRangeEndVar: "",
		}},
		{"both whitespace", map[string]string{
			portRangeStartVar: "  ", portRangeEndVar: "\t",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := resolvePortRange(envMap(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if r.start != 30001 || r.end != 30100 {
				t.Fatalf("range = %s, want 30001-30100", r)
			}
			if r.size() != 100 {
				t.Fatalf("size = %d, want 100", r.size())
			}
		})
	}
}

func TestResolvePortRange_Accepted(t *testing.T) {
	for _, tc := range []struct {
		name             string
		start, end       string
		wantStart, wantE int
		wantSize         int
	}{
		{"a pool of three, as a test stack would set", "40000", "40002", 40000, 40002, 3},
		{"a pool of one", "40000", "40000", 40000, 40000, 1},
		{"whitespace is trimmed", " 30001 ", " 30100 ", 30001, 30100, 100},
		{"at the usable floor", "1024", "1030", 1024, 1030, 7},
		{"at the usable ceiling", "65530", "65535", 65530, 65535, 6},
		{"the largest pool allowed", "20000", "29999", 20000, 29999, maxPortPoolSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := resolvePortRange(envMap(map[string]string{
				portRangeStartVar: tc.start, portRangeEndVar: tc.end,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if r.start != tc.wantStart || r.end != tc.wantE {
				t.Fatalf("range = %s, want %d-%d", r, tc.wantStart, tc.wantE)
			}
			if r.size() != tc.wantSize {
				t.Fatalf("size = %d, want %d", r.size(), tc.wantSize)
			}
		})
	}
}

// TestResolvePortRange_Rejected is the whole point of the validation: every one
// of these boots agentd with a pool that can never work, so each must be a boot
// error naming the offending variable, not a silent start.
func TestResolvePortRange_Rejected(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end string
		wantIn     []string // substrings the operator needs in the message
	}{
		{"non-numeric start", "thirty-thousand", "30100",
			[]string{portRangeStartVar, "not a number"}},
		{"non-numeric end", "30001", "30100x",
			[]string{portRangeEndVar, "not a number"}},
		{"float", "30001.5", "30100",
			[]string{portRangeStartVar, "not a number"}},
		{"start above end", "30100", "30001",
			[]string{portRangeStartVar, portRangeEndVar, "above", "empty"}},
		{"zero start", "0", "30100",
			[]string{portRangeStartVar, "outside the usable port range"}},
		{"zero end", "30001", "0",
			[]string{portRangeEndVar, "outside the usable port range"}},
		{"negative start", "-1", "30100",
			[]string{portRangeStartVar, "outside the usable port range"}},
		{"negative end", "30001", "-30100",
			[]string{portRangeEndVar, "outside the usable port range"}},
		{"below the privileged floor", "80", "1000",
			[]string{portRangeStartVar, "outside the usable port range"}},
		{"above the port space", "30001", "70000",
			[]string{portRangeEndVar, "outside the usable port range"}},
		{"pool too large to be anything but a typo", "1024", "65535",
			[]string{"spans", "typo"}},
		{"one over the size cap", "20000", "30000",
			[]string{"spans 10001 ports", "typo"}},
		{"start set alone", "40000", "",
			[]string{portRangeStartVar, portRangeEndVar, "set both or neither"}},
		{"end set alone", "", "40002",
			[]string{portRangeStartVar, portRangeEndVar, "set both or neither"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolvePortRange(envMap(map[string]string{
				portRangeStartVar: tc.start, portRangeEndVar: tc.end,
			}))
			if err == nil {
				t.Fatalf("start=%q end=%q: want a boot error, got none", tc.start, tc.end)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestPortRangeString matches what the exhaustion error and the low-water
// warning print, so an operator can line the boot log up with the failure.
func TestPortRangeString(t *testing.T) {
	if got := (portRange{start: 40000, end: 40002}).String(); got != "40000-40002" {
		t.Fatalf("String() = %q, want 40000-40002", got)
	}
}
