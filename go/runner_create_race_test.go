package agentkit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bayes-price/agentkit/agentdb"
)

// TestSendMessage_DuringAsyncCreate reproduces the "Restore failed: session has
// no running instance and no snapshot" race.
//
// Hosts background CreateSession (image pulls can take minutes) after calling
// MarkCreating synchronously. A first message can therefore reach the runner
// while provisioning is still in flight — the instance is not yet tracked. The
// runner must WAIT for the in-flight create to settle, not fall into
// restore-from-snapshot (which fails for a brand-new session that has no
// snapshot, surfacing a spurious "Restore failed" to the user).
func TestSendMessage_DuringAsyncCreate(t *testing.T) {
	ctx := context.Background()
	r, env, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Job: "j1"})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"sessionId":"s1"}}`))
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/s1/query-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			for _, f := range []string{
				"event: content_delta\ndata: {\"delta\":\"Hello\"}\n\n",
				"event: query_complete\ndata: {\"status\":\"complete\"}\n\n",
			} {
				_, _ = w.Write([]byte(f))
				if fl != nil {
					fl.Flush()
				}
			}
		default:
			http.NotFound(w, req)
		}
	}))
	defer ts.Close()
	env.AddrOverride = ts.URL

	// Host path: MarkCreating runs synchronously, CreateSession is backgrounded.
	r.MarkCreating("s1")
	go func() {
		// A brief delay guarantees SendMessage reaches ensureRunning while the
		// create op is still in flight — the exact window that used to break.
		time.Sleep(30 * time.Millisecond)
		_, _ = r.CreateSession(ctx, CreateSessionRequest{SessionID: "s1", Customer: "acme", Job: "j1"})
	}()

	var buf bytes.Buffer
	err := r.SendMessage(ctx, SessionRef{SessionID: "s1", ScopedToken: "test-token"},
		SendMessageRequest{Content: "hello"}, &buf)
	if err != nil {
		t.Fatalf("SendMessage during async create should wait for provisioning, got error: %v", err)
	}
	if !strings.Contains(buf.String(), "Hello") {
		t.Errorf("client did not receive streamed content: %q", buf.String())
	}

	// No spurious restore op must have been attempted — the create op must remain
	// the only lifecycle op for this session.
	if p, ok := r.progress.get("s1"); ok && p.Op == "restore" {
		t.Errorf("ensureRunning wrongly attempted restore during create: %+v", p)
	}
}
