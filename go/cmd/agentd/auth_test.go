package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/binocarlos/badcode-agent-orange/httpapi"
)

// noKeys is the key index of a deployment with no project map — what every
// pre-existing agentd deployment has.
func noKeys(t *testing.T) *projectKeyIndex {
	t.Helper()
	keys, err := newProjectKeys(nil, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("newProjectKeys: %v", err)
	}
	return keys
}

// wolfKeys is a one-project index whose key is goodKey (apikey_test.go).
func wolfKeys(t *testing.T) *projectKeyIndex {
	t.Helper()
	keys, err := newProjectKeys(
		map[string]projectConfig{"wolf": {APIKeyEnv: "WOLF_API_KEY"}},
		envFrom(map[string]string{"WOLF_API_KEY": goodKey}), nil)
	if err != nil {
		t.Fatalf("newProjectKeys: %v", err)
	}
	return keys
}

// captureIdentity mounts the middleware around a handler that records the
// identity the request arrived with.
func captureIdentity(secret []byte, keys projectKeys, got *httpapi.Identity) http.Handler {
	return apiAuthMiddleware(secret, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := identityFromRequest(r)
		*got = id
		w.WriteHeader(http.StatusOK)
	}))
}

// --- the JWT path, unchanged ---

func TestAuthMiddleware_ValidTokenSetsPrincipal(t *testing.T) {
	secret := []byte("test-secret")
	tok, err := devclaims.New(secret).Issue(context.Background(),
		extensionScope("alice@acme.com", "acme"), "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	var got httpapi.Identity
	h := captureIdentity(secret, noKeys(t), &got)

	req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got.UserEmail != "alice@acme.com" || got.Customer != "acme" {
		t.Fatalf("identity = %+v, want (alice@acme.com, acme)", got)
	}
	if got.SessionScope != "" {
		t.Fatalf("an ordinary login token arrived scoped to %q", got.SessionScope)
	}
}

func TestAuthMiddleware_RejectsMissingAndBadToken(t *testing.T) {
	secret := []byte("test-secret")
	var got httpapi.Identity
	h := captureIdentity(secret, noKeys(t), &got)
	for _, tc := range []struct{ name, auth string }{
		{"missing", ""},
		{"garbage", "Bearer not-a-jwt"},
		{"wrong scheme", "Basic abc"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, rr.Code)
		}
	}
}

func TestAuthMiddleware_EmptySecretIsDevOpen(t *testing.T) {
	var got httpapi.Identity
	h := captureIdentity(nil, noKeys(t), &got)
	req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || got.Customer == "" {
		t.Fatalf("dev-open should pass with a default principal; status=%d identity=%+v", rr.Code, got)
	}
}

// --- the API-key path ---

func TestAuthMiddleware_APIKeyAuthenticatesItsProject(t *testing.T) {
	var got httpapi.Identity
	h := captureIdentity([]byte("test-secret"), wolfKeys(t), &got)

	req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	req.Header.Set(apiKeyHeader, goodKey)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got.Customer != "wolf" {
		t.Fatalf("customer = %q, want wolf", got.Customer)
	}
	if got.UserEmail != "api-key:wolf" {
		t.Fatalf("user email = %q, want api-key:wolf", got.UserEmail)
	}
	if got.SessionScope != "" {
		t.Fatalf("an API key arrived scoped to %q — a key grants the whole project", got.SessionScope)
	}
}

func TestAuthMiddleware_InvalidAPIKeyIs401(t *testing.T) {
	var got httpapi.Identity
	h := captureIdentity([]byte("test-secret"), wolfKeys(t), &got)
	for _, key := range []string{"wrong-but-long-enough-key-value-x", "short", goodKey + "x"} {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
		req.Header.Set(apiKeyHeader, key)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("key %q: status = %d, want 401", key, rr.Code)
		}
	}
}

// A caller that sent a key meant to use it. Falling through to the JWT path (or
// worse, to dev-open) on a bad key would turn a typo into a silent downgrade.
func TestAuthMiddleware_BadAPIKeyDoesNotFallThroughToJWT(t *testing.T) {
	secret := []byte("test-secret")
	tok, err := devclaims.New(secret).Issue(context.Background(),
		extensionScope("alice@acme.com", "acme"), "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	var got httpapi.Identity
	h := captureIdentity(secret, wolfKeys(t), &got)

	req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	req.Header.Set(apiKeyHeader, "definitely-not-the-right-key-value")
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (a bad key must not fall back to the bearer token)", rr.Code)
	}
}

// A configured key means a real deployment. Dev-open there would hand every
// unauthenticated request the "demo" project alongside a live third-party key.
func TestAuthMiddleware_ConfiguredKeyDisablesDevOpen(t *testing.T) {
	var got httpapi.Identity
	h := captureIdentity(nil, wolfKeys(t), &got) // no JWT secret at all

	req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — dev-open must be off when a key exists", rr.Code)
	}

	// The key itself still works with no JWT secret set.
	req = httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	req.Header.Set(apiKeyHeader, goodKey)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || got.Customer != "wolf" {
		t.Fatalf("status = %d identity = %+v, want 200 / wolf", rr.Code, got)
	}
}

// --- the embed scope ---

func TestAuthMiddleware_ScopedTokenCarriesItsSession(t *testing.T) {
	secret := []byte("test-secret")
	tok, err := devclaims.New(secret).IssueScoped(context.Background(),
		extension.ContextScope{Customer: "wolf", UserEmail: "api-key:wolf", Job: "embed"},
		"", devclaims.SessionScope("s-hyp-a"))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	var got httpapi.Identity
	h := captureIdentity(secret, wolfKeys(t), &got)

	req := httptest.NewRequest(http.MethodGet, "/agent/session/s-hyp-a/stream", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got.Customer != "wolf" {
		t.Fatalf("customer = %q, want wolf", got.Customer)
	}
	if got.SessionScope != "s-hyp-a" {
		t.Fatalf("SessionScope = %q, want s-hyp-a (this is what httpapi enforces)", got.SessionScope)
	}
}

// A scope this middleware does not understand must not be silently read as
// "unrestricted" — it reads as no session scope, and any future scope kind gets
// its own explicit handling.
func TestAuthMiddleware_UnknownScopeKindIsNotASessionScope(t *testing.T) {
	secret := []byte("test-secret")
	tok, err := devclaims.New(secret).IssueScoped(context.Background(),
		extension.ContextScope{Customer: "wolf"}, "", "project:wolf")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	var got httpapi.Identity
	h := captureIdentity(secret, noKeys(t), &got)
	req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got.SessionScope != "" {
		t.Fatalf("SessionScope = %q, want empty for a non-session scope", got.SessionScope)
	}
}
