package agentdb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// This file holds two catalogues in one table (agent_skills).
//
//  1. The LEGACY host-built catalogue (UpsertSkill / ListSkills / GetSkill /
//     SetSkillVisibility / DeleteSkill): latest-wins rows keyed by a visibility
//     scope, carrying a content hash and a blob prefix, with no markdown.
//
//  2. The §14 CATALOGUE (CreateSkill / ListProjectSkills / GetProjectSkill):
//     project-scoped, labeled, append-only records that an agent writes from
//     inside a session, pairing a markdown document with its install script.
//
// THE DISCRIMINATOR. §13 rows are told apart from legacy image rows by
// `version > 0`; §14 has no version column (identity is the name alone, §14.1),
// so the discriminator is `markdown <> ''`. Markdown is REQUIRED on a §14 write
// and the legacy path never sets it, so the two populations cannot overlap —
// and a legacy row can never be listed, read or installed as a §14 skill.
//
// PROJECT NAMESPACE. §14.1 calls the namespace column `project`; this table has
// called it `customer` since migration 013 and J1 already treats it as the
// project when it writes `skill_create` config events. As with the §13
// catalogue, the §14 API speaks `project` and the column stays `customer`,
// mapped in exactly one place — here.
//
// APPEND-ONLY (§14.1): there is no UpdateSkill and no §14 delete. `skill_create`
// on an existing name records a NEW revision and resolution is newest-wins; the
// superseded revisions stay as an honest record of how the capability was taught
// over time.
// ---------------------------------------------------------------------------

// scopeWhere returns the GORM where-clause that identifies the unique slot a
// skill occupies, by visibility boundary: private → (owner_email, name);
// organizational → (customer, name); public → (name). Used for latest-wins upsert.
//
// It excludes §14 catalogue rows (`markdown <> ”`): a legacy latest-wins upsert
// that happened to share a name with a project skill would otherwise OVERWRITE
// an append-only revision — silently, and picking an arbitrary one of several.
func scopeWhere(db *gorm.DB, sk *Skill) *gorm.DB {
	db = db.Where("markdown = ?", "")
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
//
// Configuration mutation: the catalog row and a `skill_create` config event are
// written in one transaction (§15.3, §15.4). cw carries the acting worker /
// session and an optional rationale; human/API callers pass the zero value.
// The skill's Customer is its project namespace and is therefore required.
func (s *Store) UpsertSkill(ctx context.Context, sk *Skill, cw ConfigWrite) (*Skill, error) {
	if sk.Name == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	if sk.Customer == "" {
		return nil, fmt.Errorf("skill customer (project) is required")
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
		existing.CreatedBySession = sk.CreatedBySession
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: existing.Customer,
			Action:  ActionSkillCreate,
			Payload: &existing,
			Write:   cw,
		}, func(tx *gorm.DB) error {
			return tx.Save(&existing).Error
		}); err != nil {
			return nil, fmt.Errorf("failed to update agent skill: %w", err)
		}
		return &existing, nil
	}

	if sk.ID == "" {
		sk.ID = uuid.New().String()
	}
	if _, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: sk.Customer,
		Action:  ActionSkillCreate,
		Payload: sk,
		Write:   cw,
	}, func(tx *gorm.DB) error {
		return tx.Create(sk).Error
	}); err != nil {
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

// ═══════════════════════════════════════════════════════════════════════════
// §14 — project-scoped, labeled, append-only skills
// ═══════════════════════════════════════════════════════════════════════════

// Errors of the §14 catalogue, sentinels so the tool layer (I3) can tell "you
// asked for something that was never recorded" from "you asked wrongly" without
// string-matching.
var (
	// ErrSkillNotFound is returned when no §14 revision of that name exists in
	// this project. A legacy (markdown-less) row of the same name does not
	// count: it is not a project skill and has nothing to install.
	ErrSkillNotFound = errors.New("agentdb: skill not found")
	// ErrSkillInvalid wraps every validation failure on a catalogue write.
	ErrSkillInvalid = errors.New("agentdb: invalid skill")
)

// skillNameRe is the charset of a §14 skill name: kebab-case (§14.1).
//
// It is deliberately NARROWER than the image-name charset, which allows '.' and
// '_'. A skill name becomes a DIRECTORY NAME in the harness's skills tree
// (§14.2, `skill_install`), so the charset is the first line of defence against
// a name that escapes that tree — no dots, no slashes, nothing to traverse with.
var skillNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// maxSkillNameLen matches the `name` column width.
const maxSkillNameLen = 255

// ValidateSkillName reports whether name is a legal §14 skill name. Exported
// because the tool layer and the sandbox route both need the same rule, and two
// spellings of "kebab-case" would eventually disagree.
func ValidateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: skill name is required", ErrSkillInvalid)
	}
	if len(name) > maxSkillNameLen {
		return fmt.Errorf("%w: skill name is %d chars, max %d", ErrSkillInvalid, len(name), maxSkillNameLen)
	}
	if !skillNameRe.MatchString(name) {
		return fmt.Errorf("%w: skill name %q is invalid: kebab-case only — lowercase alphanumerics and '-', starting and ending alphanumeric (it becomes a directory name when the skill is installed)", ErrSkillInvalid, name)
	}
	return nil
}

