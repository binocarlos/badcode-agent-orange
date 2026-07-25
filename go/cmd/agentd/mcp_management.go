package main

// mcp_management.go — the core MANAGEMENT MCP tools (spec 05-management-tools
// §9), registered onto the host MCP server in mcpserver.go.
//
// These are the tools with which a worker manages the organisation:
//
//	worker_list()                                    → the workforce
//	worker_create(name, description, system_prompt, …) → hire (§8.8)
//	worker_update(name, fields, rationale?)          → retune the NON-prompt fields
//	worker_prompt_read(name)  / worker_prompt_write(name, system_prompt, rationale)
//	project_prompt_read()     / project_prompt_write(system_prompt, rationale)
//	subscription_list/create/delete                  → rewire what triggers whom (§8.3)
//	schedule_list/create/update/delete               → retune the cadence (§8.6)
//	request_human_attention(message, expires_in?)    → the human-in-the-loop primitive
//
// # Why worker_prompt_write is the point of the whole spec
//
// §8.7 is the definition of done: an answerer worker replies to email, a
// reviewer worker reads the transcript and REWRITES THE ANSWERER'S SYSTEM
// PROMPT. That single act is the self-improvement loop, and this file is where
// it lives. Everything else here exists so the loop has an organisation to
// improve.
//
// It recurses with nothing added to core: because every prompt rewrite lands in
// memory as a `kind=prompt-revision` record, the PATTERN of rewrites is itself
// searchable evidence, so a second-layer consultant can study how a first-layer
// consultant has been revising prompts — and steer it by rewriting the
// consultant's prompt, with these same two tools.
//
// # The prompt-revision memory (§9, §15.5)
//
// A prompt write does three things, in this order:
//
//  1. writes the new prompt through the dedicated store method, which appends a
//     `worker_prompt_write` / `project_prompt_write` config event carrying the
//     rationale in ONE transaction (§15.4);
//  2. reads the stored row back and echoes it (§9);
//  3. appends a memory labelled `kind=prompt-revision` containing the RATIONALE
//     and the PREVIOUS prompt.
//
// That memory is provenance and manual rollback — a human or a worker can find
// the superseded text and write it back with an ordinary prompt write (§15.7).
// It is NOT a versioning feature and there is no restore verb; the config log is
// the authoritative history and the memory is the searchable one.
//
// Step 3 can fail without unwriting step 1 (they are different substrates), so a
// failed memory is reported IN the successful result rather than raised as an
// error: telling the model "that failed" when the prompt is live would be the
// more dangerous lie. See promptRevision.
//
// # rationale
//
// REQUIRED and validated non-empty on the two prompt writes, optional on every
// other mutation (§15.5). Core checks non-empty and nothing else — judging
// whether a rationale is *good* is a reviewing worker's prompt's job (P1).
//
// # What is deliberately NOT here
//
// No approval gate, no draft queue, no pending-items UI, no `worker_delete`
// tool, no `restore_project` verb. §9 closes with "we deleted the approval
// engine deliberately; do not re-grow it" — review-before-apply is a worker
// arrangement (a proposer writing `kind=prompt-proposal` memories and a
// gatekeeper applying them), not machinery in core.
//
// And no tool takes a project parameter: the project is the session token's
// `customer` claim, applied in code (D3's rule).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/extension/embedding"
)

// managementStore is the narrow slice of *agentdb.Store these tools need.
//
// Note what is NOT in it: no DeleteWorker (§9 gives the tools no worker_delete —
// retiring a worker is `worker_update(name, {enabled:false})`, which keeps its
// history and its sessions), and no PutProjectSettings (the whole-object
// settings write is the HTTP/UI surface of §5, not a management tool).
type managementStore interface {
	// workers
	ListWorkers(ctx context.Context, project string) ([]*agentdb.Worker, error)
	GetWorker(ctx context.Context, project, name string) (*agentdb.Worker, error)
	UpsertWorker(ctx context.Context, w *agentdb.Worker, cw agentdb.ConfigWrite) (*agentdb.Worker, error)
	SetWorkerPrompt(ctx context.Context, project, name, prompt string, cw agentdb.ConfigWrite) (*agentdb.Worker, string, error)
	// project prompt
	GetProjectSettings(ctx context.Context, project string) (*agentdb.ProjectSettings, error)
	SetProjectPrompt(ctx context.Context, project, prompt string, cw agentdb.ConfigWrite) (*agentdb.ProjectSettings, string, error)
	// subscriptions (§8.3 — list/create/delete only; there is no subscription_update tool)
	ListSubscriptions(ctx context.Context, project string) ([]*agentdb.Subscription, error)
	GetSubscription(ctx context.Context, project, id string) (*agentdb.Subscription, error)
	CreateSubscription(ctx context.Context, sub *agentdb.Subscription, cw agentdb.ConfigWrite) (*agentdb.Subscription, error)
	DeleteSubscription(ctx context.Context, project, id string, cw agentdb.ConfigWrite) error
	// schedules (§8.6)
	ListSchedules(ctx context.Context, project string) ([]*agentdb.Schedule, error)
	GetSchedule(ctx context.Context, project, id string) (*agentdb.Schedule, error)
	CreateSchedule(ctx context.Context, sch *agentdb.Schedule, cw agentdb.ConfigWrite) (*agentdb.Schedule, error)
	UpdateSchedule(ctx context.Context, sch *agentdb.Schedule, cw agentdb.ConfigWrite) (*agentdb.Schedule, error)
	DeleteSchedule(ctx context.Context, project, id string, cw agentdb.ConfigWrite) error
	// the prompt-revision memory (§9)
	CreateMemory(ctx context.Context, m *agentdb.Memory, embedding []float32) (*agentdb.Memory, error)
}

var _ managementStore = (*agentdb.Store)(nil)

// attentionRequester is the §9 attention mechanics, implemented ONCE in
// attention.go (H2). This file adapts the tool onto it and implements nothing:
// the webhook, the session stamp, the permalink, the expiry and the log-only
// fallback all live there, and a second implementation is exactly the drift H2's
// log entry warned about.
type attentionRequester interface {
	Request(ctx context.Context, in attentionRequestInput) (*attentionResult, error)
}

var _ attentionRequester = (*attentionService)(nil)

// managementTools is the tool set. attention may be nil (no product-layer
// tables), in which case request_human_attention refuses loudly rather than
// pretending a human was told.
type managementTools struct {
	store      managementStore
	embedder   embedding.Provider
	attention  attentionRequester
	permalinks permalinker
}

