package main

// mcp_sessions.go — the session-provenance MCP tool
// (design/2026-08-06-embeddable-agent-orange.md, T16), registered onto the host
// MCP server in mcpserver.go.
//
// The whole surface is ONE tool:
//
//	session_list(worker?, limit?) → this worker's recent sessions, newest first
//
// and, as with config_history, most of the design is in what is absent. There
// is no session_get, no session_messages and no transcript of any kind. The
// question this tool answers is "WHEN did I run, and what came of it" — dates,
// statuses, how much each run produced, and a permalink a human can open. The
// question it deliberately does NOT answer is "what did I say last time":
// re-reading one's own transcript burns a context window re-deriving what an
// archivist worker already summarised into a memory, and memory_search /
// memory_current are the tools for that.
//
// # Why an empty list is a normal answer (the decision the ticket asked for)
//
// The `worker` column on a session row is written by exactly one place:
// runner.persistComposition (runner.go:536-549), which fires only when a
// CreateSessionRequest carries a Worker — i.e. only for a job DISPATCHED from
// an event or a schedule (dispatch.go → ComposeJob). A session created through
// POST /agent/session — every console chat, and the long-lived conversational
// half of the two-bot pattern — leaves that column empty and carries its worker
// name in `persona` instead (httpapi/session.go:127, runner.go:467).
//
// This tool does NOT fall back to Session.Persona, and that is deliberate:
//
//   - mcpCaller.Worker is the ONE auth-established answer to "who am I",
//     resolved once in sessionTokenAuth.authenticate (mcpserver.go:505) from the
//     session row. Every other core tool reads the same field, and the config
//     log's RD4 invariant is built on it — an empty ActorWorker means "a human
//     did this". A second, tool-local notion of the caller's identity would let
//     session_list say "you are reviewer-a" in the same turn that
//     worker_prompt_write refuses because the caller could not be identified.
//     One identity per caller, or neither answer can be trusted.
//   - `persona` is an unvalidated free-text field taken straight off the create
//     request body; it is not a foreign key into `workers`. Silently promoting
//     it to a filter would mean a client-chosen string decided what history the
//     model believes it has.
//
// The cost of that decision is that a chat session calling session_list with no
// argument gets `[]`. That is only safe if the model is TOLD, or it reads the
// empty list as "I have never run" and acts on it — so the tool description
// says so in as many words, and TestSessionListDescriptionStatesTheJobSessionCaveat
// pins it. The chat session's actual route to its reviewer's history is the
// explicit `worker` argument.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// sessionListStore is the narrow read seam: one method, no writes and no way to
// reach a message body. *agentdb.Store satisfies it.
type sessionListStore interface {
	ListSessions(ctx context.Context, q *agentdb.SessionQuery) ([]*agentdb.Session, error)
}

// Result sizing. The cap is not decoration: Store.ListSessions joins THREE
// unconditional COUNT(*) subqueries over agent_artifacts and agent_messages on
// every call (agentdb/sessions.go:293-300), regardless of which columns anyone
// reads. Fine at fifty rows; do not raise this without revisiting that query.
const (
	sessionListDefaultLimit = 10
	sessionListMaxLimit     = 50
)

type sessionTools struct {
	store      sessionListStore
	permalinks permalinker
}

func newSessionTools(store sessionListStore, permalinks permalinker) *sessionTools {
	return &sessionTools{store: store, permalinks: permalinks}
}

// ---------------------------------------------------------------------------
// Result shape
// ---------------------------------------------------------------------------

// sessionRecord is one run. Every field is metadata; nothing here can carry
// text a model or a tool wrote.
//
// artifact_count and message_count are included because the store's query has
// already paid for them (see the cap comment above) and they are the honest,
// content-free answer to "what came of it": a run that produced three artifacts
// and forty messages went differently from one that produced none.
type sessionRecord struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Status is the session row's lifecycle status, not a job outcome — a
	// finished job's result lives on the event spine, and its conclusions in
	// memory.
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	SessionURL string `json:"session_url"`
	// CreateError is why a run never started, when that is the answer — a bare
	// `status: "error"` tells a model something broke and not what
	// (agentdb/types.go, Session.CreateError).
	CreateError   string `json:"create_error,omitempty"`
	ArtifactCount int    `json:"artifact_count"`
	MessageCount  int    `json:"message_count"`
	// AttentionOpen is the §9 stamp: this run parked itself waiting for a human
	// rather than finishing. Worth knowing before assuming last night's run
	// completed.
	AttentionOpen bool `json:"attention_requested,omitempty"`
}

