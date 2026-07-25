package agentdb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// This file holds two catalogues in one table (agent_custom_images).
//
//  1. The LEGACY host-built catalogue (UpsertCustomImage / ListCustomImages* /
//     GetCustomImage / DeleteCustomImage): latest-wins rows keyed by a
//     visibility scope, with no version. Those rows carry version 0.
//
//  2. The §13 CATALOGUE (CreateCustomImage / ListCustomImageVersions /
//     ResolveCustomImage): named, versioned, labeled, append-only records that
//     an agent burns from inside a session. Those rows carry version >= 1.
//
// PROJECT NAMESPACE. §13.2 calls the namespace column `project`; this table has
// called it `customer` since migration 001 and J1 already treats it as the
// project when it writes `image_create` config events. Migration 025
// deliberately does NOT add a second `project` column: two columns meaning the
// same thing is drift waiting to happen, and the rename would have to sweep the
// legacy visibility paths, the httpapi handlers and the runner. So: the §13 API
// speaks `project` and the column stays `customer`, mapped in exactly one
// place — here.
//
// APPEND-ONLY at the tool/store surface (§13.2): there is no UpdateCustomImage
// and no method that deletes a catalogue version. Publishing an improved
// environment is a NEW version under the same name, exactly as improving a
// rolling summary is a newer memory. The one write that touches an existing
// catalogue row is MarkCustomImageReaped, and it is storage GC, not curation
// (§13.7).
// ---------------------------------------------------------------------------

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
//
// Configuration mutation: the catalog row and an `image_create` config event
// are written in one transaction (§15.3, §15.4). cw carries the acting worker /
// session and an optional rationale; human/API callers pass the zero value.
// The image's Customer is its project namespace and is therefore required.
func (s *Store) UpsertCustomImage(ctx context.Context, ci *CustomImage, cw ConfigWrite) (*CustomImage, error) {
	if ci.Name == "" {
		return nil, fmt.Errorf("custom image name is required")
	}
	if ci.Customer == "" {
		return nil, fmt.Errorf("custom image customer (project) is required")
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
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: existing.Customer,
			Action:  ActionImageCreate,
			Payload: &existing,
			Write:   cw,
		}, func(tx *gorm.DB) error {
			return tx.Save(&existing).Error
		}); err != nil {
			return nil, fmt.Errorf("failed to update custom image: %w", err)
		}
		return &existing, nil
	}

	if ci.ID == "" {
		ci.ID = uuid.New().String()
	}
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: ci.Customer,
		Action:  ActionImageCreate,
		Payload: ci,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		return tx.Create(ci).Error
	}); err != nil {
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

// ═══════════════════════════════════════════════════════════════════════════
// §13 — named, versioned, labeled images
// ═══════════════════════════════════════════════════════════════════════════

// Errors of the §13 catalogue. They are sentinels so the tool layer (I2) and
// the launch path (I4) can distinguish "you asked for something that never
// existed" from "you asked for something whose bytes we no longer keep" without
// string-matching — and so neither can be mistaken for "fall back to the
// project default", which §13.3 forbids outright.
var (
	// ErrCustomImageNotFound is returned when no catalogue version matches the
	// reference in this project. Resolution NEVER falls back (§13.3).
	ErrCustomImageNotFound = errors.New("agentdb: image not found")
	// ErrCustomImageReaped is returned when the version exists as a record but
	// its bytes were reaped by the snapshot_ttl_days reaper (§13.7).
	ErrCustomImageReaped = errors.New("agentdb: image version was reaped")
	// ErrCustomImageUnmaterialisable is returned when the version exists and is
	// not reaped but carries no registry handle — nothing to launch from.
	ErrCustomImageUnmaterialisable = errors.New("agentdb: image version cannot be materialised")
	// ErrCustomImageInvalid wraps every validation failure on a catalogue write
	// or a malformed reference.
	ErrCustomImageInvalid = errors.New("agentdb: invalid image")
)

// imageNameRe is the charset of a §13 image name. It is lowercase and, above
// all, colon-free: `name:version` is the identity (§13.2), so a colon inside a
// name would make references ambiguous. Otherwise it follows the OCI-ish
// repository-name shape operators already expect.
var imageNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// maxImageNameLen matches the `name` column width.
const maxImageNameLen = 255

// imageVersionAllocAttempts bounds the retry loop that allocates a version.
// Two sessions burning the same name at the same instant collide on the unique
// index; the loser re-reads the high-water mark and tries again. Bounded so a
// pathological hot name fails loudly instead of spinning.
const imageVersionAllocAttempts = 5

// ImageRef is a parsed §13.3 reference: a floating `name` or a pinned
// `name:version`.
type ImageRef struct {
	Name string
	// Version is 0 for a floating reference (resolve to latest) and >= 1 for a
	// pinned one.
	Version int
}

// Pinned reports whether the reference names an exact version.
func (r ImageRef) Pinned() bool { return r.Version > 0 }

// String renders the reference back to its canonical text (ParseImageRef ∘
// String is a fixed point).
func (r ImageRef) String() string {
	if r.Pinned() {
		return r.Name + ":" + strconv.Itoa(r.Version)
	}
	return r.Name
}

// ParseImageRef parses the two reference forms of §13.3 and nothing else. A
// reference is one text field's worth of expressiveness on purpose: the common
// case ("give me the current toolbox") costs no ceremony, and pinning is
// available without a new mechanism.
func ParseImageRef(ref string) (ImageRef, error) {
	var zero ImageRef
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return zero, fmt.Errorf("%w: image reference is required", ErrCustomImageInvalid)
	}
	name, versionText, pinned := strings.Cut(ref, ":")
	if err := validateImageName(name); err != nil {
		return zero, fmt.Errorf("%w (reference %q)", err, ref)
	}
	if !pinned {
		return ImageRef{Name: name}, nil
	}
	version, err := strconv.Atoi(versionText)
	if err != nil {
		return zero, fmt.Errorf("%w: reference %q: version %q is not an integer", ErrCustomImageInvalid, ref, versionText)
	}
	if version < 1 {
		return zero, fmt.Errorf("%w: reference %q: versions start at 1", ErrCustomImageInvalid, ref)
	}
	return ImageRef{Name: name, Version: version}, nil
}

