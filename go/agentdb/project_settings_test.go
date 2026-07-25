package agentdb

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// newProjectSettingsTestStore returns a Store with ONLY project_settings
// auto-migrated (the production Postgres migrations cannot run on sqlite).
func newProjectSettingsTestStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t) // from artifacts_test.go (sqlite + AutoMigrate(&Artifact{}))
	if err := s.gdb.AutoMigrate(&ProjectSettings{}); err != nil {
		t.Fatalf("automigrate ProjectSettings: %v", err)
	}
	return s
}

// A project nobody has written yet reads back as the §5 defaults, not an error
// and not a zero struct: the row is created lazily on first write.
func TestProjectSettingsDefaultsForUnwrittenProject(t *testing.T) {
	s := newProjectSettingsTestStore(t)

	ps, err := s.GetProjectSettings(context.Background(), "acme")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ps.Project != "acme" {
		t.Fatalf("project: want acme, got %q", ps.Project)
	}
	if ps.MaxConcurrentJobs != 4 || ps.BriefingMaxBytes != 2048 || ps.SnapshotTTLDays != 30 {
		t.Fatalf("defaults: want 4/2048/30, got %d/%d/%d",
			ps.MaxConcurrentJobs, ps.BriefingMaxBytes, ps.SnapshotTTLDays)
	}
	if ps.DailyTokensSoft != 0 || ps.DailyTokensHard != 0 {
		t.Fatalf("token budgets default to off (0), got %d/%d", ps.DailyTokensSoft, ps.DailyTokensHard)
	}
	if ps.BaseImage != "" || ps.SystemPrompt != "" {
		t.Fatalf("expected empty base image/prompt, got %q/%q", ps.BaseImage, ps.SystemPrompt)
	}
	if ps.MCPConfig == nil || ps.AttentionChannel == nil {
		t.Fatalf("json columns should default to empty objects, got %v/%v", ps.MCPConfig, ps.AttentionChannel)
	}
	if ps.UpdatedAt != 0 {
		t.Fatalf("defaults are not persisted, so updated_at must be 0, got %d", ps.UpdatedAt)
	}
}

// Round-trip every column, including the four §5 budget/cap columns and the
// zero-means-something cases the spec calls out.
func TestProjectSettingsPutRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		in    ProjectSettings
		check func(t *testing.T, got *ProjectSettings)
	}{
		{
			name: "all fields",
			in: ProjectSettings{
				Project:           "acme",
				BaseImage:         "acme/base:v3",
				SystemPrompt:      "you work for acme",
				MCPConfig:         JSONMap{"gmail": map[string]any{"url": "http://gmail-mcp:9000"}},
				AttentionChannel:  JSONMap{"kind": "webhook", "url": "https://hooks.example/x"},
				MaxConcurrentJobs: 9,
				DailyTokensSoft:   1_000_000,
				DailyTokensHard:   4_000_000,
				BriefingMaxBytes:  4096,
				SnapshotTTLDays:   7,
			},
			check: func(t *testing.T, got *ProjectSettings) {
				if got.BaseImage != "acme/base:v3" || got.SystemPrompt != "you work for acme" {
					t.Fatalf("prompt/image: %+v", got)
				}
				if got.MaxConcurrentJobs != 9 || got.DailyTokensSoft != 1_000_000 ||
					got.DailyTokensHard != 4_000_000 || got.BriefingMaxBytes != 4096 || got.SnapshotTTLDays != 7 {
					t.Fatalf("budget/cap columns: %+v", got)
				}
				if got.MCPConfig["gmail"] == nil {
					t.Fatalf("mcp_config lost: %+v", got.MCPConfig)
				}
				if got.AttentionChannel["kind"] != "webhook" {
					t.Fatalf("attention_channel lost: %+v", got.AttentionChannel)
				}
			},
		},
		{
			// 0 is meaningful for the token budgets (off) and the TTL (never):
			// it must survive the write untouched.
			name: "zero means off for budgets and ttl",
			in: ProjectSettings{
				Project:         "acme",
				DailyTokensSoft: 0,
				DailyTokensHard: 0,
				SnapshotTTLDays: 0,
			},
			check: func(t *testing.T, got *ProjectSettings) {
				if got.DailyTokensSoft != 0 || got.DailyTokensHard != 0 {
					t.Fatalf("token budgets must stay off, got %d/%d", got.DailyTokensSoft, got.DailyTokensHard)
				}
				if got.SnapshotTTLDays != 0 {
					t.Fatalf("snapshot_ttl_days 0 (= never) must survive, got %d", got.SnapshotTTLDays)
				}
			},
		},
		{
			// 0 has no meaning for these two, so it reads as "unset" → default.
			name: "zero means default for concurrency and briefing cap",
			in: ProjectSettings{
				Project:           "acme",
				MaxConcurrentJobs: 0,
				BriefingMaxBytes:  0,
			},
			check: func(t *testing.T, got *ProjectSettings) {
				if got.MaxConcurrentJobs != 4 || got.BriefingMaxBytes != 2048 {
					t.Fatalf("want defaults 4/2048, got %d/%d", got.MaxConcurrentJobs, got.BriefingMaxBytes)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newProjectSettingsTestStore(t)
			ctx := context.Background()

			in := tc.in
			written, err := s.PutProjectSettings(ctx, &in)
			if err != nil {
				t.Fatalf("put: %v", err)
			}
			tc.check(t, written)

			read, err := s.GetProjectSettings(ctx, tc.in.Project)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			tc.check(t, read)
			if read.UpdatedAt == 0 {
				t.Fatalf("updated_at must be stamped on write, got 0")
			}
		})
	}
}

