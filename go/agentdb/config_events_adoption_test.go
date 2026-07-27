package agentdb

import (
	"context"
	"testing"
)

// The behaviour the adopted mutations owe §15.3: the most specific action a
// write represents, a delete that carries the row as it last stood, and a
// failed write that leaves neither the projection row nor the log record.
//
// These run on the shared config-log test store (config_events_test.go), which
// migrates the log alongside every projection table a registered mutation
// touches.

// newestConfigEvent returns the most recent log record for a project, failing
// the test when the count is not what the caller expected.
func newestConfigEvent(t *testing.T, s *Store, project string, wantCount int) *ConfigEvent {
	t.Helper()
	evs, err := s.ListConfigEvents(context.Background(), ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != wantCount {
		var actions []string
		for _, e := range evs {
			actions = append(actions, e.Action)
		}
		t.Fatalf("want %d config events, got %d: %v", wantCount, len(evs), actions)
	}
	return evs[0]
}

// ── Workers: create vs update vs the enabled toggle ─────────────────────────

// A worker upsert logs the most specific action the write represents. The
// distinction matters to a reader of the changelog: "someone switched the tweet
// author off" is a different sentence from "someone rewrote the tweet author".
func TestConfigEvents_WorkerUpsertPicksTheMostSpecificAction(t *testing.T) {
	ctx := context.Background()

	// Each case starts from a worker created in the project (except "create",
	// which asserts the first write itself), then applies one edit.
	tests := []struct {
		name string
		// edit mutates the row as read back from the store; nil means the case
		// only exercises the create.
		edit       func(w *Worker)
		wantAction string
	}{
		{
			name:       "new row is a create",
			wantAction: ActionWorkerCreate,
		},
		{
			name:       "flipping enabled off and nothing else is a disable",
			edit:       func(w *Worker) { w.Enabled = false },
			wantAction: ActionWorkerDisable,
		},
		{
			name:       "a body change is an update",
			edit:       func(w *Worker) { w.Description = "answers customer email" },
			wantAction: ActionWorkerUpdate,
		},
		{
			name: "a prompt rewrite through the whole-object path is an update, not a prompt write",
			// worker_prompt_write requires a rationale (§15.5) and belongs to the
			// dedicated prompt path; a PUT that carries new text is an update.
			edit:       func(w *Worker) { w.SystemPrompt = "Be brief." },
			wantAction: ActionWorkerUpdate,
		},
		{
			name:       "flipping enabled AND changing the body is an update",
			edit:       func(w *Worker) { w.Enabled = false; w.MaxInstances = 3 },
			wantAction: ActionWorkerUpdate,
		},
		{
			name:       "flipping frozen on and nothing else is a freeze",
			edit:       func(w *Worker) { w.Frozen = true },
			wantAction: ActionWorkerFreeze,
		},
		{
			name:       "flipping frozen AND changing the body is an update",
			edit:       func(w *Worker) { w.Frozen = true; w.Description = "now a scorer" },
			wantAction: ActionWorkerUpdate,
		},
		{
			// Neither toggle can claim the write when both flip: neither
			// "froze" nor "disabled" alone would be the honest sentence.
			name:       "flipping frozen AND enabled is an update",
			edit:       func(w *Worker) { w.Frozen = true; w.Enabled = false },
			wantAction: ActionWorkerUpdate,
		},
		{
			name:       "rewriting the same values changes nothing but still logs an update",
			edit:       func(w *Worker) {},
			wantAction: ActionWorkerUpdate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newConfigLogTestStore(t)
			created, err := s.UpsertWorker(ctx, NewWorker("acme", "email-answerer"),
				ConfigWrite{Worker: "manager", Session: "s-1"})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			ev := newestConfigEvent(t, s, "acme", 1)
			if ev.Action != ActionWorkerCreate {
				t.Fatalf("first write: want %q, got %q", ActionWorkerCreate, ev.Action)
			}
			if ev.ActorWorker != "manager" || ev.ActorSession != "s-1" {
				t.Fatalf("actor not threaded: %+v", ev)
			}
			if ev.Payload["name"] != created.Name || ev.Payload["enabled"] != true {
				t.Fatalf("payload is not the full new row: %+v", ev.Payload)
			}
			if tc.edit == nil {
				return
			}

			// Read back before editing: a toggle is a toggle only when every other
			// field is byte-identical, which is what read-modify-write gives.
			next, err := s.GetWorker(ctx, "acme", "email-answerer")
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			tc.edit(next)
			if _, err := s.UpsertWorker(ctx, next, ConfigWrite{Worker: "manager", Session: "s-2"}); err != nil {
				t.Fatalf("second write: %v", err)
			}
			ev = newestConfigEvent(t, s, "acme", 2)
			if ev.Action != tc.wantAction {
				t.Fatalf("second write: want %q, got %q", tc.wantAction, ev.Action)
			}
		})
	}
}

