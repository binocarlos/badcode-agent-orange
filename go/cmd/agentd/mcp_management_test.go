package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ---------------------------------------------------------------------------
// E4 — the core management MCP tools (§9).
//
// What is worth a test here, in order of what it would cost to get wrong:
//
//  1. worker_update cannot touch a system prompt, and neither prompt write can
//     happen without a rationale. Those two rules are the whole reason the
//     self-improvement loop leaves an honest record (§8.7, §15.5).
//  2. A prompt write leaves the kind=prompt-revision memory, containing the
//     PREVIOUS prompt and the rationale.
//  3. Nothing takes a project parameter, and every store call is scoped to the
//     token's project (D3's rule).
//  4. Malformed input fails loudly and writes nothing — never half (§9).
//  5. Every mutation echoes the row the STORE holds, not the one it was given.
// ---------------------------------------------------------------------------

// ── The fake store ──────────────────────────────────────────────────────────

// storedAt is stamped on every row the fake writes. A result carrying it can
// only have come from the store, which is how the read-back rule of §9 is
// proved rather than assumed.
const storedAt int64 = 4242

type fakeManagementStore struct {
	workers       map[string]*agentdb.Worker // project|name
	settings      map[string]*agentdb.ProjectSettings
	subscriptions map[string]*agentdb.Subscription // project|id
	schedules     map[string]*agentdb.Schedule
	// namedSessions keys session names by project|name — a session-mode
	// schedule's target is checked against them at write time (T9).
	namedSessions map[string]*agentdb.Session
	memories      []*agentdb.Memory
	// events records every project event the tools emitted — the
	// worker.freeze_refused signal is asserted against this (F1).
	events []*agentdb.ProjectEvent

	// Every ConfigWrite the tools passed, keyed by the call that made it — the
	// actor and the rationale are the audit story (§15.2).
	writes map[string][]agentdb.ConfigWrite
	// Every project string the tools scoped a call to.
	scopes []string

	memoryErr error
	eventErr  error
	nextID    int
}

func newFakeManagementStore() *fakeManagementStore {
	return &fakeManagementStore{
		workers:       map[string]*agentdb.Worker{},
		settings:      map[string]*agentdb.ProjectSettings{},
		subscriptions: map[string]*agentdb.Subscription{},
		schedules:     map[string]*agentdb.Schedule{},
		namedSessions: map[string]*agentdb.Session{},
		writes:        map[string][]agentdb.ConfigWrite{},
	}
}

func (f *fakeManagementStore) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func (f *fakeManagementStore) scope(project string) { f.scopes = append(f.scopes, project) }

func (f *fakeManagementStore) note(call string, cw agentdb.ConfigWrite) {
	f.writes[call] = append(f.writes[call], cw)
}

func key(a, b string) string { return a + "|" + b }

// seedWorker puts a worker in place without going through the tools.
func (f *fakeManagementStore) seedWorker(w *agentdb.Worker) *agentdb.Worker {
	w.CreatedAt, w.UpdatedAt = storedAt, storedAt
	f.workers[key(w.Project, w.Name)] = w
	return w
}

