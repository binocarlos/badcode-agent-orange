package topology

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func assemblyLineV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("assembly-line", "v1")
	if !ok {
		t.Fatal("assembly-line@v1 not registered")
	}
	return top
}

// The reference render: two stages chained head to tail — the inbound event
// feeds stage 1, stage 2 hears stage 1's finishes and nothing else.
func TestAssemblyLineRenderDefaults(t *testing.T) {
	b, err := assemblyLineV1(t).Instantiate(Answers{
		AssemblyLineQuestionMission: "Turn rough chairs into painted ones.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 2 {
		t.Fatalf("workers: want 2, got %d", len(b.Workers))
	}
	first, last := b.Workers[0], b.Workers[1]
	if first.Name != "stage-1" || last.Name != "stage-2" {
		t.Fatalf("stage names: want stage-1/stage-2 (the defaults), got %q/%q", first.Name, last.Name)
	}
	for _, w := range b.Workers {
		if !w.Enabled || w.Frozen {
			t.Errorf("stage %q: want enabled and unfrozen, got enabled=%v frozen=%v", w.Name, w.Enabled, w.Frozen)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("stage %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
		if !strings.Contains(w.SystemPrompt, "Turn rough chairs into painted ones.") {
			t.Errorf("stage %q prompt must fold the mission in; got:\n%s", w.Name, w.SystemPrompt)
		}
	}

	if len(b.Subscriptions) != 2 {
		t.Fatalf("subscriptions: want 2, got %d", len(b.Subscriptions))
	}
	inbound := b.Subscriptions[0]
	if inbound.EventType != "work.arrived" || inbound.Worker != "stage-1" || len(inbound.Filter) != 0 {
		t.Errorf("inbound edge: want work.arrived → stage-1 unfiltered, got %+v", inbound)
	}
	relay := b.Subscriptions[1]
	if relay.EventType != agentdb.EventTypeWorkerFinished || relay.Worker != "stage-2" {
		t.Errorf("relay edge: want worker.finished → stage-2, got %+v", relay)
	}
	if got := relay.Filter["worker"]; got != "stage-1" {
		t.Errorf("relay filter: want worker=stage-1, got %v", relay.Filter)
	}

	if len(b.Schedules) != 0 || b.SettingsPatch != nil || len(b.MemorySeeds) != 0 {
		t.Error("assembly-line renders no schedules, no settings patch, no memory seeds")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// Three stages chain as a chain: each middle/last stage hears exactly its
// predecessor, and no stage subscribes to the last one's finishes.
func TestAssemblyLineThreeStageChain(t *testing.T) {
	b, err := assemblyLineV1(t).Instantiate(Answers{
		AssemblyLineQuestionStagePrefix:  "belt",
		AssemblyLineQuestionStageCount:   "3",
		AssemblyLineQuestionInboundEvent: "item.submitted",
		AssemblyLineQuestionMission:      "m",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if len(b.Workers) != 3 || len(b.Subscriptions) != 3 {
		t.Fatalf("want 3 workers + 3 subscriptions, got %d + %d", len(b.Workers), len(b.Subscriptions))
	}
	if b.Subscriptions[0].EventType != "item.submitted" || b.Subscriptions[0].Worker != "belt-1" {
		t.Errorf("inbound: want item.submitted → belt-1, got %+v", b.Subscriptions[0])
	}
	for k := 2; k <= 3; k++ {
		sub := b.Subscriptions[k-1]
		prev := fmt.Sprintf("belt-%d", k-1)
		if sub.EventType != agentdb.EventTypeWorkerFinished || sub.Worker != fmt.Sprintf("belt-%d", k) || sub.Filter["worker"] != prev {
			t.Errorf("edge %d: want worker.finished(worker=%s) → belt-%d, got %+v", k, prev, k, sub)
		}
	}
	// Nothing hears the last stage — the chain terminates.
	for _, sub := range b.Subscriptions {
		if sub.Filter["worker"] == "belt-3" {
			t.Errorf("no subscription may hear the final stage; got %+v", sub)
		}
	}
}

// The prompts tell the truth about the relay, per stage position: the first
// stage names the inbound event, later stages name their predecessor's
// transcript as their input, non-final stages document the worker.finished
// hand-off and the last stage says it is last. And the identity-phrase
// discipline holds: each prompt contains its OWN "You are <name>," and no
// other stage's — neighbours are named bare, never as an identity.
func TestAssemblyLinePromptsDescribeTheRelay(t *testing.T) {
	b, err := assemblyLineV1(t).Instantiate(Answers{
		AssemblyLineQuestionStageCount: "3",
		AssemblyLineQuestionMission:    "m",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	prompts := map[string]string{}
	for _, w := range b.Workers {
		prompts[w.Name] = w.SystemPrompt
	}

	if !strings.Contains(prompts["stage-1"], "work.arrived") {
		t.Error("stage-1 must name the inbound event")
	}
	for _, pair := range [][2]string{{"stage-2", "stage-1"}, {"stage-3", "stage-2"}} {
		if !strings.Contains(prompts[pair[0]], pair[1]+"'s full finished transcript") {
			t.Errorf("%s must name %s's transcript as its input; got:\n%s", pair[0], pair[1], prompts[pair[0]])
		}
	}
	for _, name := range []string{"stage-1", "stage-2"} {
		for _, want := range []string{"worker.finished", "cannot post events yourself"} {
			if !strings.Contains(prompts[name], want) {
				t.Errorf("%s must document the hand-off honestly (%q); got:\n%s", name, want, prompts[name])
			}
		}
	}
	if !strings.Contains(prompts["stage-3"], "last stage") {
		t.Error("the final stage must be told it is last")
	}

	for name, prompt := range prompts {
		if !strings.Contains(prompt, stageIdentity(name)) {
			t.Errorf("%s must open with its identity phrase %q", name, stageIdentity(name))
		}
		for other := range prompts {
			if other != name && strings.Contains(prompt, stageIdentity(other)) {
				t.Errorf("%s's prompt contains %s's identity phrase — the phrase must stay unique per stage", name, other)
			}
		}
	}
}

// Renderer refusals: the shared event-type and naming discipline.
func TestAssemblyLineRenderRefusals(t *testing.T) {
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
				AssemblyLineQuestionMission: "  ",
			},
			wantErr: "mission must not be blank",
		},
		{
			name: "non-kebab prefix",
			answers: Answers{
				AssemblyLineQuestionStagePrefix: "Belt Stage",
				AssemblyLineQuestionMission:     "m",
			},
			wantErr: "not kebab-case",
		},
		{
			name: "inbound in the worker.* namespace",
			answers: Answers{
				AssemblyLineQuestionInboundEvent: "worker.finished",
				AssemblyLineQuestionMission:      "m",
			},
			wantErr: "worker.* namespace",
		},
		{
			name: "wildcard inbound",
			answers: Answers{
				AssemblyLineQuestionInboundEvent: "work.*",
				AssemblyLineQuestionMission:      "m",
			},
			wantErr: "not a wildcard",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := assemblyLineV1(t).Instantiate(tc.answers)
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
