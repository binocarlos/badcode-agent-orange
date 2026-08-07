package agentkit

// runner_coremcp_test.go — what happens when the host puts its core tool server
// on the create request of a plain chat session (httpapi.Config.CoreMCP).
//
// The httpapi cases prove the merge happened. This one proves the merge is
// HARMLESS: the project's own MCP servers, the project system prompt and the
// project base image all still reach the session exactly as they did, because
// they arrive by a different route — the host's SessionContextProvider, which
// the Runner folds UNDER the request (runner.go:mergeSessionMCPServers). That
// ordering is also what makes core non-overridable, which is the same rule
// ComposeJob applies to dispatched jobs (compose.go:436-441).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

// chatSessionRecorder is a fake sandbox that records both halves of the launch
// this test is about: the MCP config posted at create, and the system prompt of
// each turn. The existing recorders each capture one of the two.
type chatSessionRecorder struct {
	mu       sync.Mutex
	creates  []map[string]any
	prompts  []string
	sessions string
}

func (c *chatSessionRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			var payload map[string]any
			_ = json.NewDecoder(req.Body).Decode(&payload)
			c.mu.Lock()
			c.creates = append(c.creates, payload)
			c.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"sessionId":"` + c.sessions + `"}}`))
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/load-conversation"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/query-stream"):
			var payload struct {
				SystemPrompt string `json:"systemPrompt"`
			}
			_ = json.NewDecoder(req.Body).Decode(&payload)
			c.mu.Lock()
			c.prompts = append(c.prompts, payload.SystemPrompt)
			c.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: content_delta\ndata: {\"delta\":\"ok\"}\n\n"))
			_, _ = w.Write([]byte("event: query_complete\ndata: {\"status\":\"complete\"}\n\n"))
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// wireMCPServers is the mcp_servers of the last create, read back through JSON
// so the assertion is about what crossed the wire.
func (c *chatSessionRecorder) wireMCPServers(t *testing.T) agentdb.MCPServers {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.creates) == 0 {
		t.Fatal("the sandbox was never asked to create a session")
	}
	raw, ok := c.creates[len(c.creates)-1]["mcp_servers"]
	if !ok {
		t.Fatalf("create payload carries no mcp_servers: %#v", c.creates[len(c.creates)-1])
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal mcp_servers: %v", err)
	}
	var got agentdb.MCPServers
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal mcp_servers: %v", err)
	}
	return got
}

// TestCoreMCPOnAChatSessionLeavesTheProjectContextIntact is the regression half
// of T15. A chat session created with the host's core servers on the request
// must end up with core ∪ project ∪ worker tools — core winning a name
// collision — while the three things the SessionContextProvider already
// delivered are untouched: the project prompt (re-resolved per turn), the
// project base image (resolved through the §13 catalogue), and the project's
// own MCP servers.
func TestCoreMCPOnAChatSessionLeavesTheProjectContextIntact(t *testing.T) {
	const (
		projectPrompt = "House style. Be brief."
		// A curated catalogue name, not a registry reference: newCatalogueStub
		// resolves it to registry.local/acme/toolbox:3. Using the pointer form
		// is the point — the I4 fix (sessioncontext.go:139-142) is what a
		// composition-based create path would have re-broken.
		projectBase = "toolbox"
	)
	core := agentdb.MCPServers{
		"agentkit-core": {URL: "http://agentd:8099/mcp", Headers: map[string]string{"Authorization": "${SESSION_TOKEN}"}},
	}
	project := agentdb.MCPServers{
		"gmail": {Command: "gmail-mcp", Args: []string{"--stdio"}},
		// A project trying to shadow the core server by name. Core is merged
		// last for exactly this case: memory_search must not be reroutable by
		// editing project_settings.mcp_config.
		"agentkit-core": {Command: "impostor"},
	}

	rec := &chatSessionRecorder{sessions: "s-chat"}
	ts := rec.server(t)
	env := execenv.NewMock()
	env.AddrOverride = ts.URL
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  imageregistry.NewMock(),
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Images:    newCatalogueStub(),
		SessionContext: staticSessionContext{sc: &extension.SessionContext{
			SystemPrompt:     projectPrompt,
			BaseImage:        projectBase,
			ProjectBaseImage: projectBase,
			MCPServers:       project,
		}},
		Policy: Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)

	ctx := context.Background()
	store.Seed(&agentdb.Session{ID: "s-chat", Customer: "acme", Persona: "marketing"})
	// Exactly what httpapi.CreateSession now sends: a vanilla session, no
	// worker, no system prompt, core servers on the request.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-chat", Customer: "acme", Persona: "marketing", MCPServers: core,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s-chat"},
		SendMessageRequest{Content: "hi", Customer: "acme"}, io.Discard); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	want := agentdb.MCPServers{
		"gmail":         project["gmail"],
		"agentkit-core": core["agentkit-core"],
	}
	if got := rec.wireMCPServers(t); !reflect.DeepEqual(got, want) {
		t.Errorf("mcp_servers on the wire = %#v, want core over project = %#v", got, want)
	}

	sess, err := store.GetSession(ctx, "s-chat")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// The merged set is what a restore re-supplies (§4.5) — a chat session that
	// loses its container must come back with its tools.
	if !reflect.DeepEqual(sess.MCPServers, want) {
		t.Errorf("persisted mcp config = %#v, want %#v", sess.MCPServers, want)
	}
	// Unchanged #1: the prompt still comes from the provider, per turn.
	if len(rec.prompts) != 1 || rec.prompts[0] != projectPrompt {
		t.Errorf("system prompt sent to the model = %#v, want the provider's %q", rec.prompts, projectPrompt)
	}
	// Unchanged #2: the project base image still resolves through the catalogue.
	if len(env.Provisions) != 1 {
		t.Fatalf("expected one provision, got %d", len(env.Provisions))
	}
	if got := string(env.Provisions[0].Image); got != "registry.local/acme/toolbox:3" {
		t.Errorf("launch image = %q, want the catalogue's resolution of %q", got, projectBase)
	}
	// Unchanged #3: a chat session gains no worker identity. Setting one would
	// make it emit worker.finished with its transcript onto the event spine —
	// the reason ComposeJob was rejected for this path.
	if sess.Worker != "" || sess.ComposedPrompt != "" {
		t.Errorf("chat session was given a composition: worker=%q composed_prompt=%q", sess.Worker, sess.ComposedPrompt)
	}
}
