package fleet_test

import (
	"context"
	"testing"

	"github.com/bayes-price/agentkit/agentkittest"
	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/fleet"
)

// makeMock returns a MockExecutionEnvironment with the given capabilities.
func makeMock(caps execenv.Capabilities) *execenv.MockExecutionEnvironment {
	m := execenv.NewMock()
	m.Caps = &caps
	return m
}

// perSessionCaps returns default per-session capabilities (trusted container).
func perSessionCaps() execenv.Capabilities {
	return execenv.Capabilities{
		Backend:            execenv.BackendDockerDinD,
		Tenancy:            execenv.TenancyPerSession,
		IsolationTier:      execenv.TierContainer,
		SupportsSnapshot:   true,
		IsolatedPerSession: true,
	}
}

// newTwoWorkerFleet builds an in-memory fleet with two registered workers.
func newTwoWorkerFleet(t *testing.T, store *agentkittest.MemStore, policy fleet.PlacementPolicy) (fleet.Fleet, *fleet.Worker, *fleet.Worker) {
	t.Helper()
	env1 := makeMock(perSessionCaps())
	env2 := makeMock(perSessionCaps())
	w1 := &fleet.Worker{ID: "w1", Env: env1, Caps: env1.Capabilities(), Labels: map[string]string{"zone": "a"}}
	w2 := &fleet.Worker{ID: "w2", Env: env2, Caps: env2.Capabilities(), Labels: map[string]string{"zone": "b"}}

	opts := &fleet.MemFleetOptions{Policy: policy, TrustedWorkload: false}
	f := fleet.NewMemory(store, opts)
	ctx := context.Background()
	if err := f.Register(ctx, w1); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	if err := f.Register(ctx, w2); err != nil {
		t.Fatalf("Register w2: %v", err)
	}
	return f, w1, w2
}

// --- LeastLoaded placement ---------------------------------------------------

func TestLeastLoaded_PicksLessLoadedWorker(t *testing.T) {
	store := agentkittest.NewMemStore()
	ll := &fleet.LeastLoaded{}
	f, w1, _ := newTwoWorkerFleet(t, store, ll)
	ctx := context.Background()

	// Acquire one load unit on w1 to make w2 the less loaded.
	ll.Acquire("w1")
	defer ll.Release("w1")

	chosen, err := f.PlaceForSession(ctx, "sess-a", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("PlaceForSession: %v", err)
	}
	if chosen.ID != "w2" {
		t.Errorf("expected w2 (less loaded), got %q", chosen.ID)
	}
	_ = w1 // suppress unused warning
}

func TestLeastLoaded_HonoursStickyPrefer(t *testing.T) {
	store := agentkittest.NewMemStore()
	ll := &fleet.LeastLoaded{}
	f, w1, _ := newTwoWorkerFleet(t, store, ll)
	ctx := context.Background()

	// Even with higher load, PreferWorkerID wins.
	ll.Acquire("w1")
	ll.Acquire("w1")
	defer ll.Release("w1")
	defer ll.Release("w1")

	chosen, err := f.PlaceForSession(ctx, "sess-b", fleet.PlacementHint{PreferWorkerID: "w1"})
	if err != nil {
		t.Fatalf("PlaceForSession with prefer: %v", err)
	}
	if chosen.ID != "w1" {
		t.Errorf("expected w1 (preferred), got %q", chosen.ID)
	}
	_ = w1
}

// --- RoundRobin placement ---------------------------------------------------

func TestRoundRobin_Alternates(t *testing.T) {
	store := agentkittest.NewMemStore()
	rr := &fleet.RoundRobin{}
	f, _, _ := newTwoWorkerFleet(t, store, rr)
	ctx := context.Background()

	// Two fresh sessions should land on different workers (RR alternates).
	chosen1, err := f.PlaceForSession(ctx, "rr-sess-1", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("PlaceForSession 1: %v", err)
	}
	chosen2, err := f.PlaceForSession(ctx, "rr-sess-2", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("PlaceForSession 2: %v", err)
	}
	if chosen1.ID == chosen2.ID {
		t.Errorf("RoundRobin: both sessions placed on the same worker %q; expected alternation", chosen1.ID)
	}
}

func TestRoundRobin_HonoursStickyPrefer(t *testing.T) {
	store := agentkittest.NewMemStore()
	rr := &fleet.RoundRobin{}
	f, _, w2 := newTwoWorkerFleet(t, store, rr)
	ctx := context.Background()

	chosen, err := f.PlaceForSession(ctx, "rr-sticky", fleet.PlacementHint{PreferWorkerID: "w2"})
	if err != nil {
		t.Fatalf("PlaceForSession: %v", err)
	}
	if chosen.ID != "w2" {
		t.Errorf("expected w2 (preferred), got %q", chosen.ID)
	}
	_ = w2
}

// --- Sticky binding ---------------------------------------------------------