func (f *fakeManagementStore) ListWorkers(_ context.Context, project string) ([]*agentdb.Worker, error) {
	f.scope(project)
	var out []*agentdb.Worker
	for _, w := range f.workers {
		if w.Project == project {
			copied := *w
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (f *fakeManagementStore) GetWorker(_ context.Context, project, name string) (*agentdb.Worker, error) {
	f.scope(project)
	w, ok := f.workers[key(project, name)]
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s", agentdb.ErrWorkerNotFound, project, name)
	}
	copied := *w
	return &copied, nil
}

func (f *fakeManagementStore) UpsertWorker(_ context.Context, w *agentdb.Worker, cw agentdb.ConfigWrite) (*agentdb.Worker, error) {
	f.scope(w.Project)
	f.note("UpsertWorker", cw)
	stored := *w
	stored.CreatedAt, stored.UpdatedAt = storedAt, storedAt
	f.workers[key(w.Project, w.Name)] = &stored
	copied := stored
	return &copied, nil
}

func (f *fakeManagementStore) SetWorkerPrompt(_ context.Context, project, name, prompt string, cw agentdb.ConfigWrite) (*agentdb.Worker, string, error) {
	f.scope(project)
	w, ok := f.workers[key(project, name)]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s/%s", agentdb.ErrWorkerNotFound, project, name)
	}
	// The seam refuses the action without a rationale (§15.5) — the fake must
	// too, or the tool-level check would be the only thing tested.
	if strings.TrimSpace(cw.Rationale) == "" {
		return nil, "", errors.New(`agentdb: action "worker_prompt_write" requires a non-empty rationale (§15.5)`)
	}
	f.note("SetWorkerPrompt", cw)
	previous := w.SystemPrompt
	w.SystemPrompt = prompt
	w.UpdatedAt = storedAt
	copied := *w
	return &copied, previous, nil
}

func (f *fakeManagementStore) GetProjectSettings(_ context.Context, project string) (*agentdb.ProjectSettings, error) {
	f.scope(project)
	if ps, ok := f.settings[project]; ok {
		copied := *ps
		return &copied, nil
	}
	return agentdb.DefaultProjectSettings(project), nil
}

func (f *fakeManagementStore) SetProjectPrompt(_ context.Context, project, prompt string, cw agentdb.ConfigWrite) (*agentdb.ProjectSettings, string, error) {
	f.scope(project)
	if strings.TrimSpace(cw.Rationale) == "" {
		return nil, "", errors.New(`agentdb: action "project_prompt_write" requires a non-empty rationale (§15.5)`)
	}
	f.note("SetProjectPrompt", cw)
	ps, ok := f.settings[project]
	if !ok {
		ps = agentdb.DefaultProjectSettings(project)
		f.settings[project] = ps
	}
	previous := ps.SystemPrompt
	ps.SystemPrompt = prompt
	ps.UpdatedAt = storedAt
	copied := *ps
	return &copied, previous, nil
}

func (f *fakeManagementStore) ListSubscriptions(_ context.Context, project string) ([]*agentdb.Subscription, error) {
	f.scope(project)
	var out []*agentdb.Subscription
	for _, s := range f.subscriptions {
		if s.Project == project {
			copied := *s
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (f *fakeManagementStore) GetSubscription(_ context.Context, project, id string) (*agentdb.Subscription, error) {
	f.scope(project)
	s, ok := f.subscriptions[key(project, id)]
	if !ok {
		return nil, errors.New("subscription not found")
	}
	copied := *s
	return &copied, nil
}

func (f *fakeManagementStore) CreateSubscription(_ context.Context, sub *agentdb.Subscription, cw agentdb.ConfigWrite) (*agentdb.Subscription, error) {
	f.scope(sub.Project)
	f.note("CreateSubscription", cw)
	stored := *sub
	if stored.ID == "" {
		stored.ID = f.id("sub")
	}
	stored.CreatedAt, stored.UpdatedAt = storedAt, storedAt
	f.subscriptions[key(stored.Project, stored.ID)] = &stored
	copied := stored
	return &copied, nil
}

func (f *fakeManagementStore) DeleteSubscription(_ context.Context, project, id string, cw agentdb.ConfigWrite) error {
	f.scope(project)
	if _, ok := f.subscriptions[key(project, id)]; !ok {
		return errors.New("subscription not found")
	}
	f.note("DeleteSubscription", cw)
	delete(f.subscriptions, key(project, id))
	return nil
}

func (f *fakeManagementStore) addNamedSession(project, name string) *agentdb.Session {
	sess := &agentdb.Session{ID: f.id("sess"), Customer: project, Name: name}
	f.namedSessions[key(project, name)] = sess
	return sess
}

func (f *fakeManagementStore) GetSessionByName(_ context.Context, customer, name string) (*agentdb.Session, error) {
	f.scope(customer)
	s, ok := f.namedSessions[key(customer, name)]
	if !ok {
		return nil, fmt.Errorf("%w: %q in project %q", agentdb.ErrSessionNotFound, name, customer)
	}
	copied := *s
	return &copied, nil
}

func (f *fakeManagementStore) ListSchedules(_ context.Context, project string) ([]*agentdb.Schedule, error) {
	f.scope(project)
	var out []*agentdb.Schedule
	for _, s := range f.schedules {
		if s.Project == project {
			copied := *s
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (f *fakeManagementStore) GetSchedule(_ context.Context, project, id string) (*agentdb.Schedule, error) {
	f.scope(project)
	s, ok := f.schedules[key(project, id)]
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s", agentdb.ErrScheduleNotFound, project, id)
	}
	copied := *s
	return &copied, nil
}

func (f *fakeManagementStore) CreateSchedule(_ context.Context, sch *agentdb.Schedule, cw agentdb.ConfigWrite) (*agentdb.Schedule, error) {
	f.scope(sch.Project)
	// The real store validates the cron and never stores an unparseable one; the
	// fake must, or a tool test would "pass" with "@daily" in the table.
	if _, err := agentdb.ParseCron(sch.Cron); err != nil {
		return nil, fmt.Errorf("%w: %w", agentdb.ErrScheduleInvalid, err)
	}
	f.note("CreateSchedule", cw)
	stored := *sch
	if stored.ID == "" {
		stored.ID = f.id("sched")
	}
	stored.CreatedAt, stored.UpdatedAt = storedAt, storedAt
	f.schedules[key(stored.Project, stored.ID)] = &stored
	copied := stored
	return &copied, nil
}

func (f *fakeManagementStore) UpdateSchedule(_ context.Context, sch *agentdb.Schedule, cw agentdb.ConfigWrite) (*agentdb.Schedule, error) {
	f.scope(sch.Project)
	if _, err := agentdb.ParseCron(sch.Cron); err != nil {
		return nil, fmt.Errorf("%w: %w", agentdb.ErrScheduleInvalid, err)
	}
	if _, ok := f.schedules[key(sch.Project, sch.ID)]; !ok {
		return nil, fmt.Errorf("%w: %s/%s", agentdb.ErrScheduleNotFound, sch.Project, sch.ID)
	}
	f.note("UpdateSchedule", cw)
	stored := *sch
	stored.UpdatedAt = storedAt
	f.schedules[key(sch.Project, sch.ID)] = &stored
	copied := stored
	return &copied, nil
}

func (f *fakeManagementStore) DeleteSchedule(_ context.Context, project, id string, cw agentdb.ConfigWrite) error {
	f.scope(project)
	if _, ok := f.schedules[key(project, id)]; !ok {
		return fmt.Errorf("%w: %s/%s", agentdb.ErrScheduleNotFound, project, id)
	}
	f.note("DeleteSchedule", cw)
	delete(f.schedules, key(project, id))
	return nil
}

func (f *fakeManagementStore) CreateProjectEvent(_ context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error) {
	if f.eventErr != nil {
		return nil, f.eventErr
	}
	f.scope(ev.Project)
	stored := *ev
	if stored.ID == "" {
		stored.ID = f.id("evt")
	}
	f.events = append(f.events, &stored)
	copied := stored
	return &copied, nil
}

func (f *fakeManagementStore) CreateMemory(_ context.Context, m *agentdb.Memory, _ []float32) (*agentdb.Memory, error) {
	if f.memoryErr != nil {
		return nil, f.memoryErr
	}
	f.scope(m.Project)
	stored := *m
	stored.ID = f.id("mem")
	stored.CreatedAt = storedAt
	f.memories = append(f.memories, &stored)
	copied := stored
	return &copied, nil
}

// fakeAttention records the one call the adapter is allowed to make.
type fakeAttention struct {
	inputs []attentionRequestInput
	err    error
}

func (f *fakeAttention) Request(_ context.Context, in attentionRequestInput) (*attentionResult, error) {
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return nil, f.err
	}
	return &attentionResult{
		SessionURL: "https://orange.example.com/p/acme/s/" + in.SessionID,
		Message:    in.Message,
		Channel:    "webhook",
		Delivered:  true,
		RequestID:  "att-1",
	}, nil
}

func testManagementTools(store managementStore, attention attentionRequester) *managementTools {
	return newManagementTools(store, nil, attention, testPermalinker())
}

// seededTools returns the tools over a store holding one worker.
func seededTools(t *testing.T) (*fakeManagementStore, []*mcpTool) {
	t.Helper()
	store := newFakeManagementStore()
	w := agentdb.NewWorker("acme", "email-answerer")
	w.Description = "answers customer email"
	w.SystemPrompt = "Answer customer email."
	store.seedWorker(w)
	return store, testManagementTools(store, &fakeAttention{}).tools()
}

// ── Surface ─────────────────────────────────────────────────────────────────

// TestManagementToolsSurface pins the whole §9 surface — and, just as
// importantly, what is NOT in it.
func TestManagementToolsSurface(t *testing.T) {
	tools := testManagementTools(newFakeManagementStore(), &fakeAttention{}).tools()

	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Fatalf("tool %q has no description — descriptions are prompt, not documentation", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", tool.Name)
		}
	}
	want := []string{
		"worker_list", "worker_create", "worker_update",
		"worker_prompt_read", "worker_prompt_write",
		"project_prompt_read", "project_prompt_write",
		"subscription_list", "subscription_create", "subscription_delete",
		"schedule_list", "schedule_create", "schedule_update", "schedule_delete",
		"request_human_attention",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool surface =\n %v\nwant exactly\n %v", names, want)
	}

	// §9 closes with "no approval gate in core... do not re-grow it". A tool
	// named for approval, drafts or a pending queue would be exactly that.
	for _, name := range names {
		for _, forbidden := range []string{"approve", "approval", "draft", "pending", "review_queue", "restore_project"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("tool %q re-grows the approval engine §9 deleted", name)
			}
		}
	}

	// D3's rule: the project is the token's, in code. No tool may name one.
	for _, tool := range tools {
		props, _ := tool.InputSchema["properties"].(map[string]any)
		for _, forbidden := range []string{"project", "customer", "session", "session_id", "actor", "worker_id"} {
			if _, bad := props[forbidden]; bad {
				t.Fatalf("tool %q must not take a %q parameter (P5: the scope comes from the token)", tool.Name, forbidden)
			}
		}
	}

	// The two prompt writes must DEMAND a rationale in their schema, not merely
	// accept one: a required field is what a model reads first.
	for _, name := range []string{"worker_prompt_write", "project_prompt_write"} {
		tool := findTool(t, tools, name)
		required, _ := tool.InputSchema["required"].([]string)
		if !contains(required, "rationale") {
			t.Fatalf("%s must declare rationale as required (§15.5), got %v", name, required)
		}
	}

	// worker_update's description must say it refuses the prompt — the model
	// reads the description long before it reads an error.
	updateDesc := strings.ToLower(findTool(t, tools, "worker_update").Description)
	for _, phrase := range []string{"system_prompt", "worker_prompt_write"} {
		if !strings.Contains(updateDesc, phrase) {
			t.Fatalf("worker_update's description must mention %q", phrase)
		}
	}
}

// register panics on a duplicate tool name, and that panic happens at BOOT —
// the first time agentd starts, in production. This is the cheap place to find
// out that E4's names do not collide with D3's, I2's or I3's.
func TestManagementToolsRegisterAlongsideTheOtherToolSets(t *testing.T) {
	srv := newMCPServer(coreMCPServerName, func(*http.Request) (mcpCaller, error) {
		return testCaller(), nil
	})
	srv.register(newMemoryTools(nil, nil, testPermalinker()).tools()...)
	srv.register(newImageTools(nil, nil, testPermalinker()).tools()...)
	srv.register(newSkillTools(nil, nil, testPermalinker()).tools()...)
	srv.register(testManagementTools(newFakeManagementStore(), &fakeAttention{}).tools()...)

	names := srv.toolNames()
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate tool %q", n)
		}
		seen[n] = true
	}
	// The §9 surface really is reachable under the core server.
	for _, want := range []string{"worker_prompt_write", "worker_update", "request_human_attention", "memory_search"} {
		if !seen[want] {
			t.Fatalf("%q is not registered; have %v", want, names)
		}
	}
}

func findTool(t *testing.T, tools []*mcpTool, name string) *mcpTool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool %q", name)
	return nil
}

