package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fakeAttention records the query it was handed and returns a canned page, so
// the handler's parameter plumbing can be asserted without a database.
type fakeAttention struct {
	got  agentdb.AttentionRequestQuery
	out  []*agentdb.AttentionRequest
	err  error
	call int
}

func (f *fakeAttention) ListAttentionRequests(_ context.Context, q agentdb.AttentionRequestQuery) ([]*agentdb.AttentionRequest, error) {
	f.call++
	f.got = q
	return f.out, f.err
}

func newAttentionHandlers(t *testing.T, store AttentionStore, id IdentityFunc) *Handlers {
	t.Helper()
	return newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Identity:  id,
		Attention: store,
	})
}

// The project comes from the token, `state` widens the read only for the exact
// word "all", and `limit` passes through.
func TestListAttentionRequests_QueryPlumbing(t *testing.T) {
	tests := []struct {
		name string
		path string
		want agentdb.AttentionRequestQuery
	}{
		{
			name: "default is open only",
			path: "/agent/attention-requests",
			want: agentdb.AttentionRequestQuery{Project: "acme"},
		},
		{
			name: "state=open is the default spelled out",
			path: "/agent/attention-requests?state=open",
			want: agentdb.AttentionRequestQuery{Project: "acme"},
		},
		{
			name: "state=all widens the read",
			path: "/agent/attention-requests?state=all",
			want: agentdb.AttentionRequestQuery{Project: "acme", IncludeResolved: true},
		},
		{
			name: "an unknown state narrows rather than erroring",
			path: "/agent/attention-requests?state=everything",
			want: agentdb.AttentionRequestQuery{Project: "acme"},
		},
		{
			name: "limit passes through",
			path: "/agent/attention-requests?state=all&limit=25",
			want: agentdb.AttentionRequestQuery{Project: "acme", IncludeResolved: true, Limit: 25},
		},
		{
			name: "junk numerics degrade to unbounded rather than erroring",
			path: "/agent/attention-requests?limit=-1",
			want: agentdb.AttentionRequestQuery{Project: "acme"},
		},
		{
			name: "a project in the query is ignored, not honoured",
			path: "/agent/attention-requests?project=other",
			want: agentdb.AttentionRequestQuery{Project: "acme"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeAttention{}
			h := newAttentionHandlers(t, store, identityFor("acme"))
			if rec := do(h, http.MethodGet, tc.path, ""); rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if store.got != tc.want {
				t.Fatalf("query:\n got %+v\nwant %+v", store.got, tc.want)
			}
		})
	}
}

// The response is exactly {"attention_requests":[…]} — the envelope shape every
// sibling read route uses — and the message the worker wrote survives it.
func TestListAttentionRequests_ResponseShape(t *testing.T) {
	store := &fakeAttention{out: []*agentdb.AttentionRequest{
		{ID: "r-2", Project: "acme", SessionID: "sess-2", Worker: "tweet-author",
			Message: "which of these two drafts should go out?", SessionURL: "https://x/sessions/sess-2",
			Channel: "webhook", Delivered: true, CreatedAt: 1789000000123},
		{ID: "r-1", Project: "acme", SessionID: "sess-1", Worker: "email-answerer",
			Message: "the customer is asking for a refund", CreatedAt: 1789000000000},
	}}
	h := newAttentionHandlers(t, store, identityFor("acme"))
	rec := do(h, http.MethodGet, "/agent/attention-requests", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		AttentionRequests []map[string]any `json:"attention_requests"`
	}
	decodeInto(t, rec, &body)
	if len(body.AttentionRequests) != 2 {
		t.Fatalf("want 2 records, got %d", len(body.AttentionRequests))
	}
	// Newest first: the store orders, the handler must not resort.
	if body.AttentionRequests[0]["id"] != "r-2" {
		t.Fatalf("records must be newest-first: %+v", body.AttentionRequests)
	}
	first := body.AttentionRequests[0]
	for _, k := range []string{"id", "project", "session_id", "worker", "message", "session_url", "channel", "delivered", "expires_at", "created_at", "answered_at", "timed_out_at"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("record is missing %q (the pinned AttentionRequest shape): %+v", k, first)
		}
	}

	empty := &fakeAttention{}
	h = newAttentionHandlers(t, empty, identityFor("acme"))
	rec = do(h, http.MethodGet, "/agent/attention-requests", "")
	if got := rec.Body.String(); got != "{\"attention_requests\":[]}\n" {
		t.Fatalf("an empty stack must be [], not null: %s", got)
	}
}