// CreateSkill appends a revision of `name` in `project` — §14.2's `skill_create`
// at the store level.
//
// There is no update: recording an improved skill under an existing name writes
// a NEW row, and every read takes the newest (§14.1). Nothing is overwritten, so
// the history of how a capability was taught survives.
//
// Configuration mutation: the catalogue row and a `skill_create` config event
// are written in one transaction (§15.3, §15.4). cw carries the acting worker /
// session and an optional rationale; human/API callers pass the zero value.
// Provenance (CreatedByWorker / CreatedBySession, §14.1) is taken from cw when
// the caller has not set it explicitly, so the tool layer cannot record one
// actor in the log and another in the catalogue.
//
// The stored row is read back by id and returned (§9), never the caller's
// struct — by id and not "the newest", because a concurrent create of the same
// name would otherwise make a caller echo somebody else's revision.
func (s *Store) CreateSkill(ctx context.Context, sk *Skill, cw ConfigWrite) (*Skill, error) {
	if sk == nil {
		return nil, fmt.Errorf("%w: skill is required", ErrSkillInvalid)
	}
	if strings.TrimSpace(sk.Customer) == "" {
		return nil, fmt.Errorf("%w: project is required (P5: the namespace is never inferred)", ErrSkillInvalid)
	}
	if err := ValidateSkillName(sk.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(sk.Markdown) == "" {
		// Not a formality: markdown is what makes a row a §14 skill at all (see
		// the discriminator note at the top of this file), and a skill without
		// its document is a Dockerfile nobody knows to use (§14.1).
		return nil, fmt.Errorf("%w: markdown is required — a skill is knowledge plus its install, and a skill with no document cannot be taught (§14.1)", ErrSkillInvalid)
	}
	if err := ValidateLabels(sk.Labels); err != nil {
		return nil, fmt.Errorf("%w: labels: %w", ErrSkillInvalid, err)
	}
	if sk.Revision != 0 {
		return nil, fmt.Errorf("%w: revision is allocated by the store, not supplied (got %d)", ErrSkillInvalid, sk.Revision)
	}
	normalizeCatalogueSkill(sk, cw)

	var lastErr error
	for attempt := 0; attempt < skillRevisionAllocAttempts; attempt++ {
		next, err := s.nextSkillRevision(ctx, sk.Customer, sk.Name)
		if err != nil {
			return nil, err
		}
		sk.ID = uuid.New().String()
		sk.Revision = next
		if _, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: sk.Customer,
			Action:  ActionSkillCreate,
			Payload: sk, // the FULL new row, revision included (§15.2)
			Write:   cw,
		}, func(tx *gorm.DB) error {
			return tx.Create(sk).Error
		}); err != nil {
			sk.Revision = 0 // leave the caller's struct as it was found
			if isUniqueViolation(err) {
				lastErr = err
				continue // someone else took this revision; re-read and retry
			}
			return nil, fmt.Errorf("agentdb: create skill revision: %w", err)
		}
		return s.getSkillRevision(ctx, sk.Customer, sk.ID)
	}
	return nil, fmt.Errorf("agentdb: could not allocate a revision for skill %q after %d attempts: %w",
		sk.Name, skillRevisionAllocAttempts, lastErr)
}

