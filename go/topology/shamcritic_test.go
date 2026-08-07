package topology

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func shamCriticV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("sham-critic", "v1")
	if !ok {
		t.Fatal("sham-critic@v1 not registered")
	}
	return top
}

// The reference render: two workers, the actor's seed verbatim, the sham's
// charter honest about what it is.
func TestShamCriticRenderDefaults(t *testing.T) {
	b, err := shamCriticV1(t).Instantiate(Answers{
		ShamCriticQuestionActorSeed: "Keep the till ledger.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 2 {
		t.Fatalf("workers: want 2, got %d", len(b.Workers))
	}
	actor, critic := b.Workers[0], b.Workers[1]
	if actor.Name != "actor" || critic.Name != "shuffler" {
		t.Fatalf("worker names: want actor/shuffler (the defaults), got %q/%q", actor.Name, critic.Name)
	}
	if actor.SystemPrompt != "Keep the till ledger." {
		t.Errorf("actor prompt: want the seed verbatim, got %q", actor.SystemPrompt)
	}
	for _, w := range b.Workers {
		if !w.Enabled || w.Frozen {
			t.Errorf("worker %q: want enabled and unfrozen, got enabled=%v frozen=%v", w.Name, w.Enabled, w.Frozen)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
	}

	// The charter: reorder-only, the real tools, and the honesty rule. And no
	// standard of good anywhere — there is no criterion question to leak in.
	for _, want := range []string{"REORDER", "worker_prompt_write", "no additions, no removals", "arbitrary reshuffle", "Never claim something was wrong"} {
		if !strings.Contains(critic.SystemPrompt, want) {
			t.Errorf("sham charter must contain %q; got:\n%s", want, critic.SystemPrompt)
		}
	}

	if len(b.Schedules) != 0 {
		t.Errorf("schedules: want none, got %d", len(b.Schedules))
	}
	if b.SettingsPatch != nil || len(b.MemorySeeds) != 0 {
		t.Error("sham-critic must not patch settings or seed memories")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// The load-bearing structural pin: given the same names, sham-critic renders
// EXACTLY actor-critic's subscription rows. The two arms of the experiment
// differ in the critic's words and nowhere in the wiring — otherwise the
// comparison would be measuring topology, not diagnosis.
func TestShamCriticWiringMatchesActorCritic(t *testing.T) {
	real, err := actorCriticV1(t).Instantiate(Answers{
		ActorCriticQuestionActorName:  "writer",
		ActorCriticQuestionCriticName: "reviewer",
		ActorCriticQuestionActorSeed:  "p",
		ActorCriticQuestionCriterion:  "c",
	})
	if err != nil {
		t.Fatalf("actor-critic instantiate: %v", err)
	}
	sham, err := shamCriticV1(t).Instantiate(Answers{
		ShamCriticQuestionActorName:  "writer",
		ShamCriticQuestionCriticName: "reviewer",
		ShamCriticQuestionActorSeed:  "p",
	})
	if err != nil {
		t.Fatalf("sham-critic instantiate: %v", err)
	}
	if !reflect.DeepEqual(real.Subscriptions, sham.Subscriptions) {
		t.Fatalf("subscription rows must be identical between the arms:\nactor-critic %#v\nsham-critic  %#v",
			real.Subscriptions, sham.Subscriptions)
	}
	// Same inbound trigger derivation too: the task event comes from the
	// actor's name in both.
	if got := sham.Subscriptions[0].EventType; got != TaskEventType("writer") {
		t.Fatalf("inbound event: want %q, got %q", TaskEventType("writer"), got)
	}
}

// The naming discipline is shared with every multi-worker seed.
func TestShamCriticRenderRefusals(t *testing.T) {
	tests := []struct {
		name    string
		answers Answers
		wantErr string
	}{
		{
			name:    "missing actor seed",
			answers: Answers{},
			wantErr: `question "actor-prompt-seed" is required`,
		},
		{
			name: "blank actor seed",
			answers: Answers{
				ShamCriticQuestionActorSeed: " \t",
			},
			wantErr: "actor-prompt-seed must not be blank",
		},
		{
			name: "actor and critic share a name",
			answers: Answers{
				ShamCriticQuestionActorName:  "worker",
				ShamCriticQuestionCriticName: "worker",
				ShamCriticQuestionActorSeed:  "p",
			},
			wantErr: "must be distinct",
		},
		{
			name: "critic name a substring of the actor's",
			answers: Answers{
				ShamCriticQuestionActorName:  "book-keeper",
				ShamCriticQuestionCriticName: "keeper",
				ShamCriticQuestionActorSeed:  "p",
			},
			wantErr: "must not be substrings",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := shamCriticV1(t).Instantiate(tc.answers)
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
