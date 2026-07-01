package orchestrator

import (
	"errors"
	"fmt"
	"sync"
)

// The floors (contracts §7) — refused as sentinels so callers can branch fail-loud.
var (
	ErrMaxDepth      = errors.New("floor: max depth exceeded")
	ErrMaxSpawns     = errors.New("floor: max spawns exceeded")
	ErrTreeExhausted = errors.New("floor: tree token budget exhausted")
	ErrUnknownParent = errors.New("floor: unknown parent session")
)

// SpawnLedger is the work-state that enforces the three independent recursion
// controls (execution model §7): tree height (depth), branching factor (per-scope
// spawns), and total cost (shared tree tokens). It is NOT versioned policy — it is
// ephemeral per-goal-tree work state, the natural home for depth and the shared
// token counter that the value-typed Budget cannot carry.
type SpawnLedger struct {
	mu        sync.Mutex
	seq       int
	depth     map[string]int    // sessionID -> depth
	maxSpawns map[string]int    // sessionID -> its own fan-out cap
	spawns    map[string]int    // sessionID -> children spawned so far
	root      map[string]string // sessionID -> tree-root sessionID
	tree      map[string]int64  // tree-root sessionID -> shared tokens remaining
}

// NewSpawnLedger returns an empty ledger.
func NewSpawnLedger() *SpawnLedger {
	return &SpawnLedger{
		depth: map[string]int{}, maxSpawns: map[string]int{},
		spawns: map[string]int{}, root: map[string]string{}, tree: map[string]int64{},
	}
}

// RegisterRoot records a depth-0 tree root (the manager exchange). Idempotent.
func (l *SpawnLedger) RegisterRoot(sessionID string, b Budget) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.depth[sessionID]; ok {
		return
	}
	l.depth[sessionID] = 0
	l.maxSpawns[sessionID] = b.MaxSpawns
	l.root[sessionID] = sessionID
	l.tree[sessionID] = b.TreeTokens
}

// Admit is the spawn path floor check. It returns a fresh sessionID or refuses.
func (l *SpawnLedger) Admit(s Scope) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pd, ok := l.depth[s.Parent]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownParent, s.Parent)
	}
	depth := pd + 1
	if depth > s.Budget.MaxDepth {
		return "", fmt.Errorf("%w: depth %d > %d", ErrMaxDepth, depth, s.Budget.MaxDepth)
	}
	if l.spawns[s.Parent] >= l.maxSpawns[s.Parent] {
		return "", fmt.Errorf("%w: parent %q at %d", ErrMaxSpawns, s.Parent, l.maxSpawns[s.Parent])
	}
	troot := l.root[s.Parent]
	if l.tree[troot] <= 0 {
		return "", fmt.Errorf("%w: tree %q", ErrTreeExhausted, troot)
	}
	l.seq++
	sid := fmt.Sprintf("s%d", l.seq)
	l.depth[sid] = depth
	l.maxSpawns[sid] = s.Budget.MaxSpawns
	l.root[sid] = troot
	l.spawns[s.Parent]++
	return sid, nil
}

// Charge decrements the session's tree-root shared budget (clamped at 0). This is
// how TreeTokens is "decremented down the whole goal-tree."
func (l *SpawnLedger) Charge(sessionID string, tokens int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	troot, ok := l.root[sessionID]
	if !ok {
		return fmt.Errorf("floor: charge unknown session %q", sessionID)
	}
	l.tree[troot] -= tokens
	if l.tree[troot] < 0 {
		l.tree[troot] = 0
	}
	return nil
}
