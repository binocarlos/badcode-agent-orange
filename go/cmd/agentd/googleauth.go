// Login endpoints for the standalone stack: Google Sign-In and a fixed
// password login for tests. Both verify an identity, look the email up in the
// hard-coded email → projects map, and mint one project-scoped HS256 JWT per
// allowed project ("project" is the existing customer claim/column — a pure
// namespacing concept, no project table). The existing jwtAuthMiddleware
// verifies the minted tokens; nothing downstream changes.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/golang-jwt/jwt/v5"
)

// projectMap maps a lowercased email address to the project IDs (kebab-case
// strings, e.g. "apples-oranges") that user may enter.
type projectMap map[string][]string

// projectWildcard in a user's project list grants every project in the map,
// plus a login token that can mint tokens for brand-new project IDs (dev
// convenience — a project "exists" once a session carries its name).
const projectWildcard = "*"

// validProjectID gates project IDs mintable via the wildcard: kebab-case,
// like "apples-oranges". Keeps arbitrary strings out of the customer column.
var validProjectID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// parseProjectMap decodes the JSON email → project-IDs map, lowercasing emails
// and rejecting entries that could silently grant nothing or everything.
func parseProjectMap(raw []byte) (projectMap, error) {
	var in map[string][]string
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("project map: %w", err)
	}
	if len(in) == 0 {
		return nil, fmt.Errorf("project map: empty")
	}
	out := make(projectMap, len(in))
	for email, projects := range in {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			return nil, fmt.Errorf("project map: empty email key")
		}
		if len(projects) == 0 {
			return nil, fmt.Errorf("project map: %s has no projects", email)
		}
		for _, p := range projects {
			if strings.TrimSpace(p) == "" {
				return nil, fmt.Errorf("project map: %s has an empty project id", email)
			}
		}
		out[email] = projects
	}
	return out, nil
}

// loadProjectMap reads the map from AGENTKIT_PROJECT_MAP (inline JSON, wins)
// or AGENTKIT_PROJECT_MAP_FILE (path to a mounted JSON file).
func loadProjectMap(getenv func(string) string) (projectMap, error) {
	if inline := getenv("AGENTKIT_PROJECT_MAP"); inline != "" {
		return parseProjectMap([]byte(inline))
	}
	if path := getenv("AGENTKIT_PROJECT_MAP_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("project map file: %w", err)
		}
		return parseProjectMap(raw)
	}
	return nil, fmt.Errorf("no project map: set AGENTKIT_PROJECT_MAP or AGENTKIT_PROJECT_MAP_FILE")
}

