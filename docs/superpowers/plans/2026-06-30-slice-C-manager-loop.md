# Slice C — The Manager Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the **tick-based manager exchange** — the §2 coordination heart of Agent Orange v1.
On each trigger the manager re-derives all state from the board + tickets, verifies In-Review work
against acceptance with a *separate* verify-scope, chooses next work, and spawns workers
**fire-and-forget** through the `WorkerRuntime` seam under **enforced resource floors** (depth /
per-scope spawns / shared tree-tokens). Workers deliver a `Result` to a `ResultSink` that flips the
ticket to In-Review; the manager sees it on the next tick. This is the execution-coordination model
with the deferred event bus replaced by tick reconciliation (contracts §2). The end-to-end test *is*
the demo: **goal → ticket → worker draft → In-Review → verify → Done (or re-plan)**, plus the floors
refusing runaway spawns — all offline and deterministic.

**Architecture:** Extend the Slice-0 `go/orchestrator` package. This slice **consumes** the frozen
contract seams — `ModelRouter` (Slice B), `BoardStore`/`TicketStore`/`Telemetry` (Slice A) — via their
interfaces, and **produces** the `ManagerExchange`, an in-process `WorkerRuntime`, the `ResultSink`, a
verify-scope, the floor-enforcing spawn path, and the generalised `write_fragment` policy syscall.
Every dependency that A/B own is stood up here as a **deterministic test double** (`MemBoard` +
`MemTickets` + `ScriptedModel`/`ScriptedRouter`) so Slice C is fully testable **without A or B done**.

**Tech Stack:** Go 1.25, standard library only (deterministic, offline, no DB, no containers, no
network). Reuses `agentdb` plain structs + `agentdb.BoardStore`, and the Slice-0 `orchestrator`
primitives (`MemBoard`, `Compose`, `Model`, `ScriptedModel`, `Telemetry`, `ApplyFeedback`).

## Global Constraints

- Module path `github.com/binocarlos/badcode-agent-orange`; all new code under `go/orchestrator/`.
- `go build ./...` and `go vet ./...` must stay green; add tests with every change.
- **No external dependencies** — stdlib only (deterministic, offline, fast tests). No Postgres, no Docker, no Anthropic SDK in this slice.
- **In-memory / in-process implementations only** behind the frozen seams — the Postgres board (Slice A), real model (Slice B), and DinD runtime (Slice F) swap in later behind the *same* interfaces.
- **Consume contract types verbatim** (`2026-06-30-v1-contracts.md` §3/§4/§5). Never redefine or renegotiate a contract; a needed change is a stop-and-escalate event, not a local edit.
- **Coordination is tick-based** (contracts §2): the manager holds nothing between ticks; it re-derives from board + tickets each trigger. NO event bus, NO pipelines, NO memory store (contracts §1).
- **The floors are mechanism, non-editable** (contracts §7): depth / per-scope spawns / shared tree-tokens are checked by the spawn path; exceed → refuse. Refusal fails loud (ticket → Needs-Human), never silent.
- **Nothing publishes in Slice C** — there is no `Connector` yet (Slice D). A worker produces a *draft* onto a ticket; the publish-approval floor lands with the connector. Do not add a network-to-the-world path here.
- **The in-proc `WorkerRuntime` runs synchronously** for determinism, but the *fire-and-forget boundary is preserved at the exchange*: the manager never reads a worker's result within the tick that spawned it — it re-derives on the next tick. Deterministic tests, honest semantics.
- Revision ids (`r1`, `r2`, …), ticket ids (`t1`, `t2`, …), session ids (`s1`, `s2`, …), and run ids (`run1`, …) are **deterministic counters** — no uuid/time/random — so the narrative is reproducible.
- TDD: failing test first, minimal impl, frequent commits.

---

## File Structure

New files (all in `go/orchestrator/`):

| File | Produces |
| --- | --- |
| `types.go` | Frozen contract types: `TicketStatus`/`ModelTier`/`FragmentKind`/`ResultStatus` enums, `Budget`, `Result`, `Ticket`, `HumanFeedback`, `Post`. |
| `memtickets.go` | `MemTickets` — in-memory `TicketStore` double (Slice A stand-in). |
| `router.go` | `ModelRouter` interface + `ScriptedRouter` double (Slice B stand-in). |
| `floors.go` | `SpawnLedger` — depth / per-scope spawn / shared tree-token enforcement (contracts §7). |
| `worker.go` | `WorkerRuntime` interface, `InProcRuntime` impl, `ResultSink` interface, `TicketResultSink` impl. |
| `verify.go` | `Verdict` + `Verify(...)` — the separate verify-scope (ARCHITECTURE §11). |
| `fragment.go` | `WriteFragment(...)` — the generalised `write_fragment` policy syscall; `ApplyHumanFeedback(...)`. |
| `manager.go` | `ManagerExchange` + `Tick(...)` + `TickReport` — the §2 tick reconciliation. |

Edited files:

| File | Change |
| --- | --- |
| `runner.go` | **Evolve** the Slice-0 `Scope` struct: add `Tier ModelTier`, `Tools []string`, `Budget Budget`, `Parent string`, `TicketID string` (contracts §4, "evolves Slice 0"). Existing `RunScope` keeps using `Name`/`Template`/`Input`. |
| `feedback.go` | Refactor `ApplyFeedback` to perform its board append via `WriteFragment` (DRY the policy syscall). Behaviour unchanged. |

New test files mirror each source file (`*_test.go`), plus `manager_narrative_test.go` for the end-to-end story.

---

### Task 1: Frozen contract types (enums + data types)

**Files:**
- Create: `go/orchestrator/types.go`
- Create: `go/orchestrator/types_test.go`
- Edit: `go/orchestrator/runner.go` (evolve `Scope`)

**Interfaces:**
- Consumes: `encoding/json`, `agentdb` (none of its types redefined).
- Produces (verbatim from contracts §3/§4): `TicketStatus`+lane consts, `ModelTier`+tier consts, `FragmentKind`+kind consts, `ResultStatus`+status consts, `Budget{MaxDepth int; MaxSpawns int; TreeTokens int64}`, `Result{SessionID,TicketID,Output string; Status ResultStatus; TokensUsed int64}`, `Ticket{...}` (all §4 fields), `HumanFeedback{TargetRef,Note string}`, `Post{Channel,Text string; Media []string}`. Evolves `Scope` (adds `Tier`,`Tools`,`Budget`,`Parent`,`TicketID`).

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"encoding/json"
	"testing"
)

func TestContractStringsAreFrozen(t *testing.T) {
	cases := map[string]string{
		string(StatusBacklog):    "backlog",
		string(StatusTodo):       "todo",
		string(StatusInProgress): "in_progress",
		string(StatusInReview):   "in_review",
		string(StatusDone):       "done",
		string(StatusBlocked):    "blocked",
		string(StatusNeedsHuman): "needs_human",
		string(TierFull):         "full",
		string(TierMid):          "mid",
		string(TierCheap):        "cheap",
		string(FragmentRole):     "role",
		string(FragmentRouting):  "routing",
		string(FragmentProcedure): "procedure",
		string(ResultDone):       "done",
		string(ResultEscalated):  "escalated",
		string(ResultFailed):     "failed",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("frozen string drift: got %q want %q", got, want)
		}
	}
}

