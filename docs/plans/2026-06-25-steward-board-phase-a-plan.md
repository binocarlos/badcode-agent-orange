# Steward & Board — Phase A Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make agentkit able to hand a *mandate* to a worker session and have that worker orient, work, and report back through a durable Postgres **board** — driven by hand (no dispatch loop, no steward yet).

**Architecture:** The board (Postgres) is the single source of truth. Phase A builds the smallest vertical slice that proves the board↔worker contract: a per-session `Env` injection seam in the engine, a `board` Go package (models + store) reusing agentkit's GORM/sqlite patterns, a token-authed board HTTP API, env-gated board tools in the sandbox, and a worker protocol prompt. You post a mandate and launch a session by hand; the worker calls `mandate_show` → works → `mandate_complete` (or `refer_up`); the board reflects it. No dispatch loop (Phase B), no steward (Phase D).

**Tech Stack:** Go 1.25 (engine + board host, `net/http.ServeMux`, GORM over Postgres/sqlite, `golang-jwt`), TypeScript (sandbox in-image agent, `@anthropic-ai/claude-agent-sdk`, zod, vitest).

**Design source:** `docs/specs/2026-06-25-steward-board-orchestration-design.md` (this repo). Phase A = §6.2 Phase A.

## Global Constraints

- **Go floor is 1.25** — keep `go build ./...` and `go vet ./...` green at all times.
- **Module path is `github.com/binocarlos/badcode-agent-orange`** (rooted at `go/`). Never reintroduce `bayes-price/agentkit` or any Platinum coupling.
- **Liftability invariant (CI-enforced):** engine packages (`agentkit`, `runner`, `execenv`, `agentdb`, `events`, `imageregistry`, …) must import **nothing** from the new orchestrator/board code. The board depends on the engine, never the reverse.
- **Phase-A home decision (open question §6.3 #5, resolved for now):** the board package lives at `go/orchestrator/board/` and the host binary at `go/cmd/orchestratord/`, **inside the existing Go module**, reusing the engine as a library. It may later extract to its own module/repo — keep it self-contained to ease that.
- **Canon vocabulary all the way to the API** (master spec §4): `mandate`, `the board`, `engage`, `the steward`, `shift`/`call`, `refer up`/`consent`, `the keys`, `the floor`, `attest`, `budget`, `overreach`, `the owner`. Tool names use this register (`mandate_show`, `refer_up`, …).
- **Auth: Anthropic API key, not a Max subscription.**
- **Tests must be hermetic** — no real Postgres/Docker in unit tests. Go store tests use temp-file sqlite + `AutoMigrate` (the agentdb test convention); sandbox tests use vitest with `fetch` stubbed.
- **CI gates** (`.github/workflows/ci.yml`): Go = `go build ./...`, `go test ./... -count=1`, `go vet ./...`; sandbox = `yarn typecheck`, `yarn test`. No lint/format gate.

---

## File Structure

**Engine (modified):**
- `go/agentkit.go` — add `Env map[string]string` to `CreateSessionRequest`.
- `go/runner.go` — thread `req.Env` through `sessionEnv(...)` into the provision env map.
- `go/runner_env_test.go` (create) — white-box test for env merge precedence.

**Board package (new) — `go/orchestrator/board/`:**
- `models.go` — `Mandate`, `MandateRun` GORM structs (+ status/outcome constants).
- `store.go` — `Store` over GORM: `Open`, `CreateMandate`, `GetMandate`, `SetStatus`, `OpenRun`, `Heartbeat`, `CompleteRun`, `ReferUp`.
- `store_test.go` — sqlite-backed unit tests for every store method.

**Board HTTP API (new) — `go/orchestrator/boardapi/`:**
- `handlers.go` — `Handlers` over `board.Store` + an `Identity` func; `Mux()` + per-endpoint handlers.
- `handlers_test.go` — httptest tests with a stub store.

**Board host (new) — `go/cmd/orchestratord/`:**
- `main.go` — wire Runner + agentdb + board store + mount `httpapi.Mux()` and `boardapi.Mux()` under JWT auth; a `--post-mandate` dev flag to hand-create a mandate and launch its worker session.
- `auth.go` — JWT middleware (mirrors `go/cmd/agentd/auth.go`).

**Sandbox (modified) — `sandbox/src/`:**
- `tools/registry.ts` — add optional `requiresEnv?: string` to `ToolPlugin`.
- `tools/registry-impl.ts` — filter plugins whose `requiresEnv` env var is unset in `resolve()`.
- `tools/registry-impl.test.ts` (create) — vitest for env-gating.
- `tools/board/client.ts` (create) — tiny authed board-API client (`HOST_API_URL` + `SESSION_TOKEN`).
- `tools/board/index.ts` (create) — the board tool plugins (`mandate_show`, `mandate_heartbeat`, `mandate_complete`, `refer_up`, `mandate_create`).
- `tools/board/board.test.ts` (create) — vitest with `fetch` stubbed.
- `prompt/worker-protocol.ts` (create) — the `WORKER_PROTOCOL` system-prompt constant.

---

## Task 1: Engine — per-session `Env` on `CreateSessionRequest`

Adds the seam that lets the board host inject `AO_MANDATE` and the key-gate env vars into a worker session, without touching the host-wide `Policy.SessionEnv`.

**Files:**
- Modify: `go/agentkit.go` (the `CreateSessionRequest` struct)
- Modify: `go/runner.go` (`sessionEnv` + its one call site in `CreateSession`)
- Test: `go/runner_env_test.go` (create)

**Interfaces:**
- Produces: `CreateSessionRequest.Env map[string]string` — per-session env, merged into the container env *under* the hard per-session keys (`SESSION_ID`, `SESSION_TOKEN`, `ANTHROPIC_API_KEY`, `DEFAULT_MODEL`), *over* `Policy.SessionEnv`.
- Produces: `(*runnerImpl).sessionEnv(sessionID, token, model string, extra map[string]string) map[string]string`.

- [ ] **Step 1: Write the failing test**

Create `go/runner_env_test.go`:

```go
package agentkit

import "testing"

func TestSessionEnvMergePrecedence(t *testing.T) {
	r := &runnerImpl{deps: Deps{Policy: Policy{SessionEnv: map[string]string{
		"ANTHROPIC_BASE_URL": "http://proxy",
		"SHARED":             "from-policy",
	}}}}

	env := r.sessionEnv("sess-1", "tok-1", "claude-opus-4-5", map[string]string{
		"AO_MANDATE": "m-123",
		"SHARED":     "from-extra", // extra overrides policy
		"SESSION_ID": "HIJACK",     // hard key must win over extra
	})

	if env["ANTHROPIC_BASE_URL"] != "http://proxy" {
		t.Fatalf("policy env dropped: %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["AO_MANDATE"] != "m-123" {
		t.Fatalf("extra env not injected: %q", env["AO_MANDATE"])
	}
	if env["SHARED"] != "from-extra" {
		t.Fatalf("extra should override policy: %q", env["SHARED"])
	}
	if env["SESSION_ID"] != "sess-1" {
		t.Fatalf("hard key must win over extra: %q", env["SESSION_ID"])
	}
	if env["SESSION_TOKEN"] != "tok-1" || env["ANTHROPIC_API_KEY"] != "tok-1" {
		t.Fatalf("token keys wrong: %q / %q", env["SESSION_TOKEN"], env["ANTHROPIC_API_KEY"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test . -run TestSessionEnvMergePrecedence -count=1`
Expected: FAIL — compile error (`sessionEnv` takes 3 args, not 4) or wrong results.

- [ ] **Step 3: Add the `Env` field to `CreateSessionRequest`**

In `go/agentkit.go`, inside `type CreateSessionRequest struct { ... }`, add after `Harness Harness`:

```go
	// Env is a per-session set of environment variables injected into this
	// session's container, merged over Policy.SessionEnv but under the hard
	// per-session keys (SESSION_ID/SESSION_TOKEN/ANTHROPIC_API_KEY/DEFAULT_MODEL).
	// The board host uses this to pass AO_MANDATE and the key-gate flags that
	// decide which tools appear in the worker's schema.
	Env map[string]string
```

- [ ] **Step 4: Thread `extra` through `sessionEnv`**

In `go/runner.go`, change the `sessionEnv` signature and add the merge loop. Replace:

```go
func (r *runnerImpl) sessionEnv(sessionID, token, model string) map[string]string {
	env := make(map[string]string, len(r.deps.Policy.SessionEnv)+3)
	for k, v := range r.deps.Policy.SessionEnv {
		env[k] = v
	}
	env["SESSION_ID"] = sessionID
```

with:

```go
func (r *runnerImpl) sessionEnv(sessionID, token, model string, extra map[string]string) map[string]string {
	env := make(map[string]string, len(r.deps.Policy.SessionEnv)+len(extra)+4)
	for k, v := range r.deps.Policy.SessionEnv {
		env[k] = v
	}
	// Per-session extras (e.g. AO_MANDATE, key-gate flags) override policy env,
	// but the hard keys below still win over extras.
	for k, v := range extra {
		env[k] = v
	}
	env["SESSION_ID"] = sessionID
```

- [ ] **Step 5: Update the call site in `CreateSession`**

In `go/runner.go`, in `CreateSession`, replace:

```go
	inst, err := r.provisionOnWorker(ctx, req.SessionID, img, worker,
		r.sessionEnv(req.SessionID, token, req.Model))
```

with:

```go
	inst, err := r.provisionOnWorker(ctx, req.SessionID, img, worker,
		r.sessionEnv(req.SessionID, token, req.Model, req.Env))
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd go && go test . -run TestSessionEnvMergePrecedence -count=1`
Expected: PASS.

- [ ] **Step 7: Build + vet + full test**

Run: `cd go && go build ./... && go vet ./... && go test ./... -count=1`
Expected: all green (no other call sites of `sessionEnv` — it's private to `runner.go`).

- [ ] **Step 8: Commit**

```bash
git add go/agentkit.go go/runner.go go/runner_env_test.go
git commit -m "feat(engine): per-session Env on CreateSessionRequest"
```

---

## Task 2: Board package — models + store

The durable heart. Mirrors agentkit's `agentdb` GORM conventions; tested on temp-file sqlite via `AutoMigrate` (the agentdb test pattern).

**Files:**
- Create: `go/orchestrator/board/models.go`
- Create: `go/orchestrator/board/store.go`
- Test: `go/orchestrator/board/store_test.go`

**Interfaces:**
- Produces: `Mandate` (status enum: `triage|ready|claimed|working|attesting|needs-consent|done|failed|abandoned`), `MandateRun` (outcome enum: `completed|needs_consent|crashed|timed_out|reclaimed|failed|overreach`).
- Produces: `Store` with `Open(dsn string) (*Store, error)`, `CreateMandate(ctx, *Mandate) (*Mandate, error)`, `GetMandate(ctx, id) (*Mandate, error)`, `SetStatus(ctx, id, status string) error`, `OpenRun(ctx, mandateID, sessionID string) (*MandateRun, error)`, `Heartbeat(ctx, mandateID string) error`, `CompleteRun(ctx, mandateID, summary string, result JSONMap) error`, `ReferUp(ctx, mandateID, reason string) error`.

- [ ] **Step 1: Write the failing test**

Create `go/orchestrator/board/store_test.go`:

```go
package board

import (
	"context"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "board_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Mandate{}, &MandateRun{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return &Store{gdb: db}
}

func TestCreateAndGetMandate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m, err := s.CreateMandate(ctx, &Mandate{
		ID: "m-1", Customer: "badcode", Job: "marketing",
		Title: "Draft launch posts", Brief: "Draft 3 posts about the Camping comic.",
		Model: "mid", Status: StatusReady,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetMandate(ctx, m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Draft launch posts" || got.Status != StatusReady {
		t.Fatalf("round-trip wrong: %+v", got)
	}
}

func TestRunLifecycle_Complete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateMandate(ctx, &Mandate{ID: "m-2", Status: StatusReady}); err != nil {
		t.Fatalf("create: %v", err)
	}
	run, err := s.OpenRun(ctx, "m-2", "sess-2")
	if err != nil {
		t.Fatalf("open run: %v", err)
	}
	if run.SessionID != "sess-2" || run.Outcome != "" {
		t.Fatalf("run wrong: %+v", run)
	}
	m, _ := s.GetMandate(ctx, "m-2")
	if m.Status != StatusWorking || m.CurrentRunID != run.ID {
		t.Fatalf("mandate not working: %+v", m)
	}
	if err := s.Heartbeat(ctx, "m-2"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := s.CompleteRun(ctx, "m-2", "posted 3 drafts", JSONMap{"files": "3"}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	m, _ = s.GetMandate(ctx, "m-2")
	if m.Status != StatusAttesting {
		t.Fatalf("want attesting, got %q", m.Status)
	}
}

func TestReferUp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateMandate(ctx, &Mandate{ID: "m-3", Status: StatusReady}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.OpenRun(ctx, "m-3", "sess-3"); err != nil {
		t.Fatalf("open run: %v", err)
	}
	if err := s.ReferUp(ctx, "m-3", "need the @badcode password"); err != nil {
		t.Fatalf("refer up: %v", err)
	}
	m, _ := s.GetMandate(ctx, "m-3")
	if m.Status != StatusNeedsConsent || m.BlockedReason != "need the @badcode password" {
		t.Fatalf("refer-up not recorded: %+v", m)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/board/ -count=1`
Expected: FAIL — package `board` does not exist yet.

- [ ] **Step 3: Write `models.go`**

Create `go/orchestrator/board/models.go`:

```go
// Package board is the durable Kanban-style coordination surface (the board)
// for the Agent Orange orchestration layer: mandates (the plan) and their runs.
// It reuses agentkit's GORM conventions but is a host concern — the engine never
// imports it (liftability invariant).
package board

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Mandate statuses (the state machine; see the design spec §2).
const (
	StatusTriage       = "triage"
	StatusReady        = "ready"
	StatusClaimed      = "claimed"
	StatusWorking      = "working"
	StatusAttesting    = "attesting"
	StatusNeedsConsent = "needs-consent"
	StatusDone         = "done"
	StatusFailed       = "failed"
	StatusAbandoned    = "abandoned"
)

// Run outcomes.
const (
	OutcomeCompleted   = "completed"
	OutcomeNeedsConsent = "needs_consent"
	OutcomeCrashed     = "crashed"
	OutcomeTimedOut    = "timed_out"
	OutcomeReclaimed   = "reclaimed"
	OutcomeFailed      = "failed"
	OutcomeOverreach   = "overreach"
)

// JSONMap is a string→any map persisted as jsonb (Postgres) / text (sqlite).
type JSONMap map[string]any

func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	return json.Marshal(m)
}

func (m *JSONMap) Scan(src any) error {
	if src == nil {
		*m = JSONMap{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("board: cannot scan %T into JSONMap", src)
	}
	return json.Unmarshal(b, m)
}

// Mandate is a unit of work plus its scope (the spec's "mandate"). It carries
// its own keys/model/budget — there are no personas to look up.
type Mandate struct {
	ID            string  `json:"id" gorm:"primaryKey;type:varchar(36)"`
	CreatedAt     int64   `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt     int64   `json:"updated_at" gorm:"autoUpdateTime"`
	Customer      string  `json:"customer" gorm:"type:varchar(255);index:idx_mandates_customer"`
	Job           string  `json:"job" gorm:"type:varchar(255);default:''"`
	Title         string  `json:"title" gorm:"type:varchar(255);default:''"`
	Brief         string  `json:"brief" gorm:"type:text;default:''"`
	Keys          JSONMap `json:"keys" gorm:"type:jsonb;default:'{}'"`
	Model         string  `json:"model" gorm:"type:varchar(50);default:''"`
	Image         string  `json:"image" gorm:"type:varchar(512);default:''"`
	CustomImageID string  `json:"custom_image_id" gorm:"type:text;default:''"`
	Budget        JSONMap `json:"budget" gorm:"type:jsonb;default:'{}'"`
	Lane          string  `json:"lane" gorm:"type:varchar(100);default:''"`
	Priority      int     `json:"priority" gorm:"default:0"`
	Depth         int     `json:"depth" gorm:"default:0"`
	Attest        string  `json:"attest" gorm:"type:varchar(20);default:'auto'"`
	Status        string  `json:"status" gorm:"type:varchar(30);default:'triage';index:idx_mandates_status"`
	ClaimLock     string  `json:"claim_lock" gorm:"type:varchar(255);default:''"`
	ClaimExpires  int64   `json:"claim_expires" gorm:"default:0"`
	SessionID     string  `json:"session_id" gorm:"type:varchar(36);default:''"`
	CurrentRunID  string  `json:"current_run_id" gorm:"type:varchar(36);default:''"`
	Attempts      int     `json:"attempts" gorm:"default:0"`
	MaxRetries    int     `json:"max_retries" gorm:"default:2"`
	BlockedReason string  `json:"blocked_reason" gorm:"type:text;default:''"`
	CreatedBy     string  `json:"created_by" gorm:"type:varchar(36);default:''"`
}

func (Mandate) TableName() string { return "mandates" }

// MandateRun is one attempt at a mandate (the audit of labor).
type MandateRun struct {
	ID          string  `json:"id" gorm:"primaryKey;type:varchar(36)"`
	MandateID   string  `json:"mandate_id" gorm:"type:varchar(36);index:idx_mandate_runs_mandate_id"`
	SessionID   string  `json:"session_id" gorm:"type:varchar(36);default:''"`
	StartedAt   int64   `json:"started_at" gorm:"autoCreateTime"`
	EndedAt     int64   `json:"ended_at" gorm:"default:0"`
	HeartbeatAt int64   `json:"heartbeat_at" gorm:"default:0"`
	Outcome     string  `json:"outcome" gorm:"type:varchar(30);default:''"`
	Summary     string  `json:"summary" gorm:"type:text;default:''"`
	Result      JSONMap `json:"result" gorm:"type:jsonb;default:'{}'"`
	AttestedBy  string  `json:"attested_by" gorm:"type:varchar(36);default:''"`
	AttestedAt  int64   `json:"attested_at" gorm:"default:0"`
	Error       string  `json:"error" gorm:"type:text;default:''"`
}

func (MandateRun) TableName() string { return "mandate_runs" }
```

- [ ] **Step 4: Write `store.go`**

Create `go/orchestrator/board/store.go`:

```go
package board

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store is the board's persistence handle (Postgres in prod, sqlite in tests).
type Store struct {
	gdb *gorm.DB
}

// Open connects to Postgres and ensures the board tables exist.
func Open(dsn string) (*Store, error) {
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("board: connect: %w", err)
	}
	if err := gdb.AutoMigrate(&Mandate{}, &MandateRun{}); err != nil {
		return nil, fmt.Errorf("board: migrate: %w", err)
	}
	return &Store{gdb: gdb}, nil
}

