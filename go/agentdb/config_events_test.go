package agentdb

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// newConfigLogTestStore returns a sqlite Store with the config log and every
// projection table a registered mutation writes.
//
// WHEN YOU REGISTER A NEW MUTATION in ConfigMutations, add its model here.
func newConfigLogTestStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t) // sqlite + AutoMigrate(&Artifact{})
	if err := s.gdb.AutoMigrate(&ConfigEvent{}, &Skill{}, &CustomImage{},
		&ProjectSettings{}, &Worker{}, &Subscription{}, &Schedule{}); err != nil {
		t.Fatalf("automigrate config log + projections: %v", err)
	}
	return s
}

// ── The seam (§15.4) ────────────────────────────────────────────────────────

func TestConfigEvents_DualWriteInOneTransaction(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	sk, err := s.UpsertSkill(ctx, &Skill{
		Name: "graph-gen", Visibility: "organizational", Customer: "acme",
		OwnerEmail: "u@acme.com", ContentHash: "hash1",
	}, ConfigWrite{Worker: "curator", Session: "s-991", Rationale: "publish the graph generator"})
	if err != nil {
		t.Fatalf("upsert skill: %v", err)
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected exactly one config event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Action != ActionSkillCreate {
		t.Fatalf("action: want %q, got %q", ActionSkillCreate, ev.Action)
	}
	if ev.Project != "acme" || ev.ActorWorker != "curator" || ev.ActorSession != "s-991" {
		t.Fatalf("actor/project not threaded through: %+v", ev)
	}
	if ev.Rationale != "publish the graph generator" {
		t.Fatalf("rationale not stored: %q", ev.Rationale)
	}
	if ev.ID == "" {
		t.Fatalf("config event must carry a stable id (§15.2)")
	}
	// created_at is unix milliseconds — far past the seconds-scale epoch value
	// the other agentdb tables use.
	if ev.CreatedAt < 1_000_000_000_000 {
		t.Fatalf("created_at must be unix milliseconds, got %d", ev.CreatedAt)
	}
	// Payload is the FULL new state, not a diff (§15.2).
	for k, want := range map[string]any{"name": "graph-gen", "content_hash": "hash1", "customer": "acme", "id": sk.ID} {
		if got := ev.Payload[k]; got != want {
			t.Fatalf("payload[%s]: want %v, got %v (payload must be the full new row)", k, want, got)
		}
	}
}

func TestConfigEvents_PayloadIsFullStateOnUpdateToo(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	base := &Skill{Name: "graph-gen", Visibility: "organizational", Customer: "acme", OwnerEmail: "u@acme.com", ContentHash: "hash1"}
	if _, err := s.UpsertSkill(ctx, base, ConfigWrite{}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // distinct created_at ms, so "newest" is unambiguous
	if _, err := s.UpsertSkill(ctx, &Skill{
		Name: "graph-gen", Visibility: "organizational", Customer: "acme", OwnerEmail: "u2@acme.com", ContentHash: "hash2",
	}, ConfigWrite{}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected two events (the update appends, it does not overwrite), got %d", len(evs))
	}
	// Newest first, and the newest payload carries the WHOLE row — unchanged
	// fields included — so a fold is last-writer-wins with no merge algebra.
	newest := evs[0]
	if newest.Payload["content_hash"] != "hash2" {
		t.Fatalf("newest payload should hold the new state: %v", newest.Payload)
	}
	if newest.Payload["name"] != "graph-gen" || newest.Payload["visibility"] != "organizational" {
		t.Fatalf("payload is a diff, not full state: %v", newest.Payload)
	}
}

func TestConfigEvents_RollbackWritesNeither(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	boom := fmt.Errorf("projection write failed")
	_, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: "acme",
		Action:  ActionSkillCreate,
		Payload: &Skill{ID: "sk-rollback", Name: "doomed", Customer: "acme"},
	}, func(tx *gorm.DB) error {
		if err := tx.Create(&Skill{ID: "sk-rollback", Name: "doomed", Customer: "acme", Visibility: "organizational"}).Error; err != nil {
			return err
		}
		return boom
	})
	if err == nil {
		t.Fatalf("expected the mutation error to surface")
	}

	var skills []Skill
	if err := s.gdb.WithContext(ctx).Where("id = ?", "sk-rollback").Find(&skills).Error; err != nil {
		t.Fatalf("read skills: %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("projection row survived a rolled-back transaction: %+v", skills)
	}
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("config event survived a rolled-back transaction: %+v", evs)
	}
}

