// Package blobartifacts is a generic artifacts.ArtifactStore backed by any
// extension.BlobStore for bytes, with metadata held in an in-process map.
//
// It is backend-agnostic: pair it with extension/filesblob for local/dev or
// extension/gcsblob for Google Cloud Storage. The metadata map is NOT durable
// across restarts (a production host persists metadata to SQLite/Postgres and
// keeps only the bytes in the BlobStore); this mirrors the filesblob reference
// and is meant for the standalone stack and tests.
//
// Dedup key: SessionID + "\x00" + FilePath. Status machine: Live → Extracted
// (bytes saved), Live → Lost (MarkLost with no bytes), Extracted preserved by
// MarkLost (bytes safe), write-once Source. These rules are ported verbatim from
// the proven MockArtifactStore / filesblob implementations.
package blobartifacts

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/extension"
)

// ArtifactStore stores artifact bytes in a BlobStore and metadata in memory.
type ArtifactStore struct {
	blobs extension.BlobStore

	mu    sync.Mutex
	byID  map[string]*artifacts.Artifact
	byKey map[string]string // sessionID+"\x00"+filePath → id
	seq   atomic.Int64
}

// New returns an ArtifactStore backed by blobs for bytes and an in-process map
// for metadata.
func New(blobs extension.BlobStore) *ArtifactStore {
	return &ArtifactStore{
		blobs: blobs,
		byID:  map[string]*artifacts.Artifact{},
		byKey: map[string]string{},
	}
}

// Compile-time assertion.
var _ artifacts.ArtifactStore = (*ArtifactStore)(nil)

func artKey(sessionID, filePath string) string { return sessionID + "\x00" + filePath }

func (a *ArtifactStore) nextID() string { return fmt.Sprintf("art-%d", a.seq.Add(1)) }

// blobKey returns the BlobStore key for a file artifact's bytes.
func blobKey(id string) string { return "_artifacts/bytes/" + id }

// dirPrefix returns the BlobStore key prefix for a directory artifact's blobs.
func dirPrefix(id string) string { return "_artifacts/dirs/" + id }

// countingReader counts bytes read, so we can record FileSize without re-statting
// the backend (which a generic BlobStore can't do).
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Save upserts artifact metadata, optionally uploading bytes. When content is
// non-nil, bytes are stored and Status is set to Extracted.
func (a *ArtifactStore) Save(ctx context.Context, art *artifacts.Artifact, content io.Reader) (*artifacts.Artifact, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	k := artKey(art.SessionID, art.FilePath)
	stored := *art // copy so we don't mutate the caller's struct

	if existingID, ok := a.byKey[k]; ok {
		// Upsert: merge onto the existing row, applying the invariants.
		prev := a.byID[existingID]
		stored.ID = prev.ID
		// Never regress Extracted → Live.
		if prev.Status == artifacts.StatusExtracted && stored.Status == artifacts.StatusLive {
			stored.Status = artifacts.StatusExtracted
		}
		// Preserve a blob path the caller didn't supply.
		if stored.BlobPath == "" {
			stored.BlobPath = prev.BlobPath
		}
		// Source is write-once.
		if prev.Source != "" {
			stored.Source = prev.Source
		}
	} else {
		if stored.ID == "" {
			stored.ID = a.nextID()
		}
		a.byKey[k] = stored.ID
	}

	if content != nil {
		if stored.IsDir {
			prefix := dirPrefix(stored.ID)
			entries, err := artifacts.WriteTarToBlobs(ctx, content, func(rel string, r io.Reader) error {
				return a.blobs.Write(ctx, prefix+"/"+rel, r)
			})
			if err != nil {
				return nil, fmt.Errorf("blobartifacts: Save dir: %w", err)
			}
			var total int64
			for _, e := range entries {
				total += e.Size
			}
			stored.FileSize = total
			stored.Status = artifacts.StatusExtracted
			if stored.BlobPath == "" {
				stored.BlobPath = prefix
			}
			if stored.Meta == nil {
				stored.Meta = map[string]string{}
			}
			stored.Meta["dirDigest"] = artifacts.DirDigest(entries)
		} else {
			bk := blobKey(stored.ID)
			cr := &countingReader{r: content}
			if err := a.blobs.Write(ctx, bk, cr); err != nil {
				return nil, fmt.Errorf("blobartifacts: Save bytes: %w", err)
			}
			stored.FileSize = cr.n
			stored.Status = artifacts.StatusExtracted
			if stored.BlobPath == "" {
				stored.BlobPath = bk
			}
		}
	}

	cp := stored
	a.byID[stored.ID] = &cp
	out := stored
	return &out, nil
}

// Load returns metadata and an open reader for the bytes (nil reader if
// metadata-only, a directory artifact, or Lost).
func (a *ArtifactStore) Load(ctx context.Context, artifactID string) (*artifacts.Artifact, io.ReadCloser, error) {
	a.mu.Lock()
	art, ok := a.byID[artifactID]
	if !ok {
		a.mu.Unlock()
		return nil, nil, fmt.Errorf("blobartifacts: artifact %q not found", artifactID)
	}
	out := *art
	a.mu.Unlock()

	if out.IsDir {
		// Directory artifacts store one blob per file under BlobPath (a prefix);
		// there is no single byte stream to Load. Callers materialize a dir by
		// listing BlobStore.List(out.BlobPath). Return metadata with a nil reader.
		return &out, nil, nil
	}

	if out.Status == artifacts.StatusLost || out.BlobPath == "" {
		return &out, nil, nil
	}

	bk := blobKey(artifactID)
	// Distinguish "bytes gone" (return metadata + nil reader, matching the
	// filesblob reference) from a real backend error (propagate it).
	exists, err := a.blobs.Exists(ctx, bk)
	if err != nil {
		return nil, nil, fmt.Errorf("blobartifacts: Load exists: %w", err)
	}
	if !exists {
		return &out, nil, nil
	}
	rc, err := a.blobs.Read(ctx, bk)
	if err != nil {
		return nil, nil, fmt.Errorf("blobartifacts: Load bytes: %w", err)
	}
	return &out, rc, nil
}

// List returns all artifacts for a session.
func (a *ArtifactStore) List(_ context.Context, sessionID string) ([]*artifacts.Artifact, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []*artifacts.Artifact{}
	for _, art := range a.byID {
		if art.SessionID == sessionID {
			cp := *art
			out = append(out, &cp)
		}
	}
	return out, nil
}

// MarkLost flags still-Live artifacts as Lost — but promotes to Extracted any
// that already have a BlobPath (bytes safe).
func (a *ArtifactStore) MarkLost(_ context.Context, sessionID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, art := range a.byID {
		if art.SessionID != sessionID || art.Status != artifacts.StatusLive {
			continue
		}
		if art.BlobPath != "" {
			art.Status = artifacts.StatusExtracted
		} else {
			art.Status = artifacts.StatusLost
		}
	}
	return nil
}

// CaptureFolder saves the full content (typically a tar stream) as a single
// artifact with FilePath=name and ArtifactType="folder-capture".
func (a *ArtifactStore) CaptureFolder(ctx context.Context, sessionID, name string, content io.Reader) (*artifacts.Artifact, error) {
	return a.Save(ctx, &artifacts.Artifact{
		SessionID:    sessionID,
		FilePath:     name,
		ArtifactType: "folder-capture",
		Status:       artifacts.StatusLive,
		Source:       "capture",
	}, content)
}
