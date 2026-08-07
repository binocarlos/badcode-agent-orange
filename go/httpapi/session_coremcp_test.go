package httpapi

// session_coremcp_test.go — the core tool server on an HTTP-created session.
//
// Principle: there is ONE kind of session. A session started by a user in the
// chat UI must launch with the same tools a dispatched job gets, and until this
// landed it did not: the project ∪ worker MCP config reaches every create
// through the Runner's SessionContextProvider (runner.go:366,484-495), but the
// host's own core tool server — memory_*, worker_*, image_*, config_* — had
// exactly one call site, the dispatcher. A long-lived chat session therefore
// could not call memory_current at all.
//
// What these cases can and cannot prove: they prove the config was MERGED into
// the create request. That the tool then answers is a property of the running
// stack (the core MCP endpoint, the session token, the sandbox resolving
// ${SESSION_TOKEN}) and is owed a stack check, not a unit test.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// coreServers is the shape cmd/agentd's coreMCPServers(selfURL) produces
// (mcpserver.go:543-552): one http transport whose Authorization is the
// whole-value ${SESSION_TOKEN} reference the sandbox resolves at spawn time.
func coreServers() agentdb.MCPServers {
	return agentdb.MCPServers{
		"agentkit-core": {
			URL:     "http://agentd:8099/mcp",
			Headers: map[string]string{"Authorization": "${SESSION_TOKEN}"},
		},
	}
}

func TestCreateSessionMergesTheCoreMCPServers(t *testing.T) {
	tests := []struct {
		name string
		core agentdb.MCPServers
		want agentdb.MCPServers
	}{
		{
			// The Postgres deployment: agentd mounts the core MCP server, so
			// every session it creates is told how to reach it.
			name: "a chat session is told about the core tool server",
			core: coreServers(),
			want: coreServers(),
		},
		{
			// The sqlite fallback (cmd/agentd/main.go: the core MCP server is
			// wired only when agentDB != nil). No core servers configured must
			// be an explicit no-op — the same create as before, not a 500 and
			// not an advertised endpoint that answers 404.
			name: "no core server configured is a no-op, not a failure",
			core: nil,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured agentkit.CreateSessionRequest
			done := make(chan struct{}, 1)
			h := newHandlers(t, Config{
				Runner: stubRunner{createFn: func(_ context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
					captured = req
					done <- struct{}{}
					return &agentkit.SessionHandle{SessionID: req.SessionID, State: "running"}, nil
				}},
				Store:         stubStore{},
				Identity:      func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil },
				ImageResolver: func(name string) (string, error) { return "RESOLVED-" + name, nil },
				CoreMCP:       tc.core,
			})

			body := `{"sessionId":"s1","job":"j","persona":"marketing","installation":"core-v1"}`
			rec := httptest.NewRecorder()
			h.CreateSession(rec, httptest.NewRequest("POST", "/agent/session", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			awaitCreate(t, done)

			// The WHOLE request, not just the MCP field: this ticket's real risk
			// is a regression in what else the create carries. The rejected
			// alternative was routing this path through ComposeJob, which would
			// have stamped Worker and a composed SystemPrompt onto a console
			// chat — the former makes every chat emit worker.finished with its
			// transcript onto the event spine, the latter freezes a prompt that
			// is meant to re-resolve per turn. Both show up here as a diff.
			want := agentkit.CreateSessionRequest{
				SessionID:  "s1",
				Persona:    "marketing",
				Customer:   "acme",
				Job:        "j",
				UserEmail:  "a@b.c",
				Image:      "RESOLVED-core-v1",
				MCPServers: tc.want,
			}
			if !reflect.DeepEqual(captured, want) {
				t.Fatalf("create request =\n %#v\nwant\n %#v", captured, want)
			}
		})
	}
}

// TestCreateSessionDoesNotHandOutTheHostsCoreMCPMap guards a boring but nasty
// aliasing bug: Config.CoreMCP is built once at boot and shared by every create,
// so handing the map itself to the Runner would let one session's config
// mutation reach every later session.
func TestCreateSessionDoesNotHandOutTheHostsCoreMCPMap(t *testing.T) {
	core := coreServers()
	var captured agentkit.CreateSessionRequest
	done := make(chan struct{}, 1)
	h := newHandlers(t, Config{
		Runner: stubRunner{createFn: func(_ context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
			captured = req
			done <- struct{}{}
			return &agentkit.SessionHandle{SessionID: req.SessionID, State: "running"}, nil
		}},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil },
		CoreMCP:  core,
	})
	rec := httptest.NewRecorder()
	h.CreateSession(rec, httptest.NewRequest("POST", "/agent/session", strings.NewReader(`{"sessionId":"s1"}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	awaitCreate(t, done)

	captured.MCPServers["injected"] = agentdb.MCPServerConfig{Command: "whoami"}
	if _, ok := core["injected"]; ok {
		t.Fatal("the create request aliases Config.CoreMCP: a mutation reached the host's shared map")
	}
}
