# v1 Contracts — the frozen interfaces (the keystone)

**Date:** 2026-06-30
**Status:** THE shared contract for the v1 build. Every slice plan (A–F) **consumes** these types
and interfaces and **must not redefine or renegotiate them.** If a slice needs a contract changed,
that is a stop-and-escalate event, not a local edit — a contract change ripples to every slice.
**Reading order:** `AGENTS_DESIGN.md` → `objectives-and-build-path` → `execution-coordination-model`
→ `v1-deployment-plan` → this. Grounded on Slice 0 (`go/orchestrator`).

---

## 0. How to use this document

- These are **contracts, not implementations** — signatures and type shapes, frozen.
- Each slice plan declares, per task, which contracts it **Consumes** and which it **Produces/implements**.
- Slice 0 already ships some of these (`Model`, `BoardStore`, `Runner`, `Telemetry`, `Scope` minimal,
  `ApplyFeedback`). v1 **evolves** them as specified below; evolutions are marked **(evolves Slice 0)**.
- **Package:** all new code under `go/orchestrator/` (module `github.com/binocarlos/badcode-agent-orange`),
  matching Slice 0's idiom (stdlib + `agentdb`; gorm only in the Postgres impls; table-driven tests).

## 1. v1 scope boundary — build these, NOT the deferred layer

**In v1:** tick-driven manager loop · Postgres board (fragments + tickets + telemetry) · real model
behind the seam · fire-and-forget workers via the `WorkerRuntime` seam · one `Connector` behind an
un-bypassable approval gate · the watch/approve/note HTTP API + surface.

**Deferred — DO NOT build (leave the seam, stub if needed):** the event bus + `emit_event` +
continuations · pipelines + `run_pipeline` · the memory store + `search_memory`/summary bots · the
autonomous Consultant · multi-channel / multi-project · snapshot-resume for stateful workers. Any
plan that reaches for these is out of scope.

## 2. v1 coordination model — tick-based board reconciliation (NO event bus)

v1 does **not** have the event bus. Coordination is the §6 manager exchange on a **trigger (cron)**:

```
Trigger fires → ManagerExchange:
  load(board, tickets)                 # re-derive all state; nothing held between ticks
  reconcile: In-Review → verify vs acceptance → Done | re-plan; clear blocked deps
  choose next work
  spawn worker(s) fire-and-forget      # WorkerRuntime.Spawn; results land on tickets
  persist; exchange ends
```

Workers are **fire-and-forget**: `WorkerRuntime.Spawn` runs a scope and, on completion, delivers a
`Result` to a `ResultSink` (which updates the ticket to In-Review). The manager sees results **on the
next tick**. Human approvals and feedback also land as board/ticket changes seen on the next tick (an
approval/feedback action MAY also fire an immediate trigger). This is the fire-and-forget model of
the execution addendum with the bus replaced by **tick reconciliation** — the bus is the deferred
generalization.

## 3. Shared vocabulary (frozen strings)

```go
// TicketStatus — the board lanes (ARCHITECTURE.md §5).
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

// ModelTier — per-scope cost/capability tier (downgrade toward leaves).
type ModelTier string
const (
	TierFull  ModelTier = "full"  // manager / reasoning (Opus)
	TierMid   ModelTier = "mid"   // workers (Sonnet)
	TierCheap ModelTier = "cheap" // summary/leaf (Haiku)
)

// FragmentKind — a compose-only guidance fragment (dispatch-vs-compose: text, not routed on).
type FragmentKind string
const (
	FragmentRole      FragmentKind = "role"      // role/specialist guidance
	FragmentRouting   FragmentKind = "routing"   // manager routing guidance (feedback edits this)
	FragmentProcedure FragmentKind = "procedure" // reusable how-to
)

// ResultStatus — how a worker session ended.
type ResultStatus string
const (
	ResultDone      ResultStatus = "done"
	ResultEscalated ResultStatus = "escalated"
	ResultFailed    ResultStatus = "failed"
)
```

## 4. Core data types (frozen)

