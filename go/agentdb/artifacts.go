package agentdb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

func (s *Store) CreateArtifact(ctx context.Context, artifact *Artifact) (*Artifact, error) {
	if artifact.ID == "" {
		artifact.ID = uuid.New().String()
	}
	if artifact.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if err := s.gdb.WithContext(ctx).Create(artifact).Error; err != nil {
		return nil, fmt.Errorf("failed to create agent artifact: %w", err)
	}
	return artifact, nil
}

func (s *Store) UpsertArtifact(ctx context.Context, artifact *Artifact) (*Artifact, error) {
	if artifact.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if artifact.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	var existing Artifact
	err := s.gdb.WithContext(ctx).
		Where("session_id = ? AND file_path = ?", artifact.SessionID, artifact.FilePath).
		First(&existing).Error

	if err == nil {
		existing.Label = artifact.Label
		existing.Description = artifact.Description
		existing.FileSize = artifact.FileSize
		existing.MimeType = artifact.MimeType
		existing.ArtifactType = artifact.ArtifactType
		existing.PublishToFiles = artifact.PublishToFiles
		existing.IsDir = artifact.IsDir

		if artifact.Status == "extracted" {
			existing.Status = "extracted"
		} else if existing.Status != "extracted" {
			existing.Status = "live"
		}
		if artifact.AzureBlobPath != "" {
			existing.AzureBlobPath = artifact.AzureBlobPath
		}
		if artifact.Source != "" {
			existing.Source = artifact.Source
		}

		if err := s.gdb.WithContext(ctx).Save(&existing).Error; err != nil {
			return nil, fmt.Errorf("failed to update agent artifact: %w", err)
		}
		return &existing, nil
	}

	if artifact.ID == "" {
		artifact.ID = uuid.New().String()
	}
	if err := s.gdb.WithContext(ctx).Create(artifact).Error; err != nil {
		return nil, fmt.Errorf("failed to create agent artifact: %w", err)
	}
	return artifact, nil
}

func (s *Store) ListArtifacts(ctx context.Context, sessionID string) ([]*Artifact, error) {
	var artifacts []*Artifact
	db := s.gdb.WithContext(ctx).Model(&Artifact{})
	if sessionID != "" {
		db = db.Where("session_id = ?", sessionID)
	}
	if err := db.Order("created_at DESC").Find(&artifacts).Error; err != nil {
		return nil, fmt.Errorf("failed to list agent artifacts: %w", err)
	}
	if artifacts == nil {
		artifacts = []*Artifact{}
	}
	return artifacts, nil
}

func (s *Store) GetArtifact(ctx context.Context, id string) (*Artifact, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot get agent artifact without ID")
	}
	var artifact Artifact
	if err := s.gdb.WithContext(ctx).Where("id = ?", id).First(&artifact).Error; err != nil {
		return nil, fmt.Errorf("failed to get agent artifact: %w", err)
	}
	return &artifact, nil
}

func (s *Store) MarkArtifactsExtracted(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	return s.gdb.WithContext(ctx).Model(&Artifact{}).
		Where("session_id = ? AND status = ?", sessionID, "live").
		Update("status", "extracted").Error
}

func (s *Store) UpdateArtifact(ctx context.Context, artifact *Artifact) (*Artifact, error) {
	if artifact.ID == "" {
		return nil, fmt.Errorf("cannot update agent artifact without ID")
	}
	if err := s.gdb.WithContext(ctx).Save(artifact).Error; err != nil {
		return nil, fmt.Errorf("failed to update agent artifact: %w", err)
	}
	return artifact, nil
}

func (s *Store) CreateArtifacts(ctx context.Context, artifacts []*Artifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	for _, a := range artifacts {
		if a.ID == "" {
			a.ID = uuid.New().String()
		}
	}
	if err := s.gdb.WithContext(ctx).CreateInBatches(artifacts, 100).Error; err != nil {
		return fmt.Errorf("failed to create agent artifacts: %w", err)
	}
	return nil
}

func (s *Store) MarkArtifactsLost(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	s.gdb.WithContext(ctx).Model(&Artifact{}).
		Where("session_id = ? AND status = ? AND azure_blob_path != ''", sessionID, "live").
		Update("status", "extracted")
	return s.gdb.WithContext(ctx).Model(&Artifact{}).
		Where("session_id = ? AND status = ? AND (azure_blob_path = '' OR azure_blob_path IS NULL)", sessionID, "live").
		Update("status", "lost").Error
}
