package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

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

func (p modelProvider) Endpoint() string                     { return p.endpoint }
func (p modelProvider) APIKey() string                       { return p.apiKey }
func (p modelProvider) RewriteModel(name string) string      { return name }
func (p modelProvider) TargetPath(inboundPath string) string { return inboundPath }

// newModelProxyHandler chooses the model path at startup. With
// CLAUDE_CODE_OAUTH_TOKEN set (subscription mode — the token outranks the API
// key), sessions bypass the proxy entirely: main points them straight at
// api.anthropic.com, so the proxy serves the mock rather than sitting mounted
// with a real key nothing should reach. Otherwise: real Anthropic when
// ANTHROPIC_API_KEY is set in agentd's env, mock SSE when neither is.
//
// The mock is scriptable (see modelproxy/script.go): with
// AGENTKIT_MOCK_MODEL_SCRIPT (inline JSON) or AGENTKIT_MOCK_MODEL_SCRIPT_FILE
// (a path) set, the mock serves scripted turns — including `tool_use` blocks,
// which the canned stream can never produce. That is what makes an offline test
// able to drive a worker into calling an MCP tool. Neither set → the canned
// stream, unchanged.
func newModelProxyHandler() http.Handler {
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		// Not mock mode — sessions are on the real model, direct. This only says
		// the unused /agent-proxy/ route is inert (no billing key mounted on it).
		log.Printf("[agentd] subscription mode → /agent-proxy/ is unused by sessions and mounts no API key")
		return modelproxy.MockHandler()
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		table, err := loadMockModelScript()
		if err != nil {
			// A misconfigured script must not boot: a silently-ignored one turns
			// into a mysterious test failure a long way from the typo.
			log.Fatalf("[agentd] %v", err)
		}
		if table == nil {
			log.Printf("[agentd] ANTHROPIC_API_KEY unset → MOCK model proxy (set it for a real agent)")
			return modelproxy.MockHandler()
		}
		log.Printf("[agentd] ANTHROPIC_API_KEY unset → SCRIPTED mock model proxy (%d rule(s))", len(table.Rules))
		return modelproxy.ScriptedMockHandler(table)
	}
	endpoint := envOr("ANTHROPIC_UPSTREAM_URL", "https://api.anthropic.com")
	log.Printf("[agentd] real model proxy → %s", endpoint)
	return modelproxy.Handler(modelProvider{endpoint: endpoint, apiKey: key})
}

// The credential modes reported to the browser by GET /auth/config (RD18).
// agentd has always logged which model path it booted with; nothing told the
// UI, so a stack running the offline mock — which is what a user following the
// README verbatim gets, both credential lines shipping blank — writes plausible
// canned output into Desk, Events and Jobs with no marker anywhere.
const (
	credentialModeMock         = "mock"
	credentialModeAPIKey       = "api-key"
	credentialModeSubscription = "subscription"
)

// credentialMode names the model credential agentd booted with. It is the ONE
// place that precedence is expressed for reporting, and it deliberately mirrors
// the two places that act on it: newModelProxyHandler (token set → mock proxy,
// bypassed; else key → real proxy, else mock) and main's subscriptionMode
// (`oauthToken != ""` — the token outranks the key, so blanking the token is
// what flips a deployment to unattended API billing). Mirrored rather than
// shared because those two decide different things; a test pins the three
// answers so the mirror cannot drift silently.
// The comparisons are raw (not trimmed) on purpose: both of those callers test
// the raw value, and a badge that disagreed with the proxy would be worse than
// no badge at all.
func credentialMode(apiKey, oauthToken string) string {
	switch {
	case oauthToken != "":
		return credentialModeSubscription
	case apiKey != "":
		return credentialModeAPIKey
	default:
		return credentialModeMock
	}
}

// mockScriptEnv / mockScriptFileEnv name the two ways a stack supplies a mock
// model script. Inline wins when both are set (a `.env` override beating a
// baked-in file is the least surprising precedence).
const (
	mockScriptEnv     = "AGENTKIT_MOCK_MODEL_SCRIPT"
	mockScriptFileEnv = "AGENTKIT_MOCK_MODEL_SCRIPT_FILE"
)

// loadMockModelScript reads the configured script table, or (nil, nil) when
// neither environment variable is set.
func loadMockModelScript() (*modelproxy.ScriptTable, error) {
	if inline := strings.TrimSpace(os.Getenv(mockScriptEnv)); inline != "" {
		t, err := modelproxy.ParseScriptTable(inline)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mockScriptEnv, err)
		}
		return t, nil
	}
	path := strings.TrimSpace(os.Getenv(mockScriptFileEnv))
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", mockScriptFileEnv, err)
	}
	t, err := modelproxy.ParseScriptTable(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s (%s): %w", mockScriptFileEnv, path, err)
	}
	if t == nil {
		return nil, fmt.Errorf("%s (%s): file is empty", mockScriptFileEnv, path)
	}
	return t, nil
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

// subscriptionSessionEnv is the session env for subscription mode: the in-image
// `claude` CLI authenticates to api.anthropic.com directly with the Claude Code
// OAuth token (from `claude setup-token`). No ANTHROPIC_BASE_URL (the sandbox
// skips its model proxy plumbing) and no ANTHROPIC_API_KEY (the CLI must fall
// through to the OAuth token; the Runner's JWT override is disabled too, via
// Policy.DisableModelAPIKeyOverride).
func subscriptionSessionEnv(selfURL, oauthToken string) map[string]string {
	return map[string]string{
		"HOST_API_URL":            selfURL,
		"CLAUDE_CODE_OAUTH_TOKEN": oauthToken,
	}
}
