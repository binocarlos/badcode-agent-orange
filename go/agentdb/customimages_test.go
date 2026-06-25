package agentdb

import (
	"context"
	"testing"
)

// newCustomImageTestStore returns a Store with agent_custom_images auto-migrated.
func newCustomImageTestStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t) // from artifacts_test.go (sqlite + AutoMigrate(&Artifact{}))
	if err := s.gdb.AutoMigrate(&CustomImage{}); err != nil {
		t.Fatalf("automigrate CustomImage: %v", err)
	}
	return s
}

func TestCustomImage_TableNameAndPersist(t *testing.T) {
	if (CustomImage{}).TableName() != "agent_custom_images" {
		t.Fatalf("unexpected table name %q", (CustomImage{}).TableName())
	}
	s := newCustomImageTestStore(t)
	ctx := context.Background()
	ci := &CustomImage{
		ID:             "img-1",
		Name:           "research-stack",
		Visibility:     "organizational",
		Customer:       "acme",
		OwnerEmail:     "a@acme.com",
		ContentHash:    "abc123",
		RegistryHandle: `{"kind":"blob-archive","ref":"x"}`,
		SkillSet:       `[{"skillId":"s1","name":"a","content_hash":"h1"}]`,
		RequiresBuild:  true,
	}
	if err := s.gdb.WithContext(ctx).Create(ci).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got CustomImage
	if err := s.gdb.WithContext(ctx).Where("id = ?", "img-1").First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Name != "research-stack" || got.Customer != "acme" || !got.RequiresBuild {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestListCustomImages_OrgScopedStrictlyByCustomer_NoLeak(t *testing.T) {
	s := newCustomImageTestStore(t)
	ctx := context.Background()
	mustUpsertImg(t, s, &CustomImage{Name: "stack", Visibility: "organizational", Customer: "acme", OwnerEmail: "a@acme.com"})
	mustUpsertImg(t, s, &CustomImage{Name: "stack", Visibility: "organizational", Customer: "globex", OwnerEmail: "b@globex.com"})

	got, err := s.ListCustomImages(ctx, CustomImageQuery{CallerEmail: "a@acme.com", CallerCustomer: "acme", Scope: "organizational"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Customer != "acme" {
		t.Fatalf("cross-customer leak or wrong scope: %+v", got)
	}
}

func TestListCustomImages_VisibleUnionExcludesOtherCustomerAndOwner(t *testing.T) {
	s := newCustomImageTestStore(t)
	ctx := context.Background()
	mustUpsertImg(t, s, &CustomImage{Name: "mine", Visibility: "private", Customer: "acme", OwnerEmail: "me@acme.com"})
	mustUpsertImg(t, s, &CustomImage{Name: "theirs", Visibility: "private", Customer: "acme", OwnerEmail: "other@acme.com"})
	mustUpsertImg(t, s, &CustomImage{Name: "org", Visibility: "organizational", Customer: "acme", OwnerEmail: "x@acme.com"})
	mustUpsertImg(t, s, &CustomImage{Name: "foreign", Visibility: "organizational", Customer: "globex", OwnerEmail: "y@globex.com"})

	got, err := s.ListCustomImages(ctx, CustomImageQuery{CallerEmail: "me@acme.com", CallerCustomer: "acme", Scope: "visible"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := map[string]bool{}
	for _, ci := range got {
		names[ci.Name] = true
	}
	if !names["mine"] || !names["org"] {
		t.Fatalf("expected own private + org image; got %v", names)
	}
	if names["theirs"] || names["foreign"] {
		t.Fatalf("leaked other-owner private or other-customer org: %v", names)
	}
}

func TestUpsertCustomImage_LatestWinsByScopedName(t *testing.T) {
	s := newCustomImageTestStore(t)
	ctx := context.Background()
	first := mustUpsertImg(t, s, &CustomImage{Name: "stack", Visibility: "organizational", Customer: "acme", ContentHash: "h1"})
	second := mustUpsertImg(t, s, &CustomImage{Name: "stack", Visibility: "organizational", Customer: "acme", ContentHash: "h2"})
	if first.ID != second.ID {
		t.Fatalf("expected overwrite of same row, got new id %q vs %q", first.ID, second.ID)
	}
	got, _ := s.ListCustomImages(ctx, CustomImageQuery{CallerCustomer: "acme", Scope: "organizational"})
	if len(got) != 1 || got[0].ContentHash != "h2" {
		t.Fatalf("expected single row with latest hash, got %+v", got)
	}
}

func TestGetCustomImage_OrgVisibilityRequiresSameCustomer(t *testing.T) {
	s := newCustomImageTestStore(t)
	ctx := context.Background()
	img := mustUpsertImg(t, s, &CustomImage{Name: "stack", Visibility: "organizational", Customer: "acme"})
	if _, err := s.GetCustomImage(ctx, img.ID, "x@globex.com", "globex"); err == nil {
		t.Fatalf("expected not-found for foreign customer")
	}
	if _, err := s.GetCustomImage(ctx, img.ID, "x@acme.com", "acme"); err != nil {
		t.Fatalf("expected visible to same customer: %v", err)
	}
}

func TestUpsertCustomImage_PersistsBaseImageID(t *testing.T) {
	s := newCustomImageTestStore(t)
	ctx := context.Background()

	// Create with a base pointer.
	row, err := s.UpsertCustomImage(ctx, &CustomImage{
		Name:        "variant",
		Visibility:  "private",
		Customer:    "acme",
		OwnerEmail:  "u@acme.com",
		ContentHash: "h1",
		BaseImageID: "base-img-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetCustomImage(ctx, row.ID, "u@acme.com", "acme")
	if err != nil || got == nil {
		t.Fatalf("get after create: %v", err)
	}
	if got.BaseImageID != "base-img-1" {
		t.Fatalf("create did not persist base_image_id: %q", got.BaseImageID)
	}

	// Upsert again with the SAME scope (private→owner+name) to hit the update branch,
	// changing the base pointer; it must persist.
	if _, err := s.UpsertCustomImage(ctx, &CustomImage{
		Name:        "variant",
		Visibility:  "private",
		Customer:    "acme",
		OwnerEmail:  "u@acme.com",
		ContentHash: "h2",
		BaseImageID: "base-img-2",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, err := s.GetCustomImage(ctx, row.ID, "u@acme.com", "acme")
	if err != nil || got2 == nil {
		t.Fatalf("get after update: %v", err)
	}
	if got2.BaseImageID != "base-img-2" {
		t.Fatalf("update did not persist base_image_id: %q", got2.BaseImageID)
	}
}

func mustUpsertImg(t *testing.T, s *Store, ci *CustomImage) *CustomImage {
	t.Helper()
	out, err := s.UpsertCustomImage(context.Background(), ci)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return out
}
