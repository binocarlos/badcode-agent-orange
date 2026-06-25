// Package blobarchive implements ImageRegistry using a BlobStore (Azure/fs/…)
// as the durable backend for session snapshots.
//
// # Full-archive path (this implementation)
//
// Persist: docker save → gzip → write to BlobStore.
// Materialize: read from BlobStore → gunzip → docker load.
// Handle{Kind:"blob-archive", Ref:<blobPath>, Meta:{base_image_id, session_id}}.
// PortableHandles = TRUE (the blob lives in a shared BlobStore, accessible from
// any worker that has the same BlobStore configured).
//
// SupportsDiff = false in this pass — the diff fast-path (docker diff +
// getArchive → KB-MB archives instead of GB) is preserved as a follow-up.  See
// the FOLLOW-UP note at the bottom.  Behaviourally correct; archives are just
// larger.
//
// Porting source:
//   - orchestrator/src/sandbox-manager.ts archiveSandbox@886, restoreFromArchive@1093
//   - orchestrator/src/azure-upload.ts (blob I/O pattern)
//
// See agent-library/docs/03-image-registry.md (blobarchive row).
package blobarchive

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"

	"github.com/docker/docker/client"

	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

const (
	// HandleKind is the handle Kind value for blob-archive handles.
	HandleKind = "blob-archive"

	// metaKeyBaseImageID is the handle Meta key for the base image ID.
	metaKeyBaseImageID = "base_image_id"
	// metaKeySessionID is the handle Meta key for the session ID.
	metaKeySessionID = "session_id"
	// metaKeyImageRef is the handle Meta key storing the original image ref.
	metaKeyImageRef = "image_ref"
)

// Registry is the blobarchive ImageRegistry adapter.
type Registry struct {
	docker dockerAPI
	blobs  extension.BlobStore
}

// New creates a blobarchive Registry using the real Docker daemon at dockerHost
// (empty = default socket) and the supplied BlobStore. The BlobStore is
// pre-scoped to the desired container (via BlobStoreFactory.Global).
func New(dockerHost string, blobs extension.BlobStore) (*Registry, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if dockerHost != "" {
		opts = append(opts, client.WithHost(dockerHost))
	} else {
		opts = append(opts, client.FromEnv)
	}
	c, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("blobarchive: docker client: %w", err)
	}
	return &Registry{
		docker: &realDockerClient{c: c},
		blobs:  blobs,
	}, nil
}

// newWithAPI constructs a Registry with an injected dockerAPI (for tests).
func newWithAPI(d dockerAPI, blobs extension.BlobStore) *Registry {
	return &Registry{docker: d, blobs: blobs}
}

// EnsurePresent is a no-op: the image is loaded by Materialize before Provision.
func (r *Registry) EnsurePresent(_ context.Context, _ execenv.ImageRef) error {
	return nil
}

// Build is not directly implemented in blobarchive; it delegates to the host
// (blobarchive handles Persist/Materialize; Build is the host's responsibility).
// Returns an error indicating delegation is needed.
func (r *Registry) Build(_ context.Context, _ imageregistry.BuildSpec) (execenv.ImageRef, error) {
	return "", fmt.Errorf("blobarchive: Build is not supported — use a pre-built image pushed to the registry")
}

// Resolve performs a content-hash lookup: checks whether the BlobStore already
// has a persisted archive for the given spec's content hash.  Returns the blob
// path as the ref when found.
func (r *Registry) Resolve(ctx context.Context, spec imageregistry.BuildSpec) (execenv.ImageRef, bool, error) {
	hash := contentHash(spec)
	blobPath := "resolve/" + hash[:16] + ".tar.gz"
	ok, err := r.blobs.Exists(ctx, blobPath)
	if err != nil {
		return "", false, fmt.Errorf("blobarchive: resolve exists check: %w", err)
	}
	if ok {
		return execenv.ImageRef(blobPath), true, nil
	}
	return "", false, nil
}

