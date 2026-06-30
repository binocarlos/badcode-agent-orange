package orchestrator

import (
	"fmt"
	"sync"
)

// Run is one scope execution, pinned to the board revision it ran against — the
// "show your work" record the learning narrative is told from.
type Run struct {
	ID            string
	Scope         string
	BoardRevision string
	Prompt        string
	Output        string
	Seq           int
}

// Telemetry is an append-only in-memory run log (the CBR case base, minimally).
type Telemetry struct {
	mu   sync.Mutex
	runs []Run
}

// NewTelemetry returns an empty run log.
func NewTelemetry() *Telemetry { return &Telemetry{} }

// Record appends a run, assigning its 1-based Seq and id, and returns it.
func (t *Telemetry) Record(r Run) Run {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.Seq = len(t.runs) + 1
	r.ID = fmt.Sprintf("run%d", r.Seq)
	t.runs = append(t.runs, r)
	return r
}

// Runs returns a copy of the recorded runs in order.
func (t *Telemetry) Runs() []Run {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Run(nil), t.runs...)
}