// skillRevisionAllocAttempts bounds the retry loop that allocates a revision.
// Two sessions hoisting the same skill at the same instant collide on the
// partial unique index; the loser re-reads the high-water mark and tries again.
const skillRevisionAllocAttempts = 5

// nextSkillRevision reads the high-water mark for (project, name). Like the
// image-version equivalent it runs outside the config-event transaction: the
// read is only a hint, and the unique index is what makes allocation correct.
// Legacy rows carry revision 0, so they neither block nor skew the count.
func (s *Store) nextSkillRevision(ctx context.Context, project, name string) (int, error) {
	var highest int
	if err := s.gdb.WithContext(ctx).Model(&Skill{}).
		Where("customer = ? AND name = ?", project, name).
		Select("COALESCE(MAX(revision), 0)").
		Scan(&highest).Error; err != nil {
		return 0, fmt.Errorf("agentdb: read skill revision high-water mark: %w", err)
	}
	return highest + 1, nil
}

// normalizeCatalogueSkill applies the catalogue's defaults. In code rather than
// in gorm `default:` tags, for the reason recorded on the Skill struct.
func normalizeCatalogueSkill(sk *Skill, cw ConfigWrite) {
	if sk.Labels == nil {
		sk.Labels = LabelSet{}
	}
	if sk.Visibility == "" {
		// §14 has no visibility concept — a project skill belongs to its project
		// (P5). The column is NOT NULL and the legacy read paths filter on it, so
		// catalogue rows take the project-wide value.
		sk.Visibility = "organizational"
	}
	if sk.CreatedByWorker == "" {
		sk.CreatedByWorker = cw.Worker
	}
	if sk.CreatedBySession == "" {
		sk.CreatedBySession = cw.Session
	}
}

// getSkillRevision fetches one §14 revision by id within a project. The project
// predicate is mandatory: a revision in another project is not found, not
// forbidden — there is no existence leak across projects.
func (s *Store) getSkillRevision(ctx context.Context, project, id string) (*Skill, error) {
	var sk Skill
	err := s.gdb.WithContext(ctx).Model(&Skill{}).
		Where("customer = ? AND id = ? AND markdown <> ?", project, id, "").
		First(&sk).Error
	if isNotFound(err) {
		return nil, fmt.Errorf("%w: revision %s in project %s", ErrSkillNotFound, id, project)
	}
	if err != nil {
		return nil, fmt.Errorf("agentdb: get skill revision: %w", err)
	}
	return &sk, nil
}

// GetProjectSkill returns the CURRENT revision of `name` in `project` — the
// newest row, per §14.1's newest-wins resolution — in full, markdown and
// install script included. This is §14.2's `skill_get` at the store level.
func (s *Store) GetProjectSkill(ctx context.Context, project, name string) (*Skill, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: project is required (P5)", ErrSkillInvalid)
	}
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	var sk Skill
	err := s.gdb.WithContext(ctx).Model(&Skill{}).
		Where("customer = ? AND name = ? AND markdown <> ?", project, name, "").
		// By revision, NOT by created_at: created_at is seconds on this table
		// and the id is a random uuid, so two teachings inside one second would
		// order by coin toss and this could return the superseded document.
		// Revision order is allocation order (migration 028).
		Order("revision DESC").
		First(&sk).Error
	if isNotFound(err) {
		return nil, fmt.Errorf("%w: no skill named %q in project %s", ErrSkillNotFound, name, project)
	}
	if err != nil {
		return nil, fmt.Errorf("agentdb: get project skill: %w", err)
	}
	return &sk, nil
}

