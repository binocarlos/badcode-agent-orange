package fleet

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// PlacementPolicy selects a worker for a NEW session from a set of candidates.
// Only called when there is no existing binding for the session.
type PlacementPolicy interface {
	// Pick selects a worker from the non-empty candidates slice.
	// It must honour hint.PreferWorkerID when that worker is present.
	Pick(candidates []*Worker, hint PlacementHint) (*Worker, error)
}

// LeastLoaded is the default PlacementPolicy. It tracks a per-worker concurrency
// counter and picks the worker with the fewest active sessions. Workers with a
// load >= maxConcurrent (when maxConcurrent > 0) are excluded.
// hint.PreferWorkerID is honoured when that worker is present and eligible.
type LeastLoaded struct {
	// MaxConcurrent is the per-worker cap; 0 means unbounded.
	MaxConcurrent int

	mu     sync.Mutex
	counts map[string]*int64 // workerID -> active-session count
}

// Acquire increments the load counter for a worker. Call after binding a session.
func (l *LeastLoaded) Acquire(workerID string) {
	atomic.AddInt64(l.counter(workerID), 1)
}

// Release decrements the load counter for a worker.
func (l *LeastLoaded) Release(workerID string) {
	if v := atomic.AddInt64(l.counter(workerID), -1); v < 0 {
		atomic.StoreInt64(l.counter(workerID), 0)
	}
}

func (l *LeastLoaded) Pick(candidates []*Worker, hint PlacementHint) (*Worker, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("fleet: no candidates available for placement")
	}
	// If a preferred worker is present, return it without load-checking (sticky
	// restore wins over load balancing).
	if hint.PreferWorkerID != "" {
		for _, w := range candidates {
			if w.ID == hint.PreferWorkerID {
				return w, nil
			}
		}
	}

	var best *Worker
	var bestLoad int64 = -1
	for _, w := range candidates {
		load := atomic.LoadInt64(l.counter(w.ID))
		if l.MaxConcurrent > 0 && load >= int64(l.MaxConcurrent) {
			continue // over capacity
		}
		if best == nil || load < bestLoad {
			best = w
			bestLoad = load
		}
	}
	if best == nil {
		return nil, fmt.Errorf("fleet: all %d workers are at or above MaxConcurrent (%d)", len(candidates), l.MaxConcurrent)
	}
	return best, nil
}

func (l *LeastLoaded) counter(workerID string) *int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil {
		l.counts = map[string]*int64{}
	}
	if _, ok := l.counts[workerID]; !ok {
		var zero int64
		l.counts[workerID] = &zero
	}
	return l.counts[workerID]
}

// RoundRobin is an alternative PlacementPolicy that cycles through candidates in
// registration order. hint.PreferWorkerID is honoured when present.
type RoundRobin struct {
	next atomic.Uint64
}

func (r *RoundRobin) Pick(candidates []*Worker, hint PlacementHint) (*Worker, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("fleet: no candidates available for placement")
	}
	// Honour the sticky hint first.
	if hint.PreferWorkerID != "" {
		for _, w := range candidates {
			if w.ID == hint.PreferWorkerID {
				return w, nil
			}
		}
	}
	idx := int(r.next.Add(1)-1) % len(candidates)
	return candidates[idx], nil
}

// Compile-time assertions.
var (
	_ PlacementPolicy = (*LeastLoaded)(nil)
	_ PlacementPolicy = (*RoundRobin)(nil)
)
