// Package filesblob is a filesystem-backed reference implementation of
// extension.BlobStore and artifacts.ArtifactStore. For local/dev/examples — not
// optimised for production object storage. No external dependencies (stdlib only).
package filesblob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/extension"
)

// ---- BlobStore ---------------------------------------------------------------

// BlobStore stores blobs under root/<key> using the local filesystem. Key
// segments are cleaned and guarded against path-traversal attacks before any
// filesystem operation.
type BlobStore struct{ root string }

// NewBlobStore stores blobs under root/<key>.
func NewBlobStore(root string) *BlobStore { return &BlobStore{root: root} }

// Compile-time assertion.
var _ extension.BlobStore = (*BlobStore)(nil)

// resolve returns the absolute filesystem path for key, returning an error if
// the resolved path would escape root.
func (b *BlobStore) resolve(key string) (string, error) {
	if hasDotDot(key) {
		return "", fmt.Errorf("filesblob: path escapes root: %s", key)
	}
	cleanKey := filepath.Clean("/" + key)
	full := filepath.Join(b.root, cleanKey)
	rootClean := filepath.Clean(b.root)
	if !strings.HasPrefix(full, rootClean+string(os.PathSeparator)) && full != rootClean {
		return "", fmt.Errorf("filesblob: path escapes root: %s", key)
	}
	return full, nil
}

// hasDotDot reports whether s contains a ".." path segment when split on "/"
// or the OS path separator.
func hasDotDot(s string) bool {
	segs := strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == os.PathSeparator })
	return slices.Contains(segs, "..")
}

// Write creates or overwrites the blob at key.
func (b *BlobStore) Write(_ context.Context, key string, r io.Reader) error {
	full, err := b.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	f, err := os.Create(full)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// Read opens a reader for the blob at key.
func (b *BlobStore) Read(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := b.resolve(key)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

// Exists reports whether the blob at key exists.
func (b *BlobStore) Exists(_ context.Context, key string) (bool, error) {
	full, err := b.resolve(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(full)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Delete removes the blob at key. Returns nil if not found.
func (b *BlobStore) Delete(_ context.Context, key string) error {
	full, err := b.resolve(key)
	if err != nil {
		return err
	}
	err = os.Remove(full)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// List returns all keys that share the given prefix.
func (b *BlobStore) List(_ context.Context, prefix string) ([]string, error) {
	rootClean := filepath.Clean(b.root)
	var out []string
	err := filepath.WalkDir(rootClean, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(rootClean, path)
		if relErr != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
		return nil
	})
	return out, err
}

// ---- ArtifactStore -----------------------------------------------------------

// ArtifactStore is a filesystem-backed implementation of artifacts.ArtifactStore.
// Bytes are stored in the BlobStore under _artifacts/<id>; metadata is kept in an
// in-process sync.Mutex-guarded map (for dev/test simplicity — not durable across
// restarts; a production host would persist metadata to SQLite/Postgres instead).
//
// Dedup key: SessionID + "\x00" + FilePath (identical to MockArtifactStore).
// Status machine: Live → Extracted (when bytes saved), Live → Lost (MarkLost on
// no-bytes), Extracted preserved by MarkLost (bytes safe). write-once Source.
type ArtifactStore struct {
	blobs *BlobStore

	mu    sync.Mutex
	byID  map[string]*artifacts.Artifact
	byKey map[string]string // sessionID+"\x00"+filePath → id
	seq   atomic.Int64
}

// NewArtifactStore returns an ArtifactStore backed by blobs for bytes and an
// in-process map for metadata. Constructor name matches examples/server reference.
func NewArtifactStore(blobs *BlobStore) *ArtifactStore {
	return &ArtifactStore{
		blobs: blobs,
		byID:  map[string]*artifacts.Artifact{},
		byKey: map[string]string{},
	}
}

// Compile-time assertion.
var _ artifacts.ArtifactStore = (*ArtifactStore)(nil)

func artKey(sessionID, filePath string) string { return sessionID + "\x00" + filePath }

func (a *ArtifactStore) nextID() string {
	return fmt.Sprintf("art-%d", a.seq.Add(1))
}

// blobKey returns the BlobStore key for artifact bytes.
func blobKey(id string) string { return "_artifacts/bytes/" + id }

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
			prefix := "_artifacts/dirs/" + stored.ID
			entries, err := artifacts.WriteTarToBlobs(ctx, content, func(rel string, r io.Reader) error {
				return a.blobs.Write(ctx, prefix+"/"+rel, r)
			})
			if err != nil {
				return nil, fmt.Errorf("filesblob: Save dir: %w", err)
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
			if err := a.blobs.Write(ctx, bk, content); err != nil {
				return nil, fmt.Errorf("filesblob: Save bytes: %w", err)
			}
			// Re-stat to get file size.
			if full, err := a.blobs.resolve(bk); err == nil {
				if fi, err := os.Stat(full); err == nil {
					stored.FileSize = fi.Size()
				}
			}
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
// metadata-only or the artifact is Lost).
func (a *ArtifactStore) Load(ctx context.Context, artifactID string) (*artifacts.Artifact, io.ReadCloser, error) {
	a.mu.Lock()
	art, ok := a.byID[artifactID]
	if !ok {
		a.mu.Unlock()
		return nil, nil, fmt.Errorf("filesblob: artifact %q not found", artifactID)
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
	rc, err := a.blobs.Read(ctx, bk)
	if os.IsNotExist(err) {
		return &out, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("filesblob: Load bytes: %w", err)
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