```go
// Scope — one worker/manager invocation. (evolves Slice 0: adds Tier/Tools/Budget/Parent.)
type Scope struct {
	Name     string    // scope/role name (e.g. "manager", "post-writer")
	Template string    // prompt template with {{fragment:ID}} and {{input}}
	Input    string    // text templated into the prompt (goal / prior output / ticket objective)
	Tier     ModelTier // which model tier to run on
	Tools    []string  // enforced tool allowlist (empty = no tools; NEVER irreversible at leaves)
	Budget   Budget    // depth/spawn/token caps (the resource floor)
	Parent   string    // parent session id ("" = root); depth = parent.depth + 1
	TicketID string    // the ticket this scope serves ("" for the manager exchange itself)
}

// Budget — the enforced resource floor (execution-coordination-model §7).
type Budget struct {
	MaxDepth   int   // hard cap on the parent chain (runaway recursion)
	MaxSpawns  int   // fan-out cap for THIS scope
	TreeTokens int64 // shared token budget, decremented down the whole goal-tree
}

// Result — what a worker session returns (delivered to a ResultSink; lands on the ticket).
type Result struct {
	SessionID  string
	TicketID   string
	Output     string       // the produced artifact (e.g. a draft post) OR the escalation question
	Status     ResultStatus
	TokensUsed int64
}

// Ticket — a board work item (work state; ungated; NOT in the versioned log).
type Ticket struct {
	ID          string
	ProjectID   string
	Title       string
	Objective   string          // the narrowed spec slice (= Scope.Input for the worker)
	Acceptance  string          // verifiable criteria, written at plan time (checked by a verify scope)
	Status      TicketStatus
	Scope       json.RawMessage // the Scope to invoke (opaque to the board)
	Result      json.RawMessage // the Result once In-Review
	PendingPost json.RawMessage // a Post awaiting publish approval (Needs-Human), if any
	DependsOn   []string
	Parent      string
	Attempts    int
	BoardRev    string          // the board revision the ticket's work pinned to (attribution)
	CreatedAt   int64
	UpdatedAt   int64
}

// Fragment — the compose-only guidance KV (§0 collapse: generalises board_prompt_fragments).
// v1 REUSES the Slice-0 shape agentdb.BoardPromptFragment{ID,Kind,Body,LastChangedIn}; the table
// MAY be named `fragments`. Treat BoardPromptFragment as the frozen Fragment type.

// HumanFeedback — a targeted note that drives the learning loop (execution addendum §9).
type HumanFeedback struct {
	TargetRef string // what is being critiqued: "ticket:<id>" | "run:<id>" | "fragment:<id>"
	Note      string // the human's note
}

// Post — a unit of content a Connector publishes (only via the approval gate).
type Post struct {
	Channel string
	Text    string
	Media   []string // paths/urls; empty for v1 text-only
}
```

## 5. The seams (frozen interfaces)

```go
// Model — prompt in, text out. (Slice 0, unchanged.) Real impl = Anthropic API (Slice B).
type Model interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// ModelRouter — resolves a tier to a Model (Slice B). Lets a Scope pick cost/capability.
type ModelRouter interface {
	For(tier ModelTier) Model
}

// SpendMeter — the monthly spend ceiling (Slice B). Charge errors when the ceiling is hit,
// which HALTS dispatch (the cost floor, sibling to depth/spawn caps). Enforced in mechanism.
type SpendMeter interface {
	Charge(ctx context.Context, tokens int64, usd float64) error
	Spent(ctx context.Context) (usd float64, err error)
}

// BoardStore — the versioned board (Slice 0 interface, unchanged). Postgres impl = Slice A.
// Board now also folds Tickets? NO — tickets are work state, queried via TicketStore, not folded.
type BoardStore interface {
	Current(ctx context.Context) (Board, error)
	AsOf(ctx context.Context, revisionID string) (Board, error)
	Head(ctx context.Context) (revisionID string, err error)
	Append(ctx context.Context, cs Changeset) (revisionID string, err error)
}

// TicketStore — work-state CRUD (Slice A). Ungated; not versioned.
type TicketStore interface {
	Create(ctx context.Context, t Ticket) (id string, err error)
	Update(ctx context.Context, t Ticket) error
	Get(ctx context.Context, id string) (Ticket, error)
	List(ctx context.Context, status TicketStatus) ([]Ticket, error) // status "" = all
}

// WorkerRuntime — provisions and runs one scope, fire-and-forget (§7). dev impl = in-process
// (Slice C); prod impl = existing agentkit DinD on GKE (Slice F). On completion the runtime
// delivers a Result to the ResultSink it was constructed with.
type WorkerRuntime interface {
	Spawn(ctx context.Context, s Scope) (sessionID string, err error)
}

// ResultSink — where a finished worker's Result is delivered (Slice C: updates the ticket to
// In-Review). The WorkerRuntime holds one; the orchestrator implements it.
type ResultSink interface {
	Deliver(ctx context.Context, r Result) error
}

// Connector — publishes a Post to a real channel (Slice D). The ONLY network-to-the-world seam.
// Invoked EXCLUSIVELY by the approval flow — never by a worker scope directly.
type Connector interface {
	Publish(ctx context.Context, p Post) (ref string, err error)
}

// Telemetry — append-only run log (Slice 0). v1 keeps Record/Runs; the store gets a Postgres
// impl in Slice A (same interface). This is the "show your work" substrate.
type Telemetry interface {
	Record(r Run) Run
	Runs() []Run
}
```

## 6. The v1 syscall / tool surface (what scopes may do)

