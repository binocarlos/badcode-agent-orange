//go:build docker

package blobarchive

// Integration tests for blobarchive (require a real Docker daemon).
//
// Run with:   go test -tags docker ./imageregistry/blobarchive/...
//
// These tests verify that the full round-trip (Persist → BlobStore → Materialize)
// works correctly against a real daemon (commit → archive → materialize → runnable
// ref), mirroring the orchestrator's diff-archive behaviour.

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/imageregistry"
)

// dockerHostForTest returns the Docker host from the environment, or the
// default socket (empty string) if not set.
func dockerHostForTest() string {
	return os.Getenv("DOCKER_HOST") // e.g. "tcp://localhost:2375" for DinD
}

// TestIntegrationPersistMaterialize performs a real docker save → gzip → BlobStore
// → docker load round-trip using a tiny well-known image.
func TestIntegrationPersistMaterialize(t *testing.T) {
	const testImage = "hello-world:latest" // tiny image guaranteed to exist in public registries

	ctx := context.Background()
	blobs := newInMemBlobStore()

	reg, err := New(dockerHostForTest(), blobs)
	if err != nil {
		t.Skipf("could not create blobarchive registry (daemon unavailable?): %v", err)
	}

	// Ensure the test image is present (docker pull if not).
	// For CI with a real daemon, the image should already be available.
	// If not, use EnsurePresent (no-op here) or skip the test.
	if err := reg.EnsurePresent(ctx, execenv.ImageRef(testImage)); err != nil {
		t.Logf("EnsurePresent (no-op): %v", err)
	}

	h, err := reg.Persist(ctx, execenv.ImageRef(testImage), imageregistry.PersistOptions{
		SessionID: "integration-sess-1",
		BaseImage: execenv.ImageRef(testImage),
	})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if h.Kind != HandleKind {
		t.Errorf("handle kind = %q, want %q", h.Kind, HandleKind)
	}

	// Verify the blob was actually written and is a valid gzip+tar.
	rc, err := blobs.Read(ctx, h.Ref)
	if err != nil {
		t.Fatalf("Read blob: %v", err)
	}
	blobBytes, _ := io.ReadAll(rc)
	rc.Close()
	if len(blobBytes) == 0 {
		t.Error("persisted blob is empty")
	}
	verifyGzipTar(t, blobBytes)

	// Materialize: download → gunzip → docker load.
	ref, err := reg.Materialize(ctx, h)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if ref == "" {
		t.Error("Materialize returned empty ref")
	}
	t.Logf("Materialize returned ref: %q", ref)

	// Clean up.
	if err := reg.Remove(ctx, h); err != nil {
		t.Logf("Remove (cleanup): %v", err)
	}
}
