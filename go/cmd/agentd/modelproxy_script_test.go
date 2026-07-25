package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadMockModelScript is how a compose stack turns the offline mock into one
// that can call tools. The properties that matter: unset = nothing (so an
// unconfigured stack is untouched), a typo fails loudly rather than silently
// degrading to the canned response, and inline beats a file.
func TestLoadMockModelScript(t *testing.T) {
	valid := `{"rules":[{"match":"email-reviewer","turns":[
	  {"blocks":[{"type":"tool_use","name":"mcp__core__worker_prompt_write","input":{"worker":"email-answerer"}}]},
	  {"blocks":[{"type":"text","text":"done"}]}]}]}`

	tests := []struct {
		name      string
		inline    string
		file      string // written to a temp file; "" = do not set the FILE var
		wantNil   bool
		wantErr   string
		wantRules int
		wantTurns int
	}{
		{name: "neither set is no script", wantNil: true},
		{name: "blank inline is no script", inline: "   ", wantNil: true},
		{name: "inline script", inline: valid, wantRules: 1, wantTurns: 2},
		{name: "script from a file", file: valid, wantRules: 1, wantTurns: 2},
		{name: "inline wins over file", inline: `{"turns":[{"blocks":[{"type":"text","text":"inline"}]}]}`,
			file: valid, wantRules: 1, wantTurns: 1},
		{name: "malformed inline fails loudly", inline: `{"rules":`, wantErr: "AGENTKIT_MOCK_MODEL_SCRIPT"},
		{name: "malformed file fails loudly", file: `{"turns":[{"blocks":[{"type":"nope"}]}]}`,
			wantErr: "unknown type"},
		{name: "empty file fails loudly", file: "  ", wantErr: "file is empty"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(mockScriptEnv, tc.inline)
			if tc.file != "" {
				p := filepath.Join(t.TempDir(), "script.json")
				if err := os.WriteFile(p, []byte(tc.file), 0o600); err != nil {
					t.Fatalf("write script: %v", err)
				}
				t.Setenv(mockScriptFileEnv, p)
			} else {
				t.Setenv(mockScriptFileEnv, "")
			}

			got, err := loadMockModelScript()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want no script, got %+v", got)
				}
				return
			}
			if got == nil || len(got.Rules) != tc.wantRules {
				t.Fatalf("want %d rule(s), got %+v", tc.wantRules, got)
			}
			if len(got.Rules[0].Turns) != tc.wantTurns {
				t.Fatalf("want %d turn(s), got %d", tc.wantTurns, len(got.Rules[0].Turns))
			}
		})
	}
}

// A missing file named by the FILE variable is a configuration error, not a
// silent fallback: the test rig asked for a script and did not get one.
func TestLoadMockModelScript_MissingFile(t *testing.T) {
	t.Setenv(mockScriptEnv, "")
	t.Setenv(mockScriptFileEnv, filepath.Join(t.TempDir(), "absent.json"))
	if _, err := loadMockModelScript(); err == nil {
		t.Fatal("a missing script file must be an error")
	}
}

// The whole point of gap 1: without a script the mock model can never emit a
// tool_use, so no offline test can drive a worker into calling an MCP tool. The
// handler chosen with no script configured must be exactly the old one.
func TestNewModelProxyHandler_UnscriptedIsTheCannedMock(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(mockScriptEnv, "")
	t.Setenv(mockScriptFileEnv, "")
	if h := newModelProxyHandler(); h == nil {
		t.Fatal("no handler")
	}
	// The behavioural assertion lives with the handler
	// (modelproxy.TestScriptedMockHandler_NoScriptIsUnchanged); here we only
	// pin that the unscripted environment selects that path without error.
	table, err := loadMockModelScript()
	if err != nil || table != nil {
		t.Fatalf("unscripted env must yield no script: table=%+v err=%v", table, err)
	}
}
