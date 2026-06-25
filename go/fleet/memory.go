package fleet

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/bayes-price/agentkit/execenv"
)

// WorkerStore is the minimal store interface the fleet requires for durable
// session→worker bindings. Both *agentdb.Store and agentkittest.MemStore satisfy this.
type WorkerStore interface {
	GetWorkerBinding(ctx context.Context, sessionID string) (string, bool, error)
	SetWorkerBinding(ctx context.Context, sessionID, workerID string) error
	ClearWorkerBinding(ctx context.Context, sessionID string) error
}

// memFleet is an in-memory Fleet backed by the injected WorkerStore for
// durable session→worker bindings. It is the default single-host implementation
// and the test double for fleet-layer tests.
type memFleet struct {
	store           WorkerStore
	policy          PlacementPolicy
	trustedWorkload bool

	mu      sync.RWMutex
	workers map[string]*Worker // workerID -> Worker
	drained map[string]bool    // workerID -> true when draining (no new placement)
}

// MemFleetOptions configures NewMemory.
type MemFleetOptions struct {
	// Policy is the placement policy; nil defaults to &LeastLoaded{}.
	Policy PlacementPolicy
	// TrustedWorkload mirrors Policy.TrustedWorkload for the trust gate at Register.
	TrustedWorkload bool
}

// NewMemory returns a new in-memory Fleet. The store is used as the source of
// truth for session→worker bindings. options may be nil.
//
// TODO(AG-6): validate PortableHandles for multi-worker fleets. When the fleet
// has more than one worker, snapshot handles used for cross-worker restore must be
// worker-portable (e.g. blobarchive with a shared BlobStore). imageregistry.Capabilities
// does not yet expose a PortableHandles flag — that lands in AG-6.
func NewMemory(store WorkerStore, options *MemFleetOptions) Fleet {
	f := &memFleet{
		store:   store,
		workers: map[string]*Worker{},
		drained: map[string]bool{},
	}
	if options != nil {
		f.trustedWorkload = options.TrustedWorkload
		if options.Policy != nil {
			f.policy = options.Policy
		}
	}
	if f.policy == nil {
		f.policy = &LeastLoaded{}
	}
	return f
}

// Register adds a worker to the fleet after validating the AG-1 trust gate:
// shared-tenancy without TrustedWorkload requires IsolationTier >= TierVM.
func (f *memFleet) Register(ctx context.Context, w *Worker) error {
	if w == nil {
		return fmt.Errorf("fleet: cannot register nil worker")
	}
	if w.ID == "" {
		return fmt.Errorf("fleet: cannot register worker with empty ID")
	}
	// AG-1 trust gate (mirrored from NewRunner, now enforced here so the shim path
	// still validates when a bare ExecutionEnvironment is wrapped).
	caps := w.Caps
	if caps.Tenancy == execenv.TenancyShared &&
		!(f.trustedWorkload || caps.IsolationTier >= execenv.TierVM) {
		return fmt.Errorf(
			"fleet: shared-tenancy worker %q requires TrustedWorkload or IsolationTier>=TierVM (got backend=%s tier=%d)",
			w.ID, caps.Backend, caps.IsolationTier,
		)
	}

	f.mu.Lock()
	f.workers[w.ID] = w
	delete(f.drained, w.ID)
	f.mu.Unlock()
	return nil
}

// Deregister removes a worker. DrainGraceful stops new placement; DrainImmediate
// also marks the worker as immediately unavailable.
func (f *memFleet) Deregister(ctx context.Context, workerID string, mode DrainMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mode == DrainGraceful {
		// Mark draining: existing bindings remain, new placement skips this worker.
		f.drained[workerID] = true
	}
	// Remove the worker in both modes — it can no longer receive new sessions.
	delete(f.workers, workerID)
	return nil
}

// Workers returns a snapshot of the currently registered (and not-drained) workers.
func (f *memFleet) Workers(ctx context.Context) ([]*Worker, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*Worker, 0, len(f.workers))
	for _, w := range f.workers {
		out = append(out, w)
	}
	return out, nil
}

// PlaceForSession returns the worker a session is bound to, placing it on first
// call. On subsequent calls it returns the same worker (sticky). SessionStore is
// the source of truth; an in-memory hit is an optimisation.
func (f *memFleet) PlaceForSession(ctx context.Context, sessionID string, hint PlacementHint) (*Worker, error) {
	// Check the durable binding first.
	workerID, ok, err := f.store.GetWorkerBinding(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("fleet: GetWorkerBinding: %w", err)
	}
	if ok {
		// Binding already exists — return the worker if still registered.
		f.mu.RLock()
		w, exists := f.workers[workerID]
		f.mu.RUnlock()
		if exists {
			return w, nil
		}
		// Worker is gone — fall through to place on a healthy candidate.
		// (The runner's worker-loss path handles the restore logic.)
	}

	candidates := f.healthyCandidates()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("fleet: no healthy workers available for session %q", sessionID)
	}
	chosen, err := f.policy.Pick(candidates, hint)
	if err != nil {
		return nil, fmt.Errorf("fleet: placement policy: %w", err)
	}
	if err := f.store.SetWorkerBinding(ctx, sessionID, chosen.ID); err != nil {
		return nil, fmt.Errorf("fleet: SetWorkerBinding: %w", err)
	}
	return chosen, nil
}

// WorkerForSession returns the already-bound worker without triggering placement.
// Returns an error if no binding exists.
func (f *memFleet) WorkerForSession(ctx context.Context, sessionID string) (*Worker, error) {
	workerID, ok, err := f.store.GetWorkerBinding(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("fleet: GetWorkerBinding: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("fleet: no binding for session %q", sessionID)
	}
	f.mu.RLock()
	w, exists := f.workers[workerID]
	f.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("fleet: bound worker %q for session %q is no longer registered", workerID, sessionID)
	}
	return w, nil
}

// Rebind moves a session to a new worker (restore-to-different-worker or drain).
// It picks a fresh worker honouring hint and persists the new binding.
func (f *memFleet) Rebind(ctx context.Context, sessionID string, hint PlacementHint) (*Worker, error) {
	candidates := f.healthyCandidates()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("fleet: no healthy workers available for rebind of session %q", sessionID)
	}
	chosen, err := f.policy.Pick(candidates, hint)
	if err != nil {
		return nil, fmt.Errorf("fleet: rebind placement policy: %w", err)
	}
	if err := f.store.SetWorkerBinding(ctx, sessionID, chosen.ID); err != nil {
		return nil, fmt.Errorf("fleet: SetWorkerBinding (rebind): %w", err)
	}
	return chosen, nil
}

// healthyCandidates returns registered, non-drained workers in a deterministic
// order (sorted by worker ID). Determinism matters: the map iteration order is
// randomized per call, which would make RoundRobin placement (and LeastLoaded
// tie-breaking) non-reproducible.
func (f *memFleet) healthyCandidates() []*Worker {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*Worker, 0, len(f.workers))
	for id, w := range f.workers {
		if !f.drained[id] {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Compile-time assertion.
var _ Fleet = (*memFleet)(nil)
