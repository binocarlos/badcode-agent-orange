// Login endpoints for the standalone stack: Google Sign-In and a fixed
// password login for tests. Both verify an identity, look the email up in the
// hard-coded email → projects map, and mint one project-scoped HS256 JWT per
// allowed project ("project" is the existing customer claim/column — a pure
// namespacing concept, no project table). apiAuthMiddleware verifies the minted
// tokens on its bearer-token path; nothing downstream changes.
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

// projectConfig is the per-project half of the object form: ops config for a
// project that a third-party application integrates with. Both fields are
// optional — a project with neither is just a namespace, exactly as before.
type projectConfig struct {
	// APIKeyEnv names the environment variable holding this project's API key.
	// The key value itself is never written in the map; only the variable name
	// is, so the map stays safe to commit and mount. Empty ⇒ no key (see T2).
	APIKeyEnv string `json:"api_key_env"`
	// AllowedOrigins lists the origins permitted to frame this project's embed
	// page. It drives Content-Security-Policy: frame-ancestors — not CORS; no
	// browser ever makes a cross-origin request to agentd by design.
	AllowedOrigins []string `json:"allowed_origins"`
}

// projectSettings is the whole parsed map file: who may log in, and per-project
// ops config. The flat legacy form parses into this with an empty projects half.
type projectSettings struct {
	users    projectMap
	projects map[string]projectConfig
}

// envVarName is a plausible environment variable name — the shell's own rule.
// Catching "WOLF-API-KEY" at boot beats discovering at runtime that a project
// silently has no key.
var envVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseProjectSettings decodes either form of the project map.
//
//	legacy: {"kai@badcode.dev": ["wolf", "demo"]}
//	object: {"users": {...}, "projects": {"wolf": {"api_key_env": …}}}
//
// The two are told apart by the *shape of the values*, not by key names: an
// email address is a perfectly legal JSON key and there is nothing structural
// stopping someone being called "users@…", so keys prove nothing. Legacy values
// are arrays; object-form values are objects. A file mixing both is an error
// rather than a guess.
func parseProjectSettings(raw []byte) (*projectSettings, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("project map: %w", err)
	}
	if len(probe) == 0 {
		return nil, fmt.Errorf("project map: empty")
	}
	var arrays, objects int
	for _, v := range probe {
		switch firstJSONToken(v) {
		case '[':
			arrays++
		case '{':
			objects++
		}
	}
	switch {
	case arrays > 0 && objects > 0:
		return nil, fmt.Errorf("project map: mixes the flat form (email → [projects]) with the object form ({\"users\": …, \"projects\": …}); use one or the other")
	case objects > 0:
		return parseProjectSettingsObjectForm(probe)
	default:
		users, err := parseUsers(raw)
		if err != nil {
			return nil, err
		}
		return &projectSettings{users: users, projects: map[string]projectConfig{}}, nil
	}
}

// firstJSONToken returns the first non-whitespace byte of a raw JSON value.
func firstJSONToken(v json.RawMessage) byte {
	for _, b := range v {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return b
		}
	}
	return 0
}

func parseProjectSettingsObjectForm(probe map[string]json.RawMessage) (*projectSettings, error) {
	for k := range probe {
		if k != "users" && k != "projects" {
			return nil, fmt.Errorf("project map: unknown top-level key %q (the object form takes only \"users\" and \"projects\")", k)
		}
	}
	out := &projectSettings{users: projectMap{}, projects: map[string]projectConfig{}}
	if rawUsers, ok := probe["users"]; ok {
		users, err := parseUsers(rawUsers)
		if err != nil {
			return nil, err
		}
		out.users = users
	}
	if rawProjects, ok := probe["projects"]; ok {
		var projects map[string]projectConfig
		if err := json.Unmarshal(rawProjects, &projects); err != nil {
			return nil, fmt.Errorf("project map: projects: %w", err)
		}
		for id, cfg := range projects {
			if !validProjectID.MatchString(id) || len(id) > 64 {
				return nil, fmt.Errorf("project map: project %q is not a valid project id (want kebab-case, e.g. apples-oranges)", id)
			}
			if cfg.APIKeyEnv != "" && !envVarName.MatchString(cfg.APIKeyEnv) {
				return nil, fmt.Errorf("project map: project %q: api_key_env %q is not a valid environment variable name", id, cfg.APIKeyEnv)
			}
			for _, origin := range cfg.AllowedOrigins {
				if err := validateOrigin(origin); err != nil {
					return nil, fmt.Errorf("project map: project %q: allowed_origins: %w", id, err)
				}
			}
			out.projects[id] = cfg
		}
	}
	// An object form that grants nobody a login and configures no project is a
	// mistake worth failing on, the same way an empty flat map is.
	if len(out.users) == 0 && len(out.projects) == 0 {
		return nil, fmt.Errorf("project map: empty (neither users nor projects)")
	}
	return out, nil
}

