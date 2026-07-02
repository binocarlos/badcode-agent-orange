package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// WorkerRuntime and ResultSink are frozen in contracts.go (§5). This file provides
// the Slice-C in-process impls; the DinD runtime (Slice F) swaps in behind the same
// seams.

// InProcRuntime runs a scope in-process via the ModelRouter and delivers a Result
// to its ResultSink. It is synchronous for determinism; the fire-and-forget
// boundary is preserved at the manager exchange (results are read next tick).
//
// §10b E-3 note (deferred to Slice F): the in-proc runtime composes internally from
// the board it holds. Slice F's DinD runtime cannot read the board, so composition
// moves orchestrator-side (Scope.Prompt) then. The Spawn seam is unchanged either way.
type InProcRuntime struct {
	Board     agentdb.BoardStore
	Router    ModelRouter
	Sink      ResultSink
	Ledger    *SpawnLedger
	Telemetry Telemetry
}

func (rt *InProcRuntime) Spawn(ctx context.Context, s Scope) (string, error) {
	sid, err := rt.Ledger.Admit(s) // FLOOR CHECK — refuse before doing any work
	if err != nil {
		return "", err
	}
	// §10c §F: the admitted slot is IN-FLIGHT capacity — any terminal outcome
	// (delivered result, model failure, compose failure) frees it, so a parent's
	// MaxSpawns caps concurrent fan-out, not lifetime children. NOT released on
	// Admit refusal above (nothing was admitted).
	defer rt.Ledger.Release(sid)
	board, err := rt.Board.Current(ctx)
	if err != nil {
		return "", fmt.Errorf("worker %s: current board: %w", s.Name, err)
	}
	prompt, err := Compose(board, s.Template, s.Input)
	if err != nil {
		return "", fmt.Errorf("worker %s: %w", s.Name, err)
	}
	out, usage, err := rt.Router.For(s.Tier).Run(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("worker %s: model: %w", s.Name, err)
	}

	// S-4 (contracts §10b): the shared worker-completion convention maps raw output
	// to a status + cleaned text, identical for the in-proc and DinD runtimes.
	status, text := ClassifyWorkerOutput(out)
	r := Result{SessionID: sid, TicketID: s.TicketID, Output: text, Status: status}
	// §10c §A: one currency — the model's reported usage feeds both the Result
	// and the tree-token ledger (no more char-count estimate).
	r.TokensUsed = usage.Total()
	_ = rt.Ledger.Charge(sid, r.TokensUsed)

	if rt.Telemetry != nil {
		if _, err := rt.Telemetry.Record(ctx, Run{
			Scope: s.Name, BoardRevision: board.Revision, Prompt: prompt, Output: out,
			TicketID: s.TicketID, SessionID: sid, // §10c §C: run attribution
		}); err != nil {
			return "", fmt.Errorf("worker %s: telemetry: %w", s.Name, err)
		}
	}
	if err := rt.Sink.Deliver(ctx, r); err != nil {
		return "", fmt.Errorf("worker %s: sink: %w", s.Name, err)
	}
	return sid, nil
}

var _ WorkerRuntime = (*InProcRuntime)(nil)

// TicketResultSink lands a worker Result on its ticket (contracts §2/§5): In-Review
// for a normal draft, Needs-Human for an escalation or failure (fail-loud).
type TicketResultSink struct {
	Tickets TicketStore
}

func (s *TicketResultSink) Deliver(ctx context.Context, r Result) error {
	t, err := s.Tickets.Get(ctx, r.TicketID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	t.Result = body
	// §10c §D: lane moves route through the ONE transition function.
	switch r.Status {
	case ResultEscalated, ResultFailed:
		Transition(&t, EvEscalated, "", 0)
	default:
		Transition(&t, EvDelivered, "", 0)
	}
	return s.Tickets.Update(ctx, t)
}

var _ ResultSink = (*TicketResultSink)(nil)
