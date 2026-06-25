// Package artifacts defines ArtifactStore — persistence for the individual,
// user-facing files an agent produces (a report, a chart JSON, a generated web
// app). This is DISTINCT from snapshots (whole-filesystem images for resume); see
// docs/06-artifacts.md.
//
// The status state machine and dedup/never-regress rules are ported from
// Platinum's store_agent_artifacts.go because they are hard-won and correct.
package artifacts

import (
	"context"
	"io"
)

// ArtifactStore persists and retrieves agent artifacts. The real impl wraps a
// metadata store (rows) + a BlobStore (bytes); the mock keeps both in memory with
// identical semantics.
type ArtifactStore interface {
	// Save upserts metadata (dedup on SessionID+FilePath) and, when content is
	// non-nil, persists bytes and sets Status=Extracted. Preserves the
	// live -> extracted, never-regress rule and write-once Source.
	//
	// When art.IsDir is true, content MUST be a tar stream: the impl untars it and
	// writes one blob per regular file under the artifact's blob PREFIX (BlobPath),
	// sets FileSize to the sum of entry sizes, and (for stores that support a Meta map)
	// records the dir digest in Meta["dirDigest"]. When art.IsDir is false, content is stored as a single blob.
	Save(ctx context.Context, art *Artifact, content io.Reader) (*Artifact, error)

	// Load returns metadata plus an open reader for the bytes. reader is nil if the
	// artifact is metadata-only (e.g. Lost).
	Load(ctx context.Context, artifactID string) (*Artifact, io.ReadCloser, error)

	// List returns all artifacts for a session.
	List(ctx context.Context, sessionID string) ([]*Artifact, error)

	// MarkLost flags still-Live artifacts for a session as Lost — but PROMOTES to
	// Extracted any that already have a BlobPath (the bytes are safe even though
	// the instance is gone).
	MarkLost(ctx context.Context, sessionID string) error

	// CaptureFolder slurps a named set of files (or a single file — the degenerate
	// case) from the supplied reader (typically a tar stream produced by
	// ExecutionEnvironment.Exec+tar or the in-image /workspace/files/* endpoint),
	// saves all bytes as a single artifact identified by (sessionID, name), and
	// returns the saved artifact.  The content is stored under the artifact's
	// FilePath = name (the caller can choose any path-safe name).
	//
	// This is the generalised folder/file-set capture described in docs/06-artifacts.md
	// and is the building block for user images (AG-7).
	CaptureFolder(ctx context.Context, sessionID, name string, content io.Reader) (*Artifact, error)
}

// Artifact is the generic, portable artifact shape (a redefinition of Platinum's
// types.AgentArtifact so the library imports nothing from goapi).
type Artifact struct {
	ID           string            `json:"id"`
	SessionID    string            `json:"sessionId"`
	FilePath     string            `json:"filePath"` // dedup key with SessionID
	ArtifactType string            `json:"artifactType"` // "file" | "code" | "image" | "data" | "webapp" (extensible)
	Status       Status            `json:"status"`
	BlobPath     string            `json:"blobPath"`
	Label        string            `json:"label"`
	Description  string            `json:"description"`
	MimeType     string            `json:"mimeType"`
	FileSize     int64             `json:"fileSize"`
	Source       string            `json:"source"` // "tool" | "auto" | "upload" — write-once
	IsDir        bool              `json:"isDir"`  // when true, BlobPath is a PREFIX and bytes are one-blob-per-file
	Meta         map[string]string `json:"meta,omitempty"` // host-specific fields live here to keep the type portable
}

// Status is the artifact lifecycle state.
type Status string

const (
	StatusLive             Status = "live"
	StatusExtracted        Status = "extracted"
	StatusLost             Status = "lost"
	StatusExtractionFailed Status = "extraction_failed"
)
