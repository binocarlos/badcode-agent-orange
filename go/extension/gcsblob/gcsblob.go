// Package gcsblob is a Google Cloud Storage-backed implementation of
// extension.BlobStore and extension.BlobStoreFactory. A single factory serves
// BOTH artifact bytes and session snapshots, since both write through
// extension.BlobStore (snapshots via imageregistry/blobarchive, artifacts via
// an ArtifactStore the host pairs with this byte backend).
//
// Authentication uses Application Default Credentials (ADC): workload identity
// on GCP, gcloud credentials locally, or a service-account key pointed to by
// GOOGLE_APPLICATION_CREDENTIALS. No credentials are configured in this
// package — that is deliberate, so the same code runs unchanged across those
// environments. Tests may inject option.ClientOption (e.g. a fake server).
//
// Keys are opaque "/"-separated strings. The factory binds the bucket and an
// optional root prefix; ForSession/Global scope further by namespace. List
// returns store-relative keys (the store prefix stripped) so results round-trip
// back through Read/Exists/Delete.
package gcsblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/binocarlos/badcode-agent-orange/extension"
)

// Config configures a GCS-backed BlobStoreFactory.
type Config struct {
	// Bucket is the GCS bucket name (required). The bucket must already exist;
	// this package never creates buckets.
	Bucket string
	// Prefix is an optional key prefix applied to every object, letting several
	// deployments share one bucket (e.g. "agent-orange/prod"). Leading/trailing
	// slashes are ignored.
	Prefix string
}

// BlobStoreFactory creates GCS-backed BlobStore instances scoped to a session
// or a global namespace. All stores share one storage.Client.
type BlobStoreFactory struct {
	client *storage.Client
	bucket string
	prefix string
}

// NewBlobStoreFactory opens a GCS client (ADC) and returns a factory bound to
// cfg.Bucket. Extra option.ClientOption values are forwarded to the client —
// tests use them to target a fake server; production passes none. Call Close
// when done to release the client.
func NewBlobStoreFactory(ctx context.Context, cfg Config, opts ...option.ClientOption) (*BlobStoreFactory, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("gcsblob: Bucket is required")
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcsblob: new client: %w", err)
	}
	return &BlobStoreFactory{client: client, bucket: cfg.Bucket, prefix: cleanPrefix(cfg.Prefix)}, nil
}

// Close releases the underlying storage client.
func (f *BlobStoreFactory) Close() error { return f.client.Close() }

// store returns a BlobStore for the (cleaned) namespace under the factory prefix.
func (f *BlobStoreFactory) store(ns string) *BlobStore {
	return &BlobStore{
		bkt:    f.client.Bucket(f.bucket),
		prefix: joinKey(f.prefix, ns),
	}
}

// ForSession returns a BlobStore scoped to session/<sessionID>.
func (f *BlobStoreFactory) ForSession(_ context.Context, sessionID string) (extension.BlobStore, error) {
	return f.store(joinKey("session", sessionID)), nil
}

// Global returns a BlobStore scoped to the given namespace (used for snapshots
// and other shared buckets).
func (f *BlobStoreFactory) Global(namespace string) extension.BlobStore {
	return f.store(namespace)
}

// BlobStore stores blobs as objects under prefix/<key> in a single bucket.
type BlobStore struct {
	bkt    *storage.BucketHandle
	prefix string // cleaned: no leading/trailing slash
}

// NewBlobStore binds a BlobStore to bucket+prefix using an existing client.
// Most callers use a BlobStoreFactory instead.
func NewBlobStore(client *storage.Client, bucket, prefix string) *BlobStore {
	return &BlobStore{bkt: client.Bucket(bucket), prefix: cleanPrefix(prefix)}
}

// obj returns the full object name for a store-relative key.
func (b *BlobStore) obj(key string) string {
	if b.prefix == "" {
		return key
	}
	return b.prefix + "/" + key
}

// objPrefix returns the full object prefix for a List call, preserving the
// caller's trailing slash (GCS matches prefixes literally).
func (b *BlobStore) objPrefix(prefix string) string {
	if b.prefix == "" {
		return prefix
	}
	return b.prefix + "/" + prefix
}

// relKey strips the store prefix from a full object name so List results
// round-trip through Read/Exists/Delete.
func (b *BlobStore) relKey(objName string) string {
	if b.prefix == "" {
		return objName
	}
	return strings.TrimPrefix(objName, b.prefix+"/")
}

// Write creates or overwrites the object at key.
func (b *BlobStore) Write(ctx context.Context, key string, r io.Reader) error {
	w := b.bkt.Object(b.obj(key)).NewWriter(ctx)
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return fmt.Errorf("gcsblob: write %q: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("gcsblob: write %q: %w", key, err)
	}
	return nil
}

// Read opens a reader for the object at key.
func (b *BlobStore) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := b.bkt.Object(b.obj(key)).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcsblob: read %q: %w", key, err)
	}
	return rc, nil
}

// Exists reports whether the object at key exists.
func (b *BlobStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := b.bkt.Object(b.obj(key)).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("gcsblob: exists %q: %w", key, err)
	}
	return true, nil
}

// Delete removes the object at key. Returns nil if it does not exist.
func (b *BlobStore) Delete(ctx context.Context, key string) error {
	err := b.bkt.Object(b.obj(key)).Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gcsblob: delete %q: %w", key, err)
	}
	return nil
}

// List returns all store-relative keys whose full object name shares
// prefix (after the store prefix).
func (b *BlobStore) List(ctx context.Context, prefix string) ([]string, error) {
	q := &storage.Query{Prefix: b.objPrefix(prefix)}
	// Fetch only the name to avoid transferring per-object metadata we discard.
	_ = q.SetAttrSelection([]string{"Name"})
	var out []string
	it := b.bkt.Objects(ctx, q)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gcsblob: list %q: %w", prefix, err)
		}
		out = append(out, b.relKey(attrs.Name))
	}
	return out, nil
}

// cleanPrefix trims surrounding slashes from a prefix.
func cleanPrefix(p string) string { return strings.Trim(p, "/") }

// joinKey joins path segments with "/", dropping empty/slash-only segments.
func joinKey(parts ...string) string {
	nonEmpty := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.Trim(p, "/"); p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "/")
}

// Compile-time assertions.
var (
	_ extension.BlobStore        = (*BlobStore)(nil)
	_ extension.BlobStoreFactory = (*BlobStoreFactory)(nil)
)
