package httpapi

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// GET /agent/sessions?worker= narrows the list to one worker's jobs in the
// database rather than in the browser (design 15 §B5): the worker page used to
// pull a page of sessions and filter it client-side, so a busy project hid the
// older jobs of a quiet worker. The filter is ANDed with the project scope, and
// an absent/empty parameter must remain "no filter" rather than worker = ''.
func TestListSessions_WorkerFilter_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	ctx := context.Background()
	mine, theirs := "wf-mine-"+t.Name(), "wf-theirs-"+t.Name()
	t.Cleanup(func() {
		for _, p := range []string{mine, theirs} {
			_ = store.DB().Exec("DELETE FROM agent_sessions WHERE customer = ?", p).Error
		}
	})

	seed := func(id, customer, worker string) {
		t.Helper()
		if _, err := store.UpdateSession(ctx, &agentdb.Session{
			ID: id, Customer: customer, Status: "running", Worker: worker,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("wf-a", mine, "triager")
	seed("wf-b", mine, "summariser")
	seed("wf-c", mine, "") // a plain chat session
	seed("wf-d", theirs, "triager")

	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{},
		Identity: identityFor(mine), AgentDB: store,
	})

	ids := func(path string) []string {
		t.Helper()
		rec := do(h, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, rec.Code, rec.Body)
		}
		var rows []map[string]any
		decodeInto(t, rec, &rows)
		out := []string{}
		for _, r := range rows {
			if s, _ := r["id"].(string); s != "" {
				out = append(out, s)
			}
		}
		return out
	}

	tests := []struct {
		name string
		path string
		want []string
	}{
		{"no worker parameter lists the project", "/agent/sessions?user_email=*", []string{"wf-b", "wf-c", "wf-a"}},
		{"empty worker parameter is still no filter", "/agent/sessions?user_email=*&worker=", []string{"wf-b", "wf-c", "wf-a"}},
		{"one worker", "/agent/sessions?user_email=*&worker=triager", []string{"wf-a"}},
		{"another worker", "/agent/sessions?user_email=*&worker=summariser", []string{"wf-b"}},
		{"unknown worker", "/agent/sessions?user_email=*&worker=nobody", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(tc.path)
			// Order is updated_at DESC and the seeds share a timestamp, so
			// compare as sets — the assertion is membership, not ordering.
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			seen := map[string]bool{}
			for _, g := range got {
				seen[g] = true
			}
			for _, w := range tc.want {
				if !seen[w] {
					t.Fatalf("want %v, got %v", tc.want, got)
				}
			}
		})
	}

	// The other project's worker job of the SAME name must never appear: the
	// worker filter narrows within the project scope, it does not replace it.
	for _, id := range ids("/agent/sessions?user_email=*&worker=triager") {
		if id == "wf-d" {
			t.Fatal("worker filter leaked another project's session")
		}
	}
}