v1 has a **reduced** surface (no bus/pipelines/memory). The manager exchange (Go code, not a scope
tool) drives these; worker scopes expose only the thread syscalls.

- **Thread syscalls** (a worker scope): `job_finished(Result)`, `escalate_to_human(text)`.
- **Manager orchestration** (Go, in the exchange): `TicketStore.Create/Update`, `WorkerRuntime.Spawn`.
- **Policy** (the feedback path, Go): `write_fragment(id, body)` = `BoardStore.Append` of an
  `OpUpdate prompt_fragment` (this is exactly Slice 0's `ApplyFeedback`, generalised).
- **Publishing** is NOT a scope tool: a worker produces a draft → a Needs-Human ticket → human
  approval → the orchestrator calls `Connector.Publish`. The gate is in mechanism.

## 7. The floors (enforced in mechanism, non-editable)

1. **Recursion:** `depth ≤ Budget.MaxDepth`, per-scope spawns `≤ Budget.MaxSpawns`, and the shared
   `Budget.TreeTokens` decremented down the tree — checked by the spawn path; exceed → refuse.
2. **Spend:** `SpendMeter.Charge` errors at the monthly ceiling → dispatch halts.
3. **Publish-approval:** `Connector.Publish` is reachable ONLY from the approval action on a
   Needs-Human ticket. A worker cannot publish. This is the single most important v1 safety property.
4. **Fail-loud resume/rehydrate** (when the DinD runtime lands, Slice F): a session that cannot
   restore its conversation fails to a Needs-Human ticket rather than running amnesiac.

## 8. The watch/approve/note HTTP API (Slice E; the web is a client of this)

```
GET  /api/tickets?status=needs_human      → []Ticket (pending approvals + escalations)
POST /api/tickets/{id}/approve            → approves a PendingPost → orchestrator calls Connector.Publish
POST /api/tickets/{id}/reject   {note?}   → rejects; optional note becomes HumanFeedback
POST /api/feedback  {target_ref, note}    → HumanFeedback → write_fragment (the learning loop)
GET  /api/board/revisions                 → []Revision (the story timeline: author, message, ts)
GET  /api/board/current                   → Board (folded fragments)
GET  /api/runs                            → []Run (telemetry: scope, board_rev, output)
POST /api/trigger                         → fire one ManagerExchange now (else cron drives it)
```

Payloads are JSON of the §4 types. Auth for v1: a single shared token (single-tenant, one operator).

## 9. Slice ownership map (who implements what — the boundaries)

| Slice | Implements (Produces) | Consumes (from contracts) |
|---|---|---|
| **A** Postgres board | `BoardStore`, `TicketStore`, `Telemetry` (Postgres); migrations; §0 collapse | Board/Changeset/Op/Ticket/Run types |
| **B** real model | `ModelRouter`, Anthropic `Model` impl, `SpendMeter` | `Model`, `ModelTier` |
| **C** manager loop | `ManagerExchange`, in-proc `WorkerRuntime`, `ResultSink`, verify-scope, `write_fragment` | Scope/Ticket/Result/Budget, BoardStore, TicketStore, ModelRouter, WorkerRuntime, ResultSink |
| **D** connector + gate | `Connector` (one channel) + fake; the approval→publish flow; PendingPost lifecycle | Connector, Post, Ticket, TicketStore |
| **E** watch surface | the HTTP API (§8) + web client; feedback wiring | all read types; BoardStore, TicketStore, Telemetry, HumanFeedback |
| **F** deploy | agentkit-DinD `WorkerRuntime` impl; compose/secret/config; run live | WorkerRuntime, Scope, the floors |

## 10b. Second-pass reconciliation (post-fan-out, 2026-06-30)

The six slice-planning agents surfaced these gaps. All resolved as **central contract fixes** below
(apply before implementation) — except **one open decision** (D1), which needs the author.

**Foundational (a pre-slice task — land FIRST, everyone consumes it):**
- **F-1 · `go/orchestrator/contracts.go` owns all shared declarations.** Every enum (§3), core type
  (§4), and interface (§5) is declared **once** here. Flagged by B, C, D, E (unanimous). No slice
  redeclares `ModelTier`/`TicketStatus`/`Ticket`/etc.
- **F-2 · Board aggregate cleanup.** The §0 collapse drops `board_staff`/`board_pipelines`/
  `board_event_types`; remove their fields from the `Board` struct + models. **`board_subscriptions`
  is KEPT** (real dispatch per §0; unused in v1, not dropped).

**Interface evolutions (small, ripple to Slice 0 — do them with F-1):**
- **E-1 · `Telemetry` gets ctx+error:** `Record(ctx, Run) (Run, error)`, `Runs(ctx) ([]Run, error)`.
  No silent telemetry loss (fail-loud). Update Slice 0's `Runner`/`telemetry.go`.
- **E-2 · `BoardStore` gains `Revisions(ctx) ([]BoardRevision, error)`** (the story timeline; §8
  needs it). `MemBoard` + `PgBoard` implement. Add a `RevisionDTO` wire type for §8.
- **E-3 · `Scope` gains `Prompt string` and `Depth int`.** **Composition is orchestrator-side**
  (resolves the C↔F ownership clash): the manager runs `Compose` and passes the finished `Scope.Prompt`
  to `WorkerRuntime.Spawn`; the runtime just runs the prompt (identical for in-proc and DinD, no board
  access in the runtime). `Depth` carries `parent.depth+1` for the floor.
- **E-4 · `Ticket` gains `PublishedRef string`** (persist the connector's returned ref).
- **E-5 · `Post` gains an idempotency key** (= ticket id) so a publish retry can't double-post.

**Frozen shapes that were missing (add to §4/§5):**
- **S-1 · `Verdict{ Pass bool; Reason string }`** — the verify-scope output (the credit-assignment
  signal; owned by Slice C, consumed by the manager reconcile).
- **S-2 · `Triggerer` interface** — `Tick(ctx) error`; the `ManagerExchange` satisfies it and §8
  `/api/trigger` binds to it (reconciles C↔E).
- **S-3 · `FeedbackApplier` (owned by C)** resolves `HumanFeedback.TargetRef → fragment id`. v1 rule:
  `fragment:<id>` → direct; `ticket:<id>` / `run:<id>` → default to the `routing-guidance` fragment.
- **S-4 · Worker completion convention:** a worker signals result/escalation via a recognized output
  form; the `WorkerRuntime` (in-proc AND DinD) constructs the `Result` (maps output → `Status`). The
  runtime owns Result-construction (reconciles C's `ESCALATE:` convention with F's SSE parsing).

**Accepted local defaults (no contract change):** reject→`StatusTodo` + clear `PendingPost` (D+E
agree); reject-note surfaces as `HumanFeedback` (no auto-write in v1); `Post.Media` deferred
(text-only v1); `Ticket.ProjectID` kept (forward-compat); `Budget.TreeTokens` tracked in a shared
per-tree-root ledger (not copied per scope); single-writer seq via mutex `MAX(seq)+1`; auth = a single
`Authorization: Bearer` token; `ModelRouter.For` returns a fail-loud error model for an unknown tier;
model IDs env-configured (`AGENTKIT_MODEL_FULL/_MID/_CHEAP`).

**Deferred-but-flagged (not v1, must fix before the feature lands):** `Scope.Tools` allowlist is
unwired into agentkit's `CreateSessionRequest` (inert for draft-only v1; blocks tool-using scopes);
`SpendMeter` is soft real-time on the DinD path (a live turn can overshoot; fine at small budgets).

**D1 · RESOLVED (2026-06-30): DinD harness for v1 workers.** The author chose to reuse the known-good
agentkit Docker-in-Docker runtime. Consequences, now frozen:
- **Two model paths coexist, both feeding one `SpendMeter`:**
  - *Orchestrator-side scopes* (the `ManagerExchange`, the verify-scope, the feedback reviser) run
    **in-process** and call the `Model` seam (Slice B) directly → **precise** tier + metering.
  - *Worker scopes* run in **DinD**; the in-image harness calls Anthropic itself → the Slice-F
    adapter passes the tier's model id **into the container** and **charges the `SpendMeter` from the
    harness usage frames** (soft granularity; a live turn can overshoot — acceptable at small v1
    budgets).
- **Slice F's DinD `WorkerRuntime` is IN v1** (not deferred).
- **New Slice-F sub-tasks:** add a **model-id field to `CreateSessionRequest`** (so tier is honored
  in the container); wire the usage-frame → `SpendMeter.Charge` path.
- **Composition stays orchestrator-side** (E-3): the manager composes `Scope.Prompt` and passes it to
  DinD via `CreateSession`; the harness runs the finished prompt.
- `Scope.Tools` remains unwired in `CreateSessionRequest` — inert for v1 draft-only workers; **must be
  wired before any tool-using worker scope** (deferred-but-flagged).

## 10. Conventions (match Slice 0 + agentdb)

- Numbered idempotent SQL migrations after existing `021_*`; gorm/Postgres; BIGINT epoch ts; JSONB
  bodies; VARCHAR ids. Tickets/telemetry are new tables; the §0 collapse drops
  `board_staff`/`board_pipelines`/`board_event_types` and (optionally) renames
  `board_prompt_fragments` → `fragments`.
- TDD, table-driven tests, frequent commits, `go build ./...` + `go vet ./...` green.
- One impl per seam until a real need forces a second (§14.3). No external deps beyond what a seam's
  real impl genuinely requires (Anthropic SDK for Slice B; the channel SDK for Slice D).
- Reuse Slice 0 (`MemBoard` stays as the test double / dev impl behind `BoardStore`).
