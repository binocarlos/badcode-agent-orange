package agentdb

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"gorm.io/gorm"
)

// The fold (§15.6) and the property that makes restore safe (§15.7).
//
// These run on the shared config-log test store (config_events_test.go), which
// migrates the log alongside every projection table a registered mutation
// writes — so a fold can be compared against the tables it claims to reproduce.

// ── Entity keying ───────────────────────────────────────────────────────────

func TestConfigFold_EntityRefForKeysEveryAction(t *testing.T) {
	tests := []struct {
		name    string
		ev      *ConfigEvent
		want    string
		wantErr bool
	}{
		{
			name: "worker actions key on the name",
			ev:   &ConfigEvent{ID: "e1", Action: ActionWorkerUpdate, Payload: JSONMap{"name": "email-answerer"}},
			want: "worker:email-answerer",
		},
		{
			name: "a prompt write is the SAME key as any other worker write",
			ev:   &ConfigEvent{ID: "e2", Action: ActionWorkerPromptWrite, Payload: JSONMap{"name": "email-answerer"}},
			want: "worker:email-answerer",
		},
		{
			name: "worker_delete keys the same way — a tombstone must hit the key it removes",
			ev:   &ConfigEvent{ID: "e3", Action: ActionWorkerDelete, Payload: JSONMap{"name": "email-answerer"}},
			want: "worker:email-answerer",
		},
		{
			name: "subscriptions key on the generated id",
			ev:   &ConfigEvent{ID: "e4", Action: ActionSubscriptionUpdate, Payload: JSONMap{"id": "sub-7"}},
			want: "subscription:sub-7",
		},
		{
			name: "schedules key on the generated id",
			ev:   &ConfigEvent{ID: "e5", Action: ActionScheduleCreate, Payload: JSONMap{"id": "sch-2"}},
			want: "schedule:sch-2",
		},
		{
			name: "images key on name:version — every version is its own entity (§13.2)",
			ev:   &ConfigEvent{ID: "e6", Action: ActionImageCreate, Payload: JSONMap{"name": "toolbox", "version": float64(3)}},
			want: "image:toolbox:3",
		},
		{
			name: "skills key on the name — newest revision wins (§14)",
			ev:   &ConfigEvent{ID: "e7", Action: ActionSkillCreate, Payload: JSONMap{"name": "graph-gen"}},
			want: "skill:graph-gen",
		},
		{
			name: "project settings are a singleton",
			ev:   &ConfigEvent{ID: "e8", Action: ActionProjectSettingsPut, Payload: JSONMap{"project": "acme"}},
			want: "project-settings",
		},
		{
			name: "the project prompt is a singleton, distinct from the settings row",
			ev:   &ConfigEvent{ID: "e9", Action: ActionProjectPromptWrite, Payload: JSONMap{"system_prompt": "Be brief."}},
			want: "project-prompt",
		},
		{
			name:    "an action outside the closed vocabulary fails loudly",
			ev:      &ConfigEvent{ID: "e10", Action: "worker_teleport", Payload: JSONMap{"name": "x"}},
			wantErr: true,
		},
		{
			name:    "a payload missing its own identity is a corrupt record",
			ev:      &ConfigEvent{ID: "e11", Action: ActionWorkerUpdate, Payload: JSONMap{"description": "no name here"}},
			wantErr: true,
		},
		{
			name:    "an image payload without a version cannot be keyed",
			ev:      &ConfigEvent{ID: "e12", Action: ActionImageCreate, Payload: JSONMap{"name": "toolbox"}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := EntityRefFor(tc.ev)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got ref %s", ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("EntityRefFor: %v", err)
			}
			if ref.String() != tc.want {
				t.Fatalf("want %q, got %q", tc.want, ref.String())
			}
		})
	}
}

// Every action in the closed §15.3 vocabulary must be foldable. If a track adds
// a verb without teaching the fold about it, replay silently loses an entity
// kind — so this test is the tripwire.
func TestConfigFold_EveryActionIsFoldable(t *testing.T) {
	for _, a := range ConfigActions {
		if _, ok := entityKindForAction[a]; !ok {
			t.Fatalf("action %q is in the §15.3 vocabulary but the fold cannot key it (§15.6)", a)
		}
	}
}