// CreateMandate inserts a mandate (assigning an ID if absent).
func (s *Store) CreateMandate(ctx context.Context, m *Mandate) (*Mandate, error) {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.Status == "" {
		m.Status = StatusTriage
	}
	if err := s.gdb.WithContext(ctx).Create(m).Error; err != nil {
		return nil, fmt.Errorf("board: create mandate: %w", err)
	}
	return m, nil
}

// GetMandate reads one mandate by ID.
func (s *Store) GetMandate(ctx context.Context, id string) (*Mandate, error) {
	if id == "" {
		return nil, fmt.Errorf("board: get mandate without ID")
	}
	var m Mandate
	if err := s.gdb.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, fmt.Errorf("board: get mandate: %w", err)
	}
	return &m, nil
}

// SetStatus updates a mandate's status column.
func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	res := s.gdb.WithContext(ctx).Model(&Mandate{}).Where("id = ?", id).
		Update("status", status)
	if res.Error != nil {
		return fmt.Errorf("board: set status: %w", res.Error)
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("board: set status: mandate %q not found", id)
	}
	return nil
}

// OpenRun creates a run row, points the mandate at it, and flips it to working.
func (s *Store) OpenRun(ctx context.Context, mandateID, sessionID string) (*MandateRun, error) {
	run := &MandateRun{ID: uuid.NewString(), MandateID: mandateID, SessionID: sessionID}
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Model(&Mandate{}).Where("id = ?", mandateID).Updates(map[string]any{
			"status":         StatusWorking,
			"session_id":     sessionID,
			"current_run_id": run.ID,
		}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("board: open run: %w", err)
	}
	return run, nil
}