func newManagementTools(store managementStore, embedder embedding.Provider, attention attentionRequester, permalinks permalinker) *managementTools {
	// A typed nil pointer in an interface is NOT nil, so an unconfigured
	// attention service would sail past the handler's nil check and panic on the
	// first call. Unwrap it here, once.
	if svc, ok := attention.(*attentionService); ok && svc == nil {
		attention = nil
	}
	return &managementTools{store: store, embedder: embedder, attention: attention, permalinks: permalinks}
}

// ---------------------------------------------------------------------------
// Result shapes
// ---------------------------------------------------------------------------

// workerRecord is a worker WITHOUT its prompt text. §9's `worker_list` names
// {name, description, enabled}; the three §6.1 plumbing fields are added because
// a manager cannot decide a `worker_update` without seeing what it would change.
//
// The prompt is deliberately absent and reported as a byte count instead:
// prompts are large, a list of five workers would otherwise be a wall of text,
// and `worker_prompt_read` exists for exactly this. The rule, pinned by test: a
// result carries the prompt text when — and only when — the call wrote or read
// the prompt.
type workerRecord struct {
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Enabled           bool           `json:"enabled"`
	Image             string         `json:"image"`
	MaxInstances      int            `json:"max_instances"`
	Briefing          []string       `json:"briefing"`
	MCPConfig         map[string]any `json:"mcp_config,omitempty"`
	SystemPromptBytes int            `json:"system_prompt_bytes"`
	CreatedAt         int64          `json:"created_at"`
	UpdatedAt         int64          `json:"updated_at"`
}

// workerPromptRecord is a worker WITH its prompt — what the calls that wrote or
// read the prompt return.
type workerPromptRecord struct {
	workerRecord
	SystemPrompt string `json:"system_prompt"`
}

func toWorkerRecord(w *agentdb.Worker) workerRecord {
	briefing := []string(w.Briefing)
	if briefing == nil {
		briefing = []string{}
	}
	var mcp map[string]any
	if len(w.MCPConfig) > 0 {
		mcp = map[string]any(w.MCPConfig)
	}
	return workerRecord{
		Name:              w.Name,
		Description:       w.Description,
		Enabled:           w.Enabled,
		Image:             w.Image,
		MaxInstances:      w.MaxInstances,
		Briefing:          briefing,
		MCPConfig:         mcp,
		SystemPromptBytes: len(w.SystemPrompt),
		CreatedAt:         w.CreatedAt,
		UpdatedAt:         w.UpdatedAt,
	}
}

func toWorkerPromptRecord(w *agentdb.Worker) workerPromptRecord {
	return workerPromptRecord{workerRecord: toWorkerRecord(w), SystemPrompt: w.SystemPrompt}
}

// subscriptionRecord and scheduleRecord are the stored rows, echoed whole.
type subscriptionRecord struct {
	ID                string         `json:"id"`
	EventType         string         `json:"event_type"`
	Worker            string         `json:"worker"`
	Filter            map[string]any `json:"filter"`
	MaxFiringsPerHour int            `json:"max_firings_per_hour"`
	Enabled           bool           `json:"enabled"`
	CreatedAt         int64          `json:"created_at"`
	UpdatedAt         int64          `json:"updated_at"`
}

func toSubscriptionRecord(s *agentdb.Subscription) subscriptionRecord {
	filter := map[string]any(s.Filter)
	if filter == nil {
		filter = map[string]any{}
	}
	return subscriptionRecord{
		ID:                s.ID,
		EventType:         s.EventType,
		Worker:            s.Worker,
		Filter:            filter,
		MaxFiringsPerHour: s.MaxFiringsPerHour,
		Enabled:           s.Enabled,
		CreatedAt:         s.CreatedAt,
		UpdatedAt:         s.UpdatedAt,
	}
}

