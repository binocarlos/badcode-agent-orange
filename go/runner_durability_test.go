package agentkit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// D2 / doc 22 RD6 + RD24 — a crash mid-turn must not lose everything the model
// said, and a reconnect that RENDERS a turn must also RECORD it.
//
// The two halves are wired separately and tested separately:
//
//	(a) the default pipeline flushes on a cadence, so a turn that never reaches
//	    query_complete (agentd is killed) is already partly durable;
//	(b) Runner.Stream runs its SSE through a pipeline, so the events the sandbox
//	    replays out of its in-RAM buffer to a reconnecting client land in the
//	    store instead of only in the browser.

// durabilityHarness is a fake sandbox exposing both the turn route and the
// reconnect route, so one test can drive a turn, kill it, and re-attach.
type durabilityHarness struct {
	sessionID string
	// turnFrames are written to POST /query-stream, which then blocks until
	// turnRelease closes (or the request context dies) — a turn still running.
	turnFrames  []string
	turnRelease chan struct{}
	turnStarted chan struct{}
	// replayFrames are written to GET /stream/:queryId, standing in for the
	// sandbox's buffer replay, after which the stream ends.
	replayFrames []string

	mu           sync.Mutex
	streamAttach int
}

func (h *durabilityHarness) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fl, _ := w.(http.Flusher)
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/"+h.sessionID+"/query-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			for _, f := range h.turnFrames {
				_, _ = w.Write([]byte(f))
				if fl != nil {
					fl.Flush()
				}
			}
			if h.turnStarted != nil {
				close(h.turnStarted)
				h.turnStarted = nil
			}
			select {
			case <-h.turnRelease:
			case <-req.Context().Done():
			case <-time.After(5 * time.Second):
			}
		case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/sessions/"+h.sessionID+"/stream/"):
			h.mu.Lock()
			h.streamAttach++
			h.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			for _, f := range h.replayFrames {
				_, _ = w.Write([]byte(f))
				if fl != nil {
					fl.Flush()
				}
			}
		default:
			http.NotFound(w, req)
		}
	})
}

func newDurabilityRunner(t *testing.T, h *durabilityHarness, cadence time.Duration) (*runnerImpl, *agentkittest.MemStore) {
	t.Helper()
	ts := httptest.NewServer(h.handler())
	t.Cleanup(ts.Close)

	env := execenv.NewMock()
	env.AddrOverride = ts.URL
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  imageregistry.NewMock(),
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Policy:    Policy{BaseImage: "agentkit-sandbox:test", EventFlushCadence: cadence},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)
	if _, err := r.CreateSession(t.Context(), CreateSessionRequest{SessionID: h.sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return r, store
}

func flatText(t *testing.T, store *agentkittest.MemStore, sessionID string) string {
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
	return b.String()
}

// waitForStored polls until the transcript contains want. It is polling a
// background flush, so there is no happens-after signal to wait on; the timeout
// is the failure, not the wait.
func waitForStored(t *testing.T, store *agentkittest.MemStore, sessionID, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var got string
	for time.Now().Before(deadline) {
		got = flatText(t, store, sessionID)
		if strings.Contains(got, want) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return got
}

// (a) RD6: the model's words become durable DURING the turn, not only at
// query_complete. Nothing here cancels or completes anything — the turn is still
// running, exactly as it is when the machine agentd runs on dies.
func TestSendMessage_CadenceFlushesPartialTurnWhileStillRunning(t *testing.T) {
	h := &durabilityHarness{
		sessionID:   "s1",
		turnFrames:  []string{"event: content_delta\ndata: {\"delta\":\"the answer is 4\"}\n\n"},
		turnRelease: make(chan struct{}),
		turnStarted: make(chan struct{}),
	}
	started := h.turnStarted
	r, store := newDurabilityRunner(t, h, 10*time.Millisecond)
	defer close(h.turnRelease)

	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf bytes.Buffer
		_ = r.SendMessage(context.Background(), SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "what is 2+2"}, &buf)
	}()
	<-started

	got := waitForStored(t, store, "s1", "the answer is 4", 3*time.Second)
	if !strings.Contains(got, "the answer is 4") {
		t.Fatalf("an in-flight turn persisted no model output — a crash here loses everything the model said; stored: %q", got)
	}
	if !strings.Contains(got, "what is 2+2") {
		t.Fatalf("the human's prompt is missing from the flush: %q", got)
	}
}