// ── worker_create / worker_list / worker_prompt_read ────────────────────────

func TestManagementToolsWorkerCreate(t *testing.T) {
	store := newFakeManagementStore()
	tools := testManagementTools(store, &fakeAttention{}).tools()

	res, err := invokeTool(t, tools, "worker_create", testCaller(), map[string]any{
		"name":          "tweet-writer",
		"description":   "writes the daily tweets",
		"system_prompt": "You write short, plain tweets.",
		"image":         "toolbox:2",
		"max_instances": 3,
		"briefing":      []string{"kind=brand-voice"},
		"rationale":     "the marketing manager asked for a tweet writer",
	})
	if err != nil {
		t.Fatalf("worker_create: %v", err)
	}

	stored := store.workers[key("acme", "tweet-writer")]
	if stored == nil {
		t.Fatalf("nothing was stored")
	}
	if stored.Project != "acme" {
		t.Fatalf("project = %q, want the caller's — never an argument", stored.Project)
	}
	if !stored.Enabled {
		t.Fatalf("a newly hired worker must be enabled")
	}
	if stored.MaxInstances != 3 || stored.Image != "toolbox:2" || len(stored.Briefing) != 1 {
		t.Fatalf("plumbing fields not stored: %+v", stored)
	}

	// §9 read-back: the timestamps can only have come from the store.
	if res["created_at"] != float64(storedAt) || res["updated_at"] != float64(storedAt) {
		t.Fatalf("the result must echo the STORED row, got %v", res)
	}
	// This call wrote the prompt, so the result carries it.
	if res["system_prompt"] != "You write short, plain tweets." {
		t.Fatalf("worker_create must echo the stored prompt: %v", res["system_prompt"])
	}

	// The config-log actor is the caller, and the rationale rode along (§15.2).
	cw := store.writes["UpsertWorker"][0]
	if cw.Worker != "email-answerer" || cw.Session != "sess-1" {
		t.Fatalf("ConfigWrite actor = %+v, want the calling worker/session", cw)
	}
	if cw.Rationale != "the marketing manager asked for a tweet writer" {
		t.Fatalf("rationale not threaded to the config log: %q", cw.Rationale)
	}
}

