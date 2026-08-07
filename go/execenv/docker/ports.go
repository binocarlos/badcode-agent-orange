// Package docker provides ExecutionEnvironment implementations that use a Docker
// daemon — one for DinD (daemon over TCP, leased host port) and one for the
// shared host socket (container DNS addressing). Both are per-session (one
// container per session).
//
// Porting source: orchestrator/src/sandbox-manager.ts
// See docs/02-execution-environment.md and docs/90-provenance-map.md.
package docker

import (
	"fmt"
	"sync"

	"github.com/binocarlos/badcode-agent-orange/execenv"
)

// PortAllocator manages a finite pool of host ports for DinD mode.
//
// DinD containers need a host-port binding so the host can reach the in-image
// agent at http://localhost:<port>. The allocator maintains a set of
// "available" ports and a map of session→port for adopted/allocated leases.
//
// The pool is the hard ceiling on live sessions per host — one port each, held
// until the session is deleted or a host reclaims its idle container (agentd
// does, via agentkit.Policy.ArchiveTimeout; a host that sets no timeout gets
// the old behaviour, where nothing ever gave a port back). That ceiling is a
// legitimate limit, so exhaustion is not a bug; being unable to TELL that it is
// what happened is. Hence the range is remembered and every exhaustion error
// names it (see Allocate) and wraps execenv.ErrNoCapacity.
//
// Ported from orchestrator/src/sandbox-manager.ts PortAllocator@55.
type PortAllocator struct {
	mu        sync.Mutex
	available []int          // sorted free pool
	allocated map[string]int // sessionID → port

	// The pool as configured — kept so an error can say WHICH pool is full and
	// how big it is, rather than "no available ports".
	rangeStart, rangeEnd, size int
}

// NewPortAllocator creates a PortAllocator covering the inclusive range
// [rangeStart, rangeEnd]. Ports are handed out lowest-first for determinism,
// mirroring the TypeScript implementation's Math.min behaviour.
func NewPortAllocator(rangeStart, rangeEnd int) (*PortAllocator, error) {
	if rangeEnd < rangeStart {
		return nil, fmt.Errorf("port range empty: %d > %d", rangeEnd, rangeStart)
	}
	avail := make([]int, 0, rangeEnd-rangeStart+1)
	for p := rangeStart; p <= rangeEnd; p++ {
		avail = append(avail, p)
	}
	return &PortAllocator{
		available:  avail,
		allocated:  make(map[string]int),
		rangeStart: rangeStart,
		rangeEnd:   rangeEnd,
		size:       len(avail),
	}, nil
}

// exhausted builds the operator-facing error for a full pool. Caller holds mu.
//
// Every clause here is load-bearing: WHICH resource (the host port pool), HOW
// BIG it is, WHAT is holding it (live sessions, until deleted), and — the clause
// that would have saved a day — that this is a property of the HOST and not of
// the session being started, so re-creating the session cannot help.
func (pa *PortAllocator) exhausted() error {
	return fmt.Errorf("%w: the host port pool is exhausted — all %d ports in %d-%d are leased to "+
		"live sessions, and a session holds its port until it is deleted, so every further session "+
		"on this host will fail the same way until one is released (a host capacity limit, not a "+
		"lost or broken session)",
		execenv.ErrNoCapacity, pa.size, pa.rangeStart, pa.rangeEnd)
}

// Capacity implements execenv.CapacityReporter: nil if a port is free, the
// exhaustion error if not. It allocates nothing, so a caller can ask "is this
// host full?" without consuming the last port to find out.
func (pa *PortAllocator) Capacity() error {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	if len(pa.available) == 0 {
		return pa.exhausted()
	}
	return nil
}

// Allocate leases a port for sessionID. If sessionID already has a lease the
// same port is returned (idempotent). Returns an error if the pool is
// exhausted.
func (pa *PortAllocator) Allocate(sessionID string) (int, error) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	if port, ok := pa.allocated[sessionID]; ok {
		return port, nil
	}
	if len(pa.available) == 0 {
		return 0, pa.exhausted()
	}
	// Take lowest available port (list is kept sorted on construction; Adopt
	// also inserts in order).
	port := pa.available[0]
	pa.available = pa.available[1:]
	pa.allocated[sessionID] = port
	return port, nil
}

// Release returns the port leased to sessionID back to the pool. No-op if the
// sessionID has no lease.
func (pa *PortAllocator) Release(sessionID string) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	port, ok := pa.allocated[sessionID]
	if !ok {
		return
	}
	delete(pa.allocated, sessionID)
	pa.available = insertSorted(pa.available, port)
}

// Adopt re-leases an already-bound port for sessionID (used by Recover to
// re-adopt containers that survived a host restart). Removes the port from the
// available pool if present.
func (pa *PortAllocator) Adopt(sessionID string, port int) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	// Remove from available pool if present.
	pa.available = removePort(pa.available, port)
	pa.allocated[sessionID] = port
}

// Stats returns current pool statistics for observability.
func (pa *PortAllocator) Stats() (total, inUse, free int) {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	inUse = len(pa.allocated)
	free = len(pa.available)
	total = inUse + free
	return
}

// Get returns the port currently leased to sessionID, and whether one exists.
func (pa *PortAllocator) Get(sessionID string) (int, bool) {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	p, ok := pa.allocated[sessionID]
	return p, ok
}

// insertSorted inserts v into a sorted slice, keeping it sorted.
func insertSorted(s []int, v int) []int {
	out := make([]int, 0, len(s)+1)
	inserted := false
	for _, x := range s {
		if !inserted && v < x {
			out = append(out, v)
			inserted = true
		}
		out = append(out, x)
	}
	if !inserted {
		out = append(out, v)
	}
	return out
}

// removePort removes port from the sorted slice (first occurrence only).
func removePort(s []int, port int) []int {
	for i, p := range s {
		if p == port {
			out := make([]int, 0, len(s)-1)
			out = append(out, s[:i]...)
			out = append(out, s[i+1:]...)
			return out
		}
	}
	return s
}
