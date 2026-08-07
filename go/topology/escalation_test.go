package topology

import (
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func escalationV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("escalation", "v1")
	if !ok {
		t.Fatal("escalation@v1 not registered")
	}
	return top
}

// The reference render: one worker, one inbound subscription, nothing else —
// the escalation valve is the prompt plus a tool every session already holds.
func TestEscalationRenderDefaults(t *testing.T) {
	b, err := escalationV1(t).Instantiate(Answers{
		EscalationQuestionMission: "You run the returns desk for the orchard shop.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 1 {
		t.Fatalf("workers: want exactly one, got %d", len(b.Workers))
	}
	w := b.Workers[0]
	if w.Name != "handler" {
		t.Fatalf("worker name: want the default handler, got %q", w.Name)
	}
	if !w.Enabled || w.Frozen {
		t.Errorf("worker: want enabled and unfrozen, got enabled=%v frozen=%v", w.Enabled, w.Frozen)
	}
	if w.MaxInstances != agentdb.DefaultMaxInstances {
		t.Errorf("max_instances: want %d, got %d", agentdb.DefaultMaxInstances, w.MaxInstances)
	}
	if len(w.Briefing) != 0 {
		t.Errorf("escalation has no memory channel; got briefing %v", w.Briefing)
	}
	if !strings.Contains(w.SystemPrompt, "You run the returns desk for the orchard shop.") {
		t.Error("prompt is missing the mission")
	}

	if len(b.Subscriptions) != 1 {
		t.Fatalf("subscriptions: want exactly the inbound edge, got %d", len(b.Subscriptions))
	}
	sub := b.Subscriptions[0]
	if sub.EventType != "case.arrived" || sub.Worker != "handler" || len(sub.Filter) != 0 {
		t.Errorf("subscription: want unfiltered case.arrived → handler, got %+v", sub)
	}

	if len(b.Schedules) != 0 || b.SettingsPatch != nil || len(b.MemorySeeds) != 0 {
		t.Error("escalation renders no schedules, no settings patch and no memory seeds")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// The charter's honesty, pinned: it names the REAL tool (request_human_attention
// is a core MCP tool every session holds — mcp_management.go), describes the
// real mechanism (the job parks awaiting a human; §8.4's pause, not an end),
// and says out loud that the pause is the correct outcome — a worker taught
// that pausing is failure guesses instead, which is the defect (doc 11 S4)
// this shape exists to avoid.
func TestEscalationPromptDescribesTheRealValve(t *testing.T) {
	b, err := escalationV1(t).Instantiate(Answers{EscalationQuestionMission: "m"})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	prompt := b.Workers[0].SystemPrompt
	for _, want := range []string{
		"request_human_attention",
		"irreversible, outside policy, or you are uncertain",
		"do NOT guess",
		"parks awaiting a human",
		"the correct outcome, not a failure",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt must contain %q; got:\n%s", want, prompt)
		}
	}
}

// Renderer refusals: the shared naming/event discipline.
func TestEscalationRenderRefusals(t *testing.T) {
	tests := []struct {
		name    string
		answers Answers
		wantErr string
	}{
		{
			name:    "missing mission",
			answers: Answers{},
			wantErr: `question "mission" is required`,
		},
		{
			name: "blank mission",
			answers: Answers{
				EscalationQuestionMission: " ",
			},
			wantErr: "mission must not be blank",
		},
		{
			name: "non-kebab worker name",
			answers: Answers{
				EscalationQuestionWorkerName: "Case Handler",
				EscalationQuestionMission:    "m",
			},
			wantErr: "not kebab-case",
		},
		{
			name: "inbound in the worker.* namespace",
			answers: Answers{
				EscalationQuestionInboundEvent: "worker.finished",
				EscalationQuestionMission:      "m",
			},
			wantErr: "worker.* namespace",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := escalationV1(t).Instantiate(tc.answers)
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
