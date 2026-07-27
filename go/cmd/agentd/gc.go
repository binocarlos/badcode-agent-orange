package main

// gc.go — session garbage collection, as configuration.
//
// The Runner has two background loops and agentd used to start NEITHER, so a
// session held its container — and one of the host's finite host ports
// (portrange.go) — from creation until a human explicitly deleted it. That is
// the underlying cause of the port-pool exhaustion this stack has hit: nothing
// reclaimed anything on a timer, so the pool only ever drained.
//
// Two knobs, both durations, both refused at boot if they are nonsense:
//
//	AGENTKIT_SESSION_IDLE_TIMEOUT=30m   (default; "off" disables)
//	AGENTKIT_SNAPSHOT_REAP_INTERVAL=6h  (default; "off" disables)
//
// # The idle timeout is reclamation, not deletion
//
// It sets agentkit.Policy.ArchiveTimeout. Every minute the Runner snapshots each
// session idle longer than this and drops its container. The SESSION ROW
// SURVIVES: the next message restores it from the snapshot handle and the
// conversation continues. Nothing a user can see is lost — what is reclaimed is
// a container, its memory, and the host port. 30 minutes is the operator's
// chosen point on the one real trade-off: shorter reclaims sooner but pays a
// restore (an image materialise) more often; longer keeps chat instant for
// longer at the cost of holding capacity for a conversation nobody is having.
//
// # The reap interval is the snapshot TTL sweep
//
// It sets agentkit.Policy.SnapshotReapInterval and is the other half of the
// same lifecycle: the archive loop CREATES snapshots, this retires the ones
// whose per-project expiry (project_settings.snapshot_ttl_days, §5) has passed.
// The default is 6 hours because the promise it enforces is stamped in DAYS —
// four passes a day makes the worst-case lateness a rounding error against a
// 24-hour granularity, while a pass that finds nothing is a handful of queries.
// It is a slow sweep by construction, never a hot path, so there is no reason
// to run it minutely and every reason not to.
//
// Both are floored at one minute and capped at thirty days. The floor is real:
// the archive sweep ticks once a minute, and a sub-minute idle timeout would
// promise a precision the loop does not have while threatening to archive a
// session between two messages of a live conversation. The cap catches the
// dropped unit (`30` is not a duration; `720h0m` is a month) — past it, the
// honest spelling is "off".
//
// Resolution is pure and unit-tested, like portrange.go / backends.go /
// permalink.go / mcpenv.go, and a bad value stops the process at boot rather
// than starting a host whose garbage collection silently does nothing.

import (
	"fmt"
	"strings"
	"time"
)

const (
	sessionIdleTimeoutVar   = "AGENTKIT_SESSION_IDLE_TIMEOUT"
	snapshotReapIntervalVar = "AGENTKIT_SNAPSHOT_REAP_INTERVAL"

	// defaultSessionIdleTimeout is how long a session may sit untouched before
	// its container is snapshotted and released.
	defaultSessionIdleTimeout = 30 * time.Minute
	// defaultSnapshotReapInterval is how often the snapshot TTL sweep runs.
	defaultSnapshotReapInterval = 6 * time.Hour

	// minGCInterval / maxGCInterval bound both knobs. See the header.
	minGCInterval = time.Minute
	maxGCInterval = 30 * 24 * time.Hour
)

// gcDisabledWords are the spellings that mean "do not run this loop". "0" is
// included because it is what the Policy field itself uses, and an operator who
// writes it should get what they asked for rather than a validation error.
var gcDisabledWords = map[string]bool{"off": true, "0": true, "none": true, "never": true, "disabled": true}

// gcConfig is the resolved session-GC policy. The zero value disables both
// loops, which is exactly what agentd did before this file existed.
type gcConfig struct {
	// idleTimeout → agentkit.Policy.ArchiveTimeout. 0 = never archive.
	idleTimeout time.Duration
	// reapInterval → agentkit.Policy.SnapshotReapInterval. 0 = never sweep.
	reapInterval time.Duration
}

// resolveGCConfig reads the two variables into a validated policy.
func resolveGCConfig(env func(string) string) (gcConfig, error) {
	idle, err := parseGCDuration(sessionIdleTimeoutVar, env(sessionIdleTimeoutVar), defaultSessionIdleTimeout)
	if err != nil {
		return gcConfig{}, err
	}
	reap, err := parseGCDuration(snapshotReapIntervalVar, env(snapshotReapIntervalVar), defaultSnapshotReapInterval)
	if err != nil {
		return gcConfig{}, err
	}
	return gcConfig{idleTimeout: idle, reapInterval: reap}, nil
}

// parseGCDuration parses one knob: unset (or, from compose's `${VAR:-}`, empty)
// → the default; a disabling word → 0; otherwise a Go duration inside the
// bounds. Everything else is a boot error.
func parseGCDuration(name, raw string, def time.Duration) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return def, nil
	}
	if gcDisabledWords[strings.ToLower(s)] {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration — want a Go duration such as %s, or \"off\" to disable it (a bare number has no unit)",
			name, raw, def)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s=%q is negative — use \"off\" to disable it", name, raw)
	}
	if d < minGCInterval {
		return 0, fmt.Errorf("%s=%q is below the %s minimum — the sweep only runs once a minute, so a shorter setting promises a precision it does not have (use \"off\" to disable it)",
			name, raw, minGCInterval)
	}
	if d > maxGCInterval {
		return 0, fmt.Errorf("%s=%q is longer than the %s maximum, which is a missing unit rather than an intention — say \"off\" if you mean never",
			name, raw, maxGCInterval)
	}
	return d, nil
}

// describeIdleTimeout / describeReapInterval are the boot log lines, written so
// an operator reading the log knows what their host will actually do — in
// particular that archiving is NOT deletion.
func (c gcConfig) describeIdleTimeout() string {
	if c.idleTimeout <= 0 {
		return fmt.Sprintf("session gc DISABLED (%s=off) — every session holds its container and one host port until it is deleted", sessionIdleTimeoutVar)
	}
	return fmt.Sprintf("session gc: containers idle >%s are snapshotted and released (the session survives and resumes from its snapshot on the next message)", c.idleTimeout)
}

func (c gcConfig) describeReapInterval(wired bool) string {
	if !wired {
		return "snapshot TTL reaper DISABLED (no DATABASE_URL) — the image catalogue it sweeps lives in Postgres"
	}
	if c.reapInterval <= 0 {
		return fmt.Sprintf("snapshot TTL reaper DISABLED (%s=off) — expired snapshot bytes are kept for ever", snapshotReapIntervalVar)
	}
	return fmt.Sprintf("snapshot TTL reaper: sweeping every %s (expiry itself is per project — project_settings.snapshot_ttl_days, 0 = never)", c.reapInterval)
}
