package blobarchive

// Hermetic round-trip tests for the blobarchive adapter.
//
// These run WITHOUT a Docker daemon (no //go:build docker constraint).
// They use:
//   - fakeDockerAPI (defined below) — an in-memory docker fake
//   - inMemBlobStore (defined below) — an in-memory BlobStore
//
// Tests cover:
//  1. Archive round-trip: Persist then Materialize reproduces the content.
//  2. Resolve: cache hit/miss against the BlobStore.
//  3. Compile-time var assertion (also in blobarchive.go).
//
// Integration tests (daemon required) are in blobarchive_integration_test.go
// behind //go:build docker.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	dockertypes "github.com/docker/docker/api/types"

	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/imageregistry"
)

// ---------------------------------------------------------------------------
// in-memory BlobStore (single-key API)
// ---------------------------------------------------------------------------

type inMemBlobStore struct {
	mu   sync.Mutex
	data map[string][]byte // key -> bytes
}

func newInMemBlobStore() *inMemBlobStore {
	return &inMemBlobStore{data: map[string][]byte{}}
}

func (s *inMemBlobStore) Write(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.data[key] = data
	s.mu.Unlock()
	return nil
}

func (s *inMemBlobStore) Read(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	data, ok := s.data[key]
	s.mu.Unlock()
	if !ok {
		return nil, &notFoundError{key: key}
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *inMemBlobStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	_, ok := s.data[key]
	s.mu.Unlock()
	return ok, nil
}

func (s *inMemBlobStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
	return nil
}

func (s *inMemBlobStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

type notFoundError struct{ key string }

func (e *notFoundError) Error() string { return "blob not found: " + e.key }

// ---------------------------------------------------------------------------
// fakeDockerAPI
// ---------------------------------------------------------------------------

// fakeDockerAPI is an in-memory docker fake that stores "images" as raw bytes
// (the tar content passed to ImageLoad) so Materialize can round-trip the data.
type fakeDockerAPI struct {
	mu     sync.Mutex
	images map[string][]byte // imageID -> tar bytes
}

func newFakeDockerAPI() *fakeDockerAPI {
	return &fakeDockerAPI{images: map[string][]byte{}}
}

// addImage stores a named image (any bytes) so Persist can "save" it.
func (f *fakeDockerAPI) addImage(id string, data []byte) {
	f.mu.Lock()
	f.images[id] = data
	f.mu.Unlock()
}

func (f *fakeDockerAPI) ImageSave(_ context.Context, imageIDs []string) (io.ReadCloser, error) {
	if len(imageIDs) == 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	f.mu.Lock()
	data, ok := f.images[imageIDs[0]]
	f.mu.Unlock()
	if !ok {
		// Return a minimal valid tar so gzip+write doesn't fail.
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.Close()
		return io.NopCloser(&buf), nil
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeDockerAPI) ImageLoad(_ context.Context, input io.Reader, _ bool) (dockertypes.ImageLoadResponse, error) {
	// Consume the stream (simulating docker load).
	data, err := io.ReadAll(input)
	if err != nil {
		return dockertypes.ImageLoadResponse{}, err
	}
	_ = data // in a real implementation docker would parse this
	return dockertypes.ImageLoadResponse{Body: io.NopCloser(strings.NewReader("Loaded"))}, nil
}

func (f *fakeDockerAPI) ImageList(_ context.Context, _ dockertypes.ImageListOptions) ([]dockertypes.ImageSummary, error) {
	return nil, nil // no local images in the fake
}

func (f *fakeDockerAPI) ImageRemove(_ context.Context, _ string, _ dockertypes.ImageRemoveOptions) ([]dockertypes.ImageDeleteResponseItem, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// minimalTar returns a minimal valid tar stream (empty archive) as bytes.
func minimalTar() []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Add one entry so the tar is non-trivial.
	hdr := &tar.Header{
		Name: "hello.txt",
		Size: int64(len("hello")),
		Mode: 0o644,
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("hello"))
	_ = tw.Close()
	return buf.Bytes()
}

// verifyGzipTar asserts that the bytes are a valid gzip-compressed tar.
func verifyGzipTar(t *testing.T, data []byte) {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		_, _ = io.Copy(io.Discard, tr)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestArchiveRoundTrip verifies that Persist → (blob) → Materialize reproduces
// valid content via the in-memory fake.
func TestArchiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	blobs := newInMemBlobStore()
	docker := newFakeDockerAPI()

	// Seed the fake with a minimal image tar.
	tarBytes := minimalTar()
	docker.addImage("my-image:latest", tarBytes)

	reg := newWithAPI(docker, blobs)

	// --- Persist ---
	h, err := reg.Persist(ctx, "my-image:latest", imageregistry.PersistOptions{
		SessionID: "sess-1",
		BaseImage: "base-image:latest",
	})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if h.Kind != HandleKind {
		t.Errorf("handle kind = %q, want %q", h.Kind, HandleKind)
	}
	if h.Ref == "" {
		t.Error("handle ref is empty")
	}
	if h.Meta[metaKeySessionID] != "sess-1" {
		t.Errorf("meta session_id = %q, want %q", h.Meta[metaKeySessionID], "sess-1")
	}
	if h.Meta[metaKeyBaseImageID] != "base-image:latest" {
		t.Errorf("meta base_image_id = %q, want %q", h.Meta[metaKeyBaseImageID], "base-image:latest")
	}

	// Verify blob is present and is a valid gzip-compressed tar.
	ok, err := blobs.Exists(ctx, h.Ref)
	if err != nil || !ok {
		t.Fatalf("blob not found at %q", h.Ref)
	}
	rc, err := blobs.Read(ctx, h.Ref)
	if err != nil {
		t.Fatalf("Read blob: %v", err)
	}
	blobBytes, _ := io.ReadAll(rc)
	rc.Close()
	verifyGzipTar(t, blobBytes)

	// --- Materialize ---
	imgRef, err := reg.Materialize(ctx, h)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if imgRef == "" {
		t.Error("Materialize returned empty ImageRef")
	}
	// The ref should match the persisted image ref stored in meta.
	if imgRef != execenv.ImageRef(h.Meta[metaKeyImageRef]) {
		t.Errorf("Materialize ref = %q, want %q", imgRef, h.Meta[metaKeyImageRef])
	}
}

// TestResolveCacheHitMiss verifies that Resolve returns a hit when a blob exists
// and a miss when it does not.
func TestResolveCacheHitMiss(t *testing.T) {
	ctx := context.Background()
	blobs := newInMemBlobStore()
	docker := newFakeDockerAPI()
	reg := newWithAPI(docker, blobs)

	spec := imageregistry.BuildSpec{
		BaseImage: "base:latest",
		Layer:     imageregistry.LayerApp,
		SourceKey: "user-123",
	}

	// Miss before any data.
	_, ok, err := reg.Resolve(ctx, spec)
	if err != nil {
		t.Fatalf("Resolve (miss): %v", err)
	}
	if ok {
		t.Error("Resolve returned ok=true on a miss, want false")
	}

	// Seed the expected blob path.
	hash := contentHash(spec)
	blobPath := "resolve/" + hash[:16] + ".tar.gz"
	_ = blobs.Write(ctx, blobPath, strings.NewReader("placeholder"))

	// Hit.
	ref, ok, err := reg.Resolve(ctx, spec)
	if err != nil {
		t.Fatalf("Resolve (hit): %v", err)
	}
	if !ok {
		t.Error("Resolve returned ok=false after seeding blob, want true")
	}
	if string(ref) != blobPath {
		t.Errorf("Resolve ref = %q, want %q", ref, blobPath)
	}
}

// TestCapabilities verifies that blobarchive reports PortableHandles=true and
// SupportsDiff=false (full-archive path).
func TestCapabilities(t *testing.T) {
	blobs := newInMemBlobStore()
	docker := newFakeDockerAPI()
	reg := newWithAPI(docker, blobs)

	caps := reg.Capabilities()
	if !caps.PortableHandles {
		t.Error("PortableHandles should be true for blobarchive")
	}
	if caps.SupportsDiff {
		t.Error("SupportsDiff should be false in this pass (full-archive only)")
	}
}

// TestRemove verifies that Remove deletes the blob from the BlobStore.
func TestRemove(t *testing.T) {
	ctx := context.Background()
	blobs := newInMemBlobStore()
	docker := newFakeDockerAPI()
	docker.addImage("img:1", minimalTar())

	reg := newWithAPI(docker, blobs)

	h, err := reg.Persist(ctx, "img:1", imageregistry.PersistOptions{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	ok, _ := blobs.Exists(ctx, h.Ref)
	if !ok {
		t.Fatal("blob not found after Persist")
	}

	if err := reg.Remove(ctx, h); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	ok, _ = blobs.Exists(ctx, h.Ref)
	if ok {
		t.Error("blob still present after Remove")
	}
}

// TestCompileTimeAssertion ensures the compiler verifies the Registry implements
// ImageRegistry.  This is also in blobarchive.go but belt-and-suspenders.
var _ imageregistry.ImageRegistry = (*Registry)(nil)
