package agentdb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// scopeWhere returns the GORM where-clause that identifies the unique slot a
// skill occupies, by visibility boundary: private → (owner_email, name);
// organizational → (customer, name); public → (name). Used for latest-wins upsert.
func scopeWhere(db *gorm.DB, sk *Skill) *gorm.DB {
	switch sk.Visibility {
	case "private":
		return db.Where("visibility = ? AND owner_email = ? AND name = ?", "private", sk.OwnerEmail, sk.Name)
	case "public":
		return db.Where("visibility = ? AND name = ?", "public", sk.Name)
	default: // organizational
		return db.Where("visibility = ? AND customer = ? AND name = ?", "organizational", sk.Customer, sk.Name)
	}
}

// UpsertSkill inserts or overwrites the catalog row for a skill, keyed by its
// scoped name (latest-wins). Bytes are never lost (content-addressed blobs live
// under blob_prefix); only the catalog pointer moves.
func (s *Store) UpsertSkill(ctx context.Context, sk *Skill) (*Skill, error) {
	if sk.Name == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	if sk.Visibility == "" {
		sk.Visibility = "organizational"
	}

	var existing Skill
	err := scopeWhere(s.gdb.WithContext(ctx).Model(&Skill{}), sk).First(&existing).Error
	if err == nil {
		existing.Description = sk.Description
		existing.OwnerEmail = sk.OwnerEmail
		existing.RequiresBuild = sk.RequiresBuild
		existing.ContentHash = sk.ContentHash
		existing.BlobPrefix = sk.BlobPrefix
		existing.Manifest = sk.Manifest
		existing.SourceSessionID = sk.SourceSessionID
		if err := s.gdb.WithContext(ctx).Save(&existing).Error; err != nil {
			return nil, fmt.Errorf("failed to update agent skill: %w", err)
		}
		return &existing, nil
	}

	if sk.ID == "" {
		sk.ID = uuid.New().String()
	}
	if err := s.gdb.WithContext(ctx).Create(sk).Error; err != nil {
		return nil, fmt.Errorf("failed to create agent skill: %w", err)
	}
	return sk, nil
}

// SkillQuery is the only entry point for reading skills. The customer-scoping
// rule for organizational rows is enforced here and ONLY here.
type SkillQuery struct {
	CallerEmail    string
	CallerCustomer string
	// Scope: "private" | "organizational" | "public" | "visible" (union of all three).
	Scope string
}

// ListSkills returns skills visible to the caller under the requested scope.
// Organizational rows are ALWAYS bound by CallerCustomer — there is no code path
// that returns organizational rows without a customer predicate.
func (s *Store) ListSkills(ctx context.Context, q SkillQuery) ([]*Skill, error) {
	db := s.gdb.WithContext(ctx).Model(&Skill{})
	switch q.Scope {
	case "private":
		db = db.Where("visibility = ? AND owner_email = ?", "private", q.CallerEmail)
	case "organizational":
		db = db.Where("visibility = ? AND customer = ?", "organizational", q.CallerCustomer)
	case "public":
		db = db.Where("visibility = ?", "public")
	default: // "visible" — union of the three, each independently scoped.
		db = db.Where(
			"(visibility = ? AND owner_email = ?) OR (visibility = ? AND customer = ?) OR (visibility = ?)",
			"private", q.CallerEmail, "organizational", q.CallerCustomer, "public",
		)
	}
	var skills []*Skill
	if err := db.Order("updated_at DESC").Find(&skills).Error; err != nil {
		return nil, fmt.Errorf("failed to list agent skills: %w", err)
	}
	if skills == nil {
		skills = []*Skill{}
	}
	return skills, nil
}

// GetSkill fetches a single skill by id, returning an error if it is not visible
// to the caller (private→owner only; organizational→same customer; public→any).
func (s *Store) GetSkill(ctx context.Context, id, callerEmail, callerCustomer string) (*Skill, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot get agent skill without ID")
	}
	var sk Skill
	if err := s.gdb.WithContext(ctx).Where("id = ?", id).First(&sk).Error; err != nil {
		return nil, fmt.Errorf("failed to get agent skill: %w", err)
	}
	switch sk.Visibility {
	case "public":
		return &sk, nil
	case "organizational":
		if sk.Customer == callerCustomer {
			return &sk, nil
		}
	case "private":
		if sk.OwnerEmail == callerEmail {
			return &sk, nil
		}
	}
	return nil, fmt.Errorf("skill not found") // not-visible looks like not-found (no existence leak)
}

// SetSkillVisibility changes a skill's visibility (e.g. gated promotion to public,
// or a downgrade), recording who performed it in promoted_by.
func (s *Store) SetSkillVisibility(ctx context.Context, id, visibility, actorEmail string) error {
	if id == "" {
		return fmt.Errorf("skill id is required")
	}
	switch visibility {
	case "private", "organizational", "public":
	default:
		return fmt.Errorf("invalid visibility %q", visibility)
	}
	res := s.gdb.WithContext(ctx).Model(&Skill{}).Where("id = ?", id).
		Updates(map[string]any{"visibility": visibility, "promoted_by": actorEmail})
	if res.Error != nil {
		return fmt.Errorf("failed to set skill visibility: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("skill not found")
	}
	return nil
}

// DeleteSkill removes a catalog row by id. Blobs under blob_prefix are intentionally
// NOT deleted here (content-addressed; GC is out of scope v1).
func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("skill id is required")
	}
	return s.gdb.WithContext(ctx).Where("id = ?", id).Delete(&Skill{}).Error
}
