package agentdb

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestPromptWrites_LivePG proves the two E4 store methods against real Postgres:
// the narrow UPDATE really is narrow, the jsonb payload really round-trips, and
// each write takes exactly one sequence in the project's config log.
//
// The sqlite tests cover the semantics; this covers the things sqlite cannot —
// jsonb, and the real UPDATE ... RETURNING path gorm takes on pg.
func TestPromptWrites_LivePG(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		_ = s.DB().Exec("DELETE FROM workers WHERE project = ?", project).Error
		_ = s.DB().Exec("DELETE FROM project_settings WHERE project = ?", project).Error
		_ = s.PurgeConfigEvents(context.Background(), project)
	})

	w := NewWorker(project, "email-answerer")
	w.Description = "answers customer email"
	w.SystemPrompt = "Answer customer email."
	w.Image = "toolbox:2"
	w.MaxInstances = 3
	w.Briefing = SelectorList{"kind=house-style"}
	w.MCPConfig = JSONMap{"crm": map[string]any{"url": "https://crm.example.com"}}
	if _, err := s.UpsertWorker(ctx, w, ConfigWrite{}); err != nil {
		t.Fatalf("seed worker: %v", err)
	}

	stored, previous, err := s.SetWorkerPrompt(ctx, project, "email-answerer",
		"Answer customer email. Acknowledge frustration first.",
		ConfigWrite{Worker: "reviewer", Session: "sess-live", Rationale: "a hundred curt threads"})
	if err != nil {
		t.Fatalf("SetWorkerPrompt: %v", err)
	}
	if previous != "Answer customer email." {
		t.Fatalf("previous = %q", previous)
	}
	// Everything else survived the narrow UPDATE, jsonb columns included.
	if stored.Image != "toolbox:2" || stored.MaxInstances != 3 ||
		len(stored.Briefing) != 1 || len(stored.MCPConfig) != 1 {
		t.Fatalf("a prompt write disturbed another column: %+v", stored)
	}

	if _, _, err := s.SetProjectPrompt(ctx, project, "We are BadCode.",
		ConfigWrite{Worker: "reviewer", Session: "sess-live", Rationale: "founding document"}); err != nil {
		t.Fatalf("SetProjectPrompt: %v", err)
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// worker_create (the seed) + worker_prompt_write + project_prompt_write, each
	// with its own sequence — seq order is commit order (J2).
	if len(evs) != 3 {
		t.Fatalf("want 3 config events, got %d", len(evs))
	}
	if evs[0].Action != ActionProjectPromptWrite || evs[1].Action != ActionWorkerPromptWrite {
		t.Fatalf("newest-first order = %q, %q", evs[0].Action, evs[1].Action)
	}
	if evs[0].Seq != 3 || evs[1].Seq != 2 {
		t.Fatalf("sequences = %d, %d", evs[0].Seq, evs[1].Seq)
	}
	if got, _ := evs[0].Payload["system_prompt"].(string); got != "We are BadCode." {
		t.Fatalf("project payload did not survive jsonb: %v", evs[0].Payload)
	}
	if got, _ := evs[1].Payload["system_prompt"].(string); !strings.Contains(got, "Acknowledge frustration") {
		t.Fatalf("worker payload did not survive jsonb: %v", evs[1].Payload)
	}
	if evs[1].Payload["image"] != "toolbox:2" {
		t.Fatalf("the worker payload must be the WHOLE row (§15.3): %v", evs[1].Payload)
	}

	// The fold reproduces both from the log alone (§15.6).
	snap, err := s.FoldTo(ctx, project, 0)
	if err != nil {
		t.Fatalf("FoldTo: %v", err)
	}
	folded, ok := snap.Worker("email-answerer")
	if !ok {
		t.Fatalf("the worker did not fold")
	}
	if got, _ := folded.Payload()["system_prompt"].(string); !strings.Contains(got, "Acknowledge frustration") {
		t.Fatalf("the fold disagrees with the projection: %v", folded.Payload())
	}
	if _, ok := snap.Get(EntityRef{Kind: EntityProjectPrompt}); !ok {
		t.Fatalf("the project prompt did not fold")
	}
}
