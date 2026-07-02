package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// --- failing-port fakes (in-package test doubles) ---

// failBoard is an agentdb.BoardStore whose every method errors.
type failBoard struct{ err error }

func (f failBoard) Append(context.Context, agentdb.Changeset) (string, error) { return "", f.err }
func (f failBoard) Current(context.Context) (agentdb.Board, error)            { return agentdb.Board{}, f.err }
func (f failBoard) AsOf(context.Context, string) (agentdb.Board, error)       { return agentdb.Board{}, f.err }
func (f failBoard) Head(context.Context) (string, error) { return "", f.err }
func (f failBoard) Revisions(context.Context) ([]agentdb.BoardRevision, error) {
	return nil, f.err
}

var _ agentdb.BoardStore = failBoard{}

// flakyTickets wraps a real TicketStore and fails selected methods.
type flakyTickets struct {
	TicketStore
	listErr, updateErr, getErr, createErr error
}

func (f *flakyTickets) List(ctx context.Context, s TicketStatus) ([]Ticket, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.TicketStore.List(ctx, s)
}

func (f *flakyTickets) Update(ctx context.Context, t Ticket) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	return f.TicketStore.Update(ctx, t)
}

func (f *flakyTickets) Get(ctx context.Context, id string) (Ticket, error) {
	if f.getErr != nil {
		return Ticket{}, f.getErr
	}
	return f.TicketStore.Get(ctx, id)
}

func (f *flakyTickets) Create(ctx context.Context, t Ticket) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.TicketStore.Create(ctx, t)
}

// failTelemetry errors on every Record/Runs (telemetry loss must fail loud —
// contracts §10b E-1).
type failTelemetry struct{ err error }

func (f failTelemetry) Record(context.Context, Run) (Run, error) { return Run{}, f.err }
func (f failTelemetry) Runs(context.Context) ([]Run, error)      { return nil, f.err }

var _ Telemetry = failTelemetry{}

// errBoom is shared with worker_test.go (same package).

// --- Tick error paths ---

func TestTickBoardCurrentFailureFailsTheTick(t *testing.T) {
	m, _, _ := newTestExchange(t, planRouter(`[]`, ""), "goal")
	m.Board = failBoard{err: errBoom}
	if _, err := m.Tick(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("Tick with failing Board.Current: err = %v, want errBoom", err)
	}
}

func TestTickTicketsListFailureFailsTheTick(t *testing.T) {
	m, _, tickets := newTestExchange(t, planRouter(`[]`, ""), "goal")
	m.Tickets = &flakyTickets{TicketStore: tickets, listErr: errBoom}
	if _, err := m.Tick(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("Tick with failing Tickets.List: err = %v, want errBoom", err)
	}
}

// --- plan error paths ---

func TestPlanComposeFailureUnknownFragment(t *testing.T) {
	m, _, _ := newTestExchange(t, planRouter(`[]`, ""), "goal")
	m.PlanTemplate = "{{fragment:does-not-exist}} {{input}}"
	_, err := m.Tick(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown fragment") {
		t.Fatalf("plan compose failure: err = %v", err)
	}
}

func TestPlanModelFailure(t *testing.T) {
	m, _, _ := newTestExchange(t, ScriptedRouter{TierFull: failModel{err: errBoom}}, "goal")
	_, err := m.Tick(context.Background())
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "plan: model") {
		t.Fatalf("plan model failure: err = %v", err)
	}
}

func TestPlanTelemetryFailureFailsTheTick(t *testing.T) {
	m, _, _ := newTestExchange(t, planRouter(`[]`, ""), "goal")
	m.Telemetry = failTelemetry{err: errBoom}
	_, err := m.Tick(context.Background())
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "plan: telemetry") {
		t.Fatalf("plan telemetry failure: err = %v", err)
	}
}

func TestPlanCreateTicketFailure(t *testing.T) {
	planJSON := `[{"title":"a","objective":"o","acceptance":"c"}]`
	m, _, tickets := newTestExchange(t, planRouter(planJSON, ""), "goal")
	m.Tickets = &flakyTickets{TicketStore: tickets, createErr: errBoom}
	_, err := m.Tick(context.Background())
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "plan: create ticket") {
		t.Fatalf("plan create failure: err = %v", err)
	}
}

// --- reconcile error paths ---

