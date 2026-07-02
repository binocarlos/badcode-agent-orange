# Slice E — The Watch / Approve / Note Surface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the **human-in-the-loop surface** of v1 — the one screen that is simultaneously a **control panel** (approve/reject drafted posts), a **teacher's desk** (leave a note that edits guidance), and a **content artifact** (the legible board-revision + telemetry "watch it learn" story). Implement the HTTP API of contracts §8 **exactly**, plus a thin web client that is purely a consumer of it. The API is the contract; the UI is thin.

**Architecture:** A new Go package `go/orchestrator/watchapi` mounts `net/http` handlers for the eight §8 routes on a `ServeMux` (go1.22 method+path patterns), guarded by a single shared-token auth middleware. The handlers depend **only on small interfaces (ports)**, never concrete impls: `agentdb.BoardStore` + a `RevisionLister` for the timeline, `orchestrator.TicketStore` for the board work items, a `TelemetryReader` for the run log, and three thin action ports — `Approver` (approve → the Slice-D publish flow), `FeedbackApplier` (reject-note + `/api/feedback` → `write_fragment`), and `Triggerer` (fire one `ManagerExchange`). This makes Slice E fully testable with `net/http/httptest` + fakes (`MemBoard`, an in-memory `MemTicketStore`, the existing `*orchestrator.Telemetry`, and recording fakes for the three action ports) — **no Slice A/C/D required**. The web client is a single embedded static page (`go:embed` HTML + vanilla-JS `fetch`) served at `/`; no npm build, offline, deterministic.

**Tech Stack:** Go 1.25, standard library only (`net/http`, `net/http/httptest`, `encoding/json`, `embed`). Reuses `agentdb` structs and the Slice-0 `orchestrator` package. No React build, no DB, no network — every test is offline and deterministic.

## Global Constraints

- Module path `github.com/binocarlos/badcode-agent-orange`; new package under `go/orchestrator/watchapi/`. One line.
- `go build ./...` and `go vet ./...` must stay green. One line.
- **Consume the frozen contract types verbatim** (contracts §3/§4) — never redefine or renegotiate a contract; a needed change is stop-and-escalate. One line.
- **Handlers depend on interfaces, not concrete impls** — `BoardStore`, `RevisionLister`, `TicketStore`, `TelemetryReader`, `Approver`, `FeedbackApplier`, `Triggerer`; testable without Slices A/C/D. One line.
- **No external dependencies** — stdlib only; the web client is embedded static assets, no npm/React build in this fork. One line.
- **In-memory fakes only** behind the ports (`MemBoard`, `MemTicketStore`, `*orchestrator.Telemetry`, recording action fakes); Postgres/real connector/manager loop swap in behind the same ports in their own slices. One line.
- **The publish-approval floor (C3) is honored, not implemented here** — Slice E never touches a `Connector`; approve calls the injected `Approver` port only, so a worker still cannot publish and the gate stays in mechanism. One line.
- **The API is the contract, the UI is thin** — the web client only `fetch`es the §8 routes; no business logic in JS. One line.
- Auth for v1 is a **single shared bearer token** (single-tenant, one operator); empty token disables the guard for local dev. One line.
- TDD: failing `httptest` test first, minimal handler, frequent commits; table-driven where natural. One line.

---

### Task 1: Shared frozen contract types (Ticket, HumanFeedback, Post, TicketStore)

**Files:**
- Create: `go/orchestrator/contracts.go`
- Test: `go/orchestrator/contracts_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardStore` (unchanged), `encoding/json`.
- Produces (verbatim from contracts §3/§4/§5, the shared declarations Slice E needs to compile and that later slices consume): `type TicketStatus string` + the seven status consts; `type Ticket struct{...}`; `type HumanFeedback struct{ TargetRef, Note string }`; `type Post struct{ Channel, Text string; Media []string }`; `type TicketStore interface{ Create/Update/Get/List }`.
- **Contract gap (see final section):** §9 does not assign the *Go declaration* of these frozen types to any slice. Slice E is the first slice that must compile against them, so it declares them here. If Slice A has already landed them, delete this file and import theirs — the shapes are identical by construction.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"
)

func TestTicketStatusStrings(t *testing.T) {
	if StatusNeedsHuman != "needs_human" || StatusInReview != "in_review" || StatusDone != "done" {
		t.Fatalf("frozen status strings drifted: %q %q %q", StatusNeedsHuman, StatusInReview, StatusDone)
	}
}

func TestHumanFeedbackAndPostShapes(t *testing.T) {
	fb := HumanFeedback{TargetRef: "fragment:routing-guidance", Note: "be clever"}
	p := Post{Channel: "bluesky", Text: "hello", Media: nil}
	if fb.TargetRef == "" || p.Channel == "" {
		t.Fatalf("zero shapes: %+v %+v", fb, p)
	}
}

// A ticket store double must satisfy the frozen interface.
func TestMemTicketStoreImplementsTicketStore(t *testing.T) {
	var _ TicketStore = (*memTicketStoreProbe)(nil)
	_ = context.Background()
}

// compile-only probe (real impl lands in Task 2).
type memTicketStoreProbe struct{}

func (*memTicketStoreProbe) Create(context.Context, Ticket) (string, error)         { return "", nil }
func (*memTicketStoreProbe) Update(context.Context, Ticket) error                    { return nil }
func (*memTicketStoreProbe) Get(context.Context, string) (Ticket, error)             { return Ticket{}, nil }
func (*memTicketStoreProbe) List(context.Context, TicketStatus) ([]Ticket, error)    { return nil, nil }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run 'TicketStatus|HumanFeedback|TicketStore' -v`
Expected: FAIL — `undefined: StatusNeedsHuman` / `undefined: Ticket`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"encoding/json"
)

// TicketStatus — the board lanes (contracts §3, frozen strings).
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

// Ticket — a board work item (contracts §4, frozen). Work state; ungated; NOT in
// the versioned log. Scope/Result/PendingPost are opaque JSON to the board.
type Ticket struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	Title       string          `json:"title"`
	Objective   string          `json:"objective"`
	Acceptance  string          `json:"acceptance"`
	Status      TicketStatus    `json:"status"`
	Scope       json.RawMessage `json:"scope,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	PendingPost json.RawMessage `json:"pending_post,omitempty"`
	DependsOn   []string        `json:"depends_on,omitempty"`
	Parent      string          `json:"parent,omitempty"`
	Attempts    int             `json:"attempts"`
	BoardRev    string          `json:"board_rev"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
}

// HumanFeedback — a targeted note that drives the learning loop (contracts §4).
// TargetRef is "ticket:<id>" | "run:<id>" | "fragment:<id>".
type HumanFeedback struct {
	TargetRef string `json:"target_ref"`
	Note      string `json:"note"`
}

// Post — a unit of content a Connector publishes, only via the approval gate
// (contracts §4). Media is empty for v1 (text-only).
type Post struct {
	Channel string   `json:"channel"`
	Text    string   `json:"text"`
	Media   []string `json:"media,omitempty"`
}

// TicketStore — work-state CRUD (contracts §5). Ungated; not versioned.
// status "" = all.
type TicketStore interface {
	Create(ctx context.Context, t Ticket) (id string, err error)
	Update(ctx context.Context, t Ticket) error
	Get(ctx context.Context, id string) (Ticket, error)
	List(ctx context.Context, status TicketStatus) ([]Ticket, error)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TicketStatus|HumanFeedback|TicketStore' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/contracts.go go/orchestrator/contracts_test.go
git commit -m "feat(orchestrator): frozen v1 contract types (Ticket, HumanFeedback, Post, TicketStore)"
```

