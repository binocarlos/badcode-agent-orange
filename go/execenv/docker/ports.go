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
)

// PortAllocator manages a finite pool of host ports for DinD mode.
//
// DinD containers need a host-port binding so the host can reach the in-image
// agent at http://localhost:<port>. The allocator maintains a set of
// "available" ports and a map of session→port for adopted/allocated leases.
//
// Ported from orchestrator/src/sandbox-manager.ts PortAllocator@55.
type PortAllocator struct {
	mu        sync.Mutex
	available []int            // sorted free pool
	allocated map[string]int   // sessionID → port
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
		available: avail,
		allocated: make(map[string]int),
	}, nil
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
		return 0, fmt.Errorf("port pool exhausted: no available ports in the sandbox port pool")
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