// Hiring is not overwriting: a create against a live worker must not be able to
// throw away its prompt.
func TestManagementToolsWorkerCreateRefusesAnExistingName(t *testing.T) {
	store, tools := seededTools(t)

	_, err := invokeTool(t, tools, "worker_create", testCaller(), map[string]any{
		"name": "email-answerer", "description": "x", "system_prompt": "Something else entirely.",
	})
	if err == nil {
		t.Fatalf("want a refusal")
	}
	for _, phrase := range []string{"already exists", "worker_prompt_write"} {
		if !strings.Contains(err.Error(), phrase) {
			t.Fatalf("the refusal should mention %q, got %q", phrase, err)
		}
	}
	if got := store.workers[key("acme", "email-answerer")].SystemPrompt; got != "Answer customer email." {
		t.Fatalf("the existing prompt was overwritten: %q", got)
	}
}

// Validation is exhaustive and happens BEFORE any write (§9: never half-written).
func TestManagementToolsWorkerCreateValidatesEverythingFirst(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing name", map[string]any{"description": "d", "system_prompt": "p"}, "name is required"},
		{"blank prompt", map[string]any{"name": "w", "description": "d", "system_prompt": "  "}, "system_prompt is required"},
		{"not kebab case", map[string]any{"name": "Tweet_Writer", "description": "d", "system_prompt": "p"}, "kebab-case"},
		{"negative instances", map[string]any{"name": "w", "description": "d", "system_prompt": "p", "max_instances": -2}, "max_instances"},
		{"registry url as image", map[string]any{"name": "w", "description": "d", "system_prompt": "p",
			"image": "eu.gcr.io/acme/toolbox"}, "image"},
		{"unparseable briefing", map[string]any{"name": "w", "description": "d", "system_prompt": "p",
			"briefing": []string{"kind in (lesson"}}, "briefing selector"},
		{"partial env reference", map[string]any{"name": "w", "description": "d", "system_prompt": "p",
			"mcp_config": map[string]any{"crm": map[string]any{"url": "https://x", "headers": map[string]any{"Authorization": "Bearer ${TOKEN}"}}}}, "mcp_config"},
		{"unknown argument", map[string]any{"name": "w", "description": "d", "system_prompt": "p", "project": "globex"}, "invalid arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeManagementStore()
			tools := testManagementTools(store, &fakeAttention{}).tools()
			_, err := invokeTool(t, tools, "worker_create", testCaller(), tc.args)
			if err == nil {
				t.Fatalf("want a refusal")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
			if len(store.workers) != 0 || len(store.writes) != 0 {
				t.Fatalf("a rejected create must write nothing: %+v", store.workers)
			}
		})
	}
}

// worker_list is the workforce without the prompts — a list of five workers must
// not be a wall of text.
func TestManagementToolsWorkerList(t *testing.T) {
	store, tools := seededTools(t)
	store.seedWorker(&agentdb.Worker{Project: "globex", Name: "intruder", SystemPrompt: "not yours"})

	res, err := invokeTool(t, tools, "worker_list", testCaller(), map[string]any{})
	if err != nil {
		t.Fatalf("worker_list: %v", err)
	}
	if res["count"] != float64(1) {
		t.Fatalf("count = %v: another project's workers must be invisible", res["count"])
	}
	workers, _ := res["workers"].([]any)
	first, _ := workers[0].(map[string]any)
	for _, key := range []string{"name", "description", "enabled", "image", "max_instances", "briefing", "system_prompt_bytes"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("listing entry is missing %q: %v", key, first)
		}
	}
	if _, leaked := first["system_prompt"]; leaked {
		t.Fatalf("worker_list must not carry prompt text — worker_prompt_read does: %v", first)
	}
	if first["system_prompt_bytes"] != float64(len("Answer customer email.")) {
		t.Fatalf("system_prompt_bytes = %v", first["system_prompt_bytes"])
	}
}

func TestManagementToolsWorkerPromptRead(t *testing.T) {
	_, tools := seededTools(t)

	res, err := invokeTool(t, tools, "worker_prompt_read", testCaller(), map[string]any{"name": "email-answerer"})
	if err != nil {
		t.Fatalf("worker_prompt_read: %v", err)
	}
	if res["system_prompt"] != "Answer customer email." {
		t.Fatalf("prompt not returned in full: %v", res["system_prompt"])
	}

	if _, err := invokeTool(t, tools, "worker_prompt_read", testCaller(), map[string]any{"name": "nobody"}); err == nil {
		t.Fatalf("want a not-found refusal")
	}
}

// ── worker_update — the refusal that matters ────────────────────────────────

// TestWorkerUpdateRefusesSystemPrompt is the pinned rule of §9: the prompt stays
// exclusively behind worker_prompt_write, whose revision-memory and required
// rationale must not be bypassable by a partial update.
func TestWorkerUpdateRefusesSystemPrompt(t *testing.T) {
	store, tools := seededTools(t)

	_, err := invokeTool(t, tools, "worker_update", testCaller(), map[string]any{
		"name":   "email-answerer",
		"fields": map[string]any{"system_prompt": "Be curt.", "description": "also this"},
	})
	if err == nil {
		t.Fatalf("worker_update must refuse a system_prompt field")
	}
	for _, phrase := range []string{"worker_prompt_write", "rationale", "prompt-revision"} {
		if !strings.Contains(err.Error(), phrase) {
			t.Fatalf("the refusal must explain %q so the model knows where to go, got %q", phrase, err)
		}
	}
	// And it wrote NOTHING — not even the description it was also handed.
	stored := store.workers[key("acme", "email-answerer")]
	if stored.SystemPrompt != "Answer customer email." || stored.Description != "answers customer email" {
		t.Fatalf("a refused update must not half-write: %+v", stored)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused update must append no config event: %+v", store.writes)
	}
}

func TestWorkerUpdatePartialFields(t *testing.T) {
	store, tools := seededTools(t)

	res, err := invokeTool(t, tools, "worker_update", testCaller(), map[string]any{
		"name":      "email-answerer",
		"fields":    map[string]any{"image": "toolbox", "max_instances": 4},
		"rationale": "the toolbox image has the CRM client baked in",
	})
	if err != nil {
		t.Fatalf("worker_update: %v", err)
	}

	stored := store.workers[key("acme", "email-answerer")]
	if stored.Image != "toolbox" || stored.MaxInstances != 4 {
		t.Fatalf("named fields did not move: %+v", stored)
	}
	// Everything NOT named is untouched — that is what "partial" means.
	if stored.SystemPrompt != "Answer customer email." || stored.Description != "answers customer email" || !stored.Enabled {
		t.Fatalf("an unnamed field changed: %+v", stored)
	}
	// The result never carries prompt text: this call cannot touch it.
	if _, leaked := res["system_prompt"]; leaked {
		t.Fatalf("worker_update must not echo prompt text: %v", res)
	}
	if res["updated_at"] != float64(storedAt) {
		t.Fatalf("the result must echo the STORED row: %v", res)
	}
	if store.writes["UpsertWorker"][0].Rationale != "the toolbox image has the CRM client baked in" {
		t.Fatalf("rationale not threaded to the config log")
	}
}

