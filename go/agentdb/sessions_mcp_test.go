package agentdb

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// gmail is the canonical stdio server from the spec: a binary in the image plus
// a whole-value ${VAR} reference for its credential (§4.1/§4.4).
func gmailStdio() MCPServerConfig {
	return MCPServerConfig{
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-gmail"},
		Env:     map[string]string{"GMAIL_API_KEY": "${GMAIL_API_KEY}"},
	}
}

func notionHTTP() MCPServerConfig {
	return MCPServerConfig{
		URL:     "http://notion-mcp:8080/sse",
		Headers: map[string]string{"Authorization": "${NOTION_AUTH}"},
	}
}

// TestSessionMCPServersRoundTrip proves the jsonb column carries both transports
// through a write/read cycle unchanged, that writes are wholesale, and that a
// session which never had MCP config reads back as an empty (non-nil) map.
func TestSessionMCPServersRoundTrip(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		servers MCPServers
	}{
		{"stdio", MCPServers{"gmail": gmailStdio()}},
		{"http", MCPServers{"notion": notionHTTP()}},
		{"both transports side by side", MCPServers{"gmail": gmailStdio(), "notion": notionHTTP()}},
		{"literal (non-reference) values are preserved", MCPServers{
			"local": {Command: "/usr/local/bin/mcp-local", Env: map[string]string{"MODE": "debug"}},
		}},
		{"no env or headers at all", MCPServers{"bare": {Command: "mcp-bare"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSessionTestStore(t)
			sess := mustCreateSession(t, s, baseSession("s-"+tc.name))

			// Never-written sessions read back empty, not nil.
			got, err := s.GetSessionMCPServers(ctx, sess.ID)
			if err != nil {
				t.Fatalf("get before write: %v", err)
			}
			if got == nil || len(got) != 0 {
				t.Fatalf("unset config must be an empty non-nil map, got %#v", got)
			}

			if err := s.SetSessionMCPServers(ctx, sess.ID, tc.servers); err != nil {
				t.Fatalf("set: %v", err)
			}
			got, err = s.GetSessionMCPServers(ctx, sess.ID)
			if err != nil {
				t.Fatalf("get after write: %v", err)
			}
			if !reflect.DeepEqual(got, tc.servers) {
				t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", got, tc.servers)
			}

			// The whole row must survive the write (get-patch-save, not replace).
			row, err := s.GetSession(ctx, sess.ID)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if row.Customer != "acme" || row.WorkflowID != "chat" || row.UserEmail != "u@acme.com" {
				t.Fatalf("MCP write clobbered the row: %+v", row)
			}
		})
	}
}

// TestSessionMCPServersWholesaleWrite pins the "no patch semantics" decision
// (§5): a second write replaces the previous set entirely, and an empty map
// clears it.
func TestSessionMCPServersWholesaleWrite(t *testing.T) {
	ctx := context.Background()
	s := newSessionTestStore(t)
	sess := mustCreateSession(t, s, baseSession("s-wholesale"))

	if err := s.SetSessionMCPServers(ctx, sess.ID, MCPServers{"gmail": gmailStdio(), "notion": notionHTTP()}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := s.SetSessionMCPServers(ctx, sess.ID, MCPServers{"notion": notionHTTP()}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := s.GetSessionMCPServers(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("second write must replace, not merge: %#v", got)
	}
	if _, ok := got["gmail"]; ok {
		t.Fatalf("gmail survived a wholesale write: %#v", got)
	}

	for _, empty := range []MCPServers{{}, nil} {
		if err := s.SetSessionMCPServers(ctx, sess.ID, empty); err != nil {
			t.Fatalf("clearing write (%#v): %v", empty, err)
		}
		got, err = s.GetSessionMCPServers(ctx, sess.ID)
		if err != nil {
			t.Fatalf("get after clear: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("empty write must clear the config, got %#v", got)
		}
	}
}

// TestSessionMCPServersValidation pins §4.1: exactly one transport, and only
// whole-value ${VAR} references. Nothing invalid is ever persisted.
func TestSessionMCPServersValidation(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		servers MCPServers
		wantErr string // substring; "" means the config must be accepted
	}{
		{"valid stdio", MCPServers{"gmail": gmailStdio()}, ""},
		{"valid http", MCPServers{"notion": notionHTTP()}, ""},
		{"name with dash and underscore", MCPServers{"my-server_2": {Command: "x"}}, ""},
		{"no transport", MCPServers{"nope": {}}, "exactly one transport required"},
		{"both transports", MCPServers{"nope": {Command: "x", URL: "http://y"}}, "mutually exclusive"},
		{"args without command", MCPServers{"nope": {URL: "http://y", Args: []string{"-y"}}}, "args require the stdio transport"},
		{"env without command", MCPServers{"nope": {URL: "http://y", Env: map[string]string{"A": "b"}}}, "env requires the stdio transport"},
		{"headers without url", MCPServers{"nope": {Command: "x", Headers: map[string]string{"A": "b"}}}, "headers require the http transport"},
		{"partial interpolation in header", MCPServers{"notion": {
			URL: "http://y", Headers: map[string]string{"Authorization": "Bearer ${NOTION_AUTH}"},
		}}, "not a whole-value"},
		{"partial interpolation in env", MCPServers{"gmail": {
			Command: "x", Env: map[string]string{"K": "prefix-${K}"},
		}}, "not a whole-value"},
		{"nested reference", MCPServers{"gmail": {
			Command: "x", Env: map[string]string{"K": "${${K}}"},
		}}, "not a whole-value"},
		{"empty env key", MCPServers{"gmail": {Command: "x", Env: map[string]string{"": "v"}}}, "empty key"},
		{"empty server name", MCPServers{"": {Command: "x"}}, "mcp server name"},
		{"server name with spaces", MCPServers{"my server": {Command: "x"}}, "mcp server name"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSessionTestStore(t)
			sess := mustCreateSession(t, s, baseSession("s-val"))

			err := s.SetSessionMCPServers(ctx, sess.ID, tc.servers)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected accepted, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q must mention %q", err, tc.wantErr)
			}
			// Nothing may have been persisted by a rejected write.
			got, getErr := s.GetSessionMCPServers(ctx, sess.ID)
			if getErr != nil {
				t.Fatalf("get: %v", getErr)
			}
			if len(got) != 0 {
				t.Fatalf("rejected config was persisted: %#v", got)
			}
		})
	}
}

