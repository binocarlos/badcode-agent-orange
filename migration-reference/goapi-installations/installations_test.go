package installations

import (
	"encoding/json"
	"testing"
)

func TestResolve_NonRegistryMode_ReturnsTag(t *testing.T) {
	// Any mode that is not "registry" falls back to the plain :dev tag (legacy/mock).
	ref, ok := Resolve("core-v1", "other", "")
	if !ok || ref != "platinum-sandbox-core-v1:dev" {
		t.Fatalf("got (%q,%v), want platinum-sandbox-core-v1:dev,true", ref, ok)
	}
}

func TestResolve_RegistryMode_NoDigest_Unavailable(t *testing.T) {
	// core-v1 has no image digest in the committed manifest fixture.
	if _, ok := Resolve("core-v1", "registry", "platinumimages.azurecr.io"); ok {
		t.Fatal("expected unavailable when no digest in registry mode")
	}
}

func TestResolve_UnknownName(t *testing.T) {
	if _, ok := Resolve("does-not-exist", "registry", "registry:5000"); ok {
		t.Fatal("expected not-ok for unknown installation")
	}
}

func TestList_IncludesCoreV1(t *testing.T) {
	for _, i := range List() {
		if i.Name == "core-v1" {
			return
		}
	}
	t.Fatal("core-v1 missing from List()")
}

// TestResolve_RegistryMode_WithDigest_ReturnsDigestRef verifies that when a
// committed digest is present, registry mode returns a deterministic pinned ref
// of the form "<registry>@<digest>". Uses ResolveInstallation directly so the
// test is independent of the embedded manifest.json content.
func TestResolve_RegistryMode_WithDigest_ReturnsDigestRef(t *testing.T) {
	inst := Installation{
		Name: "core-v1",
		Image: &Image{
			Registry: "reg.example/foo",
			Digest:   "sha256:abc",
		},
	}
	ref, ok := ResolveInstallation(inst, "registry", "platinumimages.azurecr.io")
	if !ok {
		t.Fatal("expected available==true when digest is present")
	}
	const want = "reg.example/foo@sha256:abc"
	if ref != want {
		t.Fatalf("got %q, want %q", ref, want)
	}
}

func TestIsLocalRegistry(t *testing.T) {
	cases := map[string]bool{
		"registry:5000":             true,
		"localhost:5000":            true,
		"127.0.0.1:5000":            true,
		"platinumimages.azurecr.io": false,
		"":                          false,
	}
	for url, want := range cases {
		if got := IsLocalRegistry(url); got != want {
			t.Errorf("IsLocalRegistry(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestResolveLocalRegistryUsesDevTag(t *testing.T) {
	inst := Installation{Name: "core-v1", Image: &Image{
		Registry: "platinumimages.azurecr.io/platinum-sandbox-core-v1",
		Digest:   "sha256:abc",
	}}
	ref, ok := ResolveInstallation(inst, "registry", "registry:5000")
	if !ok || ref != "registry:5000/platinum-sandbox-core-v1:dev" {
		t.Fatalf("local registry: got (%q,%v), want registry:5000/platinum-sandbox-core-v1:dev,true", ref, ok)
	}
}

func TestInstallationParentRoundTrip(t *testing.T) {
	raw := []byte(`{"name":"core-v2","parent":"core-v1","image":{"registry":"r","digest":"sha256:x","baseDigest":"sha256:p"}}`)
	var inst Installation
	if err := json.Unmarshal(raw, &inst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if inst.Parent != "core-v1" {
		t.Fatalf("Parent = %q, want core-v1", inst.Parent)
	}
	if inst.Image == nil || inst.Image.BaseDigest != "sha256:p" {
		t.Fatalf("BaseDigest not parsed: %+v", inst.Image)
	}
}

func TestUnresolvableInstallations_ACR_FlagsMissingDigests(t *testing.T) {
	insts := []Installation{
		{Name: "has-digest", Image: &Image{Registry: "r", Digest: "sha256:abc"}},
		{Name: "no-digest"},
		{Name: "empty-digest", Image: &Image{Registry: "r", Digest: ""}},
	}
	bad := UnresolvableInstallations(insts, "registry", "platinumimages.azurecr.io")
	if len(bad) != 2 || bad[0] != "no-digest" || bad[1] != "empty-digest" {
		t.Fatalf("got %v, want [no-digest empty-digest]", bad)
	}
}

func TestUnresolvableInstallations_LocalRegistry_AllResolvable(t *testing.T) {
	insts := []Installation{{Name: "no-digest"}, {Name: "also-none"}}
	if bad := UnresolvableInstallations(insts, "registry", "registry:5000"); len(bad) != 0 {
		t.Fatalf("local registry should resolve via :dev tag, got unresolvable %v", bad)
	}
}

func TestCheckAll_ACR_NoDigestsInManifest_FlagsAll(t *testing.T) {
	// The committed manifest fixture ships no digests, so every installation is
	// unresolvable in ACR registry mode — this is exactly what the deploy guard
	// must catch before shipping.
	bad := CheckAll("registry", "platinumimages.azurecr.io")
	if len(bad) == 0 {
		t.Fatal("expected unresolvable installations (manifest has no digests)")
	}
	found := false
	for _, n := range bad {
		if n == "core-v1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected core-v1 among unresolvable, got %v", bad)
	}
}

func TestCheckAll_LocalRegistry_NoneUnresolvable(t *testing.T) {
	if bad := CheckAll("registry", "registry:5000"); len(bad) != 0 {
		t.Fatalf("local registry: expected none unresolvable, got %v", bad)
	}
}

func TestResolveACRUsesDigest(t *testing.T) {
	inst := Installation{Name: "core-v1", Image: &Image{
		Registry: "platinumimages.azurecr.io/platinum-sandbox-core-v1",
		Digest:   "sha256:abc",
	}}
	ref, ok := ResolveInstallation(inst, "registry", "platinumimages.azurecr.io")
	if !ok || ref != "platinumimages.azurecr.io/platinum-sandbox-core-v1@sha256:abc" {
		t.Fatalf("ACR: got (%q,%v)", ref, ok)
	}
}
