package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/golang-jwt/jwt/v5"
)

func TestParseProjectMap(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		check   func(t *testing.T, pm projectMap)
	}{
		{
			name: "valid map lowercases emails",
			raw:  `{"Kai@Example.COM": ["apples-oranges", "pears-plums"]}`,
			check: func(t *testing.T, pm projectMap) {
				if got := pm["kai@example.com"]; len(got) != 2 || got[0] != "apples-oranges" {
					t.Fatalf("projects = %v", got)
				}
			},
		},
		{name: "invalid json", raw: `{`, wantErr: true},
		{name: "empty map", raw: `{}`, wantErr: true},
		{name: "email with no projects", raw: `{"a@b.c": []}`, wantErr: true},
		{name: "empty project id", raw: `{"a@b.c": [""]}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm, err := parseProjectMap([]byte(tt.raw))
			if tt.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, pm)
			}
		})
	}
}

func TestLoadProjectMap_InlineWinsOverFile(t *testing.T) {
	env := map[string]string{
		"AGENTKIT_PROJECT_MAP":      `{"a@b.c": ["p1"]}`,
		"AGENTKIT_PROJECT_MAP_FILE": "/does/not/exist.json",
	}
	pm, err := loadProjectMap(func(k string) string { return env[k] })
	if err != nil || len(pm["a@b.c"]) != 1 {
		t.Fatalf("pm=%v err=%v", pm, err)
	}
	if _, err := loadProjectMap(func(string) string { return "" }); err == nil {
		t.Fatal("expected error when neither env var is set")
	}
}

func TestProjectMap_AllProjectsDedupes(t *testing.T) {
	pm := projectMap{"a@b.c": {"p1", "p2"}, "d@e.f": {"p2", "p3"}}
	all := pm.allProjects()
	if len(all) != 3 {
		t.Fatalf("allProjects = %v, want 3 unique", all)
	}
}

func TestProjectMap_Resolve(t *testing.T) {
	pm := projectMap{
		"fixed@b.c": {"p1"},
		"dev@b.c":   {"*"},
		"mix@b.c":   {"p2", "*"},
	}
	tests := []struct {
		email        string
		wantN        int
		wantWildcard bool
		wantOK       bool
	}{
		{"fixed@b.c", 1, false, true},
		{"dev@b.c", 2, true, true}, // union of p1, p2 — "*" itself excluded
		{"mix@b.c", 2, true, true},
		{"stranger@b.c", 0, false, false},
	}
	for _, tt := range tests {
		projects, wildcard, ok := pm.resolve(tt.email)
		if ok != tt.wantOK || wildcard != tt.wantWildcard || len(projects) != tt.wantN {
			t.Errorf("resolve(%s) = %v wildcard=%v ok=%v, want n=%d wildcard=%v ok=%v",
				tt.email, projects, wildcard, ok, tt.wantN, tt.wantWildcard, tt.wantOK)
		}
	}
}

// --- the object form ---

// The object form carries per-project config (API key env var, framing origins)
// alongside the user→projects map the flat form has always been. Both forms stay
// valid forever: the flat one is what every existing deployment has mounted.
func TestParseProjectMapObjectForm(t *testing.T) {
	raw := `{
	  "users":    {"Kai@Example.COM": ["wolf", "demo"]},
	  "projects": {
	    "wolf":     {"api_key_env": "WOLF_API_KEY", "allowed_origins": ["https://wolf.badcode.dev", "http://localhost:5173"]},
	    "unwanted": {}
	  }
	}`
	s, err := parseProjectSettings([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := s.users["kai@example.com"]; len(got) != 2 || got[0] != "wolf" {
		t.Fatalf("users = %v", s.users)
	}
	if got := s.projects["wolf"].APIKeyEnv; got != "WOLF_API_KEY" {
		t.Fatalf("api_key_env = %q", got)
	}
	if got := s.projects["wolf"].AllowedOrigins; len(got) != 2 || got[1] != "http://localhost:5173" {
		t.Fatalf("allowed_origins = %v", got)
	}
	// A project nobody is a member of is legal: an API-key-only integration has
	// no console users at all.
	if _, ok := s.projects["unwanted"]; !ok {
		t.Fatal("a project referenced by no user was dropped")
	}
}

// parseProjectMap keeps its old signature and old behaviour — it is what the
// login handlers take — and simply reads the users half of whichever form
// arrived.
func TestParseProjectMapAcceptsBothForms(t *testing.T) {
	for _, raw := range []string{
		`{"a@b.c": ["p1"]}`,
		`{"users": {"a@b.c": ["p1"]}}`,
		`{"users": {"a@b.c": ["p1"]}, "projects": {"p1": {"api_key_env": "P1_KEY"}}}`,
	} {
		pm, err := parseProjectMap([]byte(raw))
		if err != nil {
			t.Fatalf("parse(%s): %v", raw, err)
		}
		if len(pm["a@b.c"]) != 1 || pm["a@b.c"][0] != "p1" {
			t.Fatalf("parse(%s) = %v", raw, pm)
		}
	}
}

func TestParseProjectMapObjectFormErrors(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string // substring the message must contain
	}{
		{"unknown top-level key", `{"users": {"a@b.c": ["p"]}, "teams": {}}`, "teams"},
		{"mixed forms", `{"a@b.c": ["p"], "projects": {"p": {}}}`, "mixes"},
		{"bad env var name", `{"projects": {"wolf": {"api_key_env": "WOLF-API-KEY"}}}`, "wolf"},
		{"origin with a path", `{"projects": {"wolf": {"allowed_origins": ["https://wolf.dev/embed"]}}}`, "wolf"},
		{"origin not absolute", `{"projects": {"wolf": {"allowed_origins": ["wolf.dev"]}}}`, "wolf"},
		{"origin plain http", `{"projects": {"wolf": {"allowed_origins": ["http://wolf.dev"]}}}`, "wolf"},
		{"origin wildcard", `{"projects": {"wolf": {"allowed_origins": ["*"]}}}`, "wolf"},
		{"bad project id", `{"projects": {"Wolf_Prod": {}}}`, "Wolf_Prod"},
		{"nothing at all", `{"users": {}, "projects": {}}`, "empty"},
		{"user with no projects", `{"users": {"a@b.c": []}}`, "a@b.c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseProjectSettings([]byte(tt.raw))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not name %q", err, tt.wantErr)
			}
		})
	}
}

// A localhost origin over plain http is the one http exception — it is what a
// Vite dev server serves the embedding app from.
func TestParseProjectMapAllowsLocalhostOverHTTP(t *testing.T) {
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:8080", "http://localhost"} {
		raw := `{"projects": {"wolf": {"allowed_origins": ["` + origin + `"]}}}`
		if _, err := parseProjectSettings([]byte(raw)); err != nil {
			t.Fatalf("%s: %v", origin, err)
		}
	}
}

func TestLoadProjectMapReadsTheObjectForm(t *testing.T) {
	env := map[string]string{
		"AGENTKIT_PROJECT_MAP": `{"users": {"a@b.c": ["wolf"]}, "projects": {"wolf": {"api_key_env": "WOLF_API_KEY"}}}`,
	}
	s, err := loadProjectSettings(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.projects["wolf"].APIKeyEnv != "WOLF_API_KEY" {
		t.Fatalf("projects = %v", s.projects)
	}
	if len(s.users["a@b.c"]) != 1 {
		t.Fatalf("users = %v", s.users)
	}
}

// fakeTokeninfo serves a scripted Google tokeninfo response.
func fakeTokeninfo(t *testing.T, status int, body map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id_token") == "" {
			t.Error("id_token query param missing")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestGoogleVerifier(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		info      map[string]string
		wantEmail string
		wantErr   string
	}{
		{
			name:      "valid token",
			status:    200,
			info:      map[string]string{"aud": "client-1", "email": "Kai@Example.com", "email_verified": "true"},
			wantEmail: "kai@example.com",
		},
		{
			name:    "wrong audience",
			status:  200,
			info:    map[string]string{"aud": "other-client", "email": "a@b.c", "email_verified": "true"},
			wantErr: "audience",
		},
		{
			name:    "unverified email",
			status:  200,
			info:    map[string]string{"aud": "client-1", "email": "a@b.c", "email_verified": "false"},
			wantErr: "not verified",
		},
		{
			name:    "tokeninfo rejects token",
			status:  400,
			info:    map[string]string{"error": "invalid_token"},
			wantErr: "status 400",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeTokeninfo(t, tt.status, tt.info)
			defer srv.Close()
			v := &googleVerifier{clientID: "client-1", tokeninfoURL: srv.URL}
			req := httptest.NewRequest(http.MethodPost, "/auth/google", nil)
			email, err := v.Verify(req, "some-credential")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || email != tt.wantEmail {
				t.Fatalf("email=%q err=%v, want %q", email, err, tt.wantEmail)
			}
		})
	}
}

// decodeLoginResponse asserts each returned project token carries the right
// customer/email claims under the shared secret.
func decodeLoginResponse(t *testing.T, body []byte, secret []byte, wantEmail string) map[string]string {
	t.Helper()
	var resp loginResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if resp.Email != wantEmail {
		t.Fatalf("email = %q, want %q", resp.Email, wantEmail)
	}
	got := map[string]string{}
	for _, p := range resp.Projects {
		claims := jwt.MapClaims{}
		tok, err := jwt.ParseWithClaims(p.Token, claims, func(*jwt.Token) (any, error) { return secret, nil },
			jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || !tok.Valid {
			t.Fatalf("token for %s invalid: %v", p.ID, err)
		}
		if claims["customer"] != p.ID || claims["email"] != wantEmail {
			t.Fatalf("claims for %s: %v", p.ID, claims)
		}
		got[p.ID] = p.Token
	}
	return got
}

func TestAuthGoogleHandler(t *testing.T) {
	secret := []byte("test-secret")
	issuer := devclaims.NewWithTTL(secret, time.Hour)
	pm := projectMap{"kai@example.com": {"apples-oranges", "pears-plums"}}

	srv := fakeTokeninfo(t, 200, map[string]string{
		"aud": "client-1", "email": "kai@example.com", "email_verified": "true",
	})
	defer srv.Close()
	h := authGoogleHandler(&googleVerifier{clientID: "client-1", tokeninfoURL: srv.URL}, pm, issuer)

	t.Run("mapped email gets per-project tokens", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/auth/google", strings.NewReader(`{"credential":"c"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		tokens := decodeLoginResponse(t, rec.Body.Bytes(), secret, "kai@example.com")
		if len(tokens) != 2 {
			t.Fatalf("projects = %v, want 2", tokens)
		}
	})

	t.Run("missing credential is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/auth/google", strings.NewReader(`{}`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("unmapped email is 403", func(t *testing.T) {
		srv2 := fakeTokeninfo(t, 200, map[string]string{
			"aud": "client-1", "email": "stranger@example.com", "email_verified": "true",
		})
		defer srv2.Close()
		h2 := authGoogleHandler(&googleVerifier{clientID: "client-1", tokeninfoURL: srv2.URL}, pm, issuer)
		rec := httptest.NewRecorder()
		h2(rec, httptest.NewRequest(http.MethodPost, "/auth/google", strings.NewReader(`{"credential":"c"}`)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("bad credential is 401", func(t *testing.T) {
		srv2 := fakeTokeninfo(t, 400, map[string]string{"error": "invalid_token"})
		defer srv2.Close()
		h2 := authGoogleHandler(&googleVerifier{clientID: "client-1", tokeninfoURL: srv2.URL}, pm, issuer)
		rec := httptest.NewRecorder()
		h2(rec, httptest.NewRequest(http.MethodPost, "/auth/google", strings.NewReader(`{"credential":"c"}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestAuthPasswordHandler(t *testing.T) {
	secret := []byte("test-secret")
	issuer := devclaims.NewWithTTL(secret, time.Hour)
	pm := projectMap{
		"kai@example.com":  {"apples-oranges"},
		"test@example.com": {"pears-plums"},
	}
	h := authPasswordHandler("test@example.com", "orange-e2e", pm, issuer)

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader(body)))
		return rec
	}

	t.Run("valid credentials grant the union of all projects", func(t *testing.T) {
		rec := post(`{"email":"Test@Example.com","password":"orange-e2e"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		tokens := decodeLoginResponse(t, rec.Body.Bytes(), secret, "test@example.com")
		if len(tokens) != 2 {
			t.Fatalf("projects = %v, want union of 2", tokens)
		}
	})

	t.Run("wrong password is 401", func(t *testing.T) {
		if rec := post(`{"email":"test@example.com","password":"nope"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("wrong email is 401", func(t *testing.T) {
		if rec := post(`{"email":"other@example.com","password":"orange-e2e"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("test login is wildcard and carries a login token", func(t *testing.T) {
		rec := post(`{"email":"test@example.com","password":"orange-e2e"}`)
		var resp loginResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.Wildcard || resp.LoginToken == "" {
			t.Fatalf("wildcard=%v login_token=%q, want wildcard grant", resp.Wildcard, resp.LoginToken)
		}
	})
}

func TestAuthProjectTokenHandler(t *testing.T) {
	secret := []byte("test-secret")
	issuer := devclaims.NewWithTTL(secret, time.Hour)
	pm := projectMap{"dev@example.com": {"*"}, "fixed@example.com": {"p1"}}

	// Obtain a real wildcard login token via the password handler.
	login := authPasswordHandler("dev@example.com", "pw", pm, issuer)
	rec := httptest.NewRecorder()
	login(rec, httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader(`{"email":"dev@example.com","password":"pw"}`)))
	var loginResp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &loginResp); err != nil || loginResp.LoginToken == "" {
		t.Fatalf("login: %v %s", err, rec.Body)
	}

	h := authProjectTokenHandler(secret, issuer)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/auth/project-token", strings.NewReader(body)))
		return rec
	}

	t.Run("mints a token for a brand-new project id", func(t *testing.T) {
		rec := post(`{"token":"` + loginResp.LoginToken + `","project":"grapes-kiwis"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		var pt projectToken
		if err := json.Unmarshal(rec.Body.Bytes(), &pt); err != nil || pt.ID != "grapes-kiwis" {
			t.Fatalf("resp = %s", rec.Body)
		}
		claims := jwt.MapClaims{}
		tok, err := jwt.ParseWithClaims(pt.Token, claims, func(*jwt.Token) (any, error) { return secret, nil },
			jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || !tok.Valid || claims["customer"] != "grapes-kiwis" || claims["email"] != "dev@example.com" {
			t.Fatalf("minted claims = %v err=%v", claims, err)
		}
	})

	t.Run("rejects malformed project ids", func(t *testing.T) {
		for _, bad := range []string{"", "*", "Has-Caps", "spaces here", "trailing-", "-leading"} {
			body, _ := json.Marshal(map[string]string{"token": loginResp.LoginToken, "project": bad})
			if rec := post(string(body)); rec.Code != http.StatusBadRequest {
				t.Errorf("project %q: status = %d, want 400", bad, rec.Code)
			}
		}
	})

	t.Run("rejects a non-wildcard project token", func(t *testing.T) {
		// A regular project token (customer != "*") must not mint others.
		regular := loginResp.Projects[0].Token
		if rec := post(`{"token":"` + regular + `","project":"grapes-kiwis"}`); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("rejects a token signed with the wrong secret", func(t *testing.T) {
		otherIssuer := devclaims.NewWithTTL([]byte("other-secret"), time.Hour)
		other := authPasswordHandler("dev@example.com", "pw", pm, otherIssuer)
		rec := httptest.NewRecorder()
		other(rec, httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader(`{"email":"dev@example.com","password":"pw"}`)))
		var otherResp loginResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &otherResp)
		if rec := post(`{"token":"` + otherResp.LoginToken + `","project":"grapes-kiwis"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

func TestAuthConfigHandler(t *testing.T) {
	tests := []struct {
		name      string
		clientID  string
		password  bool
		wantModes []string
	}{
		{"google only", "client-1", false, []string{"google"}},
		{"password only", "", true, []string{"password"}},
		{"both", "client-1", true, []string{"google", "password"}},
		{"neither is dev", "", false, []string{"dev"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			authConfigHandler(tt.clientID, tt.password)(rec, httptest.NewRequest(http.MethodGet, "/auth/config", nil))
			var resp struct {
				Modes          []string `json:"modes"`
				GoogleClientID string   `json:"google_client_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(resp.Modes) != len(tt.wantModes) {
				t.Fatalf("modes = %v, want %v", resp.Modes, tt.wantModes)
			}
			for i, m := range tt.wantModes {
				if resp.Modes[i] != m {
					t.Fatalf("modes = %v, want %v", resp.Modes, tt.wantModes)
				}
			}
			if resp.GoogleClientID != tt.clientID {
				t.Fatalf("google_client_id = %q", resp.GoogleClientID)
			}
		})
	}
}

func TestParseTestLogin(t *testing.T) {
	email, password, err := parseTestLogin("Test@Example.com:pass:with:colons")
	if err != nil || email != "test@example.com" || password != "pass:with:colons" {
		t.Fatalf("got %q %q %v", email, password, err)
	}
	for _, bad := range []string{"", "no-colon", ":pass", "email:"} {
		if _, _, err := parseTestLogin(bad); err == nil {
			t.Fatalf("parseTestLogin(%q) should fail", bad)
		}
	}
}
