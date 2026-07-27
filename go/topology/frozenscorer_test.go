package topology

import (
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func frozenScorerV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("frozen-scorer", "v1")
	if !ok {
		t.Fatal("frozen-scorer@v1 not registered")
	}
	return top
}

// The reference render: an actor-critic pair plus the frozen instrument.
// The scorer — and ONLY the scorer — carries Frozen: true; the wiring keeps
// the critic and the scorer causally disconnected from each other.
func TestFrozenScorerRenderDefaults(t *testing.T) {
	b, err := frozenScorerV1(t).Instantiate(Answers{
		FrozenScorerQuestionActorSeed: "Draft release notes for the team.",
		FrozenScorerQuestionCriterion: "every draft ends with a summary line",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 3 {
		t.Fatalf("workers: want 3, got %d", len(b.Workers))
	}
	actor, critic, scorer := b.Workers[0], b.Workers[1], b.Workers[2]
	if actor.Name != "actor" || critic.Name != "critic" || scorer.Name != "scorer" {
		t.Fatalf("worker names: want actor/critic/scorer (the defaults), got %q/%q/%q", actor.Name, critic.Name, scorer.Name)
	}

	// The seed's whole point: the instrument ships frozen, and nothing else does.
	if !scorer.Frozen {
		t.Error("the scorer must render Frozen: true — a bundle must be able to ship a frozen instrument")
	}
	if actor.Frozen || critic.Frozen {
		t.Error("only the scorer is the instrument; actor and critic must not render frozen")
	}
	for _, w := range b.Workers {
		if !w.Enabled {
			t.Errorf("worker %q must render enabled (frozen is not disabled — the instrument still runs jobs)", w.Name)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
	}

	if actor.SystemPrompt != "Draft release notes for the team." {
		t.Errorf("actor prompt: want the seed verbatim, got %q", actor.SystemPrompt)
	}
	// The critic half is byte-for-byte the actor-critic seed's critic — the
	// loop under test is the same loop, so comparing the two seeds varies only
	// the instrument's presence.
	if want := criticPrompt("actor", "every draft ends with a summary line"); critic.SystemPrompt != want {
		t.Errorf("critic prompt must reuse actor-critic's verbatim:\n got %q\nwant %q", critic.SystemPrompt, want)
	}
	for _, want := range []string{"Score: N/5", "every draft ends with a summary line", "frozen"} {
		if !strings.Contains(scorer.SystemPrompt, want) {
			t.Errorf("scorer prompt must contain %q; got:\n%s", want, scorer.SystemPrompt)
		}
	}

	if len(b.Subscriptions) != 3 {
		t.Fatalf("subscriptions: want 3, got %d", len(b.Subscriptions))
	}
	inbound := b.Subscriptions[0]
	if inbound.EventType != "actor.task" || inbound.Worker != "actor" {
		t.Errorf("inbound: want actor.task → actor, got %s → %s", inbound.EventType, inbound.Worker)
	}
	for _, sub := range b.Subscriptions[1:] {
		if sub.EventType != agentdb.EventTypeWorkerFinished {
			t.Errorf("edge to %s: want worker.finished, got %s", sub.Worker, sub.EventType)
		}
		if got := sub.Filter["worker"]; got != "actor" {
			t.Errorf("edge to %s: want filter worker=actor, got %v", sub.Worker, sub.Filter)
		}
	}
	if b.Subscriptions[1].Worker != "critic" || b.Subscriptions[2].Worker != "scorer" {
		t.Fatalf("edges: want critic and scorer both observing the actor, got %q/%q",
			b.Subscriptions[1].Worker, b.Subscriptions[2].Worker)
	}

	// Causal isolation, asserted structurally: no subscription routes anything
	// to the critic except the actor's finishes — in particular, the critic
	// holds NO subscription to the scorer, and nothing subscribes to the
	// scorer's own events at all.
	for _, sub := range b.Subscriptions {
		if sub.Filter["worker"] == "scorer" {
			t.Errorf("no subscription may deliver the scorer's events (%s → %s does)", sub.EventType, sub.Worker)
		}
		if sub.Worker == "critic" && sub.Filter["worker"] != "actor" {
			t.Errorf("the critic may observe only the actor, got filter %v", sub.Filter)
		}
	}

	if len(b.Schedules) != 0 {
		t.Errorf("schedules: want none, got %d", len(b.Schedules))
	}
	if b.SettingsPatch != nil {
		t.Error("frozen-scorer must not patch project settings")
	}
	if len(b.MemorySeeds) != 0 {
		t.Errorf("memory seeds: want none, got %d", len(b.MemorySeeds))
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		// Held-out briefs live OUTSIDE the project by definition (entry 12) —
		// the seed cannot ship them and must not pretend to via preconditions.
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// Custom names flow into every referencing row.
func TestFrozenScorerCustomNames(t *testing.T) {
	b, err := frozenScorerV1(t).Instantiate(Answers{
		FrozenScorerQuestionActorName:  "tp7-author",
		FrozenScorerQuestionCriticName: "tp7-tuner",
		FrozenScorerQuestionScorerName: "tp7-judge",
		FrozenScorerQuestionActorSeed:  "p",
		FrozenScorerQuestionCriterion:  "c",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if b.Workers[2].Name != "tp7-judge" || !b.Workers[2].Frozen {
		t.Fatalf("scorer: want tp7-judge frozen, got %q frozen=%v", b.Workers[2].Name, b.Workers[2].Frozen)
	}
	if got := b.Subscriptions[0].EventType; got != "tp7-author.task" {
		t.Errorf("derived inbound event type: want tp7-author.task, got %q", got)
	}
	for _, sub := range b.Subscriptions[1:] {
		if got := sub.Filter["worker"]; got != "tp7-author" {
			t.Errorf("edge to %s: want filter worker=tp7-author, got %v", sub.Worker, got)
		}
	}
}

// Semantic refusals: the three-way naming discipline and the blank checks.
func TestFrozenScorerRenderRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(Answers)
		wantErr string
	}{
		{
			name:    "missing criterion",
			mutate:  func(a Answers) { delete(a, FrozenScorerQuestionCriterion) },
			wantErr: `question "criterion" is required`,
		},
		{
			name:    "blank actor seed",
			mutate:  func(a Answers) { a[FrozenScorerQuestionActorSeed] = " " },
			wantErr: "actor-prompt-seed must not be blank",
		},
		{
			name:    "scorer name collides with critic",
			mutate:  func(a Answers) { a[FrozenScorerQuestionScorerName] = "critic" },
			wantErr: "must be distinct",
		},
		{
			name: "scorer name is a substring of the actor's",
			mutate: func(a Answers) {
				a[FrozenScorerQuestionActorName] = "judge-of-drafts"
				a[FrozenScorerQuestionScorerName] = "judge"
			},
			wantErr: "must not be substrings",
		},
		{
			name:    "non-kebab scorer name",
			mutate:  func(a Answers) { a[FrozenScorerQuestionScorerName] = "The Judge" },
			wantErr: "not kebab-case",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answers := Answers{
				FrozenScorerQuestionActorSeed: "p",
				FrozenScorerQuestionCriterion: "c",
			}
			tc.mutate(answers)
			_, err := frozenScorerV1(t).Instantiate(answers)
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
