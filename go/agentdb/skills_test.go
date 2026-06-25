package agentdb

import (
	"context"
	"testing"
)

// newSkillTestStore returns a Store with ONLY agent_skills auto-migrated.
func newSkillTestStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t) // from artifacts_test.go (sqlite + AutoMigrate(&Artifact{}))
	if err := s.gdb.AutoMigrate(&Skill{}); err != nil {
		t.Fatalf("automigrate Skill: %v", err)
	}
	return s
}

func TestUpsertSkill_LatestWinsByScopedName(t *testing.T) {
	s := newSkillTestStore(t)
	ctx := context.Background()

	first, err := s.UpsertSkill(ctx, &Skill{
		Name: "graph-gen", Visibility: "organizational", Customer: "acme", OwnerEmail: "u@acme.com",
		ContentHash: "hash1", BlobPrefix: "acme/graph-gen/hash1",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	second, err := s.UpsertSkill(ctx, &Skill{
		Name: "graph-gen", Visibility: "organizational", Customer: "acme", OwnerEmail: "u2@acme.com",
		ContentHash: "hash2", BlobPrefix: "acme/graph-gen/hash2",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected overwrite of same row (id %s), got new id %s", first.ID, second.ID)
	}

	all := []Skill{}
	s.gdb.WithContext(ctx).Find(&all)
	if len(all) != 1 {
		t.Fatalf("expected 1 row after re-hoist, got %d", len(all))
	}
	if all[0].ContentHash != "hash2" {
		t.Fatalf("expected latest content_hash hash2, got %q", all[0].ContentHash)
	}
}

func TestUpsertSkill_SameNameDifferentScopesAreDistinctRows(t *testing.T) {
	s := newSkillTestStore(t)
	ctx := context.Background()
	_, _ = s.UpsertSkill(ctx, &Skill{Name: "x", Visibility: "organizational", Customer: "acme", OwnerEmail: "a@acme.com"})
	_, _ = s.UpsertSkill(ctx, &Skill{Name: "x", Visibility: "organizational", Customer: "globex", OwnerEmail: "g@globex.com"})
	_, _ = s.UpsertSkill(ctx, &Skill{Name: "x", Visibility: "private", Customer: "acme", OwnerEmail: "a@acme.com"})
	all := []Skill{}
	s.gdb.WithContext(ctx).Find(&all)
	if len(all) != 3 {
		t.Fatalf("expected 3 distinct rows for different scopes, got %d", len(all))
	}
}

func seedVisibilityFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	rows := []*Skill{
		{Name: "acme-org", Visibility: "organizational", Customer: "acme", OwnerEmail: "a@acme.com"},
		{Name: "globex-org", Visibility: "organizational", Customer: "globex", OwnerEmail: "g@globex.com"},
		{Name: "acme-priv", Visibility: "private", Customer: "acme", OwnerEmail: "a@acme.com"},
		{Name: "other-priv", Visibility: "private", Customer: "acme", OwnerEmail: "other@acme.com"},
		{Name: "world-pub", Visibility: "public", Customer: "globex", OwnerEmail: "g@globex.com"},
	}
	for _, r := range rows {
		if _, err := s.UpsertSkill(ctx, r); err != nil {
			t.Fatalf("seed %s: %v", r.Name, err)
		}
	}
}

func names(skills []*Skill) map[string]bool {
	m := map[string]bool{}
	for _, s := range skills {
		m[s.Name] = true
	}
	return m
}

func TestListSkills_OrgScopedStrictlyByCustomer_NoLeak(t *testing.T) {
	s := newSkillTestStore(t)
	seedVisibilityFixture(t, s)
	ctx := context.Background()

	// acme user, "visible" scope = own private ∪ acme org ∪ all public.
	got, err := s.ListSkills(ctx, SkillQuery{CallerEmail: "a@acme.com", CallerCustomer: "acme", Scope: "visible"})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	n := names(got)
	if !n["acme-org"] || !n["acme-priv"] || !n["world-pub"] {
		t.Fatalf("expected acme-org+acme-priv+world-pub, got %v", n)
	}
	// HARD RULE: never another customer's org skill, never another user's private.
	if n["globex-org"] {
		t.Fatal("LEAK: globex-org visible to acme caller")
	}
	if n["other-priv"] {
		t.Fatal("LEAK: another user's private skill visible")
	}
}

func TestListSkills_OrgScopeAloneIsCustomerBound(t *testing.T) {
	s := newSkillTestStore(t)
	seedVisibilityFixture(t, s)
	got, _ := s.ListSkills(context.Background(), SkillQuery{CallerCustomer: "acme", Scope: "organizational"})
	n := names(got)
	if !n["acme-org"] || n["globex-org"] {
		t.Fatalf("org scope must be acme-only, got %v", n)
	}
}

func TestGetSkill_VisibilityChecked(t *testing.T) {
	s := newSkillTestStore(t)
	seedVisibilityFixture(t, s)
	ctx := context.Background()
	// Find globex-org's id, then assert an acme caller cannot GetSkill it.
	var globexOrg Skill
	s.gdb.WithContext(ctx).Where("name = ?", "globex-org").First(&globexOrg)
	_, err := s.GetSkill(ctx, globexOrg.ID, "a@acme.com", "acme")
	if err == nil {
		t.Fatal("LEAK: acme caller fetched globex org skill by id")
	}
	// Owner can fetch their private skill.
	var acmePriv Skill
	s.gdb.WithContext(ctx).Where("name = ?", "acme-priv").First(&acmePriv)
	if _, err := s.GetSkill(ctx, acmePriv.ID, "a@acme.com", "acme"); err != nil {
		t.Fatalf("owner should read own private skill: %v", err)
	}
}

func TestSetSkillVisibility_PromoteToPublic(t *testing.T) {
	s := newSkillTestStore(t)
	ctx := context.Background()
	row, _ := s.UpsertSkill(ctx, &Skill{Name: "p", Visibility: "organizational", Customer: "acme", OwnerEmail: "a@acme.com"})
	if err := s.SetSkillVisibility(ctx, row.ID, "public", "admin@acme.com"); err != nil {
		t.Fatalf("SetSkillVisibility: %v", err)
	}
	got, _ := s.GetSkill(ctx, row.ID, "stranger@globex.com", "globex")
	if got == nil || got.Visibility != "public" || got.PromotedBy != "admin@acme.com" {
		t.Fatalf("expected public skill visible to anyone with promoted_by set, got %+v", got)
	}
}

func TestDeleteSkill(t *testing.T) {
	s := newSkillTestStore(t)
	ctx := context.Background()
	row, _ := s.UpsertSkill(ctx, &Skill{Name: "d", Visibility: "private", Customer: "acme", OwnerEmail: "a@acme.com"})
	if err := s.DeleteSkill(ctx, row.ID); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	if _, err := s.GetSkill(ctx, row.ID, "a@acme.com", "acme"); err == nil {
		t.Fatal("expected skill to be gone after delete")
	}
}
