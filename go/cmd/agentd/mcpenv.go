package main

// mcpenv.go — MCP credential propagation (docs/product/01-session-config.md §4.4).
//
// MCP config stored in the database only ever *names* a variable
// (`{"Env": {"GMAIL_API_KEY": "${GMAIL_API_KEY}"}}`); the value lives in the
// operator's environment. This file is step 2 of the §4.4 propagation path — the
// bit that carries the value from agentd's environment into every session
// container, where the sandbox resolves the `${VAR}` reference at MCP-process
// spawn time:
//
//	.env → compose → agentd  ──AGENTKIT_MCP_ENV──▶  session container  ──▶  MCP process
//
// Config:
//
//	AGENTKIT_MCP_ENV=GMAIL_API_KEY,NOTION_AUTH
//
// A comma-separated **allowlist of variable names** — never "forward
// everything". agentd's own environment holds things a session must never see
// (AGENTKIT_JWT_SECRET, ANTHROPIC_API_KEY, DATABASE_URL); an explicit allowlist
// is what keeps them out. Unset (the default) forwards nothing, so the stack
// behaves exactly as before until an operator opts in.
//
// Semantics:
//   - Names are trimmed; empty entries are ignored (a trailing comma is fine).
//   - A name must look like an environment variable ([A-Za-z_][A-Za-z0-9_]*) —
//     anything else is a config error that stops agentd at boot rather than
//     half-working.
//   - Reserved names are rejected with an error: forwarding them would either
//     leak an agentd secret or overwrite session plumbing the Runner owns.
//   - A name that is unset in agentd's environment is reported as "missing" and
//     NOT forwarded: injecting an empty string would produce exactly the silent
//     credential failure §4.1 forbids. agentd logs the names at boot; the
//     sandbox then fails that MCP server loudly at spawn.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// mcpEnvVar is the name of the allowlist variable itself.
const mcpEnvVar = "AGENTKIT_MCP_ENV"

// envNamePattern is the shell/POSIX-ish environment variable grammar, and is
// deliberately the same shape agentdb's `${VAR}` reference validator accepts —
// a name that cannot appear in an MCP config is not worth forwarding.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedSessionEnv are names an operator may never route through the
// allowlist. Two reasons, both fatal if ignored:
//
//   - secrets of the host process that sessions must not see (§4.4 names the
//     JWT secret and ANTHROPIC_API_KEY explicitly);
//   - per-session plumbing the Runner sets itself (SESSION_ID, SESSION_TOKEN,
//     the model-provider wiring) — a forwarded copy would fight it.
//
// The allowlist is an operator-facing knob, so a bad entry is answered with a
// boot-time error naming the variable, not a silent drop.
var reservedSessionEnv = map[string]string{
	"AGENTKIT_JWT_SECRET":         "agentd's API JWT signing secret",
	"AGENTKIT_SESSION_JWT_SECRET": "agentd's session-token signing key — forwarding it would let a session mint tokens for any other session",
	"ANTHROPIC_API_KEY":           "the host model credential (the Runner injects a per-session token under this name)",
	"CLAUDE_CODE_OAUTH_TOKEN":     "the subscription model credential",
	"ANTHROPIC_BASE_URL":          "model-provider wiring owned by agentd",
	"DATABASE_URL":                "the store connection string",
	"SESSION_ID":                  "per-session plumbing set by the Runner",
	"SESSION_TOKEN":               "per-session plumbing set by the Runner",
	"HOST_API_URL":                "the sandbox's callback URL, set by agentd",
	"AGENTKIT_MCP_ENV":            "the allowlist itself",
}

// resolveMCPEnv reads AGENTKIT_MCP_ENV and returns the variables to forward into
// every session container, plus the allowlisted names that are unset in agentd's
// own environment (logged by the caller — see the "missing" rationale above).
//
// Pure: env is the lookup function, so the whole policy is unit-testable without
// a live process (the resolve*(env) convention from backends.go).
func resolveMCPEnv(env func(string) string) (forward map[string]string, missing []string, err error) {
	forward = map[string]string{}
	raw := strings.TrimSpace(env(mcpEnvVar))
	if raw == "" {
		return forward, nil, nil
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !envNamePattern.MatchString(name) {
			return nil, nil, fmt.Errorf("%s: %q is not a valid environment variable name (want %s)", mcpEnvVar, name, envNamePattern)
		}
		if why, reserved := reservedSessionEnv[name]; reserved {
			return nil, nil, fmt.Errorf("%s: %s must not be forwarded to sessions (%s)", mcpEnvVar, name, why)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		if v := env(name); v != "" {
			forward[name] = v
		} else {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return forward, missing, nil
}

// applyMCPEnv merges the forwarded credentials into the session env injected
// into every container (Policy.SessionEnv). It mutates and returns sessionEnv;
// reserved names are already rejected by resolveMCPEnv, so nothing here can
// overwrite the Runner's own keys.
func applyMCPEnv(sessionEnv, forward map[string]string) map[string]string {
	if sessionEnv == nil {
		sessionEnv = map[string]string{}
	}
	for k, v := range forward {
		sessionEnv[k] = v
	}
	return sessionEnv
}

// mcpEnvNames lists the forwarded names (sorted) for the boot log. Only names —
// the values are credentials.
func mcpEnvNames(forward map[string]string) []string {
	names := make([]string, 0, len(forward))
	for k := range forward {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
