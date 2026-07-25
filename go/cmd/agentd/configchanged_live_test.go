package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// J3 against a real Postgres.
//
// The unit tests prove the emitter's POLICY against fakes. This proves the
// WIRING — that the hook really is called after the commit and only after it,
// which is a property of agentdb's transaction, not of any code in this
// package:
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./cmd/agentd/ -run TestConfigChangedEvent
//
// The mutations here go through WithConfigEvent directly rather than through a
// projection-writing method on purpose: the emission contract begins at the
// committed record, and testing it this way keeps this file independent of
// every other track's table shapes.
// ---------------------------------------------------------------------------

func liveConfigProject(t *testing.T, s *agentdb.Store) string {
	t.Helper()
	project := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		_ = s.DB().Exec("DELETE FROM config_events WHERE project = ?", project).Error
		_ = s.DB().Exec("DELETE FROM project_events WHERE project = ?", project).Error
	})
	return project
}

// TestConfigChangedEventRollbackEmitsNothing is the first required case: a
// mutation whose transaction rolls back leaves no log row and — the part that
// matters — announces nothing. A routed event for a change that did not happen
// would send workers off to react to fiction.
func TestConfigChangedEventRollbackEmitsNothing(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project := liveConfigProject(t, store)

	emitter := newConfigChangeEmitter(store, func(string, ...any) {})
	hookCalls := 0
	store.SetConfigEventHook(func(c context.Context, ev *agentdb.ConfigEvent) {
		hookCalls++
		emitter.Hook()(c, ev)
	})
	t.Cleanup(func() { store.SetConfigEventHook(nil) })

	boom := errors.New("the projection write failed")
	_, err := store.WithConfigEvent(ctx, agentdb.ConfigChange{
		Project: project,
		Action:  agentdb.ActionWorkerCreate,
		Payload: map[string]any{"name": "never-hired"},
		Write:   agentdb.ConfigWrite{Worker: "manager", Session: "s-x"},
	}, func(tx *gorm.DB) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("want the mutation's own error back, got %v", err)
	}

	if hookCalls != 0 {
		t.Fatalf("the post-commit hook ran for a rolled-back transaction (%d times)", hookCalls)
	}
	evs, err := store.ListConfigEvents(ctx, agentdb.ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("a rolled-back mutation left %d config event(s)", len(evs))
	}
	var events int64
	if err := store.DB().Raw("SELECT count(*) FROM project_events WHERE project = ?", project).Scan(&events).Error; err != nil {
		t.Fatalf("count project events: %v", err)
	}
	if events != 0 {
		t.Fatalf("a rolled-back mutation emitted %d event(s)", events)
	}
}

// TestConfigChangedEventLandsAfterCommit is the same seam from the other side:
// a committed mutation produces exactly one event, stamped with the acting
// session's envelope, and re-running the emitter (the crash-retry path) still
// leaves exactly one.
func TestConfigChangedEventLandsAfterCommit(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project := liveConfigProject(t, store)

	sess := liveSession(t, store, project, "marketing-manager")
	emitter := newConfigChangeEmitter(store, t.Logf)
	store.SetConfigEventHook(emitter.Hook())
	t.Cleanup(func() { store.SetConfigEventHook(nil) })

	ce, err := store.WithConfigEvent(ctx, agentdb.ConfigChange{
		Project: project,
		Action:  agentdb.ActionWorkerPromptWrite,
		Payload: map[string]any{"name": "tweet-author", "system_prompt": "be brief"},
		Write: agentdb.ConfigWrite{
			Worker: "marketing-manager", Session: sess.ID,
			Rationale: "three customers called the answers walls of text",
		},
	}, func(tx *gorm.DB) error { return nil })
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}

	emitted, err := store.GetProjectEvent(ctx, project, configChangedEventID(ce.ID))
	if err != nil {
		t.Fatalf("the committed change was not announced: %v", err)
	}
	if emitted.Type != agentdb.EventTypeConfigChanged {
		t.Fatalf("type: %q", emitted.Type)
	}
	if emitted.Envelope.Source != agentdb.EventSourceWorker ||
		emitted.Envelope.Worker != "marketing-manager" ||
		emitted.Envelope.SessionID != sess.ID {
		t.Fatalf("envelope does not name the acting session: %+v", emitted.Envelope)
	}
	if emitted.Delivered {
		t.Fatal("the emitter marked its own event delivered — routing is the router's job")
	}

	// The watermark is stamped, so the sweep does not consider it pending.
	pending, err := store.ListUnemittedConfigEvents(ctx, 0, 100)
	if err != nil {
		t.Fatalf("list unemitted: %v", err)
	}
	for _, p := range pending {
		if p.ID == ce.ID {
			t.Fatal("an announced change is still queued for repair")
		}
	}

	// Crash-retry: the sweep re-emits a record it believes unannounced.
	if err := emitter.Emit(ctx, ce); err != nil {
		t.Fatalf("re-emit: %v", err)
	}
	var n int64
	if err := store.DB().Raw(
		"SELECT count(*) FROM project_events WHERE project = ? AND type = ?",
		project, agentdb.EventTypeConfigChanged).Scan(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("a retry double-emitted: %d events", n)
	}
}

