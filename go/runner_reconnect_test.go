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

// D5 / doc 22 RD6 + RD24 — the reconnect path has to be REACHABLE, or the
// durability D2 built cannot fire.
//
// Three things had to be true and none of them were:
//
//	(i)   Status must name the in-flight turn. It read
//	      execenv.InstanceStatus.ActiveQueryID, which no adapter has ever set —
//	      so every status probe answered "nothing is running" and no client ever
//	      reached /reconnect.
//	(ii)  The runner and the in-image agent do not agree on what a query id is:
//	      agentd persists under `q-<session>-<n>`, the sandbox keys its replay
//	      buffer by a uuid of its own. Attaching with the wrong one gets a 200
//	      and none of the answer; persisting under the wrong one splits one turn
//	      across two rows.
//	(iii) The browser had the id and dropped it (web/src/useAgentSession.ts).
//
// These tests pin (i) and (ii). (iii) is pinned in web/src/plugins.test.ts.

// reconnectHarness is a fake sandbox that is HONEST ABOUT ITS KEY: it replays
// its buffer only to a stream attached under the id it minted for the turn, and
// answers any other id with an empty 200 — which is what the real one does,
// because its buffer map is keyed `sessionId:queryId`
// (sandbox/src/services/stream-service.ts).
type reconnectHarness struct {
	sessionID string
	// sandboxQueryID is the uuid the sandbox minted. It announces it in the
	// `connected` frame of the turn, and it is the only key that reads back.
	sandboxQueryID string
	turnFrames     []string
	replayFrames   []string
	turnRelease    chan struct{}
	turnStarted    chan struct{}

	mu          sync.Mutex
	attachPaths []string
}

func (h *reconnectHarness) paths() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.attachPaths...)
}

