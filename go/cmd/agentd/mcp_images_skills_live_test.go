package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// I2 + I3 against a real Postgres.
//
// The unit tests prove the tools' POLICY against fakes. These prove the wiring:
// the §13 image selector is jsonb containment and the §14 catalogue is a
// partial-index affair, so only Postgres can show that a session calling over
// the MCP transport really gets its own project's catalogue and nobody else's.
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./cmd/agentd/ -run 'TestImageTools|TestSkills'
//
// The container half is deliberately faked: burning an image needs a running
// DinD instance and installing a skill needs a sandbox, neither of which
// belongs in a store test. What is real here is everything between the token
// and the tables.
// ---------------------------------------------------------------------------

// liveSnapshotter stands in for the Runner's Snapshot: a handle, no container.
type liveSnapshotter struct{ refs []agentkit.SessionRef }

func (l *liveSnapshotter) Snapshot(_ context.Context, ref agentkit.SessionRef) (imageregistry.Handle, error) {
	l.refs = append(l.refs, ref)
	return imageregistry.Handle{Kind: "blob-archive", Ref: "blob/" + ref.SessionID}, nil
}

// TestImageToolsLiveRoundTrip drives image_create → image_list over the HTTP
// transport against a real store, and proves a second project sees none of it.
func TestImageToolsLiveRoundTrip(t *testing.T) {
	store := openLiveStore(t)
	project := "proj-" + uuid.New().String()
	intruder := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		store.DB().Exec("DELETE FROM agent_custom_images WHERE customer IN (?, ?)", project, intruder)
		_ = store.PurgeConfigEvents(context.Background(), project)
		_ = store.PurgeConfigEvents(context.Background(), intruder)
	})

	sess := liveSession(t, store, project, "curator")
	other := liveSession(t, store, intruder, "nosy-worker")

	secret := []byte("live-test-secret")
	srv := newMCPServer(coreMCPServerName, newSessionTokenAuth(secret, store).authenticate)
	snaps := &liveSnapshotter{}
	srv.register(newImageTools(store, snaps, permalinker{base: "https://ui.example"}).tools()...)

	token := mintSessionToken(t, secret, time.Hour, project, sess.ID)
	otherToken := mintSessionToken(t, secret, time.Hour, intruder, other.ID)
	call := liveToolCaller(t, srv)

	// Burn twice under one name, and once under another.
	first := call(token, "image_create", map[string]any{
		"name": "toolbox", "labels": map[string]string{"purpose": "marketing", "adds": "ffmpeg"},
	})
	if first["version"] != float64(1) {
		t.Fatalf("first burn must be version 1: %#v", first)
	}
	second := call(token, "image_create", map[string]any{
		"name": "toolbox", "labels": map[string]string{"purpose": "marketing", "adds": "imagemagick"},
	})
	if second["version"] != float64(2) {
		t.Fatalf("a second burn of one name must allocate version 2: %#v", second)
	}
	call(token, "image_create", map[string]any{
		"name": "vanilla", "labels": map[string]string{"purpose": "baseline"},
	})

	// Every burn snapshotted the CALLING session, never anything else.
	for _, ref := range snaps.refs {
		if ref.SessionID != sess.ID {
			t.Fatalf("a session may only snapshot itself, got %q", ref.SessionID)
		}
	}
	if first["session_url"] != "https://ui.example/p/"+project+"/s/"+sess.ID {
		t.Fatalf("session_url = %v", first["session_url"])
	}

	// image_list, newest first, over the real catalogue.
	all := call(token, "image_list", map[string]any{})
	rows, _ := all["images"].([]any)
	if len(rows) != 3 {
		t.Fatalf("want the project's 3 versions, got %d: %#v", len(rows), all)
	}
	// Within one name the newer version leads. Across names inside one second
	// the order is I1's `created_at DESC, version DESC` tiebreak, which is not
	// a recency statement — so this asserts only what is actually true (see the
	// I2 entry in the Discovered Issues Log).
	var seen []string
	for _, r := range rows {
		row, _ := r.(map[string]any)
		seen = append(seen, fmt.Sprintf("%v:%v", row["name"], row["version"]))
	}
	toolbox2, toolbox1 := indexOf(seen, "toolbox:2"), indexOf(seen, "toolbox:1")
	if toolbox2 < 0 || toolbox1 < 0 || indexOf(seen, "vanilla:1") < 0 {
		t.Fatalf("the catalogue must list every burn: %v", seen)
	}
	if toolbox2 > toolbox1 {
		t.Fatalf("within a name the newer version must lead: %v", seen)
	}

	// The selector leg is jsonb containment — the thing sqlite cannot prove.
	filtered := call(token, "image_list", map[string]any{"label_selector": "purpose=marketing,adds=imagemagick"})
	rows, _ = filtered["images"].([]any)
	if len(rows) != 1 {
		t.Fatalf("selector must filter to the one matching version, got %d", len(rows))
	}
	if row, _ := rows[0].(map[string]any); row["version"] != float64(2) {
		t.Fatalf("selector matched the wrong version: %#v", rows[0])
	}

	// Every burn appended exactly one image_create config event, with the
	// burning worker and session as the actor (§13.4, §15.2).
	evs, err := store.ListConfigEvents(context.Background(), agentdb.ConfigEventQuery{
		Project: project, Action: agentdb.ActionImageCreate,
	})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("want one config event per burn, got %d", len(evs))
	}
	for _, ev := range evs {
		if ev.ActorWorker != "curator" || ev.ActorSession != sess.ID {
			t.Fatalf("config event actor = %q/%q, want the burning session", ev.ActorWorker, ev.ActorSession)
		}
	}

	// Project isolation, from the other side of the boundary.
	theirs := call(otherToken, "image_list", map[string]any{})
	if rows, _ = theirs["images"].([]any); len(rows) != 0 {
		t.Fatalf("another project's session saw %d images", len(rows))
	}
}

