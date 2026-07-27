package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// TestArtifactRoutesAreProjectScoped is the §12 negative test at the HTTP edge.
//
// The ArtifactStore interface is session-keyed and takes no project, so the
// only place a cross-project read can be refused on the way in is here — the
// same ownsSession check GetSession and DeleteSession use. Before this, any
// authenticated caller could list, create into, or upload into another
// project's session by guessing its ID.
//
// 404, not 403: answering "forbidden" would confirm the session exists.
func TestArtifactRoutesAreProjectScoped(t *testing.T) {
	// The session belongs to globex; the caller's token says acme.
	foreign := stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
		return &agentdb.Session{ID: id, Customer: "globex"}, nil
	}}

	var reached bool
	store := &stubArtifacts{
		listFn: func(context.Context, string) ([]*artifacts.Artifact, error) {
			reached = true
			return []*artifacts.Artifact{{ID: "secret", SessionID: "s-globex"}}, nil
		},
		saveFn: func(_ context.Context, a *artifacts.Artifact, _ io.Reader) (*artifacts.Artifact, error) {
			reached = true
			return a, nil
		},
	}

	cases := []struct {
		name   string
		call   func(h *Handlers, w http.ResponseWriter, r *http.Request)
		method string
		url    string
		body   string
	}{
		{"list", (*Handlers).Artifacts, "GET", "/agent/session/s-globex/artifacts", ""},
		{"create", (*Handlers).CreateArtifact, "POST", "/agent/session/s-globex/artifacts", `{"path":"/x"}`},
		{"upload", (*Handlers).Upload, "POST", "/agent/session/s-globex/upload?path=/x", "bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			h := newHandlers(t, Config{
				Runner:    stubRunner{},
				Store:     foreign,
				Artifacts: store,
				Identity:  okIdentity, // Customer: "acme"
			})
			req := httptest.NewRequest(tc.method, tc.url, strings.NewReader(tc.body))
			req.SetPathValue("id", "s-globex")
			rec := httptest.NewRecorder()
			tc.call(h, rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
			}
			if reached {
				t.Fatal("the artifact store was reached for another project's session")
			}
		})
	}
}

// TestArtifactRoutesAllowOwnProject is the positive control: the same request
// against a session the caller's project owns goes through.
func TestArtifactRoutesAllowOwnProject(t *testing.T) {
	own := stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
		return &agentdb.Session{ID: id, Customer: "acme"}, nil
	}}
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store:  own,
		Artifacts: &stubArtifacts{listFn: func(context.Context, string) ([]*artifacts.Artifact, error) {
			return []*artifacts.Artifact{{ID: "a1", SessionID: "s-acme"}}, nil
		}},
		Identity: okIdentity, // Customer: "acme"
	})
	req := httptest.NewRequest("GET", "/agent/session/s-acme/artifacts", nil)
	req.SetPathValue("id", "s-acme")
	rec := httptest.NewRecorder()
	h.Artifacts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}