// Heartbeat refreshes the current run's liveness timestamp.
func (s *Store) Heartbeat(ctx context.Context, mandateID string) error {
	m, err := s.GetMandate(ctx, mandateID)
	if err != nil {
		return err
	}
	res := s.gdb.WithContext(ctx).Model(&MandateRun{}).Where("id = ?", m.CurrentRunID).
		Update("heartbeat_at", time.Now().Unix())
	if res.Error != nil {
		return fmt.Errorf("board: heartbeat: %w", res.Error)
	}
	return nil
}

// CompleteRun closes the current run as completed and moves the mandate to
// attesting (the quality gate before done).
func (s *Store) CompleteRun(ctx context.Context, mandateID, summary string, result JSONMap) error {
	m, err := s.GetMandate(ctx, mandateID)
	if err != nil {
		return err
	}
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&MandateRun{}).Where("id = ?", m.CurrentRunID).Updates(map[string]any{
			"outcome":  OutcomeCompleted,
			"summary":  summary,
			"result":   result,
			"ended_at": time.Now().Unix(),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&Mandate{}).Where("id = ?", mandateID).
			Update("status", StatusAttesting).Error
	})
}

// ReferUp records a consent request: the current run ends needs_consent and the
// mandate moves to needs-consent with the reason a human will read.
func (s *Store) ReferUp(ctx context.Context, mandateID, reason string) error {
	m, err := s.GetMandate(ctx, mandateID)
	if err != nil {
		return err
	}
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&MandateRun{}).Where("id = ?", m.CurrentRunID).Updates(map[string]any{
			"outcome":  OutcomeNeedsConsent,
			"ended_at": time.Now().Unix(),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&Mandate{}).Where("id = ?", mandateID).Updates(map[string]any{
			"status":         StatusNeedsConsent,
			"blocked_reason": reason,
		}).Error
	})
}
```

- [ ] **Step 5: Ensure deps are available**

`gorm.io/driver/sqlite`, `gorm.io/driver/postgres`, `gorm.io/gorm`, and `github.com/google/uuid` are already used by the engine (`agentdb`). Confirm and tidy:

Run: `cd go && go mod tidy && go build ./orchestrator/board/`
Expected: builds; if `sqlite` driver is missing from `go.mod`, `go mod tidy` adds it (it's already a test dep of `agentdb`).

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd go && go test ./orchestrator/board/ -count=1`
Expected: PASS (3 tests).

