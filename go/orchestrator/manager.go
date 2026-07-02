package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ManagerExchange is the §2 tick reconciliation: on each trigger it re-derives all
// state from the board + tickets, plans incrementally, verifies In-Review work,
// chooses next work, and spawns workers fire-and-forget under the enforced floors.
// It holds nothing between ticks (statelessness principle) beyond its
// configuration + the ledger.
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
	MaxAttempts                      int // 0 = DefaultMaxAttempts (§10c §D)

	// §10c §B:
	Channel            string      // the single v1 channel name for publish-disposition posts
	DefaultDisposition Disposition // disposition a planned ticket takes when the plan omits one
}

// TickReport is a summary of what one tick did (telemetry / test assertion).
type TickReport struct {
	Planned, Verified, Done, RePlanned, Spawned, Refused int
}

// plannedTicket is the manager planning-output schema (Slice C — the contracts do
// not define how a goal becomes structured tickets; see plan "Contract gaps" §6).
// §10c §B: "disposition" is optional; empty takes ManagerExchange.DefaultDisposition.
type plannedTicket struct {
	Title       string `json:"title"`
	Objective   string `json:"objective"`
	Acceptance  string `json:"acceptance"`
	Disposition string `json:"disposition"`
}

// plan runs EVERY tick (§10c §G — incremental planning, not a one-shot cliff).
// The prompt carries a compact summary of the existing non-terminal tickets and
// asks for ONLY the new ones; planned titles matching ANY existing ticket are
// skipped (title is the v1 identity key — the documented dedup limitation). An
// unparseable reply is not a tick error: it records a "manager-plan-unparseable"
// run, plans 0, and the next tick retries naturally.
func (m *ManagerExchange) plan(ctx context.Context, board agentdb.Board, existing []Ticket) (int, error) {
	prompt, err := Compose(board, m.PlanTemplate, m.Goal)
	if err != nil {
		return 0, fmt.Errorf("plan: %w", err)
	}
	prompt += planStatusAppendix(existing)
	out, _, err := m.Router.For(m.PlanTier).Run(ctx, prompt)
	if err != nil {
		return 0, fmt.Errorf("plan: model: %w", err)
	}
	planned, parsed := parsePlannedTickets(out)
	scope := "manager-plan"
	if !parsed {
		scope = "manager-plan-unparseable"
	}
	if m.Telemetry != nil {
		if _, err := m.Telemetry.Record(ctx, Run{
			Scope: scope, BoardRevision: board.Revision, Prompt: prompt, Output: out,
		}); err != nil {
			return 0, fmt.Errorf("plan: telemetry: %w", err)
		}
	}
	if !parsed {
		return 0, nil // §G: continue the tick; the next tick retries
	}

	seen := map[string]bool{}
	for _, t := range existing {
		seen[t.Title] = true // dedup against ANY status, terminal included
	}
	now := time.Now().Unix()
	created := 0
	for _, p := range planned {
		if seen[p.Title] {
			continue // §G title dedup: a repeating plan is stable
		}
		seen[p.Title] = true
		disposition := Disposition(p.Disposition)
		if disposition == "" {
			disposition = m.DefaultDisposition
		}
		id, err := m.Tickets.Create(ctx, Ticket{
			ProjectID: m.ProjectID, Title: p.Title, Objective: p.Objective, Acceptance: p.Acceptance,
			Status: StatusTodo, Disposition: disposition,
			Parent: m.ManagerSession, BoardRev: board.Revision, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return created, fmt.Errorf("plan: create ticket: %w", err)
		}
		scope := Scope{
			Name: p.Title, Template: m.WorkerTemplate, Input: p.Objective,
			Tier: m.WorkerTier, Budget: m.WorkerBudget, Parent: m.ManagerSession, TicketID: id,
		}
		raw, err := json.Marshal(scope)
		if err != nil {
			return created, err
		}
		tk, _ := m.Tickets.Get(ctx, id)
		tk.Scope = raw
		if err := m.Tickets.Update(ctx, tk); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// planStatusAppendix renders the "- [status] title" summary of non-terminal
// tickets plus the JSON-only instruction, appended to the composed plan prompt
// (the PlanTemplate contract is unchanged: {{input}} is still the goal).
func planStatusAppendix(existing []Ticket) string {
	var b strings.Builder
	b.WriteString("\n\nTickets already on the board (do NOT re-plan these; a matching title is skipped):\n")
	listed := 0
	for _, t := range existing {
		if t.Status == StatusDone {
			continue // terminal — not part of the live plan
		}
		fmt.Fprintf(&b, "- [%s] %s\n", t.Status, t.Title)
		listed++
	}
	if listed == 0 {
		b.WriteString("(none)\n")
	}
	b.WriteString("\nReturn ONLY a JSON array of NEW tickets needed — objects with " +
		`"title", "objective", "acceptance", and optional "disposition" ("internal" or "publish")` +
		" — and return [] when none are needed.")
	return b.String()
}

// parsePlannedTickets extracts the first '['..last ']' substring (tolerating
// ```json fences and prose preambles — §10c §G) and unmarshals it.
func parsePlannedTickets(out string) ([]plannedTicket, bool) {
	start := strings.Index(out, "[")
	end := strings.LastIndex(out, "]")
	if start < 0 || end < start {
		return nil, false
	}
	var planned []plannedTicket
	if err := json.Unmarshal([]byte(out[start:end+1]), &planned); err != nil {
		return nil, false
	}
	return planned, true
}

// reconcile verifies each In-Review ticket against its acceptance (via a separate
// verify-scope) and applies the verdict through the §D state machine: pass →
// Done for internal work, or — the disposition hop — a filed PendingPost on
// needs_human for publish work (the ONE wire into the approval gate); fail →
// back to todo with the Reason in AttemptNotes, needs_human at the attempts cap.
// Then it clears any Blocked ticket whose deps are all Done.
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
			Transition(&t, EvVerifyPassed, "", m.MaxAttempts)
			if uerr := m.Tickets.Update(ctx, t); uerr != nil {
				return verified, done, replanned, uerr
			}
			if t.Disposition == DispositionPublish {
				// §10c §D: verified publishable work becomes a PendingPost
				// awaiting the human gate — never straight to Done/published.
				post := Post{Channel: m.Channel, Text: r.Output}
				if perr := FilePendingPost(ctx, m.Tickets, t.ID, post); perr != nil {
					return verified, done, replanned, perr
				}
			} else {
				done++
			}
		} else {
			// §10c §D: the verdict Reason lands in AttemptNotes (EvVerifyFailed)
			// — this is how the failure reason reaches the retry prompt (§B).
			Transition(&t, EvVerifyFailed, verdict.Reason, m.MaxAttempts)
			if uerr := m.Tickets.Update(ctx, t); uerr != nil {
				return verified, done, replanned, uerr
			}
			replanned++
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
		unblock(&t)
		if uerr := m.Tickets.Update(ctx, t); uerr != nil {
			return verified, done, replanned, uerr
		}
	}
	return verified, done, replanned, nil
}

// chooseAndSpawn selects runnable Todo tickets and spawns their worker scope
// fire-and-forget under the enforced floors. §10c §D spawn-error handling — a
// ticket can never be stranded in_progress:
//
//   - floor refusal → EvFloorRefused (needs_human, fail-loud, counted refused);
//   - ErrSpendCeiling → EvSpawnFailed (revert to todo) + a "manager-spend-halt"
//     run, then STOP dispatch for this tick (budget halt; retried when the
//     ceiling lifts);
//   - any other error → EvSpawnFailed + a "manager-spawn-error" run, then
//     CONTINUE with the next ticket.
//
// §10c §B: a retry (AttemptNotes non-empty) composes the accumulated feedback
// into the worker's input, so verify Reasons / reject notes / answers reach the
// next attempt.
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
		if len(t.AttemptNotes) > 0 {
			scope.Input = t.Objective + "\n\nFeedback on previous attempts (address ALL of it):\n- " +
				strings.Join(t.AttemptNotes, "\n- ")
		}
		Transition(&t, EvSpawned, "", m.MaxAttempts)
		t.BoardRev = board.Revision
		if uerr := m.Tickets.Update(ctx, t); uerr != nil {
			return spawned, refused, uerr
		}
		if _, serr := m.Runtime.Spawn(ctx, scope); serr != nil {
			switch {
			case isFloorRefusal(serr):
				if terr := m.recordRun(ctx, Run{
					Scope: "manager-refuse", BoardRevision: board.Revision, Output: serr.Error(),
					TicketID: t.ID, // §10c §C: refuse runs are attributable to their ticket
				}); terr != nil {
					return spawned, refused, fmt.Errorf("choose: telemetry: %w", terr)
				}
				Transition(&t, EvFloorRefused, "", m.MaxAttempts) // fail-loud: surface, never silently drop
				if uerr := m.Tickets.Update(ctx, t); uerr != nil {
					return spawned, refused, uerr
				}
				refused++
				continue
			case errors.Is(serr, ErrSpendCeiling):
				Transition(&t, EvSpawnFailed, "", m.MaxAttempts) // transient revert, no attempt burned
				if uerr := m.Tickets.Update(ctx, t); uerr != nil {
					return spawned, refused, uerr
				}
				if terr := m.recordRun(ctx, Run{
					Scope: "manager-spend-halt", BoardRevision: board.Revision, Output: serr.Error(),
					TicketID: t.ID,
				}); terr != nil {
					return spawned, refused, fmt.Errorf("choose: telemetry: %w", terr)
				}
				return spawned, refused, nil // budget halt: STOP dispatch this tick
			default:
				Transition(&t, EvSpawnFailed, "", m.MaxAttempts) // transient revert, no attempt burned
				if uerr := m.Tickets.Update(ctx, t); uerr != nil {
					return spawned, refused, uerr
				}
				if terr := m.recordRun(ctx, Run{
					Scope: "manager-spawn-error", BoardRevision: board.Revision, Output: serr.Error(),
					TicketID: t.ID,
				}); terr != nil {
					return spawned, refused, fmt.Errorf("choose: telemetry: %w", terr)
				}
				continue // next ticket
			}
		}
		spawned++
	}
	return spawned, refused, nil
}

// recordRun writes to Telemetry when it is configured.
func (m *ManagerExchange) recordRun(ctx context.Context, r Run) error {
	if m.Telemetry == nil {
		return nil
	}
	_, err := m.Telemetry.Record(ctx, r)
	return err
}

// Tick runs one manager exchange (the §2 tick): re-derive → plan incrementally
// (§10c §G: every tick) → reconcile In-Review → choose+spawn next work. It holds
// nothing between ticks.
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
	if rep.Planned, err = m.plan(ctx, board, existing); err != nil {
		return rep, err
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
