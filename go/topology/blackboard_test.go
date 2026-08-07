package topology

import (
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func blackboardV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("blackboard", "v1")
	if !ok {
		t.Fatal("blackboard@v1 not registered")
	}
	return top
}

// The reference render: N peers, every one subscribed UNFILTERED to the same
// inbound type, every one carrying the same shared briefing selector.
func TestBlackboardRenderDefaults(t *testing.T) {
	b, err := blackboardV1(t).Instantiate(Answers{
		BlackboardQuestionMission: "Map the old orchard wall.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 2 {
		t.Fatalf("workers: want 2, got %d", len(b.Workers))
	}
	if b.Workers[0].Name != "contributor-1" || b.Workers[1].Name != "contributor-2" {
		t.Fatalf("names: want contributor-1/contributor-2 (the defaults), got %q/%q", b.Workers[0].Name, b.Workers[1].Name)
	}
	for _, w := range b.Workers {
		if !w.Enabled || w.Frozen {
			t.Errorf("worker %q: want enabled and unfrozen, got enabled=%v frozen=%v", w.Name, w.Enabled, w.Frozen)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
		// The shared board: same selector on every contributor.
		if len(w.Briefing) != 1 || w.Briefing[0] != "kind=blackboard" {
			t.Errorf("worker %q briefing: want [kind=blackboard], got %v", w.Name, w.Briefing)
		}
		for _, want := range []string{"Map the old orchard wall.", "memory_create", "kind=blackboard", "board.task"} {
			if !strings.Contains(w.SystemPrompt, want) {
				t.Errorf("worker %q prompt must contain %q; got:\n%s", w.Name, want, w.SystemPrompt)
			}
		}
	}

	if len(b.Subscriptions) != 2 {
		t.Fatalf("subscriptions: want 2, got %d", len(b.Subscriptions))
	}
	for i, sub := range b.Subscriptions {
		if sub.EventType != "board.task" {
			t.Errorf("subscription %d: want the shared inbound type board.task, got %q", i, sub.EventType)
		}
		if sub.Worker != b.Workers[i].Name {
			t.Errorf("subscription %d: want worker %q, got %q", i, b.Workers[i].Name, sub.Worker)
		}
		if len(sub.Filter) != 0 {
			t.Errorf("subscription %d: a blackboard edge is unfiltered — one event wakes everyone; got filter %v", i, sub.Filter)
		}
	}

	if len(b.Schedules) != 0 || b.SettingsPatch != nil {
		t.Error("blackboard renders no schedules and no settings patch")
	}
	if len(b.MemorySeeds) != 0 {
		t.Error("the board starts empty — no memory seeds")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// The seed's defining structural property, pinned: NO addressing anywhere.
// No contributor's prompt names any other contributor; no subscription filter
// singles anyone out; there is no worker.finished edge at all — the board is
// the only channel. (Contrast: supervisor routes by name, assembly-line
// chains by name; blackboard must never grow either.)
func TestBlackboardHasNoAddressing(t *testing.T) {
	b, err := blackboardV1(t).Instantiate(Answers{
		BlackboardQuestionWorkerCount: "3",
		BlackboardQuestionMission:     "m",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if len(b.Workers) != 3 {
		t.Fatalf("workers: want 3, got %d", len(b.Workers))
	}
	for _, w := range b.Workers {
		for _, other := range b.Workers {
			if other.Name != w.Name && strings.Contains(w.SystemPrompt, other.Name) {
				t.Errorf("%s's prompt names %s — a blackboard has no addressing", w.Name, other.Name)
			}
		}
	}
	for _, sub := range b.Subscriptions {
		if sub.EventType == agentdb.EventTypeWorkerFinished {
			t.Errorf("no contributor may hear another's finishes; got %+v", sub)
		}
		if len(sub.Filter) != 0 {
			t.Errorf("no filter may single a contributor out; got %+v", sub)
		}
	}
	// Interchangeability, the same property from the other side: prompts are
	// identical except for each contributor's own identity line.
	norm := func(w agentdb.Worker) string {
		return strings.Replace(w.SystemPrompt, stageIdentity(w.Name)+" contributor", "You are X, contributor", 1)
	}
	first := strings.Replace(norm(b.Workers[0]), "contributor 1 of 3", "contributor N of 3", 1)
	for _, w := range b.Workers[1:] {
		got := norm(w)
		got = strings.Replace(got, "contributor 2 of 3", "contributor N of 3", 1)
		got = strings.Replace(got, "contributor 3 of 3", "contributor N of 3", 1)
		if got != first {
			t.Errorf("contributor prompts must be identical up to identity; %s differs:\n%s\nvs\n%s", w.Name, got, first)
		}
	}
}

// Renderer refusals: the shared naming/event/label discipline.
func TestBlackboardRenderRefusals(t *testing.T) {
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
			name: "non-kebab prefix",
			answers: Answers{
				BlackboardQuestionWorkerPrefix: "Board Hand",
				BlackboardQuestionMission:      "m",
			},
			wantErr: "not kebab-case",
		},
		{
			name: "inbound in the worker.* namespace",
			answers: Answers{
				BlackboardQuestionInboundEvent: "worker.finished",
				BlackboardQuestionMission:      "m",
			},
			wantErr: "worker.* namespace",
		},
		{
			name: "label with a space",
			answers: Answers{
				BlackboardQuestionMemoryLabel: "the board",
				BlackboardQuestionMission:     "m",
			},
			wantErr: "is invalid",
		},
		{
			name: "blank label",
			answers: Answers{
				BlackboardQuestionMemoryLabel: " ",
				BlackboardQuestionMission:     "m",
			},
			wantErr: "memory-label must not be blank",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := blackboardV1(t).Instantiate(tc.answers)
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