func TestScopeRoundTripsThroughTicket(t *testing.T) {
	s := Scope{
		Name: "post-writer", Template: "{{fragment:role}}\n{{input}}", Input: "draft it",
		Tier: TierMid, Tools: []string{"read"},
		Budget: Budget{MaxDepth: 3, MaxSpawns: 2, TreeTokens: 1000},
		Parent: "mgr", TicketID: "t1",
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal scope: %v", err)
	}
	tk := Ticket{ID: "t1", Status: StatusTodo, Objective: "draft it", Acceptance: "must be witty", Scope: raw}
	var back Scope
	if err := json.Unmarshal(tk.Scope, &back); err != nil {
		t.Fatalf("unmarshal scope from ticket: %v", err)
	}
	if back.Tier != TierMid || back.Budget.MaxDepth != 3 || back.Parent != "mgr" || back.TicketID != "t1" {
		t.Fatalf("scope did not round-trip: %+v", back)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run 'TestContractStrings|TestScopeRoundTrips' -v`
Expected: FAIL — `undefined: StatusBacklog` (and `Scope` has no `Tier`).

- [ ] **Step 3: Write minimal implementation**

Create `go/orchestrator/types.go`:

```go
package orchestrator

import "encoding/json"

// TicketStatus — the board lanes (contracts §3, ARCHITECTURE §5).
type TicketStatus string

const (
	StatusBacklog    TicketStatus = "backlog"
	StatusTodo       TicketStatus = "todo"
	StatusInProgress TicketStatus = "in_progress"
	StatusInReview   TicketStatus = "in_review"
	StatusDone       TicketStatus = "done"
	StatusBlocked    TicketStatus = "blocked"
	StatusNeedsHuman TicketStatus = "needs_human"
)

// ModelTier — per-scope cost/capability tier (contracts §3).
type ModelTier string

const (
	TierFull  ModelTier = "full"
	TierMid   ModelTier = "mid"
	TierCheap ModelTier = "cheap"
)

// FragmentKind — a compose-only guidance fragment (contracts §3).
type FragmentKind string

const (
	FragmentRole      FragmentKind = "role"
	FragmentRouting   FragmentKind = "routing"
	FragmentProcedure FragmentKind = "procedure"
)

// ResultStatus — how a worker session ended (contracts §3).
type ResultStatus string

const (
	ResultDone      ResultStatus = "done"
	ResultEscalated ResultStatus = "escalated"
	ResultFailed    ResultStatus = "failed"
)

// Budget — the enforced resource floor (contracts §4; execution model §7).
type Budget struct {
	MaxDepth   int   // hard cap on the parent chain (runaway recursion)
	MaxSpawns  int   // fan-out cap for THIS scope
	TreeTokens int64 // shared token budget, decremented down the whole goal-tree
}

// Result — what a worker session returns (contracts §4).
type Result struct {
	SessionID  string
	TicketID   string
	Output     string
	Status     ResultStatus
	TokensUsed int64
}

// Ticket — a board work item (contracts §4). Ungated; not in the versioned log.
type Ticket struct {
	ID          string
	ProjectID   string
	Title       string
	Objective   string
	Acceptance  string
	Status      TicketStatus
	Scope       json.RawMessage
	Result      json.RawMessage
	PendingPost json.RawMessage
	DependsOn   []string
	Parent      string
	Attempts    int
	BoardRev    string
	CreatedAt   int64
	UpdatedAt   int64
}

// HumanFeedback — a targeted note that drives the learning loop (contracts §4).
type HumanFeedback struct {
	TargetRef string // "ticket:<id>" | "run:<id>" | "fragment:<id>"
	Note      string
}

// Post — a unit of content a Connector publishes (contracts §4). Unused until Slice D.
type Post struct {
	Channel string
	Text    string
	Media   []string
}
```

Evolve `Scope` in `runner.go` (replace the struct; `RunScope` is unchanged):

```go
// Scope — one worker/manager invocation (contracts §4; evolves Slice 0: adds
// Tier/Tools/Budget/Parent/TicketID). The Slice-0 compose fields stay.
type Scope struct {
	Name     string    // scope/role name (e.g. "manager", "post-writer")
	Template string    // prompt template with {{fragment:ID}} and {{input}}
	Input    string    // text templated into the prompt (goal / prior output / objective)
	Tier     ModelTier // which model tier to run on
	Tools    []string  // enforced tool allowlist (empty = no tools)
	Budget   Budget    // depth/spawn/token caps (the resource floor)
	Parent   string    // parent session id ("" = root); depth = parent.depth + 1
	TicketID string    // the ticket this scope serves ("" for the manager exchange itself)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TestContractStrings|TestScopeRoundTrips' -v && go build ./...`
Expected: PASS; build green (existing Slice-0 tests still compile against the widened `Scope`).

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/types.go go/orchestrator/types_test.go go/orchestrator/runner.go
git commit -m "feat(orchestrator): frozen contract types + evolve Scope (tier/tools/budget/parent/ticket)"
```

---

### Task 2: In-memory TicketStore (`MemTickets`, the Slice-A double)

**Files:**
- Create: `go/orchestrator/memtickets.go`
- Create: `go/orchestrator/memtickets_test.go`

**Interfaces:**
- Consumes: `context`, `Ticket`, `TicketStatus`.
- Produces: `type TicketStore interface { Create(ctx, Ticket) (id string, err error); Update(ctx, Ticket) error; Get(ctx, id string) (Ticket, error); List(ctx, status TicketStatus) ([]Ticket, error) }` (contracts §5, verbatim). `func NewMemTickets() *MemTickets` implementing it. Ids are `"t1"`,`"t2"`,…; `Create` ignores any inbound `ID` and assigns the counter; `List("")` returns all in creation order; `List(status)` filters; `Get`/`Update` error on unknown id.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"
)

func TestMemTicketsCRUDAndList(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()

	id1, err := ts.Create(ctx, Ticket{Title: "draft post", Objective: "write it", Status: StatusTodo})
	if err != nil || id1 != "t1" {
		t.Fatalf("create t1: id=%q err=%v", id1, err)
	}
	id2, _ := ts.Create(ctx, Ticket{Title: "review post", Status: StatusBacklog})
	if id2 != "t2" {
		t.Fatalf("create t2: id=%q", id2)
	}

	got, err := ts.Get(ctx, "t1")
	if err != nil || got.Title != "draft post" {
		t.Fatalf("get t1: %+v err=%v", got, err)
	}

	got.Status = StatusInReview
	if err := ts.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	if again, _ := ts.Get(ctx, "t1"); again.Status != StatusInReview {
		t.Fatalf("update not persisted: %s", again.Status)
	}

	all, _ := ts.List(ctx, "")
	if len(all) != 2 || all[0].ID != "t1" || all[1].ID != "t2" {
		t.Fatalf("list all wrong: %+v", all)
	}
	todo, _ := ts.List(ctx, StatusBacklog)
	if len(todo) != 1 || todo[0].ID != "t2" {
		t.Fatalf("list backlog wrong: %+v", todo)
	}

	if _, err := ts.Get(ctx, "nope"); err == nil {
		t.Fatalf("expected error on unknown id")
	}
	if err := ts.Update(ctx, Ticket{ID: "nope"}); err == nil {
		t.Fatalf("expected error updating unknown id")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestMemTicketsCRUDAndList -v`
Expected: FAIL — `undefined: NewMemTickets`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// TicketStore — work-state CRUD (contracts §5). Ungated; not versioned. The
// Postgres impl is Slice A; MemTickets is the deterministic dev/test double.
type TicketStore interface {
	Create(ctx context.Context, t Ticket) (id string, err error)
	Update(ctx context.Context, t Ticket) error
	Get(ctx context.Context, id string) (Ticket, error)
	List(ctx context.Context, status TicketStatus) ([]Ticket, error) // status "" = all
}

// MemTickets is an in-memory TicketStore with deterministic ids (t1, t2, …).
type MemTickets struct {
	mu    sync.Mutex
	seq   int
	byID  map[string]Ticket
	order []string
}

// NewMemTickets returns an empty ticket store.
func NewMemTickets() *MemTickets {
	return &MemTickets{byID: map[string]Ticket{}}
}

func (m *MemTickets) Create(_ context.Context, t Ticket) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	t.ID = fmt.Sprintf("t%d", m.seq)
	m.byID[t.ID] = t
	m.order = append(m.order, t.ID)
	return t.ID, nil
}

func (m *MemTickets) Update(_ context.Context, t Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[t.ID]; !ok {
		return fmt.Errorf("tickets: update unknown id %q", t.ID)
	}
	m.byID[t.ID] = t
	return nil
}

func (m *MemTickets) Get(_ context.Context, id string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return Ticket{}, fmt.Errorf("tickets: unknown id %q", id)
	}
	return t, nil
}

func (m *MemTickets) List(_ context.Context, status TicketStatus) ([]Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := append([]string(nil), m.order...)
	sort.Strings(ids) // t1..t9 lexicographic == creation order for our counter range
	var out []Ticket
	for _, id := range m.order {
		t := m.byID[id]
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	return out, nil
}

var _ TicketStore = (*MemTickets)(nil)
```

Note: `List` iterates `m.order` (creation order) directly; the `sort` line is unnecessary — drop it and the `ids` var. Keep the impl minimal and creation-ordered.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestMemTicketsCRUDAndList -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/memtickets.go go/orchestrator/memtickets_test.go
git commit -m "feat(orchestrator): TicketStore seam + in-memory MemTickets double"
```

---

### Task 3: ModelRouter seam + `ScriptedRouter` (the Slice-B double)

**Files:**
- Create: `go/orchestrator/router.go`
- Create: `go/orchestrator/router_test.go`

**Interfaces:**
- Consumes: `Model` (Slice 0), `ModelTier`.
- Produces: `type ModelRouter interface { For(tier ModelTier) Model }` (contracts §5, verbatim). `type ScriptedRouter map[ModelTier]Model` implementing it; `For(tier)` returns the mapped model or a non-nil fallback (`Default` model) so a missing tier never panics.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"
)

func TestScriptedRouterResolvesTiers(t *testing.T) {
	full := &ScriptedModel{Default: "opus"}
	mid := &ScriptedModel{Default: "sonnet"}
	r := ScriptedRouter{TierFull: full, TierMid: mid}

	if got, _ := r.For(TierFull).Run(context.Background(), "x"); got != "opus" {
		t.Fatalf("full tier = %q", got)
	}
	if got, _ := r.For(TierMid).Run(context.Background(), "x"); got != "sonnet" {
		t.Fatalf("mid tier = %q", got)
	}
	// An unmapped tier must not panic — it returns a usable Model.
	if m := r.For(TierCheap); m == nil {
		t.Fatalf("unmapped tier returned nil Model")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestScriptedRouterResolvesTiers -v`
Expected: FAIL — `undefined: ScriptedRouter`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

// ModelRouter resolves a tier to a Model (contracts §5). Lets a Scope pick a
// cost/capability tier. Real impl (Slice B) wraps the Anthropic API per tier.
type ModelRouter interface {
	For(tier ModelTier) Model
}

// ScriptedRouter is a deterministic offline ModelRouter for tests: a tier→Model
// map. An unmapped tier falls back to a shared empty ScriptedModel (never nil).
type ScriptedRouter map[ModelTier]Model

func (r ScriptedRouter) For(tier ModelTier) Model {
	if m, ok := r[tier]; ok && m != nil {
		return m
	}
	return &ScriptedModel{} // usable, deterministic ("" output) — never nil
}

var _ ModelRouter = ScriptedRouter(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestScriptedRouterResolvesTiers -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/router.go go/orchestrator/router_test.go
git commit -m "feat(orchestrator): ModelRouter seam + ScriptedRouter double"
```

---

### Task 4: The floors — `SpawnLedger` (depth / per-scope spawns / shared tree-tokens)

**Files:**
- Create: `go/orchestrator/floors.go`
- Create: `go/orchestrator/floors_test.go`

**Interfaces:**
- Consumes: `Scope`, `Budget`.
- Produces (mechanism, non-editable — contracts §7):
  - `type SpawnLedger struct{...}`; `func NewSpawnLedger() *SpawnLedger`.
  - `func (l *SpawnLedger) RegisterRoot(sessionID string, b Budget)` — idempotent; records a depth-0 tree root with `b.TreeTokens` shared budget and `b.MaxSpawns` fan-out.
  - `func (l *SpawnLedger) Admit(s Scope) (sessionID string, err error)` — the spawn path floor check. Resolves `depth = ledger.depth[s.Parent] + 1`; refuses if `depth > s.Budget.MaxDepth`, if the parent has hit its `MaxSpawns`, or if the shared tree-token budget is exhausted (`≤ 0`). On admit, allocates a deterministic `sessionID` (`s1`,`s2`,…), records the child's own depth/maxSpawns/tree-root, and increments the parent's spawn count.
  - `func (l *SpawnLedger) Charge(sessionID string, tokens int64) error` — decrements the child's tree-root shared counter by `tokens` (clamped at 0). This is how `TreeTokens` is "decremented down the whole goal-tree."
  - Sentinel errors: `ErrMaxDepth`, `ErrMaxSpawns`, `ErrTreeExhausted`, `ErrUnknownParent`.

> **Contract gap addressed here — see "Contract gaps found" §1 and §2.** `Scope` carries no `Depth`
> field and `Budget.TreeTokens` is a *value*, so neither runaway-depth nor a *shared* tree counter can
> live on the scope alone. The `SpawnLedger` is work-state (not the versioned board) that holds depth
> per session and one shared token counter per tree root — the minimal structure the floors need.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestSpawnLedgerEnforcesFloors -v`
Expected: FAIL — `undefined: NewSpawnLedger`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"errors"
	"fmt"
	"sync"
)

// The floors (contracts §7) — refused as sentinels so callers can branch fail-loud.
var (
	ErrMaxDepth      = errors.New("floor: max depth exceeded")
	ErrMaxSpawns     = errors.New("floor: max spawns exceeded")
	ErrTreeExhausted = errors.New("floor: tree token budget exhausted")
	ErrUnknownParent = errors.New("floor: unknown parent session")
)

// SpawnLedger is the work-state that enforces the three independent recursion
// controls (execution model §7): tree height (depth), branching factor (per-scope
// spawns), and total cost (shared tree tokens). It is NOT versioned policy — it is
// ephemeral per-goal-tree work state, the natural home for depth and the shared
// token counter that the value-typed Budget cannot carry.
type SpawnLedger struct {
	mu        sync.Mutex
	seq       int
	depth     map[string]int    // sessionID -> depth
	maxSpawns map[string]int    // sessionID -> its own fan-out cap
	spawns    map[string]int    // sessionID -> children spawned so far
	root      map[string]string // sessionID -> tree-root sessionID
	tree      map[string]int64  // tree-root sessionID -> shared tokens remaining
}

// NewSpawnLedger returns an empty ledger.
func NewSpawnLedger() *SpawnLedger {
	return &SpawnLedger{
		depth: map[string]int{}, maxSpawns: map[string]int{},
		spawns: map[string]int{}, root: map[string]string{}, tree: map[string]int64{},
	}
}

// RegisterRoot records a depth-0 tree root (the manager exchange). Idempotent.
func (l *SpawnLedger) RegisterRoot(sessionID string, b Budget) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.depth[sessionID]; ok {
		return
	}
	l.depth[sessionID] = 0
	l.maxSpawns[sessionID] = b.MaxSpawns
	l.root[sessionID] = sessionID
	l.tree[sessionID] = b.TreeTokens
}

// Admit is the spawn path floor check. It returns a fresh sessionID or refuses.
func (l *SpawnLedger) Admit(s Scope) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pd, ok := l.depth[s.Parent]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownParent, s.Parent)
	}
	depth := pd + 1
	if depth > s.Budget.MaxDepth {
		return "", fmt.Errorf("%w: depth %d > %d", ErrMaxDepth, depth, s.Budget.MaxDepth)
	}
	if l.spawns[s.Parent] >= l.maxSpawns[s.Parent] {
		return "", fmt.Errorf("%w: parent %q at %d", ErrMaxSpawns, s.Parent, l.maxSpawns[s.Parent])
	}
	troot := l.root[s.Parent]
	if l.tree[troot] <= 0 {
		return "", fmt.Errorf("%w: tree %q", ErrTreeExhausted, troot)
	}
	l.seq++
	sid := fmt.Sprintf("s%d", l.seq)
	l.depth[sid] = depth
	l.maxSpawns[sid] = s.Budget.MaxSpawns
	l.root[sid] = troot
	l.spawns[s.Parent]++
	return sid, nil
}

// Charge decrements the session's tree-root shared budget (clamped at 0).
func (l *SpawnLedger) Charge(sessionID string, tokens int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	troot, ok := l.root[sessionID]
	if !ok {
		return fmt.Errorf("floor: charge unknown session %q", sessionID)
	}
	l.tree[troot] -= tokens
	if l.tree[troot] < 0 {
		l.tree[troot] = 0
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestSpawnLedgerEnforcesFloors -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/floors.go go/orchestrator/floors_test.go
git commit -m "feat(orchestrator): SpawnLedger — depth/spawn/tree-token floors (contracts §7)"
```

---

### Task 5: In-process `WorkerRuntime` + `ResultSink`

**Files:**
- Create: `go/orchestrator/worker.go`
- Create: `go/orchestrator/worker_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardStore`, `ModelRouter`, `*SpawnLedger`, `*Telemetry`, `Compose`, `TicketStore`.
- Produces:
  - `type WorkerRuntime interface { Spawn(ctx, s Scope) (sessionID string, err error) }` (contracts §5, verbatim).
  - `type ResultSink interface { Deliver(ctx, r Result) error }` (contracts §5, verbatim).
  - `type InProcRuntime struct { Board agentdb.BoardStore; Router ModelRouter; Sink ResultSink; Ledger *SpawnLedger; Telemetry *Telemetry }` implementing `WorkerRuntime`. `Spawn`: floor-check via `Ledger.Admit` (refusal → error); fold `Board.Current`; `Compose(board, s.Template, s.Input)`; run `Router.For(s.Tier)`; map output → `Result` (thread-syscall convention: an output prefixed `ESCALATE:` → `ResultEscalated` with the trimmed question, else `ResultDone`); estimate `TokensUsed` deterministically; `Ledger.Charge`; record a pinned `Run`; `Sink.Deliver`; return the `sessionID`. **Synchronous** (deterministic) — the manager still only reads results next tick.
  - `type TicketResultSink struct { Tickets TicketStore }` implementing `ResultSink`. `Deliver`: `Get` the ticket, store the marshalled `Result`, set `Status = StatusInReview` (or `StatusNeedsHuman` when `ResultEscalated`/`ResultFailed`), bump `UpdatedAt`, `Update`.

> **Contract gap addressed here — see "Contract gaps found" §3.** The §6 thread syscalls
> (`job_finished(Result)`, `escalate_to_human(text)`) are a *scope tool surface*; the in-proc runtime
> has no tool-call channel, so it maps the model's returned **text** to a `Result` by the `ESCALATE:`
> convention. The real DinD harness (Slice F) will surface the actual tool calls behind the same seam.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestInProcRuntimeDraftsAndSinksToInReview(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("role-writer", "You are a witty writer."))

	tickets := NewMemTickets()
	id, _ := tickets.Create(ctx, Ticket{Title: "draft", Objective: "write it", Status: StatusInProgress})

	ledger := NewSpawnLedger()
	ledger.RegisterRoot("mgr", Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 10000})

	rt := &InProcRuntime{
		Board:     board,
		Router:    ScriptedRouter{TierMid: &ScriptedModel{Default: "a witty draft post", Rules: nil}},
		Sink:      &TicketResultSink{Tickets: tickets},
		Ledger:    ledger,
		Telemetry: NewTelemetry(),
	}
	scope := Scope{
		Name: "post-writer", Template: "{{fragment:role-writer}}\nTask: {{input}}", Input: "launch post",
		Tier: TierMid, Budget: Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 10000},
		Parent: "mgr", TicketID: id,
	}

	sid, err := rt.Spawn(ctx, scope)
	if err != nil || sid != "s1" {
		t.Fatalf("spawn: id=%q err=%v", sid, err)
	}
	// The result landed on the ticket as In-Review (fire-and-forget → sink).
	got, _ := tickets.Get(ctx, id)
	if got.Status != StatusInReview {
		t.Fatalf("ticket status = %q, want in_review", got.Status)
	}
	var r Result
	if err := json.Unmarshal(got.Result, &r); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if r.Output != "a witty draft post" || r.Status != ResultDone || r.TicketID != id {
		t.Fatalf("result wrong: %+v", r)
	}
	if len(rt.Telemetry.Runs()) != 1 {
		t.Fatalf("expected 1 recorded run")
	}
}