type scheduleRecord struct {
	ID        string `json:"id"`
	Worker    string `json:"worker"`
	Cron      string `json:"cron"`
	Input     string `json:"input"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func toScheduleRecord(s *agentdb.Schedule) scheduleRecord {
	return scheduleRecord{
		ID: s.ID, Worker: s.Worker, Cron: s.Cron, Input: s.Input,
		Enabled: s.Enabled, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

// promptRevision reports what became of the automatic `kind=prompt-revision`
// memory (§9). `stored:false` with an error is a real outcome, not a fiction:
// the prompt is already live and the config event already records the rationale,
// so the loss is the searchable copy, and saying so is more useful than either
// failing the call or hiding it.
type promptRevision struct {
	Stored        bool              `json:"stored"`
	MemoryID      string            `json:"memory_id,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	PreviousBytes int               `json:"previous_prompt_bytes"`
	Error         string            `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Tool descriptions — prompt, not documentation. Read on every job; the only
// thing between a model and a mismanaged organisation.
// ---------------------------------------------------------------------------

const workerListDescription = `List this project's workers: who exists, what each is for, ` +
	`whether it is switched on, which image it runs, its instance cap and its briefing selectors.

Prompts are NOT included — they are large. Read one with worker_prompt_read.

Start here before hiring: a worker that already does the job should be retuned ` +
	`(worker_update / worker_prompt_write), not duplicated under a second name.`

const workerCreateDescription = `Hire a worker: create a new configured persona in this project.

Give it a kebab-case name (email-answerer, tweet-writer), a one-line description ` +
	`other workers can read, and the system prompt that IS the worker — everything ` +
	`it believes about its job goes in the prompt, not in fields.

Creating a worker does not make it run. It runs when a subscription routes an ` +
	`event to it (subscription_create) or a schedule fires for it (schedule_create), ` +
	`or when a human chats with it.

The optional fields are plumbing: image points it at a project image (§13; bare ` +
	`name = always the latest, name:version = pinned), max_instances caps how many ` +
	`of its jobs may run at once (default 1), briefing is a list of label selectors ` +
	`whose newest matching memory is injected into every job it runs.

This refuses a name that already exists rather than overwriting it — a silent ` +
	`overwrite would throw away a working prompt. Retune with worker_update, or ` +
	`replace the prompt with worker_prompt_write.`

const workerUpdateDescription = `Change a worker's NON-prompt fields: description, image, ` +
	`max_instances, briefing, enabled. Pass only the fields you want to change.

It REFUSES system_prompt. Rewriting a prompt goes through worker_prompt_write, ` +
	`which demands a rationale and records the superseded text as a memory — ` +
	`semantics a partial update must not be able to bypass.

Retiring a worker is {"enabled": false}: it stops reacting to subscriptions and ` +
	`schedules, keeps its history, and can be switched back on.

Adopting an image is done HERE, deliberately: burning an image (image_create) ` +
	`points nothing at it, so "when did this worker change environment, and who ` +
	`decided?" stays answerable. Use "toolbox" to follow the latest version or ` +
	`"toolbox:3" to pin one.

rationale is optional but worth writing — it is the commit message on this change.`

const workerPromptReadDescription = `Read a worker's system prompt in full, exactly as stored.

Read before you write: worker_prompt_write REPLACES the whole prompt, so an edit ` +
	`means reading the current text, changing what needs changing, and writing the ` +
	`whole thing back. Writing only your new paragraph deletes everything else.`

const workerPromptWriteDescription = `Replace a worker's system prompt, wholesale.

This is the tool the whole system exists for: a reviewing worker reads how ` +
	`another worker actually performed and rewrites its instructions. That is how ` +
	`this organisation improves itself.

WHOLESALE REPLACE. The text you pass becomes the entire prompt. Call ` +
	`worker_prompt_read first and send back the full revised text, or you will ` +
	`silently delete the rest of the worker's instructions.

rationale is REQUIRED: the commit-message WHY. A diff shows that "acknowledge ` +
	`the customer's frustration" was added; only the rationale says a reviewer read ` +
	`a hundred curt threads and decided it. It is stored in the config log AND in ` +
	`the automatic prompt-revision memory this call writes, which also carries the ` +
	`SUPERSEDED prompt — so a bad rewrite can be found and put back by writing the ` +
	`old text again.

The new prompt applies to the worker's NEXT job, never to one already running.`

const projectPromptReadDescription = `Read the project-level system prompt in full.

Every worker in this project gets it, before its own prompt. It is where facts ` +
	`true of the whole organisation belong — house style, what the business is, ` +
	`shared conventions — not anything specific to one worker.`

const projectPromptWriteDescription = `Replace the project-level system prompt, wholesale.

It is prepended to EVERY worker's prompt in this project, so a careless rewrite ` +
	`changes every job. Read it first (project_prompt_read) and send back the whole ` +
	`revised text: this replaces, it does not append.

rationale is REQUIRED, exactly as for worker_prompt_write, and the superseded ` +
	`text is recorded as a prompt-revision memory.

Prefer a worker's own prompt for anything that is not true of every worker.`

const subscriptionListDescription = `List this project's subscriptions: which event type starts ` +
	`which worker, with what envelope filter and what firing cap.

This is the project's wiring. Read it before adding a subscription — two ` +
	`subscriptions on one event type start two jobs.`

const subscriptionCreateDescription = `Route an event type to a worker: when a matching event ` +
	`arrives, start a job for that worker with the event as its first message.

event_type is an exact match ("email.received") or a trailing-* prefix ` +
	`("email.*"). Nothing else is a pattern, and "*" alone is refused.

filter is an optional equality match on ENVELOPE fields only, e.g. ` +
	`{"worker":"email-answerer"} to react to what one worker finished. Anything ` +
	`smarter belongs in the reacting worker's prompt ("if this does not concern ` +
	`you, finish immediately").

max_firings_per_hour caps this subscription (0 = unlimited); excess deliveries ` +
	`are recorded rate_limited rather than dropped silently.

BEWARE LOOPS: a worker whose own worker.finished event matches a subscription ` +
	`that starts it again will run forever. Core has a depth floor, but the design ` +
	`is yours.`

const subscriptionDeleteDescription = `Delete a subscription: the worker stops reacting to that ` +
	`event type. Jobs already running are unaffected.

The subscription as it last stood is echoed back and kept in the config log, so ` +
	`putting it back is a lookup rather than a reconstruction.

To pause routing to a worker without touching the wiring, disable the worker ` +
	`instead (worker_update with enabled:false).`

const scheduleListDescription = `List this project's schedules: which worker runs on which cron, ` +
	`and — the important column — what INPUT each firing delivers.

Two rows targeting one worker with different inputs ("10:00 → write the morning ` +
	`tweet", "17:00 → write the evening tweet") is the normal shape, not a ` +
	`duplicate.`

const scheduleCreateDescription = `Put a worker on a cron: at each firing, start a job for it ` +
	`whose first message is the input text.

cron is a standard 5-field expression (minute hour day-of-month month ` +
	`day-of-week) in the stack's local time zone. Nicknames like @daily are ` +
	`refused — write "0 0 * * *".

input is the centre of gravity: the schedule says not only WHEN the worker runs ` +
	`but WHAT IT IS TOLD each time. Write it as an instruction, not a label.

Firings missed while the system was down are skipped, never replayed — a ` +
	`tweet-writer must not wake to a backlog of stale mornings. A firing for a ` +
	`worker already at its max_instances queues rather than starting a second copy.`

const scheduleUpdateDescription = `Change a schedule's fields: worker, cron, input, enabled. Pass ` +
	`only what you want to change.

Retuning a workforce's cadence — or what each firing asks for — is an ordinary ` +
	`data edit, which is exactly why a manager worker can do it. Pausing is ` +
	`{"enabled": false}.`

const scheduleDeleteDescription = `Delete a schedule; it stops firing. The row as it last stood is ` +
	`echoed and kept in the config log, so restoring it is a lookup. To pause it ` +
	`instead, schedule_update with {"enabled": false}.`

const requestHumanAttentionDescription = `Tell a human that this thread needs them, then END YOUR TURN.

Use it when you need permission, a judgement call, or a fact only a person has. ` +
	`Say specifically what you need in the message — the human sees your message ` +
	`and a link to this conversation.

There is no approval queue and no waiting state. Your session simply pauses the ` +
	`way every session pauses. The human opens the link, reads the thread, and ` +
	`WHATEVER THEY TYPE IS YOUR NEXT MESSAGE: "post it" is permission, "change the ` +
	`tone" is a conversation. So do not ask a yes/no question you cannot act on ` +
	`from either answer, and do not keep working after calling this.

expires_in (seconds, optional) sets a fallback: if nobody has replied by then, ` +
	`you are woken with a human.attention.timeout event and YOUR PROMPT decides ` +
	`what to do — proceed, escalate or abandon. Without it the request simply waits.

If the project has no attention channel configured, the request is logged and ` +
	`the permalink still comes back; the thread is the review surface either way.`

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// tools returns the management tools in the order the model sees them: the
// workforce, then the prompts, then the wiring, then the human.
func (m *managementTools) tools() []*mcpTool {
	return []*mcpTool{
		{
			Name:        "worker_list",
			Description: workerListDescription,
			InputSchema: objectSchema(nil, nil),
			Handler:     m.workerList,
		},
		{
			Name:        "worker_create",
			Description: workerCreateDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Kebab-case identity, e.g. \"email-answerer\": lowercase letters and digits, single hyphens.",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "One line saying what this worker is for — read by humans and by other workers.",
				},
				"system_prompt": map[string]any{
					"type":        "string",
					"description": "The worker's instructions. Everything it believes about its job lives here.",
				},
				"mcp_config": map[string]any{
					"type":        "object",
					"description": "Optional worker-level MCP servers, merged over the project's. Credential values must be whole-value ${VAR} references.",
				},
				"image": map[string]any{
					"type":        "string",
					"description": "Optional project image pointer: \"toolbox\" (always latest) or \"toolbox:3\" (pinned). Omit for the project default.",
				},
				"max_instances": map[string]any{
					"type":        "integer",
					"description": "Optional cap on simultaneously running jobs for this worker. Default 1.",
				},
				"briefing": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional label selectors; the newest memory matching each is injected as a briefing section into every job.",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "Optional commit-message why, recorded in the config log.",
				},
			}, []string{"name", "description", "system_prompt"}),
			Handler: m.workerCreate,
		},
		{
			Name:        "worker_update",
			Description: workerUpdateDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{"type": "string", "description": "The worker to change."},
				"fields": map[string]any{
					"type": "object",
					"description": "The fields to change, and only those: description (string), image (string), " +
						"max_instances (integer), briefing (array of strings), enabled (boolean). " +
						"system_prompt is REFUSED here — use worker_prompt_write.",
					"properties": map[string]any{
						"description":   map[string]any{"type": "string"},
						"image":         map[string]any{"type": "string"},
						"max_instances": map[string]any{"type": "integer"},
						"briefing":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"enabled":       map[string]any{"type": "boolean"},
					},
					"additionalProperties": false,
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "Optional commit-message why, recorded in the config log.",
				},
			}, []string{"name", "fields"}),
			Handler: m.workerUpdate,
		},
		{
			Name:        "worker_prompt_read",
			Description: workerPromptReadDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{"type": "string", "description": "The worker whose prompt to read."},
			}, []string{"name"}),
			Handler: m.workerPromptRead,
		},
		{
			Name:        "worker_prompt_write",
			Description: workerPromptWriteDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{"type": "string", "description": "The worker whose prompt to replace."},
				"system_prompt": map[string]any{
					"type":        "string",
					"description": "The COMPLETE new prompt. It replaces the old one entirely.",
				},
				"rationale": map[string]any{
					"type": "string",
					"description": "REQUIRED. Why you are making this change — stored in the config log and in the " +
						"prompt-revision memory alongside the superseded prompt.",
				},
			}, []string{"name", "system_prompt", "rationale"}),
			Handler: m.workerPromptWrite,
		},
		{
			Name:        "project_prompt_read",
			Description: projectPromptReadDescription,
			InputSchema: objectSchema(nil, nil),
			Handler:     m.projectPromptRead,
		},
		{
			Name:        "project_prompt_write",
			Description: projectPromptWriteDescription,
			InputSchema: objectSchema(map[string]any{
				"system_prompt": map[string]any{
					"type":        "string",
					"description": "The COMPLETE new project prompt. It replaces the old one entirely.",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "REQUIRED. Why — stored in the config log and in the prompt-revision memory.",
				},
			}, []string{"system_prompt", "rationale"}),
			Handler: m.projectPromptWrite,
		},
		{
			Name:        "subscription_list",
			Description: subscriptionListDescription,
			InputSchema: objectSchema(nil, nil),
			Handler:     m.subscriptionList,
		},
		{
			Name:        "subscription_create",
			Description: subscriptionCreateDescription,
			InputSchema: objectSchema(map[string]any{
				"event_type": map[string]any{
					"type":        "string",
					"description": "Exact type (\"email.received\") or trailing-* prefix (\"email.*\"). \"*\" alone is refused.",
				},
				"worker": map[string]any{"type": "string", "description": "The worker to start a job for."},
				"filter": map[string]any{
					"type":        "object",
					"description": "Optional equality match on envelope fields, e.g. {\"worker\":\"email-answerer\"}.",
				},
				"max_firings_per_hour": map[string]any{
					"type":        "integer",
					"description": "Optional cap; 0 (default) = unlimited.",
				},
				"enabled": map[string]any{
					"type":        "boolean",
					"description": "Optional; defaults to true — a new subscription is live.",
				},
				"rationale": map[string]any{"type": "string", "description": "Optional commit-message why."},
			}, []string{"event_type", "worker"}),
			Handler: m.subscriptionCreate,
		},
		{
			Name:        "subscription_delete",
			Description: subscriptionDeleteDescription,
			InputSchema: objectSchema(map[string]any{
				"id":        map[string]any{"type": "string", "description": "The subscription id, from subscription_list."},
				"rationale": map[string]any{"type": "string", "description": "Optional commit-message why."},
			}, []string{"id"}),
			Handler: m.subscriptionDelete,
		},
		{
			Name:        "schedule_list",
			Description: scheduleListDescription,
			InputSchema: objectSchema(nil, nil),
			Handler:     m.scheduleList,
		},
		{
			Name:        "schedule_create",
			Description: scheduleCreateDescription,
			InputSchema: objectSchema(map[string]any{
				"worker": map[string]any{"type": "string", "description": "The worker to start a job for."},
				"cron": map[string]any{
					"type":        "string",
					"description": "Standard 5-field cron expression, stack-local time. Nicknames (@daily) are refused.",
				},
				"input": map[string]any{
					"type":        "string",
					"description": "What the worker is told at each firing — this becomes the job's first message.",
				},
				"enabled":   map[string]any{"type": "boolean", "description": "Optional; defaults to true."},
				"rationale": map[string]any{"type": "string", "description": "Optional commit-message why."},
			}, []string{"worker", "cron", "input"}),
			Handler: m.scheduleCreate,
		},
		{
			Name:        "schedule_update",
			Description: scheduleUpdateDescription,
			InputSchema: objectSchema(map[string]any{
				"id": map[string]any{"type": "string", "description": "The schedule id, from schedule_list."},
				"fields": map[string]any{
					"type":        "object",
					"description": "The fields to change: worker (string), cron (string), input (string), enabled (boolean).",
					"properties": map[string]any{
						"worker":  map[string]any{"type": "string"},
						"cron":    map[string]any{"type": "string"},
						"input":   map[string]any{"type": "string"},
						"enabled": map[string]any{"type": "boolean"},
					},
					"additionalProperties": false,
				},
				"rationale": map[string]any{"type": "string", "description": "Optional commit-message why."},
			}, []string{"id", "fields"}),
			Handler: m.scheduleUpdate,
		},
		{
			Name:        "schedule_delete",
			Description: scheduleDeleteDescription,
			InputSchema: objectSchema(map[string]any{
				"id":        map[string]any{"type": "string", "description": "The schedule id, from schedule_list."},
				"rationale": map[string]any{"type": "string", "description": "Optional commit-message why."},
			}, []string{"id"}),
			Handler: m.scheduleDelete,
		},
		{
			Name:        "request_human_attention",
			Description: requestHumanAttentionDescription,
			InputSchema: objectSchema(map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "What you need from a human, specifically. They see this and a link to this conversation.",
				},
				"expires_in": map[string]any{
					"type":        "integer",
					"description": "Optional seconds until the request lapses; on lapsing you are woken with a human.attention.timeout event.",
				},
			}, []string{"message"}),
			Handler: m.requestHumanAttention,
		},
	}
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// configWrite builds the who/why for a mutation. The actor is always the token's
// worker and session — never an argument (§15.2).
func (m *managementTools) configWrite(caller mcpCaller, rationale string) agentdb.ConfigWrite {
	return agentdb.ConfigWrite{
		Worker:    caller.Worker,
		Session:   caller.SessionID,
		Rationale: strings.TrimSpace(rationale),
	}
}

