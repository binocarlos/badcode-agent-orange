package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestCredentialMode pins the three answers the browser is told, in the same
// precedence the two acting call sites apply: an OAuth token wins outright
// (attended subscription billing by default; production blanks the token), an
// API key alone is proxy mode, neither is the mock. RD18: the mock is the
// DEFAULT (both credential lines ship blank in .env.example), so a wrong
// answer here is the difference between "the product works" and "no model was
// ever called".
func TestCredentialMode(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		oauth string
		want  string
	}{
		{"neither is mock", "", "", credentialModeMock},
		{"api key alone", "sk-ant-api03-x", "", credentialModeAPIKey},
		{"oauth token alone", "", "sk-ant-oat01-x", credentialModeSubscription},
		{"oauth token wins over api key", "sk-ant-api03-x", "sk-ant-oat01-x", credentialModeSubscription},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := credentialMode(tt.key, tt.oauth); got != tt.want {
				t.Fatalf("credentialMode(%q, %q) = %q, want %q", tt.key, tt.oauth, got, tt.want)
			}
		})
	}
}

// TestAuthConfigReportsCredentialMode is the wire-level half: whatever
// credentialMode decided has to reach the browser, in every login mode.
func TestAuthConfigReportsCredentialMode(t *testing.T) {
	for _, mode := range []string{credentialModeMock, credentialModeAPIKey, credentialModeSubscription} {
		t.Run(mode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			authConfigHandler("", false, mode)(rec, httptest.NewRequest(http.MethodGet, "/auth/config", nil))
			var resp struct {
				CredentialMode string `json:"credential_mode"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.CredentialMode != mode {
				t.Fatalf("credential_mode = %q, want %q", resp.CredentialMode, mode)
			}
		})
	}
}

func TestNewModelProxyHandler_MockWhenNoKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	h := newModelProxyHandler()
	if h == nil {
		t.Fatal("handler is nil")
	}
	// Mock provider answers GET .../health with the mock note.
	if got := os.Getenv("ANTHROPIC_API_KEY"); got != "" {
		t.Fatalf("precondition: key should be empty, got %q", got)
	}
}

func TestModelProvider_TargetPathIsDirectAnthropic(t *testing.T) {
	p := modelProvider{endpoint: "https://api.anthropic.com", apiKey: "k"}
	if got := p.TargetPath("/v1/messages"); got != "/v1/messages" {
		t.Fatalf("TargetPath = %q, want /v1/messages (no /anthropic prefix)", got)
	}
	if p.Endpoint() != "https://api.anthropic.com" || p.APIKey() != "k" {
		t.Fatalf("provider accessors wrong: %+v", p)
	}
}

func TestSandboxSessionEnv_PointsAtAgentProxyAndDummyKey(t *testing.T) {
	env := sandboxSessionEnv("http://172.17.0.1:8099")
	if env["ANTHROPIC_BASE_URL"] != "http://172.17.0.1:8099/agent-proxy" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["HOST_API_URL"] != "http://172.17.0.1:8099" {
		t.Fatalf("HOST_API_URL = %q", env["HOST_API_URL"])
	}
	if !strings.HasPrefix(env["ANTHROPIC_API_KEY"], "sk-ant-") {
		t.Fatalf("expected dummy passthrough key, got %q", env["ANTHROPIC_API_KEY"])
	}
}

// TestSubscriptionSessionEnv locks direct (subscription) mode: sessions get the
// OAuth token and NO proxy plumbing — no base URL, no API key of any kind.
func TestSubscriptionSessionEnv_DirectWithOAuthTokenOnly(t *testing.T) {
	env := subscriptionSessionEnv("http://172.17.0.1:8099", "sk-ant-oat01-test")
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat01-test" {
		t.Fatalf("CLAUDE_CODE_OAUTH_TOKEN = %q", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if env["HOST_API_URL"] != "http://172.17.0.1:8099" {
		t.Fatalf("HOST_API_URL = %q", env["HOST_API_URL"])
	}
	if v, ok := env["ANTHROPIC_BASE_URL"]; ok {
		t.Fatalf("ANTHROPIC_BASE_URL must be absent in subscription mode, got %q", v)
	}
	if v, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("ANTHROPIC_API_KEY must be absent in subscription mode, got %q", v)
	}
}
