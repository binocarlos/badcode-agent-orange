package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ManagerExchange is the §2 tick reconciliation: on each trigger it re-derives all
// state from the board + tickets, verifies In-Review work, chooses next work, and
// spawns workers fire-and-forget under the enforced floors. It holds nothing
// between ticks (statelessness principle) beyond its configuration + the ledger.
type ManagerExchange struct {
	Board     agentdb.BoardStore
	Tickets   TicketStore
	Router    ModelRouter
	Runtime   WorkerRuntime
	Ledger    *SpawnLedger
	Telemetry Telemetry

	Goal           string
	ProjectID      string
	ManagerSession string // the root session id for the floors (depth 0)

	PlanTier, WorkerTier, VerifyTier ModelTier
	WorkerBudget                     Budget
	PlanTemplate, WorkerTemplate     string
	MaxAttempts                      int
}

// TickReport is a summary of what one tick did (telemetry / test assertion).
type TickReport struct {
	Planned, Verified, Done, RePlanned, Spawned, Refused int
}

// plannedTicket is the manager planning-output schema (Slice C — the contracts do
// not define how a goal becomes structured tickets; see plan "Contract gaps" §6).
type plannedTicket struct {
	Title      string `json:"title"`
	Objective  string `json:"objective"`
	Acceptance string `json:"acceptance"`
}