// requireRationale enforces §15.5 on the two prompt writes. It is checked HERE
// as well as in the store seam so the model gets a sentence it can act on rather
// than a wrapped database-layer complaint — and so nothing is validated after a
// write has begun.
func requireRationale(rationale string) error {
	if strings.TrimSpace(rationale) == "" {
		return errors.New("rationale is required on a prompt write and must not be blank: " +
			"say WHY you are changing this prompt (§15.5). The text is stored in the config log and " +
			"in the prompt-revision memory that records the prompt you are replacing")
	}
	return nil
}

// decodeFields decodes a partial-update `fields` object into raw values, so each
// key can be type-checked and an unknown one refused individually. A blanket
// json.Unmarshal into a struct would silently ignore both.
func decodeFields(raw json.RawMessage, allowed []string, refused map[string]string) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("fields must be an object of the values to change: %w", err)
		}
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("fields is empty — name at least one of %s to change", strings.Join(allowed, ", "))
	}
	for key := range fields {
		if why, no := refused[key]; no {
			return nil, errors.New(why)
		}
		if !contains(allowed, key) {
			return nil, fmt.Errorf("fields: %q is not an updatable field; allowed: %s",
				key, strings.Join(allowed, ", "))
		}
	}
	return fields, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// decodeField unmarshals one field value, naming the field on failure.
