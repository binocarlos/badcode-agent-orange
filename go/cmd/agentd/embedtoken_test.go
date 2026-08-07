package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/golang-jwt/jwt/v5"
)

// fakeSessionNames is the embedSessionLookup seam. It answers only for the
// (customer, name) pair it was built with, which is exactly how the real store
// behaves — a foreign project's name simply does not match, so "absent" and
// "somebody else's" are one answer here as they are there.
type fakeSessionNames struct {
	customer, name, id string
	calls              int
	err                error
}

func (f *fakeSessionNames) GetSessionByName(_ context.Context, customer, name string) (*agentdb.Session, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if customer != f.customer || name != f.name {
		return nil, fmt.Errorf("%w: %q in project %q", agentdb.ErrSessionNotFound, name, customer)
	}
	return &agentdb.Session{ID: f.id, Name: f.name, Customer: f.customer}, nil
}

const (
	embedTestSecret  = "embed-test-secret"
	embedTestSession = "sess-hyp-a-0001"
)

func wolfSessionNames() *fakeSessionNames {
	return &fakeSessionNames{customer: "wolf", name: "hypothesis-a", id: embedTestSession}
}

// embedRequest builds a request as apiAuthMiddleware would have left it: the
// principal in the context AND the X-API-Key header still on the request, which
// is what authenticatedByAPIKey reads (googleauth.go:478).
func embedRequest(project, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/agent/embed-token", strings.NewReader(body))
	req.Header.Set(apiKeyHeader, goodKey)
	return req.WithContext(contextWithPrincipal(req.Context(),
		principal{email: apiKeyEmail(project), customer: project}))
}

