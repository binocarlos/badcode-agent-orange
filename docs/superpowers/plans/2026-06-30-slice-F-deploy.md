# Slice F — Deploy (agentkit DinD `WorkerRuntime` + run live) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Take the v1 orchestrator (Slices A–E, developed against trivial seam impls) and make it run **live** for BadCode marketing on the author's **existing agentkit Docker-in-Docker + GKE** setup — by implementing the production `WorkerRuntime` that adapts the existing agentkit `Runner`/`execenv` to the frozen `orchestrator.WorkerRuntime` seam, wiring config + secrets, packaging a single-box compose (orchestrator + Postgres + web), enforcing the **fail-loud resume** floor (contracts §7 #4; execution-coordination-model §6 REVERSAL of agentkit's best-effort rehydrate), and shipping a runbook to run BadCode marketing behind the approval gate. **This slice adapts known-good infra — it does not rebuild it.**

**Architecture:** The in-proc `WorkerRuntime` (Slice C) and this DinD impl satisfy the **same** `orchestrator.WorkerRuntime` interface (`Spawn(ctx, Scope) (sessionID, error)`, delivering a `Result` to a `ResultSink`). Swapping dev↔prod is therefore **config, not code** — a factory keyed on one env var (`WORKER_RUNTIME=inproc|dind`). The DinD impl is a thin adapter package `go/orchestrator/dindworker` that: (1) resolves a `Scope` (compose fragments against the pinned board) into one prompt; (2) enforces the recursion/spend **floors** in mechanism *before* provisioning; (3) drives the existing `agentkit.Runner` (`CreateSession` → one-turn `SendMessage`) fire-and-forget in a goroutine; (4) decodes the SSE envelope stream into an `orchestrator.Result`; (5) `Charge`s the `SpendMeter` and `Deliver`s the Result to the sink. The fail-loud floor is a small **reversal inside `go/runner.go`** (`rehydrateConversation` best-effort → typed error) that the adapter maps to a Needs-Human ticket via a `ResultFailed`.

**Tech Stack:** Go 1.25. Adapter reuses the existing engine packages (`agentkit`, `execenv`, `imageregistry`, `agentdb`, `events`) and the Slice A–E `orchestrator` package. New external deps: none beyond what the engine already pulls (Docker SDK, gorm/Postgres). Ops: `docker compose` on a single box; Google Secret Manager for the two secrets (reuse the ADC seam already wired in `cmd/agentd/backends.go`).

## Global Constraints

