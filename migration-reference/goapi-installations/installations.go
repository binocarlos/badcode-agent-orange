// Package installations resolves a Platinum "installation" name to a launch image
// reference. The set of installations is declared in the repo (installations/<name>/
// installation.json), aggregated at build time into manifest.json and embedded here,
// so deployed goapi knows the committed digests without runtime filesystem/registry
// access. See docs/superpowers/specs/2026-06-18-installations-multi-base-image-design.md.
package installations

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed manifest.json
var manifestJSON []byte

type Image struct {
	Registry    string `json:"registry"`
	Digest      string `json:"digest"`
	BaseDigest  string `json:"baseDigest,omitempty"`
	BuiltCommit string `json:"builtCommit"`
	BuiltAt     string `json:"builtAt"`
}

type Installation struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parent      string `json:"parent,omitempty"`
	Image       *Image `json:"image,omitempty"`

	// Build provenance derived from the installation's Dockerfile at manifest-build
	// time (scripts/build-installations-manifest.py). Surfaced to the frontend's
	// "Base image" info dialog so users can see what an image is built from and
	// what tooling it ships. All best-effort: empty when the Dockerfile is absent
	// or uses unfamiliar syntax.
	BaseImage   string   `json:"baseImage,omitempty"`
	AptPackages []string `json:"aptPackages,omitempty"`
	PipPackages []string `json:"pipPackages,omitempty"`
	NpmPackages []string `json:"npmPackages,omitempty"`
	Dockerfile  string   `json:"dockerfile,omitempty"`
}

var all []Installation

func init() {
	if err := json.Unmarshal(manifestJSON, &all); err != nil {
		panic("installations: bad embedded manifest.json: " + err.Error())
	}
}

// List returns all declared installations.
func List() []Installation { return all }

func find(name string) *Installation {
	for i := range all {
		if all[i].Name == name {
			return &all[i]
		}
	}
	return nil
}

// IsLocalRegistry reports whether url is the local dev registry (registry:5000)
// rather than a remote registry such as ACR. Local-registry mode resolves
// installation images by the :dev tag and force-pulls on launch.
func IsLocalRegistry(url string) bool {
	switch url {
	case "registry:5000", "localhost:5000", "127.0.0.1:5000":
		return true
	}
	return false
}

// UnresolvableInstallations returns the names of the given installations that do
// NOT resolve to a launch image under the registry mode + URL. An empty result
// means every installation is launchable. In ACR registry mode this flags any
// installation missing a committed image digest — the condition that silently
// breaks agent session launches in production.
func UnresolvableInstallations(insts []Installation, registryMode, registryURL string) []string {
	var bad []string
	for _, inst := range insts {
		if _, ok := ResolveInstallation(inst, registryMode, registryURL); !ok {
			bad = append(bad, inst.Name)
		}
	}
	return bad
}

// CheckAll resolves every declared installation (List) under the registry mode +
// URL and returns the names of any that are unresolvable. Used by the deploy
// guard to fail fast before shipping an app whose agent sessions resolve to
// nothing.
func CheckAll(registryMode, registryURL string) []string {
	return UnresolvableInstallations(List(), registryMode, registryURL)
}

// ResolveInstallation maps a concrete Installation value to a launch image reference.
// This is the core resolution logic used by Resolve; it is exported so callers and
// tests can resolve an in-memory Installation without round-tripping through the
// embedded manifest (useful for deterministic-pin testing with a known digest).
//
//	registry mode + ACR url    -> "<registry>@<digest>" (requires inst.Image.Digest)
//	registry mode + local url  -> "<registryURL>/platinum-sandbox-<name>:dev"
//	any other mode             -> "platinum-sandbox-<name>:dev" (legacy local tag)
//
// available is false when registry mode is requested against ACR but no digest is present.
func ResolveInstallation(inst Installation, registryMode, registryURL string) (imageRef string, available bool) {
	if registryMode == "registry" {
		if IsLocalRegistry(registryURL) {
			return fmt.Sprintf("%s/platinum-sandbox-%s:dev", registryURL, inst.Name), true
		}
		if inst.Image == nil || inst.Image.Digest == "" {
			return "", false
		}
		return fmt.Sprintf("%s@%s", inst.Image.Registry, inst.Image.Digest), true
	}
	return fmt.Sprintf("platinum-sandbox-%s:dev", inst.Name), true
}

// Resolve maps an installation name to a launch image reference.
//
//	registry mode + ACR url    -> "<registry>@<digest>" (requires a committed digest)
//	registry mode + local url  -> "<registryURL>/platinum-sandbox-<name>:dev"
//	any other mode             -> "platinum-sandbox-<name>:dev" (legacy local tag)
//
// available is false for unknown names, or ACR registry mode without a digest.
func Resolve(name, registryMode, registryURL string) (imageRef string, available bool) {
	inst := find(name)
	if inst == nil {
		return "", false
	}
	return ResolveInstallation(*inst, registryMode, registryURL)
}

// Default resolves the configured default installation; returns ("",false) if the
// default name is empty or unresolvable (caller falls back to Policy.BaseImage).
func Default(registryMode, defaultName, registryURL string) (string, bool) {
	if defaultName == "" {
		return "", false
	}
	return Resolve(defaultName, registryMode, registryURL)
}

// Available reports whether an installation can be launched in the given mode
// (used by the GET /installations listing for the dropdown).
func Available(inst Installation, registryMode, registryURL string) bool {
	_, ok := Resolve(inst.Name, registryMode, registryURL)
	return ok
}