// Persist commits the in-engine image to a full docker save archive, gzips it,
// and writes it to the BlobStore.
//
// Full-archive path (SupportsDiff = false):
//
//	docker save <ref> | gzip → BlobStore(<container>/<blobPath>)
//
// FOLLOW-UP: implement the diff fast-path (docker diff + getArchive →
// KB-MB delta archive) for SupportsDiff=true.  See docs/03-image-registry.md
// "The diff-archive optimisation".
func (r *Registry) Persist(ctx context.Context, ref execenv.ImageRef, opts imageregistry.PersistOptions) (imageregistry.Handle, error) {
	blobPath := blobPathFor(opts.SessionID, ref)

	// docker save streams the image as a tar.
	saveRC, err := r.docker.ImageSave(ctx, []string{string(ref)})
	if err != nil {
		return imageregistry.Handle{}, fmt.Errorf("blobarchive: docker save: %w", err)
	}
	defer saveRC.Close()

	// Pipe through gzip into the BlobStore.
	pr, pw := io.Pipe()
	gzipErrCh := make(chan error, 1)
	go func() {
		gw := gzip.NewWriter(pw)
		_, copyErr := io.Copy(gw, saveRC)
		closeErr := gw.Close()
		if copyErr != nil {
			pw.CloseWithError(copyErr)
			gzipErrCh <- copyErr
			return
		}
		pw.CloseWithError(closeErr)
		gzipErrCh <- closeErr
	}()

	if err := r.blobs.Write(ctx, blobPath, pr); err != nil {
		return imageregistry.Handle{}, fmt.Errorf("blobarchive: blob write: %w", err)
	}
	if err := <-gzipErrCh; err != nil {
		return imageregistry.Handle{}, fmt.Errorf("blobarchive: gzip: %w", err)
	}

	meta := map[string]string{
		metaKeySessionID: opts.SessionID,
		metaKeyImageRef:  string(ref),
	}
	if opts.BaseImage != "" {
		meta[metaKeyBaseImageID] = string(opts.BaseImage)
	}

	return imageregistry.Handle{
		Kind: HandleKind,
		Ref:  blobPath,
		Meta: meta,
	}, nil
}

// Materialize downloads the gzip archive from the BlobStore, decompresses it,
// and loads it into the local Docker daemon via docker load.  Returns the
// runnable image ref.
func (r *Registry) Materialize(ctx context.Context, h imageregistry.Handle) (execenv.ImageRef, error) {
	if h.Kind != HandleKind {
		return "", fmt.Errorf("blobarchive: expected handle kind %q, got %q", HandleKind, h.Kind)
	}

	rc, err := r.blobs.Read(ctx, h.Ref)
	if err != nil {
		return "", fmt.Errorf("blobarchive: blob read: %w", err)
	}
	defer rc.Close()

	// Decompress.
	gr, err := gzip.NewReader(rc)
	if err != nil {
		return "", fmt.Errorf("blobarchive: gzip reader: %w", err)
	}
	defer gr.Close()

	// docker load.
	resp, err := r.docker.ImageLoad(ctx, gr, true)
	if err != nil {
		return "", fmt.Errorf("blobarchive: docker load: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	imgRef := execenv.ImageRef(h.Meta[metaKeyImageRef])
	if imgRef == "" {
		imgRef = execenv.ImageRef(h.Ref)
	}
	return imgRef, nil
}

// Remove deletes the blob from the BlobStore (cleanup after session deletion).
func (r *Registry) Remove(ctx context.Context, h imageregistry.Handle) error {
	if h.Kind != HandleKind {
		return nil
	}
	return r.blobs.Delete(ctx, h.Ref)
}

// Capabilities reports blobarchive's abilities.
func (r *Registry) Capabilities() imageregistry.Capabilities {
	return imageregistry.Capabilities{
		SupportsDiff:    false, // full-archive only in this pass (diff fast-path is a follow-up)
		SupportsBuild:   false, // host is responsible for building images
		SupportsRemote:  false,
		PortableHandles: true, // the blob is in a shared BlobStore, accessible from any worker
	}
}

// Compile-time assertion.
var _ imageregistry.ImageRegistry = (*Registry)(nil)

// --- internal helpers --------------------------------------------------------

// blobPathFor returns a deterministic blob path for a given session and image ref.
func blobPathFor(sessionID string, ref execenv.ImageRef) string {
	safe := sanitizeName(string(ref))
	if sessionID != "" {
		return "sessions/" + sanitizeName(sessionID) + "/" + safe + ".tar.gz"
	}
	return "images/" + safe + ".tar.gz"
}

// sanitizeName makes a string safe for use as a blob path segment.
func sanitizeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			out = append(out, c)
		} else {
			out = append(out, '-')
		}
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// sortStrings sorts a slice of strings in-place.
func sortStrings(s []string) { sort.Strings(s) }

// contentHash computes the content-hash for a BuildSpec.
// sha256(base + layer + sourceKey + sorted overlays + sorted build args).
func contentHash(spec imageregistry.BuildSpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "base:%s\n", spec.BaseImage)
	fmt.Fprintf(h, "layer:%s\n", spec.Layer)
	fmt.Fprintf(h, "sourcekey:%s\n", spec.SourceKey)

	srcs := make([]string, len(spec.Overlays))
	for i, o := range spec.Overlays {
		srcs[i] = o.Source + ":" + o.Target
	}
	sortStrings(srcs)
	for _, s := range srcs {
		fmt.Fprintf(h, "overlay:%s\n", s)
	}

	argKeys := make([]string, 0, len(spec.BuildArgs))
	for k := range spec.BuildArgs {
		argKeys = append(argKeys, k)
	}
	sortStrings(argKeys)
	for _, k := range argKeys {
		fmt.Fprintf(h, "arg:%s=%s\n", k, spec.BuildArgs[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
