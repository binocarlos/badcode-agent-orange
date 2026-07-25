package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestMCPEnvAllowlist locks docs/product/01-session-config.md §4.4 step 2: only
// operator-designated variables travel from agentd into a session container.
// The load-bearing case is the last one — agentd's own secrets must never reach
// a container, whether or not the operator tries.
func TestMCPEnvAllowlist(t *testing.T) {
	// A realistic agentd environment: two MCP credentials alongside the secrets
	// a session must never see.
	agentdEnv := map[string]string{
		"AGENTKIT_JWT_SECRET":     "super-secret-signing-key",
		"ANTHROPIC_API_KEY":       "sk-ant-real-billing-key",
		"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-subscription",
		"DATABASE_URL":            "postgres://user:pw@postgres:5432/agentorange",
		"GMAIL_API_KEY":           "gmail-token-value",
		"NOTION_AUTH":             "notion-token-value",
	}

	tests := []struct {
		name        string
		allowlist   string
		env         map[string]string
		wantForward map[string]string
		wantMissing []string
		wantErr     string // substring; empty = expect success
	}{
		{
			// Default posture: forward nothing. The stack behaves exactly as it
			// did before A5 until an operator opts in.
			name:        "unset allowlist forwards nothing",
			env:         agentdEnv,
			wantForward: map[string]string{},
		},
		{
			name:        "empty allowlist forwards nothing",
			allowlist:   "   ",
			env:         agentdEnv,
			wantForward: map[string]string{},
		},
		{
			name:        "allowlisted credentials are forwarded",
			allowlist:   "GMAIL_API_KEY,NOTION_AUTH",
			env:         agentdEnv,
			wantForward: map[string]string{"GMAIL_API_KEY": "gmail-token-value", "NOTION_AUTH": "notion-token-value"},
		},
		{
			name:        "whitespace, blanks and duplicates are tolerated",
			allowlist:   " GMAIL_API_KEY , , NOTION_AUTH ,GMAIL_API_KEY, ",
			env:         agentdEnv,
			wantForward: map[string]string{"GMAIL_API_KEY": "gmail-token-value", "NOTION_AUTH": "notion-token-value"},
		},
		{
			// An unset name is reported, never forwarded as "": an empty
			// credential is the silent failure §4.1 forbids. The sandbox fails
			// that MCP server loudly at spawn instead.
			name:        "unset allowlisted name is reported, not forwarded empty",
			allowlist:   "GMAIL_API_KEY,SLACK_TOKEN",
			env:         agentdEnv,
			wantForward: map[string]string{"GMAIL_API_KEY": "gmail-token-value"},
			wantMissing: []string{"SLACK_TOKEN"},
		},
		{
			// §4.4 names these two explicitly as things sessions must not see.
			name:      "the JWT secret cannot be allowlisted",
			allowlist: "GMAIL_API_KEY,AGENTKIT_JWT_SECRET",
			env:       agentdEnv,
			wantErr:   "AGENTKIT_JWT_SECRET",
		},
		{
			name:      "the model API key cannot be allowlisted",
			allowlist: "ANTHROPIC_API_KEY",
			env:       agentdEnv,
			wantErr:   "ANTHROPIC_API_KEY",
		},
		{
			name:      "per-session plumbing cannot be allowlisted",
			allowlist: "SESSION_TOKEN",
			env:       agentdEnv,
			wantErr:   "SESSION_TOKEN",
		},
		{
			name:      "the store connection string cannot be allowlisted",
			allowlist: "DATABASE_URL",
			env:       agentdEnv,
			wantErr:   "DATABASE_URL",
		},
		{
			// A name that could not appear in an MCP ${VAR} reference is a
			// config error, caught at boot rather than half-working.
			name:      "malformed variable name is a boot error",
			allowlist: "GMAIL-API-KEY",
			env:       agentdEnv,
			wantErr:   "not a valid environment variable name",
		},
		{
			name:      "a leading digit is a boot error",
			allowlist: "1PASSWORD",
			env:       agentdEnv,
			wantErr:   "not a valid environment variable name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range tt.env {
				env[k] = v
			}
			if tt.allowlist != "" {
				env[mcpEnvVar] = tt.allowlist
			}

			forward, missing, err := resolveMCPEnv(envMap(env))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got none (forward=%v)", tt.wantErr, forward)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				if forward != nil {
					t.Errorf("a rejected allowlist must forward nothing, got %v", forward)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMCPEnv: %v", err)
			}
			if !reflect.DeepEqual(forward, tt.wantForward) {
				t.Errorf("forward = %v, want %v", forward, tt.wantForward)
			}
			if len(missing) != len(tt.wantMissing) || (len(missing) > 0 && !reflect.DeepEqual(missing, tt.wantMissing)) {
				t.Errorf("missing = %v, want %v", missing, tt.wantMissing)
			}
		})
	}
}