func TestInProcRuntimeEscalationSinksToNeedsHuman(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("role-writer", "writer"))
	tickets := NewMemTickets()
	id, _ := tickets.Create(ctx, Ticket{Status: StatusInProgress})
	ledger := NewSpawnLedger()
	ledger.RegisterRoot("mgr", Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 10000})

	rt := &InProcRuntime{
		Board:     board,
		Router:    ScriptedRouter{TierMid: &ScriptedModel{Default: "ESCALATE: what tone should I use?"}},
		Sink:      &TicketResultSink{Tickets: tickets},
		Ledger:    ledger,
		Telemetry: NewTelemetry(),
	}
	if _, err := rt.Spawn(ctx, Scope{Template: "{{fragment:role-writer}}", Input: "x",
		Tier: TierMid, Budget: Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 10000}, Parent: "mgr", TicketID: id}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	got, _ := tickets.Get(ctx, id)
	if got.Status != StatusNeedsHuman {
		t.Fatalf("escalation status = %q, want needs_human", got.Status)
	}
	var r Result
	_ = json.Unmarshal(got.Result, &r)
	if r.Status != ResultEscalated || r.Output != "what tone should I use?" {
		t.Fatalf("escalation result wrong: %+v", r)
	}
}

func TestInProcRuntimeRefusesOnFloor(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("role-writer", "writer"))
	tickets := NewMemTickets()
	id, _ := tickets.Create(ctx, Ticket{Status: StatusInProgress})
	ledger := NewSpawnLedger()
	ledger.RegisterRoot("mgr", Budget{MaxDepth: 0, MaxSpawns: 5, TreeTokens: 10000}) // MaxDepth 0 → any worker refused

	rt := &InProcRuntime{Board: board, Router: ScriptedRouter{}, Sink: &TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: NewTelemetry()}
	if _, err := rt.Spawn(ctx, Scope{Template: "{{fragment:role-writer}}", Tier: TierMid,
		Budget: Budget{MaxDepth: 0, MaxSpawns: 5, TreeTokens: 10000}, Parent: "mgr", TicketID: id}); err == nil {
		t.Fatalf("expected floor refusal")
	}
	// Refusal must NOT have touched the ticket (the exchange decides fail-loud handling).
	if got, _ := tickets.Get(ctx, id); got.Status != StatusInProgress {
		t.Fatalf("refused spawn mutated ticket: %s", got.Status)
	}
	_ = agentdb.OpAdd // keep agentdb imported if unused elsewhere; remove if not needed
}
```

Note: drop the trailing `_ = agentdb.OpAdd` line if `agentdb` is otherwise referenced in the test file; it is only there to satisfy the import in the snippet.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestInProcRuntime -v`
Expected: FAIL — `undefined: InProcRuntime`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// WorkerRuntime provisions and runs one scope, fire-and-forget (contracts §5/§7).
// Dev impl = InProcRuntime (this slice); prod impl = agentkit DinD on GKE (Slice F).
type WorkerRuntime interface {
	Spawn(ctx context.Context, s Scope) (sessionID string, err error)
}