// SkillSummary is one entry of §14.2's `skill_list`: identity, labels and
// provenance — never the markdown. It exists as its own type so the list query
// can SELECT the cheap columns: a project's skill documents can be large, and
// listing a catalogue must not drag every one of them across the wire.
type SkillSummary struct {
	ID               string   `json:"id"`
	Project          string   `json:"project"`
	Name             string   `json:"name"`
	Labels           LabelSet `json:"labels"`
	Revision         int      `json:"revision"`
	CreatedByWorker  string   `json:"created_by_worker"`
	CreatedBySession string   `json:"created_by_session"`
	CreatedAt        int64    `json:"created_at"`
	// HasInstallScript says whether installing this skill will run anything.
	// Cheap to compute, and the one thing a reader wants that is not identity,
	// labels or provenance: "does this bring software, or only knowledge?".
	HasInstallScript bool `json:"has_install_script"`
	// Revisions is how many times this skill has been recorded (§14.1). 1 means
	// it was taught once; more means it was improved, and the older documents
	// are still there.
	Revisions int `json:"revisions"`
}

// SkillCatalogQuery filters the §14 catalogue. Project is REQUIRED and is the
// only tenancy boundary: skill names never cross projects (P5, §14.1).
type SkillCatalogQuery struct {
	// Project is the hard namespace (the `customer` column). Required.
	Project string
	// LabelSelector is Kubernetes-style selector text (§7.2), parsed by the one
	// selector parser the whole system shares.
	LabelSelector string
	// Limit caps the result. 0 = no limit.
	Limit int
}

// ListProjectSkills returns the project's skills newest first — §14.2's
// `skill_list` at the store level. One entry PER NAME, carrying the newest
// revision, because §14.1 gives a skill no version: its identity is its name,
// and the current teaching is the newest one.
//
// The label selector is applied to the surviving (newest) revision, IN GO, and
// that ordering is the whole point. Filtering in SQL first would let an old
// revision that still carries a label surface as if it were current — exactly
// the silent substitution newest-wins exists to prevent. Matching in Go also
// means `skill_list` works on every backend, where the §13 image selector needs
// Postgres jsonb; the parser is D1's either way, never a second one.
func (s *Store) ListProjectSkills(ctx context.Context, q SkillCatalogQuery) ([]*SkillSummary, error) {
	if strings.TrimSpace(q.Project) == "" {
		return nil, fmt.Errorf("%w: project is required (P5)", ErrSkillInvalid)
	}
	var sel *LabelSelector
	if strings.TrimSpace(q.LabelSelector) != "" {
		parsed, err := ParseLabelSelector(q.LabelSelector)
		if err != nil {
			return nil, fmt.Errorf("%w: label selector: %w", ErrSkillInvalid, err)
		}
		sel = parsed
	}

	var rows []*SkillSummary
	if err := s.gdb.WithContext(ctx).Model(&Skill{}).
		Select("id, customer AS project, name, labels, revision, created_by_worker, "+
			"source_session_id AS created_by_session, created_at, "+
			"(install_sh <> '') AS has_install_script").
		Where("customer = ? AND markdown <> ?", q.Project, "").
		// Grouped by name, newest revision of each first — so the first row seen
		// for a name IS its current revision, deterministically (migration 028).
		Order("name ASC, revision DESC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("agentdb: list project skills: %w", err)
	}

	out := make([]*SkillSummary, 0, len(rows))
	byName := map[string]*SkillSummary{}
	for _, r := range rows {
		if cur, seen := byName[r.Name]; seen {
			cur.Revisions++
			continue
		}
		r.Revisions = 1
		byName[r.Name] = r
		out = append(out, r)
	}
	if sel != nil && !sel.Empty() {
		kept := out[:0]
		for _, r := range out {
			if sel.Matches(r.Labels) {
				kept = append(kept, r)
			}
		}
		out = kept
	}
	// Newest first (§14.2). created_at is seconds, so the name is the tiebreak:
	// arbitrary, but stable and explicable — unlike ordering by a random uuid,
	// which would shuffle a catalogue between two identical calls.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].Name < out[j].Name
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	if out == nil {
		out = []*SkillSummary{}
	}
	return out, nil
}