func validateImageName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: image name is required", ErrCustomImageInvalid)
	}
	if len(name) > maxImageNameLen {
		return fmt.Errorf("%w: image name is %d chars, max %d", ErrCustomImageInvalid, len(name), maxImageNameLen)
	}
	if !imageNameRe.MatchString(name) {
		return fmt.Errorf("%w: image name %q is invalid: lowercase alphanumerics, '.', '-' and '_', starting and ending alphanumeric, and never a ':'", ErrCustomImageInvalid, name)
	}
	return nil
}

// CreateCustomImage appends a new version of `name` in `project` — §13.4's
// `image_create` at the store level.
//
// The version is allocated here, never supplied: it is the next integer after
// the highest version this (project, name) has ever held, so versions are
// monotonic and gap-free and a caller cannot fabricate an identity. Tombstoned
// versions still count toward the high-water mark (§13.7) — reaping bytes must
// not make a version number reusable.
//
// Configuration mutation: the catalogue row and an `image_create` config event
// are written in one transaction (§15.3, §15.4). cw carries the acting worker /
// session and an optional rationale; human/API callers pass the zero value.
// Provenance (CreatedByWorker / CreatedBySession, §13.2) is taken from cw when
// the caller has not set it explicitly, so the tool layer cannot record one
// actor in the log and another in the catalogue.
//
// The stored row is read back and returned (§9), never the caller's struct.
func (s *Store) CreateCustomImage(ctx context.Context, ci *CustomImage, cw ConfigWrite) (*CustomImage, error) {
	if ci == nil {
		return nil, fmt.Errorf("%w: image is required", ErrCustomImageInvalid)
	}
	if ci.Customer == "" {
		return nil, fmt.Errorf("%w: project is required (P5: the namespace is never inferred)", ErrCustomImageInvalid)
	}
	if err := validateImageName(ci.Name); err != nil {
		return nil, err
	}
	if ci.Version != 0 {
		return nil, fmt.Errorf("%w: version is allocated by the store, not supplied (got %d)", ErrCustomImageInvalid, ci.Version)
	}
	if err := ValidateLabels(ci.Labels); err != nil {
		return nil, fmt.Errorf("%w: labels: %w", ErrCustomImageInvalid, err)
	}
	normalizeCatalogueImage(ci, cw)

	var lastErr error
	for attempt := 0; attempt < imageVersionAllocAttempts; attempt++ {
		next, err := s.nextCustomImageVersion(ctx, ci.Customer, ci.Name)
		if err != nil {
			return nil, err
		}
		ci.ID = uuid.New().String()
		ci.Version = next
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: ci.Customer,
			Action:  ActionImageCreate,
			Payload: ci, // the FULL new row, version included
			Write:   cw,
		}, func(tx *gorm.DB) error {
			return tx.Create(ci).Error
		}); err != nil {
			ci.Version = 0 // leave the caller's struct as it was found
			if isUniqueViolation(err) {
				lastErr = err
				continue // someone else took this version; re-read and retry
			}
			return nil, fmt.Errorf("agentdb: create image version: %w", err)
		}
		return s.GetCustomImageVersion(ctx, ci.Customer, ci.Name, next)
	}
	return nil, fmt.Errorf("agentdb: could not allocate a version for image %q after %d attempts: %w",
		ci.Name, imageVersionAllocAttempts, lastErr)
}