---

### Task 2: In-memory TicketStore (the dev/test double)

**Files:**
- Create: `go/orchestrator/memticket.go`
- Test: `go/orchestrator/memticket_test.go`

**Interfaces:**
- Consumes: `TicketStore`, `Ticket`, `TicketStatus` (Task 1).
- Produces: `func NewMemTicketStore() *MemTicketStore` implementing `TicketStore`. Ids are a deterministic counter `t1`,`t2`,… `Create` stamps `CreatedAt/UpdatedAt` from an injectable clock (default: a monotonic counter, NOT wall time, so tests stay deterministic). `List("")` returns all in creation order; `List(status)` filters. `Update` on an unknown id errors; `Get` on unknown id errors.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"
)

func TestMemTicketStoreCRUDAndListFilter(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTicketStore()

	id1, err := ts.Create(ctx, Ticket{Title: "draft post", Status: StatusNeedsHuman})
	if err != nil || id1 != "t1" {
		t.Fatalf("create t1: id=%q err=%v", id1, err)
	}
	id2, _ := ts.Create(ctx, Ticket{Title: "verify", Status: StatusInReview})
	if id2 != "t2" {
		t.Fatalf("create t2: id=%q", id2)
	}

	got, err := ts.Get(ctx, "t1")
	if err != nil || got.Title != "draft post" || got.CreatedAt == 0 {
		t.Fatalf("get t1: %+v err=%v", got, err)
	}

	needs, _ := ts.List(ctx, StatusNeedsHuman)
	if len(needs) != 1 || needs[0].ID != "t1" {
		t.Fatalf("needs_human filter: %+v", needs)
	}
	all, _ := ts.List(ctx, "")
	if len(all) != 2 || all[0].ID != "t1" || all[1].ID != "t2" {
		t.Fatalf("list all order: %+v", all)
	}

	got.Status = StatusDone
	if err := ts.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	back, _ := ts.Get(ctx, "t1")
	if back.Status != StatusDone || back.UpdatedAt < back.CreatedAt {
		t.Fatalf("update not applied: %+v", back)
	}

	if _, err := ts.Get(ctx, "nope"); err == nil {
		t.Fatalf("expected error on unknown get")
	}
	if err := ts.Update(ctx, Ticket{ID: "nope"}); err == nil {
		t.Fatalf("expected error on unknown update")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestMemTicketStoreCRUD -v`
Expected: FAIL — `undefined: NewMemTicketStore`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"fmt"
	"sync"
)

// MemTicketStore is an in-memory TicketStore: the Slice-0 style test double and
// dev impl behind the frozen TicketStore seam (Postgres impl = Slice A). Ids are
// a deterministic counter (t1, t2, …) and timestamps a monotonic counter, so the
// watch surface renders reproducibly offline.
type MemTicketStore struct {
	mu     sync.Mutex
	order  []string
	byID   map[string]Ticket
	seq    int
	clock  int64
}

// NewMemTicketStore returns an empty store.
func NewMemTicketStore() *MemTicketStore {
	return &MemTicketStore{byID: map[string]Ticket{}}
}

func (s *MemTicketStore) tick() int64 { s.clock++; return s.clock }

func (s *MemTicketStore) Create(_ context.Context, t Ticket) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	t.ID = fmt.Sprintf("t%d", s.seq)
	now := s.tick()
	t.CreatedAt, t.UpdatedAt = now, now
	s.byID[t.ID] = t
	s.order = append(s.order, t.ID)
	return t.ID, nil
}

func (s *MemTicketStore) Update(_ context.Context, t Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[t.ID]; !ok {
		return fmt.Errorf("memticket: unknown ticket %q", t.ID)
	}
	t.UpdatedAt = s.tick()
	s.byID[t.ID] = t
	return nil
}

func (s *MemTicketStore) Get(_ context.Context, id string) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return Ticket{}, fmt.Errorf("memticket: unknown ticket %q", id)
	}
	return t, nil
}

func (s *MemTicketStore) List(_ context.Context, status TicketStatus) ([]Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Ticket{}
	for _, id := range s.order {
		t := s.byID[id]
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestMemTicketStoreCRUD -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/memticket.go go/orchestrator/memticket_test.go
git commit -m "feat(orchestrator): in-memory TicketStore (deterministic dev/test double)"
```

---

### Task 3: Board revision timeline — `MemBoard.Revisions()` + the `RevisionLister` port

**Files:**
- Modify: `go/orchestrator/memboard.go`
- Test: `go/orchestrator/memboard_revisions_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardRevision`, the existing `MemBoard`.
- Produces: `func (m *MemBoard) Revisions(ctx context.Context) ([]agentdb.BoardRevision, error)` — returns the full applied log in ascending `Seq` order (a copy). This is the timeline source for `GET /api/board/revisions`.
- **Contract gap (see final section):** the frozen `BoardStore` (§5) has only `Current/AsOf/Head/Append` — **no way to list revisions**, which `/api/board/revisions` requires. Slice E introduces the narrow `RevisionLister` port (declared in Task 4) rather than editing the frozen `BoardStore`; `MemBoard` satisfies it, and the Postgres board (Slice A) must too.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestMemBoardRevisionsInOrder(t *testing.T) {
	ctx := context.Background()
	b := NewMemBoard()
	_, _ = b.Append(ctx, SeedFragment("routing-guidance", "Be basic."))
	_, _ = b.Append(ctx, agentdb.Changeset{Author: "human-feedback", Message: "be clever",
		Ops: []agentdb.Op{fragOp(agentdb.OpUpdate, "routing-guidance", "Be clever.")}})

	revs, err := b.Revisions(ctx)
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if len(revs) != 2 || revs[0].ID != "r1" || revs[1].ID != "r2" {
		t.Fatalf("order wrong: %+v", revs)
	}
	if revs[1].Author != "human-feedback" || revs[1].Message != "be clever" {
		t.Fatalf("author/message not preserved: %+v", revs[1])
	}
	if revs[0].Seq != 1 || revs[1].Seq != 2 {
		t.Fatalf("seq wrong: %+v", revs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestMemBoardRevisionsInOrder -v`
Expected: FAIL — `b.Revisions undefined`.

- [ ] **Step 3: Write minimal implementation**

Add to `go/orchestrator/memboard.go`:

```go
// Revisions returns the applied changeset log in ascending Seq order (a copy).
// It is the timeline substrate for the watch surface's story view. The frozen
// BoardStore has no list method; watchapi consumes this via its RevisionLister
// port, which the Postgres board must also satisfy.
func (m *MemBoard) Revisions(_ context.Context) ([]agentdb.BoardRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]agentdb.BoardRevision, len(m.revs))
	copy(out, m.revs)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}
```

(`sort` is already imported by `memboard.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestMemBoardRevisionsInOrder -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/memboard.go go/orchestrator/memboard_revisions_test.go
git commit -m "feat(orchestrator): MemBoard.Revisions — the board-revision timeline source"
```

---

### Task 4: watchapi skeleton — Config, ports, New, Mux, auth, writeJSON

**Files:**
- Create: `go/orchestrator/watchapi/watchapi.go`
- Test: `go/orchestrator/watchapi/watchapi_test.go`
- Create: `go/orchestrator/watchapi/fakes_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardStore`, `agentdb.BoardRevision`, `orchestrator.TicketStore`, `orchestrator.Run`, `orchestrator.HumanFeedback`.
- Produces: the ports `RevisionLister`, `TelemetryReader`, `Approver`, `FeedbackApplier`, `Triggerer`; `type Config struct{...}`; `type Handlers struct{...}`; `func New(Config) (*Handlers, error)` (guards required deps); `func (*Handlers) Mux() *http.ServeMux` registering the eight §8 routes (handlers stubbed to `501` until their task); an `auth` middleware enforcing a shared bearer token when `AuthToken != ""`; `writeJSON`/`writeErr` helpers.
- `fakes_test.go` provides recording fakes: `fakeApprover`, `fakeFeedback`, `fakeTrigger`, plus a helper `newTestHandlers(t)` wiring `MemBoard`+`MemTicketStore`+`*orchestrator.Telemetry`+the fakes.

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRequiresPorts(t *testing.T) {
	full := newTestConfig()
	if _, err := New(full); err != nil {
		t.Fatalf("valid config errored: %v", err)
	}
	// each required port missing → error
	missingBoard := full
	missingBoard.Board = nil
	if _, err := New(missingBoard); err == nil {
		t.Fatalf("expected error when Board nil")
	}
	missingTickets := full
	missingTickets.Tickets = nil
	if _, err := New(missingTickets); err == nil {
		t.Fatalf("expected error when Tickets nil")
	}
}

func TestAuthGuard(t *testing.T) {
	cfg := newTestConfig()
	cfg.AuthToken = "secret"
	h, _ := New(cfg)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	// no token → 401
	resp, _ := http.Get(srv.URL + "/api/runs")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: got %d want 401", resp.StatusCode)
	}
	// wrong token → 401
	req, _ := http.NewRequest("GET", srv.URL+"/api/runs", nil)
	req.Header.Set("Authorization", "Bearer nope")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d want 401", resp.StatusCode)
	}
	// right token → 200
	req, _ = http.NewRequest("GET", srv.URL+"/api/runs", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("right token: got %d want 200", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Write the fakes helper (`fakes_test.go`)**

```go
package watchapi

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

type fakeApprover struct {
	calls []string
	ref   string
	err   error
}

func (f *fakeApprover) Approve(_ context.Context, id string) (string, error) {
	f.calls = append(f.calls, id)
	return f.ref, f.err
}

type fakeFeedback struct {
	got []orchestrator.HumanFeedback
	rev string
	err error
}

func (f *fakeFeedback) Apply(_ context.Context, fb orchestrator.HumanFeedback) (string, error) {
	f.got = append(f.got, fb)
	return f.rev, f.err
}

type fakeTrigger struct {
	n   int
	err error
}

func (f *fakeTrigger) Trigger(context.Context) error { f.n++; return f.err }

// newTestConfig wires a fully in-memory, deterministic Config.
func newTestConfig() Config {
	board := orchestrator.NewMemBoard()
	return Config{
		Board:     board,
		Revisions: board,
		Tickets:   orchestrator.NewMemTicketStore(),
		Telemetry: orchestrator.NewTelemetry(),
		Approver:  &fakeApprover{ref: "at://did/post/1"},
		Feedback:  &fakeFeedback{rev: "r2"},
		Trigger:   &fakeTrigger{},
	}
}

// newTestHandlers returns wired Handlers plus the concrete fakes/stores for
// assertions.
type testDeps struct {
	cfg      Config
	board    *orchestrator.MemBoard
	tickets  *orchestrator.MemTicketStore
	tel      *orchestrator.Telemetry
	approver *fakeApprover
	feedback *fakeFeedback
	trigger  *fakeTrigger
}

func newTestHandlers(t *testing.T) (*Handlers, testDeps) {
	t.Helper()
	board := orchestrator.NewMemBoard()
	tickets := orchestrator.NewMemTicketStore()
	tel := orchestrator.NewTelemetry()
	ap := &fakeApprover{ref: "at://did/post/1"}
	fb := &fakeFeedback{rev: "r2"}
	tr := &fakeTrigger{}
	cfg := Config{Board: board, Revisions: board, Tickets: tickets, Telemetry: tel,
		Approver: ap, Feedback: fb, Trigger: tr}
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, testDeps{cfg, board, tickets, tel, ap, fb, tr}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/watchapi/ -run 'TestNewRequires|TestAuthGuard' -v`
Expected: FAIL — `undefined: New` / `undefined: Config`.

- [ ] **Step 4: Write minimal implementation**

```go
// Package watchapi is the Slice-E watch/approve/note HTTP surface (contracts §8).
// The web client is a thin consumer of these routes; the API is the contract.
// Handlers depend only on the ports below, so the surface is testable with
// in-memory fakes and needs no Postgres board, manager loop, or real connector.
package watchapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// RevisionLister lists the board changeset log for the story timeline. The frozen
// BoardStore lacks this; MemBoard and the Postgres board both satisfy it.
type RevisionLister interface {
	Revisions(ctx context.Context) ([]agentdb.BoardRevision, error)
}

// TelemetryReader is the read side of the run log (contracts §5 Telemetry.Runs).
type TelemetryReader interface {
	Runs() []orchestrator.Run
}

// Approver runs the Slice-D approval→publish flow for one Needs-Human ticket and
// returns the published ref. Slice E calls THROUGH this port and never touches a
// Connector — the publish-approval floor (contracts §7.3) stays in mechanism.
type Approver interface {
	Approve(ctx context.Context, ticketID string) (ref string, err error)
}

// FeedbackApplier turns a HumanFeedback into a write_fragment board revision
// (contracts §6 policy path). Slice E forwards notes; the ref→fragment mapping is
// the applier's concern (Slice C).
type FeedbackApplier interface {
	Apply(ctx context.Context, fb orchestrator.HumanFeedback) (revisionID string, err error)
}

// Triggerer fires one ManagerExchange now (else cron drives it).
type Triggerer interface {
	Trigger(ctx context.Context) error
}

// Config wires the ports. AuthToken "" disables the guard (local dev only).
type Config struct {
	Board     agentdb.BoardStore
	Revisions RevisionLister
	Tickets   orchestrator.TicketStore
	Telemetry TelemetryReader
	Approver  Approver
	Feedback  FeedbackApplier
	Trigger   Triggerer
	AuthToken string
}

// Handlers is the mountable handler set.
type Handlers struct {
	cfg Config
}

// New validates required ports and returns the handler set.
func New(cfg Config) (*Handlers, error) {
	switch {
	case cfg.Board == nil:
		return nil, errors.New("watchapi: Board is required")
	case cfg.Revisions == nil:
		return nil, errors.New("watchapi: Revisions is required")
	case cfg.Tickets == nil:
		return nil, errors.New("watchapi: Tickets is required")
	case cfg.Telemetry == nil:
		return nil, errors.New("watchapi: Telemetry is required")
	case cfg.Approver == nil:
		return nil, errors.New("watchapi: Approver is required")
	case cfg.Feedback == nil:
		return nil, errors.New("watchapi: Feedback is required")
	case cfg.Trigger == nil:
		return nil, errors.New("watchapi: Trigger is required")
	}
	return &Handlers{cfg: cfg}, nil
}

// Mux registers the eight §8 routes (go1.22 method+path patterns) behind auth.
func (h *Handlers) Mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/tickets", h.ListTickets)
	m.HandleFunc("POST /api/tickets/{id}/approve", h.Approve)
	m.HandleFunc("POST /api/tickets/{id}/reject", h.Reject)
	m.HandleFunc("POST /api/feedback", h.Feedback)
	m.HandleFunc("GET /api/board/revisions", h.Revisions)
	m.HandleFunc("GET /api/board/current", h.Current)
	m.HandleFunc("GET /api/runs", h.Runs)
	m.HandleFunc("POST /api/trigger", h.Trigger)
	// wrap every route in the shared-token guard
	guarded := http.NewServeMux()
	guarded.Handle("/", h.auth(m))
	return guarded
}

// auth enforces a single shared bearer token when configured.
func (h *Handlers) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.AuthToken != "" && r.Header.Get("Authorization") != "Bearer "+h.cfg.AuthToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

// --- handlers stubbed until their task ---
func (h *Handlers) ListTickets(w http.ResponseWriter, r *http.Request) { writeErr(w, 501, "not implemented") }
func (h *Handlers) Approve(w http.ResponseWriter, r *http.Request)     { writeErr(w, 501, "not implemented") }
func (h *Handlers) Reject(w http.ResponseWriter, r *http.Request)      { writeErr(w, 501, "not implemented") }
func (h *Handlers) Feedback(w http.ResponseWriter, r *http.Request)    { writeErr(w, 501, "not implemented") }
func (h *Handlers) Revisions(w http.ResponseWriter, r *http.Request)   { writeErr(w, 501, "not implemented") }
func (h *Handlers) Current(w http.ResponseWriter, r *http.Request)     { writeErr(w, 501, "not implemented") }
func (h *Handlers) Runs(w http.ResponseWriter, r *http.Request)        { writeErr(w, 501, "not implemented") }
func (h *Handlers) Trigger(w http.ResponseWriter, r *http.Request)     { writeErr(w, 501, "not implemented") }
```

Note: `GET /api/runs` returns `501` here but the auth test only asserts the status code differs (401 vs not-401). Adjust the auth test to assert `!= 401` if you prefer; the sample asserts `200`, so land Task 10 (`Runs`) before re-running `TestAuthGuard`, OR temporarily point the guard test at a route you implement first. Simplest: keep the auth test asserting the **401 vs non-401** distinction (change the "right token" assertion to `resp.StatusCode == http.StatusUnauthorized → fail`). Pick one and keep it green.

- [ ] **Step 5: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/watchapi/ -run 'TestNewRequires|TestAuthGuard' -v`
Expected: PASS (with the auth-test assertion resolved per the note).

- [ ] **Step 6: Commit**

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): package skeleton — ports, Config, Mux, shared-token auth"
```

---

### Task 5: `GET /api/tickets?status=` — list needs-human tickets

**Files:**
- Modify: `go/orchestrator/watchapi/tickets.go` (create), replacing the stub in `watchapi.go`
- Test: `go/orchestrator/watchapi/tickets_test.go`

**Interfaces:**
- Consumes: `orchestrator.TicketStore`, `orchestrator.Ticket`, `orchestrator.TicketStatus`.
- Produces: `func (h *Handlers) ListTickets(w, r)` — reads `?status=` (default `""` = all, per §5/§8), calls `Tickets.List`, writes `[]Ticket` JSON. `500` on store error. Move `ListTickets` out of `watchapi.go` into `tickets.go` (delete the stub line).

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestListTicketsFiltersNeedsHuman(t *testing.T) {
	h, d := newTestHandlers(t)
	ctx := context.Background()
	_, _ = d.tickets.Create(ctx, orchestrator.Ticket{Title: "draft", Status: orchestrator.StatusNeedsHuman})
	_, _ = d.tickets.Create(ctx, orchestrator.Ticket{Title: "wip", Status: orchestrator.StatusInProgress})

	req := httptest.NewRequest("GET", "/api/tickets?status=needs_human", nil)
	rec := httptest.NewRecorder()
	h.ListTickets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var got []orchestrator.Ticket
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Status != orchestrator.StatusNeedsHuman {
		t.Fatalf("filter wrong: %+v", got)
	}

	// no status → all
	rec = httptest.NewRecorder()
	h.ListTickets(rec, httptest.NewRequest("GET", "/api/tickets", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Fatalf("all wrong: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestListTickets -v`
Expected: FAIL — `501`, body mismatch.

- [ ] **Step 3: Write minimal implementation**

Delete the `ListTickets` stub in `watchapi.go` and add `tickets.go`:

```go
package watchapi

import (
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// ListTickets serves GET /api/tickets?status= (default all). status="needs_human"
// yields the pending approvals + escalations the operator acts on.
func (h *Handlers) ListTickets(w http.ResponseWriter, r *http.Request) {
	status := orchestrator.TicketStatus(r.URL.Query().Get("status"))
	tickets, err := h.cfg.Tickets.List(r.Context(), status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tickets)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestListTickets -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): GET /api/tickets — list by status (needs_human = the desk)"
```

---

### Task 6: `POST /api/tickets/{id}/approve` — approve → publish flow

**Files:**
- Create: `go/orchestrator/watchapi/approve.go` (delete the `Approve` stub)
- Test: `go/orchestrator/watchapi/approve_test.go`

**Interfaces:**
- Consumes: `Approver` port, `r.PathValue("id")`.
- Produces: `func (h *Handlers) Approve(w, r)` — reads the path id, calls `Approver.Approve(ctx, id)`, writes `{"ref": <ref>}` on success. `502` (bad gateway) if the publish flow errors — the human should see the channel refused, not a generic 500. The handler NEVER touches a `Connector`; the gate lives entirely in the injected `Approver` (Slice D). An empty id → `400`.

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doPath(h *Handlers, method, target string) *httptest.ResponseRecorder {
	// route through the Mux so {id} PathValue is populated
	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestApproveCallsApprover(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := doPath(h, "POST", "/api/tickets/t1/approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var out struct{ Ref string `json:"ref"` }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Ref != "at://did/post/1" {
		t.Fatalf("ref = %q", out.Ref)
	}
	if len(d.approver.calls) != 1 || d.approver.calls[0] != "t1" {
		t.Fatalf("approver not called with t1: %+v", d.approver.calls)
	}
}

func TestApproveSurfacesPublishError(t *testing.T) {
	h, d := newTestHandlers(t)
	d.approver.err = errors.New("channel refused")
	rec := doPath(h, "POST", "/api/tickets/t1/approve")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d want 502", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestApprove -v`
Expected: FAIL — `501`.

- [ ] **Step 3: Write minimal implementation**

```go
package watchapi

import "net/http"

// Approve serves POST /api/tickets/{id}/approve: the single human click that lets
// a drafted post reach the world. It calls the injected Approver (the Slice-D
// approval→publish flow) and returns the published ref. It never touches a
// Connector directly — that is the un-bypassable publish-approval floor.
func (h *Handlers) Approve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing ticket id")
		return
	}
	ref, err := h.cfg.Approver.Approve(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ref": ref})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestApprove -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): POST approve — through the injected publish gate (never a Connector)"
```

---

### Task 7: `POST /api/tickets/{id}/reject {note?}` — reject + optional note

**Files:**
- Create: `go/orchestrator/watchapi/reject.go` (delete the `Reject` stub)
- Test: `go/orchestrator/watchapi/reject_test.go`

**Interfaces:**
- Consumes: `orchestrator.TicketStore`, `FeedbackApplier`, `orchestrator.HumanFeedback`.
- Produces: `func (h *Handlers) Reject(w, r)` — decodes optional `{"note":...}`; `Get`s the ticket (`404` if unknown); clears `PendingPost` and sets `Status = StatusTodo` (re-plan on the next tick — see Contract gap: reject's target lane is unspecified); `Update`s it; if a note is present, calls `Feedback.Apply(HumanFeedback{TargetRef:"ticket:"+id, Note:note})` so the rejection teaches. Writes `{"status":"rejected","revision":<rev-or-empty>}`.
- **Contract gap:** §8 says "rejects; optional note becomes HumanFeedback" but does not specify the resulting lane. This plan chooses `StatusTodo` (re-plan) + clear `PendingPost`; flagged for the author.

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestRejectWithNoteAppliesFeedbackAndReplans(t *testing.T) {
	h, d := newTestHandlers(t)
	ctx := context.Background()
	id, _ := d.tickets.Create(ctx, orchestrator.Ticket{
		Title: "draft", Status: orchestrator.StatusNeedsHuman,
		PendingPost: json.RawMessage(`{"channel":"bluesky","text":"meh"}`),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/"+id+"/reject", strings.NewReader(`{"note":"too boring, be witty"}`))
	req.SetPathValue("id", id)
	h.Reject(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}

	got, _ := d.tickets.Get(ctx, id)
	if got.Status != orchestrator.StatusTodo || len(got.PendingPost) != 0 {
		t.Fatalf("ticket not re-planned/cleared: %+v", got)
	}
	if len(d.feedback.got) != 1 || d.feedback.got[0].TargetRef != "ticket:"+id ||
		!strings.Contains(d.feedback.got[0].Note, "witty") {
		t.Fatalf("feedback not applied: %+v", d.feedback.got)
	}
}

func TestRejectWithoutNoteSkipsFeedback(t *testing.T) {
	h, d := newTestHandlers(t)
	ctx := context.Background()
	id, _ := d.tickets.Create(ctx, orchestrator.Ticket{Status: orchestrator.StatusNeedsHuman})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/"+id+"/reject", strings.NewReader(`{}`))
	req.SetPathValue("id", id)
	h.Reject(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(d.feedback.got) != 0 {
		t.Fatalf("feedback should be skipped with no note")
	}
}

func TestRejectUnknownTicket404(t *testing.T) {
	h, _ := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tickets/nope/reject", strings.NewReader(`{}`))
	req.SetPathValue("id", "nope")
	h.Reject(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d want 404", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestReject -v`
Expected: FAIL — `501`.

- [ ] **Step 3: Write minimal implementation**

```go
package watchapi

import (
	"encoding/json"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

type rejectBody struct {
	Note string `json:"note"`
}

// Reject serves POST /api/tickets/{id}/reject: discards a drafted post and,
// if a note is given, feeds it back as a HumanFeedback (the teacher's desk).
// The ticket returns to Todo (re-plan on the next tick) with its PendingPost
// cleared — nothing was published.
func (h *Handlers) Reject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing ticket id")
		return
	}
	var body rejectBody
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // empty/absent body = no note
	}
	t, err := h.cfg.Tickets.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	t.PendingPost = nil
	t.Status = orchestrator.StatusTodo
	if err := h.cfg.Tickets.Update(r.Context(), t); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var revision string
	if body.Note != "" {
		rev, err := h.cfg.Feedback.Apply(r.Context(), orchestrator.HumanFeedback{
			TargetRef: "ticket:" + id, Note: body.Note,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		revision = rev
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected", "revision": revision})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestReject -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): POST reject — clear draft, re-plan, optional teaching note"
```

---

### Task 8: `POST /api/feedback {target_ref, note}` — the learning loop

**Files:**
- Create: `go/orchestrator/watchapi/feedback.go` (delete the `Feedback` stub)
- Test: `go/orchestrator/watchapi/feedback_test.go`

**Interfaces:**
- Consumes: `FeedbackApplier`, `orchestrator.HumanFeedback`.
- Produces: `func (h *Handlers) Feedback(w, r)` — decodes `{target_ref, note}` (both required → `400` if missing), calls `Feedback.Apply`, writes `{"revision": <id>}`. This is the direct teacher's-desk route (the reject-note is the same call from a different trigger).

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeedbackAppliesAndReturnsRevision(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := httptest.NewRecorder()
	body := `{"target_ref":"fragment:routing-guidance","note":"be more clever"}`
	h.Feedback(rec, httptest.NewRequest("POST", "/api/feedback", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var out struct{ Revision string `json:"revision"` }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Revision != "r2" {
		t.Fatalf("revision = %q", out.Revision)
	}
	if len(d.feedback.got) != 1 || d.feedback.got[0].TargetRef != "fragment:routing-guidance" {
		t.Fatalf("feedback not forwarded: %+v", d.feedback.got)
	}
}

func TestFeedbackRejectsMissingFields(t *testing.T) {
	h, _ := newTestHandlers(t)
	for _, body := range []string{`{"note":"x"}`, `{"target_ref":"fragment:r"}`, `{}`} {
		rec := httptest.NewRecorder()
		h.Feedback(rec, httptest.NewRequest("POST", "/api/feedback", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d want 400", body, rec.Code)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestFeedback -v`
Expected: FAIL — `501`.

- [ ] **Step 3: Write minimal implementation**

```go
package watchapi

import (
	"encoding/json"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// Feedback serves POST /api/feedback: a targeted note that becomes a
// write_fragment board revision (the learning loop). target_ref is
// "ticket:<id>" | "run:<id>" | "fragment:<id>"; both fields are required.
func (h *Handlers) Feedback(w http.ResponseWriter, r *http.Request) {
	var fb orchestrator.HumanFeedback
	if err := json.NewDecoder(r.Body).Decode(&fb); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if fb.TargetRef == "" || fb.Note == "" {
		writeErr(w, http.StatusBadRequest, "target_ref and note are required")
		return
	}
	rev, err := h.cfg.Feedback.Apply(r.Context(), fb)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"revision": rev})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestFeedback -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): POST feedback — targeted note → write_fragment (the learning loop)"
```

---

### Task 9: `GET /api/board/revisions` + `GET /api/board/current` — the story

**Files:**
- Create: `go/orchestrator/watchapi/board.go` (delete the `Revisions`/`Current` stubs)
- Test: `go/orchestrator/watchapi/board_test.go`

**Interfaces:**
- Consumes: `RevisionLister`, `agentdb.BoardStore`, `agentdb.BoardRevision`, `agentdb.Board`.
- Produces: `type RevisionDTO struct{ ID, ParentID, Author, Message string; Seq, CreatedAt int64 }` (the timeline projection: author, message, ts — no ops payload); `type BoardDTO struct{ Revision string; Fragments []FragmentDTO }` with `FragmentDTO{ID, Kind, Body, LastChangedIn}` (projects away the deferred Staff/EventTypes/etc); `func (h *Handlers) Revisions(w, r)` → `[]RevisionDTO`; `func (h *Handlers) Current(w, r)` → `BoardDTO`.

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestRevisionsAndCurrentRenderTheStory(t *testing.T) {
	h, d := newTestHandlers(t)
	_, _ = d.board.Append(nil, orchestrator.SeedFragment("routing-guidance", "Be basic."))
	_, _ = d.board.Append(nil, agentdb.Changeset{Author: "human-feedback", Message: "be clever",
		Ops: orchestrator.SeedFragment("routing-guidance", "Be clever.").Ops})

	// revisions
	rec := httptest.NewRecorder()
	h.Revisions(rec, httptest.NewRequest("GET", "/api/board/revisions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("revisions status %d", rec.Code)
	}
	var revs []RevisionDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &revs)
	if len(revs) != 2 || revs[1].Author != "human-feedback" || revs[1].Message != "be clever" {
		t.Fatalf("revisions wrong: %+v", revs)
	}

	// current
	rec = httptest.NewRecorder()
	h.Current(rec, httptest.NewRequest("GET", "/api/board/current", nil))
	var board BoardDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &board)
	if board.Revision != "r2" || len(board.Fragments) != 1 || board.Fragments[0].Body != "Be clever." {
		t.Fatalf("current wrong: %+v", board)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestRevisionsAndCurrent -v`
Expected: FAIL — `501` / `undefined: RevisionDTO`.

- [ ] **Step 3: Write minimal implementation**

```go
package watchapi

import "net/http"

// RevisionDTO is the timeline projection of a board revision (author, message,
// ts) — the legible "watch it learn" story, without the raw ops payload.
type RevisionDTO struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Seq       int64  `json:"seq"`
	Author    string `json:"author"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"created_at"`
}

// FragmentDTO / BoardDTO project the folded board to the fragments the surface
// renders (deferred Staff/EventTypes/Pipelines are omitted).
type FragmentDTO struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Body          string `json:"body"`
	LastChangedIn string `json:"last_changed_in"`
}
type BoardDTO struct {
	Revision  string        `json:"revision"`
	Fragments []FragmentDTO `json:"fragments"`
}

// Revisions serves GET /api/board/revisions — the story timeline.
func (h *Handlers) Revisions(w http.ResponseWriter, r *http.Request) {
	revs, err := h.cfg.Revisions.Revisions(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]RevisionDTO, 0, len(revs))
	for _, rv := range revs {
		out = append(out, RevisionDTO{
			ID: rv.ID, ParentID: rv.ParentID, Seq: rv.Seq,
			Author: rv.Author, Message: rv.Message, CreatedAt: rv.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// Current serves GET /api/board/current — the folded fragments at head.
func (h *Handlers) Current(w http.ResponseWriter, r *http.Request) {
	board, err := h.cfg.Board.Current(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dto := BoardDTO{Revision: board.Revision, Fragments: make([]FragmentDTO, 0, len(board.Fragments))}
	for _, f := range board.Fragments {
		dto.Fragments = append(dto.Fragments, FragmentDTO{
			ID: f.ID, Kind: f.Kind, Body: f.Body, LastChangedIn: f.LastChangedIn,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestRevisionsAndCurrent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): GET board/revisions + board/current — the legible story"
```

---

### Task 10: `GET /api/runs` — telemetry (show your work)

**Files:**
- Create: `go/orchestrator/watchapi/runs.go` (delete the `Runs` stub)
- Test: `go/orchestrator/watchapi/runs_test.go`

**Interfaces:**
- Consumes: `TelemetryReader`, `orchestrator.Run`.
- Produces: `type RunDTO struct{ ID, Scope, BoardRev, Prompt, Output string; Seq int }` (snake_case JSON; `orchestrator.Run` has no json tags, so project); `func (h *Handlers) Runs(w, r)` → `[]RunDTO` in order.

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestRunsRendersTelemetry(t *testing.T) {
	h, d := newTestHandlers(t)
	d.tel.Record(orchestrator.Run{Scope: "manager", BoardRevision: "r1", Output: "dumb plan"})
	d.tel.Record(orchestrator.Run{Scope: "manager", BoardRevision: "r2", Output: "clever plan"})

	rec := httptest.NewRecorder()
	h.Runs(rec, httptest.NewRequest("GET", "/api/runs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var runs []RunDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &runs)
	if len(runs) != 2 || runs[0].BoardRev != "r1" || runs[1].Output != "clever plan" {
		t.Fatalf("runs wrong: %+v", runs)
	}
	if runs[0].ID != "run1" || runs[0].Seq != 1 {
		t.Fatalf("run identity wrong: %+v", runs[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestRunsRenders -v`
Expected: FAIL — `501` / `undefined: RunDTO`.

- [ ] **Step 3: Write minimal implementation**

```go
package watchapi

import "net/http"

// RunDTO is the JSON projection of a telemetry Run (scope, board_rev, output —
// the "show your work" substrate the story is told from).
type RunDTO struct {
	ID       string `json:"id"`
	Scope    string `json:"scope"`
	BoardRev string `json:"board_rev"`
	Prompt   string `json:"prompt"`
	Output   string `json:"output"`
	Seq      int    `json:"seq"`
}

// Runs serves GET /api/runs — the append-only run log.
func (h *Handlers) Runs(w http.ResponseWriter, r *http.Request) {
	runs := h.cfg.Telemetry.Runs()
	out := make([]RunDTO, 0, len(runs))
	for _, rn := range runs {
		out = append(out, RunDTO{
			ID: rn.ID, Scope: rn.Scope, BoardRev: rn.BoardRevision,
			Prompt: rn.Prompt, Output: rn.Output, Seq: rn.Seq,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestRunsRenders -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): GET /api/runs — telemetry render (show your work)"
```

---

### Task 11: `POST /api/trigger` — fire one ManagerExchange

**Files:**
- Create: `go/orchestrator/watchapi/trigger.go` (delete the `Trigger` stub)
- Test: `go/orchestrator/watchapi/trigger_test.go`

**Interfaces:**
- Consumes: `Triggerer`.
- Produces: `func (h *Handlers) Trigger(w, r)` — calls `Trigger.Trigger(ctx)`, writes `{"triggered": true}` (`202 Accepted`); `500` on error. This lets the operator drive a tick now instead of waiting for cron.

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTriggerFiresExchange(t *testing.T) {
	h, d := newTestHandlers(t)
	rec := httptest.NewRecorder()
	h.Trigger(rec, httptest.NewRequest("POST", "/api/trigger", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d want 202", rec.Code)
	}
	if d.trigger.n != 1 {
		t.Fatalf("trigger not fired: %d", d.trigger.n)
	}
}

func TestTriggerSurfacesError(t *testing.T) {
	h, d := newTestHandlers(t)
	d.trigger.err = errors.New("dispatch halted: spend ceiling")
	rec := httptest.NewRecorder()
	h.Trigger(rec, httptest.NewRequest("POST", "/api/trigger", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d want 500", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestTrigger -v`
Expected: FAIL — `501`.

- [ ] **Step 3: Write minimal implementation**

```go
package watchapi

import "net/http"

// Trigger serves POST /api/trigger: fire one ManagerExchange now (else cron
// drives it). Fire-and-forget from the operator's view: 202 Accepted.
func (h *Handlers) Trigger(w http.ResponseWriter, r *http.Request) {
	if err := h.cfg.Trigger.Trigger(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"triggered": true})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestTrigger -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): POST /api/trigger — drive one manager tick now"
```

---

### Task 12: Full-Mux end-to-end test (the watch story over HTTP)

**Files:**
- Create: `go/orchestrator/watchapi/e2e_test.go`

**Interfaces:**
- Consumes: everything above via `httptest.NewServer(h.Mux())` — no new production code. Asserts the operator loop over real HTTP: a Needs-Human ticket appears in `GET /api/tickets?status=needs_human` → `POST /api/feedback` writes a revision that shows in `GET /api/board/revisions` and `GET /api/board/current` → `POST /api/tickets/{id}/approve` returns a ref → `POST /api/trigger` fires the exchange.

- [ ] **Step 1: Write the test**

```go
package watchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestWatchSurfaceEndToEnd(t *testing.T) {
	h, d := newTestHandlers(t)
	ctx := context.Background()
	_, _ = d.board.Append(ctx, orchestrator.SeedFragment("routing-guidance", "Be basic."))
	id, _ := d.tickets.Create(ctx, orchestrator.Ticket{
		Title: "draft post", Status: orchestrator.StatusNeedsHuman,
		PendingPost: json.RawMessage(`{"channel":"bluesky","text":"hi"}`),
	})

	srv := newAuthServer(t, h) // helper: httptest server, no token
	defer srv.Close()

	// 1. the desk shows the pending approval
	var tickets []orchestrator.Ticket
	getJSON(t, srv, "/api/tickets?status=needs_human", &tickets)
	if len(tickets) != 1 || tickets[0].ID != id {
		t.Fatalf("pending approvals: %+v", tickets)
	}

	// 2. leave a note → a new board revision
	var fbOut struct{ Revision string `json:"revision"` }
	postJSON(t, srv, "/api/feedback", `{"target_ref":"fragment:routing-guidance","note":"be clever"}`, &fbOut)
	if fbOut.Revision == "" {
		t.Fatalf("no revision from feedback")
	}

	// 3. the story timeline reflects it
	var revs []RevisionDTO
	getJSON(t, srv, "/api/board/revisions", &revs)
	if len(revs) < 1 {
		t.Fatalf("empty timeline")
	}

	// 4. approve → publish flow returns a ref
	var apOut struct{ Ref string `json:"ref"` }
	postJSON(t, srv, "/api/tickets/"+id+"/approve", ``, &apOut)
	if apOut.Ref == "" || len(d.approver.calls) != 1 {
		t.Fatalf("approve did not run the gate: %+v", apOut)
	}

	// 5. trigger a tick
	req, _ := http.NewRequest("POST", srv.URL+"/api/trigger", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusAccepted || d.trigger.n != 1 {
		t.Fatalf("trigger failed: %d n=%d", resp.StatusCode, d.trigger.n)
	}
}
```

- [ ] **Step 2: Add the small HTTP test helpers**

Add `newAuthServer`, `getJSON`, `postJSON` to `fakes_test.go` (thin wrappers over `httptest.NewServer` + `http.Get`/`http.Post` + `json.Unmarshal`; fail the test on non-2xx). Keep them ~15 lines total.

- [ ] **Step 3: Run + commit**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestWatchSurfaceEndToEnd -v`
Expected: PASS.

```bash
git add go/orchestrator/watchapi/
git commit -m "test(watchapi): end-to-end operator loop over HTTP (the watch story)"
```

---

### Task 13: Minimal web client (embedded single page — the thin consumer)

**Files:**
- Create: `go/orchestrator/watchapi/webui/index.html`
- Create: `go/orchestrator/watchapi/webui/app.js`
- Create: `go/orchestrator/watchapi/webui.go` (`go:embed` + serve handler)
- Test: `go/orchestrator/watchapi/webui_test.go`

**Interfaces:**
- Consumes: `embed.FS`, the §8 routes (from JS `fetch`).
- Produces: `//go:embed webui/*` `var webFS embed.FS`; register `GET /` on the Mux serving `index.html`, and `GET /app.js` serving the script (both via `http.FileServerFS` or a tiny handler). The page is **vanilla JS, no build step**: it renders (a) pending approvals with **Approve**/**Reject+note** buttons, (b) a **feedback** box (target_ref + note), (c) the **board-revision timeline**, (d) **current fragments**, (e) the **runs** table, and a **Trigger** button. All logic is `fetch(...)` + DOM; **zero business logic** — the API is the contract. Keep `app.js` under ~120 lines. Reuse of the React `web/` app is rejected: it is a session-chat library, not this surface; a single embedded page is far lighter and needs no npm build (this fork has not built `web/`).

- [ ] **Step 1: Write the failing test**

```go
package watchapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebUIServesIndex(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: err=%v code=%d", err, resp.StatusCode)
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "<html") {
		t.Fatalf("index not served: %q", string(buf[:n]))
	}
}
```

- [ ] **Step 2: Write the page + serve handler**

`webui/index.html` (skeleton — sections + a `<script src="/app.js">`):

```html
<!doctype html>
<html>
<head><meta charset="utf-8"><title>Agent Orange — watch</title></head>
<body>
  <h1>Agent Orange — watch / approve / note</h1>
  <button onclick="trigger()">Trigger a tick</button>
  <section><h2>Pending approvals</h2><div id="tickets"></div></section>
  <section><h2>Leave a note</h2>
    <input id="fb-ref" placeholder="fragment:routing-guidance">
    <input id="fb-note" placeholder="be more clever">
    <button onclick="feedback()">Send</button>
  </section>
  <section><h2>The story (board revisions)</h2><div id="revisions"></div></section>
  <section><h2>Current guidance</h2><div id="fragments"></div></section>
  <section><h2>Runs</h2><div id="runs"></div></section>
  <script src="/app.js"></script>
</body>
</html>
```

`webui/app.js` (vanilla fetch; token from a `?token=` query param or empty):

```js
const tok = new URLSearchParams(location.search).get("token") || "";
const H = tok ? { Authorization: "Bearer " + tok } : {};
const j = (p) => fetch(p, { headers: H }).then((r) => r.json());
const post = (p, b) => fetch(p, { method: "POST", headers: { ...H, "Content-Type": "application/json" }, body: b ? JSON.stringify(b) : null });

async function refresh() {
  const tickets = await j("/api/tickets?status=needs_human");
  document.getElementById("tickets").innerHTML = tickets.map((t) =>
    `<div>${t.title} [${t.id}]
       <button onclick="approve('${t.id}')">Approve</button>
       <button onclick="reject('${t.id}')">Reject</button></div>`).join("") || "none";
  const revs = await j("/api/board/revisions");
  document.getElementById("revisions").innerHTML = revs.map((r) => `<div>${r.seq}. ${r.author}: ${r.message}</div>`).join("");
  const board = await j("/api/board/current");
  document.getElementById("fragments").innerHTML = (board.fragments || []).map((f) => `<div><b>${f.id}</b>: ${f.body} <i>(@${f.last_changed_in})</i></div>`).join("");
  const runs = await j("/api/runs");
  document.getElementById("runs").innerHTML = runs.map((r) => `<div>${r.seq}. ${r.scope} @${r.board_rev}: ${r.output}</div>`).join("");
}
async function approve(id) { await post(`/api/tickets/${id}/approve`); refresh(); }
async function reject(id) { const note = prompt("Note (optional):") || ""; await post(`/api/tickets/${id}/reject`, { note }); refresh(); }
async function feedback() { await post("/api/feedback", { target_ref: document.getElementById("fb-ref").value, note: document.getElementById("fb-note").value }); refresh(); }
async function trigger() { await post("/api/trigger"); refresh(); }
refresh();
```

`webui.go` (embed + register on the Mux):

```go
package watchapi

import (
	"embed"
	"net/http"
)

//go:embed webui/index.html webui/app.js
var webFS embed.FS

// serveWeb serves the thin single-page client. Registered on the Mux at GET /
// and GET /app.js. The page is a pure consumer of the §8 API.
func (h *Handlers) serveWeb(w http.ResponseWriter, r *http.Request) {
	path := "webui/index.html"
	if r.URL.Path == "/app.js" {
		path = "webui/app.js"
		w.Header().Set("Content-Type", "application/javascript")
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	b, err := webFS.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(b)
}
```

Then in `Mux()` (Task 4), register the web routes **inside the auth wrapper is optional** — serve the static page unauthenticated (the JS supplies the token for API calls), so add to the inner mux `m`:

```go
	m.HandleFunc("GET /", h.serveWeb)
	m.HandleFunc("GET /app.js", h.serveWeb)
```

(If you prefer the page itself gated, leave it under `auth`; the test uses no token so keep `AuthToken=""` in `newTestHandlers`.)

- [ ] **Step 3: Run + commit**

Run: `cd go && go test ./orchestrator/watchapi/ -run TestWebUIServesIndex -v && go build ./...`
Expected: PASS.

```bash
git add go/orchestrator/watchapi/
git commit -m "feat(watchapi): thin embedded web client (watch/approve/note single page)"
```

---

### Task 14: Runnable demo command (boot the surface over the Slice-0 loop)

**Files:**
- Create: `go/examples/watchsurface/main.go`

**Interfaces:**
- Consumes: `orchestrator` (MemBoard, MemTicketStore, Telemetry, Runner, ApplyFeedback, ScriptedModel, SeedFragment), `watchapi`.
- Produces: a `main` that wires an in-memory `Config` — with tiny inline adapters for the three action ports (`Approver` = record + mark ticket Done; `FeedbackApplier` = a closure over `orchestrator.ApplyFeedback` against the shared board + a `ScriptedModel` reviser; `Triggerer` = a no-op that logs) — seeds a Needs-Human ticket + a `routing-guidance` fragment, and `http.ListenAndServe(":8099", h.Mux())` so a human can open the page, approve, and leave a note. Offline, deterministic, no real publishing (the fake Approver never touches a Connector). Keep it a single readable `main` (~70 lines); this is the human-watchable sibling of the Slice-0 `learningloop` example.

- [ ] **Step 1: Write the implementation**

Sketch (fill in the inline port adapters):

```go
// Command watchsurface boots the Slice-E watch/approve/note surface over an
// in-memory board + tickets so a human can click Approve / leave a note and see
// the story update. Offline, deterministic, nothing publishes.
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
	"github.com/binocarlos/badcode-agent-orange/orchestrator/watchapi"
)

// inline port adapters (fakes for the demo)
type demoApprover struct{ tickets *orchestrator.MemTicketStore }
func (a demoApprover) Approve(ctx context.Context, id string) (string, error) {
	t, err := a.tickets.Get(ctx, id)
	if err != nil { return "", err }
	t.Status = orchestrator.StatusDone
	_ = a.tickets.Update(ctx, t)
	return "demo://published/" + id, nil // no real Connector — the gate is honored
}

type demoFeedback struct{ board *orchestrator.MemBoard }
func (f demoFeedback) Apply(ctx context.Context, fb orchestrator.HumanFeedback) (string, error) {
	reviser := &orchestrator.ScriptedModel{Default: "Be basic.", Rules: []orchestrator.Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."}}}
	// demo maps any target_ref to the routing-guidance fragment
	return orchestrator.ApplyFeedback(ctx, f.board, reviser, "routing-guidance", fb.Note)
}

type demoTrigger struct{}
func (demoTrigger) Trigger(context.Context) error { log.Println("trigger: (no manager loop in demo)"); return nil }

func main() {
	ctx := context.Background()
	board := orchestrator.NewMemBoard()
	_, _ = board.Append(ctx, orchestrator.SeedFragment("routing-guidance", "Be basic."))
	tickets := orchestrator.NewMemTicketStore()
	_, _ = tickets.Create(ctx, orchestrator.Ticket{Title: "draft launch post", Status: orchestrator.StatusNeedsHuman})

	h, err := watchapi.New(watchapi.Config{
		Board: board, Revisions: board, Tickets: tickets,
		Telemetry: orchestrator.NewTelemetry(),
		Approver:  demoApprover{tickets},
		Feedback:  demoFeedback{board},
		Trigger:   demoTrigger{},
	})
	if err != nil { log.Fatal(err) }
	log.Println("watch surface on http://localhost:8099")
	log.Fatal(http.ListenAndServe(":8099", h.Mux()))
}
```

- [ ] **Step 2: Verify build + vet + the whole suite**

Run: `cd go && go build ./... && go vet ./orchestrator/... && go test ./orchestrator/...`
Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add go/examples/watchsurface/main.go
git commit -m "feat(examples): watchsurface — boot the watch/approve/note surface (offline demo)"
```

---

## Self-Review notes

- **Spec coverage (contracts §8, exact):** `GET /api/tickets?status=` (Task 5) ✓; `POST /api/tickets/{id}/approve` → injected publish flow (Task 6) ✓; `POST /api/tickets/{id}/reject {note?}` (Task 7) ✓; `POST /api/feedback {target_ref, note}` → write_fragment port (Task 8) ✓; `GET /api/board/revisions` + `GET /api/board/current` (Task 9) ✓; `GET /api/runs` (Task 10) ✓; `POST /api/trigger` (Task 11) ✓; shared-token auth (Task 4) ✓; thin web client (Task 13) ✓. **Ownership honored (§9):** Slice E produces the HTTP API + web + feedback wiring and consumes read types + BoardStore/TicketStore/Telemetry/HumanFeedback via ports — it never implements the Postgres board (A), the model (B), the manager loop (C), or the Connector (D).
- **Publish-approval floor (§7.3):** the surface reaches publishing ONLY through the injected `Approver` port; there is no `Connector` import anywhere in `watchapi`. A worker still cannot publish; the gate stays in mechanism.
- **Testability without A/C/D:** every handler is exercised with `net/http/httptest` + `MemBoard`/`MemTicketStore`/`*orchestrator.Telemetry` + recording fakes for the three action ports — deterministic, offline, no DB/network/model.
- **Type consistency:** consumes `orchestrator.Ticket/TicketStatus/HumanFeedback/Post/TicketStore/Run` and `agentdb.Board/BoardRevision/BoardStore` verbatim; response DTOs are thin JSON projections (snake_case) that never redefine a contract type, only reshape it for the wire.
- **Placeholder scan:** no `TODO`/stub survives past its task — Task 4 lands `501` stubs that Tasks 5–11 each replace with a real handler; the auth-test note (401-vs-non-401) must be resolved when landing Task 4.

## Contract gaps found

1. **`BoardStore` cannot list revisions.** `GET /api/board/revisions` needs the changeset log (author, message, ts, seq), but the frozen `BoardStore` (§5) exposes only `Current/AsOf/Head/Append`. Slice E introduces a narrow `RevisionLister` port (satisfied by `MemBoard.Revisions`) rather than editing the frozen interface. **The Postgres board (Slice A) must also satisfy `RevisionLister`, or a `Revisions()`/`ListRevisions()` method should be added to the frozen `BoardStore`.** Escalate the choice.
2. **No slice owns the *Go declaration* of the frozen §3/§4 types.** §9 assigns impls, but `Ticket`, `TicketStatus`, `HumanFeedback`, `Post`, and the `TicketStore` interface must be *declared* somewhere both Slice A and Slice E import. This plan declares them in `go/orchestrator/contracts.go` (Task 1); if Slice A lands them first, Slice E deletes its copy and imports theirs. **Assign an owner for the shared type declarations** (recommend: whichever of A/E lands first, in `orchestrator`).
3. **`Revision` response type is unspecified.** §8 says `GET /api/board/revisions → []Revision (author, message, ts)`, but no `Revision` type exists — only `agentdb.BoardRevision`. Slice E returns a `RevisionDTO` projection. Confirm the wire field names (`id, parent_id, seq, author, message, created_at`).
4. **Reject's resulting lane is unspecified.** §8 says reject "rejects; optional note becomes HumanFeedback" but not what status the ticket lands in. This plan chooses `StatusTodo` + clear `PendingPost` (re-plan on the next tick). **Confirm** — the alternative is `StatusDone` (drop the work entirely) or a new `rejected`/`cancelled` lane (which would be a §3 change → escalate).
5. **`feedback`/reject `target_ref` → fragment mapping is out of Slice E.** §6 defines `write_fragment(id, body)`, but a `target_ref` of `ticket:<id>` or `run:<id>` must be resolved to a *fragment id* to edit. Slice E forwards the raw `HumanFeedback` to the `FeedbackApplier` port and leaves the mapping to the applier (Slice C, over `orchestrator.ApplyFeedback`). **Confirm Slice C owns the ref→fragment resolution** (e.g. ticket/run notes edit the `routing` fragment).
6. **Auth mechanism unspecified beyond "single shared token."** This plan uses `Authorization: Bearer <token>`. Confirm the header/scheme (vs. a `?token=` query param, which the web client also supports for convenience).