// (b) RD6/RD24: a reconnect drains the sandbox's replay buffer, and what it
// renders is written down. The row already holds the pre-crash half (the prompt
// and the words flushed before the process died); the reconnect must APPEND to
// it, not replace it.
func TestStream_ReconnectPersistsReplayedBufferOntoTheExistingTurn(t *testing.T) {
	h := &durabilityHarness{
		sessionID:   "s1",
		turnRelease: make(chan struct{}),
		replayFrames: []string{
			"event: content_delta\ndata: {\"delta\":\" and a half\"}\n\n",
			"event: query_complete\ndata: {\"status\":\"complete\"}\n\n",
		},
	}
	defer close(h.turnRelease)
	r, store := newDurabilityRunner(t, h, 10*time.Millisecond)

	// What the pre-crash generation of this turn left behind.
	pre := []events.Envelope{
		{Type: events.UserMessage, Data: map[string]any{"content": "how long"}},
		{Type: events.ContentDelta, Data: map[string]any{"delta": "about an hour"}},
	}
	if err := store.PersistQueryEventsFlat(context.Background(), "s1", "q-live", pre, events.ExtractSearchText(pre)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var buf bytes.Buffer
	if err := r.Stream(context.Background(), SessionRef{SessionID: "s1"}, StreamOptions{QueryID: "q-live", IsReconnect: true}, &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if !strings.Contains(buf.String(), "and a half") {
		t.Fatalf("the reconnect did not relay the replay to the client: %q", buf.String())
	}
	got := flatText(t, store, "s1")
	if !strings.Contains(got, "and a half") {
		t.Fatalf("the reconnect rendered a turn it never recorded (RD6/RD24); stored: %q", got)
	}
	if !strings.Contains(got, "how long") {
		t.Fatalf("the reconnect erased the human's prompt to save the model's reply; stored: %q", got)
	}
	if !strings.Contains(got, "about an hour") {
		t.Fatalf("the reconnect erased the pre-crash half of the turn; stored: %q", got)
	}
	// Order matters: the pre-crash half must still come first, so the sentence
	// reads whole when the browser's reducer concatenates the deltas.
	if strings.Index(got, "about an hour") > strings.Index(got, "and a half") {
		t.Fatalf("the turn was reassembled out of order: %q", got)
	}
}

// No duplicate rows and no duplicate WORDS when the same events are seen twice:
// two successive reconnects replaying the same buffer must leave one transcript.
func TestStream_RepeatedReconnectDoesNotDuplicateEvents(t *testing.T) {
	h := &durabilityHarness{
		sessionID:   "s1",
		turnRelease: make(chan struct{}),
		replayFrames: []string{
			"event: content_delta\ndata: {\"delta\":\"once only\",\"messageId\":\"m1\"}\n\n",
			"event: query_complete\ndata: {\"status\":\"complete\"}\n\n",
		},
	}
	defer close(h.turnRelease)
	r, store := newDurabilityRunner(t, h, 10*time.Millisecond)

	for i := 0; i < 2; i++ {
		var buf bytes.Buffer
		if err := r.Stream(context.Background(), SessionRef{SessionID: "s1"}, StreamOptions{QueryID: "q-live", IsReconnect: true}, &buf); err != nil {
			t.Fatalf("Stream %d: %v", i, err)
		}
	}

	got := flatText(t, store, "s1")
	if n := strings.Count(got, "once only"); n != 1 {
		t.Fatalf("the model's words were recorded %d times, want once: %q", n, got)
	}
	evs, err := store.ListQueryEventsFlat(context.Background(), "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var complete int
	for _, e := range evs {
		if e.Type == events.QueryComplete {
			complete++
		}
	}
	if complete != 1 {
		t.Fatalf("query_complete recorded %d times, want once: %q", complete, got)
	}
}

// The single-writer rule. A second client attaching to a turn this process is
// already streaming must NOT persist: both attachments receive every event the
// sandbox sends, so two writers would record the same words twice.
func TestStream_DoesNotPersistWhileTheTurnIsOwnedElsewhere(t *testing.T) {
	h := &durabilityHarness{
		sessionID:    "s1",
		turnFrames:   []string{"event: content_delta\ndata: {\"delta\":\"live words\"}\n\n"},
		turnRelease:  make(chan struct{}),
		turnStarted:  make(chan struct{}),
		replayFrames: []string{"event: content_delta\ndata: {\"delta\":\"live words\"}\n\n"},
	}
	started := h.turnStarted
	r, store := newDurabilityRunner(t, h, 10*time.Millisecond)
	defer close(h.turnRelease)

	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf bytes.Buffer
		_ = r.SendMessage(context.Background(), SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "say something"}, &buf)
	}()
	<-started
	// The turn's own pipeline has flushed at least once by now.
	if got := waitForStored(t, store, "s1", "live words", 3*time.Second); !strings.Contains(got, "live words") {
		t.Fatalf("precondition: the turn's own pipeline never flushed: %q", got)
	}

	var buf bytes.Buffer
	if err := r.Stream(context.Background(), SessionRef{SessionID: "s1"}, StreamOptions{QueryID: "q-live", IsReconnect: true}, &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(buf.String(), "live words") {
		t.Fatalf("the second client was not served the stream: %q", buf.String())
	}

	got := flatText(t, store, "s1")
	if n := strings.Count(got, "live words"); n != 1 {
		t.Fatalf("a second attachment double-recorded the turn (%d copies): %q", n, got)
	}
}

// A store without the optional read-one-turn capability must not be written to
// by a reconnect at all: a write there would REPLACE the turn with its tail.
func TestStream_WithoutQueryEventReaderCapability_RelaysOnly(t *testing.T) {
	h := &durabilityHarness{
		sessionID:    "s1",
		turnRelease:  make(chan struct{}),
		replayFrames: []string{"event: content_delta\ndata: {\"delta\":\"tail only\"}\n\n"},
	}
	defer close(h.turnRelease)
	ts := httptest.NewServer(h.handler())
	t.Cleanup(ts.Close)
	env := execenv.NewMock()
	env.AddrOverride = ts.URL
	mem := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  imageregistry.NewMock(),
		Store:     noReaderStore{mem},
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Policy:    Policy{BaseImage: "agentkit-sandbox:test", EventFlushCadence: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)
	if _, err := r.CreateSession(t.Context(), CreateSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	pre := []events.Envelope{{Type: events.UserMessage, Data: map[string]any{"content": "keep me"}}}
	if err := mem.PersistQueryEventsFlat(context.Background(), "s1", "q-live", pre, "keep me"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var buf bytes.Buffer
	if err := r.Stream(context.Background(), SessionRef{SessionID: "s1"}, StreamOptions{QueryID: "q-live"}, &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(buf.String(), "tail only") {
		t.Fatalf("relay broken: %q", buf.String())
	}
	if got := flatText(t, mem, "s1"); !strings.Contains(got, "keep me") {
		t.Fatalf("a store that cannot be read back was written to anyway: %q", got)
	}
}

// noReaderStore is a RunnerStore that deliberately does NOT implement the
// optional ListQueryEventsFlatForQuery capability — the shape of a host store
// written against the published interface. Embedding the INTERFACE (not the
// MemStore) is what hides the capability: the method set is exactly RunnerStore.
type noReaderStore struct{ RunnerStore }
