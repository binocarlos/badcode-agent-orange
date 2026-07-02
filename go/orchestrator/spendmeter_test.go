package orchestrator

import (
	"context"
	"errors"
	"testing"
)

func TestMemSpendMeterHaltsAtCeiling(t *testing.T) {
	ctx := context.Background()
	m := NewMemSpendMeter(10.0)

	if err := m.Charge(ctx, 100, 6.0); err != nil {
		t.Fatalf("charge under ceiling: %v", err)
	}
	if spent, _ := m.Spent(ctx); spent != 6.0 {
		t.Fatalf("spent = %v, want 6.0", spent)
	}

	// The charge that first crosses the ceiling still records (6 < 10).
	if err := m.Charge(ctx, 100, 6.0); err != nil {
		t.Fatalf("crossing charge should record: %v", err)
	}
	if spent, _ := m.Spent(ctx); spent != 12.0 {
		t.Fatalf("spent = %v, want 12.0", spent)
	}

	// Now over ceiling: even a zero-amount probe halts the next dispatch.
	if err := m.Charge(ctx, 0, 0); !errors.Is(err, ErrSpendCeiling) {
		t.Fatalf("probe over ceiling = %v, want ErrSpendCeiling", err)
	}
}

// §10c I-5: spend is ALWAYS counted. Two in-flight calls that both passed the
// pre-dispatch probe both record, even though the second lands past the ceiling —
// the over-shoot is bounded by in-flight work and the next probe halts.
func TestMemSpendMeterCountsEveryChargePastTheProbe(t *testing.T) {
	ctx := context.Background()
	m := NewMemSpendMeter(10.0)

	// Both calls probe while spend is 0 — both pass.
	if err := m.Charge(ctx, 0, 0); err != nil {
		t.Fatalf("probe A: %v", err)
	}
	if err := m.Charge(ctx, 0, 0); err != nil {
		t.Fatalf("probe B: %v", err)
	}

	// Both post-call charges record: the second crosses the ceiling but is not dropped.
	if err := m.Charge(ctx, 700, 7.0); err != nil {
		t.Fatalf("charge A must record: %v", err)
	}
	if err := m.Charge(ctx, 700, 7.0); err != nil {
		t.Fatalf("charge B (crosses ceiling) must still record: %v", err)
	}
	if spent, _ := m.Spent(ctx); spent != 14.0 {
		t.Fatalf("spent = %v, want 14.0 (BOTH charges counted)", spent)
	}

	// Now at/over ceiling: the next probe halts dispatch.
	if err := m.Charge(ctx, 0, 0); !errors.Is(err, ErrSpendCeiling) {
		t.Fatalf("probe after ceiling = %v, want ErrSpendCeiling", err)
	}
}

func TestMemSpendMeterZeroCeilingHaltsImmediately(t *testing.T) {
	m := NewMemSpendMeter(0.0)
	if err := m.Charge(context.Background(), 0, 0); !errors.Is(err, ErrSpendCeiling) {
		t.Fatalf("zero-ceiling probe = %v, want ErrSpendCeiling", err)
	}
}
