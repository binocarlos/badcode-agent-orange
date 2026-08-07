package agentdb

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// J3's half of the seam, tested where it lives: the post-commit hook, the
// emission watermark, and the entity filter `config_history` reads through.
//
// The emitter itself is cmd/agentd's (stamping an event envelope is the host's
// job) — what belongs here is the guarantee it rests on: the hook fires once,
// after the commit, and never for a mutation that rolled back.
// ---------------------------------------------------------------------------

func TestConfigChangedEventHookFiresOnceAfterCommit(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	var seen []*ConfigEvent
	s.SetConfigEventHook(func(_ context.Context, ev *ConfigEvent) { seen = append(seen, ev) })

	_, err := s.UpsertSkill(ctx, &Skill{
		Name: "graph-gen", Visibility: "organizational", Customer: "acme",
		OwnerEmail: "u@acme.com", ContentHash: "hash1",
	}, ConfigWrite{Worker: "curator", Session: "s-991", Rationale: "publish it"})
	if err != nil {
		t.Fatalf("upsert skill: %v", err)
	}

	if len(seen) != 1 {
		t.Fatalf("want exactly one hook call per mutation, got %d", len(seen))
	}
	ev := seen[0]
	if ev.ID == "" || ev.Seq == 0 {
		t.Fatalf("the hook was handed an uncommitted-looking record: %+v", ev)
	}
	if ev.Action != ActionSkillCreate || ev.Project != "acme" {
		t.Fatalf("hook record: %+v", ev)
	}
	// It really is committed: the emitter may read it back.
	stored, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil || len(stored) != 1 || stored[0].ID != ev.ID {
		t.Fatalf("the hook's record is not the stored one: %v / %+v", err, stored)
	}
}

func TestConfigChangedEventHookNotCalledOnRollback(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	calls := 0
	s.SetConfigEventHook(func(context.Context, *ConfigEvent) { calls++ })

	boom := errors.New("projection write failed")
	_, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: "acme",
		Action:  ActionWorkerCreate,
		Payload: map[string]any{"name": "never-hired"},
	}, func(tx *gorm.DB) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("want the caller's error, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("the hook ran %d time(s) for a rolled-back mutation — a routed event would describe a change that never happened", calls)
	}

	// Nor does a validation failure reach it.
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: "acme", Action: ActionWorkerPromptWrite, Payload: map[string]any{"name": "w"},
	}, func(tx *gorm.DB) error { return nil }); err == nil {
		t.Fatal("a prompt write with no rationale was accepted (§15.5)")
	}
	if calls != 0 {
		t.Fatalf("the hook ran for a rejected mutation (%d)", calls)
	}
}

func TestConfigChangedEventWatermark(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	ev, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: "acme", Action: ActionWorkerCreate, Payload: map[string]any{"name": "w"},
	}, func(tx *gorm.DB) error { return nil })
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}

	pending, err := s.ListUnemittedConfigEvents(ctx, 0, 10)
	if err != nil {
		t.Fatalf("list unemitted: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != ev.ID {
		t.Fatalf("a fresh record must be queued for emission: %+v", pending)
	}

	if err := s.MarkConfigEventEmitted(ctx, ev.ID); err != nil {
		t.Fatalf("mark emitted: %v", err)
	}
	pending, err = s.ListUnemittedConfigEvents(ctx, 0, 10)
	if err != nil {
		t.Fatalf("list unemitted: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("an announced record is still queued: %+v", pending)
	}

	// Stamping twice is a no-op rather than a re-dating: the sweep may run
	// concurrently with the hook.
	var before ConfigEvent
	if err := s.DB().Where("id = ?", ev.ID).First(&before).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.MarkConfigEventEmitted(ctx, ev.ID); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	var after ConfigEvent
	if err := s.DB().Where("id = ?", ev.ID).First(&after).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.EmittedAt != before.EmittedAt {
		t.Fatalf("a second stamp moved the watermark: %d → %d", before.EmittedAt, after.EmittedAt)
	}

	// The grace window keeps the sweep off records the hook may still be
	// handling.
	fresh, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: "acme", Action: ActionWorkerUpdate, Payload: map[string]any{"name": "w"},
	}, func(tx *gorm.DB) error { return nil })
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	pending, err = s.ListUnemittedConfigEvents(ctx, fresh.CreatedAt, 10)
	if err != nil {
		t.Fatalf("list unemitted: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("the grace window did not exclude a just-written record: %+v", pending)
	}
}

// ── the `entity` filter (§15.9) ─────────────────────────────────────────────