func (h *reconnectHarness) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fl, _ := w.(http.Flusher)
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/"+h.sessionID+"/query-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			// The sandbox announces its own id first, before anything else.
			frames := append([]string{
				"event: connected\ndata: {\"queryId\":\"" + h.sandboxQueryID + "\",\"sessionId\":\"" + h.sessionID + "\"}\n\n",
			}, h.turnFrames...)
			for _, f := range frames {
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
			h.attachPaths = append(h.attachPaths, req.URL.Path)
			h.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if req.URL.Path != "/sessions/"+h.sessionID+"/stream/"+h.sandboxQueryID {
				// Wrong key: a live connection to an empty buffer. No error, no
				// events — the failure this whole item is about.
				return
			}
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

func newReconnectRunner(t *testing.T, h *reconnectHarness, store *agentkittest.MemStore) *runnerImpl {
	t.Helper()
	ts := httptest.NewServer(h.handler())
	t.Cleanup(ts.Close)

	env := execenv.NewMock()
	env.AddrOverride = ts.URL
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  imageregistry.NewMock(),
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Policy:    Policy{BaseImage: "agentkit-sandbox:test", EventFlushCadence: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)
	if _, err := r.CreateSession(t.Context(), CreateSessionRequest{SessionID: h.sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return r
}

func rowFor(t *testing.T, store *agentkittest.MemStore, sessionID, queryID string) string {
	t.Helper()
	evs, err := store.ListQueryEventsFlatForQuery(context.Background(), sessionID, queryID)
	if err != nil {
		t.Fatalf("ListQueryEventsFlatForQuery: %v", err)
	}
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(string(e.Type))
		for _, k := range []string{"content", "delta"} {
			if s, ok := e.Data[k].(string); ok {
				b.WriteString("=" + s)
			}
		}
		b.WriteString(" ")
	}
	return b.String()
}

// (i) After the process that dispatched a turn is gone, Status must still be
// able to name that turn — under the RUNNER's id, because that is the row a
// reconnect has to continue.
func TestStatus_NamesTheInFlightTurnAfterARestart(t *testing.T) {
	h := &reconnectHarness{sessionID: "s1", sandboxQueryID: "sbx-uuid-1", turnRelease: make(chan struct{})}
	defer close(h.turnRelease)
	store := agentkittest.NewMemStore()
	// The previous agentd recorded the turn and then died. This runner is the
	// restarted one: its memory holds nothing.
	if err := store.SetActiveQuery(context.Background(), "s1", "q-s1-9", "sbx-uuid-1"); err != nil {
		t.Fatalf("SetActiveQuery: %v", err)
	}
	r := newReconnectRunner(t, h, store)

	st, err := r.Status(context.Background(), SessionRef{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.ActiveQueryID != "q-s1-9" {
		t.Fatalf("Status hid the in-flight turn after a restart: ActiveQueryID = %q, want %q. "+
			"A client told 'nothing is running' never calls /reconnect, so the answer the "+
			"sandbox is still holding is never collected (RD6).", st.ActiveQueryID, "q-s1-9")
	}
}

// ...and must stop naming it once the turn's row shows the turn ended, or every
// status probe for the rest of the session's life offers a reconnect to a
// buffer the sandbox dropped long ago.
func TestStatus_StopsNamingATurnWhoseRowAlreadyHoldsItsEnd(t *testing.T) {
	h := &reconnectHarness{sessionID: "s1", sandboxQueryID: "sbx-uuid-1", turnRelease: make(chan struct{})}
	defer close(h.turnRelease)
	store := agentkittest.NewMemStore()
	if err := store.SetActiveQuery(context.Background(), "s1", "q-s1-9", "sbx-uuid-1"); err != nil {
		t.Fatalf("SetActiveQuery: %v", err)
	}
	// Half a turn on the record: a prompt, no end. This is a live turn and must
	// be advertised — the assertion that fails without the fix at all.
	half := []events.Envelope{{Type: events.UserMessage, Data: map[string]any{"content": "hello"}}}
	if err := store.PersistQueryEventsFlat(context.Background(), "s1", "q-s1-9", half, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := newReconnectRunner(t, h, store)

	st, err := r.Status(context.Background(), SessionRef{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.ActiveQueryID != "q-s1-9" {
		t.Fatalf("a turn with no recorded end was not advertised: ActiveQueryID = %q", st.ActiveQueryID)
	}

	// Now the turn's row holds its end. The stale record on the session row is a
	// claim, not a fact, and this is what stops it outliving the turn.
	done := append(half, events.Envelope{Type: events.QueryComplete, Data: map[string]any{"status": "complete"}})
	if err := store.PersistQueryEventsFlat(context.Background(), "s1", "q-s1-9", done, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	st, err = r.Status(context.Background(), SessionRef{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.ActiveQueryID != "" {
		t.Fatalf("Status advertised a finished turn as in flight: ActiveQueryID = %q", st.ActiveQueryID)
	}
}

// (ii) The reconnect attaches under the SANDBOX's id and persists under the
// RUNNER's. Both halves are asserted, because getting either one wrong is
// silent: the wrong attach id drains nothing, and the wrong persist id would
// write the second half of one turn into a row of its own.
func TestStream_ReconnectTranslatesTheIDAndKeepsOneRowPerTurn(t *testing.T) {
	h := &reconnectHarness{
		sessionID:      "s1",
		sandboxQueryID: "sbx-uuid-7",
		turnRelease:    make(chan struct{}),
		replayFrames: []string{
			"event: content_delta\ndata: {\"delta\":\" and a half\"}\n\n",
			"event: query_complete\ndata: {\"status\":\"complete\"}\n\n",
		},
	}
	defer close(h.turnRelease)
	store := agentkittest.NewMemStore()
	if err := store.SetActiveQuery(context.Background(), "s1", "q-s1-7", "sbx-uuid-7"); err != nil {
		t.Fatalf("SetActiveQuery: %v", err)
	}
	pre := []events.Envelope{
		{Type: events.UserMessage, Data: map[string]any{"content": "how long"}},
		{Type: events.ContentDelta, Data: map[string]any{"delta": "about an hour"}},
	}
	if err := store.PersistQueryEventsFlat(context.Background(), "s1", "q-s1-7", pre, events.ExtractSearchText(pre)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := newReconnectRunner(t, h, store)

	// The client reconnects with the only id it was ever given: the runner's.
	var buf bytes.Buffer
	if err := r.Stream(context.Background(), SessionRef{SessionID: "s1"},
		StreamOptions{QueryID: "q-s1-7", IsReconnect: true}, &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	wantPath := "/sessions/s1/stream/sbx-uuid-7"
	if got := h.paths(); len(got) != 1 || got[0] != wantPath {
		t.Fatalf("the reconnect attached with the wrong id: attached %v, want [%s]. "+
			"The sandbox keys its replay buffer by the id IT minted, so any other key "+
			"is a live connection to an empty buffer.", got, wantPath)
	}
	if !strings.Contains(buf.String(), "and a half") {
		t.Fatalf("the reconnect relayed nothing to the client: %q", buf.String())
	}

	// One turn, one row: the reconnect appended to the pre-crash half.
	row := rowFor(t, store, "s1", "q-s1-7")
	for _, want := range []string{"how long", "about an hour", "and a half"} {
		if !strings.Contains(row, want) {
			t.Fatalf("the turn's row lost %q — got %q", want, row)
		}
	}
	// ...and nothing was written under the sandbox's id, which would be one turn
	// rendered as two in every transcript that reads it back.
	if stray := rowFor(t, store, "s1", "sbx-uuid-7"); stray != "" {
		t.Fatalf("the reconnect wrote a SECOND row keyed by the sandbox's id: %q", stray)
	}

	// The turn ended in this attachment, so it must no longer be advertised.
	if qid, _, _ := store.GetActiveQuery(context.Background(), "s1"); qid != "" {
		t.Fatalf("a drained-to-completion turn is still recorded as in flight: %q", qid)
	}
}

// The dispatch side of (ii): a turn records both ids as it starts, so a crash at
// any point after the first frame leaves enough behind to find it again.
func TestSendMessage_RecordsBothIDsAndRetiresThemWhenTheTurnEnds(t *testing.T) {
	h := &reconnectHarness{
		sessionID:      "s1",
		sandboxQueryID: "sbx-uuid-3",
		turnRelease:    make(chan struct{}),
		turnStarted:    make(chan struct{}),
		turnFrames:     []string{"event: content_delta\ndata: {\"delta\":\"four\"}\n\n"},
	}
	started := h.turnStarted
	store := agentkittest.NewMemStore()
	r := newReconnectRunner(t, h, store)

	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf bytes.Buffer
		_ = r.SendMessage(context.Background(), SessionRef{SessionID: "s1"},
			SendMessageRequest{Content: "what is 2+2"}, &buf)
	}()
	<-started

	// While the turn is still running, both ids must be on record.
	var qid, sbx string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		qid, sbx, _ = store.GetActiveQuery(context.Background(), "s1")
		if qid != "" && sbx != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if qid == "" {
		t.Fatalf("an in-flight turn recorded no query id — after a restart nothing knows a turn is running")
	}
	if !strings.HasPrefix(qid, "q-s1-") {
		t.Fatalf("the recorded id is not the runner's (the one rows are keyed by): %q", qid)
	}
	if sbx != "sbx-uuid-3" {
		t.Fatalf("the sandbox's id for the turn was not recorded: %q — without it a reconnect "+
			"cannot attach to the buffer holding the answer", sbx)
	}

	// Finish the turn: the record must retire, or Status offers a reconnect to a
	// buffer that no longer exists.
	close(h.turnRelease)
	<-done
	if qid, _, _ := store.GetActiveQuery(context.Background(), "s1"); qid != "" {
		t.Fatalf("a settled turn is still recorded as in flight: %q", qid)
	}
}

// A turn interrupted by its client going away is NOT settled: the sandbox keeps
// running it. Forgetting it there is the same bug as never recording it.
func TestSendMessage_KeepsTheRecordWhenTheClientVanishesMidTurn(t *testing.T) {
	h := &reconnectHarness{
		sessionID:      "s1",
		sandboxQueryID: "sbx-uuid-4",
		turnRelease:    make(chan struct{}),
		turnStarted:    make(chan struct{}),
		turnFrames:     []string{"event: content_delta\ndata: {\"delta\":\"thinking\"}\n\n"},
	}
	defer close(h.turnRelease)
	started := h.turnStarted
	store := agentkittest.NewMemStore()
	r := newReconnectRunner(t, h, store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf bytes.Buffer
		_ = r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "hello"}, &buf)
	}()
	<-started
	// Wait until the sandbox's id is on record, then drop the client.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, sbx, _ := store.GetActiveQuery(ctx, "s1"); sbx != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	qid, sbx, _ := store.GetActiveQuery(context.Background(), "s1")
	if qid == "" || sbx != "sbx-uuid-4" {
		t.Fatalf("a turn whose client went away was forgotten (%q/%q) — it is still running in "+
			"the sandbox, and this record is the only route back to it", qid, sbx)
	}
}