func decodeField(fields map[string]json.RawMessage, key string, dst any) error {
	if err := json.Unmarshal(fields[key], dst); err != nil {
		return fmt.Errorf("fields.%s: %w", key, err)
	}
	return nil
}

// toMCPConfig validates a caller-supplied MCP config before it is stored. B2's
// finding: an invalid config written here fails later, at session start, for
// EVERY session in the project — so it is refused now, at the only moment there
// is someone to tell.
func toMCPConfig(raw map[string]any) (agentdb.JSONMap, error) {
	if len(raw) == 0 {
		return agentdb.JSONMap{}, nil
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("mcp_config: %w", err)
	}
	var servers agentdb.MCPServers
	if err := json.Unmarshal(blob, &servers); err != nil {
		return nil, fmt.Errorf("mcp_config is not a map of MCP server configs: %w", err)
	}
	if err := servers.Validate(); err != nil {
		return nil, fmt.Errorf("mcp_config: %w", err)
	}
	return agentdb.JSONMap(raw), nil
}

// validateImagePointer checks the syntactic shape of a §13 image reference.
// Resolution (bare name → latest, name:version → pinned) is the launch path's
// job; this only refuses what can be refused now — an empty pointer is legal and
// means "use the project default".
func validateImagePointer(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if _, err := agentdb.ParseImageRef(ref); err != nil {
		return fmt.Errorf("image: %w (a pointer is \"name\" for the latest version or \"name:version\" to pin one)", err)
	}
	return nil
}

