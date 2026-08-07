package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/events"
)

// The seven session-by-ID routes that authenticated but never authorized: they
// called identify() and then discarded the identity, so any logged-in principal
// could stream, message, inspect or cancel another project's session by guessing
// its id. The artifact routes were fixed first (artifacts_isolation_test.go);
// these are the rest of the surface.
//
// 404, not 403 — answering "forbidden" would confirm the session exists.
type byIDRoute struct {
	name   string
	call   func(*Handlers, http.ResponseWriter, *http.Request)
	method string
	path   string
	body   string
}

func sessionByIDRoutes() []byIDRoute {
	return []byIDRoute{
		{"stream", (*Handlers).Stream, "GET", "/agent/session/%s/stream", ""},
		{"reconnect", (*Handlers).Reconnect, "GET", "/agent/session/%s/reconnect", ""},
		{"send-message", (*Handlers).SendMessage, "POST", "/agent/session/%s/message", `{"content":"hi"}`},
		{"status", (*Handlers).Status, "GET", "/agent/session/%s/status", ""},
		{"cancel", (*Handlers).Cancel, "POST", "/agent/session/%s/cancel", ""},
		{"messages", (*Handlers).Messages, "GET", "/agent/session/%s/messages", ""},
		{"query-events", (*Handlers).QueryEvents, "GET", "/agent/session/%s/query-events", ""},
		{"get-session", (*Handlers).GetSession, "GET", "/agent/session/%s", ""},
	}
}

// trackingRunner records whether the Runner was reached at all. A 404 that still
// touched the session is not a fix.
func trackingRunner(reached *bool) stubRunner {
	mark := func() { *reached = true }
	return stubRunner{
		streamFn: func(context.Context, agentkit.SessionRef, agentkit.StreamOptions, agentkit.Writer) error {
			mark()
			return nil
		},
		sendFn: func(context.Context, agentkit.SessionRef, agentkit.SendMessageRequest, agentkit.Writer) error {
			mark()
			return nil
		},
		statusFn: func(_ context.Context, ref agentkit.SessionRef) (*agentkit.SessionStatus, error) {
			mark()
			return &agentkit.SessionStatus{SessionID: ref.SessionID, RuntimeState: "running"}, nil
		},
		stopFn: func(context.Context, agentkit.SessionRef) error {
			mark()
			return nil
		},
	}
}

func TestSessionByIDRoutesAreProjectScoped(t *testing.T) {
	// The session belongs to globex; the caller's token says acme.
	foreign := stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
		return &agentdb.Session{ID: id, Customer: "globex"}, nil
	}}

	for _, tc := range sessionByIDRoutes() {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			h := newHandlers(t, Config{
				Runner:   trackingRunner(&reached),
				Store:    foreign,
				Identity: okIdentity, // Customer: "acme"
			})
			req := httptest.NewRequest(tc.method, strings.Replace(tc.path, "%s", "s-globex", 1), strings.NewReader(tc.body))
			req.SetPathValue("id", "s-globex")
			rec := httptest.NewRecorder()
			tc.call(h, rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
			}
			if reached {
				t.Fatal("the runner was reached for another project's session")
			}
		})
	}
}

