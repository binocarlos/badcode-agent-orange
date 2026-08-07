package agentdb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// E4 — the two dedicated prompt-write store methods (§9, §15.3, §15.5).
//
// They exist because a prompt rewrite is not an ordinary update: it is the ONLY
// writer of `worker_prompt_write` / `project_prompt_write`, its rationale is
// mandatory, and it must change the prompt and nothing else.
// ---------------------------------------------------------------------------

func newPromptTestStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.gdb.AutoMigrate(&ConfigEvent{}, &Worker{}, &ProjectSettings{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return s
}

func seedWorkerForPrompt(t *testing.T, s *Store) *Worker {
	t.Helper()
	w := NewWorker("acme", "email-answerer")
	w.Description = "answers customer email"
	w.SystemPrompt = "Answer customer email."
	w.Image = "toolbox:2"
	w.MaxInstances = 3
	stored, err := s.UpsertWorker(context.Background(), w, ConfigWrite{})
	if err != nil {
		t.Fatalf("seed worker: %v", err)
	}
	return stored
}

// TestSetWorkerPrompt is §8.7's mechanism: the prompt is replaced wholesale, the
// superseded text comes back for the prompt-revision memory, and the config log
// records `worker_prompt_write` with the rationale — and NOTHING else moves.
func TestSetWorkerPrompt(t *testing.T) {
	s := newPromptTestStore(t)
	ctx := context.Background()
	before := seedWorkerForPrompt(t, s)

	stored, previous, err := s.SetWorkerPrompt(ctx, "acme", "email-answerer",
		"Answer customer email. Acknowledge frustration first.",
		ConfigWrite{Worker: "email-review-consultant", Session: "sess-9",
			Rationale: "a hundred curt threads"})
	if err != nil {
		t.Fatalf("SetWorkerPrompt: %v", err)
	}
	if previous != "Answer customer email." {
		t.Fatalf("previous prompt = %q — this is what the revision memory records (§9)", previous)
	}
	if !strings.Contains(stored.SystemPrompt, "Acknowledge frustration") {
		t.Fatalf("the prompt was not replaced: %q", stored.SystemPrompt)
	}
	// The one mutation a worker performs on another worker must have no side
	// effects (§8.7).
	if stored.Description != before.Description || stored.Image != before.Image ||
		stored.MaxInstances != before.MaxInstances || stored.Enabled != before.Enabled {
		t.Fatalf("a prompt write changed something else:\nbefore %+v\nafter  %+v", before, stored)
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme", Action: ActionWorkerPromptWrite})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("want exactly one worker_prompt_write event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Rationale != "a hundred curt threads" {
		t.Fatalf("rationale = %q (§15.5 stores it in the event)", ev.Rationale)
	}
	if ev.ActorWorker != "email-review-consultant" || ev.ActorSession != "sess-9" {
		t.Fatalf("actor = %s/%s, want the rewriting worker and its session (§15.2)", ev.ActorWorker, ev.ActorSession)
	}
	// §15.3: the payload is the WHOLE worker row, new prompt included in full.
	if ev.Payload["name"] != "email-answerer" {
		t.Fatalf("payload must be the whole worker row, got %v", ev.Payload)
	}
	if got, _ := ev.Payload["system_prompt"].(string); !strings.Contains(got, "Acknowledge frustration") {
		t.Fatalf("payload must carry the NEW prompt in full (§15.3), got %q", got)
	}
	// And it folds — the fold keys workers by payload name (§15.6).
	ref, err := EntityRefFor(ev)
	if err != nil {
		t.Fatalf("the event must be foldable: %v", err)
	}
	if ref.Kind != EntityWorker || ref.Key != "email-answerer" {
		t.Fatalf("fold key = %v", ref)
	}
}

// A missing rationale writes NEITHER row: the seam refuses the action before the
// transaction opens (§15.5).
func TestSetWorkerPromptRequiresARationale(t *testing.T) {
	s := newPromptTestStore(t)
	ctx := context.Background()
	seedWorkerForPrompt(t, s)

	for _, rationale := range []string{"", "   "} {
		if _, _, err := s.SetWorkerPrompt(ctx, "acme", "email-answerer", "Be curt.",
			ConfigWrite{Rationale: rationale}); err == nil {
			t.Fatalf("want a refusal for rationale %q", rationale)
		}
		w, err := s.GetWorker(ctx, "acme", "email-answerer")
		if err != nil {
			t.Fatalf("get worker: %v", err)
		}
		if w.SystemPrompt != "Answer customer email." {
			t.Fatalf("the prompt was written without a rationale: %q", w.SystemPrompt)
		}
	}
	evs, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme", Action: ActionWorkerPromptWrite})
	if len(evs) != 0 {
		t.Fatalf("a refused prompt write must append nothing, got %d events", len(evs))
	}
}

func TestSetWorkerPromptRefusals(t *testing.T) {
	s := newPromptTestStore(t)
	ctx := context.Background()
	seedWorkerForPrompt(t, s)

	cases := []struct {
		name                    string
		project, worker, prompt string
		want                    string
	}{
		{"no project", "", "email-answerer", "p", "project is required"},
		{"no name", "acme", "", "p", "name is required"},
		{"blank prompt", "acme", "email-answerer", "   ", "must not be blank"},
		{"unknown worker", "acme", "nobody", "p", "worker not found"},
		{"another project's worker", "globex", "email-answerer", "p", "worker not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.SetWorkerPrompt(ctx, tc.project, tc.worker, tc.prompt, ConfigWrite{Rationale: "why"})
			if err == nil {
				t.Fatalf("want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
		})
	}
	// Project isolation is not a filter applied afterwards: nothing was written
	// into the other project either.
	if evs, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "globex"}); len(evs) != 0 {
		t.Fatalf("a cross-project write leaked %d events", len(evs))
	}
}

// The write guard proves the dual write really is one transaction: a rolled-back
// mutation leaves neither row.
func TestSetWorkerPromptDualWriteRollsBackTogether(t *testing.T) {
	s := newPromptTestStore(t)
	ctx := context.Background()
	seedWorkerForPrompt(t, s)
	if err := InstallConfigEventGuard(s.gdb); err != nil {
		t.Fatalf("install guard: %v", err)
	}

	if _, _, err := s.SetWorkerPrompt(ctx, "acme", "email-answerer", "Be warmer.",
		ConfigWrite{Rationale: "warmth"}); err != nil {
		t.Fatalf("the guarded write must succeed — it goes through the seam: %v", err)
	}

	// And a write that skips the seam is rejected, so the check above is not
	// vacuous.
	err := s.gdb.WithContext(ctx).Model(&Worker{}).
		Where("project = ? AND name = ?", "acme", "email-answerer").
		Update("system_prompt", "sneaked in").Error
	if err == nil || !strings.Contains(err.Error(), "outside a config-event transaction") {
		t.Fatalf("the guard did not reject an unlogged prompt write: %v", err)
	}
}

// TestSetProjectPrompt is the project-level twin, including the lazy creation of
// §5's settings row on the first write.
func TestSetProjectPrompt(t *testing.T) {
	s := newPromptTestStore(t)
	ctx := context.Background()

	// First write: the settings row does not exist yet.
	stored, previous, err := s.SetProjectPrompt(ctx, "acme", "We are BadCode.",
		ConfigWrite{Worker: "manager", Session: "sess-3", Rationale: "founding document"})
	if err != nil {
		t.Fatalf("SetProjectPrompt: %v", err)
	}
	if previous != "" {
		t.Fatalf("nothing was superseded on the first write, got %q", previous)
	}
	if stored.SystemPrompt != "We are BadCode." {
		t.Fatalf("prompt = %q", stored.SystemPrompt)
	}
	// The lazily created row carries the §5 defaults, not zeroes — a row of
	// zeroes would read as "max_concurrent_jobs 0".
	if stored.MaxConcurrentJobs != DefaultMaxConcurrentJobs || stored.BriefingMaxBytes != DefaultBriefingMaxBytes {
		t.Fatalf("the lazily created settings row must carry the spec defaults: %+v", stored)
	}

	// Second write: supersedes, and changes nothing else.
	stored.DailyTokensHard = 500000
	if _, err := s.PutProjectSettings(ctx, stored, ConfigWrite{}); err != nil {
		t.Fatalf("put settings: %v", err)
	}
	next, previous, err := s.SetProjectPrompt(ctx, "acme", "We are BadCode. Write in plain English.",
		ConfigWrite{Rationale: "house style"})
	if err != nil {
		t.Fatalf("SetProjectPrompt: %v", err)
	}
	if previous != "We are BadCode." {
		t.Fatalf("previous = %q", previous)
	}
	if next.DailyTokensHard != 500000 {
		t.Fatalf("a prompt write clobbered another setting: %+v", next)
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme", Action: ActionProjectPromptWrite})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("want two project_prompt_write events, got %d", len(evs))
	}
	// F1's decision, pinned: the payload key is `system_prompt`, matching the
	// worker and settings rows — the UI accepts several only because this was
	// unspecified.
	newest := evs[0]
	if got, _ := newest.Payload["system_prompt"].(string); got != "We are BadCode. Write in plain English." {
		t.Fatalf("payload key must be system_prompt with the new prompt, got %v", newest.Payload)
	}
	if newest.Rationale != "house style" {
		t.Fatalf("rationale = %q", newest.Rationale)
	}
	// It folds as the project-prompt singleton (§15.6).
	ref, err := EntityRefFor(newest)
	if err != nil {
		t.Fatalf("the event must be foldable: %v", err)
	}
	if ref.Kind != EntityProjectPrompt || ref.Key != "" {
		t.Fatalf("fold key = %v, want the project-prompt singleton", ref)
	}
}

func TestSetProjectPromptRefusals(t *testing.T) {
	s := newPromptTestStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name             string
		project, prompt  string
		rationale        string
		want             string
		wantInvalidError bool
	}{
		{"no project", "", "p", "why", "project is required", true},
		{"blank prompt", "acme", "  ", "why", "must not be blank", true},
		{"no rationale", "acme", "p", "", "requires a non-empty rationale", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.SetProjectPrompt(ctx, tc.project, tc.prompt, ConfigWrite{Rationale: tc.rationale})
			if err == nil {
				t.Fatalf("want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
			if tc.wantInvalidError && !errors.Is(err, ErrInvalidProjectSettings) {
				t.Fatalf("caller mistakes must be ErrInvalidProjectSettings so HTTP can answer 400: %v", err)
			}
		})
	}
	// Nothing was created by any of them.
	var rows int64
	if err := s.gdb.WithContext(ctx).Model(&ProjectSettings{}).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Fatalf("a refused prompt write created %d settings rows", rows)
	}
}

