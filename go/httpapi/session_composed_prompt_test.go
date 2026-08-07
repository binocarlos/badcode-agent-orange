package httpapi

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// GET /agent/session/{id} must return the composition provenance C2 writes on
// the row: `worker` and `composed_prompt`. Without them nothing outside the
// database can prove which prompt a job actually ran with — which is exactly
// how G1 proves a memory written by one job reached the next job's prompt
// (§7.4, briefing sections).
func TestGetSession_ReturnsComposedPrompt_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	ctx := context.Background()
	mine, theirs := "cp-mine-"+t.Name(), "cp-theirs-"+t.Name()
	t.Cleanup(func() {
		for _, p := range []string{mine, theirs} {
			_ = store.DB().Exec("DELETE FROM agent_sessions WHERE customer = ?", p).Error
		}
	})

	const composed = "PREAMBLE\n\nPROJECT PROMPT\n\nWORKER PROMPT\n\n" +
		"Your memory briefing: kind=lesson\n- do not be curt"

	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "sess-composed", Customer: mine, Status: "running",
		Worker: "email-answerer", ComposedPrompt: composed,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "sess-other", Customer: theirs, Status: "running",
		Worker: "their-worker", ComposedPrompt: "THEIR SECRET PROMPT",
	}); err != nil {
		t.Fatalf("seed other session: %v", err)
	}

	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{},
		Identity: identityFor(mine), AgentDB: store,
	})

	rec := do(h, http.MethodGet, "/agent/session/sess-composed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body map[string]any
	decodeInto(t, rec, &body)
	if body["composed_prompt"] != composed {
		t.Fatalf("composed_prompt:\n got %q\nwant %q", body["composed_prompt"], composed)
	}
	if body["worker"] != "email-answerer" {
		t.Fatalf("worker: got %v", body["worker"])
	}
	// The briefing section is the assertion G1 actually makes: a memory reached
	// the prompt.
	if !strings.Contains(body["composed_prompt"].(string), "Your memory briefing:") {
		t.Fatalf("briefing section missing from the served prompt: %v", body["composed_prompt"])
	}

	// PROJECT ISOLATION: the composed prompt carries the project's system prompt
	// and its memories, so another project's session must not be readable — and
	// must 404 rather than 403, so its existence does not leak.
	rec = do(h, http.MethodGet, "/agent/session/sess-other", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project read: status=%d body=%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "THEIR SECRET PROMPT") {
		t.Fatal("a cross-project read leaked the composed prompt")
	}

	// A plain human session has neither field — the JSON says so explicitly
	// rather than omitting the keys, so a client can tell "not a worker job"
	// from "an older row".
	if _, err := store.UpdateSession(ctx, &agentdb.Session{
		ID: "sess-plain", Customer: mine, Status: "running",
	}); err != nil {
		t.Fatalf("seed plain session: %v", err)
	}
	rec = do(h, http.MethodGet, "/agent/session/sess-plain", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body = nil
	decodeInto(t, rec, &body)
	if v, ok := body["composed_prompt"]; !ok || v != "" {
		t.Fatalf("a plain session must report an empty composed_prompt, got %#v (present=%v)", v, ok)
	}
	if v, ok := body["worker"]; !ok || v != "" {
		t.Fatalf("a plain session must report an empty worker, got %#v (present=%v)", v, ok)
	}
}