// TestSkillsLiveRoundTrip drives skill_create → list → get over the HTTP
// transport against a real store: append-only revisions, newest-wins reads,
// no markdown in the listing, and hard project isolation.
func TestSkillsLiveRoundTrip(t *testing.T) {
	store := openLiveStore(t)
	project := "proj-" + uuid.New().String()
	intruder := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		store.DB().Exec("DELETE FROM agent_skills WHERE customer IN (?, ?)", project, intruder)
		_ = store.PurgeConfigEvents(context.Background(), project)
		_ = store.PurgeConfigEvents(context.Background(), intruder)
	})

	sess := liveSession(t, store, project, "curator")
	other := liveSession(t, store, intruder, "nosy-worker")

	secret := []byte("live-test-secret")
	srv := newMCPServer(coreMCPServerName, newSessionTokenAuth(secret, store).authenticate)
	srv.register(newSkillTools(store, nil, permalinker{base: "https://ui.example"}).tools()...)

	token := mintSessionToken(t, secret, time.Hour, project, sess.ID)
	otherToken := mintSessionToken(t, secret, time.Hour, intruder, other.ID)
	call := liveToolCaller(t, srv)

	call(token, "skill_create", map[string]any{
		"name": "render-social-video", "markdown": "# v1", "install_sh": "apt-get install -y ffmpeg",
		"labels": map[string]string{"kind": "media", "status": "experimental"},
	})
	second := call(token, "skill_create", map[string]any{
		"name": "render-social-video", "markdown": "# v2 — the good one",
		"labels": map[string]string{"kind": "media"},
	})
	if second["revision"] != float64(2) {
		t.Fatalf("an existing name must append a revision: %#v", second)
	}
	call(token, "skill_create", map[string]any{
		"name": "write-brief", "markdown": "# brief", "labels": map[string]string{"kind": "writing"},
	})

	// skill_get: newest-wins, in full.
	got := call(token, "skill_get", map[string]any{"name": "render-social-video"})
	if got["markdown"] != "# v2 — the good one" {
		t.Fatalf("skill_get must return the newest revision: %#v", got)
	}
	if got["install_sh"] != "" {
		t.Fatalf("the newest revision carries no script; get must not fold in the old one: %#v", got)
	}
	if got["session_url"] != "https://ui.example/p/"+project+"/s/"+sess.ID {
		t.Fatalf("session_url = %v", got["session_url"])
	}

	// skill_list: one per name, no markdown.
	all := call(token, "skill_list", map[string]any{})
	rows, _ := all["skills"].([]any)
	if len(rows) != 2 {
		t.Fatalf("want one entry per name (2), got %d: %#v", len(rows), all)
	}
	for _, r := range rows {
		entry, _ := r.(map[string]any)
		if _, leaked := entry["markdown"]; leaked {
			t.Fatalf("skill_list must not carry markdown: %#v", entry)
		}
	}

	// A label the newer revision dropped must not resurface the older one.
	stale := call(token, "skill_list", map[string]any{"label_selector": "status=experimental"})
	if rows, _ = stale["skills"].([]any); len(rows) != 0 {
		t.Fatalf("a label dropped by a newer teaching is gone, got %#v", stale)
	}
	media := call(token, "skill_list", map[string]any{"label_selector": "kind=media"})
	if rows, _ = media["skills"].([]any); len(rows) != 1 {
		t.Fatalf("selector on the current revision: %#v", media)
	}

	// One config event per create, actor = the recording session (§14.2, §15.2).
	evs, err := store.ListConfigEvents(context.Background(), agentdb.ConfigEventQuery{
		Project: project, Action: agentdb.ActionSkillCreate,
	})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("want one config event per skill_create, got %d", len(evs))
	}

	// Project isolation, from the other side of the boundary.
	theirs := call(otherToken, "skill_list", map[string]any{})
	if rows, _ = theirs["skills"].([]any); len(rows) != 0 {
		t.Fatalf("another project's session saw %d skills", len(rows))
	}
	_, res := rpc(t, srv, otherToken, "tools/call", map[string]any{
		"name": "skill_get", "arguments": map[string]any{"name": "render-social-video"},
	})
	result, _ := res["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("another project's skill must be NOT FOUND, got %#v", result)
	}
	if !strings.Contains(fmt.Sprint(result["content"]), "no skill named") {
		t.Fatalf("cross-project read must look like not-found, got %#v", result["content"])
	}
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

// liveToolCaller returns a helper that calls one tool over the JSON-RPC
// transport and fails the test on a tool error.
func liveToolCaller(t *testing.T, srv *mcpServer) func(tok, name string, args map[string]any) map[string]any {
	t.Helper()
	return func(tok, name string, args map[string]any) map[string]any {
		t.Helper()
		_, res := rpc(t, srv, tok, "tools/call", map[string]any{"name": name, "arguments": args})
		result, _ := res["result"].(map[string]any)
		if result == nil {
			t.Fatalf("%s: no result in %#v", name, res)
		}
		if result["isError"] == true {
			t.Fatalf("%s failed: %v", name, result["content"])
		}
		structured, _ := result["structuredContent"].(map[string]any)
		if structured == nil {
			t.Fatalf("%s: no structuredContent in %#v", name, result)
		}
		return structured
	}
}
