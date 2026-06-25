package modelproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockProvider implements ModelProvider for testing.
type mockProvider struct {
	endpoint     string
	apiKey       string
	rewriteModel func(string) string
}

func (m *mockProvider) Endpoint() string { return m.endpoint }
func (m *mockProvider) APIKey() string   { return m.apiKey }
func (m *mockProvider) RewriteModel(name string) string {
	if m.rewriteModel != nil {
		return m.rewriteModel(name)
	}
	return name
}

func TestEventLoggingPassthrough(t *testing.T) {
	// The handler should return {"ok":true} for /api/event_logging/ paths
	// without hitting upstream at all.
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	provider := &mockProvider{endpoint: upstream.URL, apiKey: "test-key"}
	handler := Handler(provider)

	req := httptest.NewRequest(http.MethodPost, "/api/event_logging/some_event", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if body != `{"ok":true}` {
		t.Fatalf("expected {\"ok\":true}, got %q", body)
	}
	if upstreamCalled {
		t.Fatal("upstream should not have been called for event_logging")
	}
}

func TestMissingConfig(t *testing.T) {
	provider := &mockProvider{endpoint: "", apiKey: ""}
	handler := Handler(provider)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "model endpoint not configured") {
		t.Fatalf("expected 'model endpoint not configured' error, got %q", rec.Body.String())
	}
}

func TestModelRewriting(t *testing.T) {
	var receivedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	provider := &mockProvider{
		endpoint: upstream.URL,
		apiKey:   "test-key",
		rewriteModel: func(name string) string {
			if name == "claude-sonnet" {
				return "claude-sonnet-rewritten"
			}
			return name
		},
	}
	handler := Handler(provider)

	body := `{"model":"claude-sonnet","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if receivedBody["model"] != "claude-sonnet-rewritten" {
		t.Fatalf("expected model to be rewritten, got %v", receivedBody["model"])
	}
}

func TestHeaderForwarding(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	provider := &mockProvider{endpoint: upstream.URL, apiKey: "my-api-key"}
	handler := Handler(provider)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Custom-Header", "custom-value")
	req.Header.Set("Authorization", "Bearer should-be-stripped")
	req.Header.Set("anthropic-version", "2024-01-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if receivedHeaders.Get("x-api-key") != "my-api-key" {
		t.Fatalf("expected x-api-key=my-api-key, got %q", receivedHeaders.Get("x-api-key"))
	}
	if receivedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Fatalf("expected X-Custom-Header forwarded, got %q", receivedHeaders.Get("X-Custom-Header"))
	}
	if receivedHeaders.Get("Authorization") != "" {
		t.Fatal("Authorization header should have been stripped")
	}
	if receivedHeaders.Get("anthropic-version") != "2024-01-01" {
		t.Fatalf("expected anthropic-version forwarded, got %q", receivedHeaders.Get("anthropic-version"))
	}
}

func TestSSEPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
	}))
	defer upstream.Close()

	provider := &mockProvider{endpoint: upstream.URL, apiKey: "test-key"}
	handler := Handler(provider)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("expected Cache-Control no-cache, got %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected X-Accel-Buffering no, got %q", rec.Header().Get("X-Accel-Buffering"))
	}
}

func TestUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	defer upstream.Close()

	provider := &mockProvider{endpoint: upstream.URL, apiKey: "test-key"}
	handler := Handler(provider)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 500 {
		t.Fatalf("expected 500 forwarded from upstream, got %d", rec.Code)
	}
}

func TestHandler_DefaultPrefix_WhenNoPathRewriter(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	// plainProvider does NOT implement PathRewriter → default "/anthropic" prefix.
	h := Handler(plainProvider{endpoint: upstream.URL, key: "k"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m"}`)))
	if gotPath != "/anthropic/v1/messages" {
		t.Fatalf("default path = %q, want /anthropic/v1/messages", gotPath)
	}
}

func TestHandler_PathRewriter_OverridesPrefix(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	h := Handler(rewriteProvider{endpoint: upstream.URL, key: "k"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m"}`)))
	if gotPath != "/v1/messages" {
		t.Fatalf("rewritten path = %q, want /v1/messages", gotPath)
	}
}

func TestMockHandler_ServesCannedSSE(t *testing.T) {
	rr := httptest.NewRecorder()
	MockHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "message_stop") || !strings.Contains(body, "content_block_delta") {
		t.Fatalf("mock body missing SSE events: %q", body)
	}
}

type plainProvider struct{ endpoint, key string }

func (p plainProvider) Endpoint() string             { return p.endpoint }
func (p plainProvider) APIKey() string               { return p.key }
func (p plainProvider) RewriteModel(s string) string { return s }

type rewriteProvider struct{ endpoint, key string }

func (p rewriteProvider) Endpoint() string             { return p.endpoint }
func (p rewriteProvider) APIKey() string               { return p.key }
func (p rewriteProvider) RewriteModel(s string) string { return s }
func (p rewriteProvider) TargetPath(in string) string  { return in }
