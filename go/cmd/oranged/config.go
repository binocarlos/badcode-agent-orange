package main

import (
	"fmt"
	"strconv"
	"time"
)

// The two model backends: direct API calls (pay-per-token) or the Claude Code
// CLI in headless print mode (the user's Claude subscription).
const (
	backendAPI = "api"
	backendCLI = "claude-cli"
)

// Config is oranged's resolved configuration. Resolution is pure — getenv and
// readFile are injected — so it table-tests without touching the process env
// (the cmd/agentd/backends.go house idiom).
type Config struct {
	Backend   string // backendAPI | backendCLI
	APIKey    string
	Addr      string
	DataDir   string
	Goal      string // initial goal; used only when the board has none yet
	AuthToken string // shared bearer token; "" disables the guard (local dev)
	Channel   string // stamped onto publish-disposition posts

	// claude-cli backend (per-tier model aliases; the CLI accepts "opus" etc.)
	CLIBin                                   string
	CLIModelFull, CLIModelMid, CLIModelCheap string
	CLITimeout                               time.Duration

	TickInterval       time.Duration
	ConsultantInterval time.Duration
	SpendCeilingUSD    float64
	MaxTokens          int

	PlanTemplate      string
	WorkerTemplate    string
	SeedGuidance      string
	ConsultantCharter string
}

func resolveConfig(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	cfg := Config{
		APIKey:            getenv("ANTHROPIC_API_KEY"),
		Addr:              getOr(getenv, "ADDR", ":8099"),
		DataDir:           getOr(getenv, "DATA_DIR", "./oranged-data"),
		Goal:              getenv("GOAL"),
		AuthToken:         getenv("AUTH_TOKEN"),
		Channel:           getOr(getenv, "CHANNEL", "drafts"),
		CLIBin:            getOr(getenv, "CLAUDE_BIN", "claude"),
		CLIModelFull:      getOr(getenv, "CLI_MODEL_FULL", "opus"),
		CLIModelMid:       getOr(getenv, "CLI_MODEL_MID", "sonnet"),
		CLIModelCheap:     getOr(getenv, "CLI_MODEL_CHEAP", "haiku"),
		PlanTemplate:      defaultPlanTemplate,
		WorkerTemplate:    defaultWorkerTemplate,
		SeedGuidance:      defaultSeedGuidance,
		ConsultantCharter: defaultConsultantCharter,
	}
	// Backend: explicit MODEL_BACKEND wins; otherwise the API key decides —
	// key present → api, absent → the subscription-backed CLI.
	switch b := getenv("MODEL_BACKEND"); b {
	case backendAPI, backendCLI:
		cfg.Backend = b
	case "":
		if cfg.APIKey != "" {
			cfg.Backend = backendAPI
		} else {
			cfg.Backend = backendCLI
		}
	default:
		return Config{}, fmt.Errorf("MODEL_BACKEND: unknown backend %q (want %q or %q)", b, backendAPI, backendCLI)
	}
	if cfg.Backend == backendAPI && cfg.APIKey == "" {
		return Config{}, fmt.Errorf("ANTHROPIC_API_KEY is required for the %s backend "+
			"(or set MODEL_BACKEND=%s to run on your Claude subscription via the claude CLI)",
			backendAPI, backendCLI)
	}
	var err error
	if cfg.CLITimeout, err = durationOr(getenv, "CLI_TIMEOUT", 10*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.TickInterval, err = durationOr(getenv, "TICK_INTERVAL", 3*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.ConsultantInterval, err = durationOr(getenv, "CONSULTANT_INTERVAL", 30*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.SpendCeilingUSD, err = floatOr(getenv, "SPEND_CEILING_USD", 25); err != nil {
		return Config{}, err
	}
	if cfg.MaxTokens, err = intOr(getenv, "MAX_TOKENS", 4096); err != nil {
		return Config{}, err
	}
	// Multiline prompt text comes from files, not env values.
	for _, o := range []struct {
		env string
		dst *string
	}{
		{"PLAN_TEMPLATE_FILE", &cfg.PlanTemplate},
		{"WORKER_TEMPLATE_FILE", &cfg.WorkerTemplate},
		{"SEED_GUIDANCE_FILE", &cfg.SeedGuidance},
		{"CONSULTANT_CHARTER_FILE", &cfg.ConsultantCharter},
	} {
		if path := getenv(o.env); path != "" {
			b, err := readFile(path)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", o.env, err)
			}
			*o.dst = string(b)
		}
	}
	return cfg, nil
}

func getOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func durationOr(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func floatOr(getenv func(string) string, key string, def float64) (float64, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}

func intOr(getenv func(string) string, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
