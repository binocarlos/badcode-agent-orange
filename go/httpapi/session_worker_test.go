package httpapi

// session_worker_test.go — `worker` on POST /agent/session: "talk to the
// architect".
//
// Why this file exists, in one paragraph. The console has sent `worker` on this
// route since WorkerChatPanel was written, and until 2026-08-08 there was no
// such field on createSessionBody — encoding/json is not strict, so the name was
// dropped in silence and the "Chat with <worker>" button produced a bare session
// with none of the worker's prompt, tools or briefing. Nothing failed. The
// second, larger consequence was invisible for the same reason: Session.Worker
// stayed empty, and emitIdleFinish (runner.go) refuses to emit worker.finished
// for a session with no worker, so a human conversation could never wake
// anything downstream — the archivist in architect-archivist@v1 above all, whose
// entire job is to be woken when a conversation goes quiet.
//
// So the assertions here are not about a field being copied. They are about the
// two things that field has to make true: the chat runs as the worker (Persona,
// which resolves the prompt) and the chat IS the worker (Session.Worker, which
// is what the event spine stamps).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// capturingSessionStore records the row CreateSession persists, which is the
// only way to see Session.Worker: the response body carries {id,status} and
// nothing else.
type capturingSessionStore struct {
	stubStore
	mu   sync.Mutex
	rows []*agentdb.Session
}

func (c *capturingSessionStore) UpdateSession(_ context.Context, sess *agentdb.Session) (*agentdb.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := *sess
	c.rows = append(c.rows, &copied)
	return sess, nil
}

func (c *capturingSessionStore) last() *agentdb.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rows) == 0 {
		return nil
	}
	return c.rows[len(c.rows)-1]
}

// workerChatHandlers wires a project holding one enabled worker ("architect")
// and one disabled worker ("retired").
func workerChatHandlers(t *testing.T) (*Handlers, *capturingSessionStore, *chan agentkit.CreateSessionRequest) {
	t.Helper()
	workers := &fakeWorkerStore{rows: map[string]*agentdb.Worker{
		"acme/architect": {
			Project: "acme", Name: "architect",
			SystemPrompt: "you design orgs", Enabled: true,
		},
		"acme/retired": {
			Project: "acme", Name: "retired",
			SystemPrompt: "you did design orgs", Enabled: false,
		},
	}}
	store := &capturingSessionStore{}
	seen := make(chan agentkit.CreateSessionRequest, 4)
	h := newHandlers(t, Config{
		Runner: stubRunner{createFn: func(_ context.Context, r agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
			seen <- r
			return &agentkit.SessionHandle{SessionID: r.SessionID, State: "running"}, nil
		}},
		Store:    store,
		Identity: okIdentity,
		Workers:  workers,
	})
	return h, store, &seen
}

