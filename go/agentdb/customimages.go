package agentdb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// customImageScopeWhere identifies the unique slot a custom image occupies, by
// visibility boundary: private → (owner_email, name); organizational →
// (customer, name). Custom images are never public. Used for latest-wins upsert.
func customImageScopeWhere(db *gorm.DB, ci *CustomImage) *gorm.DB {
	if ci.Visibility == "private" {
		return db.Where("visibility = ? AND owner_email = ? AND name = ?", "private", ci.OwnerEmail, ci.Name)
	}
	return db.Where("visibility = ? AND customer = ? AND name = ?", "organizational", ci.Customer, ci.Name)
}

// UpsertCustomImage inserts or overwrites the catalog row for a built image,
// keyed by its scoped name (latest-wins).
func (s *Store) UpsertCustomImage(ctx context.Context, ci *CustomImage) (*CustomImage, error) {
	if ci.Name == "" {
		return nil, fmt.Errorf("custom image name is required")
	}
	if ci.Visibility == "" {
		ci.Visibility = "organizational"
	}
	if ci.Visibility != "private" && ci.Visibility != "organizational" {
		return nil, fmt.Errorf("invalid custom image visibility %q (private|organizational only)", ci.Visibility)
	}

	var existing CustomImage
	err := customImageScopeWhere(s.gdb.WithContext(ctx).Model(&CustomImage{}), ci).First(&existing).Error
	if err == nil {
		existing.Description = ci.Description
		existing.OwnerEmail = ci.OwnerEmail
		existing.ContentHash = ci.ContentHash
		existing.RegistryHandle = ci.RegistryHandle
		existing.SkillSet = ci.SkillSet
		existing.RequiresBuild = ci.RequiresBuild
		existing.BaseImageID = ci.BaseImageID
		if err := s.gdb.WithContext(ctx).Save(&existing).Error; err != nil {
			return nil, fmt.Errorf("failed to update custom image: %w", err)
		}
		return &existing, nil
	}

	if ci.ID == "" {
		ci.ID = uuid.New().String()
	}
	if err := s.gdb.WithContext(ctx).Create(ci).Error; err != nil {
		return nil, fmt.Errorf("failed to create custom image: %w", err)
	}
	return ci, nil
}

// CustomImageQuery is the only entry point for reading custom images. The
// customer-scoping rule for organizational rows is enforced here and ONLY here.
type CustomImageQuery struct {
	CallerEmail    string
	CallerCustomer string
	// Scope: "private" | "organizational" | "visible" (union of private + org).
	Scope string
}

// ListCustomImages returns images visible to the caller under the requested
// scope. Organizational rows are ALWAYS bound by CallerCustomer.
func (s *Store) ListCustomImages(ctx context.Context, q CustomImageQuery) ([]*CustomImage, error) {
	db := s.gdb.WithContext(ctx).Model(&CustomImage{})
	switch q.Scope {
	case "private":
		db = db.Where("visibility = ? AND owner_email = ?", "private", q.CallerEmail)
	case "organizational":
		db = db.Where("visibility = ? AND customer = ?", "organizational", q.CallerCustomer)
	default: // "visible" — union of private(own) + org(same customer); no public.
		db = db.Where(
			"(visibility = ? AND owner_email = ?) OR (visibility = ? AND customer = ?)",
			"private", q.CallerEmail, "organizational", q.CallerCustomer,
		)
	}
	var images []*CustomImage
	if err := db.Order("updated_at DESC").Find(&images).Error; err != nil {
		return nil, fmt.Errorf("failed to list custom images: %w", err)
	}
	if images == nil {
		images = []*CustomImage{}
	}
	return images, nil
}

// GetCustomImage fetches a single image by id, returning an error if it is not
// visible to the caller (private→owner only; organizational→same customer).
func (s *Store) GetCustomImage(ctx context.Context, id, callerEmail, callerCustomer string) (*CustomImage, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot get custom image without ID")
	}
	var ci CustomImage
	if err := s.gdb.WithContext(ctx).Where("id = ?", id).First(&ci).Error; err != nil {
		return nil, fmt.Errorf("failed to get custom image: %w", err)
	}
	switch ci.Visibility {
	case "organizational":
		if ci.Customer == callerCustomer {
			return &ci, nil
		}
	case "private":
		if ci.OwnerEmail == callerEmail {
			return &ci, nil
		}
	}
	return nil, fmt.Errorf("custom image not found") // not-visible looks like not-found
}

// DeleteCustomImage removes a catalog row by id. The built image blobs/registry
// entry are intentionally NOT removed here (content-addressed; GC out of scope v1).
func (s *Store) DeleteCustomImage(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("custom image id is required")
	}
	return s.gdb.WithContext(ctx).Where("id = ?", id).Delete(&CustomImage{}).Error
}
