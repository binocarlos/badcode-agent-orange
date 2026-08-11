package agentkit

import (
	"context"
	"fmt"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// What a session launched from, recorded beside what it was configured with.
//
// The configured value is a string and usually a mutable one — `:latest` names
// different bytes on different days — so the string alone cannot answer "which
// image did this session run". These tests pin the pair: the resolved ref, and
// the content digest it pointed at.

// digestingRegistry is a MockImageRegistry that also implements the optional
// ImageDigester seam. Separate type rather than a field on the mock, so the
// "registry cannot report digests" case below is a genuinely different object
// and not a flag that could be defaulted wrong.
type digestingRegistry struct {
	*imageregistry.MockImageRegistry
	digest string
	err    error
	asked  []execenv.ImageRef
}

func (d *digestingRegistry) Digest(ctx context.Context, ref execenv.ImageRef) (string, error) {
	d.asked = append(d.asked, ref)
	if d.err != nil {
		return "", d.err
	}
	return d.digest, nil
}

// runnerWithRegistry builds a runner over an arbitrary registry, which
// newTestRunner cannot do (it always supplies the plain mock).
func runnerWithRegistry(t *testing.T, reg imageregistry.ImageRegistry) (*runnerImpl, *agentkittest.MemStore) {
	t.Helper()
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:       execenv.NewMock(),
		Registry:  reg,
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy:    Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), store
}

func createOne(t *testing.T, r *runnerImpl, store *agentkittest.MemStore) *agentdb.Session {
	t.Helper()
	ctx := context.Background()
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Job: "j1", UserEmail: "u@example.com"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s1", Customer: "acme", Job: "j1", UserEmail: "u@example.com",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess, err := store.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	return sess
}

func TestCreateSessionRecordsLaunchImageAndDigest(t *testing.T) {
	const want = "reg.example.io/agentkit/agent-wolf@sha256:6666666666666666666666666666666666666666666666666666666666666666"
	reg := &digestingRegistry{MockImageRegistry: imageregistry.NewMock(), digest: want}
	r, store := runnerWithRegistry(t, reg)

	sess := createOne(t, r, store)

	if sess.LaunchImage != "agentkit-sandbox:test" {
		t.Errorf("LaunchImage = %q, want the resolved launch ref %q", sess.LaunchImage, "agentkit-sandbox:test")
	}
	if sess.LaunchImageDigest != want {
		t.Errorf("LaunchImageDigest = %q, want %q", sess.LaunchImageDigest, want)
	}
	// Asked about the image that was actually launched, not the configured
	// string — those differ the moment a catalogue name is involved.
	if len(reg.asked) != 1 || reg.asked[0] != execenv.ImageRef("agentkit-sandbox:test") {
		t.Errorf("Digest asked about %v, want exactly [agentkit-sandbox:test]", reg.asked)
	}
}

func TestLaunchImageIsRecordedEvenWhenNoDigestIsAvailable(t *testing.T) {
	// Two ways a digest can be missing, and neither may cost the session its
	// record of WHICH REF ran — that half needs no round trip and is most of
	// the value.
	t.Run("registry does not implement ImageDigester", func(t *testing.T) {
		r, store := runnerWithRegistry(t, imageregistry.NewMock())
		sess := createOne(t, r, store)

		if sess.LaunchImage != "agentkit-sandbox:test" {
			t.Errorf("LaunchImage = %q, want it recorded anyway", sess.LaunchImage)
		}
		if sess.LaunchImageDigest != "" {
			t.Errorf("LaunchImageDigest = %q, want empty", sess.LaunchImageDigest)
		}
	})

	t.Run("the digest lookup fails", func(t *testing.T) {
		reg := &digestingRegistry{MockImageRegistry: imageregistry.NewMock(), err: fmt.Errorf("daemon says no")}
		r, store := runnerWithRegistry(t, reg)

		// The session must still be created: provenance is telemetry about a
		// launch that has already succeeded, and trading a working container
		// for a bookkeeping entry is the wrong way round.
		sess := createOne(t, r, store)

		if sess.LaunchImage != "agentkit-sandbox:test" {
			t.Errorf("LaunchImage = %q, want it recorded despite the digest failure", sess.LaunchImage)
		}
		if sess.LaunchImageDigest != "" {
			t.Errorf("LaunchImageDigest = %q, want empty", sess.LaunchImageDigest)
		}
	})
}
