package agentdb

import (
	"context"
	"strings"
	"testing"
)

func TestSetSkillVisibility_Validation(t *testing.T) {
	s := newSkillTestStore(t)
	ctx := context.Background()

	if err := s.SetSkillVisibility(ctx, "", "public", "admin@acme.com"); err == nil {
		t.Fatalf("expected error for empty id")
	}
	if err := s.SetSkillVisibility(ctx, "some-id", "everyone", "admin@acme.com"); err == nil ||
		!strings.Contains(err.Error(), "invalid visibility") {
		t.Fatalf("expected invalid-visibility error, got %v", err)
	}
	if err := s.SetSkillVisibility(ctx, "missing-id", "public", "admin@acme.com"); err == nil ||
		!strings.Contains(err.Error(), "skill not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestSetSkillVisibility_DowngradeRecordsActor(t *testing.T) {
	s := newSkillTestStore(t)
	ctx := context.Background()

	sk, err := s.UpsertSkill(ctx, &Skill{Name: "tool", Visibility: "public", Customer: "acme", OwnerEmail: "o@acme.com"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetSkillVisibility(ctx, sk.ID, "private", "admin@acme.com"); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	got, err := s.GetSkill(ctx, sk.ID, "o@acme.com", "acme")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Visibility != "private" || got.PromotedBy != "admin@acme.com" {
		t.Fatalf("downgrade not recorded: %+v", got)
	}
}

func TestDeleteSkill_Validation(t *testing.T) {
	s := newSkillTestStore(t)
	ctx := context.Background()

	if err := s.DeleteSkill(ctx, ""); err == nil {
		t.Fatalf("expected error for empty id")
	}
	// Deleting a nonexistent skill is a no-op, not an error.
	if err := s.DeleteSkill(ctx, "never-existed"); err != nil {
		t.Fatalf("delete nonexistent: %v", err)
	}
}

func TestListSkills_PrivateAndPublicScopes(t *testing.T) {
	s := newSkillTestStore(t)
	ctx := context.Background()

	mustUpsert := func(sk *Skill) {
		t.Helper()
		if _, err := s.UpsertSkill(ctx, sk); err != nil {
			t.Fatalf("upsert %s: %v", sk.Name, err)
		}
	}
	mustUpsert(&Skill{Name: "mine", Visibility: "private", Customer: "acme", OwnerEmail: "me@acme.com"})
	mustUpsert(&Skill{Name: "theirs", Visibility: "private", Customer: "acme", OwnerEmail: "other@acme.com"})
	mustUpsert(&Skill{Name: "shared", Visibility: "public", Customer: "globex", OwnerEmail: "g@globex.com"})

	priv, err := s.ListSkills(ctx, SkillQuery{CallerEmail: "me@acme.com", CallerCustomer: "acme", Scope: "private"})
	if err != nil {
		t.Fatalf("list private: %v", err)
	}
	if len(priv) != 1 || priv[0].Name != "mine" {
		t.Fatalf("private scope must be owner-bound: %+v", priv)
	}

	pub, err := s.ListSkills(ctx, SkillQuery{CallerEmail: "me@acme.com", CallerCustomer: "acme", Scope: "public"})
	if err != nil {
		t.Fatalf("list public: %v", err)
	}
	if len(pub) != 1 || pub[0].Name != "shared" {
		t.Fatalf("public scope must return public rows regardless of customer: %+v", pub)
	}

	empty, err := s.ListSkills(ctx, SkillQuery{CallerEmail: "nobody@x.com", CallerCustomer: "x", Scope: "private"})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v err=%v", empty, err)
	}
}