// TestSessionMCPServersUnknownSession proves a missing row is a loud error on
// both accessors rather than a silent no-op.
func TestSessionMCPServersUnknownSession(t *testing.T) {
	ctx := context.Background()
	s := newSessionTestStore(t)

	if _, err := s.GetSessionMCPServers(ctx, "does-not-exist"); err == nil {
		t.Fatalf("expected get error for unknown session")
	}
	if err := s.SetSessionMCPServers(ctx, "does-not-exist", MCPServers{"gmail": gmailStdio()}); err == nil {
		t.Fatalf("expected set error for unknown session")
	}
}

// TestSessionMCPServersJSONShape pins the stored/wire shape: snake_case-free
// lower-case keys, omitted empties, and the ${VAR} reference stored verbatim —
// safe to persist and display whole because it names a variable, never a secret
// (§4.4).
func TestSessionMCPServersJSONShape(t *testing.T) {
	v, err := MCPServers{"gmail": gmailStdio()}.Value()
	if err != nil {
		t.Fatalf("value: %v", err)
	}
	b, ok := v.([]byte)
	if !ok {
		t.Fatalf("expected []byte JSON, got %T", v)
	}
	want := `{"gmail":{"command":"npx","args":["-y","@modelcontextprotocol/server-gmail"],"env":{"GMAIL_API_KEY":"${GMAIL_API_KEY}"}}}`
	if string(b) != want {
		t.Fatalf("json shape:\n got %s\nwant %s", b, want)
	}

	// nil marshals to the empty object, matching the column default.
	v, err = MCPServers(nil).Value()
	if err != nil || v != "{}" {
		t.Fatalf("nil Value: got %#v err=%v", v, err)
	}

	// Scan accepts both driver shapes and NULL.
	for _, in := range []any{[]byte(want), want, nil} {
		var back MCPServers
		if err := back.Scan(in); err != nil {
			t.Fatalf("scan %T: %v", in, err)
		}
		if back == nil {
			t.Fatalf("scan %T produced a nil map", in)
		}
		if in != nil && !reflect.DeepEqual(back, MCPServers{"gmail": gmailStdio()}) {
			t.Fatalf("scan %T: %#v", in, back)
		}
	}
	var bad MCPServers
	if err := bad.Scan(42); err == nil {
		t.Fatalf("scan of an unsupported type must error")
	}
	if err := bad.Scan([]byte("not json")); err == nil {
		t.Fatalf("scan of malformed json must error")
	}

	// The Session row serialises the column under mcp_servers.
	row := Session{ID: "s1", MCPServers: MCPServers{"gmail": gmailStdio()}}
	rowJSON, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if !strings.Contains(string(rowJSON), `"mcp_servers":{"gmail"`) {
		t.Fatalf("session json missing mcp_servers: %s", rowJSON)
	}
}
