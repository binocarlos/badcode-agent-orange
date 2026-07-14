package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// cliModel satisfies orchestrator.Model by driving the Claude Code CLI in its
// headless print mode (`claude -p --output-format json`) — the supported
// non-interactive surface — so the daemon runs on the user's Claude
// subscription instead of pay-per-token API billing. A host-side adapter like
// the consultant composition: the engine seam is untouched.
//
// ANTHROPIC_API_KEY is stripped from the subprocess env deliberately: when the
// key is present the CLI bills the API instead of the subscription.
type cliModel struct {
	bin     string        // the claude binary ("claude")
	model   string        // per-tier model alias or full id ("opus", "sonnet", "haiku")
	timeout time.Duration // per-call ceiling; 0 = no extra timeout
}

func (m cliModel) Run(ctx context.Context, prompt string) (string, orchestrator.Usage, error) {
	if m.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, m.bin, "-p", "--model", m.model, "--output-format", "json")
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = envWithoutAPIKey(os.Environ())
	// On ctx cancel the CLI is killed, but grandchildren can keep the stdout
	// pipe open — don't let a wedged call hang the tick forever.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", orchestrator.Usage{}, fmt.Errorf("claude -p (%s): %w: %s",
				m.model, err, firstLine(string(ee.Stderr)))
		}
		return "", orchestrator.Usage{}, fmt.Errorf("claude -p (%s): %w", m.model, err)
	}
	return parseCLIResult(out)
}

var _ orchestrator.Model = cliModel{}

// parseCLIResult reads the headless-mode event array and returns the terminal
// "result" event's text + token usage.
func parseCLIResult(out []byte) (string, orchestrator.Usage, error) {
	var events []struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
		Usage   struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &events); err != nil {
		return "", orchestrator.Usage{}, fmt.Errorf("claude -p: unparseable output: %w: %s",
			err, firstLine(string(out)))
	}
	for _, e := range events {
		if e.Type != "result" {
			continue
		}
		usage := orchestrator.Usage{InputTokens: e.Usage.InputTokens, OutputTokens: e.Usage.OutputTokens}
		if e.IsError {
			return "", usage, fmt.Errorf("claude -p: %s: %s", e.Subtype, firstLine(e.Result))
		}
		return e.Result, usage, nil
	}
	return "", orchestrator.Usage{}, fmt.Errorf("claude -p: no result event in output")
}

func envWithoutAPIKey(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
