package agentkit

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func mcpFixture() map[string]MCPServerConfig {
	return map[string]MCPServerConfig{
		"gmail": {
			Command: "npx",
			Args:    []string{"-y", "server-gmail"},
			Env:     map[string]string{"GMAIL_API_KEY": "${GMAIL_API_KEY}"},
		},
		"notion": {
			URL:     "http://notion-mcp:8080/sse",
			Headers: map[string]string{"Authorization": "${NOTION_AUTH}"},
		},
	}
}

// TestCreateSessionPersistsMCPServers proves session-supplied MCP config lands
// on the (host-owned) session row at create time, which is what lets resume /
// re-provision re-supply it (§4.5) — a snapshot cannot carry it, because it is
// session config rather than filesystem state.
func TestCreateSessionPersistsMCPServers(t *testing.T) {
	ctx := context.Background()
	r, _, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s-mcp", Customer: "acme", Job: "j1"})

	want := mcpFixture()
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID:  "s-mcp",
		Customer:   "acme",
		Job:        "j1",
		MCPServers: want,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	row, err := store.GetSession(ctx, "s-mcp")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !reflect.DeepEqual(map[string]MCPServerConfig(row.MCPServers), want) {
		t.Fatalf("row.MCPServers:\n got %#v\nwant %#v", row.MCPServers, want)
	}
}

// TestCreateSessionMCPServersEmptyIsNoop keeps every pre-existing caller on the
// old path: with no MCP config the runner must not touch the session row (and
// must not require one to exist).
func TestCreateSessionMCPServersEmptyIsNoop(t *testing.T) {
	ctx := context.Background()
	r, _, _, store, _, _ := newTestRunner(t)

	// Deliberately no Seed: the row is absent, and that must not break create.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-no-mcp", Customer: "acme", Job: "j1",
	}); err != nil {
		t.Fatalf("CreateSession without MCP config: %v", err)
	}
	if _, err := store.GetSession(ctx, "s-no-mcp"); err == nil {
		t.Fatalf("runner must not create the session row itself")
	}
}

// TestCreateSessionMCPServersInvalid pins the loud-failure contract: a config
// that would silently misbehave inside the container (partial interpolation,
// two transports) fails the create before anything is provisioned.
func TestCreateSessionMCPServersInvalid(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		servers map[string]MCPServerConfig
		wantErr string
	}{
		{"partial interpolation", map[string]MCPServerConfig{
			"notion": {URL: "http://y", Headers: map[string]string{"Authorization": "Bearer ${NOTION_AUTH}"}},
		}, "not a whole-value"},
		{"two transports", map[string]MCPServerConfig{
			"nope": {Command: "x", URL: "http://y"},
		}, "mutually exclusive"},
		{"no transport", map[string]MCPServerConfig{"nope": {}}, "exactly one transport required"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, env, _, store, _, _ := newTestRunner(t)
			store.Seed(&agentdb.Session{ID: "s-bad", Customer: "acme", Job: "j1"})

			_, err := r.CreateSession(ctx, CreateSessionRequest{
				SessionID: "s-bad", Customer: "acme", Job: "j1", MCPServers: tc.servers,
			})
			if err == nil {
				t.Fatalf("expected create to fail")
			}
			if !strings.Contains(err.Error(), "mcp servers") || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q must mention mcp servers and %q", err, tc.wantErr)
			}
			if len(env.Provisions) != 0 {
				t.Fatalf("nothing may be provisioned when MCP config is rejected: %#v", env.Provisions)
			}
			row, getErr := store.GetSession(ctx, "s-bad")
			if getErr != nil {
				t.Fatalf("get session: %v", getErr)
			}
			if len(row.MCPServers) != 0 {
				t.Fatalf("rejected config was persisted: %#v", row.MCPServers)
			}
		})
	}
}

// TestCreateSessionMCPServersMissingRow proves the drop is never silent: when
// MCP config is supplied but the host skipped its persist-the-row contract, the
// create fails instead of losing the config.
func TestCreateSessionMCPServersMissingRow(t *testing.T) {
	ctx := context.Background()
	r, _, _, _, _, _ := newTestRunner(t)

	_, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-missing", Customer: "acme", Job: "j1", MCPServers: mcpFixture(),
	})
	if err == nil || !strings.Contains(err.Error(), "persist mcp servers") {
		t.Fatalf("expected a persist error naming the missing row, got %v", err)
	}
}
