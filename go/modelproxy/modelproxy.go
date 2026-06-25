package modelproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ModelProvider is the host-injected configuration for the upstream model API.
type ModelProvider interface {
	Endpoint() string
	APIKey() string
	RewriteModel(name string) string
}

// PathRewriter is an optional ModelProvider extension. When a provider implements
// it, the proxy uses TargetPath(inboundPath) as the upstream request path instead
// of the default Azure-style "/anthropic" + inboundPath. A direct-Anthropic
// provider returns inboundPath unchanged so the upstream sees e.g. /v1/messages.
type PathRewriter interface {
	TargetPath(inboundPath string) string
}

// Handler returns an http.Handler that proxies model requests to the upstream
// provider. Mount under your auth middleware.
func Handler(provider ModelProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/event_logging/") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}

		endpoint := provider.Endpoint()
		apiKey := provider.APIKey()
		if endpoint == "" || apiKey == "" {
			http.Error(w, "model endpoint not configured", http.StatusBadGateway)
			return
		}

		body, _ := io.ReadAll(r.Body)
		req, err := buildProxyRequest(endpoint, apiKey, r.URL.Path, body, r.Header, provider)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "upstream request failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		ct := resp.Header.Get("Content-Type")
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		if strings.Contains(ct, "text/event-stream") {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
		}
		w.WriteHeader(resp.StatusCode)
		bw := bufio.NewWriter(w)
		_, _ = io.Copy(bw, resp.Body)
		_ = bw.Flush()
	})
}

func buildProxyRequest(endpoint, apiKey, inboundPath string, body []byte, headers http.Header, provider ModelProvider) (*http.Request, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("modelproxy: bad endpoint: %w", err)
	}
	base := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	upstreamPath := "/anthropic" + inboundPath
	if pr, ok := provider.(PathRewriter); ok {
		upstreamPath = pr.TargetPath(inboundPath)
	}
	target := base + upstreamPath

	outBody := body
	if len(body) > 0 {
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			if mv, ok := m["model"].(string); ok {
				if rw := provider.RewriteModel(mv); rw != mv {
					m["model"] = rw
					if b, err := json.Marshal(m); err == nil {
						outBody = b
					}
				}
			}
		}
	}

	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(outBody))
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{"Host": true, "Connection": true, "Keep-Alive": true,
		"Transfer-Encoding": true, "Content-Length": true, "Authorization": true}
	for k, vals := range headers {
		if skip[k] {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("x-api-key", apiKey)
	return req, nil
}

// mockSSEStream is the minimal valid Anthropic streaming body satisfying the
// claude-agent-sdk parser for a single successful text turn. Used by MockHandler.
const mockSSEStream = "" +
	"event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_mock001","type":"message","role":"assistant","content":[],"model":"claude-opus-4-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: ping\n" +
	`data: {"type":"ping"}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello from the agentd mock model proxy. Set ANTHROPIC_API_KEY for a real agent."}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":6}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// MockHandler returns an http.Handler that serves a canned, valid Anthropic
// streaming SSE response for any POST — no upstream, no key. agentd mounts this
// at /agent-proxy when no ANTHROPIC_API_KEY is configured so the UI still works
// end-to-end with zero config. It also answers GET /health.
func MockHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/health") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","note":"mock-anthropic-proxy"}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/event_logging/") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher, canFlush := w.(http.Flusher)
		for _, chunk := range splitSSE(mockSSEStream) {
			_, _ = fmt.Fprint(w, chunk)
			if canFlush {
				flusher.Flush()
			}
			time.Sleep(2 * time.Millisecond)
		}
	})
}

// splitSSE splits a monolithic SSE stream into per-event chunks (each ending \n\n).
func splitSSE(stream string) []string {
	var chunks []string
	start := 0
	for i := 0; i < len(stream)-1; i++ {
		if stream[i] == '\n' && stream[i+1] == '\n' {
			chunks = append(chunks, stream[start:i+2])
			start = i + 2
		}
	}
	if start < len(stream) {
		chunks = append(chunks, stream[start:])
	}
	return chunks
}