// normalizeCatalogueImage applies the catalogue's defaults. It lives in code
// rather than in gorm `default:` tags because GORM omits zero-valued fields
// from the INSERT when a default is declared — the DDL defaults in migration
// 025 exist only for rows written outside this store.
func normalizeCatalogueImage(ci *CustomImage, cw ConfigWrite) {
	if ci.Labels == nil {
		ci.Labels = LabelSet{}
	}
	if ci.Visibility == "" {
		// The catalogue has no visibility concept — a §13 image belongs to its
		// project (P5). The column is NOT NULL and the legacy read paths filter
		// on it, so catalogue rows take the project-wide value.
		ci.Visibility = "organizational"
	}
	if ci.CreatedByWorker == "" {
		ci.CreatedByWorker = cw.Worker
	}
	if ci.CreatedBySession == "" {
		ci.CreatedBySession = cw.Session
	}
	if ci.CreatedAt == 0 {
		ci.CreatedAt = time.Now().Unix()
	}
	ci.ReapedAt = 0
}

// nextCustomImageVersion reads the high-water mark for (project, name). It runs
// outside the config-event transaction on purpose: the read is only a hint, and
// the partial unique index — not this query — is what makes allocation correct
// under concurrency.
func (s *Store) nextCustomImageVersion(ctx context.Context, project, name string) (int, error) {
	var highest int
	if err := s.gdb.WithContext(ctx).Model(&CustomImage{}).
		Where("customer = ? AND name = ?", project, name).
		Select("COALESCE(MAX(version), 0)").
		Scan(&highest).Error; err != nil {
		return 0, fmt.Errorf("agentdb: read image version high-water mark: %w", err)
	}
	return highest + 1, nil
}

// isUniqueViolation reports whether err is a unique-constraint failure, on
// either backend (Postgres SQLSTATE 23505, sqlite's message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "unique constraint")
}

// ImageCatalogQuery filters the §13 catalogue. Project is REQUIRED and is the
// only tenancy boundary: image names never cross projects (P5, §13.2).
type ImageCatalogQuery struct {
	// Project is the hard namespace (the `customer` column). Required.
	Project string
	// Name restricts the listing to one name. Optional.
	Name string
	// LabelSelector is Kubernetes-style selector text (§7.2), translated by the
	// one selector parser the whole system shares. Postgres only — the
	// translation is jsonb containment.
	LabelSelector string
	// CreatedBefore restricts to versions older than this unix-seconds instant.
	// It exists for the snapshot_ttl_days reaper (B4, §13.7), which needs to ask
	// "what in this catalogue is older than the TTL?".
	CreatedBefore int64
	// IncludeReaped includes tombstoned versions. The default is false: the
	// catalogue view an agent sees is of images it can actually launch. The
	// reaper passes true to avoid re-reaping what it already reaped.
	IncludeReaped bool
	// Limit caps the result. 0 = no limit.
	Limit int
}

