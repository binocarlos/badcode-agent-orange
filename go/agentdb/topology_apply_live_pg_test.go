package agentdb

// topology_apply_live_pg_test.go — T2's apply against real Postgres: the parts
// sqlite cannot honestly prove. Memory seeds (the memories table is
// Postgres-only), the jsonb answers round-trip, real nested-transaction
// savepoints under gorm's postgres driver, and seq allocation inside one outer
// transaction. Skips without AGENTKIT_TEST_POSTGRES_URL, like every live test.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestApplyTopology_LivePG(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := "topo-" + uuid.New().String()
	t.Cleanup(func() {
		for _, stmt := range []string{
			"DELETE FROM workers WHERE project = ?",
			"DELETE FROM subscriptions WHERE project = ?",
			"DELETE FROM schedules WHERE project = ?",
			"DELETE FROM project_settings WHERE project = ?",
			"DELETE FROM memories WHERE project = ?",
			"DELETE FROM agent_custom_images WHERE customer = ?",
			"DELETE FROM agent_skills WHERE customer = ?",
		} {
			_ = s.DB().Exec(stmt, project).Error
		}
		// config_events is append-only in the database (migration 039), so its
		// rows come out through the one sanctioned purge, not a raw DELETE.
		_ = s.PurgeConfigEvents(context.Background(), project)
	})

	// Teach the project the required assets first (D2: apply never creates them).
	if _, err := s.CreateCustomImage(ctx, &CustomImage{
		Name: "toolbox", Customer: project, RegistryHandle: `{"kind":"blob-archive","ref":"live"}`,
	}, ConfigWrite{}); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	if _, err := s.CreateSkill(ctx, &Skill{
		Name: "graph-gen", Customer: project, Markdown: "# graphs",
	}, ConfigWrite{}); err != nil {
		t.Fatalf("seed skill: %v", err)
	}

	app := func() TopologyApplication {
		w := NewWorker("", "live-actor")
		w.SystemPrompt = "Do the work."
		w.Image = "toolbox"
		return TopologyApplication{
			Project:  project,
			Topology: "live@v1",
			Answers:  JSONMap{"worker-name": "live-actor", "strict": true},
			Workers:  []*Worker{w},
			Subscriptions: []*Subscription{{
				EventType: "worker.finished", Worker: "live-actor", Enabled: true,
			}},
			Schedules: []*Schedule{{
				Worker: "live-actor", Cron: "0 9 * * *", Input: "morning run", Enabled: true,
			}},
			SettingsPatch:  &ProjectSettings{Project: project, MaxConcurrentJobs: 2},
			MemorySeeds:    []*Memory{{Content: "House style: plain English.", Labels: LabelSet{"kind": "seed"}}},
			RequiredImages: []string{"toolbox"},
			RequiredSkills: []string{"graph-gen"},
		}
	}

	res, err := s.ApplyTopology(ctx, app(), ConfigWrite{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Memories) != 1 || res.Memories[0].ID == "" {
		t.Fatalf("memory seed not stored: %+v", res.Memories)
	}
	if got, err := s.GetMemory(ctx, project, res.Memories[0].ID); err != nil || got.Content != "House style: plain English." {
		t.Fatalf("memory read-back: %+v, %v", got, err)
	}
	if res.Settings == nil || res.Settings.MaxConcurrentJobs != 2 {
		t.Fatalf("settings patch: %+v", res.Settings)
	}

	// The log: image_create + skill_create (the seeds) then the apply's four
	// row events and the bracket, seqs strictly increasing, bracket highest.
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != 7 {
		t.Fatalf("want 7 config events, got %d", len(evs))
	}
	if evs[0].Action != ActionTopologyApply || evs[0].Seq != 7 {
		t.Fatalf("newest = %q seq %d, want topology_apply seq 7", evs[0].Action, evs[0].Seq)
	}
	// jsonb round-trip of the answers, exactly as written.
	answers, _ := evs[0].Payload["answers"].(map[string]any)
	if answers["worker-name"] != "live-actor" || answers["strict"] != true {
		t.Fatalf("answers did not survive jsonb: %v", evs[0].Payload["answers"])
	}
	// The bracket is filterable by its entity.
	byEntity, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project, Entity: "topology:live@v1"})
	if err != nil || len(byEntity) != 1 {
		t.Fatalf("entity filter: %v, %v", byEntity, err)
	}

	// A second apply of the same bundle collides — and changes nothing.
	if _, err := s.ApplyTopology(ctx, app(), ConfigWrite{}); !errors.Is(err, ErrTopologyNameCollision) {
		t.Fatalf("want collision on re-apply, got %v", err)
	}
	after, _ := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if len(after) != 7 {
		t.Fatalf("refused re-apply appended events: %d", len(after))
	}
	mems, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: project})
	if err != nil || len(mems) != 1 {
		t.Fatalf("refused re-apply touched memories: %v, %v", mems, err)
	}
}
