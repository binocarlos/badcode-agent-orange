package orchestrator

// contracts.go is the single home for the v1 shared vocabulary, core data types,
// and seams (the frozen interfaces of docs/superpowers/specs/2026-06-30-v1-contracts.md,
// §3/§4/§5, plus the §10b reconciliation shapes). Every v1 slice CONSUMES these and
// must not redeclare or renegotiate them: a contract change ripples to every slice
// and is a stop-and-escalate event, not a local edit (contracts.md §0).
//
// The versioned-board types stay canonical in package agentdb (Board, Changeset,
// Op, BoardRevision, BoardPromptFragment, BoardStore) — the contract treats
// agentdb.BoardPromptFragment as the frozen Fragment type and agentdb.BoardStore as
// the board seam (v1-contracts §4/§5). This file owns the orchestrator-level decls.

import (
	"context"
	"encoding/json"
	"strings"
)

// ── §3 Shared vocabulary (frozen strings) ────────────────────────────────────

// TicketStatus is a board lane (ARCHITECTURE.md §5).
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

// ModelTier is a per-scope cost/capability tier (downgrade toward the leaves).
type ModelTier string

const (
	TierFull  ModelTier = "full"  // manager / reasoning (Opus)
	TierMid   ModelTier = "mid"   // workers (Sonnet)
	TierCheap ModelTier = "cheap" // summary / leaf (Haiku)
)

// FragmentKind classifies a compose-only guidance fragment (dispatch-vs-compose:
// fragments are text the model reads, never something the runtime routes on).
type FragmentKind string

const (
	FragmentRole      FragmentKind = "role"      // role/specialist guidance
	FragmentRouting   FragmentKind = "routing"   // manager routing guidance (feedback edits this)
	FragmentProcedure FragmentKind = "procedure" // reusable how-to
)

// ResultStatus is how a worker session ended.
type ResultStatus string

const (
	ResultDone      ResultStatus = "done"
	ResultEscalated ResultStatus = "escalated"
	ResultFailed    ResultStatus = "failed"
)

// ── §4 Core data types (frozen) ──────────────────────────────────────────────

// Scope is one manager/worker invocation. (Evolves Slice 0: adds Tier/Tools/Budget/
// Parent/TicketID and — §10b E-3 — Prompt/Depth.) Composition is orchestrator-side:
// the manager runs Compose and passes the finished Prompt to WorkerRuntime.Spawn, so
// the runtime never touches the board (identical for in-proc and DinD).
type Scope struct {
	Name     string    // scope/role name (e.g. "manager", "post-writer")
	Template string    // prompt template with {{fragment:ID}} and {{input}}
	Input    string    // text templated into the prompt (goal / prior output / ticket objective)
	Prompt   string    // §10b E-3: the composed prompt handed to the runtime (orchestrator composes)
	Tier     ModelTier // which model tier to run on
	Tools    []string  // enforced tool allowlist (empty = no tools; never irreversible at leaves)
	Budget   Budget    // depth/spawn/token caps (the resource floor)
	Parent   string    // parent session id ("" = root)
	Depth    int       // §10b E-3: parent.depth+1, carried for the recursion floor
	TicketID string    // the ticket this scope serves ("" for the manager exchange itself)
}

// Budget is the enforced resource floor (execution-coordination-model §7).
type Budget struct {
	MaxDepth   int   // hard cap on the parent chain (runaway recursion)
	MaxSpawns  int   // fan-out cap for THIS scope
	TreeTokens int64 // shared token budget, decremented down the whole goal-tree
}

// Result is what a worker session returns (delivered to a ResultSink; lands on the ticket).
type Result struct {
	SessionID  string
	TicketID   string
	Output     string // the produced artifact (e.g. a draft post) OR the escalation question
	Status     ResultStatus
	TokensUsed int64
}

// Ticket is a board work item (work state; ungated; NOT in the versioned log).
type Ticket struct {
	ID           string
	ProjectID    string
	Title        string
	Objective    string // the narrowed spec slice (= Scope.Input for the worker)
	Acceptance   string // verifiable criteria, written at plan time (checked by a verify scope)
	Status       TicketStatus
	Scope        json.RawMessage // the Scope to invoke (opaque to the board)
	Result       json.RawMessage // the Result once In-Review
	PendingPost  json.RawMessage // a Post awaiting publish approval (Needs-Human), if any
	PublishedRef string          // §10b E-4: the connector's returned ref once published
	DependsOn    []string
	Parent       string
	Attempts     int
	BoardRev     string // the board revision the ticket's work pinned to (attribution)
	CreatedAt    int64
	UpdatedAt    int64
}

// HumanFeedback is a targeted note that drives the learning loop (execution addendum §9).
type HumanFeedback struct {
	TargetRef string // what is being critiqued: "ticket:<id>" | "run:<id>" | "fragment:<id>"
	Note      string // the human's note
}