// ListCustomImageVersions returns catalogue versions newest first — §13.4's
// `image_list` at the store level.
//
// Legacy rows (version 0) are never returned: they belong to the pre-§13
// host-built catalogue and have no version to pin or resolve.
func (s *Store) ListCustomImageVersions(ctx context.Context, q ImageCatalogQuery) ([]*CustomImage, error) {
	if strings.TrimSpace(q.Project) == "" {
		return nil, fmt.Errorf("%w: project is required (P5)", ErrCustomImageInvalid)
	}
	db := s.gdb.WithContext(ctx).Model(&CustomImage{}).
		Where("customer = ? AND version > 0", q.Project)
	if q.Name != "" {
		if err := validateImageName(q.Name); err != nil {
			return nil, err
		}
		db = db.Where("name = ?", q.Name)
	}
	if !q.IncludeReaped {
		db = db.Where("reaped_at = 0")
	}
	if q.CreatedBefore > 0 {
		db = db.Where("created_at < ?", q.CreatedBefore)
	}
	if strings.TrimSpace(q.LabelSelector) != "" {
		if s.gdb.Dialector == nil || s.gdb.Dialector.Name() != "postgres" {
			return nil, fmt.Errorf("%w: label selectors require Postgres (jsonb containment)", ErrCustomImageInvalid)
		}
		labelSQL, labelArgs, err := LabelSelectorSQL(q.LabelSelector, "labels")
		if err != nil {
			return nil, fmt.Errorf("%w: label selector: %w", ErrCustomImageInvalid, err)
		}
		if labelSQL != "" {
			db = db.Where(labelSQL, labelArgs...)
		}
	}
	if q.Limit > 0 {
		db = db.Limit(q.Limit)
	}
	var images []*CustomImage
	// version DESC and id DESC are tiebreaks, not the sort: created_at is
	// seconds on this table, so two burns in the same second must still come
	// back in a stable, newest-first-looking order.
	if err := db.Order("created_at DESC, version DESC, id DESC").Find(&images).Error; err != nil {
		return nil, fmt.Errorf("agentdb: list image versions: %w", err)
	}
	if images == nil {
		images = []*CustomImage{}
	}
	return images, nil
}

// GetCustomImageVersion fetches one catalogue version by (project, name,
// version). The project predicate is mandatory: a version in another project is
// not found, not forbidden — there is no existence leak across projects.
func (s *Store) GetCustomImageVersion(ctx context.Context, project, name string, version int) (*CustomImage, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: project is required (P5)", ErrCustomImageInvalid)
	}
	if err := validateImageName(name); err != nil {
		return nil, err
	}
	if version < 1 {
		return nil, fmt.Errorf("%w: versions start at 1 (got %d)", ErrCustomImageInvalid, version)
	}
	var ci CustomImage
	err := s.gdb.WithContext(ctx).Model(&CustomImage{}).
		Where("customer = ? AND name = ? AND version = ?", project, name, version).
		First(&ci).Error
	if isNotFound(err) {
		return nil, fmt.Errorf("%w: %s:%d in project %s", ErrCustomImageNotFound, name, version, project)
	}
	if err != nil {
		return nil, fmt.Errorf("agentdb: get image version: %w", err)
	}
	return &ci, nil
}