func TestConfigEventsEntityFilter(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	write := func(action string, payload map[string]any) {
		t.Helper()
		cw := ConfigWrite{Worker: "manager", Session: "s-1"}
		if rationaleRequired[action] {
			cw.Rationale = "because"
		}
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: "acme", Action: action, Payload: payload, Write: cw,
		}, func(tx *gorm.DB) error { return nil }); err != nil {
			t.Fatalf("write %s: %v", action, err)
		}
	}
	write(ActionWorkerCreate, map[string]any{"name": "email-answerer"})
	write(ActionWorkerCreate, map[string]any{"name": "tweet-author"})
	write(ActionWorkerPromptWrite, map[string]any{"name": "email-answerer"})
	write(ActionScheduleCreate, map[string]any{"id": "sch-7"})
	write(ActionImageCreate, map[string]any{"name": "toolbox", "version": 1})
	write(ActionImageCreate, map[string]any{"name": "toolbox", "version": 2})
	write(ActionProjectSettingsPut, map[string]any{"project": "acme"})

	cases := []struct {
		entity  string
		want    int
		actions []string
	}{
		{"worker:email-answerer", 2, []string{ActionWorkerPromptWrite, ActionWorkerCreate}},
		{"worker:tweet-author", 1, []string{ActionWorkerCreate}},
		{"schedule:sch-7", 1, []string{ActionScheduleCreate}},
		{"image:toolbox:2", 1, []string{ActionImageCreate}},
		{"project-settings", 1, []string{ActionProjectSettingsPut}},
		{"worker:nobody", 0, nil},
	}
	for _, tc := range cases {
		got, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme", Entity: tc.entity})
		if err != nil {
			t.Fatalf("entity %q: %v", tc.entity, err)
		}
		if len(got) != tc.want {
			t.Fatalf("entity %q: want %d records, got %d", tc.entity, tc.want, len(got))
		}
		for i, action := range tc.actions {
			if got[i].Action != action {
				t.Fatalf("entity %q record %d: want %s (newest first), got %s", tc.entity, i, action, got[i].Action)
			}
		}
	}

	// The limit applies to the FILTERED records, not to the scan.
	got, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme", Entity: "worker:email-answerer", Limit: 1})
	if err != nil {
		t.Fatalf("limit: %v", err)
	}
	if len(got) != 1 || got[0].Action != ActionWorkerPromptWrite {
		t.Fatalf("limited entity query returned %+v", got)
	}

	// A project cannot read another's history through the filter (P5).
	got, err = s.ListConfigEvents(ctx, ConfigEventQuery{Project: "other", Entity: "worker:email-answerer"})
	if err != nil {
		t.Fatalf("cross project: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("entity filter leaked %d record(s) across projects", len(got))
	}
}

func TestConfigEventsParseEntityRef(t *testing.T) {
	ok := []struct {
		in   string
		want EntityRef
	}{
		{"worker:email-answerer", EntityRef{Kind: EntityWorker, Key: "email-answerer"}},
		{"image:toolbox:2", EntityRef{Kind: EntityImage, Key: "toolbox:2"}},
		{"project-settings", EntityRef{Kind: EntityProjectSettings}},
		{"project-prompt", EntityRef{Kind: EntityProjectPrompt}},
		{" schedule:sch-7 ", EntityRef{Kind: EntitySchedule, Key: "sch-7"}},
	}
	for _, tc := range ok {
		got, err := ParseEntityRef(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: want %+v, got %+v", tc.in, tc.want, got)
		}
		if round := got.String(); round != EntityRef(tc.want).String() {
			t.Fatalf("%q does not round-trip: %q", tc.in, round)
		}
	}
	for _, bad := range []string{"", "worker", "wroker:x", "project-settings:x", "worker: "} {
		if _, err := ParseEntityRef(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// TestConfigEventsEveryActionHasAnEntityKind is the tripwire for a track adding
// a §15.3 verb: without a kind, its records are invisible to every entity
// filter, which is a silent hole rather than a failure.
func TestConfigEventsEveryActionHasAnEntityKind(t *testing.T) {
	for _, a := range ConfigActions {
		kind, ok := entityKindForAction[a]
		if !ok {
			t.Fatalf("action %q has no entity kind — teach entityKindForAction about it", a)
		}
		found := false
		for _, listed := range ActionsForEntityKind(kind) {
			if listed == a {
				found = true
			}
		}
		if !found {
			t.Fatalf("ActionsForEntityKind(%q) does not list %q", kind, a)
		}
	}
}