// Post is a unit of content a Connector publishes (only via the approval gate).
// §10b E-5: DedupeKey (= the originating ticket id) makes a publish retry idempotent
// so a redelivered approval can never double-post.
type Post struct {
	Channel   string
	Text      string
	Media     []string // paths/urls; empty for v1 text-only
	DedupeKey string   // idempotency key (= ticket id); a retry with the same key must not double-post
}

// Verdict is the verify-scope output (§10b S-1): the credit-assignment signal the
// manager reconcile reads to move a ticket In-Review → Done or back to re-plan.
type Verdict struct {
	Pass   bool
	Reason string
}

// RevisionDTO is the §8 wire shape for the board-revision timeline (the "show your
// work" story). Derived from an agentdb.BoardRevision by the HTTP layer (Slice E).
type RevisionDTO struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Message string `json:"message"`
	Ts      int64  `json:"ts"`
}

// ── §5 The seams (frozen interfaces) ─────────────────────────────────────────

// Model turns a composed prompt into text. Slice 0 uses ScriptedModel; the real
// impl (Slice B) wraps the Anthropic API.
type Model interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// ModelRouter resolves a tier to a Model (Slice B), letting a Scope pick cost/capability.
// For an unknown tier the impl returns a fail-loud error model (§10b accepted default).
type ModelRouter interface {
	For(tier ModelTier) Model
}

// SpendMeter enforces the monthly spend ceiling (Slice B): Charge errors once the
// ceiling is hit, which HALTS dispatch (the cost floor, sibling to depth/spawn caps).
type SpendMeter interface {
	Charge(ctx context.Context, tokens int64, usd float64) error
	Spent(ctx context.Context) (usd float64, err error)
}

// TicketStore is work-state CRUD (Slice A). Ungated; not versioned. List status ""
// returns all.
type TicketStore interface {
	Create(ctx context.Context, t Ticket) (id string, err error)
	Update(ctx context.Context, t Ticket) error
	Get(ctx context.Context, id string) (Ticket, error)
	List(ctx context.Context, status TicketStatus) ([]Ticket, error)
}

// WorkerRuntime provisions and runs one scope, fire-and-forget (§7). Dev impl =
// in-process (Slice C); prod impl = the existing agentkit DinD on GKE (Slice F). On
// completion the runtime delivers a Result to the ResultSink it was constructed with.
type WorkerRuntime interface {
	Spawn(ctx context.Context, s Scope) (sessionID string, err error)
}

// ResultSink is where a finished worker's Result is delivered (Slice C updates the
// ticket to In-Review). The WorkerRuntime holds one; the orchestrator implements it.
type ResultSink interface {
	Deliver(ctx context.Context, r Result) error
}

// Connector publishes a Post to a real channel (Slice D) — the ONLY network-to-the-
// world seam. Invoked EXCLUSIVELY by the approval flow, never by a worker scope: the
// publish-approval gate (v1-contracts §7.3) is the single most important safety property.
type Connector interface {
	Publish(ctx context.Context, p Post) (ref string, err error)
}

// Telemetry is the append-only run log (Slice 0; Postgres impl in Slice A) — the
// "show your work" substrate. §10b E-1: ctx+error so telemetry loss fails loud.
type Telemetry interface {
	Record(ctx context.Context, r Run) (Run, error)
	Runs(ctx context.Context) ([]Run, error)
}

// Triggerer fires one manager exchange (§10b S-2). The ManagerExchange (Slice C)
// satisfies it; the §8 POST /api/trigger endpoint (Slice E) binds to it.
type Triggerer interface {
	Tick(ctx context.Context) error
}

// FeedbackApplier resolves a HumanFeedback.TargetRef to a fragment and applies the
// note as a write_fragment (§10b S-3; owned by Slice C). v1 resolution rule:
// "fragment:<id>" → that id directly; "ticket:<id>" / "run:<id>" → the routing-guidance
// fragment. It returns the new board revision id (the learning loop's audit anchor).
type FeedbackApplier interface {
	Apply(ctx context.Context, fb HumanFeedback) (revisionID string, err error)
}

// ── §10b S-4 Worker-completion convention ────────────────────────────────────

// EscalatePrefix marks a worker's output as an escalation to a human rather than a
// finished artifact. Both worker runtimes (in-proc Slice C and DinD Slice F) share
// this one convention so Result-construction is identical regardless of transport.
const EscalatePrefix = "ESCALATE:"

// ClassifyWorkerOutput maps a worker's raw output to a completion status and the
// cleaned text (§10b S-4). An ESCALATE:-prefixed output is an escalation; anything
// else is a finished artifact. Failure is set by the runtime on its own error paths,
// not derived from output, so it is deliberately not inferred here.
func ClassifyWorkerOutput(raw string) (status ResultStatus, text string) {
	if rest, ok := strings.CutPrefix(raw, EscalatePrefix); ok {
		return ResultEscalated, strings.TrimSpace(rest)
	}
	return ResultDone, raw
}