// parseUsers decodes the email → project-IDs map, lowercasing emails and
// rejecting entries that could silently grant nothing or everything. It is the
// whole of the legacy form and the "users" half of the object form.
func parseUsers(raw []byte) (projectMap, error) {
	var in map[string][]string
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("project map: %w", err)
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

// validateOrigin accepts scheme://host[:port] and nothing else. Origins land in
// a CSP frame-ancestors list, where a path is meaningless and a wildcard would
// let anyone frame the project — so both are refused rather than trimmed.
// Plain http is allowed only for loopback, which is where a dev server lives.
func validateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("%q is not a URL: %w", origin, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%q must be an absolute origin, e.g. https://wolf.badcode.dev", origin)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%q must be scheme://host[:port] with no path, query or fragment", origin)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return fmt.Errorf("%q must use https (plain http is allowed only for localhost)", origin)
		}
	default:
		return fmt.Errorf("%q has scheme %q; want https", origin, u.Scheme)
	}
	return nil
}

// parseProjectMap decodes either form and returns just the user→projects half —
// what the login handlers need. Its signature is unchanged from before the
// object form existed.
func parseProjectMap(raw []byte) (projectMap, error) {
	s, err := parseProjectSettings(raw)
	if err != nil {
		return nil, err
	}
	return s.users, nil
}

// loadProjectSettings reads the map from AGENTKIT_PROJECT_MAP (inline JSON,
// wins) or AGENTKIT_PROJECT_MAP_FILE (path to a mounted JSON file).
func loadProjectSettings(getenv func(string) string) (*projectSettings, error) {
	if inline := getenv("AGENTKIT_PROJECT_MAP"); inline != "" {
		return parseProjectSettings([]byte(inline))
	}
	if path := getenv("AGENTKIT_PROJECT_MAP_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("project map file: %w", err)
		}
		return parseProjectSettings(raw)
	}
	return nil, fmt.Errorf("no project map: set AGENTKIT_PROJECT_MAP or AGENTKIT_PROJECT_MAP_FILE")
}

// loadProjectSettingsOptional is loadProjectSettings but tolerant of the map
// being absent entirely: it returns (nil, nil) when neither env var is set. The
// zero-config demo has no map at all, and it must still boot.
func loadProjectSettingsOptional(getenv func(string) string) (*projectSettings, error) {
	if getenv("AGENTKIT_PROJECT_MAP") == "" && getenv("AGENTKIT_PROJECT_MAP_FILE") == "" {
		return nil, nil
	}
	return loadProjectSettings(getenv)
}

// loadProjectMap is loadProjectSettings' user-half shorthand.
func loadProjectMap(getenv func(string) string) (projectMap, error) {
	s, err := loadProjectSettings(getenv)
	if err != nil {
		return nil, err
	}
	return s.users, nil
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
// credMode is the model credential agentd booted with (mock | api-key |
// subscription, from credentialMode in modelproxy.go). It rides this payload
// because it is the same kind of fact as the login modes — runtime truth the
// browser cannot infer — and because the mock is the DEFAULT: a stack whose
// credential lines are blank produces plausible canned output everywhere, and
// the UI has to be able to say so (RD18).
func authConfigHandler(googleClientID string, passwordLogin bool, credMode string) http.HandlerFunc {
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
			"credential_mode":  credMode,
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