// ResolveCustomImage implements §13.3 resolution at launch time:
//
//   - a bare `name` resolves to the LATEST version of that name in the project —
//     a floating pointer, so curation can publish improvements without touching
//     a single worker row;
//   - `name:version` pins exactly.
//
// Every failure — unknown name, pinned version that was never burned, a version
// whose bytes were reaped, a version with nothing to materialise — is a LOUD
// error. There is deliberately no fallback to the project default and no
// "nearest live version" search: a worker that was pointed at an environment
// and quietly got a different one is exactly the drift §13 exists to prevent.
// Skipping past a dead newest version would be that same silent substitution,
// so a floating reference resolves the newest version and then insists on it.
func (s *Store) ResolveCustomImage(ctx context.Context, project, ref string) (*CustomImage, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: project is required (P5)", ErrCustomImageInvalid)
	}
	parsed, err := ParseImageRef(ref)
	if err != nil {
		return nil, err
	}

	var ci *CustomImage
	if parsed.Pinned() {
		ci, err = s.GetCustomImageVersion(ctx, project, parsed.Name, parsed.Version)
		if err != nil {
			return nil, err
		}
	} else {
		var newest CustomImage
		err := s.gdb.WithContext(ctx).Model(&CustomImage{}).
			Where("customer = ? AND name = ? AND version > 0", project, parsed.Name).
			Order("version DESC").First(&newest).Error
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: no image named %q in project %s — it was never burned, or it was burned in another project (§13.3: resolution never falls back to the project default)",
				ErrCustomImageNotFound, parsed.Name, project)
		}
		if err != nil {
			return nil, fmt.Errorf("agentdb: resolve image %q: %w", ref, err)
		}
		ci = &newest
	}

	if ci.ReapedAt != 0 {
		return nil, fmt.Errorf("%w: %s:%d was reaped at %d by the snapshot_ttl_days reaper — pin an earlier live version or burn a new one (§13.7)",
			ErrCustomImageReaped, ci.Name, ci.Version, ci.ReapedAt)
	}
	if strings.TrimSpace(ci.RegistryHandle) == "" {
		return nil, fmt.Errorf("%w: %s:%d has no registry handle", ErrCustomImageUnmaterialisable, ci.Name, ci.Version)
	}
	return ci, nil
}

// MarkCustomImageReaped tombstones a catalogue version whose bytes the
// snapshot_ttl_days reaper (§5, B4) has deleted. This is HOW §13's append-only
// tool surface and the operator's storage GC are reconciled (§13.7):
//
//   - the reaper deletes the bytes and then calls this — the record survives,
//     so `image_list` history, provenance and the version high-water mark stay
//     intact and a reaped number is never handed out again;
//   - resolution of a tombstoned version fails with ErrCustomImageReaped, which
//     says what happened, instead of resolving to bytes that are gone.
//
// It writes no config event by design: reaping is the operator's storage
// policy, not a configuration decision an agent made, and §15.3's closed
// vocabulary has no verb for it (see ConfigMutationExempt). reapedAt is unix
// seconds; 0 means now. Re-reaping an already-tombstoned version keeps the
// original timestamp — the honest one is when the bytes actually went.
//
// THE B4 RECIPE, so the reaper and this catalogue cannot drift apart:
//
//	cutoff := now - snapshot_ttl_days*86400            // 0 days ⇒ never reap
//	stale, _ := store.ListCustomImageVersions(ctx, ImageCatalogQuery{
//		Project: project, CreatedBefore: cutoff,       // IncludeReaped defaults
//	})                                                 // to false: no re-reaping
//	for _, ci := range stale {
//		// delete the bytes first (imageregistry), THEN tombstone: a crash
//		// between them leaves a resolvable record pointing at missing bytes
//		// for one cycle, which the next pass fixes — the opposite order would
//		// orphan bytes forever.
//		store.MarkCustomImageReaped(ctx, project, ci.Name, ci.Version, 0)
//	}
func (s *Store) MarkCustomImageReaped(ctx context.Context, project, name string, version int, reapedAt int64) error {
	if strings.TrimSpace(project) == "" {
		return fmt.Errorf("%w: project is required (P5)", ErrCustomImageInvalid)
	}
	if err := validateImageName(name); err != nil {
		return err
	}
	if version < 1 {
		return fmt.Errorf("%w: versions start at 1 (got %d)", ErrCustomImageInvalid, version)
	}
	if reapedAt <= 0 {
		reapedAt = time.Now().Unix()
	}
	res := s.gdb.WithContext(ctx).Model(&CustomImage{}).
		Where("customer = ? AND name = ? AND version = ? AND reaped_at = 0", project, name, version).
		Update("reaped_at", reapedAt)
	if res.Error != nil {
		return fmt.Errorf("agentdb: mark image reaped: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// Either it does not exist or it is already tombstoned; only the first
		// is an error, and the reaper deserves to know which.
		if _, err := s.GetCustomImageVersion(ctx, project, name, version); err != nil {
			return err
		}
	}
	return nil
}
