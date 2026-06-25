package agentkit

import "context"

// SkillPromotion is the payload handed to a SkillCatalog when a skill is hoisted.
type SkillPromotion struct {
	SessionID     string // source session
	ArtifactPath  string // workspace-relative bundle path (== captured artifact FilePath)
	Name          string
	Description   string
	Visibility    string // "private" | "organizational" (never "public" from a hoist)
	Customer      string
	OwnerEmail    string
	RequiresBuild bool
	Manifest      []byte // raw skill.manifest.json
}

// SkillCatalog persists a hoisted skill into the durable cross-session catalog
// (copies the bundle's blobs to a skill-owned prefix + records the row). The
// runner calls it from onSkillHoisted after the bundle artifact is captured.
type SkillCatalog interface {
	Promote(ctx context.Context, p SkillPromotion) error
}
