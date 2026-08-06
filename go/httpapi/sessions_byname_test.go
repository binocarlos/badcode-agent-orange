package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// recordingStore (session_createerror_test.go) is the only test store that keeps
// rows, and a name is a property of a persisted row rather than of a request —
// so these two methods make it satisfy SessionNameStore too. One fake then wires
// as BOTH Store and SessionNames, which is the shape agentd has: there they are
// the same *agentdb.Store, and a test that split them would prove nothing about
// the deployment.
//
// The name rules here are migration 035 in miniature: reject a duplicate
// (customer, name), ignore the unnamed rows entirely.
func (s *recordingStore) CreateSession(_ context.Context, sess *agentdb.Session) (*agentdb.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rows[sess.ID]; exists {
		return nil, fmt.Errorf("duplicate session id %q", sess.ID)
	}
	if sess.Name != "" {
		if err := agentdb.ValidateSessionName(sess.Name); err != nil {
			return nil, err
		}
		for _, row := range s.rows {
			if row.Customer == sess.Customer && row.Name == sess.Name {
				return nil, fmt.Errorf("%w: %q in project %q", agentdb.ErrSessionNameTaken, sess.Name, sess.Customer)
			}
		}
	}
	cp := *sess
	s.rows[sess.ID] = &cp
	return &cp, nil
}