// UpsertWorker must never be able to write `worker_prompt_write`: that action
// requires a rationale, and a PUT carrying a different prompt is an ordinary
// update. This is the invariant that makes the dedicated method necessary.
func TestUpsertWorkerNeverWritesPromptWriteAction(t *testing.T) {
	s := newPromptTestStore(t)
	ctx := context.Background()
	w := seedWorkerForPrompt(t, s)

	w.SystemPrompt = "Something completely different."
	if _, err := s.UpsertWorker(ctx, w, ConfigWrite{}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, ev := range evs {
		if ev.Action == ActionWorkerPromptWrite {
			t.Fatalf("UpsertWorker wrote %q — that action belongs to SetWorkerPrompt alone", ev.Action)
		}
	}
	if evs[0].Action != ActionWorkerUpdate {
		t.Fatalf("a whole-object write carrying a new prompt is an update, got %q", evs[0].Action)
	}
}

// A concurrent delete must roll the whole thing back rather than log a rewrite
// of a worker that no longer exists.
func TestSetWorkerPromptRollsBackWhenTheWorkerVanishes(t *testing.T) {
	s := newPromptTestStore(t)
	ctx := context.Background()
	seedWorkerForPrompt(t, s)

	// Delete the row out from under the read, the way a racing DeleteWorker would.
	deleted := false
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		deleted = true
		return tx.Exec(`DELETE FROM workers WHERE project = ? AND name = ?`, "acme", "email-answerer").Error
	})
	if err != nil || !deleted {
		t.Fatalf("seed the race: %v", err)
	}
	if _, _, err := s.SetWorkerPrompt(ctx, "acme", "email-answerer", "Be warmer.",
		ConfigWrite{Rationale: "warmth"}); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("want ErrWorkerNotFound, got %v", err)
	}
	evs, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme", Action: ActionWorkerPromptWrite})
	if len(evs) != 0 {
		t.Fatalf("a rewrite of a vanished worker must append nothing, got %d", len(evs))
	}
}
