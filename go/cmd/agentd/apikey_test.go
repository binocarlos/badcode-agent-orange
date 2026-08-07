package main

import (
	"strings"
	"testing"
)

// goodKey is a 32-char value — comfortably over minAPIKeyLen.
const goodKey = "0123456789abcdef0123456789abcdef"

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestAPIKeyResolution(t *testing.T) {
	cfgs := map[string]projectConfig{
		"wolf": {APIKeyEnv: "WOLF_API_KEY", AllowedOrigins: []string{"https://wolf.badcode.dev"}},
		"demo": {APIKeyEnv: "DEMO_API_KEY"},
		"bare": {}, // no key, no origins — just a namespace
	}
	keys, err := newProjectKeys(cfgs, envFrom(map[string]string{
		"WOLF_API_KEY": goodKey,
		"DEMO_API_KEY": "fedcba9876543210fedcba9876543210",
	}), nil)
	if err != nil {
		t.Fatalf("newProjectKeys: %v", err)
	}

	if p, ok := keys.ProjectForKey(goodKey); !ok || p != "wolf" {
		t.Fatalf("ProjectForKey(wolf key) = %q, %v", p, ok)
	}
	if p, ok := keys.ProjectForKey("fedcba9876543210fedcba9876543210"); !ok || p != "demo" {
		t.Fatalf("ProjectForKey(demo key) = %q, %v", p, ok)
	}
	for _, bad := range []string{"", "nope", goodKey + "x", strings.ToUpper(goodKey)} {
		if p, ok := keys.ProjectForKey(bad); ok {
			t.Fatalf("ProjectForKey(%q) unexpectedly granted %q", bad, p)
		}
	}
	if got := keys.AllowedOrigins("wolf"); len(got) != 1 || got[0] != "https://wolf.badcode.dev" {
		t.Fatalf("AllowedOrigins(wolf) = %v", got)
	}
	if got := keys.AllowedOrigins("demo"); got != nil {
		t.Fatalf("AllowedOrigins(demo) = %v, want nil", got)
	}
	if got := keys.AllowedOrigins("stranger"); got != nil {
		t.Fatalf("AllowedOrigins(stranger) = %v, want nil", got)
	}
	if !keys.hasKeys() {
		t.Fatal("hasKeys() = false with two keys configured")
	}
}

// A key is trimmed on the way in: mounting one from a file or a compose env_file
// routinely appends a newline, and debugging that through a constant-time
// compare is miserable.
func TestAPIKeyIsTrimmed(t *testing.T) {
	keys, err := newProjectKeys(
		map[string]projectConfig{"wolf": {APIKeyEnv: "WOLF_API_KEY"}},
		envFrom(map[string]string{"WOLF_API_KEY": "  " + goodKey + "\n"}), nil)
	if err != nil {
		t.Fatalf("newProjectKeys: %v", err)
	}
	if p, ok := keys.ProjectForKey(goodKey); !ok || p != "wolf" {
		t.Fatalf("ProjectForKey = %q, %v", p, ok)
	}
}

// A project whose env var is unset or empty simply has no key. That is the
// normal state of most projects and must not fail the boot — but it is logged,
// because "I set the key and it doesn't work" is otherwise silent.
func TestAPIKeyMissingEnvVarIsNotAnError(t *testing.T) {
	var logged []string
	keys, err := newProjectKeys(
		map[string]projectConfig{"wolf": {APIKeyEnv: "WOLF_API_KEY"}, "quiet": {}},
		envFrom(map[string]string{}),
		func(format string, args ...any) { logged = append(logged, format) })
	if err != nil {
		t.Fatalf("newProjectKeys: %v", err)
	}
	if keys.hasKeys() {
		t.Fatal("hasKeys() = true with no key values in the environment")
	}
	if _, ok := keys.ProjectForKey(goodKey); ok {
		t.Fatal("an unconfigured key granted a project")
	}
	if !strings.Contains(strings.Join(logged, "\n"), "unset or empty") {
		t.Fatalf("the empty env var was not logged: %v", logged)
	}
}

func TestAPIKeyBootErrors(t *testing.T) {
	tests := []struct {
		name    string
		cfgs    map[string]projectConfig
		env     map[string]string
		wantErr string
	}{
		{
			name:    "key below the length floor",
			cfgs:    map[string]projectConfig{"wolf": {APIKeyEnv: "WOLF_API_KEY"}},
			env:     map[string]string{"WOLF_API_KEY": "short"},
			wantErr: "at least 24",
		},
		{
			name:    "key one character below the floor",
			cfgs:    map[string]projectConfig{"wolf": {APIKeyEnv: "WOLF_API_KEY"}},
			env:     map[string]string{"WOLF_API_KEY": strings.Repeat("k", minAPIKeyLen-1)},
			wantErr: "at least 24",
		},
		{
			name: "two projects sharing a key value",
			cfgs: map[string]projectConfig{
				"wolf": {APIKeyEnv: "WOLF_API_KEY"},
				"demo": {APIKeyEnv: "DEMO_API_KEY"},
			},
			env:     map[string]string{"WOLF_API_KEY": goodKey, "DEMO_API_KEY": goodKey},
			wantErr: "same API key value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newProjectKeys(tt.cfgs, envFrom(tt.env), nil)
			if err == nil {
				t.Fatal("expected a boot error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// Exactly at the floor is fine — the check is "at least", not "more than".
func TestAPIKeyAtTheLengthFloorIsAccepted(t *testing.T) {
	key := strings.Repeat("k", minAPIKeyLen)
	keys, err := newProjectKeys(
		map[string]projectConfig{"wolf": {APIKeyEnv: "WOLF_API_KEY"}},
		envFrom(map[string]string{"WOLF_API_KEY": key}), nil)
	if err != nil {
		t.Fatalf("newProjectKeys: %v", err)
	}
	if p, _ := keys.ProjectForKey(key); p != "wolf" {
		t.Fatalf("ProjectForKey = %q", p)
	}
}

// A deployment with no project map at all (the zero-config demo) must still get
// a usable, empty index rather than a nil-pointer panic on the first request.
func TestAPIKeyIndexToleratesNoProjectMap(t *testing.T) {
	keys, err := newProjectKeys(projectConfigsOf(nil), envFrom(nil), nil)
	if err != nil {
		t.Fatalf("newProjectKeys: %v", err)
	}
	if keys.hasKeys() {
		t.Fatal("hasKeys() = true for an empty index")
	}
	if _, ok := keys.ProjectForKey(goodKey); ok {
		t.Fatal("an empty index granted a project")
	}
	if got := keys.AllowedOrigins("wolf"); got != nil {
		t.Fatalf("AllowedOrigins = %v", got)
	}
	// And the nil index itself, which is what a construction failure leaves.
	var nilIdx *projectKeyIndex
	if _, ok := nilIdx.ProjectForKey(goodKey); ok || nilIdx.hasKeys() || nilIdx.AllowedOrigins("wolf") != nil {
		t.Fatal("a nil index is not inert")
	}
}

// The interface the rest of agentd depends on is satisfied by the concrete type.
var _ projectKeys = (*projectKeyIndex)(nil)
