package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicModelRunHappyPath(t *testing.T) {
	var gotKey, gotVersion, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"clever plan"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	m := &AnthropicModel{APIKey: "sk-test", ModelID: "claude-opus-4-8", BaseURL: srv.URL}
	out, err := m.Run(context.Background(), "Goal: grow the brand")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "clever plan" {
		t.Fatalf("output = %q, want clever plan", out)
	}
	if gotKey != "sk-test" || gotVersion != "2023-06-01" || gotModel != "claude-opus-4-8" {
		t.Fatalf("headers/body wrong: key=%q version=%q model=%q", gotKey, gotVersion, gotModel)
	}
}

func TestAnthropicModelRunErrors(t *testing.T) {
	// Non-200.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	m := &AnthropicModel{APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: bad.URL}
	if _, err := m.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got %v", err)
	}

	// Refusal.
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"refusal","usage":{"input_tokens":1,"output_tokens":0}}`))
	}))
	defer refuse.Close()
	m2 := &AnthropicModel{APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: refuse.URL}
	if _, err := m2.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "refus") {
		t.Fatalf("expected refusal error, got %v", err)
	}
}

func TestAnthropicModelMeteredDispatch(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50}}`))
	}))
	defer srv.Close()

	ctx := context.Background()

	// A live call records actual spend: (100*5 + 50*25)/1e6 = 0.00175 USD.
	meter := NewMemSpendMeter(1.0)
	m := &AnthropicModel{
		APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: srv.URL,
		Meter: meter, Pricing: Pricing{InputPerMTok: 5, OutputPerMTok: 25},
	}
	if _, err := m.Run(ctx, "x"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if spent, _ := meter.Spent(ctx); spent < 0.00174 || spent > 0.00176 {
		t.Fatalf("spent = %v, want ~0.00175", spent)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	// A pre-exhausted meter halts BEFORE dispatch — no HTTP call.
	exhausted := &AnthropicModel{
		APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: srv.URL,
		Meter: NewMemSpendMeter(0.0), Pricing: Pricing{InputPerMTok: 5, OutputPerMTok: 25},
	}
	if _, err := exhausted.Run(ctx, "x"); err == nil {
		t.Fatalf("expected spend-ceiling halt, got nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want still 1 (dispatch halted before HTTP)", calls)
	}
}