// The enable direction of the toggle, from a worker that starts switched off.
func TestConfigEvents_WorkerEnableIsItsOwnAction(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	off := NewWorker("acme", "tweet-author")
	off.Enabled = false
	if _, err := s.UpsertWorker(ctx, off, ConfigWrite{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	back, err := s.GetWorker(ctx, "acme", "tweet-author")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	back.Enabled = true
	if _, err := s.UpsertWorker(ctx, back, ConfigWrite{}); err != nil {
		t.Fatalf("enable: %v", err)
	}

	ev := newestConfigEvent(t, s, "acme", 2)
	if ev.Action != ActionWorkerEnable {
		t.Fatalf("want %q, got %q", ActionWorkerEnable, ev.Action)
	}
	if ev.Payload["enabled"] != true {
		t.Fatalf("payload must carry the new state: %+v", ev.Payload)
	}
}

// The unfreeze direction of the frozen toggle (F1), from a worker that starts
// frozen — the direction the gorm-default footgun would silently break, since
// `frozen: false` is exactly the zero value a declared default would swallow.
func TestConfigEvents_WorkerFreezeAndUnfreezeAreTheirOwnActions(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	w := NewWorker("acme", "quality-scorer")
	w.Frozen = true
	if _, err := s.UpsertWorker(ctx, w, ConfigWrite{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Creating a worker already frozen is a create, not a freeze.
	if ev := newestConfigEvent(t, s, "acme", 1); ev.Action != ActionWorkerCreate {
		t.Fatalf("want %q, got %q", ActionWorkerCreate, ev.Action)
	}

	back, err := s.GetWorker(ctx, "acme", "quality-scorer")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !back.Frozen {
		t.Fatalf("frozen: true did not persist — the gorm-default trap is back")
	}
	back.Frozen = false
	if _, err := s.UpsertWorker(ctx, back, ConfigWrite{}); err != nil {
		t.Fatalf("unfreeze: %v", err)
	}
	ev := newestConfigEvent(t, s, "acme", 2)
	if ev.Action != ActionWorkerUnfreeze {
		t.Fatalf("want %q, got %q", ActionWorkerUnfreeze, ev.Action)
	}
	if ev.Payload["frozen"] != false {
		t.Fatalf("payload must carry the new state: %+v", ev.Payload)
	}
	thawed, err := s.GetWorker(ctx, "acme", "quality-scorer")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if thawed.Frozen {
		t.Fatalf("frozen: false did not persist — the gorm-default trap is back")
	}
}

// ── Deletes append too (§15.3 rule 2) ───────────────────────────────────────

func TestConfigEvents_DeletesCarryTheFinalState(t *testing.T) {
	ctx := context.Background()

	t.Run("worker", func(t *testing.T) {
		s := newConfigLogTestStore(t)
		w := NewWorker("acme", "archivist")
		w.SystemPrompt = "File everything."
		w.MaxInstances = 2
		if _, err := s.UpsertWorker(ctx, w, ConfigWrite{}); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.DeleteWorker(ctx, "acme", "archivist",
			ConfigWrite{Worker: "manager", Rationale: "the filing job moved to a schedule"}); err != nil {
			t.Fatalf("delete: %v", err)
		}

		ev := newestConfigEvent(t, s, "acme", 2)
		if ev.Action != ActionWorkerDelete {
			t.Fatalf("want %q, got %q", ActionWorkerDelete, ev.Action)
		}
		// The projection row is gone; the record is not, and it holds the worker
		// as it last stood — which is what makes a restore a lookup (§15.7).
		if _, err := s.GetWorker(ctx, "acme", "archivist"); err == nil {
			t.Fatalf("projection row survived the delete")
		}
		if ev.Payload["system_prompt"] != "File everything." || ev.Payload["max_instances"] != float64(2) {
			t.Fatalf("delete payload is not the final state: %+v", ev.Payload)
		}
		if ev.Rationale != "the filing job moved to a schedule" {
			t.Fatalf("rationale not stored: %q", ev.Rationale)
		}
	})

	t.Run("subscription", func(t *testing.T) {
		s := newConfigLogTestStore(t)
		sub, err := s.CreateSubscription(ctx, &Subscription{
			Project: "acme", EventType: "email.received", Worker: "email-answerer",
			Filter: JSONMap{"interactive": false}, Enabled: true,
		}, ConfigWrite{})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.DeleteSubscription(ctx, "acme", sub.ID, ConfigWrite{}); err != nil {
			t.Fatalf("delete: %v", err)
		}

		ev := newestConfigEvent(t, s, "acme", 2)
		if ev.Action != ActionSubscriptionDelete {
			t.Fatalf("want %q, got %q", ActionSubscriptionDelete, ev.Action)
		}
		if ev.Payload["id"] != sub.ID || ev.Payload["event_type"] != "email.received" ||
			ev.Payload["worker"] != "email-answerer" {
			t.Fatalf("delete payload is not the final state: %+v", ev.Payload)
		}
		if _, err := s.GetSubscription(ctx, "acme", sub.ID); err == nil {
			t.Fatalf("projection row survived the delete")
		}
	})

	// Deleting something that is not there logs nothing at all: there is no
	// final state to record, and the log must not grow a phantom deletion.
	t.Run("missing rows log nothing", func(t *testing.T) {
		s := newConfigLogTestStore(t)
		if err := s.DeleteWorker(ctx, "acme", "nobody", ConfigWrite{}); err == nil {
			t.Fatalf("expected a not-found error")
		}
		if err := s.DeleteSubscription(ctx, "acme", "sub-nope", ConfigWrite{}); err == nil {
			t.Fatalf("expected a not-found error")
		}
		evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(evs) != 0 {
			t.Fatalf("a failed delete wrote a record: %+v", evs)
		}
	})
}

// ── Subscriptions and settings ──────────────────────────────────────────────

func TestConfigEvents_SubscriptionLifecycleActions(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	sub, err := s.CreateSubscription(ctx, &Subscription{
		Project: "acme", EventType: "worker.finished", Worker: "email-reviewer", Enabled: true,
	}, ConfigWrite{Worker: "manager", Session: "s-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ev := newestConfigEvent(t, s, "acme", 1); ev.Action != ActionSubscriptionCreate {
		t.Fatalf("create: want %q, got %q", ActionSubscriptionCreate, ev.Action)
	}

	sub.MaxFiringsPerHour = 12
	if _, err := s.UpdateSubscription(ctx, sub, ConfigWrite{Worker: "manager", Session: "s-2"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	ev := newestConfigEvent(t, s, "acme", 2)
	if ev.Action != ActionSubscriptionUpdate {
		t.Fatalf("update: want %q, got %q", ActionSubscriptionUpdate, ev.Action)
	}
	// Full state, not a diff: the unchanged fields are in the payload too.
	if ev.Payload["max_firings_per_hour"] != float64(12) ||
		ev.Payload["event_type"] != "worker.finished" || ev.Payload["worker"] != "email-reviewer" {
		t.Fatalf("payload is a diff, not full state: %+v", ev.Payload)
	}

	// Disabling a subscription is an ordinary update: unlike workers, §15.3
	// gives subscriptions no enable/disable verbs.
	sub.Enabled = false
	if _, err := s.UpdateSubscription(ctx, sub, ConfigWrite{}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if ev := newestConfigEvent(t, s, "acme", 3); ev.Action != ActionSubscriptionUpdate {
		t.Fatalf("disable: want %q, got %q", ActionSubscriptionUpdate, ev.Action)
	}
}

func TestConfigEvents_ProjectSettingsPutLogsBothBranches(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	// First write creates the row lazily…
	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
		Project: "acme", SystemPrompt: "first", DailyTokensHard: 500,
	}, ConfigWrite{Worker: "manager", Session: "s-1"}); err != nil {
		t.Fatalf("first put: %v", err)
	}
	ev := newestConfigEvent(t, s, "acme", 1)
	if ev.Action != ActionProjectSettingsPut {
		t.Fatalf("want %q, got %q", ActionProjectSettingsPut, ev.Action)
	}
	if ev.Payload["system_prompt"] != "first" || ev.Payload["daily_tokens_hard"] != float64(500) {
		t.Fatalf("payload: %+v", ev.Payload)
	}

	// …and the second replaces it, under the same action: §5's settings row has
	// no create/update distinction, it is always written whole.
	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
		Project: "acme", SystemPrompt: "second",
	}, ConfigWrite{}); err != nil {
		t.Fatalf("second put: %v", err)
	}
	ev = newestConfigEvent(t, s, "acme", 2)
	if ev.Action != ActionProjectSettingsPut {
		t.Fatalf("want %q, got %q", ActionProjectSettingsPut, ev.Action)
	}
	// Whole-object replace: the dropped budget is absent from the new state too.
	if ev.Payload["system_prompt"] != "second" || ev.Payload["daily_tokens_hard"] != float64(0) {
		t.Fatalf("payload must be the full row after the write: %+v", ev.Payload)
	}
	// An empty actor is the recorded fact for a human/API edit (§15.2).
	if ev.ActorWorker != "" || ev.ActorSession != "" {
		t.Fatalf("human edit must log no actor: %+v", ev)
	}
}

// ── Rollback: neither row, in both directions ───────────────────────────────

func TestConfigEvents_FailedMutationWritesNeitherRow(t *testing.T) {
	ctx := context.Background()

	// The projection write fails: a create against an id that already exists.
	t.Run("projection write fails", func(t *testing.T) {
		s := newConfigLogTestStore(t)
		if _, err := s.CreateSubscription(ctx, &Subscription{
			ID: "sub-fixed", Project: "acme", EventType: "email.received", Worker: "answerer", Enabled: true,
		}, ConfigWrite{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := s.CreateSubscription(ctx, &Subscription{
			ID: "sub-fixed", Project: "acme", EventType: "email.*", Worker: "reviewer", Enabled: true,
		}, ConfigWrite{}); err == nil {
			t.Fatalf("expected the duplicate id to fail")
		}

		// Exactly the seed's record, and the stored row is untouched.
		ev := newestConfigEvent(t, s, "acme", 1)
		if ev.Payload["worker"] != "answerer" {
			t.Fatalf("the rolled-back write left a record: %+v", ev.Payload)
		}
		got, err := s.GetSubscription(ctx, "acme", "sub-fixed")
		if err != nil || got.Worker != "answerer" {
			t.Fatalf("projection row was modified by a rolled-back write: %+v err=%v", got, err)
		}
	})

	// The log write fails: with no config_events table the append cannot land,
	// and the projection row must not survive on its own. This is the direction
	// that would otherwise leave the log quietly incomplete.
	t.Run("log write fails", func(t *testing.T) {
		s := newConfigLogTestStore(t)
		if err := s.gdb.Migrator().DropTable(&ConfigEvent{}); err != nil {
			t.Fatalf("drop config_events: %v", err)
		}
		if _, err := s.UpsertWorker(ctx, NewWorker("acme", "email-answerer"), ConfigWrite{}); err == nil {
			t.Fatalf("expected the unloggable write to fail")
		}
		if _, err := s.GetWorker(ctx, "acme", "email-answerer"); err == nil {
			t.Fatalf("a worker was created that the log knows nothing about")
		}
	})
}
