package agentdb

// topology_apply_test.go — T2's atomic apply, on sqlite. What must hold:
//
//   - every row lands through its ordinary config-logged mutation, and the
//     `topology_apply` bracket is the LAST event of the apply (highest seq);
//   - a refusal (collision, unmet precondition) changes NOTHING;
//   - a failure past the first write rolls the whole apply back — a
//     half-applied topology is impossible;
//   - the settings patch is read-current → overlay non-zero → write whole;
//   - `config.changed` hooks fire only after the commit, one per record, in
//     write order — and never for an apply that rolled back.
//
// The Postgres-only parts (memory seeds, jsonb answers round-trip) live in
// topology_apply_live_pg_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// applyFixture returns a two-worker bundle wired like a miniature topology:
// one subscription and one schedule pointing at the workers, plus a settings
// patch. Rows are fresh on every call — ApplyTopology stamps them.
func applyFixture() TopologyApplication {
	actor := NewWorker("", "fixture-actor")
	actor.SystemPrompt = "Do the work."
	critic := NewWorker("", "fixture-critic")
	critic.SystemPrompt = "Score the work."
	return TopologyApplication{
		Project:  "topo-project",
		Topology: "fixture@v1",
		Answers:  JSONMap{"cadence": "daily", "strict": true},
		Workers:  []*Worker{actor, critic},
		Subscriptions: []*Subscription{{
			EventType: "worker.finished", Worker: "fixture-critic", Enabled: true,
			Filter: JSONMap{"worker": "fixture-actor"},
		}},
		Schedules: []*Schedule{{
			Worker: "fixture-actor", Cron: "0 9 * * *", Input: "morning run", Enabled: true,
		}},
		SettingsPatch: &ProjectSettings{Project: "topo-project", MaxConcurrentJobs: 7},
	}
}

func TestApplyTopology_WritesEveryRowAndBracketsThemInOneApply(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	res, err := s.ApplyTopology(ctx, applyFixture(), ConfigWrite{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The rows exist, read back through the ordinary reads.
	workers, err := s.ListWorkers(ctx, "topo-project")
	if err != nil || len(workers) != 2 {
		t.Fatalf("workers = %v, %v", workers, err)
	}
	subs, err := s.ListSubscriptions(ctx, "topo-project")
	if err != nil || len(subs) != 1 {
		t.Fatalf("subscriptions = %v, %v", subs, err)
	}
	schedules, err := s.ListSchedules(ctx, "topo-project")
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules = %v, %v", schedules, err)
	}
	ps, err := s.GetProjectSettings(ctx, "topo-project")
	if err != nil || ps.MaxConcurrentJobs != 7 {
		t.Fatalf("settings = %+v, %v", ps, err)
	}
	// …and the result echoes the stored rows, project stamped, ids allocated.
	if len(res.Workers) != 2 || res.Workers[0].Project != "topo-project" {
		t.Fatalf("result workers: %+v", res.Workers)
	}
	if len(res.Subscriptions) != 1 || res.Subscriptions[0].ID == "" {
		t.Fatalf("result subscription got no id: %+v", res.Subscriptions)
	}

	// One config event per row plus the bracket, and the bracket is LAST.
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "topo-project"})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 6 { // 2× worker_create, subscription_create, schedule_create, project_settings_put, topology_apply
		t.Fatalf("want 6 config events, got %d: %+v", len(evs), evs)
	}
	newest := evs[0] // newest first
	if newest.Action != ActionTopologyApply {
		t.Fatalf("the bracket must carry the highest seq; newest action = %q", newest.Action)
	}
	for _, ev := range evs[1:] {
		if ev.Seq >= newest.Seq {
			t.Fatalf("row event seq %d not below the bracket's %d", ev.Seq, newest.Seq)
		}
	}
	if newest.Payload["topology"] != "fixture@v1" {
		t.Fatalf("bracket payload: %v", newest.Payload)
	}
	answers, _ := newest.Payload["answers"].(map[string]any)
	if answers["cadence"] != "daily" {
		t.Fatalf("bracket answers: %v", newest.Payload["answers"])
	}
	if res.Event == nil || res.Event.ID != newest.ID {
		t.Fatalf("result.Event = %+v, want the bracket %s", res.Event, newest.ID)
	}

	// The bracket folds under its own entity kind.
	ref, err := EntityRefFor(newest)
	if err != nil || ref.String() != "topology:fixture@v1" {
		t.Fatalf("EntityRefFor = %v, %v", ref, err)
	}
}

