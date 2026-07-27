package main

// portrange.go — the host's session port pool, as configuration.
//
// Every live session leases one host port from a fixed pool, and holds it until
// the session is deleted or its container is reclaimed for idleness (gc.go —
// AGENTKIT_SESSION_IDLE_TIMEOUT, 30 minutes by default; before that existed,
// nothing gave a port back on a timer at all). The size of that pool is the
// hard ceiling on CONCURRENT sessions per host: at zero free,
// every further session on the host fails with execenv.ErrNoCapacity and the
// operator-facing "host port pool is exhausted" diagnostic
// (go/execenv/docker/ports.go).
//
// Config:
//
//	AGENTKIT_PORT_RANGE_START=30001   (default)
//	AGENTKIT_PORT_RANGE_END=30100     (default)
//
// Both default to the range agentd has always hardcoded, so a deployment that
// sets neither is byte-identical to before. The point of the seam is not tuning
// production — it is that the exhaustion path becomes *exercisable*: a test
// stack can boot with a pool of three and reach the real error, at a real
// caller, in seconds, instead of needing a hundred live containers. A failure
// path nobody can reach is a failure path that rots.
//
// Set both or neither. Setting one alone would silently pair an operator's
// number with a default from the other end — the exact accident this validation
// exists to refuse.
//
// Everything else here is boot-time refusal rather than a pool that starts
// broken and then fails every session: a misconfigured pool that boots happily
// and fails at use time is precisely the silent trap this area keeps producing.
// Config resolution is pure and unit-tested (the resolve*(env) convention from
// backends.go); nothing here does I/O.

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	portRangeStartVar = "AGENTKIT_PORT_RANGE_START"
	portRangeEndVar   = "AGENTKIT_PORT_RANGE_END"

	// The historical hardcoded pool: 100 concurrent sessions per host.
	defaultPortRangeStart = 30001
	defaultPortRangeEnd   = 30100

	// Usable host ports. The floor excludes the privileged range — a session
	// container's ephemeral health port has no business there, and a value
	// below it is far more likely a typo than an intention.
	minUsablePort = 1024
	maxUsablePort = 65535

	// maxPortPoolSize is a typo guard, not a capacity claim. One session is one
	// container; a host that could run ten thousand of them does not exist, so a
	// range wider than this is a mistyped bound (a dropped digit, a swapped
	// pair), and swallowing it would reserve most of the port space for a pool
	// that can never fill.
	maxPortPoolSize = 10000
)

// portRange is the inclusive host-port pool handed to the DinD execution
// environment. The zero value is not usable; build one with resolvePortRange.
type portRange struct {
	start int
	end   int
}

// size is the number of ports in the pool — the concurrent-session ceiling.
func (r portRange) size() int { return r.end - r.start + 1 }

// String renders the pool the way the exhaustion error and the low-water
// warning render it, so an operator can match the boot log to the failure.
func (r portRange) String() string { return fmt.Sprintf("%d-%d", r.start, r.end) }

// resolvePortRange reads the port-pool env into a validated range, defaulting
// to the historical 30001-30100 when neither variable is set.
func resolvePortRange(env func(string) string) (portRange, error) {
	rawStart := strings.TrimSpace(env(portRangeStartVar))
	rawEnd := strings.TrimSpace(env(portRangeEndVar))

	// Unset (or, from compose's `${VAR:-}`, empty) → the historical default.
	if rawStart == "" && rawEnd == "" {
		return portRange{start: defaultPortRangeStart, end: defaultPortRangeEnd}, nil
	}
	if rawStart == "" || rawEnd == "" {
		set, unset := portRangeStartVar, portRangeEndVar
		if rawStart == "" {
			set, unset = portRangeEndVar, portRangeStartVar
		}
		return portRange{}, fmt.Errorf("%s is set but %s is not — set both or neither (default %d-%d)",
			set, unset, defaultPortRangeStart, defaultPortRangeEnd)
	}

	start, err := parsePortRangeBound(portRangeStartVar, rawStart)
	if err != nil {
		return portRange{}, err
	}
	end, err := parsePortRangeBound(portRangeEndVar, rawEnd)
	if err != nil {
		return portRange{}, err
	}
	if start > end {
		return portRange{}, fmt.Errorf("%s=%d is above %s=%d — the pool would be empty and every session on this host would fail",
			portRangeStartVar, start, portRangeEndVar, end)
	}
	r := portRange{start: start, end: end}
	if r.size() > maxPortPoolSize {
		return portRange{}, fmt.Errorf("%s=%d %s=%d spans %d ports — more than the %d-port maximum, which is a typo rather than a pool (one session is one container)",
			portRangeStartVar, start, portRangeEndVar, end, r.size(), maxPortPoolSize)
	}
	return r, nil
}

// parsePortRangeBound parses one bound, rejecting anything that is not a plain
// decimal port inside the usable range (which is what catches zero, negatives
// and 70000 alike).
func parsePortRangeBound(name, raw string) (int, error) {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a number (want a port between %d and %d)", name, raw, minUsablePort, maxUsablePort)
	}
	if v < minUsablePort || v > maxUsablePort {
		return 0, fmt.Errorf("%s=%d is outside the usable port range %d-%d", name, v, minUsablePort, maxUsablePort)
	}
	return v, nil
}
