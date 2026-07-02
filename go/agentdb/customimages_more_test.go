package agentdb

import (
	"context"
	"testing"
)

func TestDeleteCustomImage(t *testing.T) {
	s := newCustomImageTestStore(t)
	ctx := context.Background()

	if err := s.DeleteCustomImage(ctx, ""); err == nil {
		t.Fatalf("expected error for empty id")
	}

	ci, err := s.UpsertCustomImage(ctx, &CustomImage{
		Name: "stack", Visibility: "organizational", Customer: "acme", OwnerEmail: "a@acme.com",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.DeleteCustomImage(ctx, ci.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetCustomImage(ctx, ci.ID, "a@acme.com", "acme"); err == nil {
		t.Fatalf("expected not-found after delete")
	}
	// Deleting a nonexistent image is a no-op, not an error.
	if err := s.DeleteCustomImage(ctx, "never-existed"); err != nil {
		t.Fatalf("delete nonexistent: %v", err)
	}
}

func TestUpsertCustomImage_Validation(t *testing.T) {
	s := newCustomImageTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertCustomImage(ctx, &CustomImage{Visibility: "organizational", Customer: "acme"}); err == nil {
		t.Fatalf("expected error for missing name")
	}
	if _, err := s.UpsertCustomImage(ctx, &CustomImage{Name: "x", Visibility: "public", Customer: "acme"}); err == nil {
		t.Fatalf("expected error for public visibility (custom images are never public)")
	}
	// Empty visibility defaults to organizational.
	ci, err := s.UpsertCustomImage(ctx, &CustomImage{Name: "x", Customer: "acme", OwnerEmail: "a@acme.com"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if ci.Visibility != "organizational" {
		t.Fatalf("expected default visibility organizational, got %q", ci.Visibility)
	}
}
