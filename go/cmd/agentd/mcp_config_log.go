package main

// mcp_config_log.go — the config-log read tool (spec §15.9,
// docs/product/09-config-log.md), registered onto the host MCP server in
// mcpserver.go. Work-plan item J3.
//
// The whole surface is ONE tool:
//
//	config_history(query) → the project's configuration history, newest first
//
// and its most important property is what is absent. There is no
// `config_write`, no `config_restore`, no verb of any kind here, because a
// config-log record is never written directly: a record appears only ever as
// the shadow of a real mutation, inside that mutation's transaction (§15.4).
// Restoring is therefore not an operation on this table at all — it is an
// ordinary `worker_prompt_write` / `worker_update` / `schedule_create` whose
// rationale names the event being restored (§15.7). Git revert, never git
// reset.
//
// Project scope comes from the session token, in code (D3's rule): there is no
// project parameter, so a session physically cannot read another project's
// history (P5).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// configHistoryStore is the narrow read seam: one method, no writes. The type
// system is doing the "read-only" half of §15.9 here.
type configHistoryStore interface {
	ListConfigEvents(ctx context.Context, q agentdb.ConfigEventQuery) ([]*agentdb.ConfigEvent, error)
}

// Result sizing. §15.9 names no limit; the canonical uses ("the last ten prompt
// rewrites", "everything that changed this week") are small, and payloads are
// full rows — a skill_create payload carries a whole skill document (I3), so an
// unbounded page could be megabytes of tool result.
const (
	configHistoryDefaultLimit = 25
	configHistoryMaxLimit     = 200
)

type configLogTools struct {
	store      configHistoryStore
	permalinks permalinker
}

func newConfigLogTools(store configHistoryStore, permalinks permalinker) *configLogTools {
	return &configLogTools{store: store, permalinks: permalinks}
}

// ---------------------------------------------------------------------------
// Result shape
// ---------------------------------------------------------------------------

// configHistoryRecord is one record, exactly the tuple §15.9 names, plus
// `entity` — the key this record folds under, which is both the filter's own
// vocabulary ("ask again with entity: worker:x") and the only cheap way for a
// reader to tell two workers' rewrites apart in one mixed page.
type configHistoryRecord struct {
	ID           string          `json:"id"`
	Action       string          `json:"action"`
	Entity       string          `json:"entity"`
	ActorWorker  string          `json:"actor_worker"`
	ActorSession string          `json:"actor_session"`
	SessionURL   string          `json:"session_url"`
	Rationale    string          `json:"rationale"`
	Payload      agentdb.JSONMap `json:"payload"`
	CreatedAt    int64           `json:"created_at"`
}

func (c *configLogTools) record(project string, ev *agentdb.ConfigEvent) configHistoryRecord {
	entity := ""
	if ref, err := agentdb.EntityRefFor(ev); err == nil {
		entity = ref.String()
	}
	return configHistoryRecord{
		ID:           ev.ID,
		Action:       ev.Action,
		Entity:       entity,
		ActorWorker:  ev.ActorWorker,
		ActorSession: ev.ActorSession,
		// Empty for a human edit, which has no session to link to — the acting
		// human's identity is the login audit's business, not the log's (§15.2).
		SessionURL: c.sessionURL(project, ev.ActorSession),
		Rationale:  ev.Rationale,
		Payload:    ev.Payload,
		CreatedAt:  ev.CreatedAt,
	}
}

func (c *configLogTools) sessionURL(project, session string) string {
	if strings.TrimSpace(session) == "" {
		return ""
	}
	return c.permalinks.SessionURL(project, session)
}

// ---------------------------------------------------------------------------
// Tool description — prompt, not documentation
// ---------------------------------------------------------------------------

const configHistoryDescription = `Read this project's configuration history, newest first.

Every management change ever made — workers hired, disabled and retired, system ` +
	`prompts rewritten, subscriptions and schedules created and retuned, project ` +
	`settings changed, images and skills published — is a record here, with WHO ` +
	`made it, WHY (the rationale, a commit message), the FULL new state of the ` +
	`changed row, and a link to the session where it was decided.

Use it to answer "why is this worker like this?", to review what the ` +
	`organisation decided this week, and above all to RESTORE something: read the ` +
	`revision you want out of an old record's payload and write it forward with ` +
	`the ordinary tool (worker_prompt_write, worker_update, schedule_create…), ` +
	`quoting the event id in your rationale. Nothing here rewinds anything by ` +
	`itself and nothing deletes history — restoring is appending, so the mistake ` +
	`and the fix both stay on the record.

Filters, all optional and all ANDed:
 - entity: one thing's whole history — "worker:email-answerer", "schedule:<id>", ` +
	`"subscription:<id>", "image:toolbox:2", "skill:<name>", "project-settings", ` +
	`"project-prompt".
 - action: exactly one verb ("worker_prompt_write") or a trailing-* prefix ` +
	`("worker_*", "schedule_*").
 - actor_worker: only changes made by that worker.
 - since / until: an RFC3339 timestamp ("2026-07-18T00:00:00Z"), unix ` +
	`milliseconds, or a relative age such as "7d" or "24h", inclusive.
 - limit: how many records (default 25, maximum 200).

Timestamps in and out are unix MILLISECONDS, which is not what the event log ` +
	`uses — do not compare created_at here with an event's occurred_at without ` +
	`converting.

Payloads are the full row as it stood AFTER the change, never a diff: to see ` +
	`what a rewrite changed, read two consecutive records for the same entity and ` +
	`compare them yourself.`