// PUT is whole-object (§5: "no patch semantics"): the second write must clear
// what the first one set, not merge into it.
func TestProjectSettingsPutIsWholeObject(t *testing.T) {
	s := newProjectSettingsTestStore(t)
	ctx := context.Background()

	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
		Project: "acme", BaseImage: "acme/base:v1", SystemPrompt: "first",
		MCPConfig:       JSONMap{"gmail": map[string]any{"url": "http://x"}},
		DailyTokensHard: 500, SnapshotTTLDays: 90,
	}); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
		Project: "acme", SystemPrompt: "second",
	}); err != nil {
		t.Fatalf("second put: %v", err)
	}

	got, err := s.GetProjectSettings(ctx, "acme")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SystemPrompt != "second" {
		t.Fatalf("prompt: want second, got %q", got.SystemPrompt)
	}
	if got.BaseImage != "" {
		t.Fatalf("whole-object write must clear base_image, got %q", got.BaseImage)
	}
	if len(got.MCPConfig) != 0 {
		t.Fatalf("whole-object write must clear mcp_config, got %v", got.MCPConfig)
	}
	if got.DailyTokensHard != 0 {
		t.Fatalf("whole-object write must clear daily_tokens_hard, got %d", got.DailyTokensHard)
	}
	if got.SnapshotTTLDays != 0 {
		t.Fatalf("whole-object write must clear snapshot_ttl_days, got %d", got.SnapshotTTLDays)
	}

	// Exactly one row: a second PUT updates, it does not insert.
	var count int64
	if err := s.gdb.Model(&ProjectSettings{}).Where("project = ?", "acme").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row for the project, got %d", count)
	}
}

func TestProjectSettingsValidation(t *testing.T) {
	tests := []struct {
		name string
		in   *ProjectSettings
	}{
		{"nil settings", nil},
		{"empty project", &ProjectSettings{}},
		{"negative concurrency", &ProjectSettings{Project: "acme", MaxConcurrentJobs: -1}},
		{"negative soft budget", &ProjectSettings{Project: "acme", DailyTokensSoft: -1}},
		{"negative hard budget", &ProjectSettings{Project: "acme", DailyTokensHard: -1}},
		{"negative briefing cap", &ProjectSettings{Project: "acme", BriefingMaxBytes: -1}},
		{"negative ttl", &ProjectSettings{Project: "acme", SnapshotTTLDays: -1}},
	}
	s := newProjectSettingsTestStore(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.PutProjectSettings(context.Background(), tc.in)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !errors.Is(err, ErrInvalidProjectSettings) {
				t.Fatalf("expected ErrInvalidProjectSettings, got %v", err)
			}
		})
	}

	if _, err := s.GetProjectSettings(context.Background(), ""); !errors.Is(err, ErrInvalidProjectSettings) {
		t.Fatalf("empty project read: expected ErrInvalidProjectSettings, got %v", err)
	}

	// A rejected write must leave nothing behind.
	var count int64
	if err := s.gdb.Model(&ProjectSettings{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid writes must not create rows, got %d", count)
	}
}

