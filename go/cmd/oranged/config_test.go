package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func noFiles(string) ([]byte, error) { return nil, fmt.Errorf("no files in this test") }

func TestResolveConfigDefaults(t *testing.T) {
	cfg, err := resolveConfig(env(map[string]string{"ANTHROPIC_API_KEY": "sk-test"}), noFiles)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Addr != ":8099" || cfg.DataDir != "./oranged-data" || cfg.Channel != "drafts" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	if cfg.TickInterval != 3*time.Minute || cfg.ConsultantInterval != 30*time.Minute {
		t.Fatalf("cadence defaults wrong: %+v", cfg)
	}
	if cfg.SpendCeilingUSD != 25 || cfg.MaxTokens != 4096 {
		t.Fatalf("budget defaults wrong: %+v", cfg)
	}
	if cfg.PlanTemplate != defaultPlanTemplate || cfg.SeedGuidance != defaultSeedGuidance {
		t.Fatalf("template defaults not applied")
	}
	if !strings.Contains(cfg.PlanTemplate, "{{fragment:routing-guidance}}") ||
		!strings.Contains(cfg.PlanTemplate, "{{input}}") {
		t.Fatalf("plan template must compose guidance + goal:\n%s", cfg.PlanTemplate)
	}
}

func TestResolveConfigOverrides(t *testing.T) {
	cfg, err := resolveConfig(env(map[string]string{
		"ANTHROPIC_API_KEY": "sk-test",
		"ADDR":              ":9000",
		"TICK_INTERVAL":     "30s",
		"SPEND_CEILING_USD": "2.5",
		"MAX_TOKENS":        "512",
		"CHANNEL":           "bluesky",
		"GOAL":              "grow the brand",
	}), noFiles)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Addr != ":9000" || cfg.TickInterval != 30*time.Second ||
		cfg.SpendCeilingUSD != 2.5 || cfg.MaxTokens != 512 ||
		cfg.Channel != "bluesky" || cfg.Goal != "grow the brand" {
		t.Fatalf("overrides lost: %+v", cfg)
	}
}

func TestResolveConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"bad duration", map[string]string{"ANTHROPIC_API_KEY": "k", "TICK_INTERVAL": "soon"}, "TICK_INTERVAL"},
		{"bad cli timeout", map[string]string{"CLI_TIMEOUT": "ages"}, "CLI_TIMEOUT"},
		{"bad float", map[string]string{"ANTHROPIC_API_KEY": "k", "SPEND_CEILING_USD": "lots"}, "SPEND_CEILING_USD"},
		{"bad int", map[string]string{"ANTHROPIC_API_KEY": "k", "MAX_TOKENS": "many"}, "MAX_TOKENS"},
		{"missing template file", map[string]string{"ANTHROPIC_API_KEY": "k", "PLAN_TEMPLATE_FILE": "/nope"}, "PLAN_TEMPLATE_FILE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveConfig(env(tc.env), noFiles)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestResolveConfigTemplateFileOverride(t *testing.T) {
	files := func(path string) ([]byte, error) {
		if path == "/tmpl/plan.txt" {
			return []byte("custom plan {{fragment:routing-guidance}} {{input}}"), nil
		}
		return nil, fmt.Errorf("unexpected read: %s", path)
	}
	cfg, err := resolveConfig(env(map[string]string{
		"ANTHROPIC_API_KEY":  "sk-test",
		"PLAN_TEMPLATE_FILE": "/tmpl/plan.txt",
	}), files)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasPrefix(cfg.PlanTemplate, "custom plan") {
		t.Fatalf("file override lost: %q", cfg.PlanTemplate)
	}
	if cfg.WorkerTemplate != defaultWorkerTemplate {
		t.Fatalf("unrelated template must keep its default")
	}
}
