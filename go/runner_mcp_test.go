package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// --- test doubles -------------------------------------------------------------

// createPayloadRecorder is a fake in-image control server that records every
// POST /sessions body — the wire protocol under test (§4.2).
type createPayloadRecorder struct {
	mu       sync.Mutex
	payloads []map[string]any
	// status/body, when set, are returned from POST /sessions instead of 200.
	status int
	body   string
}

func (c *createPayloadRecorder) server(t *testing.T, sessionID string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			var payload map[string]any
			_ = json.NewDecoder(req.Body).Decode(&payload)
			c.mu.Lock()
			c.payloads = append(c.payloads, payload)
			status, body := c.status, c.body
			c.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"sessionId":"` + sessionID + `"}}`))
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/"+sessionID+"/load-conversation":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (c *createPayloadRecorder) all() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]map[string]any, len(c.payloads))
	copy(out, c.payloads)
	return out
}

func (c *createPayloadRecorder) last() map[string]any {
	all := c.all()
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

// staticSessionContext is a host SessionContextProvider contributing project ∪
// worker MCP defaults (what agentd's provider resolves).
type staticSessionContext struct {
	sc  *extension.SessionContext
	err error
}

func (s staticSessionContext) Resolve(context.Context, extension.ContextScope) (*extension.SessionContext, error) {
	return s.sc, s.err
}

// newMCPTestRunner builds a hermetic runner whose sandbox address points at ts.
func newMCPTestRunner(t *testing.T, ts *httptest.Server, provider extension.SessionContextProvider) (*runnerImpl, *agentkittest.MemStore) {
	t.Helper()
	env := execenv.NewMock()
	env.AddrOverride = ts.URL
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:            env,
		Registry:       imageregistry.NewMock(),
		Store:          store,
		Artifacts:      artifacts.NewMock(),
		Claims:         agentkittest.StaticClaims{Token: "test-token"},
		Events:         events.NewPipeline(events.NewMockSink()),
		SessionContext: provider,
		Policy:         Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), store
}

// --- the item ------------------------------------------------------------------

// TestRunnerMCPServers is work-plan item A2: the wire protocol. Session MCP
// config must reach the sandbox on create, in exactly the documented shape, and
// must be re-supplied whenever the session is re-provisioned — MCP config is
// session config, not filesystem state, so a snapshot never carries it (§4.5).
func TestRunnerMCPServers(t *testing.T) {
	stdio := agentdb.MCPServerConfig{
		Command: "gmail-mcp",
		Args:    []string{"--stdio"},
		Env:     map[string]string{"GMAIL_API_KEY": "${GMAIL_API_KEY}"},
	}
	httpSrv := agentdb.MCPServerConfig{
		URL:     "http://notion:8080/mcp",
		Headers: map[string]string{"Authorization": "${NOTION_AUTH}"},
	}

	t.Run("create posts mcp_servers in the documented shape", func(t *testing.T) {
		rec := &createPayloadRecorder{}
		ts := rec.server(t, "s1")
		r, store := newMCPTestRunner(t, ts, nil)
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})

		if _, err := r.CreateSession(context.Background(), CreateSessionRequest{
			SessionID:  "s1",
			Customer:   "acme",
			MCPServers: map[string]MCPServerConfig{"gmail": stdio, "notion": httpSrv},
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		payload := rec.last()
		if payload == nil {
			t.Fatal("no POST /sessions recorded")
		}
		raw, ok := payload["mcp_servers"]
		if !ok {
			t.Fatalf("create payload has no mcp_servers key: %#v", payload)
		}
		// Round-trip through JSON so the assertion is about the WIRE shape, not
		// about Go types: the sandbox reads `command`/`args`/`env` and
		// `url`/`headers` (A3), which are exactly agentdb's json tags.
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("marshal mcp_servers: %v", err)
		}
		var got map[string]map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal mcp_servers: %v", err)
		}
		wantKeys := map[string][]string{
			"gmail":  {"command", "args", "env"},
			"notion": {"url", "headers"},
		}
		for name, keys := range wantKeys {
			server, ok := got[name]
			if !ok {
				t.Fatalf("mcp_servers missing %q: %#v", name, got)
			}
			for _, k := range keys {
				if _, ok := server[k]; !ok {
					t.Errorf("mcp_servers[%q] missing field %q: %#v", name, k, server)
				}
			}
		}
		if _, ok := got["gmail"]["url"]; ok {
			t.Errorf("stdio server must not carry a url field: %#v", got["gmail"])
		}
		if got["gmail"]["command"] != "gmail-mcp" {
			t.Errorf("command = %v, want gmail-mcp", got["gmail"]["command"])
		}
		if got["notion"]["url"] != "http://notion:8080/mcp" {
			t.Errorf("url = %v, want the http transport", got["notion"]["url"])
		}
	})

	t.Run("no mcp config means no key on the wire", func(t *testing.T) {
		rec := &createPayloadRecorder{}
		ts := rec.server(t, "s1")
		r, store := newMCPTestRunner(t, ts, nil)
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})

		if _, err := r.CreateSession(context.Background(), CreateSessionRequest{
			SessionID: "s1", Customer: "acme",
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, ok := rec.last()["mcp_servers"]; ok {
			t.Errorf("unconfigured session must not send mcp_servers: %#v", rec.last())
		}
	})

	t.Run("re-provision re-supplies the config from the session row", func(t *testing.T) {
		// Cases differ only in whether the restored session has a transcript:
		// re-supply must not depend on there being one to rehydrate.
		for _, tc := range []struct {
			name  string
			turns bool
		}{
			{name: "with transcript", turns: true},
			{name: "empty transcript", turns: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rec := &createPayloadRecorder{}
				ts := rec.server(t, "s1")
				r, store := newMCPTestRunner(t, ts, nil)
				store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})

				ctx := context.Background()
				if _, err := r.CreateSession(ctx, CreateSessionRequest{
					SessionID:  "s1",
					Customer:   "acme",
					MCPServers: map[string]MCPServerConfig{"gmail": stdio},
				}); err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				if tc.turns {
					if err := store.PersistQueryEventsFlat(ctx, "s1", "q-s1-1", []events.Envelope{
						{Type: events.UserMessage, Data: map[string]any{"content": "hi"}},
						{Type: events.ContentDelta, Data: map[string]any{"delta": "hello"}},
					}, "hi hello"); err != nil {
						t.Fatalf("seed events: %v", err)
					}
				}
				// Snapshot + destroy + resume is the re-provision path: a brand
				// new container that holds none of the session's config.
				if _, err := r.Snapshot(ctx, SessionRef{SessionID: "s1"}); err != nil {
					t.Fatalf("Snapshot: %v", err)
				}
				if err := r.Destroy(ctx, SessionRef{SessionID: "s1"}); err != nil {
					t.Fatalf("Destroy: %v", err)
				}
				before := len(rec.all())
				if _, err := r.Resume(ctx, SessionRef{SessionID: "s1"}); err != nil {
					t.Fatalf("Resume: %v", err)
				}
				all := rec.all()
				if len(all) <= before {
					t.Fatalf("resume did not re-create the sandbox session (%d posts)", len(all))
				}
				raw, ok := all[len(all)-1]["mcp_servers"]
				if !ok {
					t.Fatalf("resume create payload lost mcp_servers: %#v", all[len(all)-1])
				}
				b, _ := json.Marshal(raw)
				var got agentdb.MCPServers
				if err := json.Unmarshal(b, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if !reflect.DeepEqual(got, agentdb.MCPServers{"gmail": stdio}) {
					t.Errorf("re-supplied config = %#v, want the persisted one", got)
				}
			})
		}
	})

	t.Run("session-context defaults merge under the request", func(t *testing.T) {
		// The host resolves project ∪ worker MCP; the request may extend it and
		// wins name collisions (§5). Without this the union never reaches a
		// container at all.
		projectDefault := agentdb.MCPServerConfig{Command: "project-notion"}
		requestOverride := agentdb.MCPServerConfig{Command: "request-notion"}
		rec := &createPayloadRecorder{}
		ts := rec.server(t, "s1")
		r, store := newMCPTestRunner(t, ts, staticSessionContext{sc: &extension.SessionContext{
			MCPServers: agentdb.MCPServers{"gmail": stdio, "notion": projectDefault},
		}})
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})

		if _, err := r.CreateSession(context.Background(), CreateSessionRequest{
			SessionID:  "s1",
			Customer:   "acme",
			MCPServers: map[string]MCPServerConfig{"notion": requestOverride},
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		b, _ := json.Marshal(rec.last()["mcp_servers"])
		var got agentdb.MCPServers
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		want := agentdb.MCPServers{"gmail": stdio, "notion": requestOverride}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("merged config = %#v, want %#v", got, want)
		}
		// And the merged set is what resume will re-supply.
		sess, err := store.GetSession(context.Background(), "s1")
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if !reflect.DeepEqual(sess.MCPServers, want) {
			t.Errorf("persisted config = %#v, want %#v", sess.MCPServers, want)
		}
	})

	t.Run("sandbox rejection is a typed terminal error", func(t *testing.T) {
		rec := &createPayloadRecorder{
			status: 400,
			body:   `{"code":"INVALID_MCP_SERVERS","message":"gmail: exactly one transport"}`,
		}
		ts := rec.server(t, "s1")
		r, store := newMCPTestRunner(t, ts, nil)
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})

		_, err := r.CreateSession(context.Background(), CreateSessionRequest{
			SessionID:  "s1",
			Customer:   "acme",
			MCPServers: map[string]MCPServerConfig{"gmail": stdio},
		})
		var typed *ErrInvalidMCPServers
		if !errors.As(err, &typed) {
			t.Fatalf("CreateSession error = %v (%T), want *ErrInvalidMCPServers", err, err)
		}
		// A 400 that is NOT about MCP still maps to the harness error, so the
		// pre-existing host cleanup path is untouched.
		rec2 := &createPayloadRecorder{status: 400, body: `{"code":"UNKNOWN_HARNESS"}`}
		ts2 := rec2.server(t, "s2")
		r2, store2 := newMCPTestRunner(t, ts2, nil)
		store2.Seed(&agentdb.Session{ID: "s2", Customer: "acme"})
		_, err = r2.CreateSession(context.Background(), CreateSessionRequest{SessionID: "s2", Customer: "acme"})
		var harnessErr *ErrHarnessUnavailable
		if !errors.As(err, &harnessErr) {
			t.Fatalf("CreateSession error = %v (%T), want *ErrHarnessUnavailable", err, err)
		}
	})

	t.Run("a failing session-context provider fails the create", func(t *testing.T) {
		rec := &createPayloadRecorder{}
		ts := rec.server(t, "s1")
		r, store := newMCPTestRunner(t, ts, staticSessionContext{err: errors.New("settings unreachable")})
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})

		if _, err := r.CreateSession(context.Background(), CreateSessionRequest{
			SessionID: "s1", Customer: "acme",
		}); err == nil {
			t.Fatal("CreateSession succeeded despite an unresolvable session context")
		}
	})
}
