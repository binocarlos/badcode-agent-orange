package imageregistry

// Tests for the imageregistry package: MockImageRegistry Resolve cache,
// contentHash determinism, BuildSpec.Layer/SourceKey, Capabilities.PortableHandles.

import (
	"context"
	"testing"

	"github.com/bayes-price/agentkit/execenv"
)

var ctx = context.Background()

// TestMockResolveHitMiss verifies that Build populates the resolve cache and
// Resolve returns a cache hit afterwards.
func TestMockResolveHitMiss(t *testing.T) {
	m := NewMock()

	spec := BuildSpec{
		BaseImage: "base:latest",
		Layer:     LayerApp,
		SourceKey: "skill-set-1",
		Tag:       "my-app:v1",
	}

	// Before Build: miss.
	_, ok, err := m.Resolve(ctx, spec)
	if err != nil {
		t.Fatalf("Resolve (miss): %v", err)
	}
	if ok {
		t.Error("Resolve returned ok=true before Build, want false")
	}

	// Build → populates resolve cache.
	ref, err := m.Build(ctx, spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ref == "" {
		t.Error("Build returned empty ref")
	}

	// After Build: hit.
	resolvedRef, ok, err := m.Resolve(ctx, spec)
	if err != nil {
		t.Fatalf("Resolve (hit): %v", err)
	}
	if !ok {
		t.Error("Resolve returned ok=false after Build, want true")
	}
	if resolvedRef != ref {
		t.Errorf("Resolve ref = %q, want %q", resolvedRef, ref)
	}
}

// TestMockResolveDifferentSpecsDoNotCollide verifies that two different specs
// produce different cache entries.
func TestMockResolveDifferentSpecsDoNotCollide(t *testing.T) {
	m := NewMock()

	spec1 := BuildSpec{BaseImage: "base:1", Layer: LayerApp, SourceKey: "user-A", Tag: "img:A"}
	spec2 := BuildSpec{BaseImage: "base:1", Layer: LayerApp, SourceKey: "user-B", Tag: "img:B"}

	_, _ = m.Build(ctx, spec1)

	// spec2 has not been built; Resolve should miss.
	_, ok, err := m.Resolve(ctx, spec2)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Error("Resolve returned ok=true for a spec that was never built")
	}
}

// TestMockCapabilitiesPortableHandles verifies PortableHandles=true on the mock.
func TestMockCapabilitiesPortableHandles(t *testing.T) {
	m := NewMock()
	caps := m.Capabilities()
	if !caps.PortableHandles {
		t.Error("MockImageRegistry.Capabilities().PortableHandles should be true")
	}
}

// TestBuildSpecLayerAndSourceKey verifies that Layer and SourceKey are part of
// the content hash (different values → different hashes → different cache entries).
func TestBuildSpecLayerAndSourceKey(t *testing.T) {
	specCore := BuildSpec{BaseImage: "base:1", Layer: LayerCore, SourceKey: "id-1"}
	specApp := BuildSpec{BaseImage: "base:1", Layer: LayerApp, SourceKey: "id-1"}
	specUser := BuildSpec{BaseImage: "base:1", Layer: LayerApp, SourceKey: "id-2"}

	hashCore := contentHash(specCore)
	hashApp := contentHash(specApp)
	hashUser := contentHash(specUser)

	if hashCore == hashApp {
		t.Error("LayerCore and LayerApp should produce different content hashes")
	}
	if hashApp == hashUser {
		t.Error("different SourceKeys should produce different content hashes")
	}
}

// TestContentHashSortingDeterminism verifies that overlay/arg order does not affect the hash.
func TestContentHashSortingDeterminism(t *testing.T) {
	spec1 := BuildSpec{
		BaseImage: "base:1",
		Overlays: []Overlay{
			{Source: "/b", Target: "/tb"},
			{Source: "/a", Target: "/ta"},
		},
		BuildArgs: map[string]string{"Z": "1", "A": "2"},
	}
	spec2 := BuildSpec{
		BaseImage: "base:1",
		Overlays: []Overlay{
			{Source: "/a", Target: "/ta"},
			{Source: "/b", Target: "/tb"},
		},
		BuildArgs: map[string]string{"A": "2", "Z": "1"},
	}
	if contentHash(spec1) != contentHash(spec2) {
		t.Error("contentHash should be the same regardless of overlay/arg order")
	}
}

// TestPersistMaterializeRoundTrip verifies the mock Persist/Materialize cycle.
func TestPersistMaterializeRoundTrip(t *testing.T) {
	m := NewMock()

	imageRef := execenv.ImageRef("session-image:snap")
	h, err := m.Persist(ctx, imageRef, PersistOptions{SessionID: "s1", PreferDiff: true})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}

	restored, err := m.Materialize(ctx, h)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if restored != imageRef {
		t.Errorf("Materialize = %q, want %q", restored, imageRef)
	}
}

// TestMockSatisfiesInterface is the compile-time assertion (also in mock.go).
var _ ImageRegistry = (*MockImageRegistry)(nil)
