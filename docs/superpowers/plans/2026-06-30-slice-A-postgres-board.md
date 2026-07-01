# Slice A — Postgres Board Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the Slice-0 board off memory and onto Postgres. Implement the three data seams the
contract assigns to Slice A — `agentdb.BoardStore`, `orchestrator.TicketStore`,
`orchestrator.Telemetry` — as Postgres-backed (gorm) impls, add the numbered SQL migrations that do
the §0 collapse (drop `board_staff`/`board_pipelines`/`board_event_types`; add `tickets` and a
telemetry `runs` table; optionally rename `board_prompt_fragments` → `fragments`), and prove
**behavioural parity** with Slice-0's `MemBoard`/`MemTelemetry` by re-running the same assertions
against the Postgres impls. `MemBoard`/`MemTelemetry` stay as the in-memory dev/test double behind the
same interfaces.

**Architecture:** The Postgres impls live in a new sub-package `go/orchestrator/pgstore` that imports
`agentdb` (for the gorm row models + `Board`/`Changeset`/`Op` types and the `BoardStore` interface)
and `orchestrator` (for `Ticket`/`TicketStatus`/`Run`/`TicketStore`/`Telemetry`). This keeps the core
`orchestrator` package stdlib-only (gorm lives only in the Postgres impl), avoids an import cycle
(`agentdb` never imports `orchestrator`), and mirrors how the board gorm models already live in
`agentdb` while the fold logic lives in `orchestrator`. Row models + numbered migrations are added to
`agentdb` (the single schema source of truth, matching `board.go` + `migrations.go`). The Postgres
`Append` assigns a monotonic `seq` and a deterministic revision id (`r1`, `r2`, …) exactly like
`MemBoard`, so the same test bodies pass against both.

**Tech Stack:** Go 1.25, gorm (`gorm.io/gorm`), Postgres in production, **sqlite (`github.com/glebarez/sqlite`)
for fast tests** — the pattern `agentdb/board_test.go` already uses (`db.AutoMigrate(...)` over the
gorm models; no Docker, no live Postgres). A live-Postgres migration test is env-gated (`t.Skip` when
unset), matching the repo's integration-test idiom.

## Global Constraints

- **Package:** all new code under `go/orchestrator/` (module `github.com/binocarlos/badcode-agent-orange`), matching Slice 0's idiom (stdlib + `agentdb`; gorm only in the Postgres impls; table-driven tests). *(v1-contracts §0)*
- **BoardStore — the versioned board (Slice 0 interface, unchanged). Postgres impl = Slice A.** *(v1-contracts §5)*
- **Board now also folds Tickets? NO — tickets are work state, queried via TicketStore, not folded.** *(v1-contracts §5)*
- **Telemetry — append-only run log (Slice 0). v1 keeps Record/Runs; the store gets a Postgres impl in Slice A (same interface).** *(v1-contracts §5)*
- Numbered idempotent SQL migrations after existing `021_*`; gorm/Postgres; BIGINT epoch ts; JSONB bodies; VARCHAR ids. Tickets/telemetry are new tables; the §0 collapse drops `board_staff`/`board_pipelines`/`board_event_types` and (optionally) renames `board_prompt_fragments` → `fragments`. *(v1-contracts §10)*
- TDD, table-driven tests, frequent commits, `go build ./...` + `go vet ./...` green. *(v1-contracts §10)*
- One impl per seam until a real need forces a second (§14.3). No external deps beyond what a seam's real impl genuinely requires. *(v1-contracts §10)*
- Reuse Slice 0 (`MemBoard` stays as the test double / dev impl behind `BoardStore`). *(v1-contracts §10)*
- **Liftability invariant.** The `go/` module must import **nothing** from any host app — keep the engine self-contained. *(CLAUDE.md rule 1)*
- Consume the contract types/interfaces **verbatim**; a contract change is a stop-and-escalate event, not a local edit. *(v1-contracts §0)*

---

## File Structure

| Path | Create/Modify | What |
| --- | --- | --- |
| `go/orchestrator/ticket.go` | Create | `TicketStatus` + constants, `Ticket` (contract §3/§4), `TicketStore` interface (contract §5). |
| `go/orchestrator/ticket_test.go` | Create | Vocabulary + `Ticket` JSON round-trip; `TicketStore` compile assertion via a nop impl. |
| `go/orchestrator/telemetry.go` | Modify | Add `Telemetry` **interface** (contract §5); rename the concrete struct `Telemetry` → `MemTelemetry`; keep `NewTelemetry()` returning `*MemTelemetry`. |
| `go/orchestrator/telemetry_test.go` | Modify | Keep existing assertions; add interface compile assertion. |
| `go/orchestrator/runner.go` | Modify | `Runner.Telemetry` field type `*Telemetry` → `Telemetry` (interface). |
| `go/orchestrator/runner_telemetry_test.go` | Create | Runner records through the `Telemetry` interface (drives the field-type change). |
| `go/agentdb/tickets.go` | Create | `Ticket` gorm **row** model + `TableName() "tickets"`. |
| `go/agentdb/telemetry.go` | Create | `TelemetryRun` gorm **row** model + `TableName() "runs"`. |
| `go/agentdb/board_v1_test.go` | Create | sqlite `AutoMigrate` + round-trip of the new `Ticket`/`TelemetryRun` models (mirrors `board_test.go`). |
| `go/agentdb/migrations.go` | Modify | Add `022_board_collapse`, `023_tickets`, `024_runs` (+ optional `025_rename_fragments`). |
| `go/orchestrator/pgstore/pgstore.go` | Create | Test/prod constructors + shared JSON helpers for the package. |
| `go/orchestrator/pgstore/board.go` | Create | `PgBoard` implementing `agentdb.BoardStore` (append/fold/pin/head/asof). |
| `go/orchestrator/pgstore/board_test.go` | Create | Parity: the `MemBoard` assertions, against `PgBoard` on sqlite + a `MemBoard`-vs-`PgBoard` double-check. |
| `go/orchestrator/pgstore/tickets.go` | Create | `PgTicketStore` implementing `orchestrator.TicketStore`. |
| `go/orchestrator/pgstore/tickets_test.go` | Create | Ticket CRUD + `List(status)` + `List("")` = all. |
| `go/orchestrator/pgstore/telemetry.go` | Create | `PgTelemetry` implementing `orchestrator.Telemetry`. |
| `go/orchestrator/pgstore/telemetry_test.go` | Create | Parity: the `MemTelemetry` assertions, against `PgTelemetry` on sqlite. |
| `go/orchestrator/pgstore/migration_pg_test.go` | Create | Env-gated (`AGENTKIT_TEST_POSTGRES_URL`) live-Postgres migration smoke test; `t.Skip` when unset. |

---

### Task 1: Ticket vocabulary + type + TicketStore interface (contract §3/§4/§5)

**Files:**
- Create: `go/orchestrator/ticket.go`
- Test: `go/orchestrator/ticket_test.go`

**Interfaces:**
- Consumes: nothing (introduces frozen contract types verbatim).
- Produces:
  - `type TicketStatus string` + `StatusBacklog/StatusTodo/StatusInProgress/StatusInReview/StatusDone/StatusBlocked/StatusNeedsHuman` (§3).
  - `type Ticket struct { ... }` verbatim from §4.
  - `type TicketStore interface { Create(ctx context.Context, t Ticket) (id string, err error); Update(ctx context.Context, t Ticket) error; Get(ctx context.Context, id string) (Ticket, error); List(ctx context.Context, status TicketStatus) ([]Ticket, error) }` (§5).

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTicketStatusVocabulary(t *testing.T) {
	cases := map[TicketStatus]string{
		StatusBacklog:    "backlog",
		StatusTodo:       "todo",
		StatusInProgress: "in_progress",
		StatusInReview:   "in_review",
		StatusDone:       "done",
		StatusBlocked:    "blocked",
		StatusNeedsHuman: "needs_human",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Fatalf("status %q != %q", got, want)
		}
	}
}

