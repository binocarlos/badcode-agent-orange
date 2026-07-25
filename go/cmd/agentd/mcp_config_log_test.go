package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ---------------------------------------------------------------------------
// J3 — `config_history` (§15.9).
//
// What is worth a test here: the project scope is taken from the token and can
// never be argued about; a mistyped filter is an ERROR rather than an empty
// history (which would read as "nothing ever happened"); provenance carries the
// session permalink; and the tool cannot write.
// ---------------------------------------------------------------------------

type fakeConfigHistoryStore struct {
	queries []agentdb.ConfigEventQuery
	rows    []*agentdb.ConfigEvent
	err     error
}

func (f *fakeConfigHistoryStore) ListConfigEvents(_ context.Context, q agentdb.ConfigEventQuery) ([]*agentdb.ConfigEvent, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func configHistoryTools(store configHistoryStore) []*mcpTool {
	return newConfigLogTools(store, permalinker{base: "https://ui.example"}).tools()
}

func historyRow(id, action string, payload agentdb.JSONMap, worker, session, rationale string) *agentdb.ConfigEvent {
	return &agentdb.ConfigEvent{
		ID: id, Project: "acme", Action: action, Payload: payload,
		ActorWorker: worker, ActorSession: session, Rationale: rationale,
		CreatedAt: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}
}

// TestConfigHistoryIsProjectScopedByTheToken: there is no project parameter and
// there never will be — the scope is the caller's, in code (P5, D3's rule).
func TestConfigHistoryIsProjectScopedByTheToken(t *testing.T) {
	store := &fakeConfigHistoryStore{}
	tools := configHistoryTools(store)

	if _, err := invokeTool(t, tools, "config_history",
		mcpCaller{Project: "acme", SessionID: "s-1", Worker: "manager"}, map[string]any{}); err != nil {
		t.Fatalf("config_history: %v", err)
	}
	if len(store.queries) != 1 || store.queries[0].Project != "acme" {
		t.Fatalf("query did not carry the caller's project: %+v", store.queries)
	}

	// The schema has no project property, so a model cannot even ask.
	schema := tools[0].InputSchema["properties"].(map[string]any)
	if _, found := schema["project"]; found {
		t.Fatal("config_history exposes a project argument — scope must come from the token only")
	}
}

// TestConfigHistoryReturnsProvenanceAndPermalinks: "who decided this" is always
// one click from the conversation where it was decided (§15.9, §7.3).
func TestConfigHistoryReturnsProvenanceAndPermalinks(t *testing.T) {
	store := &fakeConfigHistoryStore{rows: []*agentdb.ConfigEvent{
		historyRow("ce-41", agentdb.ActionWorkerPromptWrite,
			agentdb.JSONMap{"name": "email-answerer", "system_prompt": "be brief"},
			"email-reviewer", "s-991", "shorten replies"),
		historyRow("ce-40", agentdb.ActionProjectSettingsPut,
			agentdb.JSONMap{"project": "acme"}, "", "", ""),
	}}
	out, err := invokeTool(t, configHistoryTools(store), "config_history",
		mcpCaller{Project: "acme", SessionID: "s-1"}, map[string]any{})
	if err != nil {
		t.Fatalf("config_history: %v", err)
	}
	records, _ := out["records"].([]any)
	if len(records) != 2 {
		t.Fatalf("want 2 records, got %v", out["records"])
	}
	first, _ := records[0].(map[string]any)
	if first["id"] != "ce-41" {
		t.Fatalf("records are not in the store's (newest-first) order: %v", first["id"])
	}
	if first["session_url"] != "https://ui.example/p/acme/s/s-991" {
		t.Fatalf("session_url: %v", first["session_url"])
	}
	if first["entity"] != "worker:email-answerer" {
		t.Fatalf("entity: %v", first["entity"])
	}
	if first["rationale"] != "shorten replies" {
		t.Fatalf("rationale: %v", first["rationale"])
	}
	payload, _ := first["payload"].(map[string]any)
	if payload["system_prompt"] != "be brief" {
		t.Fatalf("payload must be the full new state: %v", payload)
	}

	// A human edit has no session, so it gets no link rather than a broken one.
	second, _ := records[1].(map[string]any)
	if second["session_url"] != "" {
		t.Fatalf("a human edit was given a session permalink: %v", second["session_url"])
	}
}

// TestConfigHistoryFiltersReachTheStore covers the §15.9 filter surface,
// including the ms-vs-RFC3339 flexibility of since/until.
func TestConfigHistoryFiltersReachTheStore(t *testing.T) {
	store := &fakeConfigHistoryStore{}
	since := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)

	if _, err := invokeTool(t, configHistoryTools(store), "config_history",
		mcpCaller{Project: "acme"}, map[string]any{
			"entity":       "worker:email-answerer",
			"action":       "worker_*",
			"actor_worker": "email-reviewer",
			"since":        since.Format(time.RFC3339),
			"until":        since.Add(48 * time.Hour).UnixMilli(),
			"limit":        10,
		}); err != nil {
		t.Fatalf("config_history: %v", err)
	}
	q := store.queries[0]
	if q.Entity != "worker:email-answerer" || q.Action != "worker_*" || q.ActorWorker != "email-reviewer" {
		t.Fatalf("filters lost on the way to the store: %+v", q)
	}
	if q.Since != since.UnixMilli() {
		t.Fatalf("since: want %d (ms), got %d", since.UnixMilli(), q.Since)
	}
	if q.Until != since.Add(48*time.Hour).UnixMilli() {
		t.Fatalf("until: want %d, got %d", since.Add(48*time.Hour).UnixMilli(), q.Until)
	}
	// One more than the limit, so "there is more" is a fact rather than a guess.
	if q.Limit != 11 {
		t.Fatalf("limit: want 11 (10 + 1), got %d", q.Limit)
	}
}