- Module path `github.com/binocarlos/badcode-agent-orange`; new adapter under `go/orchestrator/dindworker/`. One line.
- `go build ./...` and `go vet ./...` must stay green; add tests with every change. One line.
- **Liftability invariant:** `go/runner.go` and every engine package must import **nothing** from `go/orchestrator/` — the fail-loud change is a typed error in `agentkit`, mapped to a ticket *by the orchestrator side*. One line.
- **The in-proc (Slice C) and DinD (this slice) `WorkerRuntime` impls satisfy the SAME `orchestrator.WorkerRuntime` interface** — swapping is a config flag (`WORKER_RUNTIME`), never a code change to the manager loop. One line.
- **Consume the frozen contracts verbatim** (contracts §4/§5/§7): `orchestrator.Scope`, `orchestrator.Result`, `orchestrator.ResultStatus`, `orchestrator.Budget`, `orchestrator.WorkerRuntime`, `orchestrator.ResultSink`, `orchestrator.SpendMeter`, `orchestrator.TicketStore`, `orchestrator.ModelTier`. Never redefine them. One line.
- **The floors are enforced in mechanism** (contracts §7): depth/spawn/tree-token caps and the spend ceiling gate `Spawn` *before* a container is provisioned; a worker can NEVER publish (the publish gate is Slice D and is untouched here). One line.
- **Fail-loud resume** (contracts §7 #4; exec-model §6): a session whose non-empty conversation cannot be restored must fail to a Needs-Human ticket, never resume amnesiac. One line.
- **Secrets never touch the board/fragments** (deployment-plan risks): `ANTHROPIC_API_KEY` + the channel token flow via env/Secret Manager into `Policy.SessionEnv`, never into a versioned fragment. One line.
- Where a step is integration/ops and not unit-testable, it specifies an exact manual command + expected observation instead of a test — but keeps the bite-sized structure. One line.

---

## File Structure

```
go/orchestrator/dindworker/
  decode.go            # SSE envelope stream → orchestrator.Result
  decode_test.go
  ledger.go            # TreeLedger: depth / per-scope spawn / tree-token floors
  ledger_test.go
  dindworker.go        # Runtime: implements orchestrator.WorkerRuntime over agentkit.Runner
  dindworker_test.go
  config.go            # tier→model map + SessionEnv builder (secrets wiring)
  config_test.go
go/runner.go           # EDIT: rehydrateConversation best-effort → fail-loud (typed error)
go/runner_test.go      # EDIT/ADD: fail-loud rehydrate test
go/errors.go           # ADD: ErrConversationRehydrate sentinel (agentkit package)
go/cmd/orchestratord/
  main.go              # the v1 orchestrator daemon (manager loop + HTTP API + WorkerRuntime factory)
  factory.go           # WORKER_RUNTIME=inproc|dind seam swap
  factory_test.go
deploy/
  orchestratord.Dockerfile
docker-compose.orchestrator.yml   # orchestrator + Postgres + web + DinD, single box
docs/superpowers/runbooks/
  2026-06-30-badcode-marketing-live.md   # the runbook (Task 10)
```

---

### Task 1: SSE envelope stream → `orchestrator.Result` decoder

The existing `agentkit.Runner.SendMessage` streams SSE frames (`event: <type>\ndata: <json>\n\n`, the `events.Type` vocabulary in `go/events/events.go`) to an `io.Writer` and returns only an `error` — it does NOT return a `Result`. The adapter must reconstruct an `orchestrator.Result` by parsing that stream. Isolate that parsing here so it is table-testable without Docker.

**Files:**
- Create: `go/orchestrator/dindworker/decode.go`
- Test: `go/orchestrator/dindworker/decode_test.go`

**Interfaces:**
- Consumes: `github.com/binocarlos/badcode-agent-orange/events` (`events.Envelope`, `events.Type` constants: `ContentDelta`, `MessageEnd`, `QueryComplete`, `Error`, `AskUser`), `orchestrator.Result`, `orchestrator.ResultStatus` (`ResultDone`/`ResultEscalated`/`ResultFailed`).
- Produces: `type sseCapture struct{ ... }` implementing `io.Writer` (buffers raw SSE bytes); `func decodeResult(sessionID, ticketID string, raw []byte, sendErr error) orchestrator.Result`. Concatenates `ContentDelta` `data.text` into `Result.Output`; terminal `QueryComplete` → `ResultDone`; an `Error` frame or non-nil `sendErr` → `ResultFailed` (Output = the error text); an `AskUser` frame → `ResultEscalated` (Output = the question). `TokensUsed` is read from a `MessageEnd`/`QueryComplete` frame's `data.usage.output_tokens`+`input_tokens` when present, else 0.

- [ ] **Step 1: Write the failing test** (`decode_test.go`)

```go
package dindworker

import (
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func sse(frames ...string) []byte {
	var b []byte
	for _, f := range frames {
		b = append(b, []byte(f)...)
	}
	return b
}

func TestDecodeResult(t *testing.T) {
	tests := []struct {
		name     string
		raw      []byte
		sendErr  error
		wantOut  string
		wantStat orchestrator.ResultStatus
		wantTok  int64
	}{
		{
			name: "done concatenates content deltas + reads usage",
			raw: sse(
				"event: content_delta\ndata: {\"text\":\"Draft: \"}\n\n",
				"event: content_delta\ndata: {\"text\":\"buy BadCode.\"}\n\n",
				"event: query_complete\ndata: {\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n\n",
			),
			wantOut: "Draft: buy BadCode.", wantStat: orchestrator.ResultDone, wantTok: 15,
		},
		{
			name:     "error frame → failed",
			raw:      sse("event: error\ndata: {\"message\":\"model refused\"}\n\n"),
			wantOut:  "model refused", wantStat: orchestrator.ResultFailed,
		},
		{
			name:     "ask_user → escalated with the question",
			raw:      sse("event: ask_user\ndata: {\"text\":\"Which channel?\"}\n\n"),
			wantOut:  "Which channel?", wantStat: orchestrator.ResultEscalated,
		},
		{
			name:     "transport error wins even with partial output",
			raw:      sse("event: content_delta\ndata: {\"text\":\"partial\"}\n\n"),
			sendErr:  errBoom,
			wantOut:  "partial", wantStat: orchestrator.ResultFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := decodeResult("sess-1", "tk-1", tc.raw, tc.sendErr)
			if r.SessionID != "sess-1" || r.TicketID != "tk-1" {
				t.Fatalf("ids: %+v", r)
			}
			if r.Output != tc.wantOut || r.Status != tc.wantStat {
				t.Fatalf("got out=%q stat=%q; want %q %q", r.Output, r.Status, tc.wantOut, tc.wantStat)
			}
			if r.TokensUsed != tc.wantTok {
				t.Fatalf("tokens = %d, want %d", r.TokensUsed, tc.wantTok)
			}
		})
	}
}

var errBoom = &boomErr{}

type boomErr struct{}

func (*boomErr) Error() string { return "boom" }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/dindworker/ -run TestDecodeResult -v`
Expected: FAIL — `undefined: decodeResult`.

- [ ] **Step 3: Write minimal implementation** (`decode.go`)

```go
// Package dindworker adapts the existing agentkit Runner (Docker-in-Docker on
// GKE) to the frozen orchestrator.WorkerRuntime seam. It is the production twin
// of the Slice-C in-process WorkerRuntime: same interface, swapped by config.
package dindworker

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// sseCapture is an io.Writer that buffers the raw SSE stream SendMessage tees to
// it. It is concurrency-safe because SendMessage writes from its own goroutine.
type sseCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *sseCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *sseCapture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}

// decodeResult reconstructs an orchestrator.Result from the raw SSE frames of one
// SendMessage turn. SendMessage returns only an error; the Result must be parsed
// from the events.Type stream (go/events/events.go). This is the single point that
// bridges "agentkit streams SSE" to "WorkerRuntime returns a Result".
func decodeResult(sessionID, ticketID string, raw []byte, sendErr error) orchestrator.Result {
	res := orchestrator.Result{
		SessionID: sessionID,
		TicketID:  ticketID,
		Status:    orchestrator.ResultDone,
	}
	var out strings.Builder
	for _, ev := range parseSSE(raw) {
		switch ev.Type {
		case events.ContentDelta:
			if s, ok := ev.Data["text"].(string); ok {
				out.WriteString(s)
			}
		case events.AskUser:
			res.Status = orchestrator.ResultEscalated
			if s, ok := ev.Data["text"].(string); ok {
				res.Output = s
			}
			return res
		case events.Error:
			res.Status = orchestrator.ResultFailed
			if s, ok := ev.Data["message"].(string); ok {
				res.Output = s
			}
			return res
		case events.MessageEnd, events.QueryComplete:
			res.TokensUsed += usage(ev)
		}
	}
	res.Output = out.String()
	if sendErr != nil {
		// Keep any partial output for the human, but the turn FAILED.
		res.Status = orchestrator.ResultFailed
		if res.Output == "" {
			res.Output = sendErr.Error()
		}
	}
	return res
}

func usage(ev events.Envelope) int64 {
	u, ok := ev.Data["usage"].(map[string]any)
	if !ok {
		return 0
	}
	num := func(k string) int64 {
		if f, ok := u[k].(float64); ok {
			return int64(f)
		}
		return 0
	}
	return num("input_tokens") + num("output_tokens")
}

// parseSSE splits a raw SSE byte stream into events.Envelope values. Frames are
// "event: <type>\ndata: <json>\n\n"; malformed frames are skipped.
func parseSSE(raw []byte) []events.Envelope {
	var out []events.Envelope
	for _, frame := range bytes.Split(raw, []byte("\n\n")) {
		var typ events.Type
		var data map[string]any
		for _, line := range bytes.Split(frame, []byte("\n")) {
			switch {
			case bytes.HasPrefix(line, []byte("event:")):
				typ = events.Type(strings.TrimSpace(string(line[len("event:"):])))
			case bytes.HasPrefix(line, []byte("data:")):
				_ = json.Unmarshal(bytes.TrimSpace(line[len("data:"):]), &data)
			}
		}
		if typ != "" {
			out = append(out, events.Envelope{Type: typ, Data: data})
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/dindworker/ -run TestDecodeResult -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/dindworker/decode.go go/orchestrator/dindworker/decode_test.go
git commit -m "feat(dindworker): SSE envelope stream → orchestrator.Result decoder"
```

---

### Task 2: `TreeLedger` — the recursion floors (depth / per-scope spawn / tree-token)

Contracts §7 #1: `depth ≤ Budget.MaxDepth`, per-scope spawns `≤ Budget.MaxSpawns`, shared `Budget.TreeTokens` decremented down the tree, "checked by the spawn path; exceed → refuse". agentkit's `Runner` has **no** notion of budget — the floor is entirely orchestrator-side. This is a small in-memory ledger keyed by root-session and parent-session; it is shared by both the in-proc and DinD `WorkerRuntime` impls (define it here, export it for Slice C to reuse).

**Files:**
- Create: `go/orchestrator/dindworker/ledger.go`
- Test: `go/orchestrator/dindworker/ledger_test.go`

**Interfaces:**
- Consumes: `orchestrator.Scope`, `orchestrator.Budget`.
- Produces:
  - `var (ErrDepthExceeded, ErrSpawnsExceeded, ErrTreeTokensExhausted error)`.
  - `type TreeLedger struct { ... }`; `func NewTreeLedger() *TreeLedger`.
  - `func (l *TreeLedger) Admit(s orchestrator.Scope) error` — atomically checks all three floors for one prospective spawn and, on success, records depth + increments the parent's spawn count + reserves `s.Budget.TreeTokens` against the root's remaining balance. Returns the first floor error on refusal (nothing recorded).
  - `func (l *TreeLedger) SettleTokens(rootSessionID string, actual int64)` — after a worker finishes, reconciles the reservation with actual `Result.TokensUsed` (release over-reservation / debit under-reservation) so the tree budget tracks real spend.
  - `func rootOf(s orchestrator.Scope) string` — walks to the root (`Parent == ""`); v1 uses `s.Parent` when set else the scope's own new id — for v1's shallow trees the root key is `s.Parent` or `"root"`.

- [ ] **Step 1: Write the failing test** (`ledger_test.go`)

```go
package dindworker

import (
	"errors"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func scope(parent string, b orchestrator.Budget) orchestrator.Scope {
	return orchestrator.Scope{Name: "post-writer", Parent: parent, Budget: b}
}

func TestTreeLedgerFloors(t *testing.T) {
	l := NewTreeLedger()
	root := orchestrator.Budget{MaxDepth: 2, MaxSpawns: 2, TreeTokens: 1000}

	// Depth: a scope whose parent is already at MaxDepth is refused.
	if err := l.Admit(scope("", root)); err != nil { // depth 1, ok
		t.Fatalf("root admit: %v", err)
	}

	// Per-scope spawn cap: parent "p" may spawn 2, the 3rd is refused.
	pbud := orchestrator.Budget{MaxDepth: 5, MaxSpawns: 2, TreeTokens: 1000}
	for i := 0; i < 2; i++ {
		if err := l.Admit(scope("p", pbud)); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}
	if err := l.Admit(scope("p", pbud)); !errors.Is(err, ErrSpawnsExceeded) {
		t.Fatalf("3rd spawn err = %v, want ErrSpawnsExceeded", err)
	}

	// Tree-token exhaustion: reserving more than remains is refused.
	tbud := orchestrator.Budget{MaxDepth: 5, MaxSpawns: 9, TreeTokens: 100}
	if err := l.Admit(scope("t", tbud)); err != nil {
		t.Fatalf("first token reserve: %v", err)
	}
	big := orchestrator.Budget{MaxDepth: 5, MaxSpawns: 9, TreeTokens: 100}
	// second reserve on same root exceeds the 100 tree budget
	if err := l.Admit(orchestrator.Scope{Parent: "t", Budget: big}); !errors.Is(err, ErrTreeTokensExhausted) {
		t.Fatalf("token exhaustion err = %v, want ErrTreeTokensExhausted", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/dindworker/ -run TestTreeLedgerFloors -v`
Expected: FAIL — `undefined: NewTreeLedger`.

- [ ] **Step 3: Write minimal implementation** (`ledger.go`)

```go
package dindworker

import (
	"errors"
	"fmt"
	"sync"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

var (
	ErrDepthExceeded       = errors.New("dindworker: Budget.MaxDepth exceeded")
	ErrSpawnsExceeded      = errors.New("dindworker: Budget.MaxSpawns exceeded")
	ErrTreeTokensExhausted = errors.New("dindworker: Budget.TreeTokens exhausted")
)

// TreeLedger enforces the contracts-§7 recursion floors in mechanism. It is
// shared by the in-proc (Slice C) and DinD (Slice F) WorkerRuntime impls, so the
// floors are identical regardless of where a scope runs.
type TreeLedger struct {
	mu        sync.Mutex
	depth     map[string]int   // sessionID/scopeKey → depth
	spawns    map[string]int   // parentID → spawns issued
	remaining map[string]int64 // rootID → tree-token balance
}

func NewTreeLedger() *TreeLedger {
	return &TreeLedger{
		depth:     map[string]int{},
		spawns:    map[string]int{},
		remaining: map[string]int64{},
	}
}

// Admit atomically checks all three floors for one prospective spawn. On success
// it records the reservation; on refusal it records nothing and returns the first
// floor breached.
func (l *TreeLedger) Admit(s orchestrator.Scope) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	depth := l.depth[s.Parent] + 1
	if depth > s.Budget.MaxDepth {
		return fmt.Errorf("%w: depth %d > %d (parent %q)", ErrDepthExceeded, depth, s.Budget.MaxDepth, s.Parent)
	}
	if s.Parent != "" && l.spawns[s.Parent]+1 > s.Budget.MaxSpawns {
		return fmt.Errorf("%w: parent %q already spawned %d", ErrSpawnsExceeded, s.Parent, l.spawns[s.Parent])
	}

	root := rootOf(s)
	if _, seen := l.remaining[root]; !seen {
		l.remaining[root] = s.Budget.TreeTokens
	}
	if l.remaining[root]-s.Budget.TreeTokens < 0 {
		return fmt.Errorf("%w: root %q has %d, need %d", ErrTreeTokensExhausted, root, l.remaining[root], s.Budget.TreeTokens)
	}

	// Record.
	l.remaining[root] -= s.Budget.TreeTokens
	l.spawns[s.Parent]++
	l.depth[s.Name+"@"+root] = depth
	return nil
}

// SettleTokens reconciles a reservation with actual usage once a worker finishes.
func (l *TreeLedger) SettleTokens(rootSessionID string, reserved, actual int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.remaining[rootSessionID] += reserved - actual // release unused; debit overrun
}

// rootOf returns the tree-root key for a scope. v1 trees are shallow (manager →
// worker), so the root is the parent id when set, else a shared "root" bucket.
func rootOf(s orchestrator.Scope) string {
	if s.Parent == "" {
		return "root"
	}
	return s.Parent
}
```

Note: `SettleTokens`'s signature evolves the test call in Task 3; the Task-2 test does not exercise it. Keep the reservation quantum simple for v1 (reserve `Budget.TreeTokens` per admit); tune the reservation model when the manager loop produces real trees.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/dindworker/ -run TestTreeLedgerFloors -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/dindworker/ledger.go go/orchestrator/dindworker/ledger_test.go
git commit -m "feat(dindworker): TreeLedger — depth/spawn/tree-token floors (contracts §7)"
```

---

### Task 3: The DinD `WorkerRuntime` adapter (`Spawn` → floors → CreateSession → fire-and-forget → Deliver)

The keystone. Implements `orchestrator.WorkerRuntime.Spawn` over the existing `agentkit.Runner`. `Spawn` enforces the floors, composes the prompt, provisions via `CreateSession`, and returns the `sessionID` immediately; a goroutine runs the single turn, decodes the `Result`, charges the `SpendMeter`, and delivers to the `ResultSink`.

**Files:**
- Create: `go/orchestrator/dindworker/dindworker.go`
- Test: `go/orchestrator/dindworker/dindworker_test.go`

**Interfaces:**
- Consumes: `agentkit.Runner` (`CreateSession`, `SendMessage`, `SessionRef`, `CreateSessionRequest`, `SendMessageRequest`), `agentdb.BoardStore`, `orchestrator.Compose`, `orchestrator.Scope`, `orchestrator.Result`, `orchestrator.ResultSink`, `orchestrator.SpendMeter`, `orchestrator.WorkerRuntime`.
- Produces:
  - `type Deps struct { Runner agentkit.Runner; Board agentdb.BoardStore; Sink orchestrator.ResultSink; Spend orchestrator.SpendMeter; Ledger *TreeLedger; TierModel map[orchestrator.ModelTier]string; NewID func(orchestrator.Scope) string; USDPerToken map[orchestrator.ModelTier]float64 }`.
  - `func New(d Deps) (*Runtime, error)` — validates required deps; defaults `NewID` and `Ledger`.
  - `type Runtime struct { ... }` implementing `orchestrator.WorkerRuntime`.
  - `func (rt *Runtime) Spawn(ctx context.Context, s orchestrator.Scope) (string, error)`.
- Static assertion: `var _ orchestrator.WorkerRuntime = (*Runtime)(nil)`.

- [ ] **Step 1: Write the failing test** (`dindworker_test.go`)

```go
package dindworker

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentkit"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// fakeRunner records CreateSession and streams a scripted SSE reply on SendMessage.
type fakeRunner struct {
	created   []agentkit.CreateSessionRequest
	sseReply  string
	sendErr   error
	sawModel  string
}

func (f *fakeRunner) CreateSession(_ context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
	f.created = append(f.created, req)
	f.sawModel = req.Model
	return &agentkit.SessionHandle{SessionID: req.SessionID, State: "running"}, nil
}
func (f *fakeRunner) SendMessage(_ context.Context, _ agentkit.SessionRef, _ agentkit.SendMessageRequest, w agentkit.Writer) error {
	_, _ = w.Write([]byte(f.sseReply))
	return f.sendErr
}
func (f *fakeRunner) Stream(context.Context, agentkit.SessionRef, agentkit.StreamOptions, agentkit.Writer) error { return nil }
func (f *fakeRunner) Stop(context.Context, agentkit.SessionRef) error                                            { return nil }
func (f *fakeRunner) Resume(context.Context, agentkit.SessionRef) (*agentkit.SessionHandle, error)               { return nil, nil }
func (f *fakeRunner) Destroy(context.Context, agentkit.SessionRef) error                                         { return nil }
func (f *fakeRunner) Snapshot(context.Context, agentkit.SessionRef) (imageregistry.Handle, error)                { return imageregistry.Handle{}, nil }
func (f *fakeRunner) WriteWorkspaceFile(context.Context, agentkit.SessionRef, string, []byte) error              { return nil }
func (f *fakeRunner) Status(context.Context, agentkit.SessionRef) (*agentkit.SessionStatus, error)               { return nil, nil }
func (f *fakeRunner) RunningSessions(context.Context) (map[string]bool, error)                                   { return nil, nil }
func (f *fakeRunner) Start(context.Context) error                                                                { return nil }
func (f *fakeRunner) Close() error                                                                               { return nil }

type capSink struct {
	mu   sync.Mutex
	got  []orchestrator.Result
	done chan struct{}
}

func (s *capSink) Deliver(_ context.Context, r orchestrator.Result) error {
	s.mu.Lock()
	s.got = append(s.got, r)
	s.mu.Unlock()
	close(s.done)
	return nil
}

type fakeSpend struct{ charged int64 }

func (f *fakeSpend) Charge(_ context.Context, tokens int64, _ float64) error { f.charged += tokens; return nil }
func (f *fakeSpend) Spent(context.Context) (float64, error)                  { return 0, nil }

func seedBoard(t *testing.T) *orchestrator.MemBoard {
	t.Helper()
	b := orchestrator.NewMemBoard()
	if _, err := b.Append(context.Background(), orchestrator.SeedFragment("role-post-writer", "You write short posts.")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return b
}

func TestSpawnRunsTurnAndDelivers(t *testing.T) {
	fr := &fakeRunner{sseReply: "event: content_delta\ndata: {\"text\":\"Buy BadCode!\"}\n\n" +
		"event: query_complete\ndata: {\"usage\":{\"input_tokens\":4,\"output_tokens\":3}}\n\n"}
	sink := &capSink{done: make(chan struct{})}
	spend := &fakeSpend{}

	rt, err := New(Deps{
		Runner:      fr,
		Board:       seedBoard(t),
		Sink:        sink,
		Spend:       spend,
		Ledger:      NewTreeLedger(),
		TierModel:   map[orchestrator.ModelTier]string{orchestrator.TierMid: "claude-sonnet-4"},
		NewID:       func(orchestrator.Scope) string { return "sess-xyz" },
		USDPerToken: map[orchestrator.ModelTier]float64{orchestrator.TierMid: 0.000003},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sid, err := rt.Spawn(context.Background(), orchestrator.Scope{
		Name:     "post-writer",
		Template: "{{fragment:role-post-writer}}\nObjective: {{input}}",
		Input:    "announce v1",
		Tier:     orchestrator.TierMid,
		TicketID: "tk-9",
		Budget:   orchestrator.Budget{MaxDepth: 3, MaxSpawns: 3, TreeTokens: 10000},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if sid != "sess-xyz" {
		t.Fatalf("sessionID = %q", sid)
	}
	// Spawn returned immediately; the result lands asynchronously.
	<-sink.done

	if len(fr.created) != 1 || fr.sawModel != "claude-sonnet-4" {
		t.Fatalf("CreateSession model wiring wrong: %+v model=%q", fr.created, fr.sawModel)
	}
	r := sink.got[0]
	if r.SessionID != "sess-xyz" || r.TicketID != "tk-9" || r.Output != "Buy BadCode!" || r.Status != orchestrator.ResultDone {
		t.Fatalf("delivered result wrong: %+v", r)
	}
	if r.TokensUsed != 7 || spend.charged != 7 {
		t.Fatalf("tokens/charge wrong: result=%d charged=%d", r.TokensUsed, spend.charged)
	}
}

func TestSpawnRefusedByFloorNeverProvisions(t *testing.T) {
	fr := &fakeRunner{}
	rt, _ := New(Deps{
		Runner: fr, Board: seedBoard(t), Sink: &capSink{done: make(chan struct{})},
		Spend: &fakeSpend{}, Ledger: NewTreeLedger(),
		TierModel: map[orchestrator.ModelTier]string{orchestrator.TierMid: "m"},
		NewID:     func(orchestrator.Scope) string { return "s" },
	})
	_, err := rt.Spawn(context.Background(), orchestrator.Scope{
		Name: "deep", Template: "x", Tier: orchestrator.TierMid, Parent: "p",
		Budget: orchestrator.Budget{MaxDepth: 0, MaxSpawns: 1, TreeTokens: 1},
	})
	if !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("err = %v, want ErrDepthExceeded", err)
	}
	if len(fr.created) != 0 {
		t.Fatalf("floor breach must NOT provision a container, got %d CreateSession", len(fr.created))
	}
	_ = bytes.MinRead
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/dindworker/ -run TestSpawn -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write minimal implementation** (`dindworker.go`)

```go
package dindworker

import (
	"context"
	"fmt"
	"log"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkit"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

var _ orchestrator.WorkerRuntime = (*Runtime)(nil)

// Deps constructs a DinD-backed WorkerRuntime.
type Deps struct {
	Runner      agentkit.Runner
	Board       agentdb.BoardStore
	Sink        orchestrator.ResultSink
	Spend       orchestrator.SpendMeter
	Ledger      *TreeLedger
	TierModel   map[orchestrator.ModelTier]string
	USDPerToken map[orchestrator.ModelTier]float64
	NewID       func(orchestrator.Scope) string
}

// Runtime implements orchestrator.WorkerRuntime by driving the existing agentkit
// Runner (DinD on GKE). It is the production twin of the Slice-C in-process impl:
// the manager loop cannot tell them apart — the choice is config (WORKER_RUNTIME).
type Runtime struct {
	d Deps
}

func New(d Deps) (*Runtime, error) {
	if d.Runner == nil || d.Board == nil || d.Sink == nil || d.Spend == nil {
		return nil, fmt.Errorf("dindworker.New: Runner, Board, Sink and Spend are required")
	}
	if d.Ledger == nil {
		d.Ledger = NewTreeLedger()
	}
	if d.NewID == nil {
		d.NewID = defaultNewID
	}
	return &Runtime{d: d}, nil
}

// Spawn enforces the floors, provisions the session, and runs one turn
// fire-and-forget. It returns the sessionID immediately; the Result lands on the
// ResultSink when the turn completes (contracts §2/§7).
func (rt *Runtime) Spawn(ctx context.Context, s orchestrator.Scope) (string, error) {
	// FLOOR: recursion (depth / per-scope spawn / tree tokens) — refuse BEFORE we
	// provision a container, so a runaway never even starts.
	if err := rt.d.Ledger.Admit(s); err != nil {
		return "", err
	}
	// FLOOR: spend ceiling — Charge errors at the monthly ceiling → dispatch halts.
	if err := rt.d.Spend.Charge(ctx, 0, 0); err != nil {
		return "", fmt.Errorf("dindworker: spend gate: %w", err)
	}

	// Resolve the scope to one prompt: compose {{fragment:ID}} against the pinned
	// board + {{input}}. Identical to Slice-C RunScope, so behaviour matches.
	board, err := rt.d.Board.Current(ctx)
	if err != nil {
		return "", fmt.Errorf("dindworker: current board: %w", err)
	}
	prompt, err := orchestrator.Compose(board, s.Template, s.Input)
	if err != nil {
		return "", fmt.Errorf("dindworker: compose %s: %w", s.Name, err)
	}

	model, ok := rt.d.TierModel[s.Tier]
	if !ok {
		return "", fmt.Errorf("dindworker: no model configured for tier %q", s.Tier)
	}

	sessionID := rt.d.NewID(s)
	if _, err := rt.d.Runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID,
		Model:     model,
		MaxTurns:  1,
	}); err != nil {
		return "", fmt.Errorf("dindworker: create session: %w", err)
	}

	// Fire-and-forget: the turn runs on its own context so the manager tick that
	// called Spawn returns at once (contracts §2).
	go rt.run(context.Background(), sessionID, s, prompt)
	return sessionID, nil
}

func (rt *Runtime) run(ctx context.Context, sessionID string, s orchestrator.Scope, prompt string) {
	var cap sseCapture
	sendErr := rt.d.Runner.SendMessage(ctx, agentkit.SessionRef{SessionID: sessionID},
		agentkit.SendMessageRequest{Content: prompt}, &cap)

	res := decodeResult(sessionID, s.TicketID, cap.bytes(), sendErr)

	// FLOOR: post-charge real spend + reconcile the tree-token reservation.
	usd := rt.d.USDPerToken[s.Tier] * float64(res.TokensUsed)
	if err := rt.d.Spend.Charge(ctx, res.TokensUsed, usd); err != nil {
		log.Printf("dindworker: charge %s: %v", sessionID, err)
	}
	rt.d.Ledger.SettleTokens(rootOf(s), s.Budget.TreeTokens, res.TokensUsed)

	if err := rt.d.Sink.Deliver(ctx, res); err != nil {
		log.Printf("dindworker: deliver %s: %v", sessionID, err)
	}
}

func defaultNewID(s orchestrator.Scope) string {
	return "wrk-" + s.Name + "-" + orchestrator.NewRunID()
}
```

Note: `orchestrator.NewRunID()` is a small helper the orchestrator already needs for unique ids; if Slice C has not added it, define a 3-line ULID/counter helper in `orchestrator` and reference it here (do NOT duplicate id-minting logic in `dindworker`).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/dindworker/ -run TestSpawn -v`
Expected: PASS.

- [ ] **Step 5: Verify the seam-swap invariant compiles**

Run: `cd go && go build ./orchestrator/... && go vet ./orchestrator/dindworker/`
Expected: green — the `var _ orchestrator.WorkerRuntime = (*Runtime)(nil)` assertion proves the DinD impl and the Slice-C in-proc impl satisfy the SAME interface.

- [ ] **Step 6: Commit**

```bash
git add go/orchestrator/dindworker/dindworker.go go/orchestrator/dindworker/dindworker_test.go
git commit -m "feat(dindworker): production WorkerRuntime over agentkit DinD Runner"
```

---

### Task 4: Fail-loud conversation rehydrate (reverse agentkit's best-effort) — the §7 #4 floor

`go/runner.go:805–807` makes `rehydrateConversation` **best-effort**: on failure the session resumes *usable but amnesiac*. exec-model §6 REVERSES this for the autonomous org — a manager resumed without its conversation makes decisions on no context and the damage propagates silently. This task makes rehydrate **fail-loud** while preserving the liftability invariant (the fix is a typed error in the `agentkit` package; the mapping to a Needs-Human ticket is Task 5, orchestrator-side).

**Files:**
- Create: `go/errors.go`
- Edit: `go/runner.go` (`rehydrateConversation` + its caller `restoreToWorker` at line 807)
- Test: `go/runner_test.go` (add)

**Interfaces:**
- Produces: `var ErrConversationRehydrate = errors.New("agentkit: could not restore session conversation")` in package `agentkit`.
- Changes: `rehydrateConversation(...)` returns `error`. It **distinguishes** legitimately-empty (`len(msgs)==0` — brand-new session, returns nil) from **failed-to-load an existing non-empty history** (wraps `ErrConversationRehydrate`). `restoreToWorker` propagates that error (after a bounded retry) instead of swallowing it, so `Resume`/`SendMessage` fail loudly rather than continuing amnesiac.

- [ ] **Step 1: Write the failing test** (`runner_test.go`, add)

```go
func TestRehydrateFailLoudOnNonEmptyHistory(t *testing.T) {
	// A store that HAS query events (non-empty history) but whose load-conversation
	// POST fails must cause restore to error with ErrConversationRehydrate — never
	// resume amnesiac (exec-model §6; contracts §7 #4).
	r := newRunnerImplForTest(t, withFailingLoadConversation(), withQueryEvents(2))
	_, err := r.restoreToWorker(context.Background(), "sess-amnesiac", testWorker(t))
	if !errors.Is(err, ErrConversationRehydrate) {
		t.Fatalf("err = %v, want ErrConversationRehydrate", err)
	}
}

func TestRehydrateEmptyHistoryIsOK(t *testing.T) {
	// A brand-new session (no query events) legitimately has nothing to load and
	// must NOT be treated as a rehydrate failure.
	r := newRunnerImplForTest(t, withFailingLoadConversation(), withQueryEvents(0))
	_, err := r.restoreToWorker(context.Background(), "sess-new", testWorker(t))
	if err != nil {
		t.Fatalf("empty-history restore should succeed, got %v", err)
	}
}
```

(Reuse the existing runner-test harness/mocks; `newRunnerImplForTest`, `withFailingLoadConversation`, `withQueryEvents`, `testWorker` are thin builders over the mocks already in `go/` tests — mirror the existing `restoreToWorker`/snapshot test setup.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./ -run TestRehydrate -v`
Expected: FAIL — `undefined: ErrConversationRehydrate` (and best-effort path returns nil).

- [ ] **Step 3: Write minimal implementation**

`go/errors.go`:

```go
package agentkit

import "errors"

// ErrConversationRehydrate is returned when a session with a non-empty persisted
// conversation cannot have that conversation restored into its freshly-provisioned
// sandbox. Per exec-coordination-model §6 / contracts §7 #4 the resume FAILS LOUD
// rather than continuing amnesiac: an autonomous manager with no human watching
// must not make spawn/update decisions on a wiped context. The orchestrator maps
// this to a Needs-Human ticket (see orchestrator/dindworker mapping).
var ErrConversationRehydrate = errors.New("agentkit: could not restore session conversation")
```

`go/runner.go` — change the caller (line 807) and the function signature:

```go
	// Rehydrate the in-image conversation history. FAIL LOUD: unlike the original
	// best-effort behaviour, a non-empty history that cannot be restored fails the
	// restore (exec-model §6). Amnesiac autonomous resume is the failure mode we are
	// most exposed to.
	if err := r.rehydrateConversation(ctx, sessionID, inst); err != nil {
		// Quarantine: tear the misleading (amnesiac) instance down so no turn runs
		// against it, and surface the typed error for the orchestrator to ticket.
		_ = worker.Env.Destroy(ctx, inst.ID, execenv.DestroyOptions{SkipSnapshot: true})
		return nil, err
	}
	return inst, nil
```

```go
// rehydrateConversation reconstructs the session's conversation from the event log
// and loads it into the restored sandbox. Returns nil when there is legitimately
// nothing to load (brand-new session). Returns a wrapped ErrConversationRehydrate
// when an EXISTING non-empty history cannot be restored — the caller fails loud.
func (r *runnerImpl) rehydrateConversation(ctx context.Context, sessionID string, inst *execenv.Instance) error {
	evs, err := r.deps.Store.ListQueryEventsFlat(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("%w: list events for %s: %v", ErrConversationRehydrate, sessionID, err)
	}
	msgs := reconstructConversation(evs)
	if len(msgs) == 0 {
		return nil // legitimately empty — brand-new session
	}

	sess, err := r.deps.Store.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("%w: get session %s: %v", ErrConversationRehydrate, sessionID, err)
	}
	scope := extension.ContextScope{Customer: sess.Customer, Job: sess.Job, Persona: sess.Persona, UserEmail: sess.UserEmail}
	token, err := r.issueToken(ctx, scope, sessionID)
	if err != nil {
		return fmt.Errorf("%w: issue token %s: %v", ErrConversationRehydrate, sessionID, err)
	}
	if err := r.postCreateSession(ctx, inst.Address, CreateSessionRequest{SessionID: sessionID}); err != nil {
		return fmt.Errorf("%w: create session %s: %v", ErrConversationRehydrate, sessionID, err)
	}

	// Bounded retry then fail (retry-then-quarantine, NOT crash-loop; exec-model §6).
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if lastErr = r.postLoadConversation(ctx, inst.Address, sessionID, token, msgs); lastErr == nil {
			log.Printf("agentkit: rehydrate %s: loaded %d conversation messages", sessionID, len(msgs))
			return nil
		}
		log.Printf("agentkit: rehydrate %s: load-conversation attempt %d: %v", sessionID, attempt, lastErr)
	}
	return fmt.Errorf("%w: load-conversation %s after 3 attempts: %v", ErrConversationRehydrate, sessionID, lastErr)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./ -run TestRehydrate -v`
Expected: PASS. Then full engine regression: `cd go && go test ./...` (the orphan-recover path is unaffected — it never calls `rehydrateConversation`; verify no existing restore test regressed).

- [ ] **Step 5: Commit**

```bash
git add go/errors.go go/runner.go go/runner_test.go
git commit -m "fix(agentkit): fail loud on conversation rehydrate (reverse best-effort; §7 #4)"
```

---

### Task 5: Map `ErrConversationRehydrate` → a Needs-Human ticket (the orchestrator side of the floor)

The floor is only complete when a fail-loud restore becomes a **visible Needs-Human ticket**, not a log line. When `Spawn`/`run` hits `agentkit.ErrConversationRehydrate` (a snapshot worker resumed with a lost conversation), the adapter delivers a `Result{Status: ResultFailed}` whose Output names the lost session — the `ResultSink` (Slice C) already routes `ResultFailed` to the Needs-Human lane. This keeps the mapping orchestrator-side (liftability preserved).

**Files:**
- Edit: `go/orchestrator/dindworker/dindworker.go` (the `run` path and a Resume branch)
- Test: `go/orchestrator/dindworker/dindworker_test.go` (add)

**Interfaces:**
- Consumes: `agentkit.ErrConversationRehydrate`, `orchestrator.ResultFailed`.
- Produces: on any `SendMessage`/Resume error that `errors.Is(err, agentkit.ErrConversationRehydrate)`, deliver `Result{SessionID, TicketID, Status: ResultFailed, Output: "session memory could not be restored — needs human (session <id>)"}`.

- [ ] **Step 1: Write the failing test**

```go
func TestRehydrateFailureBecomesNeedsHumanResult(t *testing.T) {
	fr := &fakeRunner{sendErr: agentkit.ErrConversationRehydrate}
	sink := &capSink{done: make(chan struct{})}
	rt, _ := New(Deps{
		Runner: fr, Board: seedBoard(t), Sink: sink, Spend: &fakeSpend{}, Ledger: NewTreeLedger(),
		TierModel: map[orchestrator.ModelTier]string{orchestrator.TierMid: "m"},
		NewID:     func(orchestrator.Scope) string { return "sess-lost" },
	})
	_, err := rt.Spawn(context.Background(), orchestrator.Scope{
		Name: "post-writer", Template: "{{fragment:role-post-writer}}", Tier: orchestrator.TierMid,
		TicketID: "tk-5", Budget: orchestrator.Budget{MaxDepth: 3, MaxSpawns: 3, TreeTokens: 100},
	})
	if err != nil {
		t.Fatalf("Spawn should succeed (failure surfaces async): %v", err)
	}
	<-sink.done
	r := sink.got[0]
	if r.Status != orchestrator.ResultFailed || !strings.Contains(r.Output, "needs human") {
		t.Fatalf("rehydrate failure must become a Needs-Human ResultFailed, got %+v", r)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/dindworker/ -run TestRehydrateFailureBecomesNeedsHuman -v`
Expected: FAIL — the generic decode maps the raw error text, not a "needs human" message.

- [ ] **Step 3: Write minimal implementation** — in `run`, branch before `decodeResult`:

```go
	if sendErr != nil && errors.Is(sendErr, agentkit.ErrConversationRehydrate) {
		res := orchestrator.Result{
			SessionID: sessionID, TicketID: s.TicketID, Status: orchestrator.ResultFailed,
			Output: fmt.Sprintf("session memory could not be restored — needs human (session %s)", sessionID),
		}
		if err := rt.d.Sink.Deliver(ctx, res); err != nil {
			log.Printf("dindworker: deliver rehydrate-failure %s: %v", sessionID, err)
		}
		return
	}
```

(Add `errors` + `agentkit` imports.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/dindworker/ -run TestRehydrateFailure -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/dindworker/dindworker.go go/orchestrator/dindworker/dindworker_test.go
git commit -m "feat(dindworker): rehydrate failure → Needs-Human Result (§7 #4 floor complete)"
```

---

### Task 6: Config + secrets — tier→model map and `Policy.SessionEnv` builder

Wire the two production secrets and the model tiers. `ANTHROPIC_API_KEY` (the harness's model access) and the **channel token** (the Slice-D connector's credential) flow via env into `agentkit.Policy.SessionEnv`, which the Runner injects into every session container — **never** into a board fragment (deployment-plan secret risk). Model ids per tier are env-configurable (no hardcoded ids that drift).

**Files:**
- Create: `go/orchestrator/dindworker/config.go`
- Test: `go/orchestrator/dindworker/config_test.go`

**Interfaces:**
- Consumes: `orchestrator.ModelTier` (`TierFull`/`TierMid`/`TierCheap`).
- Produces:
  - `func TierModelsFromEnv(getenv func(string) string) map[orchestrator.ModelTier]string` — reads `MODEL_TIER_FULL` (default `claude-opus-4-8`), `MODEL_TIER_MID` (default `claude-sonnet-4-5`), `MODEL_TIER_CHEAP` (default `claude-haiku-4-5`).
  - `func SessionEnvFromEnv(getenv func(string) string, selfURL string) (map[string]string, error)` — builds the per-session env map: `ANTHROPIC_API_KEY` (required — error if empty), `ANTHROPIC_BASE_URL`/`AGENTKIT_SELF_URL` plumbing, and `BADCODE_CHANNEL_TOKEN` (required for live publishing; the Slice-D connector reads it). Never logs the values.

- [ ] **Step 1: Write the failing test**

```go
package dindworker

import (
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestTierModelsDefaultsAndOverride(t *testing.T) {
	env := map[string]string{"MODEL_TIER_MID": "claude-sonnet-custom"}
	m := TierModelsFromEnv(func(k string) string { return env[k] })
	if m[orchestrator.TierFull] != "claude-opus-4-8" {
		t.Fatalf("full default wrong: %q", m[orchestrator.TierFull])
	}
	if m[orchestrator.TierMid] != "claude-sonnet-custom" {
		t.Fatalf("mid override wrong: %q", m[orchestrator.TierMid])
	}
}

func TestSessionEnvRequiresSecrets(t *testing.T) {
	// Missing API key → error (fail before we ever run a live agent).
	if _, err := SessionEnvFromEnv(func(string) string { return "" }, "http://self"); err == nil {
		t.Fatalf("expected error when ANTHROPIC_API_KEY is unset")
	}
	env := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-xxx", "BADCODE_CHANNEL_TOKEN": "tok"}
	got, err := SessionEnvFromEnv(func(k string) string { return env[k] }, "http://self")
	if err != nil {
		t.Fatalf("SessionEnv: %v", err)
	}
	if got["ANTHROPIC_API_KEY"] != "sk-ant-xxx" || got["BADCODE_CHANNEL_TOKEN"] != "tok" {
		t.Fatalf("secrets not plumbed: %+v", redactKeys(got))
	}
}

func redactKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/dindworker/ -run 'TestTierModels|TestSessionEnv' -v`
Expected: FAIL — `undefined: TierModelsFromEnv`.

- [ ] **Step 3: Write minimal implementation** (`config.go`)

```go
package dindworker

import (
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// TierModelsFromEnv resolves each ModelTier to a concrete model id, env-overridable
// so the ids never hardcode-drift. Defaults track the current production tiers.
func TierModelsFromEnv(getenv func(string) string) map[orchestrator.ModelTier]string {
	pick := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	return map[orchestrator.ModelTier]string{
		orchestrator.TierFull:  pick("MODEL_TIER_FULL", "claude-opus-4-8"),
		orchestrator.TierMid:   pick("MODEL_TIER_MID", "claude-sonnet-4-5"),
		orchestrator.TierCheap: pick("MODEL_TIER_CHEAP", "claude-haiku-4-5"),
	}
}

// SessionEnvFromEnv builds the per-session env the Runner injects into every
// container (agentkit.Policy.SessionEnv). Secrets live ONLY here — never in a
// versioned board fragment (deployment-plan secret risk). Values are never logged.
func SessionEnvFromEnv(getenv func(string) string, selfURL string) (map[string]string, error) {
	apiKey := getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("dindworker: ANTHROPIC_API_KEY is required for a live run")
	}
	channelTok := getenv("BADCODE_CHANNEL_TOKEN")
	if channelTok == "" {
		return nil, fmt.Errorf("dindworker: BADCODE_CHANNEL_TOKEN is required for live publishing")
	}
	env := map[string]string{
		"ANTHROPIC_API_KEY":     apiKey,
		"BADCODE_CHANNEL_TOKEN": channelTok,
		"AGENTKIT_SELF_URL":     selfURL,
	}
	if base := getenv("ANTHROPIC_BASE_URL"); base != "" {
		env["ANTHROPIC_BASE_URL"] = base
	}
	return env, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/dindworker/ -run 'TestTierModels|TestSessionEnv' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/dindworker/config.go go/orchestrator/dindworker/config_test.go
git commit -m "feat(dindworker): env-driven tier→model map + secret SessionEnv builder"
```

---

### Task 7: The `WORKER_RUNTIME=inproc|dind` factory (seam swap is config, not code)

Prove the constraint: the manager loop constructs a `orchestrator.WorkerRuntime` from **one env var** and never branches on which impl it got. `inproc` → the Slice-C in-process runtime; `dind` → this slice's adapter wired over a live `agentkit.Runner`.

**Files:**
- Create: `go/cmd/orchestratord/factory.go`
- Test: `go/cmd/orchestratord/factory_test.go`

**Interfaces:**
- Consumes: `orchestrator.WorkerRuntime`, `orchestrator.ResultSink`, `orchestrator.SpendMeter`, `agentdb.BoardStore`, `agentkit.Runner`, `dindworker.New`, the Slice-C `orchestrator.NewInProcRuntime` (or equivalent constructor).
- Produces: `type RuntimeDeps struct { Runner agentkit.Runner; Board agentdb.BoardStore; Sink orchestrator.ResultSink; Spend orchestrator.SpendMeter; Router orchestrator.ModelRouter; Getenv func(string) string; SelfURL string }`; `func BuildWorkerRuntime(kind string, d RuntimeDeps) (orchestrator.WorkerRuntime, error)`.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestBuildWorkerRuntimeSelectsImpl(t *testing.T) {
	env := map[string]string{
		"ANTHROPIC_API_KEY": "sk", "BADCODE_CHANNEL_TOKEN": "t",
	}
	d := RuntimeDeps{ /* fakes: Runner, Board (seeded MemBoard), Sink, Spend, Router */ }
	d.Getenv = func(k string) string { return env[k] }

	dind, err := BuildWorkerRuntime("dind", d)
	if err != nil {
		t.Fatalf("dind: %v", err)
	}
	inproc, err := BuildWorkerRuntime("inproc", d)
	if err != nil {
		t.Fatalf("inproc: %v", err)
	}
	// Both satisfy the SAME interface — the caller cannot tell them apart.
	var _ orchestrator.WorkerRuntime = dind
	var _ orchestrator.WorkerRuntime = inproc

	if _, err := BuildWorkerRuntime("banana", d); err == nil {
		t.Fatalf("expected error for unknown WORKER_RUNTIME")
	}
}
```

(Fill `RuntimeDeps` with the same fakes used in Task 3; the point is that both branches return an `orchestrator.WorkerRuntime`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./cmd/orchestratord/ -run TestBuildWorkerRuntime -v`
Expected: FAIL — `undefined: BuildWorkerRuntime`.

- [ ] **Step 3: Write minimal implementation** (`factory.go`)

```go
package main

import (
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkit"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
	"github.com/binocarlos/badcode-agent-orange/orchestrator/dindworker"
)

// RuntimeDeps carries everything both WorkerRuntime impls might need. The manager
// loop depends only on the returned orchestrator.WorkerRuntime — never on the impl.
type RuntimeDeps struct {
	Runner  agentkit.Runner
	Board   agentdb.BoardStore
	Sink    orchestrator.ResultSink
	Spend   orchestrator.SpendMeter
	Router  orchestrator.ModelRouter
	Getenv  func(string) string
	SelfURL string
}

// BuildWorkerRuntime selects the WorkerRuntime impl from config. "inproc" is the
// Slice-C dev/test runtime (no container); "dind" is the Slice-F production adapter
// over the existing agentkit DinD Runner. Swapping deploy targets is THIS FLAG —
// the manager loop code is identical for both.
func BuildWorkerRuntime(kind string, d RuntimeDeps) (orchestrator.WorkerRuntime, error) {
	switch kind {
	case "", "inproc":
		return orchestrator.NewInProcRuntime(orchestrator.InProcDeps{
			Board: d.Board, Sink: d.Sink, Router: d.Router, Spend: d.Spend,
		})
	case "dind":
		return dindworker.New(dindworker.Deps{
			Runner:    d.Runner,
			Board:     d.Board,
			Sink:      d.Sink,
			Spend:     d.Spend,
			TierModel: dindworker.TierModelsFromEnv(d.Getenv),
		})
	default:
		return nil, fmt.Errorf("orchestratord: unknown WORKER_RUNTIME %q (want inproc|dind)", kind)
	}
}
```

Note: `orchestrator.NewInProcRuntime` / `InProcDeps` are the Slice-C constructor names; if Slice C named them differently, match its exact exported constructor here — do not rename it.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./cmd/orchestratord/ -run TestBuildWorkerRuntime -v`
Expected: PASS.

- [ ] **Step 5: Wire `main.go`** (the daemon) — the orchestrator daemon that: opens Postgres (`agentdb.Open(DATABASE_URL)`), constructs the Slice-A stores + Slice-B `ModelRouter`/`SpendMeter` + Slice-C `ResultSink`/`ManagerExchange` + Slice-D `Connector` + Slice-E HTTP API, builds the `agentkit.Runner` (copy the DinD wiring from `cmd/agentd/main.go:81–120`, injecting `SessionEnvFromEnv`), then `BuildWorkerRuntime(os.Getenv("WORKER_RUNTIME"), …)` and starts the cron tick + HTTP server. This is assembly of existing pieces; verify by build.

Run: `cd go && go build ./cmd/orchestratord/ && go vet ./cmd/orchestratord/`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add go/cmd/orchestratord/
git commit -m "feat(orchestratord): WORKER_RUNTIME factory (inproc|dind) + daemon wiring"
```

---

### Task 8: Single-box `docker compose` — orchestrator + Postgres + web + DinD

Package the deploy for "a single box we watch" (deployment-plan v1 shape). Reuse the repo's DinD pattern (`docker-compose.yml`) but swap `agentd` → `orchestratord`, add **Postgres** (the Slice-A board/tickets/telemetry), and keep the web UI. This is the local/single-VM target; the GKE variant reuses the same `orchestratord` image with `WORKER_RUNTIME=dind` pointed at the cluster DinD (runbook, Task 10).

**Files:**
- Create: `deploy/orchestratord.Dockerfile` (mirror `deploy/agentd.Dockerfile`, building `./cmd/orchestratord`)
- Create: `docker-compose.orchestrator.yml`

**Interfaces:** none (ops). The compose declares services `dind`, `init-sandbox`, `postgres`, `orchestratord`, `web`.

- [ ] **Step 1: Write `deploy/orchestratord.Dockerfile`**

```dockerfile
# Build the v1 orchestrator daemon (manager loop + HTTP API + WorkerRuntime).
FROM golang:1.25 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/orchestratord ./cmd/orchestratord

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/orchestratord /orchestratord
ENTRYPOINT ["/orchestratord"]
```

- [ ] **Step 2: Write `docker-compose.orchestrator.yml`**

```yaml
# v1 single-box stack: orchestrator + Postgres + web, with DinD for workers.
#   cp .env.example .env   # set ANTHROPIC_API_KEY + BADCODE_CHANNEL_TOKEN
#   docker compose -f docker-compose.orchestrator.yml up --build
services:
  dind:
    image: docker:27-dind
    privileged: true
    command: ["dockerd", "--host=tcp://0.0.0.0:2375", "--host=unix:///var/run/docker.sock", "--tls=false"]
    environment:
      DOCKER_TLS_CERTDIR: ""
    healthcheck:
      test: ["CMD", "docker", "info"]
      interval: 5s
      timeout: 5s
      retries: 20

  init-sandbox:
    image: docker:27-cli
    depends_on:
      dind:
        condition: service_healthy
    environment:
      DOCKER_HOST: tcp://dind:2375
    volumes:
      - ./sandbox:/sandbox:ro
    entrypoint: ["sh", "-c"]
    command:
      - |
        set -e
        docker build -t ${BASE_IMAGE:-agentkit-sandbox:dev} /sandbox
        echo "sandbox image built + present in DinD"

  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: orchestrator
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-orchestrator}
      POSTGRES_DB: orchestrator
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U orchestrator"]
      interval: 5s
      timeout: 5s
      retries: 20

  orchestratord:
    build:
      context: ./go
      dockerfile: ../deploy/orchestratord.Dockerfile
    network_mode: "service:dind"    # reach the daemon at localhost:2375
    depends_on:
      dind:
        condition: service_healthy
      init-sandbox:
        condition: service_completed_successfully
      postgres:
        condition: service_healthy
    environment:
      ADDR: ":8099"
      WORKER_RUNTIME: dind
      DOCKER_HOST: tcp://localhost:2375
      AGENTKIT_IMAGE: ${BASE_IMAGE:-agentkit-sandbox:dev}
      AGENTKIT_SELF_URL: http://172.17.0.1:8099
      DATABASE_URL: postgres://orchestrator:${POSTGRES_PASSWORD:-orchestrator}@postgres:5432/orchestrator?sslmode=disable
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY:?ANTHROPIC_API_KEY required}
      BADCODE_CHANNEL_TOKEN: ${BADCODE_CHANNEL_TOKEN:?BADCODE_CHANNEL_TOKEN required}
      ORCHESTRATOR_API_TOKEN: ${ORCHESTRATOR_API_TOKEN:?set a shared API token}
      SPEND_CEILING_USD: ${SPEND_CEILING_USD:-50}

  web:
    build:
      context: .
      dockerfile: deploy/web.Dockerfile
    depends_on:
      - orchestratord
    ports:
      - "${WEB_PORT:-8080}:8080"

volumes:
  pgdata:
```

Note: `postgres` is a peer service, but `orchestratord` shares DinD's network namespace, so `orchestratord` reaches Postgres via the compose gateway. Because `network_mode: service:dind` detaches `orchestratord` from the default bridge, either (a) attach `postgres` and `dind` to a shared user-defined network, or (b) run Postgres inside DinD's namespace too. Simplest for the single box: add both `dind` and `postgres` to one `networks:` entry and set `DATABASE_URL` host to the DinD-visible alias. Resolve this concretely in Step 3 and encode the working form.

- [ ] **Step 3: Manual verification — the stack comes up and migrates**

```sh
cp .env.example .env
# edit .env: ANTHROPIC_API_KEY=sk-ant-…  BADCODE_CHANNEL_TOKEN=…  ORCHESTRATOR_API_TOKEN=…
docker compose -f docker-compose.orchestrator.yml up --build -d
```

Expected observations:
- `docker compose -f docker-compose.orchestrator.yml ps` shows `dind`, `postgres`, `orchestratord`, `web` all `Up`/`healthy` and `init-sandbox` `Exited (0)`.
- `docker compose -f docker-compose.orchestrator.yml logs orchestratord | grep -i migrat` shows the Slice-A migrations (`022_*`…) applied with no error.
- `curl -s localhost:8099/health` → `{"status":"ok"}`.
- `curl -s -H "Authorization: Bearer $ORCHESTRATOR_API_TOKEN" localhost:8099/api/board/current | head` → a JSON `Board` (folded seed fragments).

- [ ] **Step 4: Commit**

```bash
git add deploy/orchestratord.Dockerfile docker-compose.orchestrator.yml
git commit -m "feat(deploy): single-box compose — orchestratord + Postgres + web + DinD"
```

---

### Task 9: Secrets via Secret Manager (GKE) / env (single box)

The two live secrets — `ANTHROPIC_API_KEY` and `BADCODE_CHANNEL_TOKEN` — must reach the orchestrator without ever being committed or written to the board. Single box: `.env` (git-ignored). GKE: Google Secret Manager, mounted as env, reusing the ADC seam already wired for GCS/AR (`cmd/agentd/backends.go`, project `webkit-servers`).

**Files:** none new (ops + `.env.example` doc).

**Interfaces:** none.

- [ ] **Step 1: Confirm `.env` is git-ignored and document the keys**

Run: `git check-ignore .env && grep -n "BADCODE_CHANNEL_TOKEN\|ORCHESTRATOR_API_TOKEN\|SPEND_CEILING_USD\|WORKER_RUNTIME\|DATABASE_URL" .env.example`
Expected: `.env` is ignored; add the missing keys to `.env.example` (commented, no values) if absent.

- [ ] **Step 2: Create the GKE secrets (manual, one-time)**

```sh
gcloud config set project webkit-servers
printf '%s' "$ANTHROPIC_API_KEY"    | gcloud secrets create anthropic-api-key    --data-file=- --replication-policy=automatic
printf '%s' "$BADCODE_CHANNEL_TOKEN" | gcloud secrets create badcode-channel-token --data-file=- --replication-policy=automatic
```

Expected: `Created version [1] of the secret …` for each. Verify: `gcloud secrets list | grep -E 'anthropic-api-key|badcode-channel-token'`.

- [ ] **Step 3: Bind the secrets into the orchestrator Deployment (GKE)**

In the orchestrator Deployment manifest, project each secret as an env var via `secretKeyRef` (using the existing Secret Manager → CSI / External Secrets setup on the cluster). Grant the orchestrator's service account `roles/secretmanager.secretAccessor`:

```sh
gcloud secrets add-iam-policy-binding anthropic-api-key \
  --member="serviceAccount:orchestrator@webkit-servers.iam.gserviceaccount.com" \
  --role="roles/secretmanager.secretAccessor"
gcloud secrets add-iam-policy-binding badcode-channel-token \
  --member="serviceAccount:orchestrator@webkit-servers.iam.gserviceaccount.com" \
  --role="roles/secretmanager.secretAccessor"
```

Expected: `Updated IAM policy for secret [...]`.

- [ ] **Step 4: Verify no secret leaks into the board**

Run (after any live run): `curl -s -H "Authorization: Bearer $ORCHESTRATOR_API_TOKEN" localhost:8099/api/board/current | grep -c 'sk-ant\|BADCODE_CHANNEL_TOKEN'`
Expected: `0` — the deployment-plan invariant (secrets never in versioned fragments) holds.

- [ ] **Step 5: Commit any `.env.example` doc additions**

```bash
git add .env.example
git commit -m "docs(deploy): document orchestrator secrets + Secret Manager wiring"
```

---

### Task 10: Runbook — run BadCode marketing live, behind approval

The final deliverable: a runbook a human follows to bring v1 up and drive the learning loop on real stakes, with the approval gate (Slice D floor) between the model and BadCode's public reputation.

**Files:**
- Create: `docs/superpowers/runbooks/2026-06-30-badcode-marketing-live.md`

**Interfaces:** none (ops doc). It references the §8 HTTP API verbatim.

- [ ] **Step 1: Write the runbook** with these sections, each an exact command + expected observation:

1. **Preflight.** `go build ./... && go test ./orchestrator/... ./ -run 'Rehydrate|Spawn|Decode|TreeLedger'` all green; `.env` has all five keys; `SPEND_CEILING_USD` set. Expected: PASS.
2. **Bring the stack up** (Task 8 Step 3). Expected: 4 services healthy + migrations applied.
3. **Seed the marketing guidance.** POST the seed role/routing fragments (the BadCode marketing brief) via `POST /api/feedback` or a seed script. Verify `GET /api/board/current` shows them. Expected: fragments present, revision `r1`.
4. **Fire one manager tick.** `curl -X POST -H "Authorization: Bearer $ORCHESTRATOR_API_TOKEN" localhost:8099/api/trigger`. Expected: `200`; `GET /api/runs` shows a manager run pinned to the head revision; `GET /api/tickets` shows drafted tickets.
5. **Watch a worker draft a post.** After the next tick, `GET /api/tickets?status=needs_human` returns a ticket with a `PendingPost` (the draft). Expected: a Needs-Human ticket with non-empty `PendingPost.Text`. **Nothing has been published** — verify the channel shows no new post.
6. **The approval gate (the load-bearing floor).** Confirm a worker cannot publish: search logs for any `Connector.Publish` NOT preceded by an approve action — expected none. Then `POST /api/tickets/{id}/approve`. Expected: `200`; the orchestrator (not the worker) calls `Connector.Publish`; the post appears on the channel; the ticket moves to Done.
7. **Reject + note (the teacher's desk).** `POST /api/tickets/{id}/reject {"note":"too salesy, be wittier"}`. Expected: `200`; the note becomes `HumanFeedback` → `write_fragment` → a new board revision (`GET /api/board/revisions` shows author `human-feedback`, the note as message).
8. **Prove the learning.** Fire another tick; the next draft reflects the note. Expected: `GET /api/board/revisions` timeline tells the story; the new draft is visibly on-note. This is the v1 success criterion.
9. **Cost governance check.** `GET /api/runs` token totals stay under `SPEND_CEILING_USD`; force the ceiling in a staging run and confirm dispatch halts (a `Spawn` returns the spend error, no new session provisions). Expected: dispatch halts at the ceiling.
10. **Fail-loud drill (§7 #4).** In staging, kill a worker's sandbox mid-turn so its resume can't rehydrate; confirm the session becomes a Needs-Human ticket ("session memory could not be restored"), NOT a silent amnesiac run. Expected: a `ResultFailed` Needs-Human ticket.
11. **Rollback.** `docker compose -f docker-compose.orchestrator.yml down` (data persists in `pgdata`); to pause dispatch without teardown, set `SPEND_CEILING_USD=0` and restart `orchestratord`. Expected: no new workers spawn.

- [ ] **Step 2: Dry-run the runbook end-to-end** against the single-box stack with the **fake connector** (Slice D test double) so no real post is published, walking every step. Expected: every "Expected" observation holds except the real-channel publish (which the fake connector records as "would post").

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/runbooks/2026-06-30-badcode-marketing-live.md
git commit -m "docs(runbook): run BadCode marketing live behind the approval gate"
```

---

## Contract gaps found

These are genuine mismatches between the frozen contracts (§4/§5/§7) and the existing agentkit `Runner`/`execenv` shape that Slice F adapts. None require a contract change — each is absorbed inside the `dindworker` adapter or a liftability-safe `agentkit` addition — but they are the load-bearing seams to flag.

1. **`agentkit.Runner` is a rich interactive facade, not a fire-and-forget `Spawn`.** The contract's `WorkerRuntime.Spawn(ctx, Scope) (sessionID, error)` maps to a *sequence* — `CreateSession` then a single-turn `SendMessage` — run in a goroutine. There is no 1:1 method. Absorbed in Task 3.
2. **`SendMessage` returns no `Result` — it streams SSE and returns only `error`.** The contract's `Result{Output, Status, TokensUsed}` must be *reconstructed* by parsing the `events.Type` SSE stream (Task 1). In particular `TokensUsed` is not a return value; it is scraped from a `query_complete`/`message_end` usage frame (or, more robustly, from `Deps.TokenLogger`). If a live harness does not emit usage on the stream, `TokensUsed` degrades to 0 and the `SpendMeter` under-charges — flag for Slice B to confirm the harness emits token usage.
3. **`Scope.Budget` (depth/spawn/tree-token) has no home in agentkit.** The Runner knows nothing of recursion floors; the entire floor is enforced orchestrator-side in `TreeLedger.Admit` *before* `CreateSession` (Task 2/3). Contract is satisfiable, but note the floor is a *pre-provision gate*, not something the runtime can interrupt mid-turn.
4. **`SpendMeter` cannot hard-interrupt a running DinD turn.** The in-image harness bills Anthropic *directly*; the meter can only **pre-gate** (`Charge(0,0)` at spawn) and **post-charge** (`Result.TokensUsed` on completion). A single turn can overshoot the ceiling by up to one worker's spend. The in-proc impl (Slice C) has the same limitation (it charges after the model call). Acceptable for v1 (per-turn spend is bounded by `MaxTurns:1` + tier), but the ceiling is *soft-real-time*, not instantaneous — flag in the runbook cost section.
5. **`Scope.Tools` (the enforced allowlist, "NEVER irreversible at leaves") has no field on `CreateSessionRequest`.** agentkit fixes tool policy inside the sandbox/MCP config, not per-`Spawn`. v1 workers are draft-only (no tools) so this is inert, but enforcing a non-empty `Scope.Tools` allowlist through the DinD path is **not wired** — it needs either a new `CreateSessionRequest.Tools` field (a contract-adjacent agentkit addition) or a per-session MCP allowlist injected via `SessionEnv`. Flag before any tool-using worker scope lands.
6. **Fail-loud rehydrate crosses the liftability boundary.** The floor (§7 #4) requires a *rehydrate failure* to become a *Needs-Human ticket*, but `go/runner.go` must not import `orchestrator`. Resolved by a typed sentinel (`agentkit.ErrConversationRehydrate`, Task 4) that the orchestrator side maps to `ResultFailed` (Task 5). Note this only fires on the **snapshot-resume** path; v1's default ephemeral single-turn workers rarely resume, so the floor is mostly latent until stateful/snapshot workers land (deferred) — but it is wired now so it cannot be forgotten.
7. **Composition ownership is unstated in the contract.** `WorkerRuntime.Spawn` receives an *unresolved* `Scope` (`Template` with `{{fragment:ID}}` + `Input`). The contract does not say who resolves it. For the in-proc and DinD impls to stay swappable, **the runtime composes** (both call `orchestrator.Compose` against the pinned board). Documented here so Slice C makes the same choice; if Slice C composes in the manager exchange instead, the two impls diverge and the "swap is config" invariant breaks — reconcile with Slice C.

---

## Self-Review notes

- **Spec coverage:** production `WorkerRuntime` over agentkit DinD (Tasks 1–3) ✓; config + secrets (API key + channel token via env/Secret Manager) (Tasks 6, 9) ✓; single-box compose orchestrator+Postgres+web (Task 8) ✓; fail-loud resume floor §7 #4 (Tasks 4–5) ✓; runbook to run BadCode marketing live behind approval (Task 10) ✓; same-interface config swap (Task 7) ✓; floors enforced pre-provision (Tasks 2–3) ✓.
- **Consumed contract types (verbatim, never redefined):** `orchestrator.{WorkerRuntime, ResultSink, Scope, Result, ResultStatus, Budget, SpendMeter, TicketStore, ModelTier, Compose}`, `agentkit.{Runner, CreateSessionRequest, SendMessageRequest, SessionRef}`, `events.{Envelope, Type, ...}`.
- **Liftability:** the only `go/` (engine) edit is Task 4, which adds an `agentkit`-local sentinel error and returns it — no `orchestrator` import. The orchestrator→engine dependency stays one-directional.
- **Testability:** Tasks 1–7 are unit-tested (fakes for `agentkit.Runner`, `ResultSink`, `SpendMeter`, seeded `MemBoard`); Tasks 8–10 are ops with exact commands + expected observations.
- **Deferred by design (not built here):** snapshot-resume for stateful workers, multi-channel, the event bus/pipelines/memory store — all behind the same seams, no rework.