func validateBriefing(selectors []string) error {
	for i, sel := range selectors {
		if strings.TrimSpace(sel) == "" {
			return fmt.Errorf("briefing selector %d is blank", i)
		}
		if _, err := agentdb.ParseLabelSelector(sel); err != nil {
			return fmt.Errorf("briefing selector %q: %w", sel, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Workers
// ---------------------------------------------------------------------------

func (m *managementTools) workerList(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return nil, err
	}
	workers, err := m.store.ListWorkers(ctx, caller.Project)
	if err != nil {
		return nil, err
	}
	out := make([]workerRecord, 0, len(workers))
	for _, w := range workers {
		out = append(out, toWorkerRecord(w))
	}
	return map[string]any{"workers": out, "count": len(out)}, nil
}

type workerCreateArgs struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	SystemPrompt string         `json:"system_prompt"`
	MCPConfig    map[string]any `json:"mcp_config"`
	Image        string         `json:"image"`
	MaxInstances int            `json:"max_instances"`
	Briefing     []string       `json:"briefing"`
	Rationale    string         `json:"rationale"`
}

func (m *managementTools) workerCreate(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args workerCreateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}

	// Validate EVERYTHING before touching the store (§9): a half-validated create
	// that fails on its third field would leave the model guessing.
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	// The store enforces this too; refusing here means the model is told the rule
	// before anything is read or written (§9).
	if err := agentdb.ValidateWorkerName(name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.SystemPrompt) == "" {
		return nil, errors.New("system_prompt is required and must not be blank: the prompt IS the worker")
	}
	if args.MaxInstances < 0 {
		return nil, fmt.Errorf("max_instances must be >= 1 (or omitted for the default of %d), got %d",
			agentdb.DefaultMaxInstances, args.MaxInstances)
	}
	if err := validateImagePointer(args.Image); err != nil {
		return nil, err
	}
	if err := validateBriefing(args.Briefing); err != nil {
		return nil, err
	}
	mcp, err := toMCPConfig(args.MCPConfig)
	if err != nil {
		return nil, err
	}

	// Hiring is not overwriting. UpsertWorker would happily replace a live
	// worker's prompt, which is exactly the accident that must not be possible
	// from a tool called "create".
	if existing, err := m.store.GetWorker(ctx, caller.Project, name); err == nil {
		return nil, fmt.Errorf("worker %q already exists (created %d): use worker_update to change its "+
			"fields or worker_prompt_write to replace its prompt — worker_create never overwrites",
			existing.Name, existing.CreatedAt)
	} else if !errors.Is(err, agentdb.ErrWorkerNotFound) {
		return nil, err
	}

	w := agentdb.NewWorker(caller.Project, name) // project in code, never an argument
	w.Description = strings.TrimSpace(args.Description)
	w.SystemPrompt = args.SystemPrompt
	w.MCPConfig = mcp
	w.Image = strings.TrimSpace(args.Image)
	if args.MaxInstances > 0 {
		w.MaxInstances = args.MaxInstances
	}
	if args.Briefing != nil {
		w.Briefing = agentdb.SelectorList(args.Briefing)
	}

	if _, err := m.store.UpsertWorker(ctx, w, m.configWrite(caller, args.Rationale)); err != nil {
		return nil, err
	}
	stored, err := m.store.GetWorker(ctx, caller.Project, name)
	if err != nil {
		return nil, fmt.Errorf("the worker was created but could not be read back: %w", err)
	}
	// This call wrote the prompt, so this result carries it.
	return toWorkerPromptRecord(stored), nil
}

// workerUpdatableFields is §9's closed set: the prompt is NOT in it.
var workerUpdatableFields = []string{"description", "image", "max_instances", "briefing", "enabled"}

// workerRefusedFields explains, rather than merely rejecting, the one mistake
// this tool exists to prevent.
var workerRefusedFields = map[string]string{
	"system_prompt": "worker_update refuses system_prompt: a prompt is replaced only by worker_prompt_write, " +
		"which requires a rationale and records the superseded prompt as a kind=prompt-revision memory (§9, §15.5). " +
		"Those semantics must not be bypassable by a partial update",
	"name": "a worker's name is its identity and cannot be changed; create the new worker and disable the old one",
}

type workerUpdateArgs struct {
	Name      string          `json:"name"`
	Fields    json.RawMessage `json:"fields"`
	Rationale string          `json:"rationale"`
}

func (m *managementTools) workerUpdate(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args workerUpdateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	fields, err := decodeFields(args.Fields, workerUpdatableFields, workerRefusedFields)
	if err != nil {
		return nil, err
	}

	// Read-modify-write: UpsertWorker is a whole-object replace, so the stored row
	// is the base and only the named fields move. Reading first is also what makes
	// "enabled flipped and nothing else" land in the log as worker_enable /
	// worker_disable rather than a generic update (§15.3).
	next, err := m.store.GetWorker(ctx, caller.Project, name)
	if err != nil {
		if errors.Is(err, agentdb.ErrWorkerNotFound) {
			return nil, fmt.Errorf("no worker %q in this project (worker_list shows who exists)", name)
		}
		return nil, err
	}

	for key := range fields {
		switch key {
		case "description":
			var v string
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			next.Description = strings.TrimSpace(v)
		case "image":
			var v string
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			if err := validateImagePointer(v); err != nil {
				return nil, err
			}
			next.Image = strings.TrimSpace(v)
		case "max_instances":
			var v int
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			if v < 1 {
				return nil, fmt.Errorf("fields.max_instances must be >= 1, got %d", v)
			}
			next.MaxInstances = v
		case "briefing":
			var v []string
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			if err := validateBriefing(v); err != nil {
				return nil, fmt.Errorf("fields.%w", err)
			}
			// An explicit empty list means "no briefing sections", which is a
			// different state from the SQL NULL that means "never configured" —
			// SelectorList preserves the difference, so preserve it here too.
			next.Briefing = agentdb.SelectorList(v)
		case "enabled":
			var v bool
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			next.Enabled = v
		}
	}

	if _, err := m.store.UpsertWorker(ctx, next, m.configWrite(caller, args.Rationale)); err != nil {
		return nil, err
	}
	stored, err := m.store.GetWorker(ctx, caller.Project, name)
	if err != nil {
		return nil, fmt.Errorf("the worker was updated but could not be read back: %w", err)
	}
	// No prompt in the result: this call is forbidden from touching it.
	return toWorkerRecord(stored), nil
}

type workerNameArgs struct {
	Name string `json:"name"`
}

func (m *managementTools) workerPromptRead(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args workerNameArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	w, err := m.store.GetWorker(ctx, caller.Project, name)
	if err != nil {
		if errors.Is(err, agentdb.ErrWorkerNotFound) {
			return nil, fmt.Errorf("no worker %q in this project (worker_list shows who exists)", name)
		}
		return nil, err
	}
	return toWorkerPromptRecord(w), nil
}

type workerPromptWriteArgs struct {
	Name         string `json:"name"`
	SystemPrompt string `json:"system_prompt"`
	Rationale    string `json:"rationale"`
}

// workerPromptWrite is the §8.7 loop, in one handler.
func (m *managementTools) workerPromptWrite(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args workerPromptWriteArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if strings.TrimSpace(args.SystemPrompt) == "" {
		return nil, errors.New("system_prompt is required and must not be blank: this REPLACES the whole " +
			"prompt, so an empty one would delete the worker's instructions")
	}
	if err := requireRationale(args.Rationale); err != nil {
		return nil, err
	}

	stored, previous, err := m.store.SetWorkerPrompt(ctx, caller.Project, name, args.SystemPrompt,
		m.configWrite(caller, args.Rationale))
	if err != nil {
		if errors.Is(err, agentdb.ErrWorkerNotFound) {
			return nil, fmt.Errorf("no worker %q in this project, so nothing was written "+
				"(worker_list shows who exists)", name)
		}
		return nil, err
	}

	// §9's automatic provenance memory. It is written AFTER the prompt because
	// the prompt is the act and the memory is the record of it.
	revision := m.storePromptRevision(ctx, caller, promptRevisionInput{
		Subject:   fmt.Sprintf("worker %q", stored.Name),
		Labels:    map[string]string{"kind": promptRevisionKind, "worker": stored.Name},
		Previous:  previous,
		Rationale: args.Rationale,
	})

	result := map[string]any{
		"worker":          toWorkerPromptRecord(stored),
		"rationale":       strings.TrimSpace(args.Rationale),
		"prompt_revision": revision,
		"note": "The new prompt applies to this worker's NEXT job. " +
			"The previous prompt is preserved in the config log and in the prompt-revision memory.",
	}
	if !revision.Stored {
		result["warning"] = "THE PROMPT WAS WRITTEN, but the prompt-revision memory was not stored (" +
			revision.Error + "). The change and its rationale are still in the config log; only the " +
			"searchable copy of the superseded prompt is missing."
	}
	return result, nil
}

func (m *managementTools) projectPromptRead(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return nil, err
	}
	ps, err := m.store.GetProjectSettings(ctx, caller.Project)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"system_prompt":       ps.SystemPrompt,
		"system_prompt_bytes": len(ps.SystemPrompt),
		"updated_at":          ps.UpdatedAt,
	}, nil
}