// The whole point: a chat that names a worker is that worker, in both senses.
func TestCreateSessionWithWorkerAttachesIdentityAndPrompt(t *testing.T) {
	h, store, seen := workerChatHandlers(t)

	rec := do(h, http.MethodPost, "/agent/session", `{"worker":"architect"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var resp map[string]any
	decodeInto(t, rec, &resp)
	if resp["id"] == "" {
		t.Fatal("no session id")
	}

	row := store.last()
	if row == nil {
		t.Fatal("no session row persisted")
	}
	// The identity half. Without this emitIdleFinish returns early and the
	// conversation emits NOTHING when it goes quiet — the defect this route
	// carried in silence.
	if row.Worker != "architect" {
		t.Errorf("session.worker = %q, want %q — worker.finished is stamped from this,"+
			" and emitIdleFinish refuses to emit without it", row.Worker, "architect")
	}
	// The prompt half. sessioncontext.go resolves scope.Persona to a worker row;
	// runner's turnSystemPrompt falls back to it whenever composed_prompt is
	// empty, which for a chat it always is.
	if row.Persona != "architect" {
		t.Errorf("session.persona = %q, want %q — this is what resolves the worker's prompt", row.Persona, "architect")
	}

	// Provisioning is deliberately detached from the request context (the handler
	// returns "creating" and works in the background), so this must WAIT rather
	// than peek — a bare `default:` here reads the channel before the goroutine
	// has run and fails every time.
	select {
	case req := <-*seen:
		if req.Worker != "architect" {
			t.Errorf("CreateSessionRequest.Worker = %q, want architect", req.Worker)
		}
		if req.Persona != "architect" {
			t.Errorf("CreateSessionRequest.Persona = %q, want architect", req.Persona)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runner was never asked to create a session")
	}
}

// An explicit persona is not overwritten. The two fields mean different things
// and a caller that sets both has said something specific; silently rewriting
// one would be the same class of quiet lie this route was fixed for.
func TestCreateSessionWorkerDoesNotOverrideAnExplicitPersona(t *testing.T) {
	h, store, _ := workerChatHandlers(t)

	rec := do(h, http.MethodPost, "/agent/session", `{"worker":"architect","persona":"something-else"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	row := store.last()
	if row.Persona != "something-else" {
		t.Errorf("persona = %q, want the caller's %q", row.Persona, "something-else")
	}
	if row.Worker != "architect" {
		t.Errorf("worker = %q, want architect regardless of persona", row.Worker)
	}
}

// REFUSING is the fix, not merely attaching. The old behaviour — accept
// anything, hand back a session that looks fine and has none of the worker's
// prompt — is exactly how a broken console button went unnoticed, and a typo
// would reproduce it byte for byte.
func TestCreateSessionRefusesAnUnknownWorker(t *testing.T) {
	h, store, seen := workerChatHandlers(t)

	rec := do(h, http.MethodPost, "/agent/session", `{"worker":"architetc"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 — a typo must not silently become a bare session. body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "architetc") {
		t.Errorf("the refusal must name what was asked for, got %q", rec.Body.String())
	}
	// Nothing left behind: no row, and the runner never asked to provision.
	if row := store.last(); row != nil {
		t.Errorf("a refused worker must leave no session row, got %+v", row)
	}
	// A short wait, not a peek: proving a background goroutine did NOT happen
	// needs time to pass, and `default:` would pass here even if provisioning
	// were about to start.
	select {
	case req := <-*seen:
		t.Fatalf("a refused worker must not provision a container, got a create for %s", req.SessionID)
	case <-time.After(250 * time.Millisecond):
	}
}

// "Disabled" must mean one thing. A disabled worker takes no dispatched job, so
// letting a human chat as one would make the flag mean something different
// depending on who is asking.
func TestCreateSessionRefusesADisabledWorker(t *testing.T) {
	h, _, _ := workerChatHandlers(t)

	rec := do(h, http.MethodPost, "/agent/session", `{"worker":"retired"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409. body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("the refusal must say why, got %q", rec.Body.String())
	}
}

// The unchanged path, pinned so this change cannot have broken every existing
// caller: no `worker`, no worker, and none of the new refusals.
func TestCreateSessionWithoutAWorkerIsUnchanged(t *testing.T) {
	h, store, _ := workerChatHandlers(t)

	rec := do(h, http.MethodPost, "/agent/session", `{"job":"chat"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	row := store.last()
	if row.Worker != "" {
		t.Errorf("worker = %q, want empty for a vanilla chat", row.Worker)
	}
	if row.Persona != "" {
		t.Errorf("persona = %q, want empty for a vanilla chat", row.Persona)
	}
}

// A host with no workers store (the sqlite fallback) must say so rather than
// accept the field and drop it — which is the precise behaviour being retired.
func TestCreateSessionWithWorkerNeedsAWorkersStore(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: stubStore{}, Identity: okIdentity,
	})
	rec := do(h, http.MethodPost, "/agent/session", `{"worker":"architect"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d, want 501 on a host with no workers table. body=%s", rec.Code, rec.Body)
	}
}

// The console's payload, verbatim, so the contract is pinned against the actual
// caller rather than against a shape only this test knows.
func TestCreateSessionAcceptsTheConsolePayload(t *testing.T) {
	h, store, _ := workerChatHandlers(t)

	// WorkerChatPanel.tsx: createSession({ customer, job, worker: worker.name })
	payload, err := json.Marshal(map[string]any{
		"customer": "acme", "job": "desk", "worker": "architect",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(h, http.MethodPost, "/agent/session", string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if row := store.last(); row.Worker != "architect" {
		t.Errorf("the console's own payload must attach the worker, got %q", row.Worker)
	}
}