// Retiring a worker is enabled:false — and nothing else moves, which is what
// lets the config log record it as worker_disable rather than a generic update.
func TestWorkerUpdateDisableChangesOnlyEnabled(t *testing.T) {
	store, tools := seededTools(t)
	before := *store.workers[key("acme", "email-answerer")]

	if _, err := invokeTool(t, tools, "worker_update", testCaller(), map[string]any{
		"name": "email-answerer", "fields": map[string]any{"enabled": false},
	}); err != nil {
		t.Fatalf("worker_update: %v", err)
	}
	after := store.workers[key("acme", "email-answerer")]
	if after.Enabled {
		t.Fatalf("the worker is still enabled")
	}
	if after.SystemPrompt != before.SystemPrompt || after.Description != before.Description ||
		after.Image != before.Image || after.MaxInstances != before.MaxInstances {
		t.Fatalf("disabling changed something else: before %+v after %+v", before, after)
	}
}

func TestWorkerUpdateRejectsMalformedFields(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no such field", map[string]any{"name": "email-answerer", "fields": map[string]any{"model": "opus"}}, "not an updatable field"},
		{"renaming", map[string]any{"name": "email-answerer", "fields": map[string]any{"name": "other"}}, "identity"},
		{"empty fields", map[string]any{"name": "email-answerer", "fields": map[string]any{}}, "fields is empty"},
		{"wrong type", map[string]any{"name": "email-answerer", "fields": map[string]any{"enabled": "yes"}}, "fields.enabled"},
		{"zero instances", map[string]any{"name": "email-answerer", "fields": map[string]any{"max_instances": 0}}, "max_instances"},
		{"bad image", map[string]any{"name": "email-answerer", "fields": map[string]any{"image": "Toolbox:latest"}}, "image"},
		{"unknown worker", map[string]any{"name": "nobody", "fields": map[string]any{"enabled": false}}, "no worker"},
		{"unknown argument", map[string]any{"name": "email-answerer", "fields": map[string]any{"enabled": false}, "why": "x"}, "invalid arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, tools := seededTools(t)
			before := *store.workers[key("acme", "email-answerer")]
			_, err := invokeTool(t, tools, "worker_update", testCaller(), tc.args)
			if err == nil {
				t.Fatalf("want a refusal")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
			if got := *store.workers[key("acme", "email-answerer")]; got.Enabled != before.Enabled ||
				got.Image != before.Image || got.MaxInstances != before.MaxInstances {
				t.Fatalf("a rejected update must change nothing: %+v", got)
			}
		})
	}
}

// ── The prompt writes ───────────────────────────────────────────────────────

// TestManagementToolsPromptWriteRequiresRationale is the second half of E4's
// pinned pair: neither prompt write may happen without a why (§15.5).
func TestManagementToolsPromptWriteRequiresRationale(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"worker_prompt_write", map[string]any{"name": "email-answerer", "system_prompt": "Be warmer."}},
		{"worker_prompt_write", map[string]any{"name": "email-answerer", "system_prompt": "Be warmer.", "rationale": "   "}},
		{"project_prompt_write", map[string]any{"system_prompt": "House style: plain English."}},
		{"project_prompt_write", map[string]any{"system_prompt": "House style: plain English.", "rationale": ""}},
	} {
		t.Run(tc.tool+fmt.Sprintf("/%v", tc.args["rationale"]), func(t *testing.T) {
			store, tools := seededTools(t)
			_, err := invokeTool(t, tools, tc.tool, testCaller(), tc.args)
			if err == nil {
				t.Fatalf("%s must refuse a missing rationale", tc.tool)
			}
			if !strings.Contains(err.Error(), "rationale is required") {
				t.Fatalf("the refusal must name the rationale, got %q", err)
			}
			// Nothing written anywhere: not the prompt, not a config event, not a
			// memory (§9 — malformed input never half-writes).
			if store.workers[key("acme", "email-answerer")].SystemPrompt != "Answer customer email." {
				t.Fatalf("the prompt was written without a rationale")
			}
			if len(store.writes) != 0 || len(store.memories) != 0 {
				t.Fatalf("a refused prompt write must leave no trace: %+v %+v", store.writes, store.memories)
			}
		})
	}
}

