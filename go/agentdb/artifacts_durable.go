package agentdb

// Durable-artifact metadata: the store half of extension/dbartifacts.
//
// `agent_artifacts` has existed since migration 002 with full CRUD, but nothing
// wired it — agentd ran extension/blobartifacts, whose index is an in-process
// map, so restarting the process lost every artifact row while the bytes stayed
// in the BlobStore, orphaned. These methods are the durable index.
//
// They deliberately do NOT reuse UpsertArtifact. That method's merge lets a
// later write overwrite `source`, and the artifacts.ArtifactStore contract says
// Source is write-once; changing UpsertArtifact would have altered a shipped,
// tested method for callers that are not this one. SaveArtifactRecord is the
// one place the ArtifactStore contract is enforced, and it enforces it inside a
// transaction so a concurrent Save on the same (session_id, file_path) cannot
// interleave a read with a write.
//
// No config events: artifacts are runtime state, not configuration (§15.3
// rule 3). `agent_artifacts` is not a guarded projection table, and none of
// these method names trips the conformance classifier.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GetArtifactByPath returns the artifact for a (session_id, file_path) pair —
// the dedup key of the ArtifactStore contract — or (nil, nil) when there is
// none. A missing row is not an error: Save uses this to choose insert vs
// update.
func (s *Store) GetArtifactByPath(ctx context.Context, sessionID, filePath string) (*Artifact, error) {
	if sessionID == "" || filePath == "" {
		return nil, nil
	}
	var a Artifact
	err := s.gdb.WithContext(ctx).
		Where("session_id = ? AND file_path = ?", sessionID, filePath).
		First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get agent artifact by path: %w", err)
	}
	return &a, nil
}

// SaveArtifactRecord upserts one artifact row, applying the
// artifacts.ArtifactStore invariants in a single transaction:
//
//   - dedup on (session_id, file_path);
//   - status never regresses extracted → live;
//   - a blob path the caller did not supply is preserved;
//   - source is WRITE-ONCE (the first non-empty value wins);
//   - customer is preserved when the caller supplies none, so a later write
//     cannot blank a row's tenancy stamp.
//
// Every other field is taken from `in`, matching the in-process store this
// replaces (a Save without content is a whole-metadata write).
func (s *Store) SaveArtifactRecord(ctx context.Context, in *Artifact) (*Artifact, error) {
	if in == nil {
		return nil, fmt.Errorf("artifact is required")
	}
	if in.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if in.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	var out Artifact
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var prev Artifact
		err := tx.Where("session_id = ? AND file_path = ?", in.SessionID, in.FilePath).
			First(&prev).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			out = *in
			if out.ID == "" {
				out.ID = uuid.New().String()
			}
			return tx.Create(&out).Error
		case err != nil:
			return err
		}

		merged := *in
		merged.ID = prev.ID
		merged.CreatedAt = prev.CreatedAt
		if prev.Status == string(statusExtracted) && merged.Status == string(statusLive) {
			merged.Status = prev.Status
		}
		if merged.AzureBlobPath == "" {
			merged.AzureBlobPath = prev.AzureBlobPath
		}
		if prev.Source != "" {
			merged.Source = prev.Source
		}
		if merged.Customer == "" {
			merged.Customer = prev.Customer
		}
		if merged.UserEmail == "" {
			merged.UserEmail = prev.UserEmail
		}
		if err := tx.Save(&merged).Error; err != nil {
			return err
		}
		out = merged
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to save agent artifact: %w", err)
	}
	return &out, nil
}

// artifactStatus mirrors the artifacts package's Status constants. agentdb must
// not import the artifacts package (it is the lower layer), so the two strings
// the merge rule needs are restated here; artifacts_durable_test.go pins them
// against a literal.
type artifactStatus string

const (
	statusLive      artifactStatus = "live"
	statusExtracted artifactStatus = "extracted"
)

// ListArtifactsForCustomer returns a session's artifacts, but ONLY when the row
// belongs to `customer`. Cross-project reads return an empty list rather than
// an error, so a caller cannot probe another project's session IDs for
// existence (§12).
//
// An empty customer means "unscoped" and is accepted only because artifacts
// written before a session had a customer stamp (and the sqlite fallback) carry
// none; it falls back to ListArtifacts. Callers that hold a project claim must
// always pass it.
func (s *Store) ListArtifactsForCustomer(ctx context.Context, customer, sessionID string) ([]*Artifact, error) {
	if customer == "" {
		return s.ListArtifacts(ctx, sessionID)
	}
	var rows []*Artifact
	db := s.gdb.WithContext(ctx).Model(&Artifact{}).Where("customer = ?", customer)
	if sessionID != "" {
		db = db.Where("session_id = ?", sessionID)
	}
	if err := db.Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list agent artifacts: %w", err)
	}
	if rows == nil {
		rows = []*Artifact{}
	}
	return rows, nil
}

// GetArtifactForCustomer returns one artifact by ID, but ONLY when it belongs
// to `customer`. Another project's artifact reads as not-found — the same
// answer as a nonexistent ID, so existence is not leaked.
func (s *Store) GetArtifactForCustomer(ctx context.Context, customer, id string) (*Artifact, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot get agent artifact without ID")
	}
	if customer == "" {
		return s.GetArtifact(ctx, id)
	}
	var a Artifact
	if err := s.gdb.WithContext(ctx).
		Where("id = ? AND customer = ?", id, customer).
		First(&a).Error; err != nil {
		return nil, fmt.Errorf("failed to get agent artifact: %w", err)
	}
	return &a, nil
}