// ResultSink is where a finished worker's Result is delivered (contracts §5). The
// runtime holds one; the orchestrator implements it. Slice C: updates the ticket.
type ResultSink interface {
	Deliver(ctx context.Context, r Result) error
}

// escalatePrefix is the thread-syscall convention for the in-proc runtime: a model
// output beginning with it maps to escalate_to_human(text); otherwise job_finished.
const escalatePrefix = "ESCALATE:"

// InProcRuntime runs a scope in-process via the ModelRouter and delivers a Result
// to its ResultSink. It is synchronous for determinism; the fire-and-forget
// boundary is preserved at the manager exchange (results are read next tick).
type InProcRuntime struct {
	Board     agentdb.BoardStore
	Router    ModelRouter
	Sink      ResultSink
	Ledger    *SpawnLedger
	Telemetry *Telemetry
}

func (rt *InProcRuntime) Spawn(ctx context.Context, s Scope) (string, error) {
	sid, err := rt.Ledger.Admit(s) // FLOOR CHECK — refuse before doing any work
	if err != nil {
		return "", err
	}
	board, err := rt.Board.Current(ctx)
	if err != nil {
		return "", fmt.Errorf("worker %s: current board: %w", s.Name, err)
	}
	prompt, err := Compose(board, s.Template, s.Input)
	if err != nil {
		return "", fmt.Errorf("worker %s: %w", s.Name, err)
	}
	out, err := rt.Router.For(s.Tier).Run(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("worker %s: model: %w", s.Name, err)
	}

	r := Result{SessionID: sid, TicketID: s.TicketID, Output: out, Status: ResultDone}
	if rest, ok := strings.CutPrefix(out, escalatePrefix); ok {
		r.Status = ResultEscalated
		r.Output = strings.TrimSpace(rest)
	}
	r.TokensUsed = int64(len(prompt) + len(out)) // deterministic estimate (no real tokenizer offline)
	_ = rt.Ledger.Charge(sid, r.TokensUsed)

	if rt.Telemetry != nil {
		rt.Telemetry.Record(Run{Scope: s.Name, BoardRevision: board.Revision, Prompt: prompt, Output: out})
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
	switch r.Status {
	case ResultEscalated, ResultFailed:
		t.Status = StatusNeedsHuman
	default:
		t.Status = StatusInReview
	}
	t.UpdatedAt = time.Now().Unix()
	return s.Tickets.Update(ctx, t)
}

var _ ResultSink = (*TicketResultSink)(nil)
```

Note: `t.UpdatedAt = time.Now().Unix()` is the only wall-clock use; it is not asserted on (ids/revs stay deterministic). If a fully clock-free build is preferred, thread an injected clock — out of scope for Slice C tests.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestInProcRuntime -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/worker.go go/orchestrator/worker_test.go
git commit -m "feat(orchestrator): in-proc WorkerRuntime + ResultSink (fire-and-forget → In-Review)"
```

---

### Task 6: The verify-scope (`Verify` vs `ticket.Acceptance`)

**Files:**
- Create: `go/orchestrator/verify.go`
- Create: `go/orchestrator/verify_test.go`

**Interfaces:**
- Consumes: `ModelRouter`, `ModelTier`, `Ticket`, `Result`.
- Produces: `type Verdict struct { Pass bool; Reason string }`; `func Verify(ctx, router ModelRouter, tier ModelTier, t Ticket, r Result) (Verdict, error)`. Composes a verify prompt from `t.Acceptance` (criteria written at plan time by a *different* scope) + `r.Output`, runs `router.For(tier)`, and parses a verdict: `Pass` iff the (upper-cased) output contains `PASS` and not `FAIL`. Returns the raw model text as `Reason`.

> This is ARCHITECTURE §11 mechanism #1: **criteria set by a different scope than executes.** The
> worker never judges its own work; the manager exchange (Task 8) runs `Verify` on the next tick.
> **Contract gap — see "Contract gaps found" §4.** No verdict type or verify-output schema exists in
> the contracts; `Verdict` + the PASS/FAIL text convention are defined here (Slice C Produces).

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"
)

