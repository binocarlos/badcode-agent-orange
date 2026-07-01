package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTierRouterResolves(t *testing.T) {
	ctx := context.Background()
	r := NewTierRouter(map[ModelTier]Model{
		TierFull:  &ScriptedModel{Default: "opus"},
		TierCheap: &ScriptedModel{Default: "haiku"},
	})

	if got, _ := r.For(TierFull).Run(ctx, "x"); got != "opus" {
		t.Fatalf("TierFull -> %q, want opus", got)
	}
	if got, _ := r.For(TierCheap).Run(ctx, "x"); got != "haiku" {
		t.Fatalf("TierCheap -> %q, want haiku", got)
	}

	// Unmapped tier: For returns a Model whose Run errors loudly (never nil).
	m := r.For(TierMid)
	if m == nil {
		t.Fatalf("For(TierMid) returned nil Model")
	}
	if _, err := m.Run(ctx, "x"); err == nil {
		t.Fatalf("expected error for unmapped tier")
	}
}

func TestModelIDsFromEnvOverrides(t *testing.T) {
	t.Setenv("AGENTKIT_MODEL_FULL", "custom-opus")
	ids := ModelIDsFromEnv()
	if ids[TierFull] != "custom-opus" {
		t.Fatalf("TierFull = %q, want custom-opus", ids[TierFull])
	}
	// Unset tiers fall back to documented defaults.
	if ids[TierCheap] != DefaultModelIDs()[TierCheap] {
		t.Fatalf("TierCheap = %q, want default", ids[TierCheap])
	}
}

func TestNewAnthropicRouterWiresModelIDs(t *testing.T) {
	// Fake API echoes the requested model id back as the text, proving the router
	// wired the right ModelID into the AnthropicModel for that tier.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"` + req.Model + `"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	r := NewAnthropicRouter(RouterConfig{
		APIKey:   "k",
		BaseURL:  srv.URL,
		ModelIDs: map[ModelTier]string{TierMid: "claude-sonnet-5"},
	})
	out, err := r.For(TierMid).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "claude-sonnet-5" {
		t.Fatalf("router used model %q, want claude-sonnet-5", out)
	}
}
