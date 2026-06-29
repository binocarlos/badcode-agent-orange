# Architecture-Board Data Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Postgres-backed architecture-board data model — an immutable event-sourced revision log plus a typed current-state cache — as gorm structs, SQL migrations, and a `BoardStore` seam interface.

**Architecture:** Postgres is the single source of truth behind a `BoardStore` seam. The source of truth is an append-only changeset log (`board_revisions` + a single-row `board_head` pointer); five typed tables (`board_staff`, `board_event_types`, `board_subscriptions`, `board_pipelines`, `board_prompt_fragments`) are a derived, rebuildable cache of the current folded state. This spec delivers the **schema, types, and seam shape only** — no fold/apply implementation (that is a later spec).

**Tech Stack:** Go (module `github.com/binocarlos/badcode-agent-orange`), gorm + Postgres in production, gorm + `glebarez/sqlite` (temp-file) in tests. Schema lives in `go/agentdb/` following the existing `Skill`/`CustomImage` pattern.

**Spec:** `docs/superpowers/specs/2026-06-29-architecture-board-data-model-design.md`

## Global Constraints

- **Go floor is 1.25.** Don't lower it.
- **Module path** is `github.com/binocarlos/badcode-agent-orange` — never reintroduce `bayes-price/agentkit`.
- **Liftability:** `go/` imports nothing from any host app (CI-enforced).
- **`cd go && go build ./...` must stay green** after every task.
- **Schema lives in two places, kept consistent by hand** (existing codebase convention): a gorm-tagged struct in `go/agentdb/board.go` (tested via sqlite `AutoMigrate`) AND a numbered SQL entry in `go/agentdb/migrations.go` (production Postgres; build-verified only, never run on sqlite).
- **Migrations are numbered, idempotent, append-only.** The last existing migration is `019_agent_conversation_index`; new ones are `020_*`, `021_*`.
- **Test pattern:** white-box tests in `package agentdb`; construct `Store{gdb: db}` directly over a temp sqlite DB and `AutoMigrate` only the structs under test (mirror `go/agentdb/artifacts_test.go:15` `newTestStore` and `go/agentdb/skills_test.go:9`).
- **All test/build commands run from the `go/` directory.**

---

### Task 1: The changeset log (revision + head)

The append-only source of truth: `board_revisions` (each row = one changeset of ops) and `board_head` (single-row pointer to the live applied revision). Also defines the `Op` / `Changeset` value types used by the future `Append`.

**Files:**
- Create: `go/agentdb/board.go`
- Create: `go/agentdb/board_test.go`
- Modify: `go/agentdb/migrations.go` (append `020_board_revisions` to the `agentMigrations` slice, after `019_agent_conversation_index`)

**Interfaces:**
- Consumes: `Store{gdb *gorm.DB}` (`go/agentdb/store.go:14`), `JSONArray` (`go/agentdb/types.go:42`).
- Produces:
  - `type OpKind string` with consts `OpAdd = "add"`, `OpUpdate = "update"`, `OpRemove = "remove"`.
  - `type Op struct { Op OpKind; EntityType string; EntityID string; Body json.RawMessage }` (JSON tags `op`,`entity_type`,`entity_id`,`body`).
  - `type Changeset struct { ParentID string; Author string; Message string; Ops []Op }`.
  - `type BoardRevision struct{...}` table `board_revisions`, fields incl. `ID string`, `Seq int64`, `Status string`, `Ops JSONArray`.
  - `type BoardHead struct{...}` table `board_head`, fields `Singleton bool`, `RevisionID string`.

- [ ] **Step 1: Write the failing test**

Create `go/agentdb/board_test.go`:

