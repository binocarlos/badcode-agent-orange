package main

// mcp_actor_test.go — RD4 (docs/product/22-readiness.md §5).
//
// THE INVARIANT: an empty ConfigEvent.ActorWorker means a human, and nothing
// else can produce one.
//
// §15.2 chose emptiness as the record of a human/UI/API edit (httpapi's
// humanEdit) and web/src/configLog.ts renders it as "a person did this". The MCP
// server is the WORKERS' path, so anything it writes with an empty actor is a
// forgery: a worker rewriting another worker's prompt during a database blip
// appeared in the changelog as an operator hand-edit. That is the attribution
// §8.7's acceptance loop reads to tell whether a worker improved another, and
// the bucketing doctrine OM-10 requires.
//
// Two guards, tested here in that order:
//  1. the auth seam refuses a caller whose session row cannot be read; and
//  2. no mutating tool can build a ConfigWrite without an identity.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ── Guard 1: the auth seam ──────────────────────────────────────────────────

// erroringSessionLookup is a session store that is present and unhappy — the
// database-blip case, distinct from "no session store wired at all".
type erroringSessionLookup struct{ err error }

func (e erroringSessionLookup) GetSession(context.Context, string) (*agentdb.Session, error) {
	return nil, e.err
}

// TestMCPAuthRefusesWhenTheSessionCannotBeRead: this used to log and carry on,
// handing the tools a caller with an empty Worker. The old comment justified it
// with "the store may be the SQLite fallback, which knows nothing of workers" —
// which stopped being true when the core MCP server was gated on Postgres
// (main.go mounts it only when agentDB != nil), so on the SQLite fallback there
// is no /mcp to reach at all.
func TestMCPAuthRefusesWhenTheSessionCannotBeRead(t *testing.T) {
	secret := []byte("s3cret")
	auth := newSessionTokenAuth(secret, erroringSessionLookup{err: errDatabaseUnhappy})

	req := httptest.NewRequest(http.MethodPost, coreMCPPath, strings.NewReader("{}"))
	req.Header.Set("Authorization", mintSessionToken(t, secret, time.Hour, "acme", "sess-1"))

	caller, err := auth.authenticate(req)
	if err == nil {
		t.Fatalf("an unreadable session was accepted, caller = %#v — that caller would write as a human", caller)
	}
	if caller.Project != "" || caller.Worker != "" {
		t.Fatalf("a refused caller must carry nothing, got %#v", caller)
	}
	if !errors.Is(err, errMCPUnauthorized) {
		t.Fatalf("err = %v, want it to map to a 401 so the harness retries rather than gives up", err)
	}
}

// TestMCPAuthCarriesTheWorkerWhenTheSessionReads is the twin: the refusal must
// not have been implemented by simply never resolving a worker.
func TestMCPAuthCarriesTheWorkerWhenTheSessionReads(t *testing.T) {
	secret := []byte("s3cret")
	sessions := &fakeSessionLookup{sessions: map[string]*agentdb.Session{
		"sess-1": {ID: "sess-1", Customer: "acme", Worker: "email-answerer"},
	}}
	auth := newSessionTokenAuth(secret, sessions)

	req := httptest.NewRequest(http.MethodPost, coreMCPPath, strings.NewReader("{}"))
	req.Header.Set("Authorization", mintSessionToken(t, secret, time.Hour, "acme", "sess-1"))

	caller, err := auth.authenticate(req)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if caller.Worker != "email-answerer" {
		t.Fatalf("caller = %#v, want the session's worker as provenance", caller)
	}
}

// ── Guard 2: no mutation without an identity ────────────────────────────────

// TestConfigWriteRefusesAnUnidentifiedCaller pins the choke point itself.
func TestConfigWriteRefusesAnUnidentifiedCaller(t *testing.T) {
	if _, err := (mcpCaller{Project: "acme", SessionID: "sess-1"}).configWrite("why"); err == nil {
		t.Fatalf("a caller with no worker must not be able to build a config-log actor")
	}
	cw, err := (mcpCaller{Project: "acme", SessionID: "sess-1", Worker: "w"}).configWrite("  why  ")
	if err != nil {
		t.Fatalf("an identified caller must still write: %v", err)
	}
	if cw.Worker != "w" || cw.Session != "sess-1" || cw.Rationale != "why" {
		t.Fatalf("ConfigWrite = %+v", cw)
	}
}