// ── The fold reproduces the tables (§15.6) ──────────────────────────────────

// workerFields projects the parts of a worker row a fold must reproduce.
// created_at/updated_at are deliberately excluded: see the payload-timestamp
// note on TestConfigFold_PayloadTimestampsAreNotAuthoritative.
func workerFields(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"project", "name", "description", "system_prompt", "image", "max_instances", "enabled"} {
		out[k] = m[k]
	}
	return out
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// TestConfigFold_ToInstantReproducesTheTables is the §15.6 claim itself: fold to
// T and you get the projection tables as they stood at T, not as they stand
// now.
func TestConfigFold_ToInstantReproducesTheTables(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()
	const project = "acme"

	// -- The organisation as it was on "Tuesday" --------------------------------
	answerer := NewWorker(project, "email-answerer")
	answerer.SystemPrompt = "Answer customer email."
	if _, err := s.UpsertWorker(ctx, answerer, ConfigWrite{Worker: "manager", Session: "s-1"}); err != nil {
		t.Fatalf("create answerer: %v", err)
	}
	reviewer := NewWorker(project, "email-reviewer")
	if _, err := s.UpsertWorker(ctx, reviewer, ConfigWrite{}); err != nil {
		t.Fatalf("create reviewer: %v", err)
	}
	sub, err := s.CreateSubscription(ctx, &Subscription{
		Project: project, EventType: "email.received", Worker: "email-answerer", Enabled: true,
	}, ConfigWrite{})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{Project: project, BaseImage: "vanilla"}, ConfigWrite{}); err != nil {
		t.Fatalf("put settings: %v", err)
	}

	// Snapshot the live tables, then take T. Nothing after this point may change
	// what a fold to T returns.
	tuesdayWorkers := map[string]map[string]any{}
	live, err := s.ListWorkers(ctx, project)
	if err != nil {
		t.Fatalf("list workers: %v", err)
	}
	for _, w := range live {
		tuesdayWorkers[w.Name] = workerFields(asMap(t, w))
	}
	time.Sleep(2 * time.Millisecond)
	tuesday := time.Now().UnixMilli()
	time.Sleep(2 * time.Millisecond)

	// -- What happened after Tuesday -------------------------------------------
	answerer.SystemPrompt = "Answer customer email at length, with flourishes."
	if _, err := s.UpsertWorker(ctx, answerer, ConfigWrite{Worker: "manager", Session: "s-2"}); err != nil {
		t.Fatalf("rewrite answerer: %v", err)
	}
	hired := NewWorker(project, "tweet-author")
	if _, err := s.UpsertWorker(ctx, hired, ConfigWrite{}); err != nil {
		t.Fatalf("hire tweet-author: %v", err)
	}
	if err := s.DeleteWorker(ctx, project, "email-reviewer", ConfigWrite{}); err != nil {
		t.Fatalf("delete reviewer: %v", err)
	}
	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{Project: project, BaseImage: "toolbox"}, ConfigWrite{}); err != nil {
		t.Fatalf("second settings put: %v", err)
	}

	// -- Fold back to Tuesday ---------------------------------------------------
	snap, err := s.FoldTo(ctx, project, tuesday)
	if err != nil {
		t.Fatalf("FoldTo: %v", err)
	}

	gotWorkers := map[string]map[string]any{}
	for _, e := range snap.OfKind(EntityWorker) {
		gotWorkers[e.Ref.Key] = workerFields(e.Payload())
	}
	if !reflect.DeepEqual(gotWorkers, tuesdayWorkers) {
		t.Fatalf("fold does not reproduce the workers table as it stood at T\nwant %v\ngot  %v", tuesdayWorkers, gotWorkers)
	}
	// Specifically: the worker hired later is absent, the one deleted later is
	// present, and the prompt is the pre-rewrite one.
	if _, ok := snap.Worker("tweet-author"); ok {
		t.Fatalf("fold to T contains a worker hired after T")
	}
	if _, ok := snap.Worker("email-reviewer"); !ok {
		t.Fatalf("fold to T lost a worker that was deleted after T — the delete must not apply before it happened")
	}
	ans, ok := snap.Worker("email-answerer")
	if !ok {
		t.Fatalf("fold to T lost the answerer")
	}
	if got, _ := ans.Event.PayloadString("system_prompt"); got != "Answer customer email." {
		t.Fatalf("fold returned the LATER prompt: %q", got)
	}

	// Other kinds fold too, on their own keys.
	subs := snap.OfKind(EntitySubscription)
	if len(subs) != 1 || subs[0].Ref.Key != sub.ID {
		t.Fatalf("subscription did not fold on its id: %v", subs)
	}
	settings, ok := snap.Get(EntityRef{Kind: EntityProjectSettings})
	if !ok {
		t.Fatalf("project settings missing from the fold")
	}
	if got, _ := settings.Event.PayloadString("base_image"); got != "vanilla" {
		t.Fatalf("settings singleton folded to the later value: %q", got)
	}

	// A fold with no instant is "now": the tables as they stand.
	now, err := s.FoldTo(ctx, project, 0)
	if err != nil {
		t.Fatalf("FoldTo(now): %v", err)
	}
	if _, ok := now.Worker("tweet-author"); !ok {
		t.Fatalf("fold to now is missing the worker hired after T")
	}
	if _, ok := now.Worker("email-reviewer"); ok {
		t.Fatalf("fold to now still has the deleted worker")
	}
}

