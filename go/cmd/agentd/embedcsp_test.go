package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFrameAncestorsValue(t *testing.T) {
	cases := []struct {
		name    string
		origins []string
		want    string
	}{
		{
			// The acceptance criterion's default: nothing configured must deny
			// framing outright, not fall back to "no header".
			name:    "no origins configured",
			origins: nil,
			want:    "frame-ancestors 'none'",
		},
		{
			name:    "empty and blank entries are the same as none",
			origins: []string{"", "   "},
			want:    "frame-ancestors 'none'",
		},
		{
			name:    "one project",
			origins: []string{"https://wolf.badcode.dev"},
			want:    "frame-ancestors https://wolf.badcode.dev",
		},
		{
			// Two projects framing from the same origin must not produce it
			// twice, and the order must not depend on map iteration.
			name:    "union is deduped and sorted",
			origins: []string{"https://wolf.badcode.dev", "http://localhost:5173", "https://wolf.badcode.dev"},
			want:    "frame-ancestors http://localhost:5173 https://wolf.badcode.dev",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := frameAncestors(tc.origins); got != tc.want {
				t.Fatalf("frameAncestors(%v) = %q, want %q", tc.origins, got, tc.want)
			}
		})
	}
}

// The route is what nginx's auth_request talks to: it must answer 2xx (or the
// embed page never renders) and carry the header nginx copies.
func TestEmbedCSPRouteAnswersTheHeader(t *testing.T) {
	mux := http.NewServeMux()
	registerEmbedCSP(mux, []string{"https://wolf.badcode.dev"})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, embedCSPPath, nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors https://wolf.badcode.dev" {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
}

// A deployment with no project map at all still gets a working (denying)
// answer rather than a 404 that nginx would turn into a 500 on the embed page.
func TestEmbedCSPRouteWithNoProjectsDeniesFraming(t *testing.T) {
	mux := http.NewServeMux()
	var keys *projectKeyIndex // exactly what newProjectKeys yields for an absent map
	registerEmbedCSP(mux, keys.allowedOrigins())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, embedCSPPath, nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Fatalf("Content-Security-Policy = %q, want frame-ancestors 'none'", got)
	}
}

// The union walks every configured project, not just the ones that have a key:
// a project can be framable without having an API key of its own.
func TestAllowedOriginsUnionsEveryProject(t *testing.T) {
	keys, err := newProjectKeys(map[string]projectConfig{
		"wolf":  {APIKeyEnv: "WOLF_API_KEY", AllowedOrigins: []string{"https://wolf.badcode.dev"}},
		"demo":  {AllowedOrigins: []string{"http://localhost:5173"}},
		"plain": {},
	}, envFrom(map[string]string{"WOLF_API_KEY": goodKey}), nil)
	if err != nil {
		t.Fatalf("newProjectKeys: %v", err)
	}
	if got := frameAncestors(keys.allowedOrigins()); got != "frame-ancestors http://localhost:5173 https://wolf.badcode.dev" {
		t.Fatalf("frameAncestors(union) = %q", got)
	}
}