// TestConfigHistoryRefusesMistypedFilters: §9's loud-failure rule. An unknown
// verb or entity kind must not come back as an empty page, which a model would
// read as "this has never been changed".
func TestConfigHistoryRefusesMistypedFilters(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown action", map[string]any{"action": "worker_promote"}, "not a configuration verb"},
		{"unknown prefix", map[string]any{"action": "wroker_*"}, "no configuration verb starts with"},
		{"unknown entity kind", map[string]any{"entity": "wroker:x"}, "not a known entity kind"},
		{"keyless entity", map[string]any{"entity": "worker"}, "needs a key"},
		{"keyed singleton", map[string]any{"entity": "project-settings:x"}, "takes no key"},
		{"inverted range", map[string]any{"since": 200, "until": 100}, "matches nothing"},
		{"unknown argument", map[string]any{"selector": "x"}, "invalid arguments"},
		{"unparseable since", map[string]any{"since": "last tuesday"}, "RFC3339"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeConfigHistoryStore{}
			_, err := invokeTool(t, configHistoryTools(store), "config_history",
				mcpCaller{Project: "acme"}, tc.args)
			if err == nil {
				t.Fatalf("%v was accepted", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not explain the problem (want %q)", err, tc.want)
			}
			if len(store.queries) != 0 {
				t.Fatal("a rejected filter still hit the store")
			}
		})
	}
}

// TestConfigHistoryCapsAndAnnouncesTruncation: a full page must never look like
// the whole history.
func TestConfigHistoryCapsAndAnnouncesTruncation(t *testing.T) {
	rows := make([]*agentdb.ConfigEvent, 0, configHistoryDefaultLimit+1)
	for i := 0; i <= configHistoryDefaultLimit; i++ {
		rows = append(rows, historyRow("ce", agentdb.ActionWorkerUpdate,
			agentdb.JSONMap{"name": "w"}, "manager", "s-1", ""))
	}
	store := &fakeConfigHistoryStore{rows: rows}

	out, err := invokeTool(t, configHistoryTools(store), "config_history",
		mcpCaller{Project: "acme"}, map[string]any{})
	if err != nil {
		t.Fatalf("config_history: %v", err)
	}
	if got := int(out["count"].(float64)); got != configHistoryDefaultLimit {
		t.Fatalf("count: want %d, got %d", configHistoryDefaultLimit, got)
	}
	if out["truncated"] != true {
		t.Fatalf("a full page did not say it was truncated: %v", out)
	}

	// And an absurd limit is clamped rather than honoured.
	store.queries = nil
	if _, err := invokeTool(t, configHistoryTools(store), "config_history",
		mcpCaller{Project: "acme"}, map[string]any{"limit": 100000}); err != nil {
		t.Fatalf("config_history: %v", err)
	}
	if store.queries[0].Limit != configHistoryMaxLimit+1 {
		t.Fatalf("limit was not clamped: %d", store.queries[0].Limit)
	}
}

// TestConfigHistoryIsReadOnly pins the absence that matters: §15.9 says no tool
// writes config_events, and the only way to keep that true is to ship no verb.
func TestConfigHistoryIsReadOnly(t *testing.T) {
	tools := configHistoryTools(&fakeConfigHistoryStore{})
	if len(tools) != 1 || tools[0].Name != "config_history" {
		names := make([]string, 0, len(tools))
		for _, tt := range tools {
			names = append(names, tt.Name)
		}
		t.Fatalf("the config-log tool surface must be exactly config_history, got %v", names)
	}
	if !strings.Contains(tools[0].Description, "restor") {
		t.Fatal("the description must tell the model how to restore (append forward), or it will look for a restore verb")
	}
}
