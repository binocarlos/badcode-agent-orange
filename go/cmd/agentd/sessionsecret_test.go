package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/binocarlos/badcode-agent-orange/httpapi"
)

// projectRoutes are the routes doc 22's RD30 names as the blast radius: the
// whole product layer's configuration, reachable by anything holding a valid
// project credential. The middleware is mounted at "/" (main.go), so this list
// is a sample of that surface rather than an exhaustive one — but if a session
// token authenticates any of these, it authenticates all of them.
var projectRoutes = []struct{ method, path string }{
	{http.MethodGet, "/agent/workers"},
	{http.MethodPut, "/agent/workers/researcher"},
	{http.MethodDelete, "/agent/workers/researcher"},
	{http.MethodGet, "/agent/project-settings"},
	{http.MethodPut, "/agent/project-settings"},
	{http.MethodGet, "/agent/schedules"},
	{http.MethodPost, "/agent/schedules"},
	{http.MethodPost, "/agent/events"},
	{http.MethodGet, "/agent/events"},
}

// sessionTokenFor mints exactly what the Runner injects into a container:
// devclaims over the session's scope, with the session id as sid, signed with
// the key agentd resolves for the session class.
func sessionTokenFor(t *testing.T, env map[string]string, project, sessionID string) string {
	t.Helper()
	secret, _ := resolveSessionSecret(envFrom(env))
	tok, err := devclaims.New(secret).Issue(context.Background(),
		extension.ContextScope{Customer: project, Job: "worker-job", UserEmail: ""}, sessionID)
	if err != nil {
		t.Fatalf("issue session token: %v", err)
	}
	return tok
}

// RD30: the token a prompt-injected model can read out of its own environment
// must not authenticate the project's configuration routes.
func TestSessionTokenIsRejectedByProjectRoutes(t *testing.T) {
	env := map[string]string{"AGENTKIT_JWT_SECRET": "deployment-secret"}
	apiSecret := []byte(env["AGENTKIT_JWT_SECRET"])
	tok := sessionTokenFor(t, env, "wolf", "s-123")

	var got httpapi.Identity
	h := captureIdentity(apiSecret, noKeys(t), &got)

	for _, rt := range projectRoutes {
		got = httpapi.Identity{}
		req := httptest.NewRequest(rt.method, rt.path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d, want 401 — a session container's token authenticated a project route (identity %+v)",
				rt.method, rt.path, rr.Code, got)
		}
	}
}