// mutatingToolCase is one management mutation, with arguments valid enough to
// reach the store.
type mutatingToolCase struct {
	tool string
	args map[string]any
}

// managementMutations is every management tool that writes a config event.
// Keeping the list explicit (rather than deriving it) is the point: adding a
// mutation without adding it here shows up as an uncovered tool in the
// completeness check below.
func managementMutations() []mutatingToolCase {
	return []mutatingToolCase{
		{"worker_create", map[string]any{
			"name": "tweet-author", "description": "writes tweets", "system_prompt": "Write tweets.",
		}},
		{"worker_update", map[string]any{
			"name": "email-answerer", "fields": map[string]any{"description": "new"},
		}},
		{"worker_prompt_write", map[string]any{
			"name": "email-answerer", "system_prompt": "Answer briefly.", "rationale": "too verbose",
		}},
		{"project_prompt_write", map[string]any{
			"system_prompt": "We are BadCode.", "rationale": "the house voice changed",
		}},
		{"subscription_create", map[string]any{"event_type": "email.received", "worker": "email-answerer"}},
		{"subscription_delete", map[string]any{"id": "sub-seeded"}},
		{"schedule_create", map[string]any{"worker": "email-answerer", "cron": "0 9 * * *", "input": "tweet"}},
		{"schedule_update", map[string]any{"id": "sched-seeded", "fields": map[string]any{"input": "post"}}},
		{"schedule_delete", map[string]any{"id": "sched-seeded"}},
	}
}

// seedForMutations builds a store holding everything managementMutations() needs
// to reach a write.
func seedForMutations(t *testing.T) (*fakeManagementStore, []*mcpTool) {
	t.Helper()
	store := newFakeManagementStore()
	w := agentdb.NewWorker("acme", "email-answerer")
	w.Description = "answers customer email"
	w.SystemPrompt = "Answer customer email."
	store.seedWorker(w)
	store.subscriptions[key("acme", "sub-seeded")] = &agentdb.Subscription{
		ID: "sub-seeded", Project: "acme", EventType: "email.received", Worker: "email-answerer", Enabled: true,
	}
	store.schedules[key("acme", "sched-seeded")] = &agentdb.Schedule{
		ID: "sched-seeded", Project: "acme", Worker: "email-answerer",
		Cron: "0 9 * * *", Input: "tweet", Enabled: true,
	}
	return store, testManagementTools(store, &fakeAttention{}).tools()
}

// TestNoMCPConfigEventCanCarryAnEmptyActor is the invariant, tool by tool.
//
// Each case is run TWICE with identical arguments: once with an identified
// caller, which must write (proving the arguments really do reach the store, so
// the refusal below is not just an argument-validation error wearing a
// disguise), and once with a caller whose worker could not be established, which
// must refuse and write nothing at all.
func TestNoMCPConfigEventCanCarryAnEmptyActor(t *testing.T) {
	unidentified := mcpCaller{Project: "acme", SessionID: "sess-1"} // no Worker

	for _, tc := range managementMutations() {
		t.Run(tc.tool, func(t *testing.T) {
			// 1. The control: the same call from a real worker writes.
			store, tools := seedForMutations(t)
			if _, err := invokeTool(t, tools, tc.tool, testCaller(), tc.args); err != nil {
				t.Fatalf("control call failed, so this case proves nothing about the refusal: %v", err)
			}
			if countConfigWrites(store) == 0 {
				t.Fatalf("control call wrote no config event — %s is not a mutation, or the args never reached the store", tc.tool)
			}

			// 2. The invariant: the same call from an unidentified caller.
			store, tools = seedForMutations(t)
			if _, err := invokeTool(t, tools, tc.tool, unidentified, tc.args); err == nil {
				t.Fatalf("%s accepted a caller with no worker — its config event would read as a human edit", tc.tool)
			}
			for method, ws := range store.writes {
				for _, w := range ws {
					t.Fatalf("%s wrote through %s with actor %q: an empty actor is reserved for humans (§15.2)",
						tc.tool, method, w.Worker)
				}
			}
		})
	}
}