func (s *sessionTools) record(project string, sess *agentdb.Session) sessionRecord {
	return sessionRecord{
		ID:            sess.ID,
		Name:          sess.Name,
		Status:        sess.Status,
		CreatedAt:     sess.CreatedAt,
		UpdatedAt:     sess.UpdatedAt,
		SessionURL:    s.permalinks.SessionURL(project, sess.ID),
		CreateError:   sess.CreateError,
		ArtifactCount: sess.ArtifactCount,
		MessageCount:  sess.MessageCount,
		AttentionOpen: sess.AttentionRequested,
	}
}

// ---------------------------------------------------------------------------
// Tool description — prompt, not documentation
// ---------------------------------------------------------------------------

const sessionListDescription = `List a worker's recent sessions (its past runs), newest first.

This is PROVENANCE, not history: you get when each run happened, what state it ` +
	`ended in, how much it produced, and a session_url a human can open. You do ` +
	`NOT get the conversation. There is no way to read a past transcript, ` +
	`deliberately — what a previous run concluded belongs in memory, so use ` +
	`memory_search or memory_current for that, and write a memory at the end of ` +
	`a run if you want your successor to know something.

Use it to answer "have I done this already, and when?", "did last night's run ` +
	`finish or fail?", and "which run produced that artifact?" — then follow the ` +
	`session_url or read the memories that run wrote.

IMPORTANT — this lists JOB sessions only, and an empty list does NOT mean "I ` +
	`have never run". A session is filed under a worker only when it was ` +
	`dispatched by an event or a schedule. Sessions started by a person in the ` +
	`chat UI — including long-lived conversational sessions — are not filed under ` +
	`any worker, so calling this from a chat session with no arguments correctly ` +
	`returns an empty list. From a chat session, name the worker you want: ` +
	`session_list with worker: "<the scheduled worker's name>".

Timestamps are unix SECONDS, which is not what the config log uses (that one is ` +
	`milliseconds) — do not compare the two without converting.

Scope: only this project's sessions, always. There is no project argument.`

func (s *sessionTools) tools() []*mcpTool {
	return []*mcpTool{
		{
			Name:        "session_list",
			Description: sessionListDescription,
			InputSchema: objectSchema(map[string]any{
				"worker": map[string]any{
					"type":        "string",
					"description": "Whose runs to list. Defaults to your own worker, which is empty when you are a chat session rather than a dispatched job.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "How many runs to return. Default 10, maximum 50.",
				},
			}, nil),
			Handler: s.list,
		},
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

type sessionListArgs struct {
	Worker string `json:"worker"`
	Limit  int    `json:"limit"`
}

func (s *sessionTools) list(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args sessionListArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}

	limit := args.Limit
	switch {
	case limit <= 0:
		limit = sessionListDefaultLimit
	case limit > sessionListMaxLimit:
		limit = sessionListMaxLimit
	}

	worker := strings.TrimSpace(args.Worker)
	if worker == "" {
		worker = caller.Worker
	}
	if worker == "" {
		// No filter is NOT the fallback. SessionQuery.Worker == "" means "every
		// session in the project" (agentdb/sessions.go:318), which would answer a
		// question nobody asked and hand a chat session the whole console's
		// history. Refusing to guess costs one explanatory note.
		return map[string]any{
			"sessions": []sessionRecord{},
			"count":    0,
			"worker":   "",
			"note": "No worker to list: this session was not dispatched as a worker's job, so it is not filed under one. " +
				"That is NOT the same as having no history — pass worker: \"<name>\" to list a particular worker's runs.",
		}, nil
	}

	rows, err := s.store.ListSessions(ctx, &agentdb.SessionQuery{
		Customer: caller.Project, // in code, always — never an argument (P5)
		Worker:   worker,
		Limit:    limit,
	})
	if err != nil {
		// Surfaced, never swallowed into an empty list: "no runs" is an answer a
		// model acts on, and a database blip must not be able to produce it.
		return nil, err
	}

	out := make([]sessionRecord, 0, len(rows))
	for _, sess := range rows {
		out = append(out, s.record(caller.Project, sess))
	}
	result := map[string]any{
		"sessions": out,
		"count":    len(out),
		"worker":   worker,
	}
	// A full page is indistinguishable from a complete one without saying so.
	// ListSessions has no "there is more" signal and asking for limit+1 would
	// pay for an extra row of COUNT(*) subqueries on every call, so this states
	// the boundary rather than measuring past it.
	if len(out) == limit {
		result["note"] = "This is the most recent page only; there may be older runs. Ask again with a larger limit (maximum 50)."
	}
	return result, nil
}