- [ ] **Step 7: Vet + full build**

Run: `cd go && go vet ./orchestrator/board/ && go build ./...`
Expected: green.

- [ ] **Step 8: Commit**

```bash
git add go/orchestrator/board/ go/go.mod go/go.sum
git commit -m "feat(board): mandate + run models and store"
```

---

## Task 3: Board HTTP API

The token-authed write surface the worker's board tools call. DB credentials stay in the host; the worker only ever has its scoped token. Tested with httptest + a stub store.

**Files:**
- Create: `go/orchestrator/boardapi/handlers.go`
- Test: `go/orchestrator/boardapi/handlers_test.go`

**Interfaces:**
- Consumes: `board.Store` methods (Task 2); `board.Mandate`.
- Produces: `type Identity struct { UserEmail, Customer string }`; `type BoardStore interface { GetMandate(...); OpenRun(...); Heartbeat(...); CompleteRun(...); ReferUp(...); CreateMandate(...) }`; `New(Config) *Handlers`; `(*Handlers).Mux() *http.ServeMux`.
- Endpoints (all expect `?mandate=<id>` or JSON body, authenticated upstream): `GET /board/mandate`, `POST /board/heartbeat`, `POST /board/complete`, `POST /board/refer-up`, `POST /board/create`.

- [ ] **Step 1: Write the failing test**

Create `go/orchestrator/boardapi/handlers_test.go`:

```go
package boardapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator/board"
)

type stubStore struct {
	got      string // last mandate id seen
	complete struct {
		id, summary string
	}
}

func (s *stubStore) GetMandate(_ context.Context, id string) (*board.Mandate, error) {
	s.got = id
	return &board.Mandate{ID: id, Title: "T", Brief: "B", Status: board.StatusWorking}, nil
}
func (s *stubStore) OpenRun(context.Context, string, string) (*board.MandateRun, error) {
	return &board.MandateRun{ID: "r1"}, nil
}
func (s *stubStore) Heartbeat(context.Context, string) error { return nil }
func (s *stubStore) CompleteRun(_ context.Context, id, summary string, _ board.JSONMap) error {
	s.complete.id, s.complete.summary = id, summary
	return nil
}
func (s *stubStore) ReferUp(context.Context, string, string) error { return nil }
func (s *stubStore) CreateMandate(_ context.Context, m *board.Mandate) (*board.Mandate, error) {
	m.ID = "created-1"
	return m, nil
}

func testHandlers(st *stubStore) *Handlers {
	return New(Config{
		Store:    st,
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "owner@badcode", Customer: "badcode"}, nil },
	})
}

func TestShowMandate(t *testing.T) {
	st := &stubStore{}
	h := testHandlers(st)
	req := httptest.NewRequest("GET", "/board/mandate?mandate=m-9", nil)
	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	if st.got != "m-9" {
		t.Fatalf("store saw %q", st.got)
	}
	var out board.Mandate
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "m-9" {
		t.Fatalf("bad body: %s", rec.Body)
	}
}

func TestCompleteMandate(t *testing.T) {
	st := &stubStore{}
	h := testHandlers(st)
	body := `{"mandate":"m-9","summary":"done it","result":{"files":"3"}}`
	req := httptest.NewRequest("POST", "/board/complete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	if st.complete.id != "m-9" || st.complete.summary != "done it" {
		t.Fatalf("complete not forwarded: %+v", st.complete)
	}
}

func TestCreateMandateReturnsID(t *testing.T) {
	st := &stubStore{}
	h := testHandlers(st)
	body := `{"title":"child","brief":"do x","model":"cheap"}`
	req := httptest.NewRequest("POST", "/board/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var out struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.ID != "created-1" {
		t.Fatalf("missing id: %s", rec.Body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/boardapi/ -count=1`
Expected: FAIL — package `boardapi` does not exist.

- [ ] **Step 3: Write `handlers.go`**

Create `go/orchestrator/boardapi/handlers.go`:

```go
// Package boardapi exposes the board's worker-facing write surface over HTTP.
// Mount Mux() under the host's JWT auth middleware. Workers call it with their
// scoped session token; DB credentials never leave the host.
package boardapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator/board"
)

// Identity is the authenticated caller, resolved by the host from the request.
type Identity struct {
	UserEmail string
	Customer  string
}

// BoardStore is the subset of *board.Store the API needs (interface for testing).
type BoardStore interface {
	GetMandate(ctx context.Context, id string) (*board.Mandate, error)
	OpenRun(ctx context.Context, mandateID, sessionID string) (*board.MandateRun, error)
	Heartbeat(ctx context.Context, mandateID string) error
	CompleteRun(ctx context.Context, mandateID, summary string, result board.JSONMap) error
	ReferUp(ctx context.Context, mandateID, reason string) error
	CreateMandate(ctx context.Context, m *board.Mandate) (*board.Mandate, error)
}

// Config wires the handlers.
type Config struct {
	Store    BoardStore
	Identity func(*http.Request) (Identity, error)
}

// Handlers serves the board API.
type Handlers struct{ cfg Config }

// New constructs Handlers.
func New(cfg Config) *Handlers { return &Handlers{cfg: cfg} }

// Mux registers the board routes on a fresh ServeMux.
func (h *Handlers) Mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /board/mandate", h.show)
	m.HandleFunc("POST /board/heartbeat", h.heartbeat)
	m.HandleFunc("POST /board/complete", h.complete)
	m.HandleFunc("POST /board/refer-up", h.referUp)
	m.HandleFunc("POST /board/create", h.create)
	return m
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handlers) show(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("mandate")
	if id == "" {
		http.Error(w, "missing mandate", http.StatusBadRequest)
		return
	}
	m, err := h.cfg.Store.GetMandate(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, m)
}

func (h *Handlers) heartbeat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mandate string `json:"mandate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Mandate == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.cfg.Store.Heartbeat(r.Context(), in.Mandate); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handlers) complete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mandate string        `json:"mandate"`
		Summary string        `json:"summary"`
		Result  board.JSONMap `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Mandate == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.cfg.Store.CompleteRun(r.Context(), in.Mandate, in.Summary, in.Result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "attesting"})
}

func (h *Handlers) referUp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mandate string `json:"mandate"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Mandate == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.cfg.Store.ReferUp(r.Context(), in.Mandate, in.Reason); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "needs-consent"})
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	id, err := h.cfg.Identity(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		Title string `json:"title"`
		Brief string `json:"brief"`
		Model string `json:"model"`
		Job   string `json:"job"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Title == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	m, err := h.cfg.Store.CreateMandate(r.Context(), &board.Mandate{
		Customer: id.Customer, Job: in.Job, Title: in.Title, Brief: in.Brief,
		Model: in.Model, Status: board.StatusReady,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"id": m.ID})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./orchestrator/boardapi/ -count=1`
Expected: PASS (3 tests). Note `*board.Store` satisfies `BoardStore` structurally; the stub is used in tests.

- [ ] **Step 5: Vet + full build**

Run: `cd go && go vet ./orchestrator/... && go build ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add go/orchestrator/boardapi/
git commit -m "feat(board): token-authed HTTP API for worker board ops"
```

---

## Task 4: Sandbox — env-gated tool registration

Adds the Hermes `check_fn` pattern: a plugin declaring `requiresEnv` is omitted from the model's schema unless that env var is set. This is "the keys = blast radius," enforced by absence from the schema.

**Files:**
- Modify: `sandbox/src/tools/registry.ts` (`ToolPlugin`)
- Modify: `sandbox/src/tools/registry-impl.ts` (`resolve`)
- Test: `sandbox/src/tools/registry-impl.test.ts` (create)

**Interfaces:**
- Produces: `ToolPlugin.requiresEnv?: string` — when set, the plugin is included only if `process.env[requiresEnv]` is truthy.
- Produces: `DefaultToolRegistry.resolve()` filters `requiresEnv`-gated plugins before building the MCP server.

- [ ] **Step 1: Write the failing test**

Create `sandbox/src/tools/registry-impl.test.ts`:

```typescript
import { describe, it, expect, afterEach } from 'vitest';
import { DefaultToolRegistry } from './registry-impl.js';
import type { ToolPlugin } from './registry.js';
import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';

function fakePlugin(name: string, requiresEnv?: string): ToolPlugin {
  return {
    name,
    requiresEnv,
    sdkTool: tool(name, name, {}, async () => ({ content: [{ type: 'text', text: 'ok' }] })),
  };
}

afterEach(() => {
  delete process.env.AO_MANDATE;
});

describe('env-gated tool registration', () => {
  it('omits a requiresEnv plugin when the env var is unset', () => {
    const reg = new DefaultToolRegistry();
    reg.register(fakePlugin('mandate_show', 'AO_MANDATE'));
    const resolved = reg.resolve();
    expect(resolved.allowedTools).not.toContain('mcp__ui__mandate_show');
  });

  it('includes a requiresEnv plugin when the env var is set', () => {
    process.env.AO_MANDATE = 'm-1';
    const reg = new DefaultToolRegistry();
    reg.register(fakePlugin('mandate_show', 'AO_MANDATE'));
    const resolved = reg.resolve();
    expect(resolved.allowedTools).toContain('mcp__ui__mandate_show');
  });

  it('always includes plugins with no requiresEnv', () => {
    const reg = new DefaultToolRegistry();
    reg.register(fakePlugin('plain'));
    const resolved = reg.resolve();
    expect(resolved.allowedTools).toContain('mcp__ui__plain');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sandbox && yarn test registry-impl`
Expected: FAIL — `requiresEnv` not a known property and/or gating not applied.

- [ ] **Step 3: Add `requiresEnv` to `ToolPlugin`**

In `sandbox/src/tools/registry.ts`, in `interface ToolPlugin`, add after `marker?: MarkerSpec;`:

```typescript
  /**
   * When set, this tool is included in the model's schema only if the named
   * environment variable is truthy at resolve() time. This is how a session's
   * "keys" are gated: the board tools declare requiresEnv: 'AO_MANDATE', so they
   * appear only in a dispatcher-spawned worker. Absent ⇒ always included.
   */
  requiresEnv?: string;
```

- [ ] **Step 4: Filter gated plugins in `resolve()`**

In `sandbox/src/tools/registry-impl.ts`, at the very top of `resolve(allowed?: string[])`, replace:

```typescript
  resolve(allowed?: string[]): ResolvedTools {
    const allPlugins = [...this._builtins, ...this._plugins];
```

with:

```typescript
  resolve(allowed?: string[]): ResolvedTools {
    const allPlugins = [...this._builtins, ...this._plugins].filter(
      (p) => !p.requiresEnv || !!process.env[p.requiresEnv],
    );
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd sandbox && yarn test registry-impl`
Expected: PASS (3 tests).

- [ ] **Step 6: Typecheck**

Run: `cd sandbox && yarn typecheck`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add sandbox/src/tools/registry.ts sandbox/src/tools/registry-impl.ts sandbox/src/tools/registry-impl.test.ts
git commit -m "feat(sandbox): env-gated tool registration (the keys)"
```

---

## Task 5: Sandbox — the board tool plugins

The worker-tier and steward-tier board tools, gated by env, calling the board API with the scoped session token. Worker-tier gate on `AO_MANDATE`; steward-tier gate on `AO_STEWARD`.

**Files:**
- Create: `sandbox/src/tools/board/client.ts`
- Create: `sandbox/src/tools/board/index.ts`
- Test: `sandbox/src/tools/board/board.test.ts`

**Interfaces:**
- Consumes: `ToolPlugin` + `requiresEnv` (Task 4); board API routes (Task 3).
- Produces: `boardPlugins: ToolPlugin[]` exported from `board/index.ts` (`mandate_show`, `mandate_heartbeat`, `mandate_complete`, `refer_up` — gated on `AO_MANDATE`; `mandate_create` — gated on `AO_STEWARD`).
- Produces: `boardFetch(path, init?)` in `client.ts` — authed `fetch` against `HOST_API_URL` with `SESSION_TOKEN`.

- [ ] **Step 1: Write the failing test**

Create `sandbox/src/tools/board/board.test.ts`:

```typescript
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { boardPlugins } from './index.js';

function findTool(name: string) {
  const p = boardPlugins.find((t) => t.name === name);
  if (!p) throw new Error(`no plugin ${name}`);
  return p;
}

beforeEach(() => {
  process.env.HOST_API_URL = 'http://host/api/v1';
  process.env.SESSION_TOKEN = 'tok-xyz';
  process.env.AO_MANDATE = 'm-77';
});
afterEach(() => {
  vi.restoreAllMocks();
  delete process.env.AO_MANDATE;
  delete process.env.AO_STEWARD;
});

describe('board tool gating', () => {
  it('worker tools require AO_MANDATE', () => {
    expect(findTool('mandate_show').requiresEnv).toBe('AO_MANDATE');
    expect(findTool('mandate_complete').requiresEnv).toBe('AO_MANDATE');
  });
  it('mandate_create requires AO_STEWARD', () => {
    expect(findTool('mandate_create').requiresEnv).toBe('AO_STEWARD');
  });
});

describe('mandate_show', () => {
  it('GETs the board API with the session token and returns the body text', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: 'm-77', title: 'T' }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    const res = await findTool('mandate_show').sdkTool.handler({}, {} as any);

    expect(fetchMock).toHaveBeenCalledWith(
      'http://host/api/v1/board/mandate?mandate=m-77',
      expect.objectContaining({ headers: { Authorization: 'Bearer tok-xyz' } }),
    );
    expect(res.content[0].text).toContain('m-77');
  });
});