func TestConfigEvents_ValidationRejectsBadChanges(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()
	noop := func(tx *gorm.DB) error { return nil }

	tests := []struct {
		name    string
		change  ConfigChange
		fn      func(tx *gorm.DB) error
		wantErr string
	}{
		{
			name:    "missing project",
			change:  ConfigChange{Action: ActionWorkerCreate, Payload: JSONMap{"name": "w"}},
			fn:      noop,
			wantErr: "requires a project",
		},
		{
			name:    "action outside the closed vocabulary",
			change:  ConfigChange{Project: "acme", Action: "worker_yeet", Payload: JSONMap{"name": "w"}},
			fn:      noop,
			wantErr: "is not a config-log action",
		},
		{
			name:    "worker prompt write without a rationale",
			change:  ConfigChange{Project: "acme", Action: ActionWorkerPromptWrite, Payload: JSONMap{"name": "w"}},
			fn:      noop,
			wantErr: "requires a non-empty rationale",
		},
		{
			name: "project prompt write with a whitespace rationale",
			change: ConfigChange{Project: "acme", Action: ActionProjectPromptWrite, Payload: JSONMap{"system_prompt": "x"},
				Write: ConfigWrite{Rationale: "   "}},
			fn:      noop,
			wantErr: "requires a non-empty rationale",
		},
		{
			name:    "nil payload",
			change:  ConfigChange{Project: "acme", Action: ActionWorkerCreate},
			fn:      noop,
			wantErr: "requires a payload",
		},
		{
			name:    "scalar payload is not full state",
			change:  ConfigChange{Project: "acme", Action: ActionWorkerCreate, Payload: "just a string"},
			fn:      noop,
			wantErr: "must be a JSON object",
		},
		{
			name:    "nil mutation function",
			change:  ConfigChange{Project: "acme", Action: ActionWorkerCreate, Payload: JSONMap{"name": "w"}},
			fn:      nil,
			wantErr: "requires a mutation function",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.WithConfigEvent(ctx, tc.change, tc.fn)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			// Malformed input fails before EITHER row lands (§15.4).
			evs, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
			if len(evs) != 0 {
				t.Fatalf("a rejected change still wrote a log record: %+v", evs)
			}
		})
	}
}

func TestConfigEvents_RationaleOptionalOffThePromptWrites(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	for _, action := range ConfigActions {
		if action == ActionWorkerPromptWrite || action == ActionProjectPromptWrite {
			continue
		}
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: "acme", Action: action, Payload: JSONMap{"k": "v"},
		}, func(tx *gorm.DB) error { return nil }); err != nil {
			t.Fatalf("action %q must accept an empty rationale (§15.5): %v", action, err)
		}
	}
	// …and the two prompt writes accept a non-empty one.
	for _, action := range []string{ActionWorkerPromptWrite, ActionProjectPromptWrite} {
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: "acme", Action: action, Payload: JSONMap{"system_prompt": "be terse"},
			Write: ConfigWrite{Rationale: "three customers called the answers walls of text"},
		}, func(tx *gorm.DB) error { return nil }); err != nil {
			t.Fatalf("action %q with a rationale: %v", action, err)
		}
	}
}

// ── Project isolation (§12: a negative test on every new table) ─────────────

func TestConfigEvents_ProjectIsolation(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	seed := func(project, worker string) {
		t.Helper()
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: project, Action: ActionWorkerCreate,
			Payload: JSONMap{"name": worker, "project": project},
			Write:   ConfigWrite{Worker: "manager", Session: "s-" + project},
		}, func(tx *gorm.DB) error { return nil }); err != nil {
			t.Fatalf("seed %s: %v", project, err)
		}
	}
	seed("acme", "email-answerer")
	seed("globex", "tweet-author")
	seed("globex", "email-reviewer")

	acme, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list acme: %v", err)
	}
	if len(acme) != 1 {
		t.Fatalf("expected 1 acme record, got %d", len(acme))
	}
	for _, ev := range acme {
		if ev.Project != "acme" {
			t.Fatalf("cross-project leak: %+v", ev)
		}
		if ev.Payload["name"] == "tweet-author" || ev.Payload["name"] == "email-reviewer" {
			t.Fatalf("globex payload leaked into an acme query: %+v", ev)
		}
	}

	// A filter that would match the other project's rows still returns nothing:
	// the project predicate is applied in code, on every query (P5).
	leak, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme", ActorWorker: "manager", Action: "worker_*"})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(leak) != 1 {
		t.Fatalf("expected the acme record only, got %d", len(leak))
	}

	// An unscoped query is refused outright rather than silently returning all
	// projects' history.
	if _, err := s.ListConfigEvents(ctx, ConfigEventQuery{}); err == nil {
		t.Fatalf("expected an error for a project-less query")
	}
}