// TestManagementToolsWorkerPromptWrite is §8.7 in one test: the rewrite lands,
// the rationale reaches the config log, and the superseded prompt is preserved
// in a kind=prompt-revision memory.
func TestManagementToolsWorkerPromptWrite(t *testing.T) {
	store, tools := seededTools(t)

	res, err := invokeTool(t, tools, "worker_prompt_write", testCaller(), map[string]any{
		"name":          "email-answerer",
		"system_prompt": "Answer customer email. Always acknowledge the customer's frustration first.",
		"rationale":     "read a hundred threads; every one of them was curt",
	})
	if err != nil {
		t.Fatalf("worker_prompt_write: %v", err)
	}

	stored := store.workers[key("acme", "email-answerer")]
	if !strings.Contains(stored.SystemPrompt, "acknowledge the customer's frustration") {
		t.Fatalf("the prompt was not replaced: %q", stored.SystemPrompt)
	}

	// The rationale reached the config log (§15.5), with the acting worker and
	// session as the actor (§15.2).
	cw := store.writes["SetWorkerPrompt"][0]
	if cw.Rationale != "read a hundred threads; every one of them was curt" {
		t.Fatalf("rationale not stored in the config event: %q", cw.Rationale)
	}
	if cw.Worker != "email-answerer" || cw.Session != "sess-1" {
		t.Fatalf("actor = %+v, want the calling worker/session", cw)
	}

	// The automatic memory (§9): one, labelled kind=prompt-revision + the worker,
	// carrying the RATIONALE and the PREVIOUS prompt.
	if len(store.memories) != 1 {
		t.Fatalf("want exactly one prompt-revision memory, got %d", len(store.memories))
	}
	mem := store.memories[0]
	if mem.Labels["kind"] != "prompt-revision" || mem.Labels["worker"] != "email-answerer" {
		t.Fatalf("memory labels = %v, want kind=prompt-revision,worker=email-answerer", mem.Labels)
	}
	if !strings.Contains(mem.Content, "Answer customer email.") {
		t.Fatalf("the memory must contain the PREVIOUS prompt: %q", mem.Content)
	}
	if strings.Contains(mem.Content, "acknowledge the customer's frustration") {
		t.Fatalf("the memory records the superseded prompt, not the new one: %q", mem.Content)
	}
	if !strings.Contains(mem.Content, "read a hundred threads") {
		t.Fatalf("the memory must echo the rationale (§15.5): %q", mem.Content)
	}
	if mem.Project != "acme" || mem.CreatedByWorker != "email-answerer" || mem.CreatedBySession != "sess-1" {
		t.Fatalf("memory provenance = %+v", mem)
	}

	// And the result says so, carrying the stored worker with its new prompt.
	revision, _ := res["prompt_revision"].(map[string]any)
	if revision["stored"] != true || revision["memory_id"] == nil {
		t.Fatalf("the result must report the revision memory: %v", res["prompt_revision"])
	}
	if revision["previous_prompt_bytes"] != float64(len("Answer customer email.")) {
		t.Fatalf("previous_prompt_bytes = %v", revision["previous_prompt_bytes"])
	}
	worker, _ := res["worker"].(map[string]any)
	if !strings.Contains(fmt.Sprint(worker["system_prompt"]), "acknowledge") {
		t.Fatalf("the result must echo the stored prompt: %v", worker)
	}
	if _, ok := res["warning"]; ok {
		t.Fatalf("nothing failed, so there must be no warning: %v", res["warning"])
	}
}

func TestManagementToolsProjectPromptWrite(t *testing.T) {
	store, tools := seededTools(t)
	store.settings["acme"] = &agentdb.ProjectSettings{Project: "acme", SystemPrompt: "We are BadCode."}

	res, err := invokeTool(t, tools, "project_prompt_write", testCaller(), map[string]any{
		"system_prompt": "We are BadCode. Write in plain English.",
		"rationale":     "the house style was agreed on Tuesday",
	})
	if err != nil {
		t.Fatalf("project_prompt_write: %v", err)
	}
	if store.settings["acme"].SystemPrompt != "We are BadCode. Write in plain English." {
		t.Fatalf("the project prompt was not replaced: %q", store.settings["acme"].SystemPrompt)
	}
	if store.writes["SetProjectPrompt"][0].Rationale != "the house style was agreed on Tuesday" {
		t.Fatalf("rationale not stored in the config event")
	}
	if len(store.memories) != 1 {
		t.Fatalf("want one prompt-revision memory, got %d", len(store.memories))
	}
	mem := store.memories[0]
	if mem.Labels["kind"] != "prompt-revision" || mem.Labels["scope"] != "project" {
		t.Fatalf("memory labels = %v, want kind=prompt-revision,scope=project", mem.Labels)
	}
	if !strings.Contains(mem.Content, "We are BadCode.") || !strings.Contains(mem.Content, "house style was agreed") {
		t.Fatalf("the memory must carry the previous prompt and the rationale: %q", mem.Content)
	}
	if res["system_prompt"] != "We are BadCode. Write in plain English." {
		t.Fatalf("the result must echo the stored prompt: %v", res["system_prompt"])
	}
}

// project_prompt_read reads what project_prompt_write wrote, and an unwritten
// project reads as empty rather than failing.
func TestManagementToolsProjectPromptRead(t *testing.T) {
	store, tools := seededTools(t)

	res, err := invokeTool(t, tools, "project_prompt_read", testCaller(), map[string]any{})
	if err != nil {
		t.Fatalf("project_prompt_read: %v", err)
	}
	if res["system_prompt"] != "" {
		t.Fatalf("an unwritten project prompt must read as empty, got %v", res["system_prompt"])
	}

	store.settings["acme"] = &agentdb.ProjectSettings{Project: "acme", SystemPrompt: "We are BadCode."}
	res, err = invokeTool(t, tools, "project_prompt_read", testCaller(), map[string]any{})
	if err != nil {
		t.Fatalf("project_prompt_read: %v", err)
	}
	if res["system_prompt"] != "We are BadCode." {
		t.Fatalf("system_prompt = %v", res["system_prompt"])
	}
}

// A prompt write for a worker that never had one records that fact rather than
// leaving the reader to wonder whether the old text was lost.
func TestManagementToolsPromptRevisionWithNoPreviousPrompt(t *testing.T) {
	store := newFakeManagementStore()
	store.seedWorker(&agentdb.Worker{Project: "acme", Name: "fresh-hire", Enabled: true})
	tools := testManagementTools(store, &fakeAttention{}).tools()

	if _, err := invokeTool(t, tools, "worker_prompt_write", testCaller(), map[string]any{
		"name": "fresh-hire", "system_prompt": "Do the thing.", "rationale": "first draft",
	}); err != nil {
		t.Fatalf("worker_prompt_write: %v", err)
	}
	if !strings.Contains(store.memories[0].Content, promptRevisionNoPrevious) {
		t.Fatalf("an absent previous prompt must be stated, not blank: %q", store.memories[0].Content)
	}
}

// The prompt and the memory are different substrates, so the memory can fail
// after the prompt is live. Telling the model "that failed" would then be the
// more dangerous lie.
func TestManagementToolsPromptWriteReportsAFailedRevisionMemory(t *testing.T) {
	store, tools := seededTools(t)
	store.memoryErr = errors.New("memory requires Postgres")

	res, err := invokeTool(t, tools, "worker_prompt_write", testCaller(), map[string]any{
		"name": "email-answerer", "system_prompt": "Be warmer.", "rationale": "curt threads",
	})
	if err != nil {
		t.Fatalf("a failed revision memory must not fail the prompt write: %v", err)
	}
	if store.workers[key("acme", "email-answerer")].SystemPrompt != "Be warmer." {
		t.Fatalf("the prompt must still have been written")
	}
	revision, _ := res["prompt_revision"].(map[string]any)
	if revision["stored"] != false || !strings.Contains(fmt.Sprint(revision["error"]), "Postgres") {
		t.Fatalf("the failure must be visible in the result: %v", res["prompt_revision"])
	}
	warning := fmt.Sprint(res["warning"])
	if !strings.Contains(warning, "PROMPT WAS WRITTEN") {
		t.Fatalf("the warning must say the prompt IS live: %q", warning)
	}
}