func TestVerifyChecksResultAgainstAcceptance(t *testing.T) {
	ctx := context.Background()
	// The verify model rules on the ACCEPTANCE + OUTPUT text — not the worker's say-so.
	router := ScriptedRouter{TierFull: &ScriptedModel{
		Default: "FAIL: does not meet criteria",
		Rules:   []Rule{{Contains: "witty", Reply: "PASS: reads as witty"}},
	}}

	tk := Ticket{ID: "t1", Acceptance: "the post must be witty"}
	pass, err := Verify(ctx, router, TierFull, tk, Result{Output: "a witty launch post"})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !pass.Pass {
		t.Fatalf("expected PASS, got %+v", pass)
	}

	tkDry := Ticket{ID: "t2", Acceptance: "the post must be formal"}
	fail, _ := Verify(ctx, router, TierFull, tkDry, Result{Output: "a dry corporate memo"})
	if fail.Pass {
		t.Fatalf("expected FAIL, got %+v", fail)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestVerifyChecksResultAgainstAcceptance -v`
Expected: FAIL — `undefined: Verify`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"fmt"
	"strings"
)

// Verdict is the verify-scope's decision (Slice C — no contract verdict type exists).
type Verdict struct {
	Pass   bool
	Reason string
}

const verifyTemplate = `You are a verifier. Judge ONLY whether the work below meets the acceptance
criteria. Do not improve it. Reply with PASS or FAIL and one short reason.
ACCEPTANCE CRITERIA:
%s
WORK PRODUCED:
%s`

// Verify runs a SEPARATE scope (ARCHITECTURE §11) that checks a Result against the
// ticket's acceptance criteria — criteria set at plan time by a different scope than
// executed the work. It never edits the work; it only judges it.
func Verify(ctx context.Context, router ModelRouter, tier ModelTier, t Ticket, r Result) (Verdict, error) {
	prompt := fmt.Sprintf(verifyTemplate, t.Acceptance, r.Output)
	out, err := router.For(tier).Run(ctx, prompt)
	if err != nil {
		return Verdict{}, fmt.Errorf("verify %s: %w", t.ID, err)
	}
	up := strings.ToUpper(out)
	pass := strings.Contains(up, "PASS") && !strings.Contains(up, "FAIL")
	return Verdict{Pass: pass, Reason: out}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestVerifyChecksResultAgainstAcceptance -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/verify.go go/orchestrator/verify_test.go
git commit -m "feat(orchestrator): verify-scope — check Result vs ticket.Acceptance (ARCH §11)"
```

---

### Task 7: Generalised `write_fragment` (the policy syscall) + `ApplyHumanFeedback`

**Files:**
- Create: `go/orchestrator/fragment.go`
- Create: `go/orchestrator/fragment_test.go`
- Edit: `go/orchestrator/feedback.go` (route the board append through `WriteFragment`)

**Interfaces:**
- Consumes: `agentdb.BoardStore`, `agentdb.Board`, `agentdb.BoardPromptFragment`, `Model`, `HumanFeedback`, `ApplyFeedback` (Slice 0).
- Produces:
  - `func WriteFragment(ctx, board agentdb.BoardStore, id, body, author, message string) (revisionID string, err error)` — the §6 policy syscall = `BoardStore.Append` of an `OpUpdate prompt_fragment`. **Guards** (carried over from Slice 0): non-empty body, `len ≤ MaxFragmentLen`. Preserves the fragment's existing `Kind` (reads Current; defaults to `FragmentRouting` when the fragment is new).
  - `func ApplyHumanFeedback(ctx, board agentdb.BoardStore, reviser Model, fb HumanFeedback) (revisionID string, err error)` — parses `fb.TargetRef` (`"fragment:<id>"` → `ApplyFeedback` on that fragment; `"ticket:<id>"`/`"run:<id>"` → routed to the configured routing fragment, or an explicit "unsupported target" error). This is the learning-loop entry the HTTP `/api/feedback` (Slice E) will call.
  - Refactor `ApplyFeedback` (Slice 0) to build its `OpUpdate` via `WriteFragment` (behaviour unchanged; author `"human-feedback"`, message = the note).

> **Contract gap — see "Contract gaps found" §5.** `HumanFeedback.TargetRef` may be `ticket:` / `run:` /
> `fragment:`, but `write_fragment` needs a *fragment* id. The mapping from a critiqued ticket/run to
> *which* fragment to edit is undefined in the contracts. Slice C handles `fragment:` directly and
> routes `ticket:`/`run:` to a single configured routing fragment (or errors), flagging the gap.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestWriteFragmentGuardsAndAppends(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("routing-guidance", "Be basic."))

	rev, err := WriteFragment(ctx, board, "routing-guidance", "Be clever and dry.", "consultant", "tighten tone")
	if err != nil || rev != "r2" {
		t.Fatalf("write: rev=%q err=%v", rev, err)
	}
	cur, _ := board.Current(ctx)
	if cur.Fragments[0].Body != "Be clever and dry." {
		t.Fatalf("body = %q", cur.Fragments[0].Body)
	}

	// Guard: empty body refuses (never wipe a load-bearing fragment).
	if _, err := WriteFragment(ctx, board, "routing-guidance", "", "x", "y"); err == nil {
		t.Fatalf("expected empty-body refusal")
	}
	// Guard: over-length refuses.
	if _, err := WriteFragment(ctx, board, "routing-guidance", strings.Repeat("x", MaxFragmentLen+1), "x", "y"); err == nil {
		t.Fatalf("expected over-length refusal")
	}
}

func TestApplyHumanFeedbackRoutesFragmentTarget(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, SeedFragment("routing-guidance", "Be basic."))
	reviser := &ScriptedModel{Default: "Be basic.", Rules: []Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."},
	}}

	rev, err := ApplyHumanFeedback(ctx, board, reviser, HumanFeedback{
		TargetRef: "fragment:routing-guidance", Note: "make it more clever",
	})
	if err != nil || rev != "r2" {
		t.Fatalf("feedback: rev=%q err=%v", rev, err)
	}
	cur, _ := board.Current(ctx)
	if !strings.Contains(cur.Fragments[0].Body, "clever") {
		t.Fatalf("not revised: %q", cur.Fragments[0].Body)
	}

	// A ticket/run target with no configured mapping is an explicit, non-silent error.
	if _, err := ApplyHumanFeedback(ctx, board, reviser, HumanFeedback{TargetRef: "ticket:t1", Note: "x"}); err == nil {
		t.Fatalf("expected unsupported-target error for ticket ref")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run 'TestWriteFragment|TestApplyHumanFeedback' -v`
Expected: FAIL — `undefined: WriteFragment`.

- [ ] **Step 3: Write minimal implementation**

Create `go/orchestrator/fragment.go`:

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// WriteFragment is the §6 policy syscall: append an OpUpdate prompt_fragment as a
// new board revision. This is Slice 0's ApplyFeedback board-write, generalised to
// a raw (id, body) write with the coherence guards intact — the one write the
// learning loop, the Consultant, and any policy edit share.
func WriteFragment(ctx context.Context, board agentdb.BoardStore, id, body, author, message string) (string, error) {
	if body == "" {
		return "", fmt.Errorf("write_fragment: empty body (refusing to wipe %q)", id)
	}
	if len(body) > MaxFragmentLen {
		return "", fmt.Errorf("write_fragment: body %d > MaxFragmentLen %d", len(body), MaxFragmentLen)
	}
	kind := string(FragmentRouting)
	if cur, err := board.Current(ctx); err == nil {
		for _, f := range cur.Fragments {
			if f.ID == id && f.Kind != "" {
				kind = f.Kind
				break
			}
		}
	}
	raw, err := json.Marshal(agentdb.BoardPromptFragment{ID: id, Kind: kind, Body: body})
	if err != nil {
		return "", err
	}
	return board.Append(ctx, agentdb.Changeset{
		Author:  author,
		Message: message,
		Ops:     []agentdb.Op{{Op: agentdb.OpUpdate, EntityType: "prompt_fragment", EntityID: id, Body: raw}},
	})
}

// ApplyHumanFeedback routes a (target_ref, note) to a fragment edit — the learning
// loop entry point. "fragment:<id>" edits that fragment via the reviser
// (ApplyFeedback); "ticket:"/"run:" targets have no contract-defined fragment
// mapping (see plan "Contract gaps found" §5) → explicit unsupported-target error.
func ApplyHumanFeedback(ctx context.Context, board agentdb.BoardStore, reviser Model, fb HumanFeedback) (string, error) {
	kind, ref, ok := strings.Cut(fb.TargetRef, ":")
	if !ok {
		return "", fmt.Errorf("feedback: malformed target_ref %q", fb.TargetRef)
	}
	switch kind {
	case "fragment":
		return ApplyFeedback(ctx, board, reviser, ref, fb.Note)
	case "ticket", "run":
		return "", fmt.Errorf("feedback: target %q has no fragment mapping in v1 (see contract gap)", fb.TargetRef)
	default:
		return "", fmt.Errorf("feedback: unknown target kind %q", kind)
	}
}
```

Refactor the append in `feedback.go`'s `ApplyFeedback` to call `WriteFragment` (replace the inline `json.Marshal` + `board.Append`):

```go
	// (guards on `revised` unchanged above)
	return WriteFragment(ctx, board, fragmentID, revised, "human-feedback", note)
```

Remove the now-unused `encoding/json` and `agentdb` imports from `feedback.go` if they become unused after the refactor.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TestWriteFragment|TestApplyHumanFeedback|TestApplyFeedback' -v && go vet ./orchestrator/...`
Expected: PASS (including the Slice-0 `TestApplyFeedbackWritesDeltaRevision`, still green after the refactor).

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/fragment.go go/orchestrator/fragment_test.go go/orchestrator/feedback.go
git commit -m "feat(orchestrator): generalised write_fragment syscall + human-feedback routing"
```

---

### Task 8: `ManagerExchange` — plan a goal into tickets

**Files:**
- Create: `go/orchestrator/manager.go`
- Create: `go/orchestrator/manager_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardStore`, `TicketStore`, `ModelRouter`, `WorkerRuntime`, `*SpawnLedger`, `*Telemetry`, `Compose`.
- Produces:
  - `type ManagerExchange struct { Board agentdb.BoardStore; Tickets TicketStore; Router ModelRouter; Runtime WorkerRuntime; Ledger *SpawnLedger; Telemetry *Telemetry; Goal string; ProjectID string; ManagerSession string; PlanTier, WorkerTier, VerifyTier ModelTier; WorkerBudget Budget; PlanTemplate, WorkerTemplate string; MaxAttempts int }`.
  - `type TickReport struct { Planned, Verified, Done, RePlanned, Spawned, Refused int }`.
  - `type plannedTicket struct { Title, Objective, Acceptance string }` (the manager's planning-output schema — a JSON array the plan model returns; **defined here** because the contracts leave goal→ticket output undefined; see "Contract gaps found" §6).
  - `func (m *ManagerExchange) plan(ctx, board agentdb.Board) (int, error)` — when no tickets exist: compose `PlanTemplate` (board fragments + `Goal`), run `Router.For(PlanTier)`, parse the JSON `[]plannedTicket`, and `Create` a Todo ticket per entry with `Objective`, `Acceptance`, `ProjectID`, `Parent = ManagerSession`, and a marshalled worker `Scope{Name, Template: WorkerTemplate, Input: Objective, Tier: WorkerTier, Budget: WorkerBudget, Parent: ManagerSession}` (TicketID filled by a follow-up `Update` once the id is known). Records a `plan` telemetry run. Returns count planned.
  - `func NewManagerExchange(...)` optional convenience; or callers construct the struct directly and call `Ledger.RegisterRoot(ManagerSession, WorkerBudget)`.

This task builds ONLY `plan`; the full `Tick` is Task 9–10.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
)

func planRouter(planJSON, workerDraft string) ScriptedRouter {
	return ScriptedRouter{
		TierFull: &ScriptedModel{Default: planJSON},           // manager planning
		TierMid:  &ScriptedModel{Default: workerDraft},        // worker
	}
}

func newTestExchange(t *testing.T, router ScriptedRouter, goal string) (*ManagerExchange, *MemBoard, *MemTickets) {
	t.Helper()
	board := NewMemBoard()
	if _, err := board.Append(context.Background(), SeedFragment("role-writer", "You are a witty writer.")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tickets := NewMemTickets()
	ledger := NewSpawnLedger()
	budget := Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 100000}
	ledger.RegisterRoot("mgr", budget)
	m := &ManagerExchange{
		Board: board, Tickets: tickets, Router: router,
		Runtime: &InProcRuntime{Board: board, Router: router,
			Sink: &TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: NewTelemetry()},
		Ledger: ledger, Telemetry: NewTelemetry(),
		Goal: goal, ProjectID: "p1", ManagerSession: "mgr",
		PlanTier: TierFull, WorkerTier: TierMid, VerifyTier: TierFull,
		WorkerBudget:   budget,
		PlanTemplate:   "Plan this goal into tickets as JSON: {{input}}",
		WorkerTemplate: "{{fragment:role-writer}}\nTask: {{input}}",
		MaxAttempts:    2,
	}
	return m, board, tickets
}

func TestManagerPlansGoalIntoTickets(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"draft launch post","objective":"write a witty launch post","acceptance":"the post must be witty"}]`
	m, board, tickets := newTestExchange(t, planRouter(planJSON, "a witty launch post"), "grow the brand")

	cur, _ := board.Current(ctx)
	n, err := m.plan(ctx, cur)
	if err != nil || n != 1 {
		t.Fatalf("plan: n=%d err=%v", n, err)
	}
	all, _ := tickets.List(ctx, "")
	if len(all) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(all))
	}
	tk := all[0]
	if tk.Status != StatusTodo || tk.Objective != "write a witty launch post" || tk.Acceptance != "the post must be witty" {
		t.Fatalf("planned ticket wrong: %+v", tk)
	}
	var sc Scope
	if err := json.Unmarshal(tk.Scope, &sc); err != nil {
		t.Fatalf("scope unmarshal: %v", err)
	}
	if sc.Tier != TierMid || sc.Parent != "mgr" || sc.TicketID != tk.ID || sc.Input != tk.Objective {
		t.Fatalf("planned scope wrong: %+v", sc)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestManagerPlansGoalIntoTickets -v`
Expected: FAIL — `undefined: ManagerExchange`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"encoding/json"
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
	Telemetry *Telemetry

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
		m.Telemetry.Record(Run{Scope: "manager-plan", BoardRevision: board.Revision, Prompt: prompt, Output: out})
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestManagerPlansGoalIntoTickets -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/manager.go go/orchestrator/manager_test.go
git commit -m "feat(orchestrator): ManagerExchange.plan — goal → tickets (objective+acceptance+scope)"
```

---

### Task 9: `Tick` — reconcile (verify In-Review → Done | re-plan) + clear blocked deps

**Files:**
- Edit: `go/orchestrator/manager.go`
- Edit: `go/orchestrator/manager_test.go`

**Interfaces:**
- Consumes: `Verify`, `Verdict`, the Task-8 struct.
- Produces:
  - `func (m *ManagerExchange) reconcile(ctx) (verified, done, replanned int, err error)` — for each `StatusInReview` ticket: unmarshal `Result`; run `Verify(Router, VerifyTier, ticket, result)`; on `Pass` set `StatusDone`; else `Attempts++` and set `StatusTodo` (re-plan) unless `Attempts >= MaxAttempts`, then `StatusNeedsHuman` (fail-loud). Then clear blocked deps: any `StatusBlocked` ticket whose `DependsOn` are all `StatusDone` → `StatusTodo`.
  - `func (m *ManagerExchange) Tick(ctx) (TickReport, error)` — the full exchange, wired incrementally: `Ledger.RegisterRoot` (idempotent); load `Board.Current` + `Tickets.List("")`; if no tickets → `plan`; `reconcile`. (Choose + spawn added in Task 10.)

- [ ] **Step 1: Write the failing test**

```go
func TestReconcileVerifiesInReviewToDoneOrReplan(t *testing.T) {
	ctx := context.Background()
	// Verify model: PASS when the output is witty, FAIL otherwise.
	router := ScriptedRouter{TierFull: &ScriptedModel{
		Default: "FAIL: not witty enough",
		Rules:   []Rule{{Contains: "witty", Reply: "PASS: witty"}},
	}}
	m, _, tickets := newTestExchange(t, router, "grow the brand")

	// A passing In-Review ticket.
	passRes, _ := json.Marshal(Result{TicketID: "t1", Output: "a witty draft", Status: ResultDone})
	pid, _ := tickets.Create(ctx, Ticket{Objective: "x", Acceptance: "must be witty", Status: StatusInReview, Result: passRes})

	// A failing In-Review ticket (Attempts near the cap → re-plan then needs-human).
	failRes, _ := json.Marshal(Result{TicketID: "t2", Output: "a dull draft", Status: ResultDone})
	fid, _ := tickets.Create(ctx, Ticket{Objective: "y", Acceptance: "must be witty", Status: StatusInReview, Result: failRes, Attempts: 0})

	v, d, rp, err := m.reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if v != 2 || d != 1 || rp != 1 {
		t.Fatalf("counts: verified=%d done=%d replanned=%d", v, d, rp)
	}
	if got, _ := tickets.Get(ctx, pid); got.Status != StatusDone {
		t.Fatalf("pass ticket = %s, want done", got.Status)
	}
	if got, _ := tickets.Get(ctx, fid); got.Status != StatusTodo || got.Attempts != 1 {
		t.Fatalf("fail ticket = %s attempts=%d, want todo/1", got.Status, got.Attempts)
	}

	// Second failing verify pushes Attempts to MaxAttempts(2) → needs-human.
	if _, _, _, err := m.reconcileForTicket(ctx, fid); err != nil { // helper not required; re-run reconcile
	}
	// Re-set to in_review to simulate another worker attempt failing again.
	f2, _ := tickets.Get(ctx, fid)
	f2.Status = StatusInReview
	_ = tickets.Update(ctx, f2)
	if _, _, _, err := m.reconcile(ctx); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if got, _ := tickets.Get(ctx, fid); got.Status != StatusNeedsHuman {
		t.Fatalf("after 2 fails = %s, want needs_human", got.Status)
	}
}

func TestReconcileClearsBlockedDeps(t *testing.T) {
	ctx := context.Background()
	router := ScriptedRouter{TierFull: &ScriptedModel{Default: "PASS"}}
	m, _, tickets := newTestExchange(t, router, "g")
	dep, _ := tickets.Create(ctx, Ticket{Status: StatusDone})
	blocked, _ := tickets.Create(ctx, Ticket{Status: StatusBlocked, DependsOn: []string{dep}})
	if _, _, _, err := m.reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, _ := tickets.Get(ctx, blocked); got.Status != StatusTodo {
		t.Fatalf("blocked ticket = %s, want todo (deps done)", got.Status)
	}
}
```

Note: remove the placeholder `reconcileForTicket` call above (it is illustrative). The re-plan-to-needs-human path is exercised purely by flipping the ticket back to In-Review and calling `reconcile` again — implement `reconcile` only.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run 'TestReconcile' -v`
Expected: FAIL — `m.reconcile undefined`.

- [ ] **Step 3: Write minimal implementation**

Append to `manager.go`:

```go
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
				t.Status = StatusNeedsHuman
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
		allDone := true
		for _, dep := range t.DependsOn {
			if statusByID[dep] != StatusDone {
				allDone = false
				break
			}
		}
		if allDone {
			t.Status = StatusTodo
			t.UpdatedAt = time.Now().Unix()
			if uerr := m.Tickets.Update(ctx, t); uerr != nil {
				return verified, done, replanned, uerr
			}
		}
	}
	return verified, done, replanned, nil
}

