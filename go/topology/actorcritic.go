package topology

// actorcritic.go — topology library entry 4 (docs/product/10-topology-library.md
// §4, Family B: work topologies). Two workers: an actor doing the work and a
// critic that subscribes to the actor's completion and holds the improvement
// loop — literally §8.7, the minimal loop already proven to close.
//
// Shape: the actor is woken by an inbound task event (derived from its name —
// see TaskEventType); the critic is woken by the actor's `worker.finished`,
// filtered to the actor so it never reacts to its own finish. "What good means"
// is an answer, folded verbatim into the critic's prompt as its review
// standard — the criterion is configuration, not code.
//
// This file also carries the small helpers the seed family shares
// (TaskEventType, checkSeedWorkerNames, nonBlankAnswer): actor-critic is the
// canonical multi-worker seed and the others (supervisor, frozen-scorer) build
// on the same discipline.

import (
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Actor-critic's question IDs, named so tests and the e2e don't scatter string
// literals (same posture as solo.go).
const (
	ActorCriticQuestionActorName  = "actor-name"
	ActorCriticQuestionActorSeed  = "actor-prompt-seed"
	ActorCriticQuestionCriticName = "critic-name"
	ActorCriticQuestionCriterion  = "criterion"
)

// TaskEventType derives a seed's inbound event type from its actor's name.
// Deriving (rather than asking) keeps the question surface small and makes the
// wiring self-describing: the org chart's trigger is visible in the preview
// diff, and two topologies applied to one project cannot collide on it as long
// as their actor names differ (which the worker-name collision check already
// enforces).
func TaskEventType(worker string) string { return worker + ".task" }

// namedWorker pairs a would-be worker name with the question it answers, so a
// refusal can say which answer was wrong.
type namedWorker struct {
	qid  string
	name string
}

// checkSeedWorkerNames enforces the naming discipline every multi-worker seed
// needs: each name valid worker identity, all names pairwise distinct AND
// pairwise non-substring. The substring rule is scar tissue, not taste — the
// work plan's standing traps record that mock-script rules (and any other
// body-substring machinery) partition by worker name, and a name that contains
// another silently matches the wrong rule.
func checkSeedWorkerNames(names []namedWorker) error {
	for _, n := range names {
		if err := agentdb.ValidateWorkerName(n.name); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrRender, n.qid, err)
		}
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			a, b := names[i], names[j]
			if a.name == b.name {
				return fmt.Errorf("%w: %s and %s must be distinct (both are %q)", ErrRender, a.qid, b.qid, a.name)
			}
			if strings.Contains(a.name, b.name) || strings.Contains(b.name, a.name) {
				return fmt.Errorf("%w: %s (%q) and %s (%q) must not be substrings of one another (worker names partition event text and mock-script rules)",
					ErrRender, a.qid, a.name, b.qid, b.name)
			}
		}
	}
	return nil
}

// nonBlankAnswer returns the string answer for qid, refusing blank — the
// renderer-side semantic check every free-text seed answer needs.
func nonBlankAnswer(a Answers, qid string) (string, error) {
	s := a[qid].(string)
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%w: %s must not be blank", ErrRender, qid)
	}
	return s, nil
}

func init() {
	Register(&Topology{
		Name:    "actor-critic",
		Version: "v1",
		Description: "An actor doing the work and a critic subscribed to its completion. " +
			"The critic's role is to rewrite the actor's prompt (worker_prompt_write, " +
			"with a rationale) whenever the work falls short of the stated criterion — " +
			"the minimal §8.7 improvement loop. The actor is woken by <actor-name>.task events.",
		Questions: []Question{
			{
				ID:       ActorCriticQuestionActorName,
				Prompt:   "Name for the actor — the worker that does the work (kebab-case).",
				Type:     QuestionString,
				Default:  "actor",
				Required: true,
			},
			{
				ID:       ActorCriticQuestionActorSeed,
				Prompt:   "What should the actor do? This becomes its starting system prompt.",
				Type:     QuestionString,
				Required: true,
			},
			{
				ID:       ActorCriticQuestionCriticName,
				Prompt:   "Name for the critic — the worker that reviews and retunes the actor (kebab-case).",
				Type:     QuestionString,
				Default:  "critic",
				Required: true,
			},
			{
				ID:       ActorCriticQuestionCriterion,
				Prompt:   "What does good mean for the actor's output? This becomes the critic's review standard.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderActorCritic,
	})
}

// criticPrompt is the critic's system prompt, shared with the frozen-scorer
// seed (whose loop under test is exactly this pair). It folds in the criterion
// and names the real tools — the loop is made of words, and these are the
// words.
func criticPrompt(actor, criterion string) string {
	return strings.Join([]string{
		fmt.Sprintf("You review %s's finished work. Each delivery you receive is %s's full transcript.", actor, actor),
		fmt.Sprintf("What good means here: %s.", criterion),
		fmt.Sprintf("When the work falls short of that standard in a way %s's standing orders would keep producing,", actor),
		fmt.Sprintf("use worker_prompt_read and worker_prompt_write to amend %s's system prompt, with a rationale", actor),
		"saying what was wrong. Amend rather than replace: keep every rule already there.",
	}, "\n")
}

// renderActorCritic is the pure renderer for actor-critic@v1.
func renderActorCritic(a Answers) (*Bundle, error) {
	actor := a[ActorCriticQuestionActorName].(string)
	critic := a[ActorCriticQuestionCriticName].(string)
	if err := checkSeedWorkerNames([]namedWorker{
		{ActorCriticQuestionActorName, actor},
		{ActorCriticQuestionCriticName, critic},
	}); err != nil {
		return nil, err
	}
	seed, err := nonBlankAnswer(a, ActorCriticQuestionActorSeed)
	if err != nil {
		return nil, err
	}
	criterion, err := nonBlankAnswer(a, ActorCriticQuestionCriterion)
	if err != nil {
		return nil, err
	}

	return &Bundle{
		Workers: []agentdb.Worker{
			{
				Name:         actor,
				Description:  "The actor: does the work. Woken by " + TaskEventType(actor) + " events; its prompt is what the critic improves.",
				SystemPrompt: seed,
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
			},
			{
				Name:         critic,
				Description:  "The critic: reviews " + actor + "'s finished work against the stated criterion and retunes its prompt via worker_prompt_write.",
				SystemPrompt: criticPrompt(actor, criterion),
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
			},
		},
		Subscriptions: []agentdb.Subscription{
			{
				EventType: TaskEventType(actor),
				Worker:    actor,
				Enabled:   true,
			},
			{
				// Filtered to the actor so the critic reacts to the actor's
				// finishes only — never to its own (§8.4).
				EventType: agentdb.EventTypeWorkerFinished,
				Filter:    agentdb.JSONMap{"worker": actor},
				Worker:    critic,
				Enabled:   true,
			},
		},
		Schedules: []agentdb.Schedule{},
	}, nil
}
