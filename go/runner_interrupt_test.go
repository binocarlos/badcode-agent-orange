package agentkit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// ctxStore is a MemStore that honours the context on the persist path, the way
// agentdb's Postgres store does (database/sql aborts on a cancelled ctx). Reads
// stay unconditional so a test can inspect what survived.
type ctxStore struct {
	*agentkittest.MemStore
}

func (c *ctxStore) PersistQueryEventsFlat(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.MemStore.PersistQueryEventsFlat(ctx, sessionID, queryID, evs, searchText)
}

// newInterruptRunner wires a Runner against a fake sandbox whose SSE stream is
// driven by the test: it writes `pre` frames, signals `streaming`, then blocks
// until `release` closes. That lets a test cancel the caller's context at a
// precise point in the turn, which is what a browser reload does to the
// SendMessage request.
func newInterruptRunner(t *testing.T, sessionID string, pre []string, streaming chan<- struct{}, release <-chan struct{}) (*runnerImpl, *ctxStore) {
	r, s, _ := newInterruptRunnerWithWorkerEvents(t, sessionID, pre, streaming, release)
	return r, s
}

func newInterruptRunnerWithWorkerEvents(t *testing.T, sessionID string, pre []string, streaming chan<- struct{}, release <-chan struct{}) (*runnerImpl, *ctxStore, *fakeWorkerEvents) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true}`))
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/"+sessionID+"/query-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			for _, f := range pre {
				_, _ = w.Write([]byte(f))
				if fl != nil {
					fl.Flush()
				}
			}
			if streaming != nil {
				close(streaming)
			}
			select {
			case <-release:
			case <-req.Context().Done():
			case <-time.After(5 * time.Second):
			}
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(ts.Close)

	env := execenv.NewMock()
	env.AddrOverride = ts.URL
	mem := agentkittest.NewMemStore()
	store := &ctxStore{MemStore: mem}
	worker := newFakeWorkerEvents(mem)
	runner, err := NewRunner(Deps{
		Env:          env,
		Registry:     imageregistry.NewMock(),
		Store:        store,
		Artifacts:    artifacts.NewMock(),
		Claims:       agentkittest.StaticClaims{Token: "test-token"},
		WorkerEvents: worker,
		Policy:       Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), store, worker
}

func storedText(t *testing.T, store *ctxStore, sessionID string) (int, string) {
	t.Helper()
	evs, err := store.ListQueryEventsFlat(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListQueryEventsFlat: %v", err)
	}
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(string(e.Type))
		for _, k := range []string{"text", "content", "delta"} {
			if s, ok := e.Data[k].(string); ok {
				b.WriteString("=" + s)
			}
		}
		b.WriteString(" ")
	}
	return len(evs), b.String()
}

// The reported bug, at the Runner seam: a browser reload cancels the SendMessage
// request context, and the whole turn — including the human's own words — used
// to vanish, leaving GET /agent/session/{id}/messages at count 0.
func TestSendMessage_InterruptedBeforeAnyOutput_StillPersistsUserMessage(t *testing.T) {
	streaming := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	r, store := newInterruptRunner(t, "s1", nil, streaming, release)

	if _, err := r.CreateSession(t.Context(), CreateSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf bytes.Buffer
		_ = r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "tell me a joke"}, &buf)
	}()
	<-streaming
	cancel() // the human reloads the page
	<-done

	n, got := storedText(t, store, "s1")
	if n == 0 {
		t.Fatalf("interrupted turn persisted nothing — the human's message was lost (this is the reported bug)")
	}
	if !strings.Contains(got, "tell me a joke") {
		t.Fatalf("persisted %d events but not the user message: %s", n, got)
	}
}

// Interrupted mid-answer: the prompt AND the partial answer survive.
func TestSendMessage_InterruptedMidOutput_PersistsPartialTurn(t *testing.T) {
	streaming := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	pre := []string{"event: content_delta\ndata: {\"delta\":\"why did the\"}\n\n"}
	r, store := newInterruptRunner(t, "s1", pre, streaming, release)

	if _, err := r.CreateSession(t.Context(), CreateSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf bytes.Buffer
		_ = r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "tell me a joke"}, &buf)
	}()
	<-streaming
	time.Sleep(50 * time.Millisecond) // let the delta be scanned
	cancel()
	<-done

	n, got := storedText(t, store, "s1")
	if !strings.Contains(got, "tell me a joke") {
		t.Fatalf("persisted %d events but not the user message: %s", n, got)
	}
	if !strings.Contains(got, "why did the") {
		t.Fatalf("persisted %d events but discarded the partial answer: %s", n, got)
	}
}

// A worker job interrupted by a client disconnect must persist its transcript
// but emit NO worker.finished/worker.failed — E2's decision that a cancelled
// turn is not an outcome. Before the fix the failed persist returned
// context.Canceled from SendMessage, which emitJobOutcome read as a failed turn
// and turned into a spurious worker.failed; now the interruption is reported
// honestly as Status "cancelled" instead.
func TestSendMessage_InterruptedWorkerJob_PersistsButEmitsNoOutcome(t *testing.T) {
	streaming := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	pre := []string{"event: content_delta\ndata: {\"delta\":\"drafting\"}\n\n"}
	r, store, worker := newInterruptRunnerWithWorkerEvents(t, "s1", pre, streaming, release)
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Worker: "email-answerer"})

	if _, err := r.CreateSession(t.Context(), CreateSessionRequest{
		SessionID: "s1", Customer: "acme", Worker: "email-answerer",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf bytes.Buffer
		_ = r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "answer the email"}, &buf)
	}()
	<-streaming
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := worker.events(); len(got) != 0 {
		t.Fatalf("an interrupted turn emitted %d worker events, want 0: %#v", len(got), got)
	}
	n, got := storedText(t, store, "s1")
	if !strings.Contains(got, "answer the email") || !strings.Contains(got, "drafting") {
		t.Fatalf("interrupted worker turn not persisted (%d events): %s", n, got)
	}
}

// Regression floor: an uninterrupted turn is untouched by the fix.
func TestSendMessage_UninterruptedTurnPersistsWholeTurn(t *testing.T) {
	release := make(chan struct{})
	close(release)
	pre := []string{
		"event: content_delta\ndata: {\"delta\":\"why did the \"}\n\n",
		"event: content_delta\ndata: {\"delta\":\"chicken cross the road\"}\n\n",
		"event: query_complete\ndata: {\"status\":\"complete\"}\n\n",
	}
	r, store := newInterruptRunner(t, "s1", pre, nil, release)

	if _, err := r.CreateSession(t.Context(), CreateSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var buf bytes.Buffer
	if err := r.SendMessage(t.Context(), SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "tell me a joke"}, &buf); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	n, got := storedText(t, store, "s1")
	if !strings.Contains(got, "tell me a joke") || !strings.Contains(got, "chicken cross the road") {
		t.Fatalf("settled turn not fully persisted (%d events): %s", n, got)
	}
}
