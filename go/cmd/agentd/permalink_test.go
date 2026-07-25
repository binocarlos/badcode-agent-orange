package main

import "testing"

func TestResolvePublicBaseURL(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", nil, "http://localhost:8080"},
		{"empty value falls back to default", map[string]string{"AGENTKIT_PUBLIC_BASE_URL": ""}, "http://localhost:8080"},
		{"https origin", map[string]string{"AGENTKIT_PUBLIC_BASE_URL": "https://orange.example.com"}, "https://orange.example.com"},
		{"trailing slash trimmed", map[string]string{"AGENTKIT_PUBLIC_BASE_URL": "https://orange.example.com/"}, "https://orange.example.com"},
		{"many trailing slashes trimmed", map[string]string{"AGENTKIT_PUBLIC_BASE_URL": "https://orange.example.com///"}, "https://orange.example.com"},
		{"surrounding space trimmed", map[string]string{"AGENTKIT_PUBLIC_BASE_URL": "  https://orange.example.com  "}, "https://orange.example.com"},
		{"sub-path preserved", map[string]string{"AGENTKIT_PUBLIC_BASE_URL": "https://example.com/agents"}, "https://example.com/agents"},
		{"port preserved", map[string]string{"AGENTKIT_PUBLIC_BASE_URL": "http://10.0.0.4:8080"}, "http://10.0.0.4:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := resolvePublicBaseURL(envMap(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if p.BaseURL() != tc.want {
				t.Fatalf("BaseURL() = %q, want %q", p.BaseURL(), tc.want)
			}
		})
	}
}

func TestResolvePublicBaseURL_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"no scheme", "orange.example.com"},
		{"relative path", "/agents"},
		{"non-http scheme", "ftp://orange.example.com"},
		{"scheme only", "https://"},
		{"carries query", "https://orange.example.com?x=1"},
		{"carries fragment", "https://orange.example.com#top"},
		{"only slashes", "///"},
		{"unparseable", "http://[::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolvePublicBaseURL(envMap(map[string]string{
				"AGENTKIT_PUBLIC_BASE_URL": tc.value,
			})); err == nil {
				t.Fatalf("expected error for %q", tc.value)
			}
		})
	}
}

// The format is the contract with web/src/permalink.ts — these expectations
// mirror its permalink.test.ts table.
func TestPermalinkerSessionURL(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		project string
		session string
		want    string
	}{
		{"default base", "", "acme", "sess-1", "http://localhost:8080/p/acme/s/sess-1"},
		{"custom origin", "https://orange.example.com", "acme", "sess-1", "https://orange.example.com/p/acme/s/sess-1"},
		{"sub-path mount", "https://example.com/agents", "acme", "sess-1", "https://example.com/agents/p/acme/s/sess-1"},
		{"uuid session", "https://o.example.com", "p1", "9f1c2d3e-0000-4a5b-8c7d-000000000001",
			"https://o.example.com/p/p1/s/9f1c2d3e-0000-4a5b-8c7d-000000000001"},
		{"escapes slashes", "https://o.example.com", "a/b", "c/d", "https://o.example.com/p/a%2Fb/s/c%2Fd"},
		{"escapes spaces", "https://o.example.com", "a b", "c d", "https://o.example.com/p/a%20b/s/c%20d"},
		{"escapes query chars", "https://o.example.com", "p?x", "s#y", "https://o.example.com/p/p%3Fx/s/s%23y"},
		{"empty session id → no link", "https://o.example.com", "acme", "", ""},
		{"empty project id → no link", "https://o.example.com", "", "sess-1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{}
			if tc.base != "" {
				env["AGENTKIT_PUBLIC_BASE_URL"] = tc.base
			}
			p, err := resolvePublicBaseURL(envMap(env))
			if err != nil {
				t.Fatal(err)
			}
			if got := p.SessionURL(tc.project, tc.session); got != tc.want {
				t.Fatalf("SessionURL(%q, %q) = %q, want %q", tc.project, tc.session, got, tc.want)
			}
		})
	}
}