// allProjects returns the deduplicated union of every concrete project in the
// map — what wildcard entries and the fixed test login are granted.
func (pm projectMap) allProjects() []string {
	seen := map[string]bool{}
	var out []string
	for _, projects := range pm {
		for _, p := range projects {
			if p != projectWildcard && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// resolve returns the effective projects for an email plus whether the entry
// is a wildcard grant. ok=false when the email isn't in the map.
func (pm projectMap) resolve(email string) (projects []string, wildcard, ok bool) {
	projects, ok = pm[email]
	if !ok {
		return nil, false, false
	}
	for _, p := range projects {
		if p == projectWildcard {
			return pm.allProjects(), true, true
		}
	}
	return projects, false, true
}

// googleVerifier validates Google ID tokens via the tokeninfo endpoint —
// zero-dependency server-side verification (Google's TLS cert authenticates
// the response; no local JWKS handling needed at login-only volumes).
type googleVerifier struct {
	clientID     string
	tokeninfoURL string // default https://oauth2.googleapis.com/tokeninfo; injectable for tests
	hc           *http.Client
}

// Verify checks the credential (a Google ID token from Google Identity
// Services) and returns the verified, lowercased email address.
func (v *googleVerifier) Verify(r *http.Request, credential string) (string, error) {
	endpoint := v.tokeninfoURL
	if endpoint == "" {
		endpoint = "https://oauth2.googleapis.com/tokeninfo"
	}
	hc := v.hc
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		endpoint+"?id_token="+url.QueryEscape(credential), nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tokeninfo: status %d", resp.StatusCode)
	}
	var info struct {
		Aud           string `json:"aud"`
		Email         string `json:"email"`
		EmailVerified string `json:"email_verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("tokeninfo: decode: %w", err)
	}
	if info.Aud != v.clientID {
		return "", fmt.Errorf("tokeninfo: audience mismatch")
	}
	if info.EmailVerified != "true" || info.Email == "" {
		return "", fmt.Errorf("tokeninfo: email not verified")
	}
	return strings.ToLower(info.Email), nil
}

// projectToken pairs a project ID with a JWT scoped to it (customer=<id>).
type projectToken struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// loginResponse is the shape both login endpoints return. Wildcard grants
// additionally carry a login token that POST /auth/project-token exchanges
// for tokens to brand-new project IDs.
type loginResponse struct {
	Email      string         `json:"email"`
	Projects   []projectToken `json:"projects"`
	Wildcard   bool           `json:"wildcard,omitempty"`
	LoginToken string         `json:"login_token,omitempty"`
}

// mintProjectTokens issues one project-scoped JWT per project ID.
func mintProjectTokens(r *http.Request, issuer *devclaims.Issuer, email string, projects []string) ([]projectToken, error) {
	out := make([]projectToken, 0, len(projects))
	for _, p := range projects {
		tok, err := issuer.Issue(r.Context(), extension.ContextScope{
			UserEmail: email,
			Customer:  p,
			Job:       "web",
		}, "")
		if err != nil {
			return nil, err
		}
		out = append(out, projectToken{ID: p, Token: tok})
	}
	return out, nil
}

func writeLoginResponse(w http.ResponseWriter, r *http.Request, issuer *devclaims.Issuer, email string, projects []string, wildcard bool) {
	tokens, err := mintProjectTokens(r, issuer, email, projects)
	if err != nil {
		http.Error(w, "token generation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	resp := loginResponse{Email: email, Projects: tokens, Wildcard: wildcard}
	if wildcard {
		// The login token is a project token whose customer is the wildcard
		// sentinel — /auth/project-token accepts it, and it matches no real
		// session rows if someone tries to use it directly as a bearer token.
		lt, err := issuer.Issue(r.Context(), extension.ContextScope{
			UserEmail: email,
			Customer:  projectWildcard,
			Job:       "login",
		}, "")
		if err != nil {
			http.Error(w, "token generation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp.LoginToken = lt
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// authGoogleHandler serves POST /auth/google {credential} → 401 bad credential,
// 403 email not in the project map, else {email, projects:[{id, token}]}.
func authGoogleHandler(v *googleVerifier, pm projectMap, issuer *devclaims.Issuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Credential string `json:"credential"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Credential == "" {
			http.Error(w, "missing credential", http.StatusBadRequest)
			return
		}
		email, err := v.Verify(r, body.Credential)
		if err != nil {
			http.Error(w, "invalid credential", http.StatusUnauthorized)
			return
		}
		projects, wildcard, ok := pm.resolve(email)
		if !ok {
			http.Error(w, "no projects for this account", http.StatusForbidden)
			return
		}
		writeLoginResponse(w, r, issuer, email, projects, wildcard)
	}
}

// authPasswordHandler serves POST /auth/password {email, password} against the
// fixed AGENTKIT_TEST_LOGIN pair ("email:password"). TEST/DEV ONLY — it exists
// so browser e2e can exercise the full login → project → session flow without
// Google. The account is granted every project in the map.
func authPasswordHandler(testEmail, testPassword string, pm projectMap, issuer *devclaims.Issuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "missing credentials", http.StatusBadRequest)
			return
		}
		emailOK := subtle.ConstantTimeCompare([]byte(strings.ToLower(body.Email)), []byte(testEmail)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(body.Password), []byte(testPassword)) == 1
		if !emailOK || !passOK {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		// The test account is an implicit wildcard: every project, plus the
		// ability to mint new ones (it exists for e2e and local dev).
		writeLoginResponse(w, r, issuer, testEmail, pm.allProjects(), true)
	}
}

// authProjectTokenHandler serves POST /auth/project-token {token, project} —
// the wildcard-login exchange: verifies the login token (HS256, customer="*")
// and mints a project-scoped JWT for any well-formed project ID, including
// ones no session carries yet. This is how a new project is "created": pick a
// name, get a token, start a session in it.
func authProjectTokenHandler(secret []byte, issuer *devclaims.Issuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token   string `json:"token"`
			Project string `json:"project"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		if !validProjectID.MatchString(body.Project) || len(body.Project) > 64 {
			http.Error(w, "invalid project id (want kebab-case, e.g. apples-oranges)", http.StatusBadRequest)
			return
		}
		claims := jwt.MapClaims{}
		tok, err := jwt.ParseWithClaims(body.Token, claims, func(*jwt.Token) (any, error) {
			return secret, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || !tok.Valid {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		email, _ := claims["email"].(string)
		if customer, _ := claims["customer"].(string); customer != projectWildcard || email == "" {
			http.Error(w, "not a wildcard login token", http.StatusForbidden)
			return
		}
		minted, err := mintProjectTokens(r, issuer, email, []string{body.Project})
		if err != nil {
			http.Error(w, "token generation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(minted[0])
	}
}

// authConfigHandler serves GET /auth/config — the runtime config channel the
// web UI reads to decide which login UI to render (no build-time Vite env).
func authConfigHandler(googleClientID string, passwordLogin bool) http.HandlerFunc {
	modes := []string{}
	if googleClientID != "" {
		modes = append(modes, "google")
	}
	if passwordLogin {
		modes = append(modes, "password")
	}
	if len(modes) == 0 {
		modes = append(modes, "dev")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"modes":            modes,
			"google_client_id": googleClientID,
		})
	}
}

// parseTestLogin splits AGENTKIT_TEST_LOGIN ("email:password") — the password
// may itself contain colons; only the first splits.
func parseTestLogin(v string) (email, password string, err error) {
	email, password, found := strings.Cut(v, ":")
	email = strings.ToLower(strings.TrimSpace(email))
	if !found || email == "" || password == "" {
		return "", "", fmt.Errorf("AGENTKIT_TEST_LOGIN must be \"email:password\"")
	}
	return email, password, nil
}