// plan turns the goal into Todo tickets when none exist yet (contracts §2).
func (m *ManagerExchange) plan(ctx context.Context, board agentdb.Board) (int, error) {
	prompt, err := Compose(board, m.PlanTemplate, m.Goal)
	if err != nil {
		return 0, fmt.Errorf("plan: %w", err)
	}
	out, err := m.Router.For(m.PlanTier).Run(ctx, prompt)
	if err != nil {
		return 0, fmt.Errorf("plan: model: %w", err)
	}
	if m.Telemetry != nil {
		if _, err := m.Telemetry.Record(ctx, Run{
			Scope: "manager-plan", BoardRevision: board.Revision, Prompt: prompt, Output: out,
		}); err != nil {
			return 0, fmt.Errorf("plan: telemetry: %w", err)
		}
	}
	var planned []plannedTicket
	if err := json.Unmarshal([]byte(out), &planned); err != nil {
		return 0, fmt.Errorf("plan: parse plan output: %w", err)
	}
	now := time.Now().Unix()
	for _, p := range planned {
		id, err := m.Tickets.Create(ctx, Ticket{
			ProjectID: m.ProjectID, Title: p.Title, Objective: p.Objective, Acceptance: p.Acceptance,
			Status: StatusTodo, Parent: m.ManagerSession, BoardRev: board.Revision, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return 0, fmt.Errorf("plan: create ticket: %w", err)
		}
		scope := Scope{
			Name: p.Title, Template: m.WorkerTemplate, Input: p.Objective,
			Tier: m.WorkerTier, Budget: m.WorkerBudget, Parent: m.ManagerSession, TicketID: id,
		}
		raw, err := json.Marshal(scope)
		if err != nil {
			return 0, err
		}
		tk, _ := m.Tickets.Get(ctx, id)
		tk.Scope = raw
		if err := m.Tickets.Update(ctx, tk); err != nil {
			return 0, err
		}
	}
	return len(planned), nil
}

// reconcile verifies each In-Review ticket against its acceptance (via a separate
// verify-scope) and moves it to Done or back to re-plan / Needs-Human, then clears
// any Blocked ticket whose deps are all Done.
func (m *ManagerExchange) reconcile(ctx context.Context) (verified, done, replanned int, err error) {
	tickets, err := m.Tickets.List(ctx, "")
	if err != nil {
		return 0, 0, 0, err
	}
	statusByID := map[string]TicketStatus{}
	for _, t := range tickets {
		statusByID[t.ID] = t.Status
	}
	for _, t := range tickets {
		if t.Status != StatusInReview {
			continue
		}
		var r Result
		if len(t.Result) > 0 {
			_ = json.Unmarshal(t.Result, &r)
		}
		verdict, verr := Verify(ctx, m.Router, m.VerifyTier, t, r)
		if verr != nil {
			return verified, done, replanned, verr
		}
		verified++
		if verdict.Pass {
			t.Status = StatusDone
			done++
		} else {
			t.Attempts++
			if t.Attempts >= m.MaxAttempts {
				t.Status = StatusNeedsHuman // fail-loud: out of re-plan attempts
			} else {
				t.Status = StatusTodo // re-plan: back to the queue for another attempt
			}
			replanned++
		}
		t.UpdatedAt = time.Now().Unix()
		if uerr := m.Tickets.Update(ctx, t); uerr != nil {
			return verified, done, replanned, uerr
		}
		statusByID[t.ID] = t.Status
	}
	// Clear blocked deps: a Blocked ticket whose deps are all Done → Todo.
	for _, t := range tickets {
		if t.Status != StatusBlocked {
			continue
		}
		if !depsSatisfied(t, statusByID) {
			continue
		}
		t.Status = StatusTodo
		t.UpdatedAt = time.Now().Unix()
		if uerr := m.Tickets.Update(ctx, t); uerr != nil {
			return verified, done, replanned, uerr
		}
	}
	return verified, done, replanned, nil
}

// chooseAndSpawn selects runnable Todo tickets and spawns their worker scope
// fire-and-forget under the enforced floors. A floor refusal fails loud: the ticket
// goes Needs-Human (never silently dropped) and is counted refused.
func (m *ManagerExchange) chooseAndSpawn(ctx context.Context) (spawned, refused int, err error) {
	tickets, err := m.Tickets.List(ctx, "")
	if err != nil {
		return 0, 0, err
	}
	statusByID := map[string]TicketStatus{}
	for _, t := range tickets {
		statusByID[t.ID] = t.Status
	}
	board, err := m.Board.Current(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, t := range tickets {
		if t.Status != StatusTodo || !depsSatisfied(t, statusByID) {
			continue
		}
		if len(t.Scope) == 0 {
			continue // nothing to run (should not happen post-plan)
		}
		var scope Scope
		if uerr := json.Unmarshal(t.Scope, &scope); uerr != nil {
			return spawned, refused, fmt.Errorf("choose: scope of %s: %w", t.ID, uerr)
		}
		scope.TicketID = t.ID
		scope.Parent = m.ManagerSession
		t.Status = StatusInProgress
		t.BoardRev = board.Revision
		t.UpdatedAt = time.Now().Unix()
		if uerr := m.Tickets.Update(ctx, t); uerr != nil {
			return spawned, refused, uerr
		}
		if _, serr := m.Runtime.Spawn(ctx, scope); serr != nil {
			if isFloorRefusal(serr) {
				if m.Telemetry != nil {
					if _, terr := m.Telemetry.Record(ctx, Run{
						Scope: "manager-refuse", BoardRevision: board.Revision, Output: serr.Error(),
					}); terr != nil {
						return spawned, refused, fmt.Errorf("choose: telemetry: %w", terr)
					}
				}
				t.Status = StatusNeedsHuman // fail-loud: surface, never silently drop
				t.UpdatedAt = time.Now().Unix()
				if uerr := m.Tickets.Update(ctx, t); uerr != nil {
					return spawned, refused, uerr
				}
				refused++
				continue
			}
			return spawned, refused, fmt.Errorf("choose: spawn %s: %w", t.ID, serr)
		}
		spawned++
	}
	return spawned, refused, nil
}

// Tick runs one manager exchange (the §2 tick): re-derive → plan if empty →
// reconcile In-Review → choose+spawn next work. It holds nothing between ticks.
func (m *ManagerExchange) Tick(ctx context.Context) (TickReport, error) {
	m.Ledger.RegisterRoot(m.ManagerSession, m.WorkerBudget)
	board, err := m.Board.Current(ctx)
	if err != nil {
		return TickReport{}, err
	}
	existing, err := m.Tickets.List(ctx, "")
	if err != nil {
		return TickReport{}, err
	}
	var rep TickReport
	if len(existing) == 0 {
		if rep.Planned, err = m.plan(ctx, board); err != nil {
			return rep, err
		}
	}
	if rep.Verified, rep.Done, rep.RePlanned, err = m.reconcile(ctx); err != nil {
		return rep, err
	}
	if rep.Spawned, rep.Refused, err = m.chooseAndSpawn(ctx); err != nil {
		return rep, err
	}
	return rep, nil
}

func depsSatisfied(t Ticket, status map[string]TicketStatus) bool {
	for _, dep := range t.DependsOn {
		if status[dep] != StatusDone {
			return false
		}
	}
	return true
}

func isFloorRefusal(err error) bool {
	return errors.Is(err, ErrMaxDepth) || errors.Is(err, ErrMaxSpawns) ||
		errors.Is(err, ErrTreeExhausted) || errors.Is(err, ErrUnknownParent)
}

// ExchangeTrigger adapts a ManagerExchange to the frozen Triggerer seam (§10b S-2):
// ManagerExchange.Tick returns (TickReport, error), which cannot satisfy
// Triggerer.Tick(ctx) error directly, so this thin adapter discards the report.
// Slice E's POST /api/trigger binds to a Triggerer.
type ExchangeTrigger struct {
	Exchange *ManagerExchange
}

func (t ExchangeTrigger) Tick(ctx context.Context) error {
	_, err := t.Exchange.Tick(ctx)
	return err
}

var _ Triggerer = ExchangeTrigger{}