type projectPromptWriteArgs struct {
	SystemPrompt string `json:"system_prompt"`
	Rationale    string `json:"rationale"`
}

func (m *managementTools) projectPromptWrite(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args projectPromptWriteArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.SystemPrompt) == "" {
		return nil, errors.New("system_prompt is required and must not be blank: this REPLACES the whole " +
			"project prompt, which every worker in the project receives")
	}
	if err := requireRationale(args.Rationale); err != nil {
		return nil, err
	}

	stored, previous, err := m.store.SetProjectPrompt(ctx, caller.Project, args.SystemPrompt,
		m.configWrite(caller, args.Rationale))
	if err != nil {
		return nil, err
	}

	revision := m.storePromptRevision(ctx, caller, promptRevisionInput{
		Subject:   "the project prompt",
		Labels:    map[string]string{"kind": promptRevisionKind, "scope": "project"},
		Previous:  previous,
		Rationale: args.Rationale,
	})

	result := map[string]any{
		"system_prompt":       stored.SystemPrompt,
		"system_prompt_bytes": len(stored.SystemPrompt),
		"updated_at":          stored.UpdatedAt,
		"rationale":           strings.TrimSpace(args.Rationale),
		"prompt_revision":     revision,
		"note":                "Every worker in this project receives this prompt on its NEXT job.",
	}
	if !revision.Stored {
		result["warning"] = "THE PROMPT WAS WRITTEN, but the prompt-revision memory was not stored (" +
			revision.Error + "). The change and its rationale are still in the config log."
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// The prompt-revision memory (§9)
// ---------------------------------------------------------------------------

// promptRevisionKind is the label value §9 names. Everything that looks for a
// prompt's history looks for `kind=prompt-revision`.
const promptRevisionKind = "prompt-revision"

// promptRevisionNoPrevious is what the record says when there was nothing to
// supersede. Stated rather than left blank so a reader can tell "this worker had
// no prompt" from "the previous prompt was lost".
const promptRevisionNoPrevious = "(none — this is the first prompt written for this subject)"

type promptRevisionInput struct {
	// Subject names what was rewritten, in prose, for the memory's first line.
	Subject string
	// Labels are how the memory is found again: kind=prompt-revision plus either
	// worker=<name> or scope=project.
	Labels    map[string]string
	Previous  string
	Rationale string
}

// storePromptRevision appends the automatic memory of §9 and reports what
// happened. It never returns an error: by the time it runs the prompt is already
// written and config-evented, so failing the tool call would tell the model the
// opposite of the truth. The outcome is carried in the result instead.
func (m *managementTools) storePromptRevision(ctx context.Context, caller mcpCaller, in promptRevisionInput) promptRevision {
	out := promptRevision{PreviousBytes: len(in.Previous)}

	previous := in.Previous
	if strings.TrimSpace(previous) == "" {
		previous = promptRevisionNoPrevious
	}
	content := fmt.Sprintf(
		"Prompt revision — %s.\n\nRationale: %s\n\n"+
			"--- previous prompt (superseded) begins ---\n%s\n--- previous prompt ends ---\n",
		in.Subject, strings.TrimSpace(in.Rationale), previous)

	// Embedding follows D2's write-path rule: a memory stored with a NULL
	// embedding is permanently invisible to semantic search, and this one is
	// meant to be found years later. A provider failure therefore costs the
	// memory — reported, never silent — rather than storing an unfindable row.
	vec, err := embedding.Embed(ctx, m.embedder, content)
	if err != nil {
		out.Error = "could not embed the revision memory: " + err.Error()
		return out
	}

	stored, err := m.store.CreateMemory(ctx, &agentdb.Memory{
		Project:          caller.Project,
		Labels:           agentdb.LabelSet(in.Labels),
		Content:          content,
		CreatedByWorker:  caller.Worker,
		CreatedBySession: caller.SessionID,
	}, vec)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Stored = true
	out.MemoryID = stored.ID
	out.Labels = in.Labels
	return out
}

// ---------------------------------------------------------------------------
// Subscriptions (§8.3)
// ---------------------------------------------------------------------------

func (m *managementTools) subscriptionList(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return nil, err
	}
	subs, err := m.store.ListSubscriptions(ctx, caller.Project)
	if err != nil {
		return nil, err
	}
	out := make([]subscriptionRecord, 0, len(subs))
	for _, s := range subs {
		out = append(out, toSubscriptionRecord(s))
	}
	return map[string]any{"subscriptions": out, "count": len(out)}, nil
}

type subscriptionCreateArgs struct {
	EventType         string         `json:"event_type"`
	Worker            string         `json:"worker"`
	Filter            map[string]any `json:"filter"`
	MaxFiringsPerHour int            `json:"max_firings_per_hour"`
	Enabled           *bool          `json:"enabled"`
	Rationale         string         `json:"rationale"`
}

func (m *managementTools) subscriptionCreate(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args subscriptionCreateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	worker := strings.TrimSpace(args.Worker)
	if worker == "" {
		return nil, errors.New("worker is required")
	}
	// §9: "known worker name". Routing an event at a worker that does not exist
	// would fail at 03:00, once, invisibly.
	if _, err := m.store.GetWorker(ctx, caller.Project, worker); err != nil {
		if errors.Is(err, agentdb.ErrWorkerNotFound) {
			return nil, fmt.Errorf("no worker %q in this project — create it first with worker_create "+
				"(worker_list shows who exists)", worker)
		}
		return nil, err
	}

	// Enabled defaults to true: a subscription nobody asked to be off is live.
	enabled := true
	if args.Enabled != nil {
		enabled = *args.Enabled
	}
	sub := &agentdb.Subscription{
		Project:           caller.Project, // in code, always
		EventType:         strings.TrimSpace(args.EventType),
		Worker:            worker,
		Filter:            agentdb.JSONMap(args.Filter),
		MaxFiringsPerHour: args.MaxFiringsPerHour,
		Enabled:           enabled,
	}
	created, err := m.store.CreateSubscription(ctx, sub, m.configWrite(caller, args.Rationale))
	if err != nil {
		return nil, err
	}
	stored, err := m.store.GetSubscription(ctx, caller.Project, created.ID)
	if err != nil {
		return nil, fmt.Errorf("the subscription was created but could not be read back: %w", err)
	}
	return toSubscriptionRecord(stored), nil
}

type idArgs struct {
	ID        string `json:"id"`
	Rationale string `json:"rationale"`
}

