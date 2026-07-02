//go:build anthropic_smoke

package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestAnthropicSmoke hits the real Anthropic API. Excluded from the default build
// by the anthropic_smoke tag; skipped without ANTHROPIC_API_KEY. Run manually:
//
//	cd go && ANTHROPIC_API_KEY=sk-ant-... go test -tags anthropic_smoke ./orchestrator/ -run TestAnthropicSmoke -v
func TestAnthropicSmoke(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("set ANTHROPIC_API_KEY to run the live smoke test")
	}
	m := &AnthropicModel{APIKey: key, ModelID: DefaultModelIDs()[TierCheap], MaxTokens: 64}
	out, _, err := m.Run(context.Background(), "Reply with the single word: pong")
	if err != nil {
		t.Fatalf("live run: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "pong") {
		t.Fatalf("unexpected reply: %q", out)
	}
}
