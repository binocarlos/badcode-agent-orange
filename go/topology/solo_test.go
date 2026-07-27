package topology

import (
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func soloV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("solo", "v1")
	if !ok {
		t.Fatal("solo@v1 not registered")
	}
	return top
}

// The reference render: defaults everywhere except the prompt seed. One
// worker, one schedule, nothing else — solo is the control topology and its
// bundle must stay exactly that small.
func TestSoloRenderDefaults(t *testing.T) {
	b, err := soloV1(t).Instantiate(Answers{
		SoloQuestionPromptSeed: "Write one haiku about containers.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 1 {
		t.Fatalf("workers: want 1, got %d", len(b.Workers))
	}
	w := b.Workers[0]
	if w.Name != "solo" {
		t.Errorf("worker name: want solo (the default), got %q", w.Name)
	}
	if w.SystemPrompt != "Write one haiku about containers." {
		t.Errorf("system prompt: want the seed verbatim, got %q", w.SystemPrompt)
	}
	if !w.Enabled {
		t.Error("worker must render enabled")
	}
	if w.Frozen {
		t.Error("solo's worker must not render frozen")
	}
	if w.MaxInstances != agentdb.DefaultMaxInstances {
		t.Errorf("max_instances: want %d, got %d", agentdb.DefaultMaxInstances, w.MaxInstances)
	}
	if w.Description == "" {
		t.Error("worker description must not be empty")
	}

	if len(b.Schedules) != 1 {
		t.Fatalf("schedules: want 1, got %d", len(b.Schedules))
	}
	s := b.Schedules[0]
	if s.Worker != w.Name {
		t.Errorf("schedule worker: want %q, got %q", w.Name, s.Worker)
	}
	if s.Cron != "0 9 * * *" {
		t.Errorf("cron: want the daily default, got %q", s.Cron)
	}
	if s.Input == "" {
		t.Error("schedule input must not be empty — a schedule says what the worker is told")
	}
	if !s.Enabled {
		t.Error("schedule must render enabled")
	}

	if len(b.Subscriptions) != 0 {
		t.Errorf("subscriptions: want none, got %d", len(b.Subscriptions))
	}
	if b.SettingsPatch != nil {
		t.Error("solo must not patch project settings")
	}
	if len(b.MemorySeeds) != 0 {
		t.Errorf("memory seeds: want none, got %d", len(b.MemorySeeds))
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// Every cadence maps to the pinned cron string, and every one of those strings
// parses with the SAME parser the schedule store validates with — a cadence
// this table emits can never be refused at apply time.
func TestSoloCadences(t *testing.T) {
	tests := []struct {
		cadence string
		want    string
	}{
		{"hourly", "0 * * * *"},
		{"daily", "0 9 * * *"},
		{"weekly", "0 9 * * 1"},
	}
	for _, tc := range tests {
		t.Run(tc.cadence, func(t *testing.T) {
			b, err := soloV1(t).Instantiate(Answers{
				SoloQuestionPromptSeed: "p",
				SoloQuestionCadence:    tc.cadence,
			})
			if err != nil {
				t.Fatalf("instantiate: %v", err)
			}
			if got := b.Schedules[0].Cron; got != tc.want {
				t.Fatalf("cron: want %q, got %q", tc.want, got)
			}
			if _, err := agentdb.ParseCron(b.Schedules[0].Cron); err != nil {
				t.Fatalf("rendered cron %q does not parse: %v", b.Schedules[0].Cron, err)
			}
		})
	}
}

// An unlisted cadence dies in answer validation, before the renderer.
func TestSoloUnknownCadenceRefused(t *testing.T) {
	_, err := soloV1(t).Instantiate(Answers{
		SoloQuestionPromptSeed: "p",
		SoloQuestionCadence:    "fortnightly",
	})
	if !errors.Is(err, ErrBadAnswers) {
		t.Fatalf("want ErrBadAnswers, got %v", err)
	}
}

// Semantic checks the renderer owns: worker identity and a non-blank prompt.
func TestSoloRenderRefusals(t *testing.T) {
	tests := []struct {
		name    string
		answers Answers
		wantErr string
	}{
		{
			name:    "missing prompt seed",
			answers: Answers{},
			wantErr: `question "prompt-seed" is required`,
		},
		{
			name: "blank prompt seed",
			answers: Answers{
				SoloQuestionPromptSeed: "   \n",
			},
			wantErr: "prompt-seed must not be blank",
		},
		{
			name: "non-kebab worker name",
			answers: Answers{
				SoloQuestionWorkerName: "Solo Worker",
				SoloQuestionPromptSeed: "p",
			},
			wantErr: "not kebab-case",
		},
		{
			name: "empty worker name",
			answers: Answers{
				SoloQuestionWorkerName: "",
				SoloQuestionPromptSeed: "p",
			},
			wantErr: "name is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := soloV1(t).Instantiate(tc.answers)
			if err == nil {
				t.Fatalf("want error containing %q, got a bundle", tc.wantErr)
			}
			if !errors.Is(err, ErrBadAnswers) && !errors.Is(err, ErrRender) {
				t.Fatalf("error wraps neither ErrBadAnswers nor ErrRender: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// A custom worker name flows into both the worker row and the schedule row —
// they can never drift apart.
func TestSoloCustomWorkerName(t *testing.T) {
	b, err := soloV1(t).Instantiate(Answers{
		SoloQuestionWorkerName: "daily-writer",
		SoloQuestionPromptSeed: "p",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if b.Workers[0].Name != "daily-writer" {
		t.Fatalf("worker name: want daily-writer, got %q", b.Workers[0].Name)
	}
	if b.Schedules[0].Worker != "daily-writer" {
		t.Fatalf("schedule worker: want daily-writer, got %q", b.Schedules[0].Worker)
	}
}