// TestMCPEnvAllowlist_ContainerEnvNeverLeaksAgentdSecrets is the whole point of
// A5: build the env that actually reaches a session container (the same call
// chain main.go uses) and assert none of agentd's secret values appear in it.
func TestMCPEnvAllowlist_ContainerEnvNeverLeaksAgentdSecrets(t *testing.T) {
	const (
		jwtSecret   = "super-secret-signing-key"
		realAPIKey  = "sk-ant-real-billing-key"
		oauthToken  = "sk-ant-oat01-subscription"
		databaseURL = "postgres://user:pw@postgres:5432/agentorange"
	)
	env := envMap(map[string]string{
		mcpEnvVar:                 "GMAIL_API_KEY,NOTION_AUTH",
		"AGENTKIT_JWT_SECRET":     jwtSecret,
		"ANTHROPIC_API_KEY":       realAPIKey,
		"CLAUDE_CODE_OAUTH_TOKEN": oauthToken,
		"DATABASE_URL":            databaseURL,
		"AGENTKIT_PROJECT_MAP":    `{"kai@example.com":["*"]}`,
		"GMAIL_API_KEY":           "gmail-token-value",
		"NOTION_AUTH":             "notion-token-value",
	})

	forward, missing, err := resolveMCPEnv(env)
	if err != nil {
		t.Fatalf("resolveMCPEnv: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("unexpected missing vars: %v", missing)
	}

	// Both session-env shapes agentd can build (proxy mode and subscription mode).
	for _, mode := range []struct {
		name       string
		sessionEnv map[string]string
	}{
		{"proxy", sandboxSessionEnv("http://172.17.0.1:8099")},
		{"subscription", subscriptionSessionEnv("http://172.17.0.1:8099", oauthToken)},
	} {
		t.Run(mode.name, func(t *testing.T) {
			containerEnv := applyMCPEnv(mode.sessionEnv, forward)

			// The allowlisted credentials arrive…
			for k, want := range map[string]string{
				"GMAIL_API_KEY": "gmail-token-value",
				"NOTION_AUTH":   "notion-token-value",
			} {
				if containerEnv[k] != want {
					t.Errorf("%s = %q, want %q", k, containerEnv[k], want)
				}
			}

			// …and nothing that was not allowlisted does. Value-based, not
			// name-based: a leak under any name is still a leak. The
			// subscription token is exempt in subscription mode — the in-image
			// CLI authenticates with it by design (it is not MCP plumbing).
			forbidden := map[string]string{
				"the JWT secret":     jwtSecret,
				"the real model key": realAPIKey,
				"the database URL":   databaseURL,
				"the project map":    `{"kai@example.com":["*"]}`,
			}
			if mode.name != "subscription" {
				forbidden["the subscription token"] = oauthToken
			}
			for what, secret := range forbidden {
				for k, v := range containerEnv {
					if v == secret {
						t.Errorf("%s leaked into the container env as %s=%q", what, k, v)
					}
				}
			}

			// Proxy mode specifically: ANTHROPIC_API_KEY must stay the dummy
			// passthrough value (the Runner later replaces it with the session
			// JWT), never agentd's billing key.
			if mode.name == "proxy" && containerEnv["ANTHROPIC_API_KEY"] != dummyPassthroughKey {
				t.Errorf("ANTHROPIC_API_KEY = %q, want the dummy passthrough key", containerEnv["ANTHROPIC_API_KEY"])
			}
		})
	}
}

// TestMCPEnvAllowlist_ApplyIsAdditive: forwarding must extend the session env,
// never replace the model-provider wiring already in it.
func TestMCPEnvAllowlist_ApplyIsAdditive(t *testing.T) {
	base := sandboxSessionEnv("http://172.17.0.1:8099")
	want := len(base) + 1
	got := applyMCPEnv(base, map[string]string{"GMAIL_API_KEY": "v"})
	if len(got) != want {
		t.Fatalf("session env has %d keys, want %d: %v", len(got), want, sortedEnvKeys(got))
	}
	if got["HOST_API_URL"] == "" || got["ANTHROPIC_BASE_URL"] == "" {
		t.Errorf("model-provider wiring lost: %v", sortedEnvKeys(got))
	}
	// Nil-safe (a host that supplies no session env at all).
	if out := applyMCPEnv(nil, map[string]string{"GMAIL_API_KEY": "v"}); out["GMAIL_API_KEY"] != "v" {
		t.Errorf("applyMCPEnv(nil, …) = %v", out)
	}
}

func sortedEnvKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