// ── The read filters (§15.9's austerity: equality + range) ─────────────────

func TestConfigEvents_ListFilters(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	mk := func(action, worker string) *ConfigEvent {
		t.Helper()
		ev, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: "acme", Action: action, Payload: JSONMap{"a": action},
			Write: ConfigWrite{Worker: worker, Rationale: "because"},
		}, func(tx *gorm.DB) error { return nil })
		if err != nil {
			t.Fatalf("seed %s: %v", action, err)
		}
		// Distinct created_at milliseconds, so ordering and the since/until
		// range are unambiguous (the log orders by created_at, id — §15.6).
		time.Sleep(2 * time.Millisecond)
		return ev
	}
	first := mk(ActionWorkerCreate, "manager")
	mk(ActionWorkerUpdate, "manager")
	last := mk(ActionScheduleCreate, "planner")

	tests := []struct {
		name string
		q    ConfigEventQuery
		want int
	}{
		{"all", ConfigEventQuery{Project: "acme"}, 3},
		{"exact action", ConfigEventQuery{Project: "acme", Action: ActionWorkerUpdate}, 1},
		{"prefix action", ConfigEventQuery{Project: "acme", Action: "worker_*"}, 2},
		{"actor", ConfigEventQuery{Project: "acme", ActorWorker: "planner"}, 1},
		{"limit", ConfigEventQuery{Project: "acme", Limit: 2}, 2},
		{"since", ConfigEventQuery{Project: "acme", Since: last.CreatedAt}, 1},
		{"until", ConfigEventQuery{Project: "acme", Until: first.CreatedAt}, 1},
		{"no match", ConfigEventQuery{Project: "acme", ActorWorker: "nobody"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListConfigEvents(ctx, tc.q)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("want %d rows, got %d: %+v", tc.want, len(got), got)
			}
			if got == nil {
				t.Fatalf("no-hit query must return an empty slice, not nil")
			}
		})
	}

	// Newest first (§15.9).
	all, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if all[0].ID != last.ID || all[len(all)-1].ID != first.ID {
		t.Fatalf("expected newest-first ordering, got %v", []string{all[0].Action, all[1].Action, all[2].Action})
	}
}

// ── Conformance: the test that stops a later track from forgetting ─────────

