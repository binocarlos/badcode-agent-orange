# Slice 0 — The Learning Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the winning narrative — *"over time it learned to stop being dumb and got clever"* — is reachable with the smallest possible offline, deterministic vertical: a manager scope whose behaviour visibly changes after a human note, with every change versioned and pinned so the story is auditable.

**Architecture:** A small Go package `go/orchestrator` on top of the existing `agentdb` board log. Guidance lives in **versioned fragments** (reuse `agentdb.BoardPromptFragment` + `Changeset`); a **Model seam** (mock impl) turns a composed prompt into text; a **Runner** composes fragments+input into a prompt, runs the model, and records each run **pinned to a board revision** (telemetry = "show your work"); a **feedback primitive** turns a `(fragment, note)` pair into a delta-edited fragment as a new revision. The end-to-end test *is* the demo: run → note → re-run → behaviour changed, history tells the story.

**Tech Stack:** Go 1.25, standard library only for Slice 0 (no DB, no containers, no network, no external deps). Reuses `agentdb` plain structs + the `BoardStore` interface.

## Global Constraints

- Module path `github.com/binocarlos/badcode-agent-orange`; package under `go/orchestrator/`. One line.
- `go build ./...` and `go vet ./...` must stay green. One line.
- **No external dependencies** in Slice 0 — stdlib only (deterministic, offline, fast tests). One line.
- **In-memory implementations only** behind the existing seams (no Postgres, no Docker) — Postgres/containers swap in later slices behind the same interfaces. One line.
- **Nothing publishes / no real-world side effects** in Slice 0 — there is no worker runtime and no external connector yet, so the C2 publish-approval floor is **out of scope for this slice** (it lands with the first real worker/connector, before anything can publish). One line.
- TDD: failing test first, minimal impl, frequent commits. One line.
- Revision ids are a **deterministic counter** (`r1`, `r2`, …) in the in-memory board so tests and the demo are reproducible — no uuid/time/random. One line.

---

### Task 1: In-memory BoardStore (versioned fragments by folding the log)

**Files:**
- Create: `go/orchestrator/memboard.go`
- Test: `go/orchestrator/memboard_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardStore`, `agentdb.Board`, `agentdb.Changeset`, `agentdb.Op`, `agentdb.BoardPromptFragment`, `agentdb.OpAdd/OpUpdate/OpRemove`.
- Produces: `func NewMemBoard() *MemBoard` implementing `agentdb.BoardStore`. Revision ids are `"r1"`,`"r2"`,… Folds only the `prompt_fragment` entity type in Slice 0 (other entity types ignored). `Board.Revision` is set to the folded-through revision id.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func fragOp(kind agentdb.OpKind, id, body string) agentdb.Op {
	b, _ := json.Marshal(agentdb.BoardPromptFragment{ID: id, Kind: "role", Body: body})
	return agentdb.Op{Op: kind, EntityType: "prompt_fragment", EntityID: id, Body: b}
}