func TestReconcileVerifyModelFailure(t *testing.T) {
	// Plan-less exchange: seed an in_review ticket, then make the verify tier fail.
	m, _, tickets := newTestExchange(t, ScriptedRouter{TierFull: failModel{err: errBoom}}, "goal")
	ctx := context.Background()
	res, _ := json.Marshal(Result{Output: "the work"})
	if _, err := tickets.Create(ctx, Ticket{Title: "w", Status: StatusInReview, Result: res, Acceptance: "good"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, _, err := m.reconcile(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("reconcile verify failure: err = %v, want errBoom", err)
	}
}

func TestReconcileUpdateFailureAfterVerdict(t *testing.T) {
	m, _, tickets := newTestExchange(t, ScriptedRouter{TierFull: &ScriptedModel{Default: "PASS: fine"}}, "goal")
	ctx := context.Background()
	res, _ := json.Marshal(Result{Output: "the work"})
	if _, err := tickets.Create(ctx, Ticket{Title: "w", Status: StatusInReview, Result: res, Acceptance: "good"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	m.Tickets = &flakyTickets{TicketStore: tickets, updateErr: errBoom}
	if _, _, _, err := m.reconcile(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("reconcile update failure: err = %v, want errBoom", err)
	}
}

// --- chooseAndSpawn branches ---

func TestChooseAndSpawnBoardCurrentFailure(t *testing.T) {
	m, _, _ := newTestExchange(t, planRouter(`[]`, ""), "goal")
	m.Board = failBoard{err: errBoom}
	if _, _, err := m.chooseAndSpawn(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("chooseAndSpawn board failure: err = %v, want errBoom", err)
	}
}

func TestChooseAndSpawnCorruptScopeJSONFailsLoud(t *testing.T) {
	m, _, tickets := newTestExchange(t, planRouter(`[]`, ""), "goal")
	ctx := context.Background()
	id, err := tickets.Create(ctx, Ticket{Title: "bad-scope", Status: StatusTodo, Scope: json.RawMessage(`{corrupt`)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, serr := m.chooseAndSpawn(ctx)
	if serr == nil || !strings.Contains(serr.Error(), "choose: scope of "+id) {
		t.Fatalf("corrupt scope: err = %v", serr)
	}
}

func TestChooseAndSpawnSkipsWhenDepsNotSatisfiedOrScopeEmpty(t *testing.T) {
	m, _, tickets := newTestExchange(t, planRouter(`[]`, "a draft"), "goal")
	ctx := context.Background()
	// dep is still todo → not Done → the dependent ticket must be skipped.
	depID, _ := tickets.Create(ctx, Ticket{Title: "dep", Status: StatusTodo}) // empty Scope → also skipped
	scope, _ := json.Marshal(Scope{Name: "w", Template: "Task: {{input}}", Input: "x", Tier: TierMid,
		Budget: Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 1000}})
	blockedID, _ := tickets.Create(ctx, Ticket{Title: "dependent", Status: StatusTodo,
		Scope: scope, DependsOn: []string{depID}})

	spawned, refused, err := m.chooseAndSpawn(ctx)
	if err != nil {
		t.Fatalf("chooseAndSpawn: %v", err)
	}
	if spawned != 0 || refused != 0 {
		t.Fatalf("spawned=%d refused=%d, want 0/0 (dep unsatisfied + empty scope)", spawned, refused)
	}
	dep, _ := tickets.Get(ctx, depID)
	dependent, _ := tickets.Get(ctx, blockedID)
	if dep.Status != StatusTodo || dependent.Status != StatusTodo {
		t.Fatalf("statuses moved: dep=%s dependent=%s", dep.Status, dependent.Status)
	}
}

func TestChooseAndSpawnUpdateFailureBeforeSpawn(t *testing.T) {
	m, _, tickets := newTestExchange(t, planRouter(`[]`, "a draft"), "goal")
	ctx := context.Background()
	scope, _ := json.Marshal(Scope{Name: "w", Template: "Task: {{input}}", Input: "x", Tier: TierMid,
		Budget: Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 1000}})
	if _, err := tickets.Create(ctx, Ticket{Title: "w", Status: StatusTodo, Scope: scope}); err != nil {
		t.Fatalf("create: %v", err)
	}
	m.Tickets = &flakyTickets{TicketStore: tickets, updateErr: errBoom}
	if _, _, err := m.chooseAndSpawn(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("chooseAndSpawn update failure: err = %v, want errBoom", err)
	}
}

// --- depsSatisfied table ---

func TestDepsSatisfied(t *testing.T) {
	status := map[string]TicketStatus{
		"done1": StatusDone, "done2": StatusDone,
		"todo1": StatusTodo, "prog1": StatusInProgress,
	}
	cases := []struct {
		name string
		deps []string
		want bool
	}{
		{"no deps", nil, true},
		{"all done", []string{"done1", "done2"}, true},
		{"one not done", []string{"done1", "todo1"}, false},
		{"in progress", []string{"prog1"}, false},
		{"unknown dep id", []string{"ghost"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := depsSatisfied(Ticket{DependsOn: tc.deps}, status); got != tc.want {
				t.Fatalf("depsSatisfied(%v) = %v, want %v", tc.deps, got, tc.want)
			}
		})
	}
}

// --- recordRun nil-telemetry branch ---

func TestRecordRunNilTelemetryIsANoop(t *testing.T) {
	m := &ManagerExchange{} // Telemetry nil
	if err := m.recordRun(context.Background(), Run{Scope: "x"}); err != nil {
		t.Fatalf("recordRun with nil telemetry: %v", err)
	}
}

// TestChooseAndSpawnSpawnErrorTelemetryFailure covers the "manager-spawn-error"
// telemetry write itself failing: the tick must surface the telemetry error.
func TestChooseAndSpawnSpawnErrorTelemetryFailure(t *testing.T) {
	m, _, tickets := newTestExchange(t, planRouter(`[]`, "a draft"), "goal")
	ctx := context.Background()
	// A scope with an unknown fragment makes InProcRuntime.Spawn fail (compose).
	scope, _ := json.Marshal(Scope{Name: "w", Template: "{{fragment:ghost}}", Input: "x", Tier: TierMid,
		Budget: Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 1000}})
	if _, err := tickets.Create(ctx, Ticket{Title: "w", Status: StatusTodo, Scope: scope}); err != nil {
		t.Fatalf("create: %v", err)
	}
	m.Telemetry = failTelemetry{err: errBoom}
	_, _, err := m.chooseAndSpawn(ctx)
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "choose: telemetry") {
		t.Fatalf("spawn-error telemetry failure: err = %v", err)
	}
}

// planOnlyModel plans fine but errors on any other prompt (verify, worker) —
// lets a test drive Tick past planning into a failing reconcile.
type planOnlyModel struct{ planReply string }

func (m planOnlyModel) Run(_ context.Context, prompt string) (string, Usage, error) {
	if strings.Contains(prompt, "Plan this goal") {
		return m.planReply, Usage{}, nil
	}
	return "", Usage{}, errBoom
}

func TestTickSurfacesReconcileFailure(t *testing.T) {
	m, _, tickets := newTestExchange(t, ScriptedRouter{TierFull: planOnlyModel{planReply: "[]"}}, "goal")
	ctx := context.Background()
	res, _ := json.Marshal(Result{Output: "the work"})
	if _, err := tickets.Create(ctx, Ticket{Title: "w", Status: StatusInReview, Result: res, Acceptance: "good"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.Tick(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("Tick with failing verify: err = %v, want errBoom", err)
	}
}

func TestTickSurfacesChooseAndSpawnFailure(t *testing.T) {
	m, _, tickets := newTestExchange(t, planRouter(`[]`, ""), "goal")
	ctx := context.Background()
	if _, err := tickets.Create(ctx, Ticket{Title: "bad", Status: StatusTodo, Scope: json.RawMessage(`{corrupt`)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := m.Tick(ctx)
	if err == nil || !strings.Contains(err.Error(), "choose: scope of") {
		t.Fatalf("Tick with corrupt scope: err = %v", err)
	}
}

func TestPlanScopeStampUpdateFailure(t *testing.T) {
	planJSON := `[{"title":"a","objective":"o","acceptance":"c"}]`
	m, _, tickets := newTestExchange(t, planRouter(planJSON, ""), "goal")
	m.Tickets = &flakyTickets{TicketStore: tickets, updateErr: errBoom}
	if _, err := m.Tick(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("plan scope-stamp update failure: err = %v, want errBoom", err)
	}
}

func TestReconcileUnblockUpdateFailure(t *testing.T) {
	m, _, tickets := newTestExchange(t, planRouter(`[]`, ""), "goal")
	ctx := context.Background()
	depID, _ := tickets.Create(ctx, Ticket{Title: "dep", Status: StatusDone})
	if _, err := tickets.Create(ctx, Ticket{Title: "blocked", Status: StatusBlocked, DependsOn: []string{depID}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	m.Tickets = &flakyTickets{TicketStore: tickets, updateErr: errBoom}
	if _, _, _, err := m.reconcile(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("unblock update failure: err = %v, want errBoom", err)
	}
}

func TestParsePlannedTicketsRejectsNonTicketJSON(t *testing.T) {
	if got, ok := parsePlannedTickets("prose [1, 2, {]) more prose"); ok {
		t.Fatalf("bad JSON parsed: %v", got)
	}
}
