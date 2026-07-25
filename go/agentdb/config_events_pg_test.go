package agentdb

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// liveConfigLogProject returns a per-run unique project and registers cleanup —
// config_events has no FK to hang a cascade off, so rows are removed explicitly.
func liveConfigLogProject(t *testing.T, s *Store) string {
	t.Helper()
	project := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		_ = s.DB().Exec("DELETE FROM config_events WHERE project = ?", project).Error
	})
	return project
}

// TestConfigEvents_LivePG_Migration026 pins the Postgres-only shape of the
// table: migration 026 applied, the project index present, and jsonb payloads
// that survive a round trip and answer a jsonb query.
func TestConfigEvents_LivePG_Migration026(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveConfigLogProject(t, s)

	var applied int64
	if err := s.DB().Raw(
		"SELECT count(*) FROM agentdb_migrations WHERE name = ?", "026_config_events",
	).Scan(&applied).Error; err != nil {
		t.Fatalf("read migration table: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration 026_config_events not recorded as applied")
	}

	for _, idx := range []string{
		"idx_config_events_project",
		"idx_config_events_project_created",
		"idx_config_events_project_action",
	} {
		var n int64
		if err := s.DB().Raw(
			"SELECT count(*) FROM pg_indexes WHERE tablename = 'config_events' AND indexname = ?", idx,
		).Scan(&n).Error; err != nil {
			t.Fatalf("read pg_indexes: %v", err)
		}
		if n != 1 {
			t.Fatalf("index %s missing on config_events", idx)
		}
	}

	// A full-state payload with nested structure round-trips through jsonb…
	ev, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: project,
		Action:  ActionWorkerPromptWrite,
		Payload: JSONMap{
			"name":          "email-answerer",
			"system_prompt": "Answer customer email. Be brief.",
			"briefing":      []any{"kind=rolling-summary", "kind=policy"},
			"enabled":       true,
			"max_instances": 2,
		},
		Write: ConfigWrite{Worker: "email-reviewer", Session: "s-991",
			Rationale: "three customers called the answers walls of text"},
	}, func(tx *gorm.DB) error { return nil })
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != ev.ID {
		t.Fatalf("expected the one record back, got %+v", got)
	}
	if got[0].Payload["system_prompt"] != "Answer customer email. Be brief." {
		t.Fatalf("payload did not round-trip: %+v", got[0].Payload)
	}
	if got[0].Payload["enabled"] != true {
		t.Fatalf("payload booleans must survive jsonb: %+v", got[0].Payload)
	}
	if b, ok := got[0].Payload["briefing"].([]any); !ok || len(b) != 2 {
		t.Fatalf("payload arrays must survive jsonb: %+v", got[0].Payload)
	}

	// …and the column really is jsonb, not text: a jsonb accessor works.
	var name string
	if err := s.DB().Raw(
		"SELECT payload->>'name' FROM config_events WHERE id = ?", ev.ID,
	).Scan(&name).Error; err != nil {
		t.Fatalf("jsonb accessor: %v", err)
	}
	if name != "email-answerer" {
		t.Fatalf("payload->>'name' = %q", name)
	}
}

// TestConfigEvents_LivePG_ProjectIsolation is the §12 negative test against the
// real SQL: a query scoped to one project never returns another's history.
func TestConfigEvents_LivePG_ProjectIsolation(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	mine := liveConfigLogProject(t, s)
	theirs := liveConfigLogProject(t, s)

	for _, p := range []string{mine, theirs} {
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: p, Action: ActionWorkerCreate,
			Payload: JSONMap{"name": "answerer", "project": p},
			Write:   ConfigWrite{Worker: "manager"},
		}, func(tx *gorm.DB) error { return nil }); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	got, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: mine, ActorWorker: "manager", Action: "worker_*"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly my project's record, got %d", len(got))
	}
	if got[0].Project != mine || got[0].Payload["project"] != mine {
		t.Fatalf("cross-project leak: %+v", got[0])
	}
}

// TestConfigEvents_LivePG_RollbackWritesNeither proves the dual write is atomic
// on a real transactional engine, not just on sqlite.
func TestConfigEvents_LivePG_RollbackWritesNeither(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveConfigLogProject(t, s)

	marker := "sk-" + uuid.New().String()
	t.Cleanup(func() { _ = s.DB().Exec("DELETE FROM agent_skills WHERE id = ?", marker).Error })

	_, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: project, Action: ActionSkillCreate,
		Payload: JSONMap{"id": marker, "name": "doomed"},
	}, func(tx *gorm.DB) error {
		if err := tx.Create(&Skill{
			ID: marker, Name: "doomed", Visibility: "organizational", Customer: project,
		}).Error; err != nil {
			return err
		}
		// A later step of the same mutation fails: neither row may survive.
		return gorm.ErrInvalidData
	})
	if err == nil {
		t.Fatalf("expected the mutation to fail")
	}

	var skills int64
	if err := s.DB().Raw("SELECT count(*) FROM agent_skills WHERE id = ?", marker).Scan(&skills).Error; err != nil {
		t.Fatalf("count skills: %v", err)
	}
	if skills != 0 {
		t.Fatalf("projection row survived a rolled-back transaction")
	}
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("config event survived a rolled-back transaction: %+v", evs)
	}
}
