package topology

// frozenscorer.go — topology library entry 12 (docs/product/10-topology-library.md
// §4, Family C: experiment topologies). A loop under test — an actor-critic
// pair exactly as actorcritic.go renders one — PLUS a scorer worker shipped
// with Frozen: true: the measuring instrument, causally isolated from the loop
// it measures (§3; AGENTS_RESEARCH.md §4).
//
// What this seed proves about the machinery: a bundle can ship a frozen row.
// The Frozen flag rides the ordinary Worker row through the ordinary apply
// (UpsertWorker carries it), and the MCP boundary then refuses any worker's
// attempt to rewrite the scorer — each refusal a `worker.freeze_refused`
// event, counted, never swallowed.
//
// The wiring discipline that makes the isolation real: the critic holds NO
// subscription to the scorer — nothing the scorer emits reaches the critic,
// and nothing the critic emits reaches the scorer. Both observe the actor,
// independently. (Held-out briefs live outside the project by definition;
// a topology cannot ship them, and honestly does not try.)

import (
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Frozen-scorer's question IDs. The actor/critic half deliberately reuses
// actor-critic's answer shape (same questions, same prompts) — the loop under
// test IS an actor-critic pair, and an experimenter comparing the two seeds
// should be varying the scorer's presence, not accidental prompt wording.
const (
	FrozenScorerQuestionActorName  = "actor-name"
	FrozenScorerQuestionActorSeed  = "actor-prompt-seed"
	FrozenScorerQuestionCriticName = "critic-name"
	FrozenScorerQuestionScorerName = "scorer-name"
	FrozenScorerQuestionCriterion  = "criterion"
)

func init() {
	Register(&Topology{
		Name:    "frozen-scorer",
		Version: "v1",
		Description: "An actor-critic loop under test plus a FROZEN scorer worker — the measuring " +
			"instrument. The scorer observes the actor's finishes and scores them against the " +
			"criterion; its configuration is frozen so the loop it measures cannot rewrite its own " +
			"yardstick, and the critic holds no subscription to it. The actor is woken by " +
			"<actor-name>.task events.",
		Questions: []Question{
			{
				ID:       FrozenScorerQuestionActorName,
				Prompt:   "Name for the actor — the worker that does the work (kebab-case).",
				Type:     QuestionString,
				Default:  "actor",
				Required: true,
			},
			{
				ID:       FrozenScorerQuestionActorSeed,
				Prompt:   "What should the actor do? This becomes its starting system prompt.",
				Type:     QuestionString,
				Required: true,
			},
			{
				ID:       FrozenScorerQuestionCriticName,
				Prompt:   "Name for the critic — the worker that reviews and retunes the actor (kebab-case).",
				Type:     QuestionString,
				Default:  "critic",
				Required: true,
			},
			{
				ID:       FrozenScorerQuestionScorerName,
				Prompt:   "Name for the scorer — the frozen measuring instrument (kebab-case).",
				Type:     QuestionString,
				Default:  "scorer",
				Required: true,
			},
			{
				ID:       FrozenScorerQuestionCriterion,
				Prompt:   "What does good mean for the actor's output? The critic's review standard and the scorer's rubric.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderFrozenScorer,
	})
}

// scorerPrompt is the instrument's charter: measure, don't meddle — and say
// out loud that its own configuration is frozen.
func scorerPrompt(actor, criterion string) string {
	return "You are the frozen scorer — the measuring instrument for this loop.\n" +
		"Each delivery you receive is " + actor + "'s full finished transcript. Score the work against this standard: " + criterion + ".\n" +
		"Reply with a line of the form \"Score: N/5\" and one sentence saying why.\n" +
		"You do not fix anything and you do not advise: you measure. Your own configuration is frozen so the loop you measure cannot rewrite its own yardstick."
}

// renderFrozenScorer is the pure renderer for frozen-scorer@v1.
func renderFrozenScorer(a Answers) (*Bundle, error) {
	actor := a[FrozenScorerQuestionActorName].(string)
	critic := a[FrozenScorerQuestionCriticName].(string)
	scorer := a[FrozenScorerQuestionScorerName].(string)
	if err := checkSeedWorkerNames([]namedWorker{
		{FrozenScorerQuestionActorName, actor},
		{FrozenScorerQuestionCriticName, critic},
		{FrozenScorerQuestionScorerName, scorer},
	}); err != nil {
		return nil, err
	}
	seed, err := nonBlankAnswer(a, FrozenScorerQuestionActorSeed)
	if err != nil {
		return nil, err
	}
	criterion, err := nonBlankAnswer(a, FrozenScorerQuestionCriterion)
	if err != nil {
		return nil, err
	}

	return &Bundle{
		Workers: []agentdb.Worker{
			{
				Name:         actor,
				Description:  "The actor: does the work. Woken by " + TaskEventType(actor) + " events; its prompt is what the critic improves and the scorer measures.",
				SystemPrompt: seed,
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
			},
			{
				Name:         critic,
				Description:  "The critic: reviews " + actor + "'s finished work against the stated criterion and retunes its prompt via worker_prompt_write. It cannot touch the frozen scorer.",
				SystemPrompt: criticPrompt(actor, criterion),
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
			},
			{
				Name:         scorer,
				Description:  "The FROZEN scorer: scores " + actor + "'s finished work against the criterion. A measuring instrument — no worker may change it, only a human.",
				SystemPrompt: scorerPrompt(actor, criterion),
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
				// The point of the seed: the instrument ships frozen. Apply
				// carries this through UpsertWorker unchanged, and the MCP
				// boundary refuses any worker's write against it from the
				// first moment the org exists.
				Frozen: true,
			},
		},
		Subscriptions: []agentdb.Subscription{
			{
				EventType: TaskEventType(actor),
				Worker:    actor,
				Enabled:   true,
			},
			{
				EventType: agentdb.EventTypeWorkerFinished,
				Filter:    agentdb.JSONMap{"worker": actor},
				Worker:    critic,
				Enabled:   true,
			},
			{
				// The scorer observes the actor independently of the critic.
				// There is deliberately NO subscription delivering the scorer's
				// events to the critic (nor the critic's to the scorer): the
				// instrument is causally isolated from the loop it measures.
				EventType: agentdb.EventTypeWorkerFinished,
				Filter:    agentdb.JSONMap{"worker": actor},
				Worker:    scorer,
				Enabled:   true,
			},
		},
		Schedules: []agentdb.Schedule{},
	}, nil
}
