package ociregistry

import (
	"context"
	"fmt"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// Digest is what turns a configured string into a content address: the string
// `…/agent-wolf:latest` is a moving target, and the digest is the record of
// which bytes a session actually ran.

func TestRegistryImplementsImageDigester(t *testing.T) {
	var _ imageregistry.ImageDigester = (*Registry)(nil)
}

func TestDigestPrefersTheRepoDigestMatchingTheRef(t *testing.T) {
	reg, d := newTestRegistry()
	// An image pulled under one name and also tagged from another repository
	// carries several repo digests. The one that answers "pull this elsewhere
	// and get these bytes" is the one whose repository matches the ref we
	// launched from — picking any other would record a pointer into a
	// repository this deployment may not even be able to read.
	d.repoDigests = []string{
		"other.example.io/mirror/agent-wolf@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"reg.example.io/agentkit/agent-wolf@sha256:2222222222222222222222222222222222222222222222222222222222222222",
	}

	got, err := reg.Digest(context.Background(), "reg.example.io/agentkit/agent-wolf:latest")
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	want := "reg.example.io/agentkit/agent-wolf@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	if got != want {
		t.Fatalf("Digest = %q, want %q", got, want)
	}
}

func TestDigestFallsBackToAnyRepoDigestThenToTheImageID(t *testing.T) {
	t.Run("no matching repository", func(t *testing.T) {
		reg, d := newTestRegistry()
		d.repoDigests = []string{"elsewhere.example.io/x@sha256:3333333333333333333333333333333333333333333333333333333333333333"}

		got, err := reg.Digest(context.Background(), "reg.example.io/agentkit/agent-wolf:latest")
		if err != nil {
			t.Fatalf("Digest: %v", err)
		}
		if got != d.repoDigests[0] {
			t.Fatalf("Digest = %q, want the only repo digest %q", got, d.repoDigests[0])
		}
	})

	t.Run("built locally, never pushed", func(t *testing.T) {
		// This is the standalone stack's `agentkit-sandbox:dev`: built into the
		// daemon and never pushed anywhere, so it has no repo digest at all. The
		// image ID still identifies the exact bytes, which is what the record is
		// for, so recording it beats recording nothing.
		reg, d := newTestRegistry()
		d.repoDigests = nil
		d.inspectID = "sha256:4444444444444444444444444444444444444444444444444444444444444444"

		got, err := reg.Digest(context.Background(), "agentkit-sandbox:dev")
		if err != nil {
			t.Fatalf("Digest: %v", err)
		}
		if got != d.inspectID {
			t.Fatalf("Digest = %q, want the image ID %q", got, d.inspectID)
		}
	})
}

func TestDigestReportsInspectFailure(t *testing.T) {
	reg, d := newTestRegistry()
	d.inspectErr = fmt.Errorf("no such image")

	if _, err := reg.Digest(context.Background(), execenv.ImageRef("reg.example.io/agentkit/nope:1")); err == nil {
		t.Fatal("Digest on an absent image returned no error")
	}
}

// repositoryOf is the whole reason the matching case above works, and the
// registry-port form is the one a naive LastIndex(":") gets wrong.
func TestRepositoryOf(t *testing.T) {
	for _, tc := range []struct{ ref, want string }{
		{"agent-wolf:latest", "agent-wolf"},
		{"reg.example.io/agentkit/agent-wolf:3", "reg.example.io/agentkit/agent-wolf"},
		{"europe-west1-docker.pkg.dev/webkit-servers/agent-orange/agent-wolf:latest",
			"europe-west1-docker.pkg.dev/webkit-servers/agent-orange/agent-wolf"},
		// A registry port is a colon that is NOT a tag separator.
		{"localhost:5000/agent-wolf", "localhost:5000/agent-wolf"},
		{"localhost:5000/agent-wolf:dev", "localhost:5000/agent-wolf"},
		// Already a digest reference.
		{"reg.example.io/x@sha256:5555", "reg.example.io/x"},
		// No tag at all.
		{"agentkit-sandbox", "agentkit-sandbox"},
	} {
		if got := repositoryOf(tc.ref); got != tc.want {
			t.Errorf("repositoryOf(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}
