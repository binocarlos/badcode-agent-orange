package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// pausingRegistry blocks EnsurePresent — the image pull — until released. It
// models the window the whole async-create design exists for: a pull that takes
// seconds to minutes, during which the POST has already been answered "creating"
// and the caller is free to DELETE.
type pausingRegistry struct {
	*imageregistry.MockImageRegistry
	arrived chan struct{}
	release chan struct{}
}

func newPausingRegistry() *pausingRegistry {
	return &pausingRegistry{
		MockImageRegistry: imageregistry.NewMock(),
		arrived:           make(chan struct{}),
		release:           make(chan struct{}),
	}
}

func (p *pausingRegistry) EnsurePresent(ctx context.Context, ref execenv.ImageRef) error {
	close(p.arrived)
	<-p.release
	return p.MockImageRegistry.EnsurePresent(ctx, ref)
}

// TestDeleteDuringBackgroundCreateLeavesNoContainer is the HTTP-level statement
// of the port-pool leak.
//
// POST /agent/session answers 200 "creating" and provisions in a goroutine.
// DELETE the session inside that window and, before the fix, the container
// arrived seconds later owned by nothing: the row was gone, so no loop that
// iterates sessions could see it, and it held one of the host's finite ports
// until somebody went looking with `docker ps`.
//
// It runs a REAL Runner behind the handlers, because the race lives in the seam
// between the two.
func TestDeleteDuringBackgroundCreateLeavesNoContainer(t *testing.T) {
	env := execenv.NewMock()
	reg := newPausingRegistry()
	store := agentkittest.NewMemStore()

	runner, err := agentkit.NewRunner(agentkit.Deps{
		Env:       env,
		Registry:  reg,
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy:    agentkit.Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// Wrap the runner so the test can wait for the background create to RETURN.
	// Without that it could sample the environment before provisioning had even
	// happened and call the absence of a container a pass.
	settled := &createSettledRunner{Runner: runner, done: make(chan struct{})}
	h := newHandlers(t, Config{Runner: settled, Store: store, Identity: okIdentity})

	// POST: answered immediately, provisioning continues in the background.
	post := httptest.NewRequest("POST", "/agent/session", strings.NewReader(`{"sessionId":"s-race"}`))
	postRec := httptest.NewRecorder()
	h.CreateSession(postRec, post)
	if postRec.Code != http.StatusOK {
		t.Fatalf("create: status=%d body=%s", postRec.Code, postRec.Body)
	}

	// Wait until the background create is inside the image pull.
	select {
	case <-reg.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("background create never reached the image pull")
	}

	// DELETE mid-create — the exact move that manufactured the orphans.
	del := httptest.NewRequest("DELETE", "/agent/session/s-race", nil)
	del.SetPathValue("id", "s-race")
	delRec := httptest.NewRecorder()
	h.DeleteSession(delRec, del)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete: status=%d body=%s", delRec.Code, delRec.Body)
	}

	// Let the create run to completion, and wait for it to actually finish.
	close(reg.release)
	select {
	case <-settled.done:
	case <-time.After(5 * time.Second):
		t.Fatal("background create never returned")
	}

	// The create must leave nothing running.
	insts, rerr := env.Recover(context.Background())
	if rerr != nil {
		t.Fatalf("Recover: %v", rerr)
	}
	if len(insts) != 0 {
		t.Fatalf("a session deleted mid-create left %d running container(s) behind, holding their host ports", len(insts))
	}

	// And the row really is gone (the delete did its half).
	if _, gerr := store.GetSession(context.Background(), "s-race"); gerr == nil {
		t.Error("session row survived the delete")
	}
}

// createSettledRunner signals when the (backgrounded) CreateSession returns.
type createSettledRunner struct {
	agentkit.Runner
	done chan struct{}
}

func (c *createSettledRunner) CreateSession(ctx context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
	defer close(c.done)
	return c.Runner.CreateSession(ctx, req)
}

// MarkCreating forwards the capability probe the handler makes; without it the
// wrapper would silently disable the pre-registration under test.
func (c *createSettledRunner) MarkCreating(id string) {
	if mc, ok := c.Runner.(interface{ MarkCreating(string) }); ok {
		mc.MarkCreating(id)
	}
}

// TestCreateRegistersWithTheRunnerBeforeTheRowExists pins the ordering the fix
// depends on: a DELETE cannot be accepted until the session row exists (the
// ownership check 404s without it), so the create must be registered with the
// Runner BEFORE the row is written. Any other order leaves a window in which a
// delete is possible but invisible to the in-flight create.
func TestCreateRegistersWithTheRunnerBeforeTheRowExists(t *testing.T) {
	store := agentkittest.NewMemStore()
	seen := make(chan string, 4)

	runner := &orderRecordingRunner{store: store, seen: seen}
	h := newHandlers(t, Config{Runner: runner, Store: store, Identity: okIdentity})

	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(`{"sessionId":"s-order"}`))
	h.CreateSession(httptest.NewRecorder(), req)

	select {
	case first := <-seen:
		if first != "markCreating" {
			t.Fatalf("first observable step was %q; MarkCreating must run before the session row is written", first)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MarkCreating was never called")
	}
}

// orderRecordingRunner records when MarkCreating happens relative to the row
// write the handler performs.
type orderRecordingRunner struct {
	stubRunner
	store *agentkittest.MemStore
	seen  chan string
}

func (o *orderRecordingRunner) MarkCreating(id string) {
	if _, err := o.store.GetSession(context.Background(), id); err == nil {
		o.seen <- "rowWritten"
		return
	}
	o.seen <- "markCreating"
}