// Auth posture: 401 without an identity, 403 for a token carrying no project,
// 501 when the host wired no store, and no method other than GET.
func TestListAttentionRequests_AuthAndAvailability(t *testing.T) {
	t.Run("401 without identity", func(t *testing.T) {
		store := &fakeAttention{}
		h := newAttentionHandlers(t, store, func(*http.Request) (Identity, error) {
			return Identity{}, http.ErrNoCookie
		})
		if rec := do(h, http.MethodGet, "/agent/attention-requests", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", rec.Code)
		}
		if store.call != 0 {
			t.Fatal("an unauthenticated request must not reach the store")
		}
	})

	t.Run("403 with no project claim", func(t *testing.T) {
		store := &fakeAttention{}
		h := newAttentionHandlers(t, store, identityFor(""))
		if rec := do(h, http.MethodGet, "/agent/attention-requests", ""); rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d", rec.Code)
		}
		if store.call != 0 {
			t.Fatal("a projectless token must not reach the store")
		}
	})

	t.Run("501 with no store", func(t *testing.T) {
		h := newAttentionHandlers(t, nil, identityFor("acme"))
		if rec := do(h, http.MethodGet, "/agent/attention-requests", ""); rec.Code != http.StatusNotImplemented {
			t.Fatalf("status=%d", rec.Code)
		}
	})

	t.Run("write methods are not routed", func(t *testing.T) {
		// A request is answered by a human typing the next message (§9) — there
		// is no write half of this route, by design.
		h := newAttentionHandlers(t, &fakeAttention{}, identityFor("acme"))
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			if rec := do(h, m, "/agent/attention-requests", `{}`); rec.Code == http.StatusOK {
				t.Fatalf("%s must not be served", m)
			}
		}
	})
}

// PROJECT ISOLATION and the open/all split, against the real store.
func TestListAttentionRequests_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	mine, theirs := "attnread-mine-"+t.Name(), "attnread-theirs-"+t.Name()
	t.Cleanup(func() {
		for _, p := range []string{mine, theirs} {
			_ = store.DB().Exec("DELETE FROM attention_requests WHERE project = ?", p).Error
		}
	})

	ctx := context.Background()
	seed := func(project, id, message string) *agentdb.AttentionRequest {
		t.Helper()
		req, err := store.CreateAttentionRequest(ctx, &agentdb.AttentionRequest{
			ID: id, Project: project, SessionID: "sess-" + id, Worker: "tweet-author", Message: message,
		})
		if err != nil {
			t.Fatalf("seed %s/%s: %v", project, id, err)
		}
		return req
	}
	open := seed(mine, "attn-open", "which draft?")
	answered := seed(mine, "attn-answered", "already handled")
	seed(theirs, "attn-theirs", "not yours")
	if err := store.MarkAttentionAnswered(ctx, answered.ID, 1789000000); err != nil {
		t.Fatalf("mark answered: %v", err)
	}

	read := func(project, query string) []*agentdb.AttentionRequest {
		t.Helper()
		h := newHandlers(t, Config{
			Runner: stubRunner{}, Store: stubStore{},
			Identity: identityFor(project), AgentDB: store,
		})
		rec := do(h, http.MethodGet, "/agent/attention-requests"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("read %s: status=%d body=%s", project, rec.Code, rec.Body)
		}
		var body struct {
			AttentionRequests []*agentdb.AttentionRequest `json:"attention_requests"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.AttentionRequests
	}

	got := read(mine, "")
	if len(got) != 1 || got[0].ID != open.ID {
		t.Fatalf("the default read is the open stack only, got %+v", got)
	}
	if got[0].Message != "which draft?" {
		t.Fatalf("the message the worker wrote must survive the route: %+v", got[0])
	}
	if got := read(mine, "?state=all"); len(got) != 2 {
		t.Fatalf("state=all must include the answered row, got %+v", got)
	}
	if got := read(mine, "?state=all&limit=1"); len(got) != 1 {
		t.Fatalf("limit must cap the page, got %+v", got)
	}
	// A project= query is not a project selector, here or anywhere else (P5).
	if got := read(mine, "?project="+theirs); len(got) != 1 || got[0].Project != mine {
		t.Fatalf("a project= query must never cross the boundary, got %+v", got)
	}
	if got := read(theirs, "?state=all"); len(got) != 1 || got[0].ID != "attn-theirs" {
		t.Fatalf("the other project sees only its own row: %+v", got)
	}
}