// ── Subscriptions & schedules ───────────────────────────────────────────────

func TestManagementToolsSubscriptions(t *testing.T) {
	store, tools := seededTools(t)

	res, err := invokeTool(t, tools, "subscription_create", testCaller(), map[string]any{
		"event_type":           "email.received",
		"worker":               "email-answerer",
		"filter":               map[string]any{"source": "external"},
		"max_firings_per_hour": 20,
		"rationale":            "answer inbound email",
	})
	if err != nil {
		t.Fatalf("subscription_create: %v", err)
	}
	id := fmt.Sprint(res["id"])
	if id == "" || res["enabled"] != true {
		t.Fatalf("a new subscription must be live and carry its id: %v", res)
	}
	if res["created_at"] != float64(storedAt) {
		t.Fatalf("the result must echo the STORED row: %v", res)
	}

	list, err := invokeTool(t, tools, "subscription_list", testCaller(), map[string]any{})
	if err != nil {
		t.Fatalf("subscription_list: %v", err)
	}
	if list["count"] != float64(1) {
		t.Fatalf("count = %v", list["count"])
	}

	del, err := invokeTool(t, tools, "subscription_delete", testCaller(), map[string]any{
		"id": id, "rationale": "answered by a different worker now",
	})
	if err != nil {
		t.Fatalf("subscription_delete: %v", err)
	}
	deleted, _ := del["deleted"].(map[string]any)
	if deleted["event_type"] != "email.received" {
		t.Fatalf("a delete must echo what was removed: %v", del)
	}
	if len(store.subscriptions) != 0 {
		t.Fatalf("the row is still there")
	}
	if store.writes["DeleteSubscription"][0].Rationale != "answered by a different worker now" {
		t.Fatalf("rationale not threaded on delete")
	}
}

// Routing at a worker that does not exist would fail once, at 03:00, invisibly.
func TestManagementToolsSubscriptionCreateRequiresAKnownWorker(t *testing.T) {
	store, tools := seededTools(t)
	_, err := invokeTool(t, tools, "subscription_create", testCaller(), map[string]any{
		"event_type": "email.received", "worker": "nobody",
	})
	if err == nil || !strings.Contains(err.Error(), "no worker") {
		t.Fatalf("want a refusal naming the missing worker, got %v", err)
	}
	if len(store.subscriptions) != 0 {
		t.Fatalf("nothing may be stored")
	}
}

func TestManagementToolsSchedules(t *testing.T) {
	store, tools := seededTools(t)

	res, err := invokeTool(t, tools, "schedule_create", testCaller(), map[string]any{
		"worker": "email-answerer", "cron": "0 10 * * *", "input": "write the morning tweet",
	})
	if err != nil {
		t.Fatalf("schedule_create: %v", err)
	}
	id := fmt.Sprint(res["id"])
	if res["enabled"] != true || res["input"] != "write the morning tweet" {
		t.Fatalf("stored schedule not echoed: %v", res)
	}

	upd, err := invokeTool(t, tools, "schedule_update", testCaller(), map[string]any{
		"id":        id,
		"fields":    map[string]any{"cron": "0 17 * * *", "input": "write the evening tweet"},
		"rationale": "the mornings were not landing",
	})
	if err != nil {
		t.Fatalf("schedule_update: %v", err)
	}
	if upd["cron"] != "0 17 * * *" || upd["input"] != "write the evening tweet" {
		t.Fatalf("update not applied: %v", upd)
	}
	if upd["worker"] != "email-answerer" {
		t.Fatalf("an unnamed field changed: %v", upd)
	}

	list, err := invokeTool(t, tools, "schedule_list", testCaller(), map[string]any{})
	if err != nil {
		t.Fatalf("schedule_list: %v", err)
	}
	if list["count"] != float64(1) {
		t.Fatalf("count = %v", list["count"])
	}

	if _, err := invokeTool(t, tools, "schedule_delete", testCaller(), map[string]any{"id": id}); err != nil {
		t.Fatalf("schedule_delete: %v", err)
	}
	if len(store.schedules) != 0 {
		t.Fatalf("the schedule is still there")
	}
}

// A cron the scheduler cannot parse is refused at config time, not at 03:00 —
// and a nickname is refused rather than silently expanded.
func TestManagementToolsScheduleCreateValidates(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"nickname", map[string]any{"worker": "email-answerer", "cron": "@daily", "input": "x"}, "cron"},
		{"garbage cron", map[string]any{"worker": "email-answerer", "cron": "every morning", "input": "x"}, "cron"},
		{"blank input", map[string]any{"worker": "email-answerer", "cron": "0 10 * * *", "input": "  "}, "input is required"},
		{"unknown worker", map[string]any{"worker": "nobody", "cron": "0 10 * * *", "input": "x"}, "no worker"},
		// The T9 XOR, refused with a sentence the model can act on rather than a
		// schema failure it cannot read.
		{"no target", map[string]any{"cron": "0 10 * * *", "input": "x"}, "either worker"},
		{"both targets", map[string]any{
			"worker": "email-answerer", "target_session": "hypothesis-a", "cron": "0 10 * * *", "input": "x",
		}, "not both"},
		{"unknown session", map[string]any{
			"target_session": "nobody-home", "cron": "0 10 * * *", "input": "x",
		}, "no session named"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, tools := seededTools(t)
			_, err := invokeTool(t, tools, "schedule_create", testCaller(), tc.args)
			if err == nil {
				t.Fatalf("want a refusal")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
			if len(store.schedules) != 0 {
				t.Fatalf("nothing may be stored")
			}
		})
	}
}