```go
package agentdb

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newBoardTestStore returns a Store over a temp sqlite DB with the board log
// tables auto-migrated. (Grows to include the current-state tables in Task 2.)
func newBoardTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "board_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&BoardRevision{}, &BoardHead{}); err != nil {
		t.Fatalf("automigrate board log: %v", err)
	}
	return &Store{gdb: db}
}

func TestBoardRevision_OpsRoundTrip(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()

	ops, err := json.Marshal([]Op{
		{Op: OpAdd, EntityType: "staff", EntityID: "legal-expert", Body: json.RawMessage(`{"model_tier":"mid"}`)},
	})
	if err != nil {
		t.Fatalf("marshal ops: %v", err)
	}
	rev := &BoardRevision{ID: "rev1", Seq: 1, Status: "applied", Author: "kai@x", Message: "init board", Ops: JSONArray(ops)}
	if err := s.gdb.WithContext(ctx).Create(rev).Error; err != nil {
		t.Fatalf("create revision: %v", err)
	}

	var got BoardRevision
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "rev1").Error; err != nil {
		t.Fatalf("read revision: %v", err)
	}
	var decoded []Op
	if err := json.Unmarshal([]byte(got.Ops), &decoded); err != nil {
		t.Fatalf("unmarshal ops: %v", err)
	}
	if len(decoded) != 1 || decoded[0].EntityID != "legal-expert" || decoded[0].Op != OpAdd {
		t.Fatalf("ops round-trip wrong: %+v", decoded)
	}
}

func TestBoardHead_SingleRowPointer(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()
	if err := s.gdb.WithContext(ctx).Create(&BoardHead{Singleton: true, RevisionID: "rev1"}).Error; err != nil {
		t.Fatalf("create head: %v", err)
	}
	var got BoardHead
	if err := s.gdb.WithContext(ctx).First(&got).Error; err != nil {
		t.Fatalf("read head: %v", err)
	}
	if got.RevisionID != "rev1" {
		t.Fatalf("expected head -> rev1, got %q", got.RevisionID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./agentdb/ -run 'TestBoardRevision_OpsRoundTrip|TestBoardHead_SingleRowPointer' -v`
Expected: FAIL to compile — `undefined: BoardRevision`, `undefined: BoardHead`, `undefined: Op`, `undefined: OpAdd`.

- [ ] **Step 3: Write minimal implementation**

Create `go/agentdb/board.go`:

```go
package agentdb

import "encoding/json"

// OpKind is the kind of mutation a changeset op performs on a board entity.
type OpKind string

const (
	OpAdd    OpKind = "add"
	OpUpdate OpKind = "update"
	OpRemove OpKind = "remove"
)

// Op is one mutation within a changeset. Body is the full entity body for
// add/update and is empty for remove.
type Op struct {
	Op         OpKind          `json:"op"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Body       json.RawMessage `json:"body,omitempty"`
}

// Changeset is a proposed batch of ops appended to the board log as one revision.
type Changeset struct {
	ParentID string
	Author   string
	Message  string
	Ops      []Op
}

// BoardRevision is one immutable entry in the append-only board log (the source
// of truth). Ops folded in ascending Seq order reconstruct the board state.
// Postgres backs Seq with BIGSERIAL (see migration 020).
type BoardRevision struct {
	ID        string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	ParentID  string    `json:"parent_id" gorm:"type:varchar(36);default:''"`
	Seq       int64     `json:"seq" gorm:"column:seq;uniqueIndex:idx_board_revisions_seq"`
	Status    string    `json:"status" gorm:"type:varchar(20);not null;default:'applied';index:idx_board_revisions_status"`
	Author    string    `json:"author" gorm:"type:varchar(255);not null;default:''"`
	Message   string    `json:"message" gorm:"type:text;not null;default:''"`
	Ops       JSONArray `json:"ops" gorm:"type:jsonb;default:'[]'"`
	CreatedAt int64     `json:"created_at" gorm:"autoCreateTime"`
}

func (BoardRevision) TableName() string { return "board_revisions" }

// BoardHead is the single-row pointer to the currently-live applied revision.
type BoardHead struct {
	Singleton  bool   `json:"-" gorm:"primaryKey;column:singleton;default:true"`
	RevisionID string `json:"revision_id" gorm:"type:varchar(36);not null"`
}