// TestConfigChangedEventSweepRepairsALostEmission proves the at-least-once half
// end to end: a mutation committed while the emitter was absent (a crash
// between commit and append) is announced by the sweep afterwards.
func TestConfigChangedEventSweepRepairsALostEmission(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project := liveConfigProject(t, store)

	// No hook installed: this is the crash window.
	ce, err := store.WithConfigEvent(ctx, agentdb.ConfigChange{
		Project: project,
		Action:  agentdb.ActionScheduleCreate,
		Payload: map[string]any{"id": "sch-live", "cron": "0 9 * * 1-5", "worker": "tweet-author"},
		Write:   agentdb.ConfigWrite{},
	}, func(tx *gorm.DB) error { return nil })
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if _, err := store.GetProjectEvent(ctx, project, configChangedEventID(ce.ID)); !errors.Is(err, agentdb.ErrProjectEventNotFound) {
		t.Fatalf("expected no event yet, got %v", err)
	}

	emitter := newConfigChangeEmitter(store, t.Logf)
	if err := emitter.Emit(ctx, ce); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if _, err := store.GetProjectEvent(ctx, project, configChangedEventID(ce.ID)); err != nil {
		t.Fatalf("the sweep did not repair the lost emission: %v", err)
	}
}

// TestConfigHistoryLiveRoundTrip drives the tool over the MCP transport against
// a real store: the entity filter keys off a jsonb payload (the unit tests only
// prove that on sqlite), and a second project's session must see none of it.
func TestConfigHistoryLiveRoundTrip(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project := liveConfigProject(t, store)
	intruder := liveConfigProject(t, store)

	sess := liveSession(t, store, project, "email-reviewer")
	other := liveSession(t, store, intruder, "nosy-worker")

	write := func(p, action string, payload map[string]any, cw agentdb.ConfigWrite) {
		t.Helper()
		if _, err := store.WithConfigEvent(ctx, agentdb.ConfigChange{
			Project: p, Action: action, Payload: payload, Write: cw,
		}, func(tx *gorm.DB) error { return nil }); err != nil {
			t.Fatalf("write %s: %v", action, err)
		}
	}
	actor := agentdb.ConfigWrite{Worker: "email-reviewer", Session: sess.ID}
	write(project, agentdb.ActionWorkerCreate, map[string]any{"name": "email-answerer"}, actor)
	write(project, agentdb.ActionWorkerPromptWrite,
		map[string]any{"name": "email-answerer", "system_prompt": "be brief"},
		agentdb.ConfigWrite{Worker: "email-reviewer", Session: sess.ID, Rationale: "walls of text"})
	write(project, agentdb.ActionWorkerCreate, map[string]any{"name": "tweet-author"}, actor)
	write(intruder, agentdb.ActionWorkerCreate, map[string]any{"name": "email-answerer"},
		agentdb.ConfigWrite{Worker: "nosy-worker", Session: other.ID})

	secret := []byte("live-test-secret")
	srv := newMCPServer(coreMCPServerName, newSessionTokenAuth(secret, store).authenticate)
	srv.register(newConfigLogTools(store, permalinker{base: "https://ui.example"}).tools()...)

	token := mintSessionToken(t, secret, time.Hour, project, sess.ID)
	otherToken := mintSessionToken(t, secret, time.Hour, intruder, other.ID)

	callTool := liveToolCaller(t, srv)
	call := func(tok string, args map[string]any) map[string]any {
		t.Helper()
		return callTool(tok, "config_history", args)
	}

	one := call(token, map[string]any{"entity": "worker:email-answerer"})
	records, _ := one["records"].([]any)
	if len(records) != 2 {
		t.Fatalf("entity filter over jsonb returned %d records, want 2: %v", len(records), one)
	}
	first, _ := records[0].(map[string]any)
	if first["action"] != agentdb.ActionWorkerPromptWrite {
		t.Fatalf("newest first: got %v", first["action"])
	}
	if first["session_url"] != "https://ui.example/p/"+project+"/s/"+sess.ID {
		t.Fatalf("session_url: %v", first["session_url"])
	}

	// The intruder's own history exists but names only its own row.
	theirs := call(otherToken, map[string]any{"entity": "worker:email-answerer"})
	theirRecords, _ := theirs["records"].([]any)
	if len(theirRecords) != 1 {
		t.Fatalf("project isolation: the other project saw %d records", len(theirRecords))
	}
	if r, _ := theirRecords[0].(map[string]any); r["actor_worker"] != "nosy-worker" {
		t.Fatalf("project isolation: a foreign record leaked: %v", r)
	}
}