// TestConfigFold_DeleteThenRecreate covers the case the work plan names
// explicitly: a tombstone removes the key, and an ordinary create afterwards
// brings it back — with the log keeping all three records.
func TestConfigFold_DeleteThenRecreate(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()
	const project = "acme"

	w := NewWorker(project, "tweet-author")
	w.SystemPrompt = "One post at a time."
	if _, err := s.UpsertWorker(ctx, w, ConfigWrite{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// `at` is INCLUSIVE, so an instant must be separated from the writes on both
	// sides of it: a write landing in the same millisecond as T is "at T".
	time.Sleep(2 * time.Millisecond)
	afterCreate := time.Now().UnixMilli()
	time.Sleep(2 * time.Millisecond)

	if err := s.DeleteWorker(ctx, project, "tweet-author", ConfigWrite{Rationale: "experiment over"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	afterDelete := time.Now().UnixMilli()
	time.Sleep(2 * time.Millisecond)

	// §15.7: restoring a deleted entity is an ordinary create carrying the
	// payload from its delete event — not a resurrection verb.
	gone, ok := mustFold(t, s, project, afterDelete).WasDeleted(EntityRef{Kind: EntityWorker, Key: "tweet-author"})
	if !ok {
		t.Fatalf("the delete left no tombstone to restore from")
	}
	prompt, _ := gone.PayloadString("system_prompt")
	again := NewWorker(project, "tweet-author")
	again.SystemPrompt = prompt
	if _, err := s.UpsertWorker(ctx, again, ConfigWrite{Rationale: "restore from " + gone.ID}); err != nil {
		t.Fatalf("re-create: %v", err)
	}

	// Three folds, three different answers, one unmodified log.
	for _, tc := range []struct {
		name      string
		at        int64
		wantAlive bool
	}{
		{"after the create it is there", afterCreate, true},
		{"after the delete it is gone", afterDelete, false},
		{"after the re-create it is back", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := mustFold(t, s, project, tc.at)
			e, alive := snap.Worker("tweet-author")
			if alive != tc.wantAlive {
				t.Fatalf("alive = %v, want %v", alive, tc.wantAlive)
			}
			if !alive {
				if _, tomb := snap.WasDeleted(EntityRef{Kind: EntityWorker, Key: "tweet-author"}); !tomb {
					t.Fatalf("a removed key must leave a tombstone carrying its final state (§15.3 rule 2)")
				}
				return
			}
			if _, tomb := snap.WasDeleted(EntityRef{Kind: EntityWorker, Key: "tweet-author"}); tomb {
				t.Fatalf("a re-created key must not still read as deleted")
			}
			if got, _ := e.Event.PayloadString("system_prompt"); got != "One post at a time." {
				t.Fatalf("prompt: %q", got)
			}
		})
	}

	// The log itself never shrank: create, delete, create.
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var actions []string
	for i := len(evs) - 1; i >= 0; i-- {
		actions = append(actions, evs[i].Action)
	}
	want := []string{ActionWorkerCreate, ActionWorkerDelete, ActionWorkerCreate}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("log order: want %v, got %v", want, actions)
	}
}

func mustFold(t *testing.T, s *Store, project string, at int64) *ConfigSnapshot {
	t.Helper()
	snap, err := s.FoldTo(context.Background(), project, at)
	if err != nil {
		t.Fatalf("FoldTo(%d): %v", at, err)
	}
	return snap
}

// ── Ordering ────────────────────────────────────────────────────────────────

// The reason `seq` exists. created_at is milliseconds and id is a random uuid,
// so writes that share a millisecond have no order at all under §15.6's literal
// "created_at/id" rule — and a fold that picks the wrong one contradicts the
// projection table it is supposed to reproduce.
func TestConfigFold_SameMillisecondWritesFoldInCommitOrder(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()
	const project = "acme"

	w := NewWorker(project, "email-answerer")
	if _, err := s.UpsertWorker(ctx, w, ConfigWrite{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A burst with no sleeps: several of these WILL share a millisecond.
	for i := 1; i <= 12; i++ {
		next, err := s.GetWorker(ctx, project, "email-answerer")
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		next.Description = fmt.Sprintf("revision %d", i)
		if _, err := s.UpsertWorker(ctx, next, ConfigWrite{}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != 13 {
		t.Fatalf("want 13 events, got %d", len(evs))
	}
	// The sequence is dense, unique and descending in a newest-first listing.
	seen := map[int64]bool{}
	for i, ev := range evs {
		if ev.Seq == 0 {
			t.Fatalf("event %s has no sequence number", ev.ID)
		}
		if seen[ev.Seq] {
			t.Fatalf("sequence %d issued twice", ev.Seq)
		}
		seen[ev.Seq] = true
		if want := int64(len(evs) - i); ev.Seq != want {
			t.Fatalf("event %d: seq %d, want %d (allocation must be gap-free and listing newest-first)", i, ev.Seq, want)
		}
	}
	// Proof the millisecond clock alone could not have ordered these.
	byMs := map[int64]int{}
	for _, ev := range evs {
		byMs[ev.CreatedAt]++
	}
	collided := false
	for _, n := range byMs {
		if n > 1 {
			collided = true
		}
	}
	if !collided {
		t.Logf("note: no two writes shared a millisecond on this machine; the seq invariants above still hold")
	}

	// And the fold agrees with the projection — the property that matters.
	snap := mustFold(t, s, project, 0)
	e, ok := snap.Worker("email-answerer")
	if !ok {
		t.Fatalf("worker missing from the fold")
	}
	got, _ := e.Event.PayloadString("description")
	if got != "revision 12" {
		t.Fatalf("fold disagrees with the projection table: folded %q, table has %q", got, "revision 12")
	}
	live, err := s.GetWorker(ctx, project, "email-answerer")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != live.Description {
		t.Fatalf("fold %q vs table %q", got, live.Description)
	}
}

// The payload is the full state (§15.2) but its timestamp fields are not: the
// seam marshals the payload before the transaction, so autoCreateTime /
// autoUpdateTime values land in the row and not in the record. Pinned so the
// changelog UI (J4) reads config_events.created_at rather than payload.updated_at.
func TestConfigFold_PayloadTimestampsAreNotAuthoritative(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertWorker(ctx, NewWorker("acme", "w1"), ConfigWrite{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	ev := newestConfigEvent(t, s, "acme", 1)
	if ev.CreatedAt == 0 {
		t.Fatalf("the RECORD must carry the time of the change")
	}
	if got := ev.Payload["updated_at"]; got != float64(0) {
		t.Fatalf("payload.updated_at is %v — if the seam ever starts marshalling after the write, "+
			"update this pin and tell J4 it may trust payload timestamps", got)
	}
}

// ── Project isolation (P5) ──────────────────────────────────────────────────

func TestConfigFold_IsProjectScoped(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertWorker(ctx, NewWorker("acme", "shared-name"), ConfigWrite{}); err != nil {
		t.Fatalf("acme: %v", err)
	}
	if _, err := s.UpsertWorker(ctx, NewWorker("globex", "shared-name"), ConfigWrite{}); err != nil {
		t.Fatalf("globex: %v", err)
	}

	acme := mustFold(t, s, "acme", 0)
	if acme.Folded != 1 {
		t.Fatalf("a fold must see only its own project's log, folded %d events", acme.Folded)
	}
	if _, err := s.FoldTo(ctx, "", 0); err == nil {
		t.Fatalf("FoldTo with no project must fail (P5: the namespace is never inferred)")
	}
	// Sequences are per project, so both projects start at 1.
	for _, p := range []string{"acme", "globex"} {
		evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: p})
		if err != nil {
			t.Fatalf("list %s: %v", p, err)
		}
		if len(evs) != 1 || evs[0].Seq != 1 {
			t.Fatalf("project %s: want one event at seq 1, got %d events", p, len(evs))
		}
	}
}

// A record the fold cannot key stops the fold rather than being skipped: a
// snapshot that quietly omits an entity is worse than no snapshot.
func TestConfigFold_UnknownActionStopsTheFold(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertWorker(ctx, NewWorker("acme", "w1"), ConfigWrite{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Written under the raw gorm handle: the seam itself would reject this.
	if err := s.gdb.Create(&ConfigEvent{
		ID: "bogus", Project: "acme", Seq: 99, Action: "worker_teleport",
		Payload: JSONMap{"name": "w1"}, CreatedAt: time.Now().UnixMilli(),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.FoldTo(ctx, "acme", 0); err == nil {
		t.Fatalf("a fold over an unknown action must fail loudly")
	}
}

// ── Restore is a forward operation (§15.7) ──────────────────────────────────

// TestRestoreIsForward is §15.7's worked example, executed. The marketing
// manager restores a worker's prompt to an earlier revision — and the whole
// point is what the log looks like afterwards: longer, never shorter.
func TestRestoreIsForward(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()
	const project = "acme"

	// 1. A prompt, a rewrite that regressed it, and some unrelated churn after.
	w := NewWorker(project, "email-answerer")
	w.SystemPrompt = "Answer the customer's question and stop."
	if _, err := s.UpsertWorker(ctx, w, ConfigWrite{Worker: "email-reviewer", Session: "s-991",
		Rationale: "shorten replies; three customers called the answers walls of text"}); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	good := newestConfigEvent(t, s, project, 1) // "ce_41" in the spec's example
	goodPrompt, _ := good.PayloadString("system_prompt")

	time.Sleep(2 * time.Millisecond)
	florid, err := s.GetWorker(ctx, project, "email-answerer")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	florid.SystemPrompt = "Open warmly, restate the question, then answer at length."
	if _, err := s.UpsertWorker(ctx, florid, ConfigWrite{Worker: "manager", Session: "s-1000",
		Rationale: "customers like a personal touch"}); err != nil {
		t.Fatalf("regression: %v", err)
	}
	if _, err := s.UpsertWorker(ctx, NewWorker(project, "tweet-author"), ConfigWrite{}); err != nil {
		t.Fatalf("unrelated change: %v", err)
	}

	before, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	// 2+3. The restore: an ORDINARY mutation carrying the old text, with a
	// rationale that names the event being restored. No new verb, no rewind.
	restoreRationale := fmt.Sprintf("restore to %s: the later rewrite regressed tone", good.ID)
	target, err := s.GetWorker(ctx, project, "email-answerer")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	target.SystemPrompt = goodPrompt
	if _, err := s.UpsertWorker(ctx, target, ConfigWrite{
		Worker: "marketing-manager", Session: "s-1043", Rationale: restoreRationale,
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	after, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list after: %v", err)
	}

	// 4/5. The properties, in the order §15.7 states them.
	t.Run("the restore added exactly one event and removed none", func(t *testing.T) {
		if len(after) != len(before)+1 {
			t.Fatalf("want %d events after the restore, got %d", len(before)+1, len(after))
		}
		haveAfter := map[string]*ConfigEvent{}
		for _, ev := range after {
			haveAfter[ev.ID] = ev
		}
		for _, ev := range before {
			survivor, ok := haveAfter[ev.ID]
			if !ok {
				t.Fatalf("event %s (%s) vanished — history is never truncated (§15.7)", ev.ID, ev.Action)
			}
			if !reflect.DeepEqual(survivor, ev) {
				t.Fatalf("event %s was rewritten by the restore", ev.ID)
			}
		}
	})

	t.Run("the regression and its rationale are still in the log", func(t *testing.T) {
		var found bool
		for _, ev := range after {
			if ev.Rationale == "customers like a personal touch" {
				found = true
			}
		}
		if !found {
			t.Fatalf("the failed experiment was erased — that pattern IS the evidence a reviewer reads (§15.7)")
		}
	})

	t.Run("the restore is an ordinary mutation naming what it restores", func(t *testing.T) {
		newest := after[0]
		if newest.Action != ActionWorkerUpdate {
			t.Fatalf("a restore must use the ordinary verb, got %q", newest.Action)
		}
		if newest.Rationale != restoreRationale {
			t.Fatalf("rationale: %q", newest.Rationale)
		}
		if newest.ActorWorker != "marketing-manager" || newest.ActorSession != "s-1043" {
			t.Fatalf("the restore must carry the acting session as its provenance: %+v", newest)
		}
		if got, _ := newest.PayloadString("system_prompt"); got != goodPrompt {
			t.Fatalf("payload must be the full restored row: %q", got)
		}
	})

	t.Run("folding to before the restore still shows the regression", func(t *testing.T) {
		snap := mustFold(t, s, project, before[0].CreatedAt)
		e, ok := snap.Worker("email-answerer")
		if !ok {
			t.Fatalf("worker missing")
		}
		if got, _ := e.Event.PayloadString("system_prompt"); got != florid.SystemPrompt {
			t.Fatalf("the week the org was wrong must still replay: %q", got)
		}
	})

	t.Run("folding to now shows the restored prompt, and so does the table", func(t *testing.T) {
		snap := mustFold(t, s, project, 0)
		e, _ := snap.Worker("email-answerer")
		got, _ := e.Event.PayloadString("system_prompt")
		live, err := s.GetWorker(ctx, project, "email-answerer")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got != goodPrompt || live.SystemPrompt != goodPrompt {
			t.Fatalf("fold %q / table %q, want %q", got, live.SystemPrompt, goodPrompt)
		}
	})

	t.Run("unrelated entities are untouched by the restore", func(t *testing.T) {
		snap := mustFold(t, s, project, 0)
		if _, ok := snap.Worker("tweet-author"); !ok {
			t.Fatalf("a targeted restore must not disturb other entities")
		}
	})
}

// A bulk restore is the same operation applied to every entity that differs —
// a run of ordinary mutations, still additive (§15.7). There is no
// restore_project verb to test because v1 deliberately ships none.
func TestRestoreIsForward_BulkIsJustManyForwardWrites(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()
	const project = "acme"

	for _, name := range []string{"a", "b", "c"} {
		w := NewWorker(project, name)
		w.Description = "as it was"
		if _, err := s.UpsertWorker(ctx, w, ConfigWrite{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	time.Sleep(2 * time.Millisecond)
	tuesday := time.Now().UnixMilli()
	time.Sleep(2 * time.Millisecond)

	for _, name := range []string{"a", "b"} {
		w, err := s.GetWorker(ctx, project, name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		w.Description = "drifted"
		if _, err := s.UpsertWorker(ctx, w, ConfigWrite{}); err != nil {
			t.Fatalf("drift %s: %v", name, err)
		}
	}
	if err := s.DeleteWorker(ctx, project, "c", ConfigWrite{}); err != nil {
		t.Fatalf("delete c: %v", err)
	}
	countBefore := len(mustListEvents(t, s, project))

	// The whole restore, expressed as forward writes derived from a fold.
	target := mustFold(t, s, project, tuesday)
	applied := 0
	for _, e := range target.OfKind(EntityWorker) {
		var w Worker
		b, _ := json.Marshal(e.Payload())
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatalf("decode folded worker: %v", err)
		}
		if _, err := s.UpsertWorker(ctx, &w, ConfigWrite{
			Rationale: "restore project to Tuesday",
		}); err != nil {
			t.Fatalf("restore %s: %v", w.Name, err)
		}
		applied++
	}
	if applied != 3 {
		t.Fatalf("the fold should have offered three workers to restore, got %d", applied)
	}

	if got := len(mustListEvents(t, s, project)); got != countBefore+3 {
		t.Fatalf("a bulk restore must only append: %d events before, %d after", countBefore, got)
	}
	// And the org is back — including the worker that had been deleted, brought
	// back by an ordinary create carrying its final state.
	now := mustFold(t, s, project, 0)
	for _, name := range []string{"a", "b", "c"} {
		e, ok := now.Worker(name)
		if !ok {
			t.Fatalf("worker %s not restored", name)
		}
		if got, _ := e.Event.PayloadString("description"); got != "as it was" {
			t.Fatalf("worker %s: %q", name, got)
		}
	}
}

func mustListEvents(t *testing.T, s *Store, project string) []*ConfigEvent {
	t.Helper()
	evs, err := s.ListConfigEvents(context.Background(), ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	return evs
}

// The fold is read-only: no code path in this package writes a snapshot back
// over the projections, which is what would make restore destructive.
func TestRestoreIsForward_FoldWritesNothing(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertWorker(ctx, NewWorker("acme", "w1"), ConfigWrite{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := mustListEvents(t, s, "acme")

	// Arm the write guard: any projection write outside the seam now fails. A
	// fold that mutated anything would be caught here.
	if err := InstallConfigEventGuard(s.gdb); err != nil {
		t.Fatalf("install guard: %v", err)
	}
	for _, at := range []int64{0, before[0].CreatedAt, 1} {
		if _, err := s.FoldTo(ctx, "acme", at); err != nil {
			t.Fatalf("FoldTo(%d) under the write guard: %v", at, err)
		}
	}
	if got := len(mustListEvents(t, s, "acme")); got != len(before) {
		t.Fatalf("folding changed the log: %d -> %d", len(before), got)
	}
	var workers int64
	if err := s.gdb.Model(&Worker{}).Count(&workers).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if workers != 1 {
		t.Fatalf("folding changed the projection table")
	}
}

// The seam still writes both rows or neither once seq allocation is in the
// transaction — a rolled-back mutation must not burn a sequence number either.
func TestConfigFold_RollbackDoesNotConsumeASequence(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertWorker(ctx, NewWorker("acme", "w1"), ConfigWrite{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	boom := fmt.Errorf("projection write failed")
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: "acme", Action: ActionWorkerUpdate, Payload: JSONMap{"name": "w1"},
	}, func(tx *gorm.DB) error { return boom }); err == nil {
		t.Fatalf("expected the mutation to fail")
	}
	if _, err := s.UpsertWorker(ctx, NewWorker("acme", "w2"), ConfigWrite{}); err != nil {
		t.Fatalf("next write: %v", err)
	}
	evs := mustListEvents(t, s, "acme")
	var seqs []int
	for _, ev := range evs {
		seqs = append(seqs, int(ev.Seq))
	}
	sort.Ints(seqs)
	if !reflect.DeepEqual(seqs, []int{1, 2}) {
		t.Fatalf("sequence must be gap-free across a rolled-back mutation, got %v", seqs)
	}
}