func (BoardHead) TableName() string { return "board_head" }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./agentdb/ -run 'TestBoardRevision_OpsRoundTrip|TestBoardHead_SingleRowPointer' -v`
Expected: PASS (both tests).

- [ ] **Step 5: Add the production Postgres migration**

In `go/agentdb/migrations.go`, append this entry to the end of the `agentMigrations` slice (immediately after the `019_agent_conversation_index` entry, before the closing `}` of the slice):

```go
	{
		Name: "020_board_revisions",
		SQL: `
			CREATE TABLE IF NOT EXISTS board_revisions (
				id          VARCHAR(36) PRIMARY KEY,
				parent_id   VARCHAR(36) DEFAULT '',
				seq         BIGSERIAL UNIQUE,
				status      VARCHAR(20) NOT NULL DEFAULT 'applied',
				author      VARCHAR(255) NOT NULL DEFAULT '',
				message     TEXT NOT NULL DEFAULT '',
				ops         JSONB NOT NULL DEFAULT '[]',
				created_at  BIGINT NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_board_revisions_status ON board_revisions(status);
			CREATE TABLE IF NOT EXISTS board_head (
				singleton   BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
				revision_id VARCHAR(36) NOT NULL REFERENCES board_revisions(id)
			);
		`,
	},
```

- [ ] **Step 6: Verify build stays green**

Run: `cd go && go build ./... && go vet ./agentdb/`
Expected: no output, exit 0.

- [ ] **Step 7: Commit**

```bash
cd go && git add agentdb/board.go agentdb/board_test.go agentdb/migrations.go
git commit -m "feat(board): changeset log (board_revisions + board_head)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: The typed current-state cache (five tables)

The derived, queryable view of the current folded board: staff, event types, subscriptions, pipelines, prompt fragments. Each carries `last_changed_in` (the revision that last touched it). Subscriptions get a composite index on `(event_type, enabled)` — the Routing Manager's hot query.

**Files:**
- Modify: `go/agentdb/board.go` (append the five structs)
- Modify: `go/agentdb/board_test.go` (extend `newBoardTestStore` to migrate the five tables; add round-trip + lookup tests)
- Modify: `go/agentdb/migrations.go` (append `021_board_current`)

**Interfaces:**
- Consumes: `JSONArray`, `JSONMap` (`go/agentdb/types.go:9,42`), the Task 1 types.
- Produces (all in `package agentdb`):
  - `BoardStaff{ ID string; RoleFragments JSONArray; Skills JSONArray; ModelTier string; MemoryNamespace string; SelfArchiving JSONMap; Budget JSONMap; LastChangedIn string }` → table `board_staff`.
  - `BoardEventType{ ID string; Kind string; Description string; PayloadSchema JSONMap; LastChangedIn string }` → `board_event_types` (empty `PayloadSchema` map = no schema).
  - `BoardSubscription{ ID string; EventType string; ReactionKind string; ReactionRef string; ApplicabilityCondition string; Enabled bool; LastChangedIn string }` → `board_subscriptions`.
  - `BoardPipeline{ ID string; Description string; Stages JSONArray; LastChangedIn string }` → `board_pipelines`.
  - `BoardPromptFragment{ ID string; Kind string; Body string; LastChangedIn string }` → `board_prompt_fragments`.

- [ ] **Step 1: Write the failing test**

In `go/agentdb/board_test.go`, replace the `newBoardTestStore` helper body's `AutoMigrate` call so it migrates all seven tables, and add the new tests. The helper becomes:

```go
func newBoardTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "board_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&BoardRevision{}, &BoardHead{},
		&BoardStaff{}, &BoardEventType{}, &BoardSubscription{},
		&BoardPipeline{}, &BoardPromptFragment{},
	); err != nil {
		t.Fatalf("automigrate board: %v", err)
	}
	return &Store{gdb: db}
}
```

Add these tests to the same file:

```go
func TestBoardStaff_RoundTrip(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()
	in := &BoardStaff{
		ID:              "legal-expert",
		RoleFragments:   JSONArray(`["role-legal"]`),
		Skills:          JSONArray(`["search","summarize"]`),
		ModelTier:       "mid",
		MemoryNamespace: "legal",
		SelfArchiving:   JSONMap{"fragment_id": "archive-legal"},
		LastChangedIn:   "rev1",
	}
	if err := s.gdb.WithContext(ctx).Create(in).Error; err != nil {
		t.Fatalf("create staff: %v", err)
	}
	var got BoardStaff
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "legal-expert").Error; err != nil {
		t.Fatalf("read staff: %v", err)
	}
	if got.ModelTier != "mid" || got.MemoryNamespace != "legal" || got.LastChangedIn != "rev1" {
		t.Fatalf("staff round-trip wrong: %+v", got)
	}
	if got.SelfArchiving["fragment_id"] != "archive-legal" {
		t.Fatalf("self_archiving round-trip wrong: %+v", got.SelfArchiving)
	}
}

func TestBoardSubscription_LookupByEventEnabled(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()
	rows := []*BoardSubscription{
		{ID: "archive-on-complete", EventType: "session.completed", ReactionKind: "staff", ReactionRef: "archival-expert", Enabled: true},
		{ID: "plan-on-goal", EventType: "human-goal", ReactionKind: "pipeline", ReactionRef: "interview-plan", Enabled: true},
		{ID: "disabled-one", EventType: "session.completed", ReactionKind: "staff", ReactionRef: "old-bot", Enabled: false},
	}
	for _, r := range rows {
		if err := s.gdb.WithContext(ctx).Create(r).Error; err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}
	// The Routing Manager's hot query: enabled reactions to a given event.
	var got []BoardSubscription
	if err := s.gdb.WithContext(ctx).
		Where("event_type = ? AND enabled = ?", "session.completed", true).
		Find(&got).Error; err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 1 || got[0].ID != "archive-on-complete" {
		t.Fatalf("expected only the enabled session.completed sub, got %+v", got)
	}
}

func TestBoardEventType_EmptyPayloadSchemaIsNoSchema(t *testing.T) {
	s := newBoardTestStore(t)
	ctx := context.Background()
	if err := s.gdb.WithContext(ctx).Create(&BoardEventType{
		ID: "session.completed", Kind: "lifecycle", Description: "a worker session finished",
	}).Error; err != nil {
		t.Fatalf("create event type: %v", err)
	}
	var got BoardEventType
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "session.completed").Error; err != nil {
		t.Fatalf("read event type: %v", err)
	}
	if got.Kind != "lifecycle" || len(got.PayloadSchema) != 0 {
		t.Fatalf("expected lifecycle + empty payload schema, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./agentdb/ -run 'TestBoardStaff_RoundTrip|TestBoardSubscription_LookupByEventEnabled|TestBoardEventType_EmptyPayloadSchemaIsNoSchema' -v`
Expected: FAIL to compile — `undefined: BoardStaff`, `undefined: BoardEventType`, `undefined: BoardSubscription`.

- [ ] **Step 3: Write minimal implementation**

Append to `go/agentdb/board.go`:

```go
// BoardStaff is a reusable scope template (a "staff member"): role prompt
// fragment refs, assigned skills, model tier, memory view, and self-archiving
// strategy. Subscriptions are NOT here — they are standalone (BoardSubscription).
type BoardStaff struct {
	ID              string    `json:"id" gorm:"primaryKey;type:varchar(64)"`
	RoleFragments   JSONArray `json:"role_fragments" gorm:"type:jsonb;not null;default:'[]'"`
	Skills          JSONArray `json:"skills" gorm:"type:jsonb;not null;default:'[]'"`
	ModelTier       string    `json:"model_tier" gorm:"type:varchar(20);not null;default:'mid'"`
	MemoryNamespace string    `json:"memory_namespace" gorm:"type:varchar(255);not null;default:''"`
	SelfArchiving   JSONMap   `json:"self_archiving" gorm:"type:jsonb;not null;default:'{}'"`
	Budget          JSONMap   `json:"budget" gorm:"type:jsonb;not null;default:'{}'"`
	LastChangedIn   string    `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardStaff) TableName() string { return "board_staff" }