func TestApplyTopology_CollisionRefusalChangesNothing(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	// The critic's name is already taken.
	if _, err := s.UpsertWorker(ctx, NewWorker("topo-project", "fixture-critic"), ConfigWrite{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "topo-project"})

	_, err := s.ApplyTopology(ctx, applyFixture(), ConfigWrite{})
	if !errors.Is(err, ErrTopologyNameCollision) {
		t.Fatalf("want ErrTopologyNameCollision, got %v", err)
	}

	// NOTHING changed: no rows, no log records.
	workers, _ := s.ListWorkers(ctx, "topo-project")
	if len(workers) != 1 {
		t.Fatalf("refusal wrote workers: %+v", workers)
	}
	subs, _ := s.ListSubscriptions(ctx, "topo-project")
	if len(subs) != 0 {
		t.Fatalf("refusal wrote subscriptions: %+v", subs)
	}
	schedules, _ := s.ListSchedules(ctx, "topo-project")
	if len(schedules) != 0 {
		t.Fatalf("refusal wrote schedules: %+v", schedules)
	}
	after, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "topo-project"})
	if len(after) != len(before) {
		t.Fatalf("refusal appended config events: %d -> %d", len(before), len(after))
	}
}

func TestApplyTopology_UnmetPreconditionRefusalChangesNothing(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	app := applyFixture()
	app.RequiredImages = []string{"toolbox"}
	app.RequiredSkills = []string{"graph-gen"}

	_, err := s.ApplyTopology(ctx, app, ConfigWrite{})
	if !errors.Is(err, ErrTopologyPreconditionUnmet) {
		t.Fatalf("want ErrTopologyPreconditionUnmet, got %v", err)
	}
	// Both missing assets are named — the loud failure D2 asks for.
	for _, want := range []string{"image toolbox", "skill graph-gen"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not name %q: %v", want, err)
		}
	}
	workers, _ := s.ListWorkers(ctx, "topo-project")
	evs, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "topo-project"})
	if len(workers) != 0 || len(evs) != 0 {
		t.Fatalf("refusal wrote: %d workers, %d events", len(workers), len(evs))
	}
}

func TestApplyTopology_MetPreconditionsPass(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	// Teach the project the assets first, through the ordinary catalogue writes.
	if _, err := s.CreateCustomImage(ctx, &CustomImage{
		Name: "toolbox", Customer: "topo-project", RegistryHandle: `{"kind":"blob-archive","ref":"t"}`,
	}, ConfigWrite{}); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	if _, err := s.CreateSkill(ctx, &Skill{
		Name: "graph-gen", Customer: "topo-project", Markdown: "# graphs",
	}, ConfigWrite{}); err != nil {
		t.Fatalf("seed skill: %v", err)
	}

	app := applyFixture()
	app.RequiredImages = []string{"toolbox"} // floating reference, §13.3
	app.RequiredSkills = []string{"graph-gen"}
	if _, err := s.ApplyTopology(ctx, app, ConfigWrite{}); err != nil {
		t.Fatalf("apply with met preconditions: %v", err)
	}
}