// Tick runs one manager exchange (the §2 tick). Choose+spawn is added in Task 10.
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
	return rep, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TestReconcile' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/manager.go go/orchestrator/manager_test.go
git commit -m "feat(orchestrator): Tick reconcile — verify In-Review → Done|re-plan; clear blocked deps"
```

---

### Task 10: `Tick` — choose next work + spawn fire-and-forget (floors enforced, refusal fails loud)

**Files:**
- Edit: `go/orchestrator/manager.go`
- Edit: `go/orchestrator/manager_test.go`

**Interfaces:**
- Consumes: `WorkerRuntime.Spawn`, `SpawnLedger` sentinels, the Task-9 `Tick`.
- Produces:
  - `func (m *ManagerExchange) chooseAndSpawn(ctx) (spawned, refused int, err error)` — select `StatusTodo` tickets whose `DependsOn` are all `StatusDone` (or empty). For each: unmarshal the ticket's `Scope`, set `TicketID`/`Parent = ManagerSession`, pin `BoardRev`, set `StatusInProgress`, `Update`; call `Runtime.Spawn(scope)` (which enforces the floors and, on the in-proc runtime, synchronously delivers → In-Review). On a floor refusal (`errors.Is(err, ErrMaxDepth|ErrMaxSpawns|ErrTreeExhausted)`) set the ticket `StatusNeedsHuman` with the refusal reason recorded in telemetry (fail-loud), and count it `refused` — never silently drop it.
  - Extend `Tick` to call `chooseAndSpawn` after `reconcile`, filling `rep.Spawned`/`rep.Refused`.

- [ ] **Step 1: Write the failing test**

```go
func TestChooseAndSpawnDispatchesTodo(t *testing.T) {
	ctx := context.Background()
	router := planRouter(`[{"title":"draft","objective":"write a witty post","acceptance":"must be witty"}]`, "a witty post")
	m, _, tickets := newTestExchange(t, router, "grow the brand")

	// Plan first (creates the Todo ticket + scope).
	board, _ := m.Board.Current(ctx)
	if _, err := m.plan(ctx, board); err != nil {
		t.Fatalf("plan: %v", err)
	}
	spawned, refused, err := m.chooseAndSpawn(ctx)
	if err != nil || spawned != 1 || refused != 0 {
		t.Fatalf("choose: spawned=%d refused=%d err=%v", spawned, refused, err)
	}
	all, _ := tickets.List(ctx, "")
	// In-proc runtime delivered synchronously → the worker's draft flipped it to In-Review.
	if all[0].Status != StatusInReview {
		t.Fatalf("ticket after spawn = %s, want in_review", all[0].Status)
	}
}

