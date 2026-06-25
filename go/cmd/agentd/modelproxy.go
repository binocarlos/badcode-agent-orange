package main

import (
	"log"
	"net/http"
	"os"

	"github.com/binocarlos/badcode-agent-orange/modelproxy"
)

// dummyPassthroughKey satisfies Claude Code's startup check; it is NEVER sent
// upstream — newModelProxyHandler injects the real key (from agentd's own env).
const dummyPassthroughKey = "sk-ant-api03-proxy-passthrough-key-00000000000000000000000000000000000000000000000000000000AA"

// modelProvider configures the real upstream for the agentd /agent-proxy route.
// It implements modelproxy.PathRewriter so the upstream path is the direct
// Anthropic path (/v1/messages), not the Azure-style /anthropic/v1/messages.
type modelProvider struct {
	endpoint string
	apiKey   string
}

func (p modelProvider) Endpoint() string                { return p.endpoint }
func (p modelProvider) APIKey() string                  { return p.apiKey }
func (p modelProvider) RewriteModel(name string) string { return name }
func (p modelProvider) TargetPath(inboundPath string) string { return inboundPath }

// newModelProxyHandler chooses the model path at startup: real Anthropic when
// ANTHROPIC_API_KEY is set in agentd's env, canned mock SSE otherwise.
func newModelProxyHandler() http.Handler {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		log.Printf("[agentd] ANTHROPIC_API_KEY unset → MOCK model proxy (set it for a real agent)")
		return modelproxy.MockHandler()
	}
	endpoint := envOr("ANTHROPIC_UPSTREAM_URL", "https://api.anthropic.com")
	log.Printf("[agentd] real model proxy → %s", endpoint)
	return modelproxy.Handler(modelProvider{endpoint: endpoint, apiKey: key})
}

// sandboxSessionEnv is injected into every session container. It points the
// in-sandbox model SDK at agentd's own /agent-proxy route (reachable from inside
// DinD at selfURL) and supplies a dummy key so the CLI boots.
func sandboxSessionEnv(selfURL string) map[string]string {
	return map[string]string{
		"ANTHROPIC_BASE_URL": selfURL + "/agent-proxy",
		"HOST_API_URL":       selfURL,
		"ANTHROPIC_API_KEY":  dummyPassthroughKey,
	}
}
