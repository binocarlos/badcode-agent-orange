package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaude writes an executable stand-in for the claude binary that emits
// the given script's stdout (the headless JSON event array).
func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("fake claude: %v", err)
	}
	return path
}

const resultEvent = `[{"type":"system","subtype":"init"},` +
	`{"type":"result","subtype":"success","is_error":false,"result":"a fine draft",` +
	`"usage":{"input_tokens":10,"output_tokens":20}}]`

func TestCLIModelParsesResult(t *testing.T) {
	m := cliModel{bin: fakeClaude(t, `echo '`+resultEvent+`'`), model: "haiku"}
	out, usage, err := m.Run(context.Background(), "draft something")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "a fine draft" || usage.InputTokens != 10 || usage.OutputTokens != 20 {
		t.Fatalf("parsed wrong: out=%q usage=%+v", out, usage)
	}
}

// The adapter must strip ANTHROPIC_API_KEY from the subprocess env — with the
// key present the CLI would bill the API instead of the subscription.
func TestCLIModelStripsAPIKeyFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-leak")
	m := cliModel{bin: fakeClaude(t,
		`echo '[{"type":"result","subtype":"success","is_error":false,"result":"key='"${ANTHROPIC_API_KEY:-unset}"'","usage":{"input_tokens":1,"output_tokens":1}}]'`),
		model: "haiku"}
	out, _, err := m.Run(context.Background(), "x")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "key=unset" {
		t.Fatalf("API key leaked into the CLI subprocess: %q", out)
	}
}

func TestCLIModelErrorPaths(t *testing.T) {
	cases := []struct {
		name, script, want string
	}{
		{"is_error result",
			`echo '[{"type":"result","subtype":"error_during_execution","is_error":true,"result":"usage limit reached","usage":{"input_tokens":1,"output_tokens":0}}]'`,
			"usage limit reached"},
		{"no result event", `echo '[{"type":"system","subtype":"init"}]'`, "no result event"},
		{"junk output", `echo 'not json at all'`, "unparseable"},
		{"nonzero exit", `echo 'boom' >&2; exit 1`, "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := cliModel{bin: fakeClaude(t, tc.script), model: "haiku"}
			_, _, err := m.Run(context.Background(), "x")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCLIModelTimeout(t *testing.T) {
	// sleep runs as a grandchild holding the stdout pipe after the shell is
	// killed — WaitDelay must bound the wait rather than hanging out the full 30s.
	m := cliModel{bin: fakeClaude(t, `sleep 30; echo '`+resultEvent+`'`),
		model: "haiku", timeout: 100 * time.Millisecond}
	start := time.Now()
	_, _, err := m.Run(context.Background(), "x")
	if err == nil || time.Since(start) > 8*time.Second {
		t.Fatalf("timeout not enforced: err=%v after %s", err, time.Since(start))
	}
}

func TestResolveConfigBackendSelection(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
	}{
		{"key present defaults to api", map[string]string{"ANTHROPIC_API_KEY": "k"}, backendAPI, ""},
		{"no key defaults to cli", map[string]string{}, backendCLI, ""},
		{"explicit cli beats key", map[string]string{"ANTHROPIC_API_KEY": "k", "MODEL_BACKEND": "claude-cli"}, backendCLI, ""},
		{"explicit api without key errors", map[string]string{"MODEL_BACKEND": "api"}, "", "ANTHROPIC_API_KEY"},
		{"unknown backend errors", map[string]string{"MODEL_BACKEND": "gpt"}, "", "unknown backend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := resolveConfig(env(tc.env), noFiles)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error mentioning %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || cfg.Backend != tc.want {
				t.Fatalf("backend = %q err=%v, want %q", cfg.Backend, err, tc.want)
			}
		})
	}
	// CLI defaults ride along.
	cfg, err := resolveConfig(env(map[string]string{}), noFiles)
	if err != nil || cfg.CLIBin != "claude" || cfg.CLIModelFull != "opus" ||
		cfg.CLIModelMid != "sonnet" || cfg.CLIModelCheap != "haiku" || cfg.CLITimeout != 10*time.Minute {
		t.Fatalf("cli defaults wrong: %+v err=%v", cfg, err)
	}
}
