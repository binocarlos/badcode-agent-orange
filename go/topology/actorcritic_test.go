package topology

import (
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func actorCriticV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("actor-critic", "v1")
	if !ok {
		t.Fatal("actor-critic@v1 not registered")
	}
	return top
}

// The reference render: defaults for the names, explicit seed and criterion.
// Two workers, two subscriptions, nothing else.
func TestActorCriticRenderDefaults(t *testing.T) {
	b, err := actorCriticV1(t).Instantiate(Answers{
		ActorCriticQuestionActorSeed: "Write product blurbs for the catalogue.",
		ActorCriticQuestionCriterion: "every blurb opens with a headline line",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 2 {
		t.Fatalf("workers: want 2, got %d", len(b.Workers))
	}
	actor, critic := b.Workers[0], b.Workers[1]
	if actor.Name != "actor" || critic.Name != "critic" {
		t.Fatalf("worker names: want actor/critic (the defaults), got %q/%q", actor.Name, critic.Name)
	}
	if actor.SystemPrompt != "Write product blurbs for the catalogue." {
		t.Errorf("actor prompt: want the seed verbatim, got %q", actor.SystemPrompt)
	}
	for _, w := range b.Workers {
		if !w.Enabled {
			t.Errorf("worker %q must render enabled", w.Name)
		}
		if w.Frozen {
			t.Errorf("worker %q must not render frozen — actor-critic ships no instrument", w.Name)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
		if w.Description == "" {
			t.Errorf("worker %q description must not be empty", w.Name)
		}
	}
	// The critic's prompt carries the criterion, names the actor, and names
	// the real tools — the loop is made of words and these are load-bearing.
	for _, want := range []string{
		"every blurb opens with a headline line",
		"actor",
		"worker_prompt_read",
		"worker_prompt_write",
		"rationale",
	} {
		if !strings.Contains(critic.SystemPrompt, want) {
			t.Errorf("critic prompt must contain %q; got:\n%s", want, critic.SystemPrompt)
		}
	}

	if len(b.Subscriptions) != 2 {
		t.Fatalf("subscriptions: want 2, got %d", len(b.Subscriptions))
	}
	inbound, review := b.Subscriptions[0], b.Subscriptions[1]
	if inbound.EventType != "actor.task" || inbound.Worker != "actor" {
		t.Errorf("inbound subscription: want actor.task → actor, got %s → %s", inbound.EventType, inbound.Worker)
	}
	if len(inbound.Filter) != 0 {
		t.Errorf("inbound subscription must carry no filter, got %v", inbound.Filter)
	}
	if review.EventType != agentdb.EventTypeWorkerFinished || review.Worker != "critic" {
		t.Errorf("review subscription: want worker.finished → critic, got %s → %s", review.EventType, review.Worker)
	}
	if got := review.Filter["worker"]; got != "actor" {
		t.Errorf("review subscription filter: want worker=actor (the critic must never react to its own finish), got %v", review.Filter)
	}
	for _, s := range b.Subscriptions {
		if !s.Enabled {
			t.Errorf("subscription %s → %s must render enabled", s.EventType, s.Worker)
		}
	}

	if len(b.Schedules) != 0 {
		t.Errorf("schedules: want none (rounds are event-driven), got %d", len(b.Schedules))
	}
	if b.SettingsPatch != nil {
		t.Error("actor-critic must not patch project settings")
	}
	if len(b.MemorySeeds) != 0 {
		t.Errorf("memory seeds: want none, got %d", len(b.MemorySeeds))
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// Custom names flow into every row that references them — worker rows,
// the derived inbound event type, and the review filter — so they can never
// drift apart.
func TestActorCriticCustomNames(t *testing.T) {
	b, err := actorCriticV1(t).Instantiate(Answers{
		ActorCriticQuestionActorName:  "tp5-writer",
		ActorCriticQuestionCriticName: "tp5-reviewer",
		ActorCriticQuestionActorSeed:  "p",
		ActorCriticQuestionCriterion:  "c",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if b.Workers[0].Name != "tp5-writer" || b.Workers[1].Name != "tp5-reviewer" {
		t.Fatalf("worker names: got %q/%q", b.Workers[0].Name, b.Workers[1].Name)
	}
	if got := b.Subscriptions[0].EventType; got != "tp5-writer.task" {
		t.Errorf("derived inbound event type: want tp5-writer.task, got %q", got)
	}
	if got := b.Subscriptions[1].Filter["worker"]; got != "tp5-writer" {
		t.Errorf("review filter: want worker=tp5-writer, got %v", got)
	}
	if !strings.Contains(b.Workers[1].SystemPrompt, "tp5-writer") {
		t.Errorf("critic prompt must name the actor; got:\n%s", b.Workers[1].SystemPrompt)
	}
}

// Semantic checks the renderer owns: identity, blankness, and the naming
// discipline (distinct, mutually non-substring names — the standing
// mock-script trap made a render-time rule).
func TestActorCriticRenderRefusals(t *testing.T) {
	valid := Answers{
		ActorCriticQuestionActorSeed: "p",
		ActorCriticQuestionCriterion: "c",
	}
	tests := []struct {
		name    string
		mutate  func(Answers)
		wantErr string
	}{
		{
			name:    "missing actor prompt seed",
			mutate:  func(a Answers) { delete(a, ActorCriticQuestionActorSeed) },
			wantErr: `question "actor-prompt-seed" is required`,
		},
		{
			name:    "blank criterion",
			mutate:  func(a Answers) { a[ActorCriticQuestionCriterion] = "  \n" },
			wantErr: "criterion must not be blank",
		},
		{
			name:    "non-kebab actor name",
			mutate:  func(a Answers) { a[ActorCriticQuestionActorName] = "The Actor" },
			wantErr: "not kebab-case",
		},
		{
			name: "identical names",
			mutate: func(a Answers) {
				a[ActorCriticQuestionActorName] = "worker"
				a[ActorCriticQuestionCriticName] = "worker"
			},
			wantErr: "must be distinct",
		},
		{
			name: "substring names",
			mutate: func(a Answers) {
				a[ActorCriticQuestionActorName] = "writer"
				a[ActorCriticQuestionCriticName] = "writer-critic"
			},
			wantErr: "must not be substrings",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answers := Answers{}
			for k, v := range valid {
				answers[k] = v
			}
			tc.mutate(answers)
			_, err := actorCriticV1(t).Instantiate(answers)
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