func TestTicketJSONRoundTrip(t *testing.T) {
	in := Ticket{
		ID: "t1", ProjectID: "badcode", Title: "Draft launch post",
		Objective: "write a post about X", Acceptance: "on-brand, <=280 chars",
		Status: StatusTodo, Scope: json.RawMessage(`{"name":"post-writer"}`),
		DependsOn: []string{"t0"}, Parent: "", Attempts: 1, BoardRev: "r3",
		CreatedAt: 100, UpdatedAt: 200,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Ticket
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != "t1" || out.Status != StatusTodo || len(out.DependsOn) != 1 || out.BoardRev != "r3" {
		t.Fatalf("round-trip wrong: %+v", out)
	}
}

// nopTicketStore proves TicketStore is implementable (compile-time only).
type nopTicketStore struct{}

func (nopTicketStore) Create(context.Context, Ticket) (string, error)      { return "", nil }
func (nopTicketStore) Update(context.Context, Ticket) error                { return nil }
func (nopTicketStore) Get(context.Context, string) (Ticket, error)         { return Ticket{}, nil }
func (nopTicketStore) List(context.Context, TicketStatus) ([]Ticket, error) { return nil, nil }

var _ TicketStore = nopTicketStore{}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run 'TestTicketStatusVocabulary|TestTicketJSONRoundTrip' -v`
Expected: FAIL — `undefined: TicketStatus` / `undefined: Ticket`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"encoding/json"
)

// TicketStatus — the board lanes (contract §3, ARCHITECTURE.md §5).
type TicketStatus string

const (
	StatusBacklog    TicketStatus = "backlog"
	StatusTodo       TicketStatus = "todo"
	StatusInProgress TicketStatus = "in_progress"
	StatusInReview   TicketStatus = "in_review"
	StatusDone       TicketStatus = "done"
	StatusBlocked    TicketStatus = "blocked"
	StatusNeedsHuman TicketStatus = "needs_human" // includes posts awaiting publish approval
)

// Ticket — a board work item (work state; ungated; NOT in the versioned log). Verbatim contract §4.
type Ticket struct {
	ID          string
	ProjectID   string
	Title       string
	Objective   string          // the narrowed spec slice (= Scope.Input for the worker)
	Acceptance  string          // verifiable criteria, checked by a verify scope
	Status      TicketStatus
	Scope       json.RawMessage // the Scope to invoke (opaque to the board)
	Result      json.RawMessage // the Result once In-Review
	PendingPost json.RawMessage // a Post awaiting publish approval (Needs-Human), if any
	DependsOn   []string
	Parent      string
	Attempts    int
	BoardRev    string // the board revision the ticket's work pinned to (attribution)
	CreatedAt   int64
	UpdatedAt   int64
}

// TicketStore — work-state CRUD (Slice A). Ungated; not versioned. Verbatim contract §5.
type TicketStore interface {
	Create(ctx context.Context, t Ticket) (id string, err error)
	Update(ctx context.Context, t Ticket) error
	Get(ctx context.Context, id string) (Ticket, error)
	List(ctx context.Context, status TicketStatus) ([]Ticket, error) // status "" = all
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TestTicketStatusVocabulary|TestTicketJSONRoundTrip' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/ticket.go go/orchestrator/ticket_test.go
git commit -m "feat(orchestrator): Ticket vocabulary + TicketStore interface (contract §3/§4/§5)"
```

---

### Task 2: Telemetry becomes an interface; concrete → MemTelemetry

**Files:**
- Modify: `go/orchestrator/telemetry.go`
- Modify: `go/orchestrator/telemetry_test.go`

**Interfaces:**
- Consumes: `Run` (existing Slice-0 type, unchanged).
- Produces: `type Telemetry interface { Record(r Run) Run; Runs() []Run }` (§5). The existing concrete
  struct is renamed `MemTelemetry` (the in-memory dev/test double); `NewTelemetry() *MemTelemetry`
  is kept so Slice-0 callers/tests are undisturbed.

- [ ] **Step 1: Write the failing test** (append to `telemetry_test.go`)

```go
// TestMemTelemetrySatisfiesInterface pins the Slice-0 double behind the §5 seam.
func TestMemTelemetrySatisfiesInterface(t *testing.T) {
	var tel Telemetry = NewTelemetry()
	a := tel.Record(Run{Scope: "manager", BoardRevision: "r1", Output: "dumb plan"})
	if a.ID != "run1" || a.Seq != 1 {
		t.Fatalf("record via interface wrong: %+v", a)
	}
	if len(tel.Runs()) != 1 {
		t.Fatalf("Runs() via interface = %d, want 1", len(tel.Runs()))
	}
}

var _ Telemetry = (*MemTelemetry)(nil)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestMemTelemetrySatisfiesInterface -v`
Expected: FAIL — `undefined: Telemetry` (as a type used in a `var` decl) / `undefined: MemTelemetry`.

- [ ] **Step 3: Write minimal implementation** (edit `telemetry.go`)

Add the interface and rename the concrete type. `Run` is unchanged.

```go
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

// Telemetry — append-only run log (contract §5). MemTelemetry is the in-memory
// double; pgstore.PgTelemetry is the Postgres impl (Slice A).
type Telemetry interface {
	Record(r Run) Run
	Runs() []Run
}

// MemTelemetry is an append-only in-memory run log (the CBR case base, minimally).
type MemTelemetry struct {
	mu   sync.Mutex
	runs []Run
}

// NewTelemetry returns an empty in-memory run log.
func NewTelemetry() *MemTelemetry { return &MemTelemetry{} }

// Record appends a run, assigning its 1-based Seq and id, and returns it.
func (t *MemTelemetry) Record(r Run) Run {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.Seq = len(t.runs) + 1
	r.ID = fmt.Sprintf("run%d", r.Seq)
	t.runs = append(t.runs, r)
	return r
}

// Runs returns a copy of the recorded runs in order.
func (t *MemTelemetry) Runs() []Run {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Run(nil), t.runs...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'Telemetry' -v`
Expected: PASS (existing `TestTelemetryRecordsInOrder` + the new interface test).

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/telemetry.go go/orchestrator/telemetry_test.go
git commit -m "refactor(orchestrator): Telemetry becomes an interface; concrete -> MemTelemetry (contract §5)"
```

---

### Task 3: Runner depends on the Telemetry interface

**Files:**
- Modify: `go/orchestrator/runner.go`
- Create: `go/orchestrator/runner_telemetry_test.go`

**Interfaces:**
- Consumes: `Telemetry` (interface, §5), `agentdb.BoardStore`, `Model`, `Compose`.
- Produces: `Runner.Telemetry` retyped from `*Telemetry`(concrete) to `Telemetry`(interface). No behaviour change.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// recordingTelemetry is a hand-rolled Telemetry to prove Runner depends on the
// interface, not the concrete MemTelemetry.
type recordingTelemetry struct{ got []Run }

func (r *recordingTelemetry) Record(run Run) Run {
	run.Seq = len(r.got) + 1
	run.ID = "x"
	r.got = append(r.got, run)
	return run
}
func (r *recordingTelemetry) Runs() []Run { return r.got }

func TestRunnerUsesTelemetryInterface(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	if _, err := board.Append(ctx, SeedFragment("routing-guidance", "Be clever.")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tel := &recordingTelemetry{}
	r := &Runner{
		Board:     board,
		Model:     &ScriptedModel{Default: "dumb plan", Rules: []Rule{{Contains: "clever", Reply: "clever plan"}}},
		Telemetry: tel, // <- only compiles once the field is the interface type
	}
	run, err := r.RunScope(ctx, Scope{Name: "manager", Template: "{{fragment:routing-guidance}}\nGoal: {{input}}", Input: "grow"})
	if err != nil {
		t.Fatalf("runscope: %v", err)
	}
	if run.Output != "clever plan" || len(tel.got) != 1 {
		t.Fatalf("interface telemetry not used: out=%q recorded=%d", run.Output, len(tel.got))
	}
	_ = agentdb.OpAdd // keep agentdb import if unused elsewhere
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestRunnerUsesTelemetryInterface -v`
Expected: FAIL (compile) — `cannot use tel (*recordingTelemetry) as *Telemetry value in struct literal`.

- [ ] **Step 3: Write minimal implementation** (edit `runner.go`)

Change one field:

```go
// Runner composes a scope's prompt from the current board, runs the model, and
// records the run pinned to the board revision it ran against.
type Runner struct {
	Board     agentdb.BoardStore
	Model     Model
	Telemetry Telemetry // interface (contract §5): MemTelemetry in dev, pgstore.PgTelemetry in prod
}
```

(Body of `RunScope` is unchanged — `r.Telemetry.Record(...)` already matches the interface.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -v`
Expected: PASS — the new test plus all existing Slice-0 tests (`runner_test`, `narrative_test`, …) stay green.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/runner.go go/orchestrator/runner_telemetry_test.go
git commit -m "refactor(orchestrator): Runner depends on the Telemetry interface"
```

---

### Task 4: agentdb row models — tickets + runs (gorm)

**Files:**
- Create: `go/agentdb/tickets.go`
- Create: `go/agentdb/telemetry.go`
- Create: `go/agentdb/board_v1_test.go`

**Interfaces:**
- Consumes: `agentdb.JSONArray` (existing JSONB helper).
- Produces: `agentdb.Ticket` (row) with `TableName() "tickets"`; `agentdb.TelemetryRun` (row) with
  `TableName() "runs"`. These are the persistence rows; the domain types are `orchestrator.Ticket`
  and `orchestrator.Run` (mapped in `pgstore`).

- [ ] **Step 1: Write the failing test** (`board_v1_test.go`)

```go
package agentdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newV1TestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "v1_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&BoardRevision{}, &BoardHead{}, &BoardPromptFragment{}, &Ticket{}, &TelemetryRun{}); err != nil {
		t.Fatalf("automigrate v1: %v", err)
	}
	return &Store{gdb: db}
}

func TestTicketRow_RoundTrip(t *testing.T) {
	s := newV1TestStore(t)
	ctx := context.Background()
	in := &Ticket{
		ID: "t1", ProjectID: "badcode", Title: "Draft post", Objective: "write X",
		Acceptance: "on-brand", Status: "todo", Scope: JSONArray(`{"name":"post-writer"}`),
		DependsOn: JSONArray(`["t0"]`), Attempts: 2, BoardRev: "r3", CreatedAt: 10, UpdatedAt: 20,
	}
	if err := s.gdb.WithContext(ctx).Create(in).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	var got Ticket
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "t1").Error; err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if got.Status != "todo" || got.BoardRev != "r3" || got.Attempts != 2 || string(got.DependsOn) != `["t0"]` {
		t.Fatalf("ticket round-trip wrong: %+v", got)
	}
}

func TestTelemetryRunRow_RoundTrip(t *testing.T) {
	s := newV1TestStore(t)
	ctx := context.Background()
	in := &TelemetryRun{ID: "run1", Seq: 1, Scope: "manager", BoardRevision: "r1", Prompt: "p", Output: "o"}
	if err := s.gdb.WithContext(ctx).Create(in).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	var got TelemetryRun
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "run1").Error; err != nil {
		t.Fatalf("read run: %v", err)
	}
	if got.Seq != 1 || got.Scope != "manager" || got.Output != "o" {
		t.Fatalf("run round-trip wrong: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./agentdb/ -run 'TestTicketRow_RoundTrip|TestTelemetryRunRow_RoundTrip' -v`
Expected: FAIL — `undefined: Ticket` / `undefined: TelemetryRun`.

- [ ] **Step 3: Write minimal implementation**

`go/agentdb/tickets.go`:

```go
package agentdb

// Ticket is the persistence row for a board work item (contract §4). It is the
// storage shape only; the domain type is orchestrator.Ticket (mapped in
// orchestrator/pgstore). Ungated work state — NOT part of the versioned board log.
type Ticket struct {
	ID          string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	ProjectID   string    `json:"project_id" gorm:"type:varchar(64);not null;default:'';index:idx_tickets_project"`
	Title       string    `json:"title" gorm:"type:text;not null;default:''"`
	Objective   string    `json:"objective" gorm:"type:text;not null;default:''"`
	Acceptance  string    `json:"acceptance" gorm:"type:text;not null;default:''"`
	Status      string    `json:"status" gorm:"type:varchar(20);not null;default:'backlog';index:idx_tickets_status"`
	Scope       JSONArray `json:"scope" gorm:"type:jsonb;not null;default:'{}'"`
	Result      JSONArray `json:"result" gorm:"type:jsonb;not null;default:'{}'"`
	PendingPost JSONArray `json:"pending_post" gorm:"type:jsonb;not null;default:'{}'"`
	DependsOn   JSONArray `json:"depends_on" gorm:"type:jsonb;not null;default:'[]'"`
	Parent      string    `json:"parent" gorm:"type:varchar(36);not null;default:''"`
	Attempts    int       `json:"attempts" gorm:"not null;default:0"`
	BoardRev    string    `json:"board_rev" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt   int64     `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt   int64     `json:"updated_at" gorm:"not null;default:0"`
}

func (Ticket) TableName() string { return "tickets" }
```

`go/agentdb/telemetry.go`:

```go
package agentdb

// TelemetryRun is the persistence row for one recorded scope execution (contract
// §5 Telemetry.Run). The domain type is orchestrator.Run (mapped in
// orchestrator/pgstore). Append-only "show your work" substrate.
type TelemetryRun struct {
	ID            string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Seq           int64  `json:"seq" gorm:"column:seq;uniqueIndex:idx_runs_seq"`
	Scope         string `json:"scope" gorm:"type:varchar(255);not null;default:''"`
	BoardRevision string `json:"board_revision" gorm:"type:varchar(36);not null;default:''"`
	Prompt        string `json:"prompt" gorm:"type:text;not null;default:''"`
	Output        string `json:"output" gorm:"type:text;not null;default:''"`
	CreatedAt     int64  `json:"created_at" gorm:"autoCreateTime"`
}

func (TelemetryRun) TableName() string { return "runs" }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./agentdb/ -run 'TestTicketRow_RoundTrip|TestTelemetryRunRow_RoundTrip' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/agentdb/tickets.go go/agentdb/telemetry.go go/agentdb/board_v1_test.go
git commit -m "feat(agentdb): tickets + runs gorm row models (contract §4/§5)"
```

---

### Task 5: Numbered migrations — §0 collapse + tickets + runs

**Files:**
- Modify: `go/agentdb/migrations.go`

**Interfaces:**
- Consumes: the existing `agentMigrations []migration` slice + `runMigrations` runner (idempotent,
  tracked in `agentdb_migrations`).
- Produces: three new entries appended after `021_board_current`: `022_board_collapse`,
  `023_tickets`, `024_runs`. All idempotent (`IF EXISTS`/`IF NOT EXISTS`), Postgres SQL, BIGINT epoch
  ts, JSONB bodies, VARCHAR ids — matching the §10 conventions and migration 020/021 exactly.

> **Note (schema truth):** production Postgres gets its tables from these SQL migrations; fast tests
> get theirs from `AutoMigrate` over the Task-4 gorm models (the same split `board.go` + migration
> `021` already uses). The two must describe the same columns — cross-check against Task 4.

- [ ] **Step 1: Write the failing test** (append to `board_v1_test.go`)

sqlite can't run the Postgres DDL, so assert the migration **registry** shape/idempotency markers
instead (the live-Postgres apply is exercised in Task 9's env-gated test).

```go
func TestV1MigrationsRegistered(t *testing.T) {
	want := []string{"022_board_collapse", "023_tickets", "024_runs"}
	have := map[string]string{}
	for _, m := range agentMigrations {
		have[m.Name] = m.SQL
	}
	for _, name := range want {
		sql, ok := have[name]
		if !ok {
			t.Fatalf("migration %q not registered", name)
		}
		if sql == "" {
			t.Fatalf("migration %q has empty SQL", name)
		}
	}
	// §0 collapse must drop exactly the three deferred board tables.
	collapse := have["022_board_collapse"]
	for _, tbl := range []string{"board_staff", "board_pipelines", "board_event_types"} {
		if !contains(collapse, "DROP TABLE IF EXISTS "+tbl) {
			t.Fatalf("022_board_collapse missing drop of %q: %s", tbl, collapse)
		}
	}
	// New tables must be created idempotently.
	if !contains(have["023_tickets"], "CREATE TABLE IF NOT EXISTS tickets") {
		t.Fatalf("023_tickets missing idempotent create")
	}
	if !contains(have["024_runs"], "CREATE TABLE IF NOT EXISTS runs") {
		t.Fatalf("024_runs missing idempotent create")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }
```

Add `"strings"` to the `board_v1_test.go` imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./agentdb/ -run TestV1MigrationsRegistered -v`
Expected: FAIL — `migration "022_board_collapse" not registered`.

- [ ] **Step 3: Write minimal implementation** — append to `agentMigrations` (after `021_board_current`):

```go
	{
		Name: "022_board_collapse",
		SQL: `
			DROP TABLE IF EXISTS board_staff;
			DROP TABLE IF EXISTS board_pipelines;
			DROP TABLE IF EXISTS board_event_types;
		`,
	},
	{
		Name: "023_tickets",
		SQL: `
			CREATE TABLE IF NOT EXISTS tickets (
				id            VARCHAR(36) PRIMARY KEY,
				project_id    VARCHAR(64) NOT NULL DEFAULT '',
				title         TEXT NOT NULL DEFAULT '',
				objective     TEXT NOT NULL DEFAULT '',
				acceptance    TEXT NOT NULL DEFAULT '',
				status        VARCHAR(20) NOT NULL DEFAULT 'backlog',
				scope         JSONB NOT NULL DEFAULT '{}',
				result        JSONB NOT NULL DEFAULT '{}',
				pending_post  JSONB NOT NULL DEFAULT '{}',
				depends_on    JSONB NOT NULL DEFAULT '[]',
				parent        VARCHAR(36) NOT NULL DEFAULT '',
				attempts      INT NOT NULL DEFAULT 0,
				board_rev     VARCHAR(36) NOT NULL DEFAULT '',
				created_at    BIGINT NOT NULL DEFAULT 0,
				updated_at    BIGINT NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_tickets_status ON tickets(status);
			CREATE INDEX IF NOT EXISTS idx_tickets_project ON tickets(project_id);
		`,
	},
	{
		Name: "024_runs",
		SQL: `
			CREATE TABLE IF NOT EXISTS runs (
				id             VARCHAR(36) PRIMARY KEY,
				seq            BIGINT NOT NULL,
				scope          VARCHAR(255) NOT NULL DEFAULT '',
				board_revision VARCHAR(36) NOT NULL DEFAULT '',
				prompt         TEXT NOT NULL DEFAULT '',
				output         TEXT NOT NULL DEFAULT '',
				created_at     BIGINT NOT NULL DEFAULT 0
			);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_seq ON runs(seq);
		`,
	},
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./agentdb/ -run 'TestV1MigrationsRegistered' -v && cd . && cd go && go build ./...`
Expected: PASS + build green.

- [ ] **Step 5: Commit**

```bash
git add go/agentdb/migrations.go go/agentdb/board_v1_test.go
git commit -m "feat(agentdb): migrations 022-024 — §0 collapse + tickets + runs"
```

---

### Task 6: PgBoard — Postgres BoardStore (append/fold/pin/head/asof)

**Files:**
- Create: `go/orchestrator/pgstore/pgstore.go`
- Create: `go/orchestrator/pgstore/board.go`
- Test: `go/orchestrator/pgstore/board_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardStore`, `agentdb.Board`, `agentdb.Changeset`, `agentdb.Op`,
  `agentdb.BoardPromptFragment`, `agentdb.BoardRevision`, `agentdb.BoardHead`, `agentdb.JSONArray`,
  `agentdb.OpAdd/OpUpdate/OpRemove`, `*gorm.DB`.
- Produces: `func NewPgBoard(db *gorm.DB) *PgBoard` implementing `agentdb.BoardStore`. `Append`
  assigns a monotonic `seq = MAX(seq)+1` and a deterministic revision id `"r{seq}"` inside a
  transaction (serialized by a store mutex for the single-writer v1), upserts `board_head`, and
  returns the id. `Current`/`AsOf` fold `prompt_fragment` ops in ascending `seq` order (parity with
  `MemBoard`); `Head` reads `board_head`.

> **Revision-id parity:** `PgBoard` yields `r1`, `r2`, … exactly like `MemBoard`, so the same test
> bodies assert against both. See "Contract gaps found" for why this diverges from the
> `board.go` "let Postgres assign seq (BIGSERIAL)" comment — the sqlite test story requires an
> explicit, driver-portable `seq`.

- [ ] **Step 1: Write the failing test** (`board_test.go`)

```go
package pgstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pgstore.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&agentdb.BoardRevision{}, &agentdb.BoardHead{}, &agentdb.BoardPromptFragment{},
		&agentdb.Ticket{}, &agentdb.TelemetryRun{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func fragOp(kind agentdb.OpKind, id, body string) agentdb.Op {
	b, _ := json.Marshal(agentdb.BoardPromptFragment{ID: id, Kind: "role", Body: body})
	return agentdb.Op{Op: kind, EntityType: "prompt_fragment", EntityID: id, Body: b}
}

// TestPgBoardAppendFoldAndPin mirrors orchestrator.TestMemBoardAppendFoldAndPin.
func TestPgBoardAppendFoldAndPin(t *testing.T) {
	ctx := context.Background()
	b := NewPgBoard(newTestDB(t))

	r1, err := b.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be basic.")}})
	if err != nil {
		t.Fatalf("append r1: %v", err)
	}
	if r1 != "r1" {
		t.Fatalf("first revision id = %q, want r1", r1)
	}

	r2, err := b.Append(ctx, agentdb.Changeset{Author: "human", Message: "note",
		Ops: []agentdb.Op{fragOp(agentdb.OpUpdate, "routing-guidance", "Be clever.")}})
	if err != nil {
		t.Fatalf("append r2: %v", err)
	}
	if r2 != "r2" {
		t.Fatalf("second revision id = %q, want r2", r2)
	}

	cur, err := b.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if cur.Revision != r2 || len(cur.Fragments) != 1 || cur.Fragments[0].Body != "Be clever." {
		t.Fatalf("current folded wrong: rev=%s frags=%+v", cur.Revision, cur.Fragments)
	}
	if cur.Fragments[0].LastChangedIn != r2 {
		t.Fatalf("LastChangedIn = %q, want r2", cur.Fragments[0].LastChangedIn)
	}

	as1, err := b.AsOf(ctx, r1)
	if err != nil {
		t.Fatalf("asof: %v", err)
	}
	if len(as1.Fragments) != 1 || as1.Fragments[0].Body != "Be basic." {
		t.Fatalf("asof r1 wrong: %+v", as1.Fragments)
	}
	if head, _ := b.Head(ctx); head != r2 {
		t.Fatalf("head = %q, want r2", head)
	}
}

// TestPgBoardRemoveFolds mirrors MemBoard's remove behaviour.
func TestPgBoardRemoveFolds(t *testing.T) {
	ctx := context.Background()
	b := NewPgBoard(newTestDB(t))
	_, _ = b.Append(ctx, agentdb.Changeset{Author: "h", Message: "add",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "f1", "one")}})
	_, _ = b.Append(ctx, agentdb.Changeset{Author: "h", Message: "rm",
		Ops: []agentdb.Op{{Op: agentdb.OpRemove, EntityType: "prompt_fragment", EntityID: "f1"}}})
	cur, err := b.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if len(cur.Fragments) != 0 {
		t.Fatalf("expected fragment removed, got %+v", cur.Fragments)
	}
}

// TestMemVsPgParity feeds identical changesets to both impls and asserts equal folds.
func TestMemVsPgParity(t *testing.T) {
	ctx := context.Background()
	mem := orchestrator.NewMemBoard()
	pg := NewPgBoard(newTestDB(t))
	changes := []agentdb.Changeset{
		{Author: "h", Message: "seed", Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing", "Be basic.")}},
		{Author: "h", Message: "role", Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "role", "Writer.")}},
		{Author: "h", Message: "note", Ops: []agentdb.Op{fragOp(agentdb.OpUpdate, "routing", "Be clever.")}},
	}
	for i, cs := range changes {
		mr, err := mem.Append(ctx, cs)
		if err != nil {
			t.Fatalf("mem append %d: %v", i, err)
		}
		pr, err := pg.Append(ctx, cs)
		if err != nil {
			t.Fatalf("pg append %d: %v", i, err)
		}
		if mr != pr {
			t.Fatalf("revision id mismatch at %d: mem=%q pg=%q", i, mr, pr)
		}
	}
	mc, _ := mem.Current(ctx)
	pc, _ := pg.Current(ctx)
	if mc.Revision != pc.Revision || len(mc.Fragments) != len(pc.Fragments) {
		t.Fatalf("parity broke: mem=%+v pg=%+v", mc, pc)
	}
	for i := range mc.Fragments {
		if mc.Fragments[i] != pc.Fragments[i] {
			t.Fatalf("fragment %d differs: mem=%+v pg=%+v", i, mc.Fragments[i], pc.Fragments[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/pgstore/ -run 'TestPgBoard|TestMemVsPgParity' -v`
Expected: FAIL — `undefined: NewPgBoard`.

- [ ] **Step 3: Write minimal implementation**

`go/orchestrator/pgstore/pgstore.go`:

```go
// Package pgstore holds the Postgres (gorm) implementations of the Slice-A data
// seams: agentdb.BoardStore (PgBoard), orchestrator.TicketStore (PgTicketStore),
// and orchestrator.Telemetry (PgTelemetry). It imports agentdb (row models +
// Board types) and orchestrator (domain types + interfaces); agentdb never
// imports orchestrator, so there is no cycle. Fast tests run against sqlite via
// AutoMigrate (the agentdb/board_test.go pattern); production wires *gorm.DB from
// agentdb.Store.DB().
package pgstore

import (
	"encoding/json"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func opsToJSONArray(ops []agentdb.Op) agentdb.JSONArray {
	b, _ := json.Marshal(ops)
	return agentdb.JSONArray(b)
}

func jsonArrayBytes(j agentdb.JSONArray) []byte {
	if len(j) == 0 {
		return []byte("[]")
	}
	return []byte(j)
}
```

`go/orchestrator/pgstore/board.go`:

```go
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// PgBoard is a Postgres-backed agentdb.BoardStore: an append-only changeset log
// (board_revisions) folded on read, with a single-row head pointer (board_head).
// Revision ids are a deterministic "r{seq}" counter for parity with MemBoard.
type PgBoard struct {
	db *gorm.DB
	mu sync.Mutex // serialize Append for the single-writer v1 (monotonic seq)
}

// NewPgBoard returns a BoardStore over db. db must have the board tables migrated
// (agentdb migration 020/021 in prod; AutoMigrate in tests).
func NewPgBoard(db *gorm.DB) *PgBoard { return &PgBoard{db: db} }

// Append records a changeset as the next revision and moves head to it.
func (b *PgBoard) Append(ctx context.Context, cs agentdb.Changeset) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var id string
	err := b.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&agentdb.BoardRevision{}).
			Select("COALESCE(MAX(seq),0)").Scan(&maxSeq).Error; err != nil {
			return fmt.Errorf("pgboard: max seq: %w", err)
		}
		seq := maxSeq + 1
		id = fmt.Sprintf("r%d", seq)
		rev := agentdb.BoardRevision{
			ID: id, ParentID: cs.ParentID, Seq: seq, Status: "applied",
			Author: cs.Author, Message: cs.Message, Ops: opsToJSONArray(cs.Ops),
			CreatedAt: time.Now().Unix(),
		}
		if err := tx.Create(&rev).Error; err != nil {
			return fmt.Errorf("pgboard: insert revision: %w", err)
		}
		head := agentdb.BoardHead{Singleton: true, RevisionID: id}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "singleton"}},
			DoUpdates: clause.AssignmentColumns([]string{"revision_id"}),
		}).Create(&head).Error; err != nil {
			return fmt.Errorf("pgboard: upsert head: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Current folds the whole log (through head) into the live board state.
func (b *PgBoard) Current(ctx context.Context) (agentdb.Board, error) {
	head, err := b.Head(ctx)
	if err != nil {
		return agentdb.Board{}, err
	}
	return b.AsOf(ctx, head)
}

// AsOf folds the log in seq order up to and including revisionID.
func (b *PgBoard) AsOf(ctx context.Context, revisionID string) (agentdb.Board, error) {
	var revs []agentdb.BoardRevision
	if err := b.db.WithContext(ctx).Order("seq asc").Find(&revs).Error; err != nil {
		return agentdb.Board{}, fmt.Errorf("pgboard: load revisions: %w", err)
	}
	frags := map[string]agentdb.BoardPromptFragment{}
	var found bool
	for _, rev := range revs {
		var ops []agentdb.Op
		if err := json.Unmarshal(jsonArrayBytes(rev.Ops), &ops); err != nil {
			return agentdb.Board{}, fmt.Errorf("fold %s: ops: %w", rev.ID, err)
		}
		for _, op := range ops {
			if op.EntityType != "prompt_fragment" {
				continue
			}
			switch op.Op {
			case agentdb.OpAdd, agentdb.OpUpdate:
				var f agentdb.BoardPromptFragment
				if err := json.Unmarshal(op.Body, &f); err != nil {
					return agentdb.Board{}, fmt.Errorf("fold %s: fragment: %w", rev.ID, err)
				}
				f.LastChangedIn = rev.ID
				frags[op.EntityID] = f
			case agentdb.OpRemove:
				delete(frags, op.EntityID)
			}
		}
		if rev.ID == revisionID {
			found = true
			break
		}
	}
	if !found {
		return agentdb.Board{}, fmt.Errorf("revision %q not found", revisionID)
	}
	out := agentdb.Board{Revision: revisionID}
	for _, f := range frags {
		out.Fragments = append(out.Fragments, f)
	}
	sort.Slice(out.Fragments, func(i, j int) bool { return out.Fragments[i].ID < out.Fragments[j].ID })
	return out, nil
}

// Head returns the currently-live applied revision id.
func (b *PgBoard) Head(ctx context.Context) (string, error) {
	var head agentdb.BoardHead
	err := b.db.WithContext(ctx).First(&head).Error
	if err != nil {
		return "", fmt.Errorf("pgboard: board empty: %w", err)
	}
	return head.RevisionID, nil
}

// compile-time assertion that PgBoard satisfies the seam.
var _ agentdb.BoardStore = (*PgBoard)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/pgstore/ -run 'TestPgBoard|TestMemVsPgParity' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/pgstore/pgstore.go go/orchestrator/pgstore/board.go go/orchestrator/pgstore/board_test.go
git commit -m "feat(pgstore): PgBoard — Postgres BoardStore with MemBoard parity"
```

---

### Task 7: PgTicketStore — Postgres TicketStore (CRUD + list)

**Files:**
- Create: `go/orchestrator/pgstore/tickets.go`
- Test: `go/orchestrator/pgstore/tickets_test.go`

**Interfaces:**
- Consumes: `orchestrator.Ticket`, `orchestrator.TicketStatus`, `agentdb.Ticket` (row),
  `agentdb.JSONArray`, `*gorm.DB`, `github.com/google/uuid`.
- Produces: `func NewPgTicketStore(db *gorm.DB) *PgTicketStore` implementing
  `orchestrator.TicketStore`:
  - `Create(ctx, t) (id, err)` — allocate `uuid` if `t.ID==""`; stamp `CreatedAt`/`UpdatedAt`; insert.
  - `Update(ctx, t) error` — stamp `UpdatedAt`; `Save`.
  - `Get(ctx, id) (Ticket, error)`.
  - `List(ctx, status) ([]Ticket, error)` — `status==""` → all; else filter; ordered by `created_at`.
  - Round-trips `Scope`/`Result`/`PendingPost` (`json.RawMessage` ↔ `JSONArray`) and `DependsOn`
    (`[]string` ↔ JSONB).

- [ ] **Step 1: Write the failing test** (`tickets_test.go`)

```go
package pgstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestPgTicketStoreCRUD(t *testing.T) {
	ctx := context.Background()
	ts := NewPgTicketStore(newTestDB(t))

	id, err := ts.Create(ctx, orchestrator.Ticket{
		ProjectID: "badcode", Title: "Draft post", Objective: "write X",
		Acceptance: "on-brand", Status: orchestrator.StatusTodo,
		Scope: json.RawMessage(`{"name":"post-writer"}`), DependsOn: []string{"t0"}, BoardRev: "r3",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == "" {
		t.Fatalf("expected generated id")
	}

	got, err := ts.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Draft post" || got.Status != orchestrator.StatusTodo ||
		len(got.DependsOn) != 1 || got.DependsOn[0] != "t0" || string(got.Scope) != `{"name":"post-writer"}` {
		t.Fatalf("get round-trip wrong: %+v", got)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatalf("timestamps not stamped: %+v", got)
	}

	got.Status = orchestrator.StatusInReview
	got.Result = json.RawMessage(`{"status":"done"}`)
	if err := ts.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := ts.Get(ctx, id)
	if again.Status != orchestrator.StatusInReview || string(again.Result) != `{"status":"done"}` {
		t.Fatalf("update not persisted: %+v", again)
	}
}

func TestPgTicketStoreList(t *testing.T) {
	ctx := context.Background()
	ts := NewPgTicketStore(newTestDB(t))
	_, _ = ts.Create(ctx, orchestrator.Ticket{ID: "a", Title: "A", Status: orchestrator.StatusTodo})
	_, _ = ts.Create(ctx, orchestrator.Ticket{ID: "b", Title: "B", Status: orchestrator.StatusNeedsHuman})
	_, _ = ts.Create(ctx, orchestrator.Ticket{ID: "c", Title: "C", Status: orchestrator.StatusTodo})

	all, err := ts.List(ctx, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list all = %d, want 3", len(all))
	}
	todo, err := ts.List(ctx, orchestrator.StatusTodo)
	if err != nil {
		t.Fatalf("list todo: %v", err)
	}
	if len(todo) != 2 {
		t.Fatalf("list todo = %d, want 2", len(todo))
	}
	nh, _ := ts.List(ctx, orchestrator.StatusNeedsHuman)
	if len(nh) != 1 || nh[0].ID != "b" {
		t.Fatalf("list needs_human wrong: %+v", nh)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/pgstore/ -run 'TestPgTicketStore' -v`
Expected: FAIL — `undefined: NewPgTicketStore`.

- [ ] **Step 3: Write minimal implementation** (`tickets.go`)

```go
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// PgTicketStore is a Postgres-backed orchestrator.TicketStore. Ungated work
// state; not versioned.
type PgTicketStore struct{ db *gorm.DB }

// NewPgTicketStore returns a TicketStore over db (tickets table migrated).
func NewPgTicketStore(db *gorm.DB) *PgTicketStore { return &PgTicketStore{db: db} }

func toRow(t orchestrator.Ticket) agentdb.Ticket {
	dep, _ := json.Marshal(t.DependsOn)
	row := agentdb.Ticket{
		ID: t.ID, ProjectID: t.ProjectID, Title: t.Title, Objective: t.Objective,
		Acceptance: t.Acceptance, Status: string(t.Status),
		Scope: rawToJSON(t.Scope, "{}"), Result: rawToJSON(t.Result, "{}"),
		PendingPost: rawToJSON(t.PendingPost, "{}"), DependsOn: agentdb.JSONArray(dep),
		Parent: t.Parent, Attempts: t.Attempts, BoardRev: t.BoardRev,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	return row
}

func fromRow(r agentdb.Ticket) orchestrator.Ticket {
	var dep []string
	_ = json.Unmarshal(jsonArrayBytes(r.DependsOn), &dep)
	return orchestrator.Ticket{
		ID: r.ID, ProjectID: r.ProjectID, Title: r.Title, Objective: r.Objective,
		Acceptance: r.Acceptance, Status: orchestrator.TicketStatus(r.Status),
		Scope: json.RawMessage(jsonArrayBytes(r.Scope)), Result: json.RawMessage(jsonArrayBytes(r.Result)),
		PendingPost: json.RawMessage(jsonArrayBytes(r.PendingPost)), DependsOn: dep,
		Parent: r.Parent, Attempts: r.Attempts, BoardRev: r.BoardRev,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func rawToJSON(raw json.RawMessage, empty string) agentdb.JSONArray {
	if len(raw) == 0 {
		return agentdb.JSONArray(empty)
	}
	return agentdb.JSONArray(raw)
}

// Create inserts a ticket, allocating an id and timestamps if unset.
func (s *PgTicketStore) Create(ctx context.Context, t orchestrator.Ticket) (string, error) {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	now := time.Now().Unix()
	if t.CreatedAt == 0 {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	if t.Status == "" {
		t.Status = orchestrator.StatusBacklog
	}
	row := toRow(t)
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return "", fmt.Errorf("pgticket: create %q: %w", t.ID, err)
	}
	return t.ID, nil
}

// Update overwrites a ticket's mutable fields.
func (s *PgTicketStore) Update(ctx context.Context, t orchestrator.Ticket) error {
	if t.ID == "" {
		return fmt.Errorf("pgticket: update requires an id")
	}
	t.UpdatedAt = time.Now().Unix()
	row := toRow(t)
	if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
		return fmt.Errorf("pgticket: update %q: %w", t.ID, err)
	}
	return nil
}

// Get returns a ticket by id.
func (s *PgTicketStore) Get(ctx context.Context, id string) (orchestrator.Ticket, error) {
	var row agentdb.Ticket
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return orchestrator.Ticket{}, fmt.Errorf("pgticket: get %q: %w", id, err)
	}
	return fromRow(row), nil
}

// List returns tickets filtered by status ("" = all), ordered by creation.
func (s *PgTicketStore) List(ctx context.Context, status orchestrator.TicketStatus) ([]orchestrator.Ticket, error) {
	q := s.db.WithContext(ctx).Model(&agentdb.Ticket{}).Order("created_at asc, id asc")
	if status != "" {
		q = q.Where("status = ?", string(status))
	}
	var rows []agentdb.Ticket
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("pgticket: list %q: %w", status, err)
	}
	out := make([]orchestrator.Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}

var _ orchestrator.TicketStore = (*PgTicketStore)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/pgstore/ -run 'TestPgTicketStore' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/pgstore/tickets.go go/orchestrator/pgstore/tickets_test.go
git commit -m "feat(pgstore): PgTicketStore — Postgres TicketStore (CRUD + list)"
```

---

### Task 8: PgTelemetry — Postgres Telemetry (append-only run log)

**Files:**
- Create: `go/orchestrator/pgstore/telemetry.go`
- Test: `go/orchestrator/pgstore/telemetry_test.go`

**Interfaces:**
- Consumes: `orchestrator.Run`, `orchestrator.Telemetry`, `agentdb.TelemetryRun` (row), `*gorm.DB`.
- Produces: `func NewPgTelemetry(db *gorm.DB) *PgTelemetry` implementing `orchestrator.Telemetry`
  (`Record(r Run) Run`, `Runs() []Run`). `Record` assigns `Seq = MAX(seq)+1` and `ID = "run{seq}"`
  in a transaction (parity with `MemTelemetry`) and persists.
  **Contract-gap note:** the `Telemetry` interface has **no `ctx`/`error`** (Slice-0 shape, §5), so a
  DB failure in `Record`/`Runs` cannot be returned. This impl is **best-effort**: on error it logs via
  `log.Printf` and returns the input `Run` unchanged (`Record`) / an empty slice (`Runs`). See
  "Contract gaps found".

- [ ] **Step 1: Write the failing test** (`telemetry_test.go`)

```go
package pgstore

import (
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// TestPgTelemetryRecordsInOrder mirrors orchestrator.TestTelemetryRecordsInOrder.
func TestPgTelemetryRecordsInOrder(t *testing.T) {
	tel := NewPgTelemetry(newTestDB(t))
	a := tel.Record(orchestrator.Run{Scope: "manager", BoardRevision: "r1", Output: "dumb plan"})
	b := tel.Record(orchestrator.Run{Scope: "manager", BoardRevision: "r2", Output: "clever plan"})
	if a.ID != "run1" || a.Seq != 1 || b.ID != "run2" || b.Seq != 2 {
		t.Fatalf("ids/seq wrong: %+v %+v", a, b)
	}
	runs := tel.Runs()
	if len(runs) != 2 || runs[0].BoardRevision != "r1" || runs[1].BoardRevision != "r2" {
		t.Fatalf("runs wrong: %+v", runs)
	}
	if runs[0].Output != "dumb plan" || runs[1].Output != "clever plan" {
		t.Fatalf("outputs not persisted: %+v", runs)
	}
}

var _ orchestrator.Telemetry = (*PgTelemetry)(nil)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/pgstore/ -run 'TestPgTelemetry' -v`
Expected: FAIL — `undefined: NewPgTelemetry`.

- [ ] **Step 3: Write minimal implementation** (`telemetry.go`)

```go
package pgstore

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// PgTelemetry is a Postgres-backed orchestrator.Telemetry (append-only run log).
// The §5 interface has no ctx/error, so writes are best-effort: failures are
// logged, not returned (see the Slice-A plan's "Contract gaps found").
type PgTelemetry struct {
	db *gorm.DB
	mu sync.Mutex // serialize Record for monotonic seq (single-writer v1)
}

// NewPgTelemetry returns a Telemetry over db (runs table migrated).
func NewPgTelemetry(db *gorm.DB) *PgTelemetry { return &PgTelemetry{db: db} }

// Record appends a run, assigning its 1-based Seq and "run{seq}" id, and returns it.
func (t *PgTelemetry) Record(r orchestrator.Run) orchestrator.Run {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx := context.Background()
	err := t.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&agentdb.TelemetryRun{}).
			Select("COALESCE(MAX(seq),0)").Scan(&maxSeq).Error; err != nil {
			return err
		}
		seq := maxSeq + 1
		r.Seq = int(seq)
		r.ID = fmt.Sprintf("run%d", seq)
		row := agentdb.TelemetryRun{
			ID: r.ID, Seq: seq, Scope: r.Scope, BoardRevision: r.BoardRevision,
			Prompt: r.Prompt, Output: r.Output, CreatedAt: time.Now().Unix(),
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		log.Printf("[pgtelemetry] record failed (best-effort, dropped): %v", err)
		return r
	}
	return r
}

// Runs returns all recorded runs in seq order.
func (t *PgTelemetry) Runs() []orchestrator.Run {
	var rows []agentdb.TelemetryRun
	if err := t.db.WithContext(context.Background()).Order("seq asc").Find(&rows).Error; err != nil {
		log.Printf("[pgtelemetry] runs failed (best-effort, empty): %v", err)
		return nil
	}
	out := make([]orchestrator.Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, orchestrator.Run{
			ID: r.ID, Seq: int(r.Seq), Scope: r.Scope, BoardRevision: r.BoardRevision,
			Prompt: r.Prompt, Output: r.Output,
		})
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/pgstore/ -run 'TestPgTelemetry' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/pgstore/telemetry.go go/orchestrator/pgstore/telemetry_test.go
git commit -m "feat(pgstore): PgTelemetry — Postgres Telemetry with MemTelemetry parity"
```

---

### Task 9: Live-Postgres migration smoke test (env-gated) + full green

**Files:**
- Create: `go/orchestrator/pgstore/migration_pg_test.go`

**Interfaces:**
- Consumes: `agentdb.Open` (runs the numbered migrations, incl. 020–024), `agentdb.Store.DB()`,
  `NewPgBoard`, `NewPgTicketStore`, `NewPgTelemetry`.
- Produces: an integration test that, **only when `AGENTKIT_TEST_POSTGRES_URL` is set**, opens a real
  Postgres via `agentdb.Open` (applying the SQL migrations), then runs one append/ticket/telemetry
  round-trip through the Postgres impls to prove the SQL schema matches the gorm expectations. Skips
  otherwise, matching the repo's `t.Skip` integration idiom (`ociregistry`/`gcsblob`).

- [ ] **Step 1: Write the test**

```go
package pgstore

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// TestPostgresMigrationsRoundTrip exercises the real SQL migrations (020-024)
// against a live Postgres. Set AGENTKIT_TEST_POSTGRES_URL to run it, e.g.
//   AGENTKIT_TEST_POSTGRES_URL=postgres://user:pass@localhost:5432/agentorange?sslmode=disable
func TestPostgresMigrationsRoundTrip(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres migration test")
	}
	ctx := context.Background()
	store, err := agentdb.Open(url) // runs numbered migrations incl. 022-024
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db := store.DB()

	board := NewPgBoard(db)
	rev, err := board.Append(ctx, agentdb.Changeset{Author: "it", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be clever.")}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	cur, err := board.Current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if cur.Revision != rev || len(cur.Fragments) == 0 {
		t.Fatalf("fold wrong on postgres: %+v", cur)
	}

	tickets := NewPgTicketStore(db)
	id, err := tickets.Create(ctx, orchestrator.Ticket{
		Title: "it", Status: orchestrator.StatusTodo, Scope: json.RawMessage(`{"name":"w"}`),
	})
	if err != nil {
		t.Fatalf("ticket create: %v", err)
	}
	if _, err := tickets.Get(ctx, id); err != nil {
		t.Fatalf("ticket get: %v", err)
	}

	tel := NewPgTelemetry(db)
	if got := tel.Record(orchestrator.Run{Scope: "manager", BoardRevision: rev, Output: "o"}); got.ID == "" {
		t.Fatalf("telemetry record produced no id")
	}
	if len(tel.Runs()) == 0 {
		t.Fatalf("telemetry runs empty after record")
	}
}
```

- [ ] **Step 2: Run it (skips without a DB) + full package green**

Run: `cd go && go test ./orchestrator/... ./agentdb/... -v`
Expected: PASS; `TestPostgresMigrationsRoundTrip` reports `SKIP` unless `AGENTKIT_TEST_POSTGRES_URL`
is set. (If a Postgres is available, export the URL and confirm it PASSES.)

- [ ] **Step 3: Whole-tree verification + commit**

Run: `cd go && go build ./... && go vet ./... && go test ./orchestrator/... ./agentdb/...`
Expected: all green.

```bash
git add go/orchestrator/pgstore/migration_pg_test.go
git commit -m "test(pgstore): env-gated live-Postgres migration round-trip"
```

---

### Task 10 (OPTIONAL): rename board_prompt_fragments → fragments

Contract §4/§10 say the fragments table **MAY** be named `fragments`. This task is optional; do it
only if the team wants the v1 name now. It touches the gorm `TableName()` and adds a rename
migration. It changes **no behaviour** (the fold reads `prompt_fragment` **ops**, not the table).

**Files:**
- Modify: `go/agentdb/board.go` (`BoardPromptFragment.TableName()` → `"fragments"`)
- Modify: `go/agentdb/migrations.go` (add `025_rename_fragments`)
- Modify: `go/agentdb/board_test.go` note (AutoMigrate is name-transparent; no assertion change needed)

**Interfaces:**
- Consumes: existing `BoardPromptFragment` model + `board_prompt_fragments` table (migration 021).
- Produces: table renamed to `fragments`; the gorm model's `TableName()` returns `"fragments"`.

- [ ] **Step 1: Write the failing test** (append to `board_v1_test.go`)

```go
func TestFragmentsTableName(t *testing.T) {
	if (BoardPromptFragment{}).TableName() != "fragments" {
		t.Fatalf("TableName = %q, want fragments", (BoardPromptFragment{}).TableName())
	}
	if !contains(have025(), "ALTER TABLE board_prompt_fragments RENAME TO fragments") {
		t.Fatalf("025 migration missing rename")
	}
}

func have025() string {
	for _, m := range agentMigrations {
		if m.Name == "025_rename_fragments" {
			return m.SQL
		}
	}
	return ""
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./agentdb/ -run TestFragmentsTableName -v`
Expected: FAIL — `TableName = "board_prompt_fragments"`.

- [ ] **Step 3: Write minimal implementation**

Edit `board.go`:

```go
func (BoardPromptFragment) TableName() string { return "fragments" }
```

Append to `agentMigrations` (after `024_runs`):

```go
	{
		Name: "025_rename_fragments",
		SQL: `
			ALTER TABLE IF EXISTS board_prompt_fragments RENAME TO fragments;
		`,
	},
```

- [ ] **Step 4: Run test + full green**

Run: `cd go && go test ./agentdb/... ./orchestrator/... && go build ./... && go vet ./...`
Expected: PASS. (Fresh sqlite AutoMigrate now creates `fragments`; the `PgBoard`/`board_test.go`
tests are unaffected because they use the model, not the table name.)

- [ ] **Step 5: Commit**

```bash
git add go/agentdb/board.go go/agentdb/migrations.go go/agentdb/board_v1_test.go
git commit -m "feat(agentdb): rename board_prompt_fragments -> fragments (migration 025)"
```

---

## Self-Review notes

- **Spec coverage (contract §9 Slice A row):** `BoardStore` Postgres impl (Task 6) ✓; `TicketStore`
  Postgres impl (Task 7) ✓; `Telemetry` Postgres impl (Task 8) ✓; migrations + §0 collapse (Task 5,
  drops `board_staff`/`board_pipelines`/`board_event_types`, adds `tickets` + `runs`) ✓; optional
  `fragments` rename (Task 10) ✓. Consumes only frozen types (`Board`/`Changeset`/`Op`/`Ticket`/`Run`);
  produces no new interface shapes beyond the frozen `TicketStatus`/`Ticket`/`TicketStore`/`Telemetry`.
- **MemBoard/MemTelemetry stay** as the dev/test doubles behind the same interfaces (§10) — Tasks 2/3
  only retype `Telemetry`; `MemBoard` is untouched and is used as the parity oracle in Task 6.
- **Test DB story:** sqlite via `github.com/glebarez/sqlite` + `AutoMigrate` for fast unit/parity
  tests (the `agentdb/board_test.go` pattern); the raw SQL migrations are exercised by an env-gated
  (`AGENTKIT_TEST_POSTGRES_URL`) live-Postgres test (Task 9), matching the repo's `t.Skip`
  integration idiom.
- **No new external deps** — gorm, glebarez/sqlite, google/uuid, gorm clause are all already in
  `go.mod`. Liftability preserved (no host-app imports).
- **Placeholder scan:** none — every code step is real, compilable Go.

## Contract gaps found

1. **`Telemetry` interface has no `ctx`/`error` (§5), but its Slice-A impl is a network DB.**
   `Record(r Run) Run` and `Runs() []Run` cannot surface a Postgres failure. `PgTelemetry` is
   therefore **best-effort** (logs + drops on write error; returns `nil` on read error), which is a
   silent-failure seam for the "show your work" substrate. *Resolution used:* best-effort + `log.Printf`.
   *Recommended contract change (escalate, do not self-apply):* evolve to
   `Record(ctx, Run) (Run, error)` / `Runs(ctx) ([]Run, error)` — this ripples to Slice 0's
   `MemTelemetry`, `Runner`, and the §8 `GET /api/runs`, so it is a stop-and-escalate, not a local edit.

2. **Revision-`seq` mechanism: `board.go` comment vs the requested sqlite test story.** The
   `BoardRevision` doc-comment prescribes, "let Postgres assign seq (omit it from the INSERT, then read
   it back)… never write a zero Seq," relying on `BIGSERIAL`. That mechanism is Postgres-only and
   cannot run on the sqlite fast-test path this slice is told to prefer. *Resolution used:* `PgBoard`
   sets `seq = MAX(seq)+1` explicitly inside a mutex-serialized transaction and derives `id = "r{seq}"`
   (driver-portable; parity with `MemBoard`; single-writer v1). This is safe for v1's single-box,
   single-operator model but is **not** safe for concurrent multi-writer Postgres — flagged for the
   day multi-writer lands. (Not a frozen-contract change — `board.go` is a code comment — but a real
   tension worth recording.)

3. **§0 collapse drops tables whose Go models + tests remain.** The migration drops `board_staff`,
   `board_pipelines`, `board_event_types`, but the gorm models (`agentdb.BoardStaff`/`BoardPipeline`/
   `BoardEventType`), their fields on the `agentdb.Board` aggregate, and `agentdb/board_test.go`
   assertions are left in place (those tests `AutoMigrate` their own sqlite tables, so they still pass
   against the dropped-in-Postgres tables). Slice A's minimal scope is the SQL collapse + the three
   seams; a full Go-model/aggregate cleanup is **not** in this slice. Flagged so a later slice removes
   the dead models rather than leaving `Board.Staff`/`.Pipelines`/`.EventTypes` permanently unfoldable.

4. **`board_subscriptions` is NOT in the collapse list, though the event bus is deferred (§1).** Both
   §10 and the deployment plan enumerate dropping staff/pipelines/event_types only; subscriptions
   (pure event-bus machinery) are left standing. Followed the explicit list (kept `board_subscriptions`),
   but note the inconsistency: if the bus is deferred, `board_subscriptions` is also dead weight in v1.

5. **`Ticket.ProjectID` exists but v1 is single-project (deployment plan §"v1 definition").** Kept the
   field verbatim (frozen §4) and defaulted it to `''`; no multi-project logic is built. Noted only so
   nobody reads `ProjectID` as a v1 multi-project signal.