// A failure PAST the first write must take the whole apply down with it — this
// is the test that separates one transaction from validate-then-write. The
// unparseable cron cannot come out of a registered topology (T1 validates cron
// at render time), which is exactly why it makes a good fault injection: it
// fails in CreateSchedule, after both workers and the subscription landed.
func TestApplyTopology_MidApplyFailureRollsEverythingBack(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	app := applyFixture()
	app.Schedules[0].Cron = "not-cron"

	_, err := s.ApplyTopology(ctx, app, ConfigWrite{})
	if !errors.Is(err, ErrScheduleInvalid) {
		t.Fatalf("want ErrScheduleInvalid, got %v", err)
	}

	workers, _ := s.ListWorkers(ctx, "topo-project")
	subs, _ := s.ListSubscriptions(ctx, "topo-project")
	evs, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "topo-project"})
	if len(workers) != 0 || len(subs) != 0 || len(evs) != 0 {
		t.Fatalf("half-applied topology survived: %d workers, %d subscriptions, %d events",
			len(workers), len(subs), len(evs))
	}
}

func TestApplyTopology_SettingsPatchOverlaysCurrentSettings(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	// The project already has settings a patch must not clobber.
	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
		Project: "topo-project", SystemPrompt: "We are BadCode.", DailyTokensHard: 900,
	}, ConfigWrite{}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	app := applyFixture() // patch sets max_concurrent_jobs only
	if _, err := s.ApplyTopology(ctx, app, ConfigWrite{}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	ps, err := s.GetProjectSettings(ctx, "topo-project")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if ps.MaxConcurrentJobs != 7 {
		t.Fatalf("patch did not apply: %+v", ps)
	}
	if ps.SystemPrompt != "We are BadCode." || ps.DailyTokensHard != 900 {
		t.Fatalf("the whole-object write clobbered current settings: %+v", ps)
	}
}

func TestTopologySettingsOverlay(t *testing.T) {
	current := DefaultProjectSettings("p")
	current.SystemPrompt = "keep me"
	current.DailyTokensSoft = 100

	merged, fields := TopologySettingsOverlay(current, &ProjectSettings{
		BaseImage:         "toolbox:2",
		MaxConcurrentJobs: 9,
	})
	if merged.BaseImage != "toolbox:2" || merged.MaxConcurrentJobs != 9 {
		t.Fatalf("overlay missed a field: %+v", merged)
	}
	if merged.SystemPrompt != "keep me" || merged.DailyTokensSoft != 100 {
		t.Fatalf("overlay clobbered a kept field: %+v", merged)
	}
	if len(fields) != 2 || fields[0] != "base_image" || fields[1] != "max_concurrent_jobs" {
		t.Fatalf("fields = %v", fields)
	}
	// A nil patch is a clean copy.
	merged, fields = TopologySettingsOverlay(current, nil)
	if merged.SystemPrompt != "keep me" || len(fields) != 0 {
		t.Fatalf("nil patch: %+v, %v", merged, fields)
	}
}

// The `config.changed` hook must see every record of a committed apply, in
// write order, and see nothing of a refused one (§15.4: emission is
// post-commit; an event for a rolled-back change must not exist).
func TestApplyTopology_HooksFireAfterCommitAndNeverOnRefusal(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	var emitted []string
	s.SetConfigEventHook(func(_ context.Context, ev *ConfigEvent) {
		emitted = append(emitted, ev.Action)
	})

	// A refused apply emits nothing.
	bad := applyFixture()
	bad.RequiredImages = []string{"missing"}
	if _, err := s.ApplyTopology(ctx, bad, ConfigWrite{}); !errors.Is(err, ErrTopologyPreconditionUnmet) {
		t.Fatalf("want refusal, got %v", err)
	}
	if len(emitted) != 0 {
		t.Fatalf("a refused apply emitted config.changed hooks: %v", emitted)
	}

	// A committed apply emits one hook per record, bracket last.
	if _, err := s.ApplyTopology(ctx, applyFixture(), ConfigWrite{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []string{
		ActionWorkerCreate, ActionWorkerCreate,
		ActionSubscriptionCreate, ActionScheduleCreate,
		ActionProjectSettingsPut, ActionTopologyApply,
	}
	if len(emitted) != len(want) {
		t.Fatalf("emitted %v, want %v", emitted, want)
	}
	for i := range want {
		if emitted[i] != want[i] {
			t.Fatalf("emitted[%d] = %q, want %q (full: %v)", i, emitted[i], want[i], emitted)
		}
	}
}