// configMutationProbes says HOW to invoke each registered mutation method.
//
// WHEN YOU REGISTER A NEW MUTATION in ConfigMutations, add a probe here — the
// conformance test fails without one, and then proves the method really does
// append exactly one config event.
var configMutationProbes = map[string]func(ctx context.Context, s *Store) error{
	"UpsertSkill": func(ctx context.Context, s *Store) error {
		_, err := s.UpsertSkill(ctx, &Skill{
			Name: "probe", Visibility: "organizational", Customer: probeProject, OwnerEmail: "p@probe.com",
		}, ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"UpsertCustomImage": func(ctx context.Context, s *Store) error {
		_, err := s.UpsertCustomImage(ctx, &CustomImage{
			Name: "probe", Visibility: "organizational", Customer: probeProject, OwnerEmail: "p@probe.com",
		}, ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"CreateSkill": func(ctx context.Context, s *Store) error {
		_, err := s.CreateSkill(ctx, &Skill{
			Name: "probe-skill", Customer: probeProject, Markdown: "# probe",
		}, ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"CreateCustomImage": func(ctx context.Context, s *Store) error {
		_, err := s.CreateCustomImage(ctx, &CustomImage{
			Name: "probe", Customer: probeProject, RegistryHandle: `{"kind":"blob-archive","ref":"probe"}`,
		}, ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"PutProjectSettings": func(ctx context.Context, s *Store) error {
		_, err := s.PutProjectSettings(ctx, &ProjectSettings{
			Project: probeProject, SystemPrompt: "probe",
		}, ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"UpsertWorker": func(ctx context.Context, s *Store) error {
		_, err := s.UpsertWorker(ctx, NewWorker(probeProject, "probe"), ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"DeleteWorker": func(ctx context.Context, s *Store) error {
		if err := seedProbeWorker(ctx, s); err != nil {
			return err
		}
		return s.DeleteWorker(ctx, probeProject, "probe", ConfigWrite{Worker: "prober", Session: "s-probe"})
	},
	"CreateSubscription": func(ctx context.Context, s *Store) error {
		_, err := s.CreateSubscription(ctx, &Subscription{
			Project: probeProject, EventType: "email.received", Worker: "probe", Enabled: true,
		}, ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"UpdateSubscription": func(ctx context.Context, s *Store) error {
		if err := seedProbeSubscription(ctx, s); err != nil {
			return err
		}
		_, err := s.UpdateSubscription(ctx, &Subscription{
			ID: probeSubscriptionID, Project: probeProject, EventType: "email.*", Worker: "probe", Enabled: false,
		}, ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"DeleteSubscription": func(ctx context.Context, s *Store) error {
		if err := seedProbeSubscription(ctx, s); err != nil {
			return err
		}
		return s.DeleteSubscription(ctx, probeProject, probeSubscriptionID, ConfigWrite{Worker: "prober", Session: "s-probe"})
	},
	"CreateSchedule": func(ctx context.Context, s *Store) error {
		_, err := s.CreateSchedule(ctx, NewSchedule(probeProject, "probe", "0 10 * * *", "write the morning tweet"),
			ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"UpdateSchedule": func(ctx context.Context, s *Store) error {
		if err := seedProbeSchedule(ctx, s); err != nil {
			return err
		}
		_, err := s.UpdateSchedule(ctx, &Schedule{
			ID: probeScheduleID, Project: probeProject, Worker: "probe",
			Cron: "0 17 * * *", Input: "write the evening tweet", Enabled: true,
		}, ConfigWrite{Worker: "prober", Session: "s-probe"})
		return err
	},
	"DisableSchedule": func(ctx context.Context, s *Store) error {
		if err := seedProbeSchedule(ctx, s); err != nil {
			return err
		}
		_, err := s.DisableSchedule(ctx, probeProject, probeScheduleID,
			ConfigWrite{Worker: "prober", Session: "s-probe", Rationale: "worker no longer exists"})
		return err
	},
	"DeleteSchedule": func(ctx context.Context, s *Store) error {
		if err := seedProbeSchedule(ctx, s); err != nil {
			return err
		}
		return s.DeleteSchedule(ctx, probeProject, probeScheduleID, ConfigWrite{Worker: "prober", Session: "s-probe"})
	},
}

const probeSubscriptionID = "sub-probe"

const probeScheduleID = "sched-probe"

// The update and delete probes need a row to act on, and they must produce
// exactly ONE config event — so the precondition cannot be seeded through the
// seam. Raw SQL is the deliberate escape: it bypasses both the seam and the
// write guard (which hooks GORM's create/update/delete callbacks, not Exec),
// which is exactly what a fixture wants and what no production path may do.
func seedProbeWorker(ctx context.Context, s *Store) error {
	return s.gdb.WithContext(ctx).Exec(
		`INSERT INTO workers (project, name, description, system_prompt, mcp_config, image, briefing,
		                      max_instances, enabled, created_at, updated_at)
		 VALUES (?, ?, '', '', '{}', '', NULL, 1, 1, 0, 0)`,
		probeProject, "probe").Error
}

func seedProbeSubscription(ctx context.Context, s *Store) error {
	return s.gdb.WithContext(ctx).Exec(
		`INSERT INTO subscriptions (id, project, event_type, filter, worker, max_firings_per_hour,
		                            enabled, created_at, updated_at)
		 VALUES (?, ?, 'email.received', '{}', 'probe', 0, 1, 0, 0)`,
		probeSubscriptionID, probeProject).Error
}

func seedProbeSchedule(ctx context.Context, s *Store) error {
	return s.gdb.WithContext(ctx).Exec(
		`INSERT INTO schedules (id, project, worker, cron, input, enabled, created_at, updated_at)
		 VALUES (?, ?, 'probe', '0 10 * * *', 'reconcile the workforce', 1, 0, 0)`,
		probeScheduleID, probeProject).Error
}

const probeProject = "probe-project"

// configEntityNouns are the configuration entities of §15.3. A *Store method
// naming one of them is presumed to be a configuration mutation unless it is a
// read (configReadVerbs) — deny by default.
var configEntityNouns = []string{
	"Worker", "Project", "Setting", "Prompt", "Subscription", "Schedule", "Image", "Skill", "Config",
}

var configReadVerbs = []string{
	"Get", "List", "Count", "Search", "Find", "Has", "Exists", "Resolve", "Fold", "Load", "Read", "Lookup", "Query",
}

// looksLikeConfigMutation is the mechanical classifier: it names a
// configuration entity and does not begin with a read verb.
func looksLikeConfigMutation(method string) bool {
	for _, v := range configReadVerbs {
		if strings.HasPrefix(method, v) {
			return false
		}
	}
	// The seam and its own helpers are not mutations of configuration.
	switch method {
	case "WithConfigEvent", "DB":
		return false
	}
	for _, n := range configEntityNouns {
		if strings.Contains(method, n) {
			return true
		}
	}
	return false
}

func TestMutationsAreLogged(t *testing.T) {
	registered := map[string]ConfigMutation{}
	for _, m := range ConfigMutations {
		registered[m.Method] = m
	}

	// 1. Deny by default: every method that looks like a configuration mutation
	//    must be registered or explicitly exempted. This is the check that fires
	//    when a later track adds a mutation method without adopting the seam.
	t.Run("every_config_mutation_method_is_registered_or_exempt", func(t *testing.T) {
		storeType := reflect.TypeOf(&Store{})
		var unaccounted []string
		for i := 0; i < storeType.NumMethod(); i++ {
			name := storeType.Method(i).Name
			if !looksLikeConfigMutation(name) {
				continue
			}
			if _, ok := registered[name]; ok {
				continue
			}
			if _, ok := ConfigMutationExempt[name]; ok {
				continue
			}
			unaccounted = append(unaccounted, name)
		}
		if len(unaccounted) > 0 {
			sort.Strings(unaccounted)
			t.Fatalf("these *Store methods mutate configuration but write no config event: %v\n"+
				"Either route them through Store.WithConfigEvent and add them to ConfigMutations "+
				"(see the adoption recipe at the top of config_events.go), or add them to "+
				"ConfigMutationExempt with the reason.", unaccounted)
		}
	})

	// 2. Exemptions are pinned: growing the escape hatch is a deliberate edit of
	//    this list, never a silent omission.
	t.Run("exemptions_are_pinned", func(t *testing.T) {
		// Grown deliberately on 2026-07-25 by the two event-store methods: §15.3
		// rule 3 keeps the event spine out of the config log — project_events is
		// already its own append-only log and the delivered flag is the router's
		// runtime watermark, not a setting.
		// Grown once more on 2026-07-25 by B4: MarkCustomImageResumed stamps §5's
		// last_resumed_at when a session launches from a catalogue version —
		// runtime telemetry, not a decision anyone made.
		// Grown twice more on 2026-07-25 by J3, and these two are NOT of the same
		// kind as the GC/telemetry escapes above: they belong to the config log
		// ITSELF. MarkConfigEventEmitted writes the log's own emission watermark
		// (the exact analogue of MarkProjectEventDelivered on the event log) and
		// SetConfigEventHook writes no table at all — it installs the post-commit
		// callback at boot. Logging a config event about announcing a config
		// event would be a loop with no bottom.
		want := []string{
			"ClearWorkerBinding",
			"CreateProjectEvent",
			"DeleteCustomImage",
			"DeleteSkill",
			"MarkConfigEventEmitted",
			"MarkCustomImageReaped",
			"MarkCustomImageResumed",
			"MarkProjectEventDelivered",
			"SetConfigEventHook",
			"SetSkillVisibility",
			"SetWorkerBinding",
		}
		var got []string
		for m, reason := range ConfigMutationExempt {
			got = append(got, m)
			if strings.TrimSpace(reason) == "" {
				t.Fatalf("exemption %q has no reason", m)
			}
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("the exemption list changed.\nwant %v\ngot  %v\n"+
				"Exempting a configuration mutation is a decision on the record: update this "+
				"expectation deliberately, with the reason in ConfigMutationExempt.", want, got)
		}
	})

	// 3. Registry entries are well formed and name real methods.
	t.Run("registry_entries_are_well_formed", func(t *testing.T) {
		storeType := reflect.TypeOf(&Store{})
		seen := map[string]bool{}
		for _, m := range ConfigMutations {
			if seen[m.Method] {
				t.Fatalf("duplicate registry entry for %q", m.Method)
			}
			seen[m.Method] = true
			if _, ok := storeType.MethodByName(m.Method); !ok {
				t.Fatalf("registry names %q, which is not a *Store method", m.Method)
			}
			if len(m.Actions) == 0 {
				t.Fatalf("%s registers no action", m.Method)
			}
			for _, a := range m.Actions {
				if !isConfigAction(a) {
					t.Fatalf("%s registers %q, outside the §15.3 vocabulary", m.Method, a)
				}
			}
			if len(m.Tables) == 0 {
				t.Fatalf("%s registers no projection table (it would not be guarded)", m.Method)
			}
		}
	})

	// 4. Every registered mutation has a probe…
	t.Run("every_registered_mutation_has_a_probe", func(t *testing.T) {
		for _, m := range ConfigMutations {
			if configMutationProbes[m.Method] == nil {
				t.Fatalf("no probe for %q — add one to configMutationProbes so this test can prove "+
					"the method appends a config event", m.Method)
			}
		}
	})

	// 5. …and running it appends exactly one config event, with the guard armed
	//    so any projection write that skipped the seam fails the call.
	t.Run("every_registered_mutation_writes_exactly_one_config_event", func(t *testing.T) {
		for _, m := range ConfigMutations {
			t.Run(m.Method, func(t *testing.T) {
				s := newConfigLogTestStore(t)
				if err := InstallConfigEventGuard(s.gdb); err != nil {
					t.Fatalf("install guard: %v", err)
				}
				ctx := context.Background()
				if err := configMutationProbes[m.Method](ctx, s); err != nil {
					t.Fatalf("probe: %v", err)
				}
				evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: probeProject})
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(evs) != 1 {
					t.Fatalf("%s must append exactly one config event, got %d", m.Method, len(evs))
				}
				ok := false
				for _, a := range m.Actions {
					if evs[0].Action == a {
						ok = true
					}
				}
				if !ok {
					t.Fatalf("%s wrote action %q, not one of its registered actions %v", m.Method, evs[0].Action, m.Actions)
				}
				if len(evs[0].Payload) == 0 {
					t.Fatalf("%s wrote an empty payload; §15.2 wants the full new state", m.Method)
				}
			})
		}
	})

	// 6. The guard really does catch a projection write that skips the seam —
	//    otherwise check 5 would pass vacuously.
	t.Run("guard_rejects_a_projection_write_that_skips_the_seam", func(t *testing.T) {
		s := newConfigLogTestStore(t)
		if err := InstallConfigEventGuard(s.gdb); err != nil {
			t.Fatalf("install guard: %v", err)
		}
		ctx := context.Background()

		err := s.gdb.WithContext(ctx).Create(&Skill{
			ID: "sk-sneaky", Name: "sneaky", Visibility: "organizational", Customer: probeProject,
		}).Error
		if err == nil {
			t.Fatalf("guard did not reject an unlogged write to a guarded projection table")
		}
		if !strings.Contains(err.Error(), "outside a config-event transaction") {
			t.Fatalf("unexpected guard error: %v", err)
		}

		// Updates and deletes are guarded too (deletes append as well, §15.3).
		if err := s.gdb.WithContext(ctx).Model(&Skill{}).Where("id = ?", "sk-x").
			Update("name", "renamed").Error; err == nil {
			t.Fatalf("guard did not reject an unlogged UPDATE")
		}
		if err := s.gdb.WithContext(ctx).Where("id = ?", "sk-x").Delete(&Skill{}).Error; err == nil {
			t.Fatalf("guard did not reject an unlogged DELETE")
		}

		// Unguarded tables are untouched by the guard.
		if err := s.gdb.WithContext(ctx).Create(&Artifact{
			ID: "a-1", SessionID: "s1", FilePath: "x", Status: "live",
		}).Error; err != nil {
			t.Fatalf("guard leaked onto a non-projection table: %v", err)
		}

		// And the seam itself passes the guard.
		if err := configMutationProbes["UpsertSkill"](ctx, s); err != nil {
			t.Fatalf("seam write must pass the guard: %v", err)
		}
	})

	// 7. The guarded set is derived from the registry, so registering a
	//    mutation guards its table without a second list to maintain.
	t.Run("guarded_tables_are_derived_from_the_registry", func(t *testing.T) {
		got := ConfigGuardedTables()
		want := []string{"agent_custom_images", "agent_skills", "project_settings", "schedules", "subscriptions", "workers"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("guarded tables: want %v, got %v", want, got)
		}
	})
}