// Positive control: the same routes against the caller's own session still work.
// The SSE routes never write a status code other than 200, so "not 404" is the
// assertion that matters here.
func TestSessionByIDRoutesAllowOwnProject(t *testing.T) {
	own := stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
		return &agentdb.Session{ID: id, Customer: "acme"}, nil
	}}

	for _, tc := range sessionByIDRoutes() {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			h := newHandlers(t, Config{
				Runner:   trackingRunner(&reached),
				Store:    own,
				Identity: okIdentity, // Customer: "acme"
			})
			req := httptest.NewRequest(tc.method, strings.Replace(tc.path, "%s", "s-acme", 1), strings.NewReader(tc.body))
			req.SetPathValue("id", "s-acme")
			rec := httptest.NewRecorder()
			tc.call(h, rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("own-project request was refused: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

// An embed token carries scope "session:<id>", which agentd turns into
// Identity.SessionScope. Such a credential is confined to that one session even
// though its customer matches every session in the project.
func TestSessionScopedIdentityIsConfinedToItsSession(t *testing.T) {
	sameProject := stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
		return &agentdb.Session{ID: id, Customer: "acme"}, nil
	}}
	scoped := func(*http.Request) (Identity, error) {
		return Identity{UserEmail: "embed", Customer: "acme", SessionScope: "s-mine"}, nil
	}

	for _, tc := range sessionByIDRoutes() {
		t.Run(tc.name+"/foreign-session", func(t *testing.T) {
			var reached bool
			h := newHandlers(t, Config{
				Runner:   trackingRunner(&reached),
				Store:    sameProject,
				Identity: scoped,
			})
			req := httptest.NewRequest(tc.method, strings.Replace(tc.path, "%s", "s-other", 1), strings.NewReader(tc.body))
			req.SetPathValue("id", "s-other")
			rec := httptest.NewRecorder()
			tc.call(h, rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
			}
			if reached {
				t.Fatal("a session-scoped credential reached another session")
			}
		})
		t.Run(tc.name+"/own-session", func(t *testing.T) {
			var reached bool
			h := newHandlers(t, Config{
				Runner:   trackingRunner(&reached),
				Store:    sameProject,
				Identity: scoped,
			})
			req := httptest.NewRequest(tc.method, strings.Replace(tc.path, "%s", "s-mine", 1), strings.NewReader(tc.body))
			req.SetPathValue("id", "s-mine")
			rec := httptest.NewRecorder()
			tc.call(h, rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("scoped credential was refused its own session: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

// The scope also confines the routes that already had a tenancy check, so an
// embed token cannot delete, snapshot, archive or restore a sibling session, nor
// read its artifacts.
func TestSessionScopeAppliesToTheAlreadyGuardedRoutes(t *testing.T) {
	sameProject := stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
		return &agentdb.Session{ID: id, Customer: "acme"}, nil
	}}
	scoped := func(*http.Request) (Identity, error) {
		return Identity{UserEmail: "embed", Customer: "acme", SessionScope: "s-mine"}, nil
	}
	cases := []struct {
		name string
		call func(*Handlers, http.ResponseWriter, *http.Request)
	}{
		{"delete", (*Handlers).DeleteSession},
		{"snapshot", (*Handlers).Snapshot},
		{"archive", (*Handlers).Archive},
		{"restore", (*Handlers).Restore},
		{"artifacts", (*Handlers).Artifacts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandlers(t, Config{
				Runner:    stubRunner{},
				Store:     sameProject,
				Artifacts: &stubArtifacts{},
				Identity:  scoped,
			})
			req := httptest.NewRequest("POST", "/agent/session/s-other", nil)
			req.SetPathValue("id", "s-other")
			rec := httptest.NewRecorder()
			tc.call(h, rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
			}
		})
	}
}

// The empty-customer skip is what keeps dev-open mode (no JWT secret, principal
// with no customer) working, and what keeps the library usable by a host that
// does not populate Customer at all. Losing it would break every zero-config
// deployment, so it is pinned.
func TestUnscopedIdentityStillReachesUnownedSessions(t *testing.T) {
	anon := stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
		return &agentdb.Session{ID: id}, nil // no Customer
	}}
	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    anon,
		Identity: func(*http.Request) (Identity, error) { return Identity{}, nil },
	})
	req := httptest.NewRequest("GET", "/agent/session/s1/status", nil)
	req.SetPathValue("id", "s1")
	rec := httptest.NewRecorder()
	h.Status(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dev-open status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}

// The sqlite fallback (AgentDB nil) routes Messages and QueryEvents through
// listQueryEventsLegacy. That path used to run before any ownership check, so it
// was the one hole a project credential could still crawl through. It is now
// gated by the same ownsSession call as the AgentDB path.
func TestLegacyQueryEventsPathIsProjectScoped(t *testing.T) {
	var reached bool
	foreign := stubStore{
		getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
			return &agentdb.Session{ID: id, Customer: "globex"}, nil
		},
		listEventsFn: func(context.Context, string) ([]events.Envelope, error) {
			reached = true
			return nil, nil
		},
	}
	for _, name := range []string{"messages", "query-events"} {
		t.Run(name, func(t *testing.T) {
			reached = false
			h := newHandlers(t, Config{Runner: stubRunner{}, Store: foreign, Identity: okIdentity})
			req := httptest.NewRequest("GET", "/agent/session/s-globex/"+name, nil)
			req.SetPathValue("id", "s-globex")
			rec := httptest.NewRecorder()
			if name == "messages" {
				h.Messages(rec, req)
			} else {
				h.QueryEvents(rec, req)
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
			}
			if reached {
				t.Fatal("the legacy event store was read for another project's session")
			}
		})
	}
}