func (s *recordingStore) GetSessionByName(_ context.Context, customer, name string) (*agentdb.Session, error) {
	if customer == "" {
		return nil, errors.New("cannot get agent session by name without customer")
	}
	// A name that cannot exist is reported as absent, exactly as the store does:
	// the caller's only sane response is the same 404 either way.
	if err := agentdb.ValidateSessionName(name); err != nil {
		return nil, fmt.Errorf("%w: %q in project %q", agentdb.ErrSessionNotFound, name, customer)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.Customer == customer && row.Name == name {
			cp := *row
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("%w: %q in project %q", agentdb.ErrSessionNotFound, name, customer)
}

// namingHandlers wires one recordingStore as both the RunnerStore and the
// SessionNameStore and hands back the store so a test can read rows.
func namingHandlers(t *testing.T, runner agentkit.Runner, ident IdentityFunc) (*Handlers, *recordingStore) {
	t.Helper()
	store := newRecordingStore()
	h := newHandlers(t, Config{
		Runner:       runner,
		Store:        store,
		SessionNames: store,
		Identity:     ident,
	})
	return h, store
}

func postSession(h *Handlers, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSession(rec, req)
	return rec
}

// --- POST /agent/session with a name -----------------------------------------

func TestCreateSessionWithNamePersistsIt(t *testing.T) {
	done := make(chan struct{})
	runner := stubRunner{createFn: func(context.Context, agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
		close(done)
		return &agentkit.SessionHandle{SessionID: "s-named", State: "running"}, nil
	}}
	h, store := namingHandlers(t, runner, okIdentity)

	rec := postSession(h, `{"sessionId":"s-named","name":"hypothesis-a"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	awaitCreate(t, done)

	if got := store.row("s-named").Name; got != "hypothesis-a" {
		t.Fatalf("stored name = %q, want %q", got, "hypothesis-a")
	}
	// The background status flip must not lose it — the row is rewritten whole
	// on every UpdateSession.
	waitFor(t, func() bool { return store.row("s-named").Status == "running" })
	if got := store.row("s-named").Name; got != "hypothesis-a" {
		t.Fatalf("the background status flip dropped the name: %q", got)
	}
}

// A name is decided BEFORE anything is provisioned: a rejected one must leave no
// half-started session, no container and no progress op behind.
func TestCreateSessionRejectsAMalformedName(t *testing.T) {
	for _, name := range []string{
		"Hypothesis-A",          // no case folding — names travel in URLs
		"has_underscore",        // kebab only
		"-leading",              // no leading hyphen
		"trailing-",             // no trailing hyphen
		"double--hyphen",        // single separators only
		"has space",             // would need escaping in a path segment
		"../etc",                // and this is why
		strings.Repeat("a", 65), // ≤64
	} {
		t.Run(name, func(t *testing.T) {
			var reached bool
			runner := stubRunner{createFn: func(context.Context, agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
				reached = true
				return nil, nil
			}}
			h, store := namingHandlers(t, runner, okIdentity)

			body, _ := json.Marshal(map[string]string{"sessionId": "s-bad", "name": name})
			rec := postSession(h, string(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			if reached {
				t.Fatal("a malformed name still provisioned a session")
			}
			if store.row("s-bad").ID != "" {
				t.Fatal("a malformed name still wrote a session row")
			}
		})
	}
}

func TestCreateSessionRejectsADuplicateName(t *testing.T) {
	var provisioned int
	runner := stubRunner{createFn: func(_ context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
		provisioned++
		return &agentkit.SessionHandle{SessionID: req.SessionID, State: "running"}, nil
	}}
	h, store := namingHandlers(t, runner, okIdentity)
	store.rows["s-first"] = &agentdb.Session{ID: "s-first", Customer: "acme", Name: "hypothesis-a"}

	rec := postSession(h, `{"sessionId":"s-second","name":"hypothesis-a"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	if store.row("s-second").ID != "" {
		t.Fatal("a duplicate name still wrote a session row")
	}
	if provisioned != 0 {
		t.Fatal("a duplicate name still provisioned a container")
	}
}

// The same name in another project is not a duplicate: names are scoped by the
// (customer, name) index, per P5.
func TestCreateSessionAllowsTheSameNameInAnotherProject(t *testing.T) {
	done := make(chan struct{})
	runner := stubRunner{createFn: func(context.Context, agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
		close(done)
		return &agentkit.SessionHandle{SessionID: "s-acme", State: "running"}, nil
	}}
	h, store := namingHandlers(t, runner, okIdentity) // Customer: "acme"
	store.rows["s-globex"] = &agentdb.Session{ID: "s-globex", Customer: "globex", Name: "hypothesis-a"}

	rec := postSession(h, `{"sessionId":"s-acme","name":"hypothesis-a"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	awaitCreate(t, done)
	if got := store.row("s-acme").Name; got != "hypothesis-a" {
		t.Fatalf("stored name = %q, want %q", got, "hypothesis-a")
	}
}

// The regression risk of this ticket: every existing caller sends no name, and
// must keep taking exactly the path it took before (upsert through Store, no
// SessionNames involvement at all).
func TestCreateSessionWithoutANameIsUnchanged(t *testing.T) {
	done := make(chan struct{})
	runner := stubRunner{createFn: func(context.Context, agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
		close(done)
		return &agentkit.SessionHandle{SessionID: "s-plain", State: "running"}, nil
	}}
	// Deliberately NO SessionNames: an unnamed create must not need the seam.
	store := newRecordingStore()
	h := newHandlers(t, Config{Runner: runner, Store: store, Identity: okIdentity})

	rec := postSession(h, `{"sessionId":"s-plain","persona":"helper"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	awaitCreate(t, done)
	row := store.row("s-plain")
	if row.Name != "" {
		t.Fatalf("an unnamed create stored name %q", row.Name)
	}
	if row.Persona != "helper" || row.Customer != "acme" {
		t.Fatalf("the unnamed create path changed shape: %+v", row)
	}
}

// The sqlite fallback's store has no name column and cannot enforce uniqueness,
// so a named create there is "not configured on this host" rather than a
// silently unnamed session.
func TestCreateSessionWithNameNeedsTheNameStore(t *testing.T) {
	var reached bool
	runner := stubRunner{createFn: func(context.Context, agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
		reached = true
		return nil, nil
	}}
	h := newHandlers(t, Config{Runner: runner, Store: newRecordingStore(), Identity: okIdentity})

	rec := postSession(h, `{"sessionId":"s1","name":"hypothesis-a"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", rec.Code, rec.Body)
	}
	if reached {
		t.Fatal("a named create was provisioned on a host that cannot store names")
	}
}

// No route renames a session, and the named create path is an INSERT precisely
// so it cannot become one: re-POSTing an id that already exists is refused
// rather than quietly re-labelling the row.
func TestNamedCreateNeverOverwritesAnExistingRow(t *testing.T) {
	h, store := namingHandlers(t, stubRunner{}, okIdentity)
	store.rows["s-live"] = &agentdb.Session{ID: "s-live", Customer: "acme", Name: "hypothesis-a", Status: "running"}

	rec := postSession(h, `{"sessionId":"s-live","name":"hypothesis-b"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("re-creating an existing id with a new name succeeded: %s", rec.Body)
	}
	if got := store.row("s-live").Name; got != "hypothesis-a" {
		t.Fatalf("the row was renamed to %q", got)
	}
}

// --- GET /agent/sessions/by-name/{name} ---------------------------------------

func getByName(h *Handlers, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/agent/sessions/by-name/"+name, nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	h.GetSessionByName(rec, req)
	return rec
}

func TestGetSessionByNameResolvesWithinTheProject(t *testing.T) {
	h, store := namingHandlers(t, stubRunner{}, okIdentity)
	store.rows["s-1"] = &agentdb.Session{
		ID: "s-1", Customer: "acme", Name: "hypothesis-a", Status: "running", Persona: "analyst",
	}

	rec := getByName(h, "hypothesis-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["id"] != "s-1" || out["name"] != "hypothesis-a" || out["status"] != "running" {
		t.Fatalf("body = %v", out)
	}
	// The one field this route must NOT carry: an embed token reaches it, and a
	// composed prompt is the project's system prompt plus its briefings.
	if _, leaked := out["composed_prompt"]; leaked {
		t.Fatal("the by-name route served composed_prompt")
	}
}

// Existence must not leak: "no such name" and "that name belongs to another
// project" have to be the same answer, byte for byte.
func TestSessionByNameDoesNotLeakExistenceAcrossProjects(t *testing.T) {
	h, store := namingHandlers(t, stubRunner{}, okIdentity) // Customer: "acme"
	store.rows["s-globex"] = &agentdb.Session{ID: "s-globex", Customer: "globex", Name: "hypothesis-a"}

	foreign := getByName(h, "hypothesis-a")
	absent := getByName(h, "no-such-name")

	if foreign.Code != http.StatusNotFound {
		t.Fatalf("another project's name = %d, want 404 (body %s)", foreign.Code, foreign.Body)
	}
	if absent.Code != foreign.Code || absent.Body.String() != foreign.Body.String() {
		t.Fatalf("absent (%d %q) is distinguishable from foreign (%d %q)",
			absent.Code, absent.Body, foreign.Code, foreign.Body)
	}
}

// An embed token (Identity.SessionScope) MUST be able to use this route — T12's
// embed page resolves a name to an id before it can mount the chat — but only
// for the one session it was minted for. Every other name is 404, so a token
// handed to a third-party browser cannot enumerate the project's names.
func TestSessionByNameHonoursTheSessionScope(t *testing.T) {
	scoped := func(*http.Request) (Identity, error) {
		return Identity{UserEmail: "embed", Customer: "acme", SessionScope: "s-mine"}, nil
	}
	h, store := namingHandlers(t, stubRunner{}, scoped)
	store.rows["s-mine"] = &agentdb.Session{ID: "s-mine", Customer: "acme", Name: "hypothesis-a"}
	store.rows["s-other"] = &agentdb.Session{ID: "s-other", Customer: "acme", Name: "hypothesis-b"}

	if rec := getByName(h, "hypothesis-a"); rec.Code != http.StatusOK {
		t.Fatalf("the embed token was refused its own session: %d %s", rec.Code, rec.Body)
	}
	sibling := getByName(h, "hypothesis-b")
	absent := getByName(h, "no-such-name")
	if sibling.Code != http.StatusNotFound {
		t.Fatalf("a scoped credential resolved a sibling session: %d %s", sibling.Code, sibling.Body)
	}
	if sibling.Body.String() != absent.Body.String() {
		t.Fatalf("a scoped credential can tell a sibling (%q) from an absent name (%q)", sibling.Body, absent.Body)
	}
}

func TestSessionByNameWithoutTheStoreIsNotImplemented(t *testing.T) {
	h := newHandlers(t, Config{Runner: stubRunner{}, Store: newRecordingStore(), Identity: okIdentity})
	if rec := getByName(h, "hypothesis-a"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", rec.Code, rec.Body)
	}
}

// Names are project-scoped, so a credential with no project has nothing to
// resolve against. Same posture as GET /agent/memories: 403, not 404 — there is
// no session being hidden, the question simply cannot be asked.
func TestSessionByNameNeedsAProject(t *testing.T) {
	anon := func(*http.Request) (Identity, error) { return Identity{}, nil }
	h, store := namingHandlers(t, stubRunner{}, anon)
	store.rows["s-1"] = &agentdb.Session{ID: "s-1", Name: "hypothesis-a"}

	if rec := getByName(h, "hypothesis-a"); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body)
	}
}

// The route has to be mounted, not merely written.
func TestSessionByNameIsRegistered(t *testing.T) {
	h, store := namingHandlers(t, stubRunner{}, okIdentity)
	store.rows["s-1"] = &agentdb.Session{ID: "s-1", Customer: "acme", Name: "hypothesis-a"}

	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, httptest.NewRequest("GET", "/agent/sessions/by-name/hypothesis-a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}