func (c *configLogTools) tools() []*mcpTool {
	return []*mcpTool{
		{
			Name:        "config_history",
			Description: configHistoryDescription,
			InputSchema: objectSchema(map[string]any{
				"entity": map[string]any{
					"type":        "string",
					"description": "One entity's history: \"worker:<name>\", \"subscription:<id>\", \"schedule:<id>\", \"image:<name>:<version>\", \"skill:<name>\", \"project-settings\", \"project-prompt\".",
				},
				"action": map[string]any{
					"type":        "string",
					"description": "One action from the vocabulary, or a trailing-* prefix such as \"worker_*\".",
				},
				"actor_worker": map[string]any{
					"type":        "string",
					"description": "Only changes made by this worker. Human/API edits have no actor, so this excludes them.",
				},
				"since": timeArgSchema("lower"),
				"until": timeArgSchema("upper"),
				"limit": map[string]any{
					"type":        "integer",
					"description": "How many records to return. Default 25, maximum 200.",
				},
			}, nil),
			Handler: c.history,
		},
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

type configHistoryArgs struct {
	Entity      string    `json:"entity"`
	Action      string    `json:"action"`
	ActorWorker string    `json:"actor_worker"`
	Since       msTimeArg `json:"since"`
	Until       msTimeArg `json:"until"`
	Limit       int       `json:"limit"`
}

// msTimeArg and its parser live in timearg.go — shared with memory_search,
// which grew the same since/until pair. Both tools therefore accept the
// identical four forms, including a relative age such as "7d".

func (c *configLogTools) history(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args configHistoryArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}

	limit := args.Limit
	switch {
	case limit <= 0:
		limit = configHistoryDefaultLimit
	case limit > configHistoryMaxLimit:
		limit = configHistoryMaxLimit
	}

	// Validate the two filters that can be typed wrong, before the query, so a
	// typo reads as an error rather than as "nothing ever happened" (§9).
	entity := strings.TrimSpace(args.Entity)
	if entity != "" {
		if _, err := agentdb.ParseEntityRef(entity); err != nil {
			return nil, fmt.Errorf("entity: %w", err)
		}
	}
	action := strings.TrimSpace(args.Action)
	if action != "" {
		if err := validateConfigActionFilter(action); err != nil {
			return nil, err
		}
	}
	if err := checkTimeRange(args.Since, args.Until); err != nil {
		return nil, err
	}

	// Ask for one more than the limit so "there is more" is a fact rather than a
	// guess from a full page.
	evs, err := c.store.ListConfigEvents(ctx, agentdb.ConfigEventQuery{
		Project:     caller.Project, // in code, always — never an argument (P5)
		Entity:      entity,
		Action:      action,
		ActorWorker: strings.TrimSpace(args.ActorWorker),
		Since:       args.Since.MS,
		Until:       args.Until.MS,
		Limit:       limit + 1,
	})
	if err != nil {
		return nil, err
	}
	truncated := len(evs) > limit
	if truncated {
		evs = evs[:limit]
	}

	out := make([]configHistoryRecord, 0, len(evs))
	for _, ev := range evs {
		out = append(out, c.record(caller.Project, ev))
	}
	result := map[string]any{
		"records": out,
		"count":   len(out),
	}
	if truncated {
		result["truncated"] = true
		result["note"] = fmt.Sprintf(
			"Only the %d most recent matching records are shown. Narrow with entity, action or since/until to see older ones.", limit)
	}
	return result, nil
}

// validateConfigActionFilter rejects a verb outside the closed §15.3
// vocabulary. A prefix is checked by whether anything could ever match it:
// "worker_*" is legal, "wroker_*" is a typo that would otherwise return an
// empty history and read as "this project has never changed a worker".
func validateConfigActionFilter(action string) error {
	if strings.HasSuffix(action, "*") {
		prefix := strings.TrimSuffix(action, "*")
		for _, a := range agentdb.ConfigActions {
			if strings.HasPrefix(a, prefix) {
				return nil
			}
		}
		return fmt.Errorf("action: no configuration verb starts with %q; the vocabulary is %s",
			prefix, strings.Join(agentdb.ConfigActions, ", "))
	}
	for _, a := range agentdb.ConfigActions {
		if a == action {
			return nil
		}
	}
	return fmt.Errorf("action: %q is not a configuration verb; the vocabulary is %s (or a trailing-* prefix such as \"worker_*\")",
		action, strings.Join(agentdb.ConfigActions, ", "))
}
