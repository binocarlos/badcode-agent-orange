package httpapi

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// GET /agent/session/{id} must return the environment provenance the runner
// writes on the row: `launch_image` and `launch_image_digest`.
//
// This is the sibling of TestGetSession_ReturnsComposedPrompt_LivePG.
// `composed_prompt` proves what a session was TOLD; these two prove what it RAN
// IN. The projection in lifecycle.go is an explicit field list, so a column
// that nobody adds to it is invisible from outside the database — which for
// provenance is the same as not having it.
func TestGetSession_ReturnsLaunchImageProvenance_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	ctx := context.Background()
	project := "li-" + t.Name()
	t.Cleanup(func() {
		_ = store.DB().Exec("DELETE FROM agent_sessions WHERE customer = ?", project).Error
	})

	// The pair that makes the record worth keeping: a mutable tag, and the
	// immutable thing it pointed at on the day this session ran.
	const (
		ref    = "europe-west1-docker.pkg.dev/webkit-servers/agent-orange/agent-wolf:latest"
		digest = "europe-west1-docker.pkg.dev/webkit-servers/agent-orange/agent-wolf@sha256:" +
			"7777777777777777777777777777777777777777777777777777777777777777"
	)

	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "sess-launch", Customer: project, Status: "running",
		LaunchImage: ref, LaunchImageDigest: digest,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{},
		Identity: identityFor(project), AgentDB: store,
	})

	rec := do(h, http.MethodGet, "/agent/session/sess-launch", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body map[string]any
	decodeInto(t, rec, &body)

	if body["launch_image"] != ref {
		t.Errorf("launch_image: got %v, want %q", body["launch_image"], ref)
	}
	if body["launch_image_digest"] != digest {
		t.Errorf("launch_image_digest: got %v, want %q", body["launch_image_digest"], digest)
	}
}

// A session whose digest could not be read still reports the ref, and reports
// the digest as an empty string rather than omitting the key: a consumer
// checking "did we capture this" should not have to tell absent from empty.
func TestGetSession_LaunchImageDigestAbsentIsEmptyNotMissing_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	ctx := context.Background()
	project := "li-empty-" + t.Name()
	t.Cleanup(func() {
		_ = store.DB().Exec("DELETE FROM agent_sessions WHERE customer = ?", project).Error
	})

	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "sess-nodigest", Customer: project, Status: "running",
		LaunchImage: "agentkit-sandbox:dev",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{},
		Identity: identityFor(project), AgentDB: store,
	})

	rec := do(h, http.MethodGet, "/agent/session/sess-nodigest", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body map[string]any
	decodeInto(t, rec, &body)

	if body["launch_image"] != "agentkit-sandbox:dev" {
		t.Errorf("launch_image: got %v", body["launch_image"])
	}
	got, present := body["launch_image_digest"]
	if !present {
		t.Fatal("launch_image_digest key is missing entirely; want it present and empty")
	}
	if got != "" {
		t.Errorf("launch_image_digest: got %v, want empty", got)
	}
}