// TestManagementMutationsAreAllCovered fails when a mutation is added to the
// management tools without being added to managementMutations() — the same
// spirit as agentdb's TestMutationsAreLogged, which fails if any store method
// can bypass the config log. A new tool that writes must be listed, or the
// invariant above silently stops covering the whole surface.
func TestManagementMutationsAreAllCovered(t *testing.T) {
	listed := map[string]bool{}
	for _, tc := range managementMutations() {
		listed[tc.tool] = true
	}
	_, tools := seedForMutations(t)
	for _, tool := range tools {
		if listed[tool.Name] {
			continue
		}
		// Not listed: prove it writes nothing, so its absence is safe. An
		// unidentified caller is used deliberately — if the tool DID mutate, the
		// invariant guard would refuse it, and a refusal is not proof of a read.
		// So the control caller is used instead.
		store, freshTools := seedForMutations(t)
		_, _ = invokeTool(t, freshTools, tool.Name, testCaller(), minimalArgsFor(tool.Name))
		if n := countConfigWrites(store); n > 0 {
			t.Fatalf("tool %q writes %d config event(s) but is not covered by managementMutations() — "+
				"add it there so RD4's invariant keeps covering the whole surface", tool.Name, n)
		}
	}
}

// minimalArgsFor gives the read/report tools enough to run. Anything missing
// just makes the tool error, which is fine: the assertion is only that it wrote
// nothing.
func minimalArgsFor(tool string) map[string]any {
	switch tool {
	case "worker_prompt_read":
		return map[string]any{"name": "email-answerer"}
	case "request_human_attention":
		return map[string]any{"message": "I need a decision"}
	default:
		return map[string]any{}
	}
}

func countConfigWrites(store *fakeManagementStore) int {
	n := 0
	for _, ws := range store.writes {
		n += len(ws)
	}
	return n
}

// ── The same invariant on the image and skill tools ─────────────────────────

// TestSkillCreateRefusesAnUnidentifiedCaller: skill_create writes a
// `skill_create` config event with the caller as actor, in the same transaction
// as the row (§15.4).
func TestSkillCreateRefusesAnUnidentifiedCaller(t *testing.T) {
	store := newFakeSkillStore()
	tools := testSkillTools(store, nil).tools()
	args := map[string]any{"name": "render-video", "markdown": "# How to render"}

	// Control.
	if _, err := invokeTool(t, tools, "skill_create", testCaller(), args); err != nil {
		t.Fatalf("control call failed, so the refusal below proves nothing: %v", err)
	}
	if len(store.writes) == 0 {
		t.Fatalf("control call wrote no config event")
	}

	store2 := newFakeSkillStore()
	tools2 := testSkillTools(store2, nil).tools()
	if _, err := invokeTool(t, tools2, "skill_create", mcpCaller{Project: "acme", SessionID: "sess-1"}, args); err == nil {
		t.Fatalf("skill_create accepted a caller with no worker")
	}
	if len(store2.writes) != 0 || len(store2.created) != 0 {
		t.Fatalf("an unidentified skill_create wrote anyway: writes=%+v created=%d", store2.writes, len(store2.created))
	}
}

// TestImageCreateRefusesAnUnidentifiedCallerBeforeSnapshotting: image_create
// commits a container before it records anything, so the actor is resolved with
// the rest of the validation — up front, where a refusal costs no bytes (§9).
func TestImageCreateRefusesAnUnidentifiedCallerBeforeSnapshotting(t *testing.T) {
	store := newFakeImageCatalog()
	snaps := &fakeSnapshotter{}
	tools := testImageTools(store, snaps).tools()

	_, err := invokeTool(t, tools, "image_create",
		mcpCaller{Project: "acme", SessionID: "sess-1"}, map[string]any{"name": "toolbox"})
	if err == nil {
		t.Fatalf("image_create accepted a caller with no worker")
	}
	if len(snaps.refs) != 0 {
		t.Fatalf("the refusal must land before the snapshot, not after %d of them", len(snaps.refs))
	}
	if len(store.writes) != 0 || len(store.created) != 0 {
		t.Fatalf("an unidentified image_create wrote anyway")
	}
}