describe('mandate_complete', () => {
  it('POSTs summary + result for the current mandate', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ status: 'attesting' }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await findTool('mandate_complete').sdkTool.handler(
      { summary: 'shipped', result: { files: '3' } },
      {} as any,
    );

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('http://host/api/v1/board/complete');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ mandate: 'm-77', summary: 'shipped', result: { files: '3' } });
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sandbox && yarn test board`
Expected: FAIL — `./index.js` (board plugins) does not exist.

- [ ] **Step 3: Write `client.ts`**

Create `sandbox/src/tools/board/client.ts`:

```typescript
/**
 * Tiny authed client for the host board API. Reads HOST_API_URL + SESSION_TOKEN
 * at call time (not import time) so tests can set them per-case. DB credentials
 * never reach the sandbox — only the scoped session token does.
 */
function base(): string {
  return (process.env.HOST_API_URL ?? 'http://localhost:80/api/v1').replace(/\/$/, '');
}

function token(): string {
  return process.env.SESSION_TOKEN ?? '';
}

/** The mandate this worker holds (set by the dispatcher/host as AO_MANDATE). */
export function currentMandate(): string {
  return process.env.AO_MANDATE ?? '';
}

export async function boardGet(path: string): Promise<string> {
  const resp = await fetch(`${base()}${path}`, {
    headers: { Authorization: `Bearer ${token()}` },
  });
  const text = await resp.text();
  if (!resp.ok) throw new Error(`board ${path} -> ${resp.status}: ${text}`);
  return text;
}

export async function boardPost(path: string, body: unknown): Promise<string> {
  const resp = await fetch(`${base()}${path}`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token()}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const text = await resp.text();
  if (!resp.ok) throw new Error(`board ${path} -> ${resp.status}: ${text}`);
  return text;
}
```

- [ ] **Step 4: Write `index.ts` (the plugins)**

Create `sandbox/src/tools/board/index.ts`:

```typescript
import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import type { ToolPlugin } from '../registry.js';
import { boardGet, boardPost, currentMandate } from './client.js';

function textResult(text: string) {
  return { content: [{ type: 'text' as const, text }] };
}

const mandateShow: ToolPlugin = {
  name: 'mandate_show',
  requiresEnv: 'AO_MANDATE',
  sdkTool: tool(
    'mandate_show',
    'Orient: read your mandate (title, brief, keys, budget) and its current state. Call this first.',
    {},
    async () => textResult(await boardGet(`/board/mandate?mandate=${encodeURIComponent(currentMandate())}`)),
  ),
};

const mandateHeartbeat: ToolPlugin = {
  name: 'mandate_heartbeat',
  requiresEnv: 'AO_MANDATE',
  sdkTool: tool(
    'mandate_heartbeat',
    'Signal you are still alive during long work, so you are not reclaimed.',
    { note: z.string().optional().describe('Optional progress note') },
    async () => textResult(await boardPost('/board/heartbeat', { mandate: currentMandate() })),
  ),
};

const mandateComplete: ToolPlugin = {
  name: 'mandate_complete',
  requiresEnv: 'AO_MANDATE',
  sdkTool: tool(
    'mandate_complete',
    'Finish your mandate with a structured handoff. summary is for humans; result is machine-readable facts downstream workers read.',
    {
      summary: z.string().min(1).describe('1-3 sentences naming concrete deliverables'),
      result: z.record(z.string(), z.any()).optional().describe('Structured handoff, e.g. {changed_files, decisions}'),
    },
    async (args) =>
      textResult(await boardPost('/board/complete', { mandate: currentMandate(), summary: args.summary, result: args.result ?? {} })),
  ),
};

const referUp: ToolPlugin = {
  name: 'refer_up',
  requiresEnv: 'AO_MANDATE',
  sdkTool: tool(
    'refer_up',
    'Refer up to the owner for a credential, money, or a decision beyond your mandate. After calling this, STOP.',
    { reason: z.string().min(1).describe('What you need and why — a human will read this') },
    async (args) =>
      textResult(await boardPost('/board/refer-up', { mandate: currentMandate(), reason: args.reason })),
  ),
};

const mandateCreate: ToolPlugin = {
  name: 'mandate_create',
  requiresEnv: 'AO_STEWARD',
  sdkTool: tool(
    'mandate_create',
    'Engage: post a new, fully-specified mandate to the board for the right hands. Steward-tier only.',
    {
      title: z.string().min(1),
      brief: z.string().min(1).describe('The narrowed objective + context slice'),
      model: z.string().optional().describe('full | mid | cheap'),
      job: z.string().optional(),
    },
    async (args) =>
      textResult(await boardPost('/board/create', { title: args.title, brief: args.brief, model: args.model ?? 'mid', job: args.job ?? '' })),
  ),
};

/** All board tools. Env gates decide which appear: worker-tier vs steward-tier. */
export const boardPlugins: ToolPlugin[] = [
  mandateShow,
  mandateHeartbeat,
  mandateComplete,
  referUp,
  mandateCreate,
];
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd sandbox && yarn test board`
Expected: PASS (gating + show + complete).

> Note: the test calls `sdkTool.handler(...)` directly. If the SDK's `tool()` stores the handler under a different property in this SDK version, adjust the test's `.handler` access to match (inspect one existing builtin test). The plugin code is unaffected.

- [ ] **Step 6: Register board plugins as built-ins of the registry**

So a worker image gets them without a product-plugins dir. In `sandbox/src/tools/registry-impl.ts`, add the import at the top:

```typescript
import { boardPlugins } from './board/index.js';
```

and spread them into the builtins array:

```typescript
  private readonly _builtins: ToolPlugin[] = [
    askUserTool,
    writeFileTool,
    viewImageTool,
    screenshotUrlTool,
    ...boardPlugins,
  ];
