package orchestrator

import (
	"context"
	"errors"
	"sync"
)

// The SpendMeter seam is declared in contracts.go (§5). This file provides an
// in-memory impl and the ceiling error. Charge errors when the ceiling is hit,
// which HALTS dispatch (the cost floor, sibling to the depth/spawn caps — §7.2).

// ErrSpendCeiling is returned by Charge once the running spend has reached the
// configured ceiling. Callers treat it as a hard halt on dispatch.
var ErrSpendCeiling = errors.New("spend ceiling reached")

// MemSpendMeter is an in-memory SpendMeter with a USD ceiling. Charge errors iff
// the meter is already at/over the ceiling — so a Charge(ctx,0,0) probe is a
// cheap "may I dispatch?" check, and the charge that first crosses the ceiling
// still records (the over-shoot is bounded by one dispatch, and the next probe
// halts). A Postgres-backed impl can swap in behind SpendMeter later (Slice A).
type MemSpendMeter struct {
	mu         sync.Mutex
	ceilingUSD float64
	spentUSD   float64
	tokens     int64
}

// NewMemSpendMeter returns a meter that halts dispatch once spend reaches ceilingUSD.
func NewMemSpendMeter(ceilingUSD float64) *MemSpendMeter {
	return &MemSpendMeter{ceilingUSD: ceilingUSD}
}

func (m *MemSpendMeter) Charge(_ context.Context, tokens int64, usd float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.spentUSD >= m.ceilingUSD {
		return ErrSpendCeiling
	}
	m.spentUSD += usd
	m.tokens += tokens
	return nil
}

func (m *MemSpendMeter) Spent(_ context.Context) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spentUSD, nil
}

var _ SpendMeter = (*MemSpendMeter)(nil)