func TestStickyBinding_SameWorkerOnSubsequentCalls(t *testing.T) {
	store := agentkittest.NewMemStore()
	f, _, _ := newTwoWorkerFleet(t, store, nil /* default LeastLoaded */)
	ctx := context.Background()

	// First placement.
	first, err := f.PlaceForSession(ctx, "sticky-sess", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("first PlaceForSession: %v", err)
	}

	// Second call with the same sessionID must return the same worker.
	second, err := f.PlaceForSession(ctx, "sticky-sess", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("second PlaceForSession: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("sticky binding broken: first=%q second=%q", first.ID, second.ID)
	}

	// Binding must also be readable via WorkerForSession.
	via, err := f.WorkerForSession(ctx, "sticky-sess")
	if err != nil {
		t.Fatalf("WorkerForSession: %v", err)
	}
	if via.ID != first.ID {
		t.Errorf("WorkerForSession returned %q, want %q", via.ID, first.ID)
	}
}

func TestStickyBinding_PersistedInStore(t *testing.T) {
	store := agentkittest.NewMemStore()
	f, _, _ := newTwoWorkerFleet(t, store, nil)
	ctx := context.Background()

	chosen, err := f.PlaceForSession(ctx, "persist-sess", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("PlaceForSession: %v", err)
	}

	// The binding must be in the store (durable identity).
	workerID, ok, err := store.GetWorkerBinding(ctx, "persist-sess")
	if err != nil {
		t.Fatalf("GetWorkerBinding: %v", err)
	}
	if !ok {
		t.Fatal("binding not found in store")
	}
	if workerID != chosen.ID {
		t.Errorf("store binding = %q, want %q", workerID, chosen.ID)
	}
}

// --- Trust gate at Register -------------------------------------------------

func TestRegister_RejectsSharedUntrustedContainer(t *testing.T) {
	store := agentkittest.NewMemStore()
	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: false})
	env := makeMock(execenv.Capabilities{
		Tenancy:       execenv.TenancyShared,
		IsolationTier: execenv.TierContainer,
		Backend:       execenv.BackendDockerDinD,
	})
	w := &fleet.Worker{ID: "bad-worker", Env: env, Caps: env.Capabilities()}
	err := f.Register(context.Background(), w)
	if err == nil {
		t.Fatal("expected Register to return an error for shared+container+untrusted, got nil")
	}
}

func TestRegister_AllowsSharedTrustedContainer(t *testing.T) {
	store := agentkittest.NewMemStore()
	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: true})
	env := makeMock(execenv.Capabilities{
		Tenancy:       execenv.TenancyShared,
		IsolationTier: execenv.TierContainer,
		Backend:       execenv.BackendDockerDinD,
	})
	w := &fleet.Worker{ID: "ok-worker", Env: env, Caps: env.Capabilities()}
	if err := f.Register(context.Background(), w); err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}
}

func TestRegister_AllowsSharedVM(t *testing.T) {
	store := agentkittest.NewMemStore()
	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: false})
	env := makeMock(execenv.Capabilities{
		Tenancy:       execenv.TenancyShared,
		IsolationTier: execenv.TierVM,
		Backend:       execenv.BackendDockerDinD,
	})
	w := &fleet.Worker{ID: "vm-worker", Env: env, Caps: env.Capabilities()}
	if err := f.Register(context.Background(), w); err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}
}

// --- Deregister / worker-loss -----------------------------------------------

func TestDeregister_WorkerRemovedFromCandidates(t *testing.T) {
	store := agentkittest.NewMemStore()
	f, w1, _ := newTwoWorkerFleet(t, store, nil)
	ctx := context.Background()

	// Deregister w1.
	if err := f.Deregister(ctx, "w1", fleet.DrainGraceful); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// Placement must go to w2.
	chosen, err := f.PlaceForSession(ctx, "post-drain", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("PlaceForSession after deregister: %v", err)
	}
	if chosen.ID == w1.ID {
		t.Errorf("deregistered worker %q was still chosen for placement", w1.ID)
	}
}

// --- Rebind -----------------------------------------------------------------

func TestRebind_ChangesBinding(t *testing.T) {
	store := agentkittest.NewMemStore()
	f, _, _ := newTwoWorkerFleet(t, store, nil)
	ctx := context.Background()

	// Place on whatever worker comes first.
	first, err := f.PlaceForSession(ctx, "rebind-sess", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("PlaceForSession: %v", err)
	}

	// Rebind to the other worker explicitly.
	other := "w1"
	if first.ID == "w1" {
		other = "w2"
	}
	rebound, err := f.Rebind(ctx, "rebind-sess", fleet.PlacementHint{PreferWorkerID: other})
	if err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	if rebound.ID != other {
		t.Errorf("Rebind: got %q, want %q", rebound.ID, other)
	}

	// The store must now reflect the new binding.
	wid, ok, err := store.GetWorkerBinding(ctx, "rebind-sess")
	if err != nil || !ok {
		t.Fatalf("GetWorkerBinding after rebind: ok=%v err=%v", ok, err)
	}
	if wid != other {
		t.Errorf("store binding after rebind = %q, want %q", wid, other)
	}
}