// §12: a project-isolation negative test on every new table. Settings written
// under one project are unreachable — read or write — from another.
func TestProjectSettingsProjectIsolation(t *testing.T) {
	s := newProjectSettingsTestStore(t)
	ctx := context.Background()

	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
		Project: "alpha", BaseImage: "alpha/base:v1", SystemPrompt: "alpha secrets",
		MCPConfig:       JSONMap{"alpha-tool": map[string]any{"url": "http://alpha"}},
		DailyTokensHard: 111, SnapshotTTLDays: 11,
	}); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}

	// Beta reads its own defaults, never alpha's row.
	beta, err := s.GetProjectSettings(ctx, "beta")
	if err != nil {
		t.Fatalf("get beta: %v", err)
	}
	if beta.BaseImage != "" || beta.SystemPrompt != "" || len(beta.MCPConfig) != 0 || beta.DailyTokensHard != 0 {
		t.Fatalf("cross-project leak into beta: %+v", beta)
	}

	// Writing beta must not touch alpha.
	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
		Project: "beta", BaseImage: "beta/base:v1", SystemPrompt: "beta prompt", SnapshotTTLDays: 3,
	}); err != nil {
		t.Fatalf("put beta: %v", err)
	}
	alpha, err := s.GetProjectSettings(ctx, "alpha")
	if err != nil {
		t.Fatalf("get alpha: %v", err)
	}
	if alpha.BaseImage != "alpha/base:v1" || alpha.SystemPrompt != "alpha secrets" ||
		alpha.DailyTokensHard != 111 || alpha.SnapshotTTLDays != 11 {
		t.Fatalf("beta's write clobbered alpha: %+v", alpha)
	}

	var count int64
	if err := s.gdb.Model(&ProjectSettings{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected one row per project (2), got %d", count)
	}
}

// TestProjectSettingsLivePG exercises migration 020 itself: the real table, its
// column defaults, and the store against Postgres. Skips without
// AGENTKIT_TEST_POSTGRES_URL (see openLivePG in live_pg_test.go).
func TestProjectSettingsLivePG(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := "proj-" + uuid.New().String()
	other := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		s.DB().Exec("DELETE FROM project_settings WHERE project IN (?, ?)", project, other)
	})

	// The migration's own DEFAULTs, proven by inserting only the PK.
	if err := s.DB().Exec("INSERT INTO project_settings (project) VALUES (?)", project).Error; err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	ps, err := s.GetProjectSettings(ctx, project)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ps.MaxConcurrentJobs != 4 || ps.BriefingMaxBytes != 2048 || ps.SnapshotTTLDays != 30 {
		t.Fatalf("column defaults: want 4/2048/30, got %d/%d/%d",
			ps.MaxConcurrentJobs, ps.BriefingMaxBytes, ps.SnapshotTTLDays)
	}
	if ps.DailyTokensSoft != 0 || ps.DailyTokensHard != 0 {
		t.Fatalf("token budget defaults: want 0/0, got %d/%d", ps.DailyTokensSoft, ps.DailyTokensHard)
	}

	// jsonb round-trip through the real driver.
	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
		Project:          project,
		BaseImage:        "gcr.io/x/base:v1",
		SystemPrompt:     "live prompt",
		MCPConfig:        JSONMap{"notion": map[string]any{"url": "http://notion-mcp:9000"}},
		AttentionChannel: JSONMap{"kind": "webhook", "url": "https://hooks.example/live"},
		DailyTokensSoft:  10, DailyTokensHard: 20, BriefingMaxBytes: 512, SnapshotTTLDays: 0,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.GetProjectSettings(ctx, project)
	if err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if got.MCPConfig["notion"] == nil || got.AttentionChannel["url"] != "https://hooks.example/live" {
		t.Fatalf("jsonb round-trip: %+v", got)
	}
	if got.SnapshotTTLDays != 0 || got.BriefingMaxBytes != 512 {
		t.Fatalf("caps: want ttl 0 / briefing 512, got %d/%d", got.SnapshotTTLDays, got.BriefingMaxBytes)
	}

	// Project isolation on the live table.
	otherPS, err := s.GetProjectSettings(ctx, other)
	if err != nil {
		t.Fatalf("get other: %v", err)
	}
	if otherPS.SystemPrompt != "" || otherPS.BaseImage != "" {
		t.Fatalf("cross-project leak: %+v", otherPS)
	}
}
