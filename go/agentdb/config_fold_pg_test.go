package agentdb

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// The Postgres-only half of J2 + B4: migration 027's shape, the deterministic
// backfill, and the unique index that makes sequence allocation correct under
// concurrency. sqlite gets the same columns from AutoMigrate, but only the real
// migration proves what a live database ends up with.

func TestConfigFold_LivePG_Migration027(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveConfigLogProject(t, s)

	var applied int64
	if err := s.DB().Raw(
		"SELECT count(*) FROM agentdb_migrations WHERE name = ?", "027_config_event_seq_and_snapshot_ttl",
	).Scan(&applied).Error; err != nil {
		t.Fatalf("read migration table: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration 027 not recorded as applied")
	}

	var n int64
	if err := s.DB().Raw(
		"SELECT count(*) FROM pg_indexes WHERE tablename = 'config_events' AND indexname = ?",
		"idx_config_events_project_seq",
	).Scan(&n).Error; err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	if n != 1 {
		t.Fatalf("the (project, seq) unique index is what makes allocation correct — it is missing")
	}

	// Every row in the table carries a sequence: the backfill left none at 0.
	var unsequenced int64
	if err := s.DB().Raw("SELECT count(*) FROM config_events WHERE seq = 0").Scan(&unsequenced).Error; err != nil {
		t.Fatalf("count unsequenced: %v", err)
	}
	if unsequenced != 0 {
		t.Fatalf("%d config events have no sequence — the 027 backfill did not cover them", unsequenced)
	}

	// A fresh project allocates 1, 2, 3 … and the fold agrees with the writes.
	for i := 1; i <= 3; i++ {
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: project,
			Action:  ActionProjectPromptWrite,
			Payload: JSONMap{"system_prompt": "revision " + uuid.New().String()[:4]},
			Write:   ConfigWrite{Rationale: "live pg"},
		}, func(tx *gorm.DB) error { return nil }); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(evs))
	}
	for i, ev := range evs {
		if want := int64(3 - i); ev.Seq != want {
			t.Fatalf("event %d: seq %d, want %d (newest first)", i, ev.Seq, want)
		}
	}

	// The unique index really is unique — a duplicate seq cannot be forced in.
	dup := &ConfigEvent{
		ID: uuid.New().String(), Project: project, Seq: 1, Action: ActionProjectPromptWrite,
		Payload: JSONMap{"system_prompt": "clash"}, CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.DB().Create(dup).Error; err == nil {
		t.Fatalf("two events shared a project sequence — allocation is not protected")
	}

	// And the fold reads the singleton back.
	snap, err := s.FoldTo(ctx, project, 0)
	if err != nil {
		t.Fatalf("FoldTo: %v", err)
	}
	if _, ok := snap.Get(EntityRef{Kind: EntityProjectPrompt}); !ok {
		t.Fatalf("the project prompt did not fold")
	}
	if snap.Folded != 3 {
		t.Fatalf("folded %d events, want 3", snap.Folded)
	}
}

func TestSnapshotTTL_LivePG_Migration027Columns(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		_ = s.DB().Exec("DELETE FROM agent_custom_images WHERE customer = ?", project).Error
		_ = s.PurgeConfigEvents(context.Background(), project)
		_ = s.DB().Exec("DELETE FROM project_settings WHERE project = ?", project).Error
	})

	for _, col := range []string{"expires_at", "last_resumed_at"} {
		var n int64
		if err := s.DB().Raw(
			"SELECT count(*) FROM information_schema.columns WHERE table_name = 'agent_custom_images' AND column_name = ?", col,
		).Scan(&n).Error; err != nil {
			t.Fatalf("read information_schema: %v", err)
		}
		if n != 1 {
			t.Fatalf("agent_custom_images.%s is missing — the §5 metadata tuple is incomplete", col)
		}
	}

	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{Project: project, SnapshotTTLDays: 7}, ConfigWrite{}); err != nil {
		t.Fatalf("put settings: %v", err)
	}
	ci, err := s.CreateCustomImage(ctx, &CustomImage{
		Name: "toolbox", Customer: project, RegistryHandle: `{"kind":"blob-archive","ref":"b1"}`,
	}, ConfigWrite{Worker: "curator", Session: "s-1"})
	if err != nil {
		t.Fatalf("burn: %v", err)
	}
	if ci.ExpiresAt != ci.CreatedAt+7*SecondsPerDay {
		t.Fatalf("expires_at = %d, want %d", ci.ExpiresAt, ci.CreatedAt+7*SecondsPerDay)
	}

	// The reaper's driver query, against real SQL.
	stale, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{
		Project: project, CreatedBefore: ci.CreatedAt + 1,
	})
	if err != nil {
		t.Fatalf("driver query: %v", err)
	}
	if len(stale) != 1 || stale[0].ExpiresAt != ci.ExpiresAt {
		t.Fatalf("the driver query must carry the stamped expiry, got %d rows", len(stale))
	}
	if err := s.MarkCustomImageResumed(ctx, project, "toolbox", ci.Version, 0); err != nil {
		t.Fatalf("mark resumed: %v", err)
	}
	if err := s.MarkCustomImageReaped(ctx, project, "toolbox", ci.Version, 0); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if _, err := s.ResolveCustomImage(ctx, project, "toolbox"); err == nil {
		t.Fatalf("a tombstoned version must stop resolving")
	}
	projects, err := s.ListCatalogueProjects(ctx)
	if err != nil {
		t.Fatalf("list catalogue projects: %v", err)
	}
	for _, p := range projects {
		if p == project {
			t.Fatalf("a project whose only version is tombstoned has nothing left to reap")
		}
	}
}