// The same, with an API key also configured: a session token must not become
// acceptable just because the deployment has another credential kind.
func TestSessionTokenIsRejectedAlongsideAPIKeys(t *testing.T) {
	env := map[string]string{"AGENTKIT_JWT_SECRET": "deployment-secret"}
	tok := sessionTokenFor(t, env, "wolf", "s-123")

	var got httpapi.Identity
	h := captureIdentity([]byte("deployment-secret"), wolfKeys(t), &got)
	req := httptest.NewRequest(http.MethodGet, "/agent/workers", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// The other half of the pin: the fix must reject the session credential without
// rejecting the credentials that are supposed to work.
func TestProjectCredentialsStillWork(t *testing.T) {
	apiSecret := []byte("deployment-secret")

	t.Run("login token", func(t *testing.T) {
		tok, err := devclaims.New(apiSecret).Issue(context.Background(),
			extensionScope("alice@acme.com", "wolf"), "")
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		var got httpapi.Identity
		h := captureIdentity(apiSecret, noKeys(t), &got)
		for _, rt := range projectRoutes {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK || got.Customer != "wolf" {
				t.Fatalf("%s %s: status = %d identity = %+v, want 200 / wolf", rt.method, rt.path, rr.Code, got)
			}
		}
	})

	t.Run("api key", func(t *testing.T) {
		var got httpapi.Identity
		h := captureIdentity(apiSecret, wolfKeys(t), &got)
		req := httptest.NewRequest(http.MethodGet, "/agent/workers", nil)
		req.Header.Set(apiKeyHeader, goodKey)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || got.Customer != "wolf" {
			t.Fatalf("status = %d identity = %+v, want 200 / wolf", rr.Code, got)
		}
	})

	// An embed token (their T10) is an API-class credential confined by the
	// scope claim, not by sid — the sid guard must leave it alone.
	t.Run("embed token", func(t *testing.T) {
		tok, err := devclaims.New(apiSecret).IssueScoped(context.Background(),
			extension.ContextScope{Customer: "wolf", UserEmail: "api-key:wolf", Job: "embed"},
			"", devclaims.SessionScope("s-123"))
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		var got httpapi.Identity
		h := captureIdentity(apiSecret, noKeys(t), &got)
		req := httptest.NewRequest(http.MethodGet, "/agent/session/s-123/stream", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || got.SessionScope != "s-123" {
			t.Fatalf("status = %d identity = %+v, want 200 / scope s-123", rr.Code, got)
		}
	})
}

// Dev-open is the zero-configuration demo, and this change must not touch it:
// no secret, no key, no token, still a default principal on every route.
func TestDevOpenIsUnchangedByKeySeparation(t *testing.T) {
	var got httpapi.Identity
	h := captureIdentity(nil, noKeys(t), &got)
	for _, rt := range projectRoutes {
		got = httpapi.Identity{}
		req := httptest.NewRequest(rt.method, rt.path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || got.Customer != "demo" {
			t.Fatalf("%s %s: status = %d identity = %+v, want 200 / demo", rt.method, rt.path, rr.Code, got)
		}
	}

	// And a dev-open deployment still mints session tokens with a non-empty
	// key — the property that keeps the core MCP server closed while the API
	// is open (mcpserver.go's sessionTokenAuth comment).
	secret, explicit := resolveSessionSecret(envFrom(nil))
	if len(secret) == 0 || explicit {
		t.Fatalf("dev-open session secret = %d bytes explicit=%v, want a non-empty derived key", len(secret), explicit)
	}
}

// A GUARD, not evidence: this passes against the unfixed code too, because it
// pins library behaviour the fix newly depends on. /dev/token now mints with
// the API secret, and in the zero-config demo that secret is EMPTY — if signing
// with an empty HMAC key ever stopped working, the shipped `docker compose up`
// demo would 500 at the first page load, which is the one path this change was
// forbidden to touch.
func TestDevTokenIssuerWorksWithEmptySecret(t *testing.T) {
	tok, err := devclaims.New(nil).Issue(context.Background(),
		extensionScope("demo@example.com", "demo"), "")
	if err != nil {
		t.Fatalf("issuing with an empty secret failed: %v — dev-open /dev/token would 500", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
}

func TestResolveSessionSecret(t *testing.T) {
	api := "deployment-secret"

	derived, explicit := resolveSessionSecret(envFrom(map[string]string{"AGENTKIT_JWT_SECRET": api}))
	if explicit {
		t.Fatalf("explicit = true with no %s set", sessionSecretEnv)
	}
	if bytes.Equal(derived, []byte(api)) {
		t.Fatalf("the derived session key IS the API secret — RD30 is not fixed")
	}
	if len(derived) != 32 {
		t.Fatalf("derived key = %d bytes, want 32 (HMAC-SHA256)", len(derived))
	}
	// Deterministic: two agentd processes with the same configuration must
	// verify each other's tokens, and a restart must not invalidate them.
	again, _ := resolveSessionSecret(envFrom(map[string]string{"AGENTKIT_JWT_SECRET": api}))
	if !bytes.Equal(derived, again) {
		t.Fatalf("derivation is not deterministic")
	}
	// Rolling the API secret rolls the session key with it.
	other, _ := resolveSessionSecret(envFrom(map[string]string{"AGENTKIT_JWT_SECRET": api + "-2"}))
	if bytes.Equal(derived, other) {
		t.Fatalf("two different API secrets derived the same session key")
	}

	// The override wins, is trimmed (a compose env_file value arrives with a
	// newline), and reports itself as explicit.
	over, explicit := resolveSessionSecret(envFrom(map[string]string{
		"AGENTKIT_JWT_SECRET": api,
		sessionSecretEnv:      "  rolled-session-secret\n",
	}))
	if !explicit || string(over) != "rolled-session-secret" {
		t.Fatalf("override = %q explicit=%v", over, explicit)
	}

	// No API secret at all (dev-open) still yields a usable key.
	dev, explicit := resolveSessionSecret(envFrom(nil))
	if explicit || len(dev) != 32 {
		t.Fatalf("dev-open derived key = %d bytes explicit=%v", len(dev), explicit)
	}
	if bytes.Equal(dev, []byte(sessionSecretFallback)) {
		t.Fatalf("dev-open session key is the literal fallback string")
	}
}

// The one configuration that re-opens RD30 is an explicit override set to the
// API secret. agentd may not refuse to boot on it (that would be a breaking
// change), so it must say so where an operator reads the boot log.
func TestSessionSecretNotice(t *testing.T) {
	api := []byte("deployment-secret")

	derived, explicit := resolveSessionSecret(envFrom(map[string]string{"AGENTKIT_JWT_SECRET": string(api)}))
	if msg := sessionSecretNotice(api, derived, explicit); msg == "" || mentions(msg, "WARNING") {
		t.Fatalf("derived-key notice = %q, want a plain line", msg)
	}

	if msg := sessionSecretNotice(api, api, true); !mentions(msg, "WARNING") || !mentions(msg, sessionSecretEnv) {
		t.Fatalf("collapsed-secret notice = %q, want a WARNING naming %s", msg, sessionSecretEnv)
	}

	if msg := sessionSecretNotice(api, []byte("something-else"), true); mentions(msg, "WARNING") {
		t.Fatalf("a genuinely independent override warned: %q", msg)
	}
}

func mentions(s, sub string) bool { return strings.Contains(s, sub) }