```

(They are gated by `requiresEnv`, so they stay invisible unless `AO_MANDATE`/`AO_STEWARD` is set.)

- [ ] **Step 7: Typecheck + full sandbox test**

Run: `cd sandbox && yarn typecheck && yarn test`
Expected: green.

- [ ] **Step 8: Commit**

```bash
git add sandbox/src/tools/board/ sandbox/src/tools/registry-impl.ts
git commit -m "feat(sandbox): board tool plugins (worker + steward tier)"
```

---

## Task 6: The worker protocol prompt

The system-prompt block prepended to a worker, in canon voice. Functional and the conscience mechanism.

**Files:**
- Create: `sandbox/src/prompt/worker-protocol.ts`
- Test: `sandbox/src/prompt/worker-protocol.test.ts`

**Interfaces:**
- Produces: `WORKER_PROTOCOL: string` and `withMandate(brief: string): string` (protocol + the brief).

- [ ] **Step 1: Write the failing test**

Create `sandbox/src/prompt/worker-protocol.test.ts`:

```typescript
import { describe, it, expect } from 'vitest';
import { WORKER_PROTOCOL, withMandate } from './worker-protocol.js';

describe('worker protocol', () => {
  it('names the core lifecycle verbs', () => {
    for (const verb of ['mandate_show', 'mandate_heartbeat', 'refer_up', 'mandate_complete']) {
      expect(WORKER_PROTOCOL).toContain(verb);
    }
  });
  it('withMandate appends the brief under the protocol', () => {
    const out = withMandate('Draft 3 posts.');
    expect(out.startsWith(WORKER_PROTOCOL)).toBe(true);
    expect(out).toContain('Draft 3 posts.');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sandbox && yarn test worker-protocol`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `worker-protocol.ts`**

Create `sandbox/src/prompt/worker-protocol.ts`:

```typescript
/**
 * The worker lifecycle protocol, prepended to a worker's system prompt. Canon
 * voice (labor register) — functional, and the conscience mechanism in motion.
 */
export const WORKER_PROTOCOL = `You hold ONE mandate. Its id is in your tools as AO_MANDATE.

  Orient.   Call mandate_show() first. The brief, your parents' handoffs, and what
            you've been given to remember are your ground truth.
  Work.     Inside your floor, with the keys you were entrusted — only those. If a
            tool you want isn't here, you were not given it. That is deliberate.
  Heartbeat.On long work, call mandate_heartbeat() — or you'll be reclaimed and lose progress.
  Refer up. Need a credential, money, or a decision that isn't yours to make?
            Call refer_up() and stop. Do not guess. Do not act in the owner's name.
  Finish.   End with mandate_complete(summary, result). Your result is the handoff the
            next worker reads — make it true. If it still needs eyes you don't control,
            say so plainly; don't bless your own work.
  Don't sprawl. Follow-up work you can see but weren't asked to do: name it in your
            result. Never wander outside your brief — that is overreach.
`;

/** Compose the full worker system prompt: protocol + the mandate's brief. */
export function withMandate(brief: string): string {
  return `${WORKER_PROTOCOL}\n---\nYour brief:\n${brief}\n`;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd sandbox && yarn test worker-protocol`
Expected: PASS.

- [ ] **Step 5: Typecheck**

Run: `cd sandbox && yarn typecheck`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add sandbox/src/prompt/worker-protocol.ts sandbox/src/prompt/worker-protocol.test.ts
git commit -m "feat(sandbox): worker lifecycle protocol prompt"
```

---

## Task 7: The board host — wiring + hand-post a mandate

The dogfood host that ties it together: construct the Runner + agentdb + board store, mount `httpapi.Mux()` and `boardapi.Mux()` under JWT auth, and provide a `--post-mandate` dev path that creates a mandate by hand and launches its worker session with `AO_MANDATE` + the worker protocol. This is the Phase-A acceptance harness (build + smoke; full run needs Docker + Postgres, out of unit-test scope).

**Files:**
- Create: `go/cmd/orchestratord/auth.go` (JWT middleware, mirrors `go/cmd/agentd/auth.go`)
- Create: `go/cmd/orchestratord/main.go`

**Interfaces:**
- Consumes: `agentkit.NewRunner`, `agentkit.CreateSessionRequest{Env, SystemPrompt}` (Task 1); `board.Open`/`board.Store` (Task 2); `boardapi.New`/`Mux` (Task 3).

- [ ] **Step 1: Write `auth.go`**

Create `go/cmd/orchestratord/auth.go` (the same HS256 verifier pattern as `go/cmd/agentd/auth.go`):

```go
package main

import (
	"context"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/boardapi_identity" // placeholder if a shared identity pkg exists; otherwise inline below
)

type principal struct {
	email    string
	customer string
}

type ctxKey int

const principalKey ctxKey = 0

func contextWithPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

func principalFromContext(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(principalKey).(principal)
	return p, ok
}

var _ = boardapi_identity.Unused // keep import note; remove if not using a shared pkg
```

> The import line above is a reminder, not real: there is no shared identity package. Delete the `boardapi_identity` import and the trailing `var _` line, and instead copy the concrete `jwtAuthMiddleware` + `principal` helpers verbatim from `go/cmd/agentd/auth.go` into this file (they are package-private to `main`, so they must be duplicated, not imported). After copying, this file should compile with only `net/http`, `context`, and `github.com/golang-jwt/jwt/v5` imports.

- [ ] **Step 2: Replace `auth.go` with the real copy**

Open `go/cmd/agentd/auth.go`, copy `jwtAuthMiddleware`, `identityFromRequest`, `principal`, `contextWithPrincipal`, `principalFromContext` (and their imports) verbatim into `go/cmd/orchestratord/auth.go`. Verify it compiles standalone:

Run: `cd go && go build ./cmd/orchestratord/ 2>&1 | head`
Expected: only an "undefined: main" or "no main" style error (main.go not written yet) — NOT an auth.go syntax error.

- [ ] **Step 3: Write `main.go`**

Create `go/cmd/orchestratord/main.go`:

```go
// Command orchestratord is the Phase-A board host: it runs the agentkit Runner,
// the agentdb session store, and the board (mandates), and mounts both HTTP
// surfaces under JWT auth. With --post-mandate it hand-creates a mandate and
// launches its worker session (no dispatch loop yet — that is Phase B).
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator/board"
	"github.com/binocarlos/badcode-agent-orange/orchestrator/boardapi"
	"github.com/binocarlos/badcode-agent-orange/sandbox/prompt" // see note in Step 4
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	postMandate := flag.String("post-mandate", "", "DEV: brief for a hand-created mandate to launch")
	flag.Parse()

	pg := envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/agentkit?sslmode=disable")

	bstore, err := board.Open(pg)
	must(err)

	// Engine wiring is environment-specific; reuse the same construction as
	// cmd/agentd (Fleet, Registry, agentdb.Store, Claims). For Phase A, copy the
	// Deps assembly from cmd/agentd/main.go. The only board-specific additions
	// are bstore + the boardapi mux below.
	runner := buildRunner(pg) // implement by mirroring cmd/agentd (Step 5)
	must(runner.Start(context.Background()))
	defer runner.Close() //nolint:errcheck

	if *postMandate != "" {
		handPost(context.Background(), runner, bstore, *postMandate)
		return
	}

	bapi := boardapi.New(boardapi.Config{
		Store:    bstore,
		Identity: identityFromRequest,
	})

	root := http.NewServeMux()
	root.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }) //nolint:errcheck
	root.Handle("/", jwtAuthMiddleware([]byte(os.Getenv("AGENTKIT_JWT_SECRET")), bapi.Mux()))

	addr := envOr("ADDR", ":8100")
	log.Printf("[orchestratord] listening on %s", addr)
	must(http.ListenAndServe(addr, root))
}

// handPost creates one mandate and launches its worker session with AO_MANDATE
// + the worker protocol. Phase-A "you are the dispatcher" path.
func handPost(ctx context.Context, runner agentkit.Runner, bstore *board.Store, brief string) {
	m, err := bstore.CreateMandate(ctx, &board.Mandate{
		Customer: "badcode", Job: "marketing", Title: "hand-posted", Brief: brief,
		Model: "mid", Status: board.StatusReady,
	})
	must(err)
	_, err = bstore.OpenRun(ctx, m.ID, m.ID) // SessionID == mandate ID for the hand path
	must(err)

	_, err = runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID:    m.ID,
		Customer:     m.Customer,
		Job:          m.Job,
		Model:        m.Model,
		SystemPrompt: workerProtocol(m.Brief),
		Env:          map[string]string{"AO_MANDATE": m.ID},
	})
	must(err)
	log.Printf("[orchestratord] launched worker for mandate %s", m.ID)
}
```

- [ ] **Step 4: Resolve the worker-protocol reference**

The worker protocol string lives in TypeScript (`sandbox/src/prompt/worker-protocol.ts`) for the in-image side. The **Go host** needs the same text to set `SystemPrompt`. Do **not** import across language. Instead, add a Go constant. Remove the `sandbox/prompt` import from Step 3 and create `go/cmd/orchestratord/protocol.go`:

```go
package main

import "fmt"

const workerProtocolText = `You hold ONE mandate. Its id is in your tools as AO_MANDATE.

  Orient.   Call mandate_show() first. The brief, your parents' handoffs, and what
            you've been given to remember are your ground truth.
  Work.     Inside your floor, with the keys you were entrusted — only those.
  Heartbeat.On long work, call mandate_heartbeat() — or you'll be reclaimed.
  Refer up. Need a credential, money, or a decision that isn't yours to make?
            Call refer_up() and stop. Do not act in the owner's name.
  Finish.   End with mandate_complete(summary, result). Make the result true.
  Don't sprawl. Name follow-up work in your result. Never exceed your brief — overreach.`

func workerProtocol(brief string) string {
	return fmt.Sprintf("%s\n---\nYour brief:\n%s\n", workerProtocolText, brief)
}
```

(The two copies — TS for the harness default, Go for the host-set prompt — are intentional and small; keep them in sync. A later phase can serve the protocol from one place.)

- [ ] **Step 5: Implement `buildRunner` by mirroring `cmd/agentd`**

Open `go/cmd/agentd/main.go`. Copy the `Deps` assembly (Fleet, Registry, Store via `agentdb.Open(pg)`, Artifacts, Claims, Policy with `SessionEnv`) into a `buildRunner(pg string) agentkit.Runner` function in `go/cmd/orchestratord/main.go`, returning the constructed runner. Use the same env var names (`AGENTKIT_IMAGE`, model-proxy `SessionEnv`) as agentd.

> This is deliberate duplication of host wiring, not engine code. Keep it faithful to `cmd/agentd` so behavior matches the known-good host.

- [ ] **Step 6: Build + vet**

Run: `cd go && go build ./cmd/orchestratord/ && go vet ./cmd/orchestratord/`
Expected: green. If any `agentdb`/`fleet`/`imageregistry` constructor differs from agentd, align with the actual `cmd/agentd/main.go` source.

- [ ] **Step 7: Full build + test**

Run: `cd go && go build ./... && go test ./... -count=1 && go vet ./...`
Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add go/cmd/orchestratord/
git commit -m "feat(orchestratord): Phase-A board host + hand-post a mandate"
```

---

## Phase-A Definition of Done

- `go build ./...`, `go vet ./...`, `go test ./... -count=1` green; `cd sandbox && yarn typecheck && yarn test` green.
- A worker session launched with `AO_MANDATE` set sees `mandate_show/heartbeat/complete/refer_up` in its tool schema and **nothing steward-tier**; with `AO_STEWARD` set it also sees `mandate_create`; with neither, no board tools appear.
- The board persists a mandate through `ready → working → attesting` on complete, and `working → needs-consent` on refer-up, in Postgres (and the sqlite unit tests prove the transitions).
- The board API authenticates with the scoped session token; the worker never holds DB credentials.
- `orchestratord --post-mandate "<brief>"` creates a mandate, opens a run, and launches a worker session whose system prompt is the worker protocol + the brief. (Manual smoke; requires Docker + Postgres.)

## Out of scope (later phases, per the design spec §6.2)

- **Phase B:** the dispatch loop (promote/claim/launch/reclaim/budget), `auto` attest, crash detection via `Runner.RunningSessions()`.
- **Phase C:** shifts (cron) + calls (event intake) + out-of-band notify; consent→`Runner.Resume`.
- **Phase D:** the steward standing mandate, `mandate_deps` DAG + handoffs, the `memory` store + recall, verify-worker attest.
- **Phase E:** bounded parallel fan-out (lanes/caps).

---

## Self-Review

**1. Spec coverage (Phase A scope only):** board tables (§2) → Tasks 2; state transitions ready/working/attesting/needs-consent → Task 2 store + tests; the keys / env-gated tools (§3) → Tasks 1 (engine Env) + 4 (registration) + 5 (tools); board API w/ token auth (§3) → Task 3; worker protocol + tool surface (§4) → Tasks 5–6; hand-driven launch (§6.2 Phase A) → Task 7. Deferred items (loop, steward, memory, deps, attest machinery, shifts/calls) are explicitly Out of Scope above. No Phase-A gap.

**2. Placeholder scan:** the only "mirror/copy from cmd/agentd" steps are Task 7 host wiring — these are deliberate verbatim-copy instructions of *existing, named* code (with the exact source file), not vague "add error handling." The Step-1 `auth.go` stub is explicitly flagged as a reminder to be replaced in Step 2 with a named verbatim copy. Acceptable given host wiring is environment-specific and must match the known-good `cmd/agentd`.

**3. Type consistency:** `Mandate`/`MandateRun` field + status/outcome constant names are identical across Task 2 (def), Task 3 (`BoardStore` interface + handlers), and Task 7 (host). `CreateSessionRequest.Env` (Task 1) is consumed in Task 7. `ToolPlugin.requiresEnv` (Task 4) is set in Task 5 and filtered in Task 4's `resolve`. `boardGet`/`boardPost`/`currentMandate` (Task 5 client) are consumed only within Task 5 tools. The board API paths (`/board/mandate`, `/board/complete`, `/board/refer-up`, `/board/heartbeat`, `/board/create`) match between Task 3 routes and Task 5 client calls.
