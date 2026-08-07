package main

import (
	"strings"
	"testing"
	"time"
)

// The session-GC knobs (gc.go). Two facts are worth pinning above all: the
// DEFAULTS, because they are what every host that configures nothing gets, and
// the REJECTIONS, because a garbage collector that silently does not run is the
// exact failure this file exists to end.

func TestResolveGCConfig(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		wantIdle time.Duration
		wantReap time.Duration
	}{
		{
			name:     "unset → 30m archive, 6h sweep",
			env:      nil,
			wantIdle: 30 * time.Minute,
			wantReap: 6 * time.Hour,
		},
		{
			// This is how compose delivers an unset variable: `${VAR:-}`.
			name: "empty (compose's ${VAR:-}) → the same defaults",
			env: map[string]string{
				sessionIdleTimeoutVar:   "",
				snapshotReapIntervalVar: "",
			},
			wantIdle: 30 * time.Minute,
			wantReap: 6 * time.Hour,
		},
		{
			name:     "explicit durations",
			env:      map[string]string{sessionIdleTimeoutVar: "5m", snapshotReapIntervalVar: "12h"},
			wantIdle: 5 * time.Minute,
			wantReap: 12 * time.Hour,
		},
		{
			name:     "surrounding space trimmed",
			env:      map[string]string{sessionIdleTimeoutVar: "  90m  "},
			wantIdle: 90 * time.Minute,
			wantReap: 6 * time.Hour,
		},
		{
			name:     "compound duration",
			env:      map[string]string{sessionIdleTimeoutVar: "1h30m"},
			wantIdle: 90 * time.Minute,
			wantReap: 6 * time.Hour,
		},
		{
			name:     "off disables archiving only",
			env:      map[string]string{sessionIdleTimeoutVar: "off"},
			wantIdle: 0,
			wantReap: 6 * time.Hour,
		},
		{
			name:     "OFF is case-insensitive",
			env:      map[string]string{sessionIdleTimeoutVar: "OFF", snapshotReapIntervalVar: "Never"},
			wantIdle: 0,
			wantReap: 0,
		},
		{
			name:     "0 means off (it is what the Policy field itself uses)",
			env:      map[string]string{sessionIdleTimeoutVar: "0", snapshotReapIntervalVar: "0"},
			wantIdle: 0,
			wantReap: 0,
		},
		{
			name:     "the floor itself is accepted",
			env:      map[string]string{sessionIdleTimeoutVar: "1m", snapshotReapIntervalVar: "1m"},
			wantIdle: time.Minute,
			wantReap: time.Minute,
		},
		{
			name:     "the cap itself is accepted",
			env:      map[string]string{sessionIdleTimeoutVar: "720h", snapshotReapIntervalVar: "720h"},
			wantIdle: 720 * time.Hour,
			wantReap: 720 * time.Hour,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveGCConfig(envMap(tc.env))
			if err != nil {
				t.Fatalf("resolveGCConfig: %v", err)
			}
			if got.idleTimeout != tc.wantIdle {
				t.Errorf("idleTimeout = %v, want %v", got.idleTimeout, tc.wantIdle)
			}
			if got.reapInterval != tc.wantReap {
				t.Errorf("reapInterval = %v, want %v", got.reapInterval, tc.wantReap)
			}
		})
	}
}

// Every rejection stops the process at boot. A host that starts with a broken
// GC setting looks healthy and reclaims nothing, which is indistinguishable
// from the bug this change fixes.
func TestResolveGCConfig_Rejections(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		wantWord string // a phrase the operator needs to see
	}{
		{"bare number has no unit", map[string]string{sessionIdleTimeoutVar: "30"}, "not a duration"},
		{"words are not durations", map[string]string{sessionIdleTimeoutVar: "half an hour"}, "not a duration"},
		{"negative", map[string]string{sessionIdleTimeoutVar: "-5m"}, "negative"},
		{"below the floor", map[string]string{sessionIdleTimeoutVar: "30s"}, "minimum"},
		{"a millisecond", map[string]string{sessionIdleTimeoutVar: "5ms"}, "minimum"},
		{"beyond the cap", map[string]string{sessionIdleTimeoutVar: "1000h"}, "maximum"},
		{"reap: bare number", map[string]string{snapshotReapIntervalVar: "6"}, "not a duration"},
		{"reap: below the floor", map[string]string{snapshotReapIntervalVar: "1s"}, "minimum"},
		{"reap: beyond the cap", map[string]string{snapshotReapIntervalVar: "8760h"}, "maximum"},
		{"reap: negative", map[string]string{snapshotReapIntervalVar: "-1h"}, "negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveGCConfig(envMap(tc.env))
			if err == nil {
				t.Fatalf("expected a boot error for %v", tc.env)
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantWord)
			}
			// Whatever went wrong, the operator must be told which variable and
			// how to turn the loop off deliberately.
			if !strings.Contains(err.Error(), "AGENTKIT_") {
				t.Errorf("error %q does not name the variable", err.Error())
			}
		})
	}
}

// The boot log is the only place an operator sees what their host will do. It
// must say that archiving is not deletion — otherwise "sessions are being
// reclaimed" reads as data loss.
func TestGCConfigBootLines(t *testing.T) {
	on := gcConfig{idleTimeout: 30 * time.Minute, reapInterval: 6 * time.Hour}
	if got := on.describeIdleTimeout(); !strings.Contains(got, "30m") || !strings.Contains(got, "resumes") {
		t.Errorf("idle line does not state the timeout and that the session survives: %q", got)
	}
	if got := on.describeReapInterval(true); !strings.Contains(got, "6h") {
		t.Errorf("reap line does not state the interval: %q", got)
	}
	off := gcConfig{}
	if got := off.describeIdleTimeout(); !strings.Contains(got, "DISABLED") || !strings.Contains(got, "host port") {
		t.Errorf("disabled idle line must say what it costs: %q", got)
	}
	if got := off.describeReapInterval(true); !strings.Contains(got, "DISABLED") {
		t.Errorf("disabled reap line: %q", got)
	}
	// No Postgres → no catalogue to sweep, and the reason must be the missing
	// database rather than the (perfectly valid) interval.
	if got := on.describeReapInterval(false); !strings.Contains(got, "DATABASE_URL") {
		t.Errorf("unwired reap line must blame the missing database: %q", got)
	}
}
