// Package fleet is the placement layer between the Runner and a pool of
// ExecutionEnvironment workers. It answers one question — "for session S, which
// worker runs it?" — and makes the binding sticky and durable so the
// orchestration core stays stateless across host replicas.
//
// See agent-library/docs/13-fleet-placement.md.
package fleet

import (
	"context"

	"github.com/binocarlos/badcode-agent-orange/execenv"
)

// Fleet is the seam the Runner calls when it needs a worker for a session.
// A single-worker deployment is just a one-worker fleet; see NewMemory.
type Fleet interface {
	// PlaceForSession returns the worker a session runs on, creating a sticky
	// binding on first placement and returning the existing one thereafter.
	PlaceForSession(ctx context.Context, sessionID string, hint PlacementHint) (*Worker, error)

	// WorkerForSession returns the already-bound worker (no placement); returns
	// an error if no binding has been established yet.
	WorkerForSession(ctx context.Context, sessionID string) (*Worker, error)

	// Rebind moves a session to a new worker (restore-to-different-worker or
	// drain). Picks a new worker honouring hint and persists it. Returns the
	// newly bound worker.
	Rebind(ctx context.Context, sessionID string, hint PlacementHint) (*Worker, error)

	// Register adds a worker to the fleet. Returns an error if the worker fails
	// the AG-1 trust gate.
	Register(ctx context.Context, w *Worker) error

	// Deregister removes a worker from the fleet. Graceful drain stops new
	// placement; Immediate marks existing bindings stale.
	Deregister(ctx context.Context, workerID string, mode DrainMode) error

	// Workers returns all currently registered workers.
	Workers(ctx context.Context) ([]*Worker, error)
}

// PlacementHint carries soft preferences for session placement. All fields are
// optional; the placement policy uses what it can.
type PlacementHint struct {
	// PreferWorkerID: if non-empty and the named worker is present and healthy,
	// use it (sticky-restore hint).
	PreferWorkerID string
	// Labels are affinity hints (zone, image, …).
	Labels map[string]string
	// Tenancy lets the caller narrow candidates to a specific tenancy model.
	Tenancy execenv.Tenancy
}

// DrainMode controls how aggressively a Deregister evicts bindings.
type DrainMode int

const (
	// DrainGraceful stops new placement on the worker but lets existing sessions
	// finish. On idle, sessions are snapshot-rebind-cycled elsewhere.
	DrainGraceful DrainMode = iota
	// DrainImmediate immediately marks all bindings for the worker as stale.
	DrainImmediate
)