// TestManagementToolsScheduleCreateSessionMode: a worker may put a long-lived
// session on a cron (T9), and the row it writes carries the session name instead
// of a worker. The tool description is asserted too, because what a resumed
// session does and does not refresh is the single thing a model is most likely
// to assume backwards — and the description is the only place it can read it.
func TestManagementToolsScheduleCreateSessionMode(t *testing.T) {
	store, tools := seededTools(t)
	store.addNamedSession("acme", "hypothesis-a")

	res, err := invokeTool(t, tools, "schedule_create", testCaller(), map[string]any{
		"target_session": "hypothesis-a",
		"cron":           "0 7 * * *",
		"input":          "read the newest research memory and update your view",
	})
	if err != nil {
		t.Fatalf("schedule_create: %v", err)
	}
	// Asserted on the JSON the model actually sees, not on the Go struct: a
	// field that never made it onto the wire would be invisible otherwise.
	if res["target_session"] != "hypothesis-a" || res["worker"] != "" {
		t.Fatalf("session mode did not round-trip through the tool: %+v", res)
	}

	var created *agentdb.Schedule
	for _, s := range store.schedules {
		created = s
	}
	if created == nil || created.TargetSession != "hypothesis-a" || created.Worker != "" {
		t.Fatalf("stored row is not a session schedule: %+v", created)
	}

	// The description must state the three refresh answers. A model that thinks a
	// resumed session picked up a new tool, or that editing a prompt is how you
	// hand it today's numbers, writes a loop that silently never works.
	var desc string
	for _, tool := range tools {
		if tool.Name == "schedule_create" {
			desc = tool.Description
		}
	}
	for _, want := range []string{"target_session", "every single turn", "fixed when the container",
		"Briefings are assembled", "memory_current"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("schedule_create's description must explain %q:\n%s", want, desc)
		}
	}
}

// ── request_human_attention ─────────────────────────────────────────────────

// The tool is an ADAPTER: it forwards the token's project and session and adds
// no mechanics of its own (H2's rule — the §9 primitive is implemented once).
func TestManagementToolsRequestHumanAttentionIsAnAdapter(t *testing.T) {
	store := newFakeManagementStore()
	att := &fakeAttention{}
	tools := testManagementTools(store, att).tools()

	res, err := invokeTool(t, tools, "request_human_attention", testCaller(), map[string]any{
		"message": "the customer is asking for a refund I cannot authorise", "expires_in": 3600,
	})
	if err != nil {
		t.Fatalf("request_human_attention: %v", err)
	}
	if len(att.inputs) != 1 {
		t.Fatalf("want exactly one call into the attention service, got %d", len(att.inputs))
	}
	in := att.inputs[0]
	if in.Project != "acme" || in.SessionID != "sess-1" {
		t.Fatalf("project/session must come from the token, got %+v", in)
	}
	if in.ExpiresIn != 3600 {
		t.Fatalf("expires_in not forwarded: %+v", in)
	}
	if res["session_url"] != "https://orange.example.com/p/acme/s/sess-1" {
		t.Fatalf("the permalink must be echoed (§9): %v", res["session_url"])
	}
	// The adapter writes nothing itself — the service owns every side effect.
	if len(store.writes) != 0 || len(store.memories) != 0 {
		t.Fatalf("the adapter must not write anything of its own: %+v", store.writes)
	}
}

func TestManagementToolsRequestHumanAttentionNeedsASession(t *testing.T) {
	att := &fakeAttention{}
	tools := testManagementTools(newFakeManagementStore(), att).tools()

	_, err := invokeTool(t, tools, "request_human_attention", mcpCaller{Project: "acme"},
		map[string]any{"message": "help"})
	if err == nil || !strings.Contains(err.Error(), "inside a session") {
		t.Fatalf("want a refusal naming the missing session, got %v", err)
	}
	if len(att.inputs) != 0 {
		t.Fatalf("nothing may be requested without a session")
	}
}

// An unconfigured deployment refuses loudly rather than pretending a human was
// told. (A typed nil in an interface is not nil — this is the regression guard.)
func TestManagementToolsRequestHumanAttentionUnavailable(t *testing.T) {
	var svc *attentionService
	tools := newManagementTools(newFakeManagementStore(), nil, svc, testPermalinker()).tools()

	_, err := invokeTool(t, tools, "request_human_attention", testCaller(), map[string]any{"message": "help"})
	if err == nil || !strings.Contains(err.Error(), "NO human was told") {
		t.Fatalf("want a loud refusal, got %v", err)
	}
}

// ── Tenancy ─────────────────────────────────────────────────────────────────

// Every store call this file makes is scoped to the token's project, in code.
// The tools have no project parameter (pinned in TestManagementToolsSurface);
// this proves nothing reaches the store under a different one either.
func TestManagementToolsScopeAlwaysComesFromTheToken(t *testing.T) {
	store, tools := seededTools(t)
	caller := testCaller()

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"worker_list", map[string]any{}},
		{"worker_create", map[string]any{"name": "new-hire", "description": "d", "system_prompt": "p"}},
		{"worker_update", map[string]any{"name": "email-answerer", "fields": map[string]any{"enabled": true}}},
		{"worker_prompt_read", map[string]any{"name": "email-answerer"}},
		{"worker_prompt_write", map[string]any{"name": "email-answerer", "system_prompt": "p2", "rationale": "r"}},
		{"project_prompt_read", map[string]any{}},
		{"project_prompt_write", map[string]any{"system_prompt": "p", "rationale": "r"}},
		{"subscription_list", map[string]any{}},
		{"subscription_create", map[string]any{"event_type": "email.received", "worker": "email-answerer"}},
		{"schedule_list", map[string]any{}},
		{"schedule_create", map[string]any{"worker": "email-answerer", "cron": "0 10 * * *", "input": "i"}},
	}
	for _, c := range calls {
		if _, err := invokeTool(t, tools, c.tool, caller, c.args); err != nil {
			t.Fatalf("%s: %v", c.tool, err)
		}
	}
	if len(store.scopes) == 0 {
		t.Fatalf("no store call was recorded — the test proves nothing")
	}
	for _, got := range store.scopes {
		if got != "acme" {
			t.Fatalf("a store call was scoped to %q, not the token's project", got)
		}
	}
}

// A tool handler must never see a raw argument it did not declare: a misspelled
// field silently ignored is how a model believes it changed something it did not.
func TestManagementToolsRejectUnknownArguments(t *testing.T) {
	_, tools := seededTools(t)
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"definitely_not_a_field": "x"})
			if _, err := tool.Handler(context.Background(), testCaller(), raw); err == nil ||
				!strings.Contains(err.Error(), "invalid arguments") {
				t.Fatalf("%s accepted an unknown argument: %v", tool.Name, err)
			}
		})
	}
}
