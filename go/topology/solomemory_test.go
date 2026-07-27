package topology

import (
	"errors"
	"strings"
	"testing"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func soloMemoryV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("solo-memory", "v1")
	if !ok {
		t.Fatal("solo-memory@v1 not registered")
	}
	return top
}

// The reference render: solo's shape — one worker, one schedule, no edges —
// plus the memory channel: a briefing selector on the worker row and the
// write/read discipline in the prompt.
func TestSoloMemoryRenderDefaults(t *testing.T) {
	b, err := soloMemoryV1(t).Instantiate(Answers{
		SoloMemoryQuestionPromptSeed: "Keep a tiny daily log of the orchard.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 1 {
		t.Fatalf("workers: want 1, got %d", len(b.Workers))
	}
	w := b.Workers[0]
	if w.Name != "solo-memory" {
		t.Errorf("worker name: want solo-memory (the default), got %q", w.Name)
	}
	if !w.Enabled {
		t.Error("worker must render enabled")
	}
	if w.Frozen {
		t.Error("solo-memory's worker must not render frozen")
	}
	if w.MaxInstances != agentdb.DefaultMaxInstances {
		t.Errorf("max_instances: want %d, got %d", agentdb.DefaultMaxInstances, w.MaxInstances)
	}

	// The read side of the channel: the briefing selector for the default label.
	wantSel := agentdb.SelectorList{"kind=task-notes"}
	if len(w.Briefing) != 1 || w.Briefing[0] != wantSel[0] {
		t.Errorf("briefing: want %v, got %v", wantSel, w.Briefing)
	}

	// The write side: the prompt keeps the seed verbatim at the top and names
	// the real tool and the real label.
	if !strings.HasPrefix(w.SystemPrompt, "Keep a tiny daily log of the orchard.") {
		t.Errorf("prompt must open with the seed verbatim; got:\n%s", w.SystemPrompt)
	}
	for _, want := range []string{"memory_create", "kind=task-notes"} {
		if !strings.Contains(w.SystemPrompt, want) {
			t.Errorf("prompt must contain %q; got:\n%s", want, w.SystemPrompt)
		}
	}

	// Solo's clock, unchanged: one daily schedule, non-empty input.
	if len(b.Schedules) != 1 {
		t.Fatalf("schedules: want 1, got %d", len(b.Schedules))
	}
	s := b.Schedules[0]
	if s.Worker != w.Name || s.Cron != "0 9 * * *" || s.Input == "" || !s.Enabled {
		t.Errorf("schedule: want enabled daily for %q with input, got %+v", w.Name, s)
	}
	if _, err := agentdb.ParseCron(s.Cron); err != nil {
		t.Fatalf("rendered cron %q does not parse: %v", s.Cron, err)
	}

	if len(b.Subscriptions) != 0 {
		t.Errorf("subscriptions: want none (solo-memory is still a control), got %d", len(b.Subscriptions))
	}
	if b.SettingsPatch != nil || len(b.MemorySeeds) != 0 {
		t.Error("solo-memory must not patch settings or seed memories — the board starts empty")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// The prompt describes the briefing section by the words compose.go really
// uses — DefaultBriefingHeading + ": " + selector. If the composition layer
// ever renamed the heading, this test is the tripwire that keeps the prompt
// from teaching the model to look for a section that no longer exists.
func TestSoloMemoryPromptNamesTheRealBriefingHeading(t *testing.T) {
	b, err := soloMemoryV1(t).Instantiate(Answers{
		SoloMemoryQuestionPromptSeed:  "p",
		SoloMemoryQuestionMemoryLabel: "lessons",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	want := agentkit.DefaultBriefingHeading + ": kind=lessons"
	if !strings.Contains(b.Workers[0].SystemPrompt, want) {
		t.Fatalf("prompt must name the real briefing heading %q; got:\n%s", want, b.Workers[0].SystemPrompt)
	}
}

// A custom label flows into BOTH ends of the channel — the prompt's
// memory_create instruction and the briefing selector can never drift apart.
func TestSoloMemoryCustomLabel(t *testing.T) {
	b, err := soloMemoryV1(t).Instantiate(Answers{
		SoloMemoryQuestionPromptSeed:  "p",
		SoloMemoryQuestionMemoryLabel: "orchard-log",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	w := b.Workers[0]
	if len(w.Briefing) != 1 || w.Briefing[0] != "kind=orchard-log" {
		t.Fatalf("briefing: want [kind=orchard-log], got %v", w.Briefing)
	}
	if !strings.Contains(w.SystemPrompt, "kind=orchard-log") {
		t.Fatalf("prompt must carry the custom label; got:\n%s", w.SystemPrompt)
	}
}

// Semantic refusals the renderer owns — including the label, which rides into
// a selector and must be a legal label value.
func TestSoloMemoryRenderRefusals(t *testing.T) {
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
			name:    "blank prompt seed",
			answers: Answers{SoloMemoryQuestionPromptSeed: "  \n"},
			wantErr: "prompt-seed must not be blank",
		},
		{
			name: "non-kebab worker name",
			answers: Answers{
				SoloMemoryQuestionWorkerName: "Solo Memory",
				SoloMemoryQuestionPromptSeed: "p",
			},
			wantErr: "not kebab-case",
		},
		{
			name: "label with a space",
			answers: Answers{
				SoloMemoryQuestionPromptSeed:  "p",
				SoloMemoryQuestionMemoryLabel: "task notes",
			},
			wantErr: "is invalid",
		},
		{
			name: "label smuggling a selector comma",
			answers: Answers{
				SoloMemoryQuestionPromptSeed:  "p",
				SoloMemoryQuestionMemoryLabel: "a,worker=other",
			},
			wantErr: "is invalid",
		},
		{
			name: "blank label",
			answers: Answers{
				SoloMemoryQuestionPromptSeed:  "p",
				SoloMemoryQuestionMemoryLabel: "   ",
			},
			wantErr: "memory-label must not be blank",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := soloMemoryV1(t).Instantiate(tc.answers)
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

// The control-pair property: solo-memory's clock is byte-for-byte solo's —
// same cadence table, same schedule input — so comparing the two topologies
// varies the memory channel and nothing else.
func TestSoloMemoryClockMatchesSolo(t *testing.T) {
	for _, cadence := range []string{"hourly", "daily", "weekly"} {
		plain, err := soloV1(t).Instantiate(Answers{
			SoloQuestionPromptSeed: "p",
			SoloQuestionCadence:    cadence,
		})
		if err != nil {
			t.Fatalf("solo instantiate: %v", err)
		}
		withMem, err := soloMemoryV1(t).Instantiate(Answers{
			SoloMemoryQuestionPromptSeed: "p",
			SoloMemoryQuestionCadence:    cadence,
		})
		if err != nil {
			t.Fatalf("solo-memory instantiate: %v", err)
		}
		if plain.Schedules[0].Cron != withMem.Schedules[0].Cron {
			t.Errorf("%s: cron differs: solo %q vs solo-memory %q", cadence, plain.Schedules[0].Cron, withMem.Schedules[0].Cron)
		}
		if plain.Schedules[0].Input != withMem.Schedules[0].Input {
			t.Errorf("%s: schedule input differs between the control pair", cadence)
		}
	}
}
