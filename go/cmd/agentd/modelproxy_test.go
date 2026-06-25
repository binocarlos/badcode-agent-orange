package main

import (
	"os"
	"strings"
	"testing"
)

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
