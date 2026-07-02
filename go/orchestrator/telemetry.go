package orchestrator

import (
	"context"
	"fmt"
	"sync"
)

// Run is one scope execution, pinned to the board revision it ran against — the
// "show your work" record the learning narrative is told from. §10c §C: TicketID/
// SessionID make a run joinable to the work it served.
type Run struct {
	ID            string
	Scope         string
	BoardRevision string
	Prompt        string
	Output        string
	TicketID      string // the ticket this run served ("" for manager-exchange-level runs)
	SessionID     string // the worker session id ("" for non-worker runs)
	Seq           int
}

// MemTelemetry is an in-memory Telemetry: an append-only run log (the CBR case
// base, minimally). It is the Slice-0 / test double; the Postgres impl (Slice A)
// satisfies the same Telemetry seam.
type MemTelemetry struct {
	mu   sync.Mutex
	runs []Run
}

// NewTelemetry returns an empty in-memory run log.
func NewTelemetry() *MemTelemetry { return &MemTelemetry{} }

// Record appends a run, assigning its 1-based Seq and id, and returns it. The
// ctx+error signature (contracts §10b E-1) lets the Postgres impl fail loud rather
// than lose a run silently; the in-memory impl never errors.
func (t *MemTelemetry) Record(_ context.Context, r Run) (Run, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.Seq = len(t.runs) + 1
	r.ID = fmt.Sprintf("run%d", r.Seq)
	t.runs = append(t.runs, r)
	return r, nil
}

// Runs returns a copy of the recorded runs in order.
func (t *MemTelemetry) Runs(_ context.Context) ([]Run, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Run(nil), t.runs...), nil
}

var _ Telemetry = (*MemTelemetry)(nil)
