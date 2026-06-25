// Package filesblob is a filesystem-backed reference implementation of
// extension.BlobStore (plus a NewArtifactStore convenience). For
// local/dev/examples — not optimised for production object storage. Stdlib only,
// apart from the generic blobartifacts store its NewArtifactStore delegates to.
package filesblob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/blobartifacts"
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

// NewArtifactStore returns an artifacts.ArtifactStore that keeps bytes in this
// filesystem BlobStore and metadata in memory. The implementation is the
// backend-agnostic blobartifacts store (the historical filesystem-specific copy
// was removed in favour of one shared, tested implementation). Constructor name
// preserved for the examples/server reference.
func NewArtifactStore(blobs *BlobStore) *blobartifacts.ArtifactStore {
	return blobartifacts.New(blobs)
}
