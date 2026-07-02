package orchestrator

import (
	"errors"
	"testing"
)

func TestSpawnLedgerEnforcesFloors(t *testing.T) {
	child := func(parent string, b Budget) Scope { return Scope{Parent: parent, Budget: b} }

	t.Run("depth cap", func(t *testing.T) {
		l := NewSpawnLedger()
		l.RegisterRoot("mgr", Budget{MaxDepth: 1, MaxSpawns: 9, TreeTokens: 1000})
		s1, err := l.Admit(child("mgr", Budget{MaxDepth: 1, MaxSpawns: 9, TreeTokens: 1000}))
		if err != nil || s1 != "s1" {
			t.Fatalf("depth-1 admit: id=%q err=%v", s1, err)
		}
		// s1 (depth 1) spawning a child would be depth 2 > MaxDepth 1 → refuse.
		if _, err := l.Admit(child("s1", Budget{MaxDepth: 1, MaxSpawns: 9, TreeTokens: 1000})); !errors.Is(err, ErrMaxDepth) {
			t.Fatalf("want ErrMaxDepth, got %v", err)
		}
	})

	t.Run("per-scope spawn cap", func(t *testing.T) {
		l := NewSpawnLedger()
		l.RegisterRoot("mgr", Budget{MaxDepth: 5, MaxSpawns: 2, TreeTokens: 1000})
		b := Budget{MaxDepth: 5, MaxSpawns: 2, TreeTokens: 1000}
		if _, err := l.Admit(child("mgr", b)); err != nil {
			t.Fatalf("spawn 1: %v", err)
		}
		if _, err := l.Admit(child("mgr", b)); err != nil {
			t.Fatalf("spawn 2: %v", err)
		}
		if _, err := l.Admit(child("mgr", b)); !errors.Is(err, ErrMaxSpawns) {
			t.Fatalf("want ErrMaxSpawns on 3rd, got %v", err)
		}
	})

	t.Run("shared tree budget", func(t *testing.T) {
		l := NewSpawnLedger()
		l.RegisterRoot("mgr", Budget{MaxDepth: 5, MaxSpawns: 9, TreeTokens: 100})
		b := Budget{MaxDepth: 5, MaxSpawns: 9, TreeTokens: 100}
		s1, err := l.Admit(child("mgr", b))
		if err != nil {
			t.Fatalf("admit s1: %v", err)
		}
		if err := l.Charge(s1, 100); err != nil { // drain the whole tree
			t.Fatalf("charge: %v", err)
		}
		if _, err := l.Admit(child("mgr", b)); !errors.Is(err, ErrTreeExhausted) {
			t.Fatalf("want ErrTreeExhausted, got %v", err)
		}
	})

	t.Run("unknown parent", func(t *testing.T) {
		l := NewSpawnLedger()
		if _, err := l.Admit(child("ghost", Budget{MaxDepth: 5})); !errors.Is(err, ErrUnknownParent) {
			t.Fatalf("want ErrUnknownParent, got %v", err)
		}
	})
}

// §10c §F: MaxSpawns is an IN-FLIGHT fan-out cap, not a lifetime one — Release
// frees the parent's slot when a session reaches a terminal outcome.
func TestSpawnLedgerReleaseFreesTheParentSlot(t *testing.T) {
	b := Budget{MaxDepth: 5, MaxSpawns: 1, TreeTokens: 1000}
	child := func(parent string) Scope { return Scope{Parent: parent, Budget: b} }

	l := NewSpawnLedger()
	l.RegisterRoot("mgr", b)
	s1, err := l.Admit(child("mgr"))
	if err != nil {
		t.Fatalf("admit s1: %v", err)
	}
	if _, err := l.Admit(child("mgr")); !errors.Is(err, ErrMaxSpawns) {
		t.Fatalf("cap not enforced while s1 in flight: %v", err)
	}

	l.Release(s1) // s1 completed → the slot frees
	s2, err := l.Admit(child("mgr"))
	if err != nil {
		t.Fatalf("admit after release: %v", err)
	}

	// Idempotent per session: double-release is a no-op (no free extra slot).
	l.Release(s1)
	l.Release(s1)
	if _, err := l.Admit(child("mgr")); !errors.Is(err, ErrMaxSpawns) {
		t.Fatalf("double-release freed a phantom slot: %v", err)
	}

	// Depth records are unaffected: s2 is still depth 1, so its child is depth 2.
	deep := Scope{Parent: s2, Budget: Budget{MaxDepth: 1, MaxSpawns: 1, TreeTokens: 1000}}
	if _, err := l.Admit(deep); !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("depth record lost after release: %v", err)
	}

	// Releasing an unknown session or the root is a safe no-op.
	l.Release("ghost")
	l.Release("mgr")
}