func (m *managementTools) subscriptionDelete(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args idArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return nil, errors.New("id is required (subscription_list shows the ids)")
	}
	// Read before deleting so the result can echo what was removed — the row is
	// gone from the table a moment later, and a delete that answers only "ok" is
	// unverifiable.
	existing, err := m.store.GetSubscription(ctx, caller.Project, id)
	if err != nil {
		return nil, err
	}
	if err := m.store.DeleteSubscription(ctx, caller.Project, id, m.configWrite(caller, args.Rationale)); err != nil {
		return nil, err
	}
	return map[string]any{
		"deleted": toSubscriptionRecord(existing),
		"note": "Kept in the config log as it last stood, so it can be recreated exactly. " +
			"Jobs already running are unaffected.",
	}, nil
}

// ---------------------------------------------------------------------------
// Schedules (§8.6)
// ---------------------------------------------------------------------------

func (m *managementTools) scheduleList(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return nil, err
	}
	schedules, err := m.store.ListSchedules(ctx, caller.Project)
	if err != nil {
		return nil, err
	}
	out := make([]scheduleRecord, 0, len(schedules))
	for _, s := range schedules {
		out = append(out, toScheduleRecord(s))
	}
	return map[string]any{"schedules": out, "count": len(out)}, nil
}

type scheduleCreateArgs struct {
	Worker    string `json:"worker"`
	Cron      string `json:"cron"`
	Input     string `json:"input"`
	Enabled   *bool  `json:"enabled"`
	Rationale string `json:"rationale"`
}

func (m *managementTools) scheduleCreate(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args scheduleCreateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	worker := strings.TrimSpace(args.Worker)
	if worker == "" {
		return nil, errors.New("worker is required")
	}
	if _, err := m.store.GetWorker(ctx, caller.Project, worker); err != nil {
		if errors.Is(err, agentdb.ErrWorkerNotFound) {
			return nil, fmt.Errorf("no worker %q in this project — create it first with worker_create", worker)
		}
		return nil, err
	}
	if strings.TrimSpace(args.Input) == "" {
		return nil, errors.New("input is required: it is what the worker is TOLD at each firing, " +
			"and it becomes the job's first message")
	}
	sch := agentdb.NewSchedule(caller.Project, worker, strings.TrimSpace(args.Cron), args.Input)
	if args.Enabled != nil {
		sch.Enabled = *args.Enabled
	}
	// The cron is validated by the store (an unparseable expression is never
	// stored to fail silently at 03:00), which is also where the nickname refusal
	// lives — one parser, not two.
	created, err := m.store.CreateSchedule(ctx, sch, m.configWrite(caller, args.Rationale))
	if err != nil {
		return nil, err
	}
	stored, err := m.store.GetSchedule(ctx, caller.Project, created.ID)
	if err != nil {
		return nil, fmt.Errorf("the schedule was created but could not be read back: %w", err)
	}
	return toScheduleRecord(stored), nil
}

var scheduleUpdatableFields = []string{"worker", "cron", "input", "enabled"}

var scheduleRefusedFields = map[string]string{
	"id": "a schedule's id is its identity and cannot be changed",
}

type scheduleUpdateArgs struct {
	ID        string          `json:"id"`
	Fields    json.RawMessage `json:"fields"`
	Rationale string          `json:"rationale"`
}

func (m *managementTools) scheduleUpdate(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args scheduleUpdateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return nil, errors.New("id is required (schedule_list shows the ids)")
	}
	fields, err := decodeFields(args.Fields, scheduleUpdatableFields, scheduleRefusedFields)
	if err != nil {
		return nil, err
	}
	next, err := m.store.GetSchedule(ctx, caller.Project, id)
	if err != nil {
		return nil, err
	}
	for key := range fields {
		switch key {
		case "worker":
			var v string
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			v = strings.TrimSpace(v)
			if _, err := m.store.GetWorker(ctx, caller.Project, v); err != nil {
				if errors.Is(err, agentdb.ErrWorkerNotFound) {
					return nil, fmt.Errorf("no worker %q in this project: a schedule pointed at a missing "+
						"worker is disabled the first time it comes due", v)
				}
				return nil, err
			}
			next.Worker = v
		case "cron":
			var v string
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			next.Cron = strings.TrimSpace(v)
		case "input":
			var v string
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			if strings.TrimSpace(v) == "" {
				return nil, errors.New("fields.input must not be blank: it is what the worker is told at each firing")
			}
			next.Input = v
		case "enabled":
			var v bool
			if err := decodeField(fields, key, &v); err != nil {
				return nil, err
			}
			next.Enabled = v
		}
	}
	if _, err := m.store.UpdateSchedule(ctx, next, m.configWrite(caller, args.Rationale)); err != nil {
		return nil, err
	}
	stored, err := m.store.GetSchedule(ctx, caller.Project, id)
	if err != nil {
		return nil, fmt.Errorf("the schedule was updated but could not be read back: %w", err)
	}
	return toScheduleRecord(stored), nil
}

func (m *managementTools) scheduleDelete(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args idArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return nil, errors.New("id is required (schedule_list shows the ids)")
	}
	existing, err := m.store.GetSchedule(ctx, caller.Project, id)
	if err != nil {
		return nil, err
	}
	if err := m.store.DeleteSchedule(ctx, caller.Project, id, m.configWrite(caller, args.Rationale)); err != nil {
		return nil, err
	}
	return map[string]any{
		"deleted": toScheduleRecord(existing),
		"note":    "Kept in the config log as it last stood, so it can be recreated exactly.",
	}, nil
}

// ---------------------------------------------------------------------------
// request_human_attention (§9) — an ADAPTER, not an implementation
// ---------------------------------------------------------------------------

type attentionArgs struct {
	Message   string `json:"message"`
	ExpiresIn int64  `json:"expires_in"`
}

func (m *managementTools) requestHumanAttention(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args attentionArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if m.attention == nil {
		return nil, errors.New("request_human_attention is not available in this deployment " +
			"(the product-layer store is not configured), so NO human was told")
	}
	if caller.SessionID == "" {
		// The request is about THIS thread — that is what the human is sent a link
		// to — and there is no argument with which to name another one.
		return nil, errors.New("request_human_attention can only be called from inside a session: " +
			"this token names no session")
	}
	// Everything else — the webhook, the session stamp, the permalink, the
	// expiry, the log-only fallback — is attentionService's (H2). Adding any of
	// it here would be a second implementation of the same primitive.
	return m.attention.Request(ctx, attentionRequestInput{
		Project:   caller.Project, // in code, always — never an argument
		SessionID: caller.SessionID,
		Message:   args.Message,
		ExpiresIn: args.ExpiresIn,
	})
}