func TestChooseAndSpawnFloorRefusalGoesNeedsHuman(t *testing.T) {
	ctx := context.Background()
	router := planRouter(`[{"title":"draft","objective":"write it","acceptance":"ok"}]`, "draft")
	m, _, tickets := newTestExchange(t, router, "g")

	// Starve the tree budget so the spawn path refuses.
	m.WorkerBudget = Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 0}
	// Rebuild ledger + runtime to use the starved budget.
	m.Ledger = NewSpawnLedger()
	m.Runtime = &InProcRuntime{Board: m.Board, Router: router,
		Sink: &TicketResultSink{Tickets: tickets}, Ledger: m.Ledger, Telemetry: m.Telemetry}
	m.Ledger.RegisterRoot("mgr", m.WorkerBudget)

	board, _ := m.Board.Current(ctx)
	if _, err := m.plan(ctx, board); err != nil {
		t.Fatalf("plan: %v", err)
	}
	spawned, refused, err := m.chooseAndSpawn(ctx)
	if err != nil {
		t.Fatalf("choose: %v", err)
	}
	if spawned != 0 || refused != 1 {
		t.Fatalf("expected 0 spawned/1 refused, got %d/%d", spawned, refused)
	}
	all, _ := tickets.List(ctx, "")
	if all[0].Status != StatusNeedsHuman {
		t.Fatalf("refused ticket = %s, want needs_human", all[0].Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run 'TestChooseAndSpawn' -v`
Expected: FAIL — `m.chooseAndSpawn undefined`.

- [ ] **Step 3: Write minimal implementation**

Append to `manager.go` (add `"errors"` to imports):

```go
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
		var scope Scope
		if len(t.Scope) == 0 {
			continue // nothing to run (should not happen post-plan)
		}
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
					m.Telemetry.Record(Run{Scope: "manager-refuse", BoardRevision: board.Revision, Output: serr.Error()})
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
```

Extend `Tick` (after the `reconcile` call, before `return`):

```go
	if rep.Spawned, rep.Refused, err = m.chooseAndSpawn(ctx); err != nil {
		return rep, err
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TestChooseAndSpawn' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/manager.go go/orchestrator/manager_test.go
git commit -m "feat(orchestrator): Tick choose+spawn fire-and-forget; floor refusal → needs-human (fail-loud)"
```

---

### Task 11: The manager-loop end-to-end narrative (the demo, as a test)

**Files:**
- Create: `go/orchestrator/manager_narrative_test.go`

**Interfaces:**
- Consumes: everything above. No new production code — this asserts the whole §2 loop across ticks:
  **goal → ticket → worker draft → In-Review → verify → Done**, and separately the **re-plan** and
  **floor-refusal** branches, all deterministic and offline.

- [ ] **Step 1: Write the failing/passing test**

```go
package orchestrator

import (
	"context"
	"testing"
)

// TestManagerLoopGoalToDone is the Slice-C demo as a test: two ticks drive a vague
// goal to a verified, Done ticket — plan, spawn fire-and-forget, then verify next tick.
func TestManagerLoopGoalToDone(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"draft launch post","objective":"write a witty launch post","acceptance":"the post must be witty"}]`
	router := ScriptedRouter{
		TierFull: &ScriptedModel{ // manager plan AND verify share the full tier, keyed by prompt text
			Default: "FAIL: not witty",
			Rules: []Rule{
				{Contains: "Plan this goal", Reply: planJSON},
				{Contains: "witty", Reply: "PASS: reads as witty"}, // verify sees acceptance+output "witty"
			},
		},
		TierMid: &ScriptedModel{Default: "a witty launch post draft"}, // worker
	}
	m, _, tickets := newTestExchange(t, router, "grow the brand")

	// TICK 1: no tickets → plan → spawn (in-proc worker drafts synchronously → In-Review).
	rep1, err := m.Tick(ctx)
	if err != nil {
		t.Fatalf("tick1: %v", err)
	}
	if rep1.Planned != 1 || rep1.Spawned != 1 {
		t.Fatalf("tick1 report: %+v", rep1)
	}
	after1, _ := tickets.List(ctx, "")
	if after1[0].Status != StatusInReview {
		t.Fatalf("after tick1 = %s, want in_review", after1[0].Status)
	}

	// TICK 2: reconcile In-Review → verify PASS → Done. No new work.
	rep2, err := m.Tick(ctx)
	if err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if rep2.Verified != 1 || rep2.Done != 1 {
		t.Fatalf("tick2 report: %+v", rep2)
	}
	final, _ := tickets.List(ctx, "")
	if final[0].Status != StatusDone {
		t.Fatalf("final = %s, want done", final[0].Status)
	}
}

// TestManagerLoopReplansOnFailedVerify: a failing draft re-plans (back to Todo),
// and on the next attempt failing again hits MaxAttempts → needs-human (fail-loud).
func TestManagerLoopReplansThenNeedsHuman(t *testing.T) {
	ctx := context.Background()
	planJSON := `[{"title":"draft","objective":"write a formal post","acceptance":"the post must be FORMAL"}]`
	router := ScriptedRouter{
		TierFull: &ScriptedModel{
			Default: "FAIL: not formal",
			Rules:   []Rule{{Contains: "Plan this goal", Reply: planJSON}},
		},
		TierMid: &ScriptedModel{Default: "a jokey casual draft"}, // never satisfies "formal"
	}
	m, _, tickets := newTestExchange(t, router, "grow the brand") // MaxAttempts=2

	_, _ = m.Tick(ctx) // plan + spawn → In-Review
	_, _ = m.Tick(ctx) // verify FAIL → Attempts=1 → Todo; then choose+spawn again → In-Review
	_, _ = m.Tick(ctx) // verify FAIL → Attempts=2 == MaxAttempts → needs-human

	final, _ := tickets.List(ctx, "")
	if final[0].Status != StatusNeedsHuman {
		t.Fatalf("final = %s (attempts=%d), want needs_human", final[0].Status, final[0].Attempts)
	}
}
```

- [ ] **Step 2: Run test**

Run: `cd go && go test ./orchestrator/ -run 'TestManagerLoop' -v`
Expected: PASS (all dependencies implemented). If it fails, fix the implementation, not the test.

- [ ] **Step 3: Full package gate + commit**

Run: `cd go && go build ./... && go vet ./orchestrator/... && go test ./orchestrator/...`
Expected: all PASS.

```bash
git add go/orchestrator/manager_narrative_test.go
git commit -m "test(orchestrator): manager-loop end-to-end — goal→draft→verify→Done + re-plan + floors"
```

---

## Self-Review notes

- **Contract coverage (contracts §9, Slice C row):** `ManagerExchange` (Tasks 8–11) ✓; in-proc `WorkerRuntime` (Task 5) ✓; `ResultSink` → In-Review (Task 5) ✓; verify-scope vs `ticket.Acceptance`, criteria by a different scope (Task 6, ARCH §11) ✓; spawn path with enforced floors — depth = parent.depth+1 ≤ MaxDepth, per-scope MaxSpawns, shared TreeTokens (Task 4 + Task 10) ✓; generalised `write_fragment` (Task 7) ✓. Consumed via interface only: `ModelRouter` (Slice B) through `ScriptedRouter`; `BoardStore`/`TicketStore`/`Telemetry` (Slice A) through `MemBoard`/`MemTickets`/`Telemetry`. **Deferred by design (contracts §1):** event bus / `emit_event` / continuations, pipelines / `run_pipeline`, memory store, the Consultant, the `Connector`/publish-approval gate (Slice D), the DinD runtime (Slice F).
- **Determinism:** ids are counters (`r*`,`t*`,`s*`,`run*`); models are `ScriptedModel`; the in-proc runtime runs synchronously and the manager reads results only on the *next* tick — no goroutines, no wall-clock in assertions (`UpdatedAt` is set but never asserted).
- **Type consistency:** contract types are declared once in `types.go` and consumed verbatim everywhere; `Scope` is *evolved* in place (additive) per contracts §4; `agentdb.BoardStore`/`BoardPromptFragment`/`Changeset`/`Op` reused unchanged.
- **Placeholder scan:** the only test-snippet placeholders (`reconcileForTicket`, the `_ = agentdb.OpAdd` import shim) are flagged inline to delete during implementation — no production placeholders.

---

## Contract gaps found

These are genuine gaps in `2026-06-30-v1-contracts.md` (and the execution model) that Slice C must fill
to be implementable. **None are contract *changes*** — each is an under-specification filled locally
with a Slice-C-owned type/convention. Flagging per the "stop, don't invent silently" rule; if any of
these should be promoted into the frozen contract, that is a stop-and-escalate decision.

1. **No `Depth` on `Scope` / no session record to read `parent.depth` from.** Contracts §7 mandate
   `depth = parent.depth + 1 ≤ MaxDepth`, but `Scope` carries only `Parent string` (a *session id*),
   not a depth, and there is no session/work-state type in the contracts to hold it. Slice C puts depth
   in the `SpawnLedger` (work state, keyed by session id). *Escalation candidate:* a `Depth`/session
   record in the contract if Slice F's runtime needs it at the seam.
2. **`Budget.TreeTokens` is a value, but must be a *shared* counter "decremented down the whole tree."**
   A value-typed `int64` copied into each child scope cannot be a shared mutable budget. Slice C models
   the shared counter as one entry per tree-root in the `SpawnLedger`; `Scope.Budget.TreeTokens` is read
   as the *root allocation*. *Escalation candidate:* the contract should say TreeTokens is an initial
   allocation resolved against a tree-scoped meter, not a per-scope value.
3. **The thread syscalls (`job_finished`/`escalate_to_human`) have no wire form for a non-tool runtime.**
   Contracts §6 list them as a worker-scope tool surface, but the in-proc runtime has no tool-call
   channel. Slice C maps model *text* → `Result` via an `ESCALATE:` prefix convention. The real DinD
   harness (Slice F) surfaces genuine tool calls behind the same `WorkerRuntime`/`ResultSink` seam, so
   the convention is contained to the dev runtime — but the *Result-construction contract* (who sets
   `Status`/`TokensUsed`) is unspecified.
4. **No verdict type / verify-output schema.** ARCH §11 requires a separate verify-scope but neither the
   contracts nor §11 define what the verify scope *returns* or how pass/fail is decided. Slice C defines
   `Verdict{Pass,Reason}` + a `PASS`/`FAIL` text convention. This is the credit-assignment oracle the
   whole self-improvement story leans on (ARCH §6A "the load-bearing caveat") — it likely deserves a
   frozen shape (and the §11 mechanism-3 human numeric score) rather than a text sniff.
5. **`HumanFeedback.TargetRef` = `ticket:`/`run:`/`fragment:` but `write_fragment` needs a *fragment*.**
   The contracts define the anchor (execution model §9.2) but not the mapping from a critiqued
   ticket/run to *which* fragment to edit. Slice C handles `fragment:` and returns an explicit
   "unsupported target" error for `ticket:`/`run:` (rather than guessing). Needs a contract answer
   before Slice E wires `/api/feedback` for non-fragment targets.
6. **No goal→ticket planning-output contract.** §2/§6 say the manager "chooses next work" and creates
   tickets with `Objective` + `Acceptance`, but nothing defines how a `Model` (text in, text out) emits
   *structured* tickets. Slice C defines a minimal `plannedTicket` JSON array the plan scope returns and
   parses it. The real model (Slice B) will need this schema (or a tool-call equivalent) pinned.
7. **`ManagerExchange` has no contract signature.** It appears only in the §9 ownership map as a thing
   Slice C "Produces," with no shape in §4/§5. Slice C defines `ManagerExchange` + `Tick(ctx)
   (TickReport, error)`. Fine as a Slice-C-owned type, but Slice E's `POST /api/trigger` (contracts §8)
   will bind to it, so its method shape becomes a de-facto contract at that boundary.
8. **Minor / unspecified, chosen locally:** re-plan attempt cap (`MaxAttempts` → Needs-Human) has no
   contract value; token accounting for a non-real model is a `len`-based estimate; `SpendMeter` (Slice
   B) is *not* wired here though §7.2 lists spend as a sibling floor — the spend halt lands with Slice B
   behind the same refusal pattern.