// BoardEventType is one entry in the org event-bus taxonomy (distinct from the
// intra-session SSE vocabulary in go/events). An empty PayloadSchema means the
// event declares no payload shape.
type BoardEventType struct {
	ID            string  `json:"id" gorm:"primaryKey;type:varchar(64)"`
	Kind          string  `json:"kind" gorm:"type:varchar(20);not null;default:'lifecycle'"`
	Description   string  `json:"description" gorm:"type:text;not null;default:''"`
	PayloadSchema JSONMap `json:"payload_schema" gorm:"type:jsonb;not null;default:'{}'"`
	LastChangedIn string  `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardEventType) TableName() string { return "board_event_types" }

// BoardSubscription is a standalone candidate binding: event -> reaction, where
// a reaction is a staff member or a pipeline. ApplicabilityCondition is the
// ReasoningBank "when X" match key. Cross-references are logical (validated
// before apply), not FK-enforced.
type BoardSubscription struct {
	ID                     string `json:"id" gorm:"primaryKey;type:varchar(64)"`
	EventType              string `json:"event_type" gorm:"type:varchar(64);not null;index:idx_board_subs_event,priority:1"`
	ReactionKind           string `json:"reaction_kind" gorm:"type:varchar(20);not null"`
	ReactionRef            string `json:"reaction_ref" gorm:"type:varchar(64);not null"`
	ApplicabilityCondition string `json:"applicability_condition" gorm:"type:text;not null;default:''"`
	// NB: no `default:true` in the gorm tag on purpose — gorm omits zero-valued
	// fields that carry a default tag, which would silently turn an explicit
	// Enabled:false into true. The production SQL keeps DEFAULT TRUE for raw
	// inserts; gorm callers set Enabled explicitly.
	Enabled       bool   `json:"enabled" gorm:"not null;index:idx_board_subs_event,priority:2"`
	LastChangedIn string `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardSubscription) TableName() string { return "board_subscriptions" }

// BoardPipeline is a named ordered sequence of stages run by run_pipeline. The
// stage inner schema is left as opaque JSON until run_pipeline is specified.
type BoardPipeline struct {
	ID            string    `json:"id" gorm:"primaryKey;type:varchar(64)"`
	Description   string    `json:"description" gorm:"type:text;not null;default:''"`
	Stages        JSONArray `json:"stages" gorm:"type:jsonb;not null;default:'[]'"`
	LastChangedIn string    `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardPipeline) TableName() string { return "board_pipelines" }

// BoardPromptFragment is a versioned prompt fragment composed into worker
// prompts at dispatch. Its version is the board revision it lives in (no
// separate version field).
type BoardPromptFragment struct {
	ID            string `json:"id" gorm:"primaryKey;type:varchar(64)"`
	Kind          string `json:"kind" gorm:"type:varchar(20);not null;default:'role'"`
	Body          string `json:"body" gorm:"type:text;not null;default:''"`
	LastChangedIn string `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardPromptFragment) TableName() string { return "board_prompt_fragments" }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./agentdb/ -run 'TestBoardStaff_RoundTrip|TestBoardSubscription_LookupByEventEnabled|TestBoardEventType_EmptyPayloadSchemaIsNoSchema' -v`
Expected: PASS (all three).

- [ ] **Step 5: Add the production Postgres migration**

In `go/agentdb/migrations.go`, append after the `020_board_revisions` entry:

```go
	{
		Name: "021_board_current",
		SQL: `
			CREATE TABLE IF NOT EXISTS board_staff (
				id               VARCHAR(64) PRIMARY KEY,
				role_fragments   JSONB NOT NULL DEFAULT '[]',
				skills           JSONB NOT NULL DEFAULT '[]',
				model_tier       VARCHAR(20) NOT NULL DEFAULT 'mid',
				memory_namespace VARCHAR(255) NOT NULL DEFAULT '',
				self_archiving   JSONB NOT NULL DEFAULT '{}',
				budget           JSONB NOT NULL DEFAULT '{}',
				last_changed_in  VARCHAR(36) NOT NULL DEFAULT ''
			);
			CREATE TABLE IF NOT EXISTS board_event_types (
				id              VARCHAR(64) PRIMARY KEY,
				kind            VARCHAR(20) NOT NULL DEFAULT 'lifecycle',
				description     TEXT NOT NULL DEFAULT '',
				payload_schema  JSONB NOT NULL DEFAULT '{}',
				last_changed_in VARCHAR(36) NOT NULL DEFAULT ''
			);
			CREATE TABLE IF NOT EXISTS board_subscriptions (
				id                      VARCHAR(64) PRIMARY KEY,
				event_type              VARCHAR(64) NOT NULL,
				reaction_kind           VARCHAR(20) NOT NULL,
				reaction_ref            VARCHAR(64) NOT NULL,
				applicability_condition TEXT NOT NULL DEFAULT '',
				enabled                 BOOLEAN NOT NULL DEFAULT TRUE,
				last_changed_in         VARCHAR(36) NOT NULL DEFAULT ''
			);
			CREATE INDEX IF NOT EXISTS idx_board_subs_event ON board_subscriptions(event_type, enabled);
			CREATE TABLE IF NOT EXISTS board_pipelines (
				id              VARCHAR(64) PRIMARY KEY,
				description     TEXT NOT NULL DEFAULT '',
				stages          JSONB NOT NULL DEFAULT '[]',
				last_changed_in VARCHAR(36) NOT NULL DEFAULT ''
			);
			CREATE TABLE IF NOT EXISTS board_prompt_fragments (
				id              VARCHAR(64) PRIMARY KEY,
				kind            VARCHAR(20) NOT NULL DEFAULT 'role',
				body            TEXT NOT NULL DEFAULT '',
				last_changed_in VARCHAR(36) NOT NULL DEFAULT ''
			);
		`,
	},
