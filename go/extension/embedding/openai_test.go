package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// unitVec builds a well-formed response body of the right width.
func openAIBody(dim int) string {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(i%7) / 7
	}
	b, _ := json.Marshal(map[string]any{"data": []map[string]any{{"embedding": v}}})
	return string(b)
}

func newTestOpenAI(t *testing.T, h http.HandlerFunc) (*OpenAI, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewOpenAI(OpenAIConfig{APIKey: "sk-test", BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return p, srv
}

func TestOpenAIRequestShape(t *testing.T) {
	var gotPath, gotAuth, gotType string
	var gotReq openAIRequest
	p, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotType = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_, _ = w.Write([]byte(openAIBody(Dim)))
	})

	if _, err := p.Embed(context.Background(), "refund policy"); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if gotPath != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if gotReq.Input != "refund policy" {
		t.Errorf("input = %q", gotReq.Input)
	}
	if gotReq.Model != DefaultOpenAIModel {
		t.Errorf("model = %q, want the default %q", gotReq.Model, DefaultOpenAIModel)
	}
	// The dimensions parameter is what lets a 3072-wide model land in the
	// vector(1536) column migration 022 built. Sending it is not optional.
	if gotReq.Dimensions != Dim {
		t.Errorf("dimensions = %d, want %d — without it text-embedding-3-large "+
			"returns 3072 and every INSERT fails", gotReq.Dimensions, Dim)
	}
}

func TestOpenAIReturnsAValidVector(t *testing.T) {
	p, _ := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(openAIBody(Dim)))
	})
	v, err := p.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if err := Validate(v); err != nil {
		t.Errorf("provider returned a vector that fails the Provider contract: %v", err)
	}
}

func TestOpenAIRejectsAWrongWidthVector(t *testing.T) {
	// A model whose width is not Dim must fail HERE, with the model named,
	// rather than at agentdb's INSERT far from the cause.
	p, _ := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(openAIBody(512)))
	})
	_, err := p.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("a 512-wide vector must be rejected")
	}
	if !strings.Contains(err.Error(), DefaultOpenAIModel) {
		t.Errorf("error should name the model, got: %v", err)
	}
}

func TestOpenAISurfacesAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{
			name:   "structured error is reported verbatim",
			status: http.StatusUnauthorized,
			body:   `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`,
			want:   "Incorrect API key provided",
		},
		{
			// A gateway returning HTML must not surface as "invalid character '<'",
			// which sends the reader hunting a parser bug instead of a 502.
			name:   "non-JSON body reports the status, not a parse error",
			status: http.StatusBadGateway,
			body:   "<html><body>502 Bad Gateway</body></html>",
			want:   "502",
		},
		{
			name:   "a non-200 with valid JSON but no error object still fails",
			status: http.StatusTooManyRequests,
			body:   `{"data":[]}`,
			want:   "429",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			_, err := p.Embed(context.Background(), "hello")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should contain %q", err, tc.want)
			}
		})
	}
}

func TestOpenAIEmptyTextIsRefused(t *testing.T) {
	p, _ := newTestOpenAI(t, func(http.ResponseWriter, *http.Request) {
		t.Error("must not reach the API for blank input")
	})
	for _, s := range []string{"", "   ", "\n\t"} {
		if _, err := p.Embed(context.Background(), s); err != ErrEmptyText {
			t.Errorf("Embed(%q) = %v, want ErrEmptyText", s, err)
		}
	}
}

func TestOpenAIHonoursContextCancellation(t *testing.T) {
	// memory_create blocks on this call, so a cancelled tool call must not keep
	// an HTTP request alive.
	p, _ := newTestOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(openAIBody(Dim)))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Embed(ctx, "hello"); err == nil {
		t.Fatal("a cancelled context must fail the call")
	}
}

func TestNewOpenAIConfig(t *testing.T) {
	if _, err := NewOpenAI(OpenAIConfig{}); err == nil {
		t.Error("a missing API key must be an error, not a silent keyword-only deployment")
	}
	p, err := NewOpenAI(OpenAIConfig{APIKey: " sk-test "})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if p.Model() != DefaultOpenAIModel {
		t.Errorf("default model = %q, want %q", p.Model(), DefaultOpenAIModel)
	}
	if p.baseURL != DefaultOpenAIBaseURL {
		t.Errorf("default baseURL = %q", p.baseURL)
	}
	if p.apiKey != "sk-test" {
		t.Errorf("key should be trimmed, got %q", p.apiKey)
	}
	// A trailing slash on a custom base URL must not produce "//embeddings".
	p2, _ := NewOpenAI(OpenAIConfig{APIKey: "k", BaseURL: "https://gw.example/v1/"})
	if p2.baseURL != "https://gw.example/v1" {
		t.Errorf("baseURL = %q, want the trailing slash trimmed", p2.baseURL)
	}
}

func TestNewFromEnvOpenAI(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	p, err := NewFromEnv(env(map[string]string{
		"AGENTKIT_EMBEDDING_BACKEND": "openai",
		"OPENAI_API_KEY":             "sk-x",
		"AGENTKIT_EMBEDDING_MODEL":   "text-embedding-3-large",
	}))
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	o, ok := p.(*OpenAI)
	if !ok {
		t.Fatalf("want *OpenAI, got %T", p)
	}
	if o.Model() != "text-embedding-3-large" {
		t.Errorf("model = %q", o.Model())
	}

	// Asking for openai without a key is a BOOT error. Degrading to keyword-only
	// here would give an operator a semantic leg they think they configured.
	if _, err := NewFromEnv(env(map[string]string{"AGENTKIT_EMBEDDING_BACKEND": "openai"})); err == nil {
		t.Error("openai without OPENAI_API_KEY must fail the boot")
	}

	// And the existing contract still holds.
	if p, err := NewFromEnv(env(map[string]string{})); p != nil || err != nil {
		t.Errorf("empty backend = (%v, %v), want (nil, nil)", p, err)
	}
	if _, err := NewFromEnv(env(map[string]string{"AGENTKIT_EMBEDDING_BACKEND": "opnai"})); err == nil {
		t.Error("a typo must be a boot error, not a silent keyword-only deployment")
	}
}
