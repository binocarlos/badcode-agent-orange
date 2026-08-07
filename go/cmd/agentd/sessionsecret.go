package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"strings"
)

// Session tokens are a different credential class from API tokens, and this
// file is what makes that structurally true rather than merely intended.
//
// # The defect this closes (doc 22, RD30)
//
// agentd mints a per-session JWT and injects it into the session container as
// SESSION_TOKEN. The harness can read it, so a prompt-injected model can reach
// it. Until this file existed, that token was signed with AGENTKIT_JWT_SECRET —
// the same value the API middleware verifies bearer tokens with — so it
// verified as a project-scoped credential on every route the middleware
// protects: workers, project settings, schedules, event ingest. §6.2.4's
// injection boundary governs the model's *reasoning*; this was its blast
// radius, and nothing could detect the use because the token was genuinely
// valid.
//
// # The fix, and why it needs no new configuration
//
// The session class gets its own signing key, DERIVED from the API secret by
// HMAC-SHA256 under a fixed label. Derivation rather than a required new
// environment variable is deliberate:
//
//   - No deployment has to change anything to get the fix. A new mandatory
//     secret would be a breaking boot requirement, which this change is
//     explicitly not allowed to ship.
//   - The derived key is a one-way function of the API secret, so holding the
//     session key does not yield the API secret, and a session token cannot be
//     verified — let alone minted — as a project credential.
//   - Rolling AGENTKIT_JWT_SECRET rolls both keys together, which is what an
//     operator already expects that variable to do.
//
// A deployment that wants to roll the two independently (or to hold the
// session key in a different secret store) sets AGENTKIT_SESSION_JWT_SECRET;
// nothing else changes. Setting it to the SAME value as AGENTKIT_JWT_SECRET
// re-opens the defect, so agentd says so loudly at boot (sessionSecretNotice).
//
// # Migration path for a live deployment
//
// Upgrade and restart: no configuration change is required and no data
// migration exists. In-flight session tokens minted by the previous binary stop
// verifying at the core MCP server; they have a one-hour TTL (devclaims.New)
// and are re-minted on every provision, restore and rehydrate, so the window is
// bounded by that TTL and affects only the core MCP tools of containers adopted
// across the restart. To roll the session key alone afterwards, set
// AGENTKIT_SESSION_JWT_SECRET and restart — same bounded window.
const (
	// sessionSecretEnv optionally overrides the derived session signing key.
	sessionSecretEnv = "AGENTKIT_SESSION_JWT_SECRET"

	// sessionSecretLabel domain-separates the derivation. Changing it
	// invalidates every outstanding session token, so it is versioned.
	sessionSecretLabel = "agent-orange/session-token/v1"

	// apiSecretEnv is the API-class secret; the session key derives from it.
	apiSecretEnv = "AGENTKIT_JWT_SECRET"

	// sessionSecretFallback is what the session key derives from when no API
	// secret is set at all (dev-open). Preserves the pre-existing default:
	// dev-open must keep working with zero configuration.
	sessionSecretFallback = "dev-secret"
)

// resolveSessionSecret returns the signing key for per-session tokens, and
// whether it came from an explicit variable rather than derivation.
func resolveSessionSecret(getenv func(string) string) (secret []byte, explicit bool) {
	if v := strings.TrimSpace(getenv(sessionSecretEnv)); v != "" {
		return []byte(v), true
	}
	base := getenv(apiSecretEnv)
	if base == "" {
		base = sessionSecretFallback
	}
	mac := hmac.New(sha256.New, []byte(base))
	mac.Write([]byte(sessionSecretLabel))
	return mac.Sum(nil), false
}

// sessionSecretNotice is the boot log line for the session key: one line
// always, naming where the key came from, and a WARNING when an explicit
// override has been set to the API secret itself — which restores exactly the
// collapse RD30 describes.
// It warns rather than fatals: refusing to boot on a configuration that worked
// yesterday is the breaking change this item may not make.
func sessionSecretNotice(apiSecret, sessionSecret []byte, explicit bool) string {
	if !explicit {
		return "[agentd] session tokens signed with a key derived from " + apiSecretEnv +
			" (a container's token cannot authenticate as its project; set " + sessionSecretEnv +
			" to roll it independently)"
	}
	if subtle.ConstantTimeCompare(apiSecret, sessionSecret) == 1 {
		return "[agentd] WARNING: " + sessionSecretEnv + " is set to the SAME value as " + apiSecretEnv +
			" — a session container's token is then a valid project credential for every API route. Set it to a different secret, or unset it to use the derived key"
	}
	return "[agentd] session tokens signed with " + sessionSecretEnv + " (independent of the API secret)"
}