```

- [ ] **Step 6: Verify the full agentdb suite + build**

Run: `cd go && go build ./... && go test ./agentdb/ -v`
Expected: build clean; all agentdb tests PASS (the new board tests plus the pre-existing ones).

- [ ] **Step 7: Commit**

```bash
cd go && git add agentdb/board.go agentdb/board_test.go agentdb/migrations.go
git commit -m "feat(board): typed current-state tables (staff/events/subs/pipelines/fragments)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: The `Board` aggregate + `BoardStore` seam

A read-side aggregate (`Board`) holding one revision's full state, and the `BoardStore` interface that later specs implement (Postgres now, git-backed swap later). Interface shape only — no production implementation. The test proves the interface is implementable and the aggregate composes the entity types.

**Files:**
- Modify: `go/agentdb/board.go` (append `Board` struct + `BoardStore` interface)
- Modify: `go/agentdb/board_test.go` (add an aggregate composition test + a compile-time interface-satisfaction check using a test-only stub)

**Interfaces:**
- Consumes: all Task 1 + Task 2 types, `context.Context`.
- Produces:
  - `type Board struct { Revision string; Staff []BoardStaff; EventTypes []BoardEventType; Subscriptions []BoardSubscription; Pipelines []BoardPipeline; Fragments []BoardPromptFragment }`.
  - `type BoardStore interface { Current(ctx context.Context) (Board, error); AsOf(ctx context.Context, revisionID string) (Board, error); Head(ctx context.Context) (string, error); Append(ctx context.Context, cs Changeset) (revisionID string, err error) }`.

- [ ] **Step 1: Write the failing test**

Add to `go/agentdb/board_test.go`:

```go
// nopBoardStore is a test-only stub proving BoardStore is implementable. It is
// NOT a production implementation (that lands in a later spec).
type nopBoardStore struct{}

func (nopBoardStore) Current(ctx context.Context) (Board, error)            { return Board{}, nil }
func (nopBoardStore) AsOf(ctx context.Context, revisionID string) (Board, error) { return Board{Revision: revisionID}, nil }
func (nopBoardStore) Head(ctx context.Context) (string, error)             { return "", nil }
func (nopBoardStore) Append(ctx context.Context, cs Changeset) (string, error) { return "", nil }

// Compile-time check: nopBoardStore satisfies BoardStore.
var _ BoardStore = nopBoardStore{}

func TestBoard_AggregateComposesEntities(t *testing.T) {
	b := Board{
		Revision:      "rev1",
		Staff:         []BoardStaff{{ID: "legal-expert"}},
		EventTypes:    []BoardEventType{{ID: "session.completed"}},
		Subscriptions: []BoardSubscription{{ID: "archive-on-complete", EventType: "session.completed"}},
		Pipelines:     []BoardPipeline{{ID: "interview-plan"}},
		Fragments:     []BoardPromptFragment{{ID: "role-legal"}},
	}
	if b.Revision != "rev1" || len(b.Staff) != 1 || len(b.Subscriptions) != 1 {
		t.Fatalf("aggregate did not compose entities: %+v", b)
	}

	var store BoardStore = nopBoardStore{}
	got, err := store.AsOf(context.Background(), "rev9")
	if err != nil {
		t.Fatalf("AsOf: %v", err)
	}
	if got.Revision != "rev9" {
		t.Fatalf("expected AsOf to echo revision, got %q", got.Revision)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./agentdb/ -run TestBoard_AggregateComposesEntities -v`