// embedClaims parses a minted token with the SAME secret apiAuthMiddleware
// verifies with. A token this fails on is a token the API would 401.
func embedClaims(t *testing.T, token string) jwt.MapClaims {
	t.Helper()
	claims := jwt.MapClaims{}
	tok, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return []byte(embedTestSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !tok.Valid {
		t.Fatalf("minted token does not verify with the API secret: %v", err)
	}
	return claims
}

func decodeEmbedToken(t *testing.T, rec *httptest.ResponseRecorder) embedTokenResponse {
	t.Helper()
	var got embedTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	return got
}

// TestEmbedTokenMintsASessionScopedToken is the happy path: the token verifies
// with the API secret, names the key's project, and carries the scope that
// confines it to the one resolved session id.
func TestEmbedTokenMintsASessionScopedToken(t *testing.T) {
	store := wolfSessionNames()
	h := embedTokenHandler([]byte(embedTestSecret), store)

	rec := httptest.NewRecorder()
	before := time.Now()
	h(rec, embedRequest("wolf", `{"session":"hypothesis-a"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	got := decodeEmbedToken(t, rec)
	claims := embedClaims(t, got.Token)
	if claims["customer"] != "wolf" {
		t.Fatalf("customer = %v, want wolf", claims["customer"])
	}
	if claims[devclaims.ScopeClaim] != devclaims.SessionScope(embedTestSession) {
		t.Fatalf("scope = %v, want %q", claims[devclaims.ScopeClaim], devclaims.SessionScope(embedTestSession))
	}
	// The scope claim is what the middleware reads; a token whose scope the
	// middleware cannot parse would authenticate with FULL project rights.
	sid, ok := devclaims.ParseSessionScope(claims[devclaims.ScopeClaim].(string))
	if !ok || sid != embedTestSession {
		t.Fatalf("ParseSessionScope(%v) = %q, %v", claims[devclaims.ScopeClaim], sid, ok)
	}
	// sid must stay EMPTY — see the handler's comment. A non-empty sid signed
	// with this secret is a valid core-MCP session token (mcpserver.go:464-505),
	// and this credential is handed to a browser in a third-party page.
	if claims["sid"] != "" {
		t.Fatalf("sid = %v, want empty (an embed token must not be a session token)", claims["sid"])
	}
	// expires_at is the token's own exp, so a client that trusts the body and a
	// server that trusts the claim agree.
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		t.Fatalf("exp: %v", err)
	}
	if got.ExpiresAt != exp.Unix() {
		t.Fatalf("expires_at = %d, token exp = %d", got.ExpiresAt, exp.Unix())
	}
	if d := exp.Sub(before); d < 890*time.Second || d > 910*time.Second {
		t.Fatalf("default TTL = %s, want ~900s", d)
	}
	if store.calls != 1 {
		t.Fatalf("store called %d times, want 1", store.calls)
	}
}

// TestEmbedTokenClampsTTL — clamped, never rejected. An integrator asking for a
// day gets an hour and a working iframe, not a 400 they discover in production.
func TestEmbedTokenClampsTTL(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantTTL time.Duration
	}{
		{"absent means the default", `{"session":"hypothesis-a"}`, 900 * time.Second},
		{"explicit zero means the default", `{"session":"hypothesis-a","ttl_seconds":0}`, 900 * time.Second},
		{"below the floor clamps up", `{"session":"hypothesis-a","ttl_seconds":30}`, 60 * time.Second},
		{"negative clamps up", `{"session":"hypothesis-a","ttl_seconds":-5}`, 60 * time.Second},
		{"the floor itself", `{"session":"hypothesis-a","ttl_seconds":60}`, 60 * time.Second},
		{"in range is honoured", `{"session":"hypothesis-a","ttl_seconds":1200}`, 1200 * time.Second},
		{"the ceiling itself", `{"session":"hypothesis-a","ttl_seconds":3600}`, 3600 * time.Second},
		{"above the ceiling clamps down", `{"session":"hypothesis-a","ttl_seconds":86400}`, 3600 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := embedTokenHandler([]byte(embedTestSecret), wolfSessionNames())
			rec := httptest.NewRecorder()
			before := time.Now()
			h(rec, embedRequest("wolf", tt.body))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			exp, err := embedClaims(t, decodeEmbedToken(t, rec).Token).GetExpirationTime()
			if err != nil || exp == nil {
				t.Fatalf("exp: %v", err)
			}
			// One second of slack: the claim is built from the handler's own
			// time.Now(), a moment after this one.
			if d := exp.Sub(before); d < tt.wantTTL-2*time.Second || d > tt.wantTTL+2*time.Second {
				t.Fatalf("ttl = %s, want %s", d, tt.wantTTL)
			}
		})
	}
}

// TestEmbedTokenRefusesUnresolvableSessions — 404 for absent and for another
// project's name alike, since the lookup is scoped to the key's project and a
// distinguishable answer would make this route a name oracle.
func TestEmbedTokenRefusesUnresolvableSessions(t *testing.T) {
	tests := []struct {
		name       string
		project    string
		body       string
		wantStatus int
	}{
		{"unknown name", "wolf", `{"session":"no-such-thing"}`, http.StatusNotFound},
		{"another project's session", "demo", `{"session":"hypothesis-a"}`, http.StatusNotFound},
		{"missing session field", "wolf", `{}`, http.StatusBadRequest},
		{"empty session field", "wolf", `{"session":""}`, http.StatusBadRequest},
		{"malformed body", "wolf", `not json`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := embedTokenHandler([]byte(embedTestSecret), wolfSessionNames())
			rec := httptest.NewRecorder()
			h(rec, embedRequest(tt.project, tt.body))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "token") {
				t.Fatalf("refusal body mentions a token: %s", rec.Body)
			}
		})
	}
}

// TestEmbedTokenNotFoundIsIndistinguishable pins that a foreign name and an
// absent one produce the SAME body, not merely the same status.
func TestEmbedTokenNotFoundIsIndistinguishable(t *testing.T) {
	absent := httptest.NewRecorder()
	embedTokenHandler([]byte(embedTestSecret), wolfSessionNames())(absent,
		embedRequest("wolf", `{"session":"no-such-thing"}`))
	foreign := httptest.NewRecorder()
	embedTokenHandler([]byte(embedTestSecret), wolfSessionNames())(foreign,
		embedRequest("demo", `{"session":"hypothesis-a"}`))
	if absent.Body.String() != foreign.Body.String() {
		t.Fatalf("absent %q vs foreign %q — the two must be indistinguishable",
			absent.Body.String(), foreign.Body.String())
	}
}

// TestEmbedTokenStoreOutageIsNotAMissingSession — a database refusing
// connections is a 500, so an operator is not sent hunting for a session that
// is sitting right there (mirrors sessions_byname.go's rule).
func TestEmbedTokenStoreOutageIsNotAMissingSession(t *testing.T) {
	store := wolfSessionNames()
	store.err = fmt.Errorf("dial tcp: connection refused")
	rec := httptest.NewRecorder()
	embedTokenHandler([]byte(embedTestSecret), store)(rec, embedRequest("wolf", `{"session":"hypothesis-a"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
}

// TestEmbedTokenRequiresAnAPIKey is the ticket's sharpest criterion: a browser
// must never mint its own embed tokens, so a console JWT — which is what a
// browser holds — is 403 on this route even though the middleware accepts it.
func TestEmbedTokenRequiresAnAPIKey(t *testing.T) {
	store := wolfSessionNames()
	secret := []byte(embedTestSecret)
	mux := http.NewServeMux()
	registerEmbedToken(mux, secret, store)
	h := apiAuthMiddleware(secret, wolfKeys(t), mux)

	consoleJWT, err := devclaims.New(secret).Issue(context.Background(), extensionScope("kai@example.com", "wolf"), "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// An embed token trying to mint another embed token — the escalation this
	// route exists to prevent.
	embedJWT, err := devclaims.New(secret).IssueScoped(context.Background(),
		extensionScope("kai@example.com", "wolf"), "", devclaims.SessionScope(embedTestSession))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	tests := []struct {
		name       string
		header     [2]string
		wantStatus int
	}{
		{"api key", [2]string{apiKeyHeader, goodKey}, http.StatusOK},
		{"console jwt", [2]string{"Authorization", "Bearer " + consoleJWT}, http.StatusForbidden},
		{"embed token", [2]string{"Authorization", "Bearer " + embedJWT}, http.StatusForbidden},
		{"no credential", [2]string{"", ""}, http.StatusUnauthorized},
		{"bad api key", [2]string{apiKeyHeader, "0123456789abcdef0123456789abcdeX"}, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := store.calls
			req := httptest.NewRequest(http.MethodPost, "/agent/embed-token",
				strings.NewReader(`{"session":"hypothesis-a"}`))
			if tt.header[0] != "" {
				req.Header.Set(tt.header[0], tt.header[1])
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			// A refused caller must not even learn whether the name exists.
			if tt.wantStatus != http.StatusOK && store.calls != before {
				t.Fatalf("store consulted on a refused request")
			}
		})
	}
}

// TestEmbedTokenAuthenticatesAsItsSession closes the loop the whole ticket is
// for: the minted token, presented to the same middleware, produces a principal
// confined to exactly the session it was minted for.
func TestEmbedTokenAuthenticatesAsItsSession(t *testing.T) {
	secret := []byte(embedTestSecret)
	mux := http.NewServeMux()
	registerEmbedToken(mux, secret, wolfSessionNames())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/agent/embed-token",
		strings.NewReader(`{"session":"hypothesis-a"}`))
	req.Header.Set(apiKeyHeader, goodKey)
	apiAuthMiddleware(secret, wolfKeys(t), mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint: status = %d: %s", rec.Code, rec.Body)
	}
	token := decodeEmbedToken(t, rec).Token

	var got principal
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = principalFromContext(r.Context())
	})
	use := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	use.Header.Set("Authorization", "Bearer "+token)
	apiAuthMiddleware(secret, wolfKeys(t), probe).ServeHTTP(httptest.NewRecorder(), use)

	if got.customer != "wolf" {
		t.Fatalf("customer = %q, want wolf", got.customer)
	}
	if got.embedSession != embedTestSession {
		t.Fatalf("embedSession = %q, want %q", got.embedSession, embedTestSession)
	}
}

// TestEmbedTokenUnavailableWithoutItsDependencies — the two deployments where
// this route cannot work answer 501 rather than minting a token nothing will
// accept. Session names are Postgres-only, and a token signed with an empty
// secret is refused by the middleware's own bearer path (auth.go:84-89).
func TestEmbedTokenUnavailableWithoutItsDependencies(t *testing.T) {
	tests := []struct {
		name    string
		secret  []byte
		store   embedSessionLookup
		wantMsg string
	}{
		{"no session name store (sqlite fallback)", []byte(embedTestSecret), nil, "session names"},
		{"no JWT secret", nil, wolfSessionNames(), "AGENTKIT_JWT_SECRET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			embedTokenHandler(tt.secret, tt.store)(rec, embedRequest("wolf", `{"session":"hypothesis-a"}`))
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.wantMsg) {
				t.Fatalf("body = %q, want it to mention %q", rec.Body.String(), tt.wantMsg)
			}
		})
	}
}

// TestEmbedTokenRefusesAProjectlessPrincipal — a name lookup needs a project to
// scope to, and there is no defensible default.
func TestEmbedTokenRefusesAProjectlessPrincipal(t *testing.T) {
	store := wolfSessionNames()
	rec := httptest.NewRecorder()
	embedTokenHandler([]byte(embedTestSecret), store)(rec, embedRequest("", `{"session":"hypothesis-a"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if store.calls != 0 {
		t.Fatalf("store consulted with no project")
	}
}