func TestMemBoardAppendFoldAndPin(t *testing.T) {
	ctx := context.Background()
	b := NewMemBoard()

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestMemBoardAppendFoldAndPin -v`
Expected: FAIL — `undefined: NewMemBoard`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// MemBoard is an in-memory agentdb.BoardStore: an append-only changeset log
// folded on read. Revision ids are a deterministic counter (r1, r2, …) so the
// learning narrative is reproducible. Slice 0 folds only prompt_fragment ops.
type MemBoard struct {
	mu   sync.Mutex
	revs []agentdb.BoardRevision
}

func NewMemBoard() *MemBoard { return &MemBoard{} }

func (m *MemBoard) Append(_ context.Context, cs agentdb.Changeset) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seq := int64(len(m.revs) + 1)
	id := fmt.Sprintf("r%d", seq)
	m.revs = append(m.revs, agentdb.BoardRevision{
		ID: id, ParentID: cs.ParentID, Seq: seq, Status: "applied",
		Author: cs.Author, Message: cs.Message, Ops: toJSONArray(cs.Ops),
	})
	return id, nil
}

func (m *MemBoard) Current(ctx context.Context) (agentdb.Board, error) {
	head, err := m.Head(ctx)
	if err != nil {
		return agentdb.Board{}, err
	}
	return m.AsOf(ctx, head)
}

func (m *MemBoard) AsOf(_ context.Context, revisionID string) (agentdb.Board, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ordered := append([]agentdb.BoardRevision(nil), m.revs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	frags := map[string]agentdb.BoardPromptFragment{}
	var foundTarget bool
	for _, rev := range ordered {
		var ops []agentdb.Op
		_ = json.Unmarshal(jsonArrayBytes(rev.Ops), &ops)
		for _, op := range ops {
			if op.EntityType != "prompt_fragment" {
				continue
			}
			switch op.Op {
			case agentdb.OpAdd, agentdb.OpUpdate:
				var f agentdb.BoardPromptFragment
				if err := json.Unmarshal(op.Body, &f); err != nil {
					return agentdb.Board{}, fmt.Errorf("fold %s: %w", rev.ID, err)
				}
				f.LastChangedIn = rev.ID
				frags[op.EntityID] = f
			case agentdb.OpRemove:
				delete(frags, op.EntityID)
			}
		}
		if rev.ID == revisionID {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		return agentdb.Board{}, fmt.Errorf("revision %q not found", revisionID)
	}
	out := agentdb.Board{Revision: revisionID}
	for _, f := range frags {
		out.Fragments = append(out.Fragments, f)
	}
	sort.Slice(out.Fragments, func(i, j int) bool { return out.Fragments[i].ID < out.Fragments[j].ID })
	return out, nil
}

func (m *MemBoard) Head(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.revs) == 0 {
		return "", fmt.Errorf("board empty")
	}
	return m.revs[len(m.revs)-1].ID, nil
}
```

Note: `toJSONArray` / `jsonArrayBytes` adapt `[]Op` ↔ the `agentdb.JSONArray` column type; define them in this file using `json.Marshal`/`json.RawMessage` per the `agentdb` JSON helpers. If `agentdb.JSONArray` is `[]byte`-backed, `toJSONArray(ops)` = marshal to bytes; `jsonArrayBytes(j)` = the underlying bytes. Verify the concrete type in `agentdb` and match it exactly.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestMemBoardAppendFoldAndPin -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/memboard.go go/orchestrator/memboard_test.go
git commit -m "feat(orchestrator): in-memory versioned-fragment BoardStore (fold + pin)"
```

---

### Task 2: Fragment composition

**Files:**
- Create: `go/orchestrator/compose.go`
- Test: `go/orchestrator/compose_test.go`

**Interfaces:**
- Consumes: `agentdb.Board`.
- Produces: `func Compose(board agentdb.Board, template, input string) (string, error)` — replaces every `{{fragment:ID}}` with the fragment body (error if the id is absent) and `{{input}}` with `input`.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestComposeResolvesFragmentsAndInput(t *testing.T) {
	board := agentdb.Board{Revision: "r1", Fragments: []agentdb.BoardPromptFragment{
		{ID: "routing-guidance", Body: "Be clever."},
	}}
	out, err := Compose(board, "{{fragment:routing-guidance}}\nGoal: {{input}}", "grow the brand")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if out != "Be clever.\nGoal: grow the brand" {
		t.Fatalf("composed = %q", out)
	}

	if _, err := Compose(board, "{{fragment:missing}}", "x"); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-fragment error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestComposeResolvesFragmentsAndInput -v`
Expected: FAIL — `undefined: Compose`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

var fragRefRe = regexp.MustCompile(`\{\{fragment:([a-zA-Z0-9_-]+)\}\}`)

// Compose resolves {{fragment:ID}} refs against the board and {{input}} against
// input. An unknown fragment id is an error (never silently empty).
func Compose(board agentdb.Board, template, input string) (string, error) {
	bodies := map[string]string{}
	for _, f := range board.Fragments {
		bodies[f.ID] = f.Body
	}
	var missing string
	out := fragRefRe.ReplaceAllStringFunc(template, func(m string) string {
		id := fragRefRe.FindStringSubmatch(m)[1]
		body, ok := bodies[id]
		if !ok {
			missing = id
			return m
		}
		return body
	})
	if missing != "" {
		return "", fmt.Errorf("compose: unknown fragment %q", missing)
	}
	return strings.ReplaceAll(out, "{{input}}", input), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestComposeResolvesFragmentsAndInput -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/compose.go go/orchestrator/compose_test.go
git commit -m "feat(orchestrator): fragment+input prompt composition"
```

---

### Task 3: Model seam + scripted mock

**Files:**
- Create: `go/orchestrator/model.go`
- Test: `go/orchestrator/model_test.go`

**Interfaces:**
- Produces: `type Model interface { Run(ctx context.Context, prompt string) (string, error) }`; `type ScriptedModel struct { Rules []Rule }` where `Rule{Contains string; Reply string}` — first rule whose `Contains` is a substring of the prompt wins; `Default` reply if none match. Deterministic, offline.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"
)

func TestScriptedModelMatchesFirstRule(t *testing.T) {
	m := &ScriptedModel{
		Default: "dumb plan",
		Rules:   []Rule{{Contains: "clever", Reply: "clever plan"}},
	}
	got, _ := m.Run(context.Background(), "guidance: Be clever.\nGoal: x")
	if got != "clever plan" {
		t.Fatalf("got %q, want clever plan", got)
	}
	got, _ = m.Run(context.Background(), "guidance: Be basic.\nGoal: x")
	if got != "dumb plan" {
		t.Fatalf("got %q, want dumb plan", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestScriptedModelMatchesFirstRule -v`
Expected: FAIL — `undefined: ScriptedModel`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"strings"
)

// Model turns a composed prompt into text. The real impl (later slice) wraps the
// Claude Agent SDK harness; Slice 0 uses ScriptedModel for deterministic tests.
type Model interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// Rule fires Reply when Contains is a substring of the prompt.
type Rule struct {
	Contains string
	Reply    string
}

// ScriptedModel is a deterministic offline Model: first matching rule wins,
// else Default. It lets a test prove behaviour changed because the composed
// prompt (a fragment) changed.
type ScriptedModel struct {
	Rules   []Rule
	Default string
}

func (s *ScriptedModel) Run(_ context.Context, prompt string) (string, error) {
	for _, r := range s.Rules {
		if strings.Contains(prompt, r.Contains) {
			return r.Reply, nil
		}
	}
	return s.Default, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestScriptedModelMatchesFirstRule -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/model.go go/orchestrator/model_test.go
git commit -m "feat(orchestrator): Model seam + deterministic ScriptedModel"
```

---

### Task 4: Telemetry (append-only run log)

**Files:**
- Create: `go/orchestrator/telemetry.go`
- Test: `go/orchestrator/telemetry_test.go`

**Interfaces:**
- Produces: `type Run struct { ID, Scope, BoardRevision, Prompt, Output string; Seq int }`; `type Telemetry struct{...}`; `func NewTelemetry() *Telemetry`; `(*Telemetry) Record(Run) Run` (assigns `Seq` = 1-based order, `ID` = `"run%d"`); `(*Telemetry) Runs() []Run` (copy, in order).

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import "testing"

func TestTelemetryRecordsInOrder(t *testing.T) {
	tel := NewTelemetry()
	a := tel.Record(Run{Scope: "manager", BoardRevision: "r1", Output: "dumb plan"})
	b := tel.Record(Run{Scope: "manager", BoardRevision: "r2", Output: "clever plan"})
	if a.ID != "run1" || a.Seq != 1 || b.ID != "run2" || b.Seq != 2 {
		t.Fatalf("ids/seq wrong: %+v %+v", a, b)
	}
	runs := tel.Runs()
	if len(runs) != 2 || runs[0].BoardRevision != "r1" || runs[1].BoardRevision != "r2" {
		t.Fatalf("runs wrong: %+v", runs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestTelemetryRecordsInOrder -v`
Expected: FAIL — `undefined: NewTelemetry`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"fmt"
	"sync"
)

// Run is one scope execution, pinned to the board revision it ran against —
// the "show your work" record the narrative is told from.
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

func NewTelemetry() *Telemetry { return &Telemetry{} }

func (t *Telemetry) Record(r Run) Run {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.Seq = len(t.runs) + 1
	r.ID = fmt.Sprintf("run%d", r.Seq)
	t.runs = append(t.runs, r)
	return r
}

func (t *Telemetry) Runs() []Run {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Run(nil), t.runs...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestTelemetryRecordsInOrder -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/telemetry.go go/orchestrator/telemetry_test.go
git commit -m "feat(orchestrator): append-only telemetry run log"
```

---

### Task 5: Runner (compose → model → pinned telemetry)

**Files:**
- Create: `go/orchestrator/runner.go`
- Test: `go/orchestrator/runner_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardStore`, `Model`, `*Telemetry`, `Compose`.
- Produces: `type Scope struct { Name, Template, Input string }`; `type Runner struct { Board agentdb.BoardStore; Model Model; Telemetry *Telemetry }`; `func (r *Runner) RunScope(ctx, Scope) (Run, error)` — folds Current board, composes, runs model, records a Run pinned to `board.Revision`, returns it.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestRunnerRunsScopePinnedToHead(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	rev, _ := board.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be clever.")}})

	r := &Runner{
		Board:     board,
		Model:     &ScriptedModel{Default: "dumb plan", Rules: []Rule{{Contains: "clever", Reply: "clever plan"}}},
		Telemetry: NewTelemetry(),
	}
	run, err := r.RunScope(ctx, Scope{
		Name:     "manager",
		Template: "{{fragment:routing-guidance}}\nGoal: {{input}}",
		Input:    "grow the brand",
	})
	if err != nil {
		t.Fatalf("runscope: %v", err)
	}
	if run.Output != "clever plan" {
		t.Fatalf("output = %q, want clever plan", run.Output)
	}
	if run.BoardRevision != rev {
		t.Fatalf("pinned to %q, want %q", run.BoardRevision, rev)
	}
	if len(r.Telemetry.Runs()) != 1 {
		t.Fatalf("expected 1 recorded run")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestRunnerRunsScopePinnedToHead -v`
Expected: FAIL — `undefined: Runner`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"fmt"
)

// Scope is one manager/worker invocation: a named prompt template + its input.
type Scope struct {
	Name     string
	Template string
	Input    string
}

// Runner composes a scope's prompt from the current board, runs the model, and
// records the run pinned to the board revision it ran against.
type Runner struct {
	Board     agentdb.BoardStore
	Model     Model
	Telemetry *Telemetry
}

func (r *Runner) RunScope(ctx context.Context, s Scope) (Run, error) {
	board, err := r.Board.Current(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("runscope %s: current board: %w", s.Name, err)
	}
	prompt, err := Compose(board, s.Template, s.Input)
	if err != nil {
		return Run{}, fmt.Errorf("runscope %s: %w", s.Name, err)
	}
	out, err := r.Model.Run(ctx, prompt)
	if err != nil {
		return Run{}, fmt.Errorf("runscope %s: model: %w", s.Name, err)
	}
	return r.Telemetry.Record(Run{
		Scope: s.Name, BoardRevision: board.Revision, Prompt: prompt, Output: out,
	}), nil
}
```

Add the `agentdb` import to the file header.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestRunnerRunsScopePinnedToHead -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/runner.go go/orchestrator/runner_test.go
git commit -m "feat(orchestrator): Runner — compose, run model, pin to revision"
```

---

### Task 6: Human-feedback primitive (note → delta fragment edit)

**Files:**
- Create: `go/orchestrator/feedback.go`
- Test: `go/orchestrator/feedback_test.go`

**Interfaces:**
- Consumes: `agentdb.BoardStore`, `Model`, `agentdb.Board`, `agentdb.BoardPromptFragment`.
- Produces: `const MaxFragmentLen = 4000`; `func ApplyFeedback(ctx, board agentdb.BoardStore, reviser Model, fragmentID, note string) (revisionID string, err error)`. Reads Current; finds `fragmentID` (error if absent); composes a reviser prompt from the *current body* + the *note*; runs the reviser; **guards**: revised body must be non-empty and ≤ `MaxFragmentLen` (else error — never wipe/overrun); appends an `OpUpdate prompt_fragment` changeset (author `"human-feedback"`, message = the note) and returns the new revision id.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestApplyFeedbackWritesDeltaRevision(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	_, _ = board.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be basic.")}})

	// Reviser mock: when the note mentions "clever", append the clever steer.
	reviser := &ScriptedModel{Default: "Be basic.", Rules: []Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."},
	}}
	rev, err := ApplyFeedback(ctx, board, reviser, "routing-guidance", "make it more clever")
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if rev != "r2" {
		t.Fatalf("rev = %q, want r2", rev)
	}
	cur, _ := board.Current(ctx)
	if !strings.Contains(cur.Fragments[0].Body, "clever") {
		t.Fatalf("body not revised: %q", cur.Fragments[0].Body)
	}

	// Guard: a reviser that returns empty must not wipe the fragment.
	empty := &ScriptedModel{Default: ""}
	if _, err := ApplyFeedback(ctx, board, empty, "routing-guidance", "x"); err == nil {
		t.Fatalf("expected error on empty revision")
	}
	// Guard: unknown fragment id errors.
	if _, err := ApplyFeedback(ctx, board, reviser, "nope", "x"); err == nil {
		t.Fatalf("expected error on unknown fragment")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestApplyFeedbackWritesDeltaRevision -v`
Expected: FAIL — `undefined: ApplyFeedback`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// MaxFragmentLen caps a revised fragment body (coherence guard against runaway
// rewrites — the ACE "context collapse" risk).
const MaxFragmentLen = 4000

const reviserTemplate = `You maintain a short guidance note. Here is the current note:
---
%s
---
A reviewer left this note: %q
Return the full revised guidance (a small delta of the current note, not a rewrite).`

// ApplyFeedback turns a (fragment, note) pair into a delta-edited fragment as a
// new board revision. It is the human-feedback half of the learning loop; the
// Consultant is the same write with the trigger/input swapped.
func ApplyFeedback(ctx context.Context, board agentdb.BoardStore, reviser Model, fragmentID, note string) (string, error) {
	cur, err := board.Current(ctx)
	if err != nil {
		return "", err
	}
	var current string
	var found bool
	for _, f := range cur.Fragments {
		if f.ID == fragmentID {
			current, found = f.Body, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("feedback: unknown fragment %q", fragmentID)
	}

	revised, err := reviser.Run(ctx, fmt.Sprintf(reviserTemplate, current, note))
	if err != nil {
		return "", fmt.Errorf("feedback: reviser: %w", err)
	}
	if revised == "" {
		return "", fmt.Errorf("feedback: reviser returned empty body (refusing to wipe %q)", fragmentID)
	}
	if len(revised) > MaxFragmentLen {
		return "", fmt.Errorf("feedback: revised body %d > MaxFragmentLen %d", len(revised), MaxFragmentLen)
	}

	body, err := json.Marshal(agentdb.BoardPromptFragment{ID: fragmentID, Kind: "role", Body: revised})
	if err != nil {
		return "", err
	}
	return board.Append(ctx, agentdb.Changeset{
		Author:  "human-feedback",
		Message: note,
		Ops:     []agentdb.Op{{Op: agentdb.OpUpdate, EntityType: "prompt_fragment", EntityID: fragmentID, Body: body}},
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestApplyFeedbackWritesDeltaRevision -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/feedback.go go/orchestrator/feedback_test.go
git commit -m "feat(orchestrator): human-feedback primitive (delta fragment edit, guarded)"
```

---

### Task 7: The narrative end-to-end test (the demo, as a test)

**Files:**
- Create: `go/orchestrator/narrative_test.go`

**Interfaces:**
- Consumes: everything above. No new production code — this task asserts the *story*: run → note → re-run → behaviour changed, and the history is auditable.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// TestLearningNarrative is the winning demo as an automated test: the manager is
// dumb, a human leaves a note, and the manager is then clever — with every step
// versioned and pinned so the story is auditable.
func TestLearningNarrative(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	seed, _ := board.Append(ctx, agentdb.Changeset{Author: "human", Message: "seed guidance",
		Ops: []agentdb.Op{fragOp(agentdb.OpAdd, "routing-guidance", "Be basic.")}})

	manager := &Runner{
		Board:     board,
		Telemetry: NewTelemetry(),
		Model: &ScriptedModel{Default: "dumb plan", Rules: []Rule{
			{Contains: "clever", Reply: "clever plan"},
		}},
	}
	scope := Scope{Name: "manager", Template: "{{fragment:routing-guidance}}\nGoal: {{input}}", Input: "grow the brand"}

	before, err := manager.RunScope(ctx, scope)
	if err != nil {
		t.Fatalf("before: %v", err)
	}

	reviser := &ScriptedModel{Default: "Be basic.", Rules: []Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."},
	}}
	noteRev, err := ApplyFeedback(ctx, board, reviser, "routing-guidance", "stop being dumb, be more clever")
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}

	after, err := manager.RunScope(ctx, scope)
	if err != nil {
		t.Fatalf("after: %v", err)
	}

	// The behaviour changed.
	if before.Output != "dumb plan" || after.Output != "clever plan" {
		t.Fatalf("no learning: before=%q after=%q", before.Output, after.Output)
	}
	// Each run is pinned to a different board revision (the cause is auditable).
	if before.BoardRevision != seed || after.BoardRevision != noteRev || seed == noteRev {
		t.Fatalf("pins wrong: before=%s after=%s seed=%s note=%s", before.BoardRevision, after.BoardRevision, seed, noteRev)
	}
	// AsOf folds the exact guidance each run saw — the before/after is reproducible.
	pre, _ := board.AsOf(ctx, seed)
	post, _ := board.AsOf(ctx, noteRev)
	if pre.Fragments[0].Body != "Be basic." || post.Fragments[0].Body == pre.Fragments[0].Body {
		t.Fatalf("history not auditable: pre=%q post=%q", pre.Fragments[0].Body, post.Fragments[0].Body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails / passes**

Run: `cd go && go test ./orchestrator/ -run TestLearningNarrative -v`
Expected: PASS (all dependencies already implemented). If it fails, fix the implementation, not the test.

- [ ] **Step 3: Commit**

```bash
git add go/orchestrator/narrative_test.go
git commit -m "test(orchestrator): end-to-end learning narrative (the demo as a test)"
```

---

### Task 8: Human-watchable demo command

**Files:**
- Create: `go/examples/learningloop/main.go`

**Interfaces:**
- Consumes: the `orchestrator` package. Produces a `main` that runs the narrative and prints the before/after outputs + the board revision log (`Message` per revision) so a human can *see* the story.

- [ ] **Step 1: Write the implementation**

```go
// Command learningloop runs the Slice-0 learning narrative and prints the
// before/after + the versioned story. Offline, deterministic, no real side effects.
package main

import (
	"context"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func main() {
	ctx := context.Background()
	board := orchestrator.NewMemBoard()
	mustAppend(board.Append(ctx, seed()))

	mgr := &orchestrator.Runner{
		Board:     board,
		Telemetry: orchestrator.NewTelemetry(),
		Model: &orchestrator.ScriptedModel{Default: "dumb plan", Rules: []orchestrator.Rule{
			{Contains: "clever", Reply: "clever plan"},
		}},
	}
	scope := orchestrator.Scope{Name: "manager", Template: "{{fragment:routing-guidance}}\nGoal: {{input}}", Input: "grow the brand"}

	before, _ := mgr.RunScope(ctx, scope)
	fmt.Printf("BEFORE (pinned %s): %q\n", before.BoardRevision, before.Output)

	reviser := &orchestrator.ScriptedModel{Default: "Be basic.", Rules: []orchestrator.Rule{
		{Contains: "clever", Reply: "Be basic. Also: be clever and witty."},
	}}
	mustString(orchestrator.ApplyFeedback(ctx, board, reviser, "routing-guidance", "stop being dumb, be more clever"))

	after, _ := mgr.RunScope(ctx, scope)
	fmt.Printf("AFTER  (pinned %s): %q\n", after.BoardRevision, after.Output)

	fmt.Println("\n--- the story (board revision log) ---")
	cur, _ := board.Current(ctx)
	for _, f := range cur.Fragments {
		fmt.Printf("fragment %q now: %q (last changed in %s)\n", f.ID, f.Body, f.LastChangedIn)
	}
}

func seed() agentdb.Changeset {
	b, _ := agentdb.BoardPromptFragment{ID: "routing-guidance", Kind: "role", Body: "Be basic."}, error(nil)
	_ = b
	return orchestrator.SeedFragment("routing-guidance", "Be basic.")
}

func mustAppend(_ string, err error) {
	if err != nil {
		panic(err)
	}
}
func mustString(_ string, err error) {
	if err != nil {
		panic(err)
	}
}
```

Note: add a small exported helper `orchestrator.SeedFragment(id, body string) agentdb.Changeset` (in `compose.go` or a new `seed.go`) that builds an `OpAdd prompt_fragment` changeset, so the example and tests don't duplicate the marshal boilerplate. Update Task 1's test helper `fragOp` to call it if convenient (optional). Keep the example compiling cleanly — drop the dead `seed()` scaffolding above and just call `orchestrator.SeedFragment` directly.

- [ ] **Step 2: Run it**

Run: `cd go && go run ./examples/learningloop`
Expected output:
```
BEFORE (pinned r1): "dumb plan"
AFTER  (pinned r2): "clever plan"

--- the story (board revision log) ---
fragment "routing-guidance" now: "Be basic. Also: be clever and witty." (last changed in r2)
```

- [ ] **Step 3: Verify the whole package + commit**

Run: `cd go && go build ./... && go vet ./orchestrator/... && go test ./orchestrator/...`
Expected: all PASS.

```bash
git add go/examples/learningloop/main.go go/orchestrator/seed.go
git commit -m "feat(examples): learningloop demo — before/after + the versioned story"
```

---

## Self-Review notes

- **Spec coverage:** seed fragments (Task 1/8) ✓; versioned/pinned guidance (Task 1) ✓; compose worker prompt (Task 2) ✓; run a scope against a model seam (Task 3/5) ✓; telemetry "show your work" (Task 4) ✓; human-feedback → delta `write_fragment` with coherence guards (Task 6) ✓; the auditable learning narrative (Task 7/8) ✓. **Deferred by design (later slices, noted in Global Constraints):** worker runtime/containers, event bus + `thread.finished` driver, pipelines, the autonomous Consultant, the publish-approval floor (no publishing exists yet), Postgres BoardStore.
- **Placeholder scan:** the only soft spot is Task 8's `SeedFragment` helper + dropping the dead `seed()` scaffold — implement `SeedFragment` as a 3-line marshal helper and call it directly.
- **Type consistency:** `agentdb.BoardPromptFragment`, `agentdb.Op{Op,EntityType,EntityID,Body}`, `agentdb.Changeset{ParentID,Author,Message,Ops}`, `Run{ID,Scope,BoardRevision,Prompt,Output,Seq}`, `Scope{Name,Template,Input}`, `Model.Run(ctx,string)(string,error)` are used identically across tasks. Verify `agentdb.JSONArray` concrete type in Task 1 and match `toJSONArray`/`jsonArrayBytes` to it exactly.