Expected: FAIL to compile — `undefined: Board`, `undefined: BoardStore`.

- [ ] **Step 3: Write minimal implementation**

Append to `go/agentdb/board.go` (add `"context"` to the import block — it becomes `import (\n\t"context"\n\t"encoding/json"\n)`):

```go
// Board is the fully-folded state of the board at one revision — the read-side
// aggregate returned by BoardStore.Current / AsOf.
type Board struct {
	Revision      string
	Staff         []BoardStaff
	EventTypes    []BoardEventType
	Subscriptions []BoardSubscription
	Pipelines     []BoardPipeline
	Fragments     []BoardPromptFragment
}

// BoardStore is the seam over the architecture board. The implementation is
// Postgres-backed (a later spec); a git-backed implementation is a future swap.
// Current/AsOf read folded state; Head returns the live applied revision id;
// Append writes a changeset and returns the new revision id.
type BoardStore interface {
	Current(ctx context.Context) (Board, error)
	AsOf(ctx context.Context, revisionID string) (Board, error)
	Head(ctx context.Context) (revisionID string, err error)
	Append(ctx context.Context, cs Changeset) (revisionID string, err error)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./agentdb/ -run TestBoard_AggregateComposesEntities -v`
Expected: PASS.

- [ ] **Step 5: Verify the full build, vet, and agentdb suite**

Run: `cd go && go build ./... && go vet ./agentdb/ && go test ./agentdb/ -v`
Expected: build clean; vet silent; all agentdb tests PASS.

- [ ] **Step 6: Commit**

```bash
cd go && git add agentdb/board.go agentdb/board_test.go
git commit -m "feat(board): Board aggregate + BoardStore seam (shape only)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Reconcile ARCHITECTURE.md §6C with the deferral

`ARCHITECTURE.md` §6C states the board store decision as "literal git + a Postgres projection (open decision resolved)." This spec deferred literal git. Add a short pointer so the two documents don't silently contradict each other. Doc-only; no code.

**Files:**
- Modify: `docs/ARCHITECTURE.md` (§6C, the "Concrete stack" bullet beginning "Board format/store")

- [ ] **Step 1: Add the deferral note**

In `docs/ARCHITECTURE.md`, find the bullet starting `**Board format/store — literal git + a Postgres projection (open decision resolved).**` and append this sentence to the end of that bullet (keep the existing text):

```
 **Update (2026-06-29):** for the initial build we defer literal git and make
 Postgres the source of truth behind a `BoardStore` seam, using an immutable
 event-sourced revision log that preserves the GitOps *properties* (pinnable
 revisions, rollback, review-before-apply). Literal git becomes a later export
 or backing-store swap when the Consultant lands. See
 `docs/superpowers/specs/2026-06-29-architecture-board-data-model-design.md`.
```

- [ ] **Step 2: Verify the reference resolves**

Run: `ls docs/superpowers/specs/2026-06-29-architecture-board-data-model-design.md`
Expected: the path prints (file exists).

- [ ] **Step 3: Commit**

```bash
git add docs/ARCHITECTURE.md
git commit -m "docs(board): note git deferral in ARCHITECTURE §6C

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Notes for the implementer

- **Why two schema definitions?** The gorm structs drive sqlite `AutoMigrate` in tests; the SQL in `migrations.go` is what actually runs in production Postgres. They are kept consistent by hand — this is the existing codebase convention (see `Skill` in `types.go:168` vs `013_agent_skills` in `migrations.go`). When you change one, change the other.
- **Why no FK constraints between board tables?** Cross-references (`reaction_ref`, `event_type`, `role_fragments`) are validated *before* a changeset is applied (a gate concern), not by the DB — FK enforcement would fight the event-sourced apply order and the future git swap. Only `board_head.revision_id → board_revisions.id` is a real FK.
- **Why is `Seq` not `autoIncrement` in the struct?** sqlite (test backend) does not honor `autoIncrement` on a non-primary-key column. Production Postgres backs `seq` with `BIGSERIAL` (migration 020). The fold contract is "ascending `seq`"; the later apply implementation assigns it.
- **What is deliberately NOT here:** the fold/apply logic (`Current`/`AsOf`/`Head`/`Append` implementations), changeset validation, canary pointers, and any git integration. Those are later specs (§8 of the design doc).
```
