package topology

// shamcritic.go — topology library control 3 (docs/product/10-topology-library.md
// §3/§4, Family A). The placebo arm of the actor-critic experiment: wiring
// STRUCTURALLY IDENTICAL to actor-critic@v1 — an actor woken by its task
// event, a critic woken by the actor's filtered `worker.finished` — but the
// critic's standing orders are to REORDER the actor's instructions without
// changing what any of them means, and to say honestly in the rationale that
// the shuffle is arbitrary.
//
// Why such a strange org exists: any observed difference between actor-critic
// and solo could be the critic's diagnosis — or just the churn of being
// rewritten at all (prompt length drift, recency effects, the model reacting
// to fresh wording). The sham critic produces the churn with none of the
// diagnosis, so actor-critic@v1 minus sham-critic@v1 isolates what the
// diagnosis itself is worth. That only works if the wiring is byte-identical
// apart from the critic's words — TestShamCriticWiringMatchesActorCritic pins
// the subscription rows against actor-critic's, same names in, same edges out.
//
// The honesty rule is load-bearing, not decoration: a sham that dressed its
// shuffles up as findings would poison the config log the experimenter reads
// (§15's rationale trail is the record of WHY prompts changed). The control's
// rationale must say "arbitrary", every time, so the log itself distinguishes
// the placebo arm.

import (
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Sham-critic's question IDs. Actor name and seed deliberately reuse
// actor-critic's IDs and wording; there is no criterion question — the sham
// holds no standard, which is the point.
const (
	ShamCriticQuestionActorName  = "actor-name"
	ShamCriticQuestionActorSeed  = "actor-prompt-seed"
	ShamCriticQuestionCriticName = "critic-name"
)

func init() {
	Register(&Topology{
		Name:    "sham-critic",
		Version: "v1",
		Description: "The placebo critic (control 3): wiring identical to actor-critic@v1, but the " +
			"critic only REORDERS the actor's instructions — no additions, removals or rewording — " +
			"and its rationale says honestly that the shuffle is arbitrary. Subtracting this arm " +
			"from actor-critic isolates what the critic's diagnosis is worth over mere prompt churn. " +
			"The actor is woken by <actor-name>.task events.",
		Questions: []Question{
			{
				ID:       ShamCriticQuestionActorName,
				Prompt:   "Name for the actor — the worker that does the work (kebab-case).",
				Type:     QuestionString,
				Default:  "actor",
				Required: true,
			},
			{
				ID:       ShamCriticQuestionActorSeed,
				Prompt:   "What should the actor do? This becomes its starting system prompt.",
				Type:     QuestionString,
				Required: true,
			},
			{
				ID:       ShamCriticQuestionCriticName,
				Prompt:   "Name for the sham critic — the worker that reshuffles the actor's prompt (kebab-case).",
				Type:     QuestionString,
				Default:  "shuffler",
				Required: true,
			},
		},
		Render: renderShamCritic,
	})
}

// shamCriticPrompt is the placebo's charter. It names the same real tools the
// genuine critic holds — the intervention channel is identical; only the
// content of the intervention differs.
func shamCriticPrompt(actor string) string {
	return strings.Join([]string{
		"You review " + actor + "'s finished work. Each delivery you receive is " + actor + "'s full transcript.",
		"You are a control, not a judge: you never evaluate quality and you hold no standard of good.",
		"After each delivery, use worker_prompt_read and worker_prompt_write to REORDER the instructions in " + actor + "'s system prompt:",
		"change the order of its sentences or rules and nothing else — no additions, no removals, no rewording beyond what the reordering itself requires.",
		"Be honest in the rationale, every time: say plainly that this is an arbitrary reshuffle with no diagnostic content. Never claim something was wrong.",
	}, "\n")
}

// renderShamCritic is the pure renderer for sham-critic@v1. The bundle's
// wiring is deliberately built the same way renderActorCritic builds it.
func renderShamCritic(a Answers) (*Bundle, error) {
	actor := a[ShamCriticQuestionActorName].(string)
	critic := a[ShamCriticQuestionCriticName].(string)
	if err := checkSeedWorkerNames([]namedWorker{
		{ShamCriticQuestionActorName, actor},
		{ShamCriticQuestionCriticName, critic},
	}); err != nil {
		return nil, err
	}
	seed, err := nonBlankAnswer(a, ShamCriticQuestionActorSeed)
	if err != nil {
		return nil, err
	}

	return &Bundle{
		Workers: []agentdb.Worker{
			{
				Name:         actor,
				Description:  "The actor: does the work. Woken by " + TaskEventType(actor) + " events; its prompt is what the sham critic reshuffles.",
				SystemPrompt: seed,
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
			},
			{
				Name:         critic,
				Description:  "The sham critic (placebo arm): reorders " + actor + "'s prompt after each finish without changing its meaning, and says so in the rationale.",
				SystemPrompt: shamCriticPrompt(actor),
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
				// finishes only — never to its own (§8.4). Same edge, same
				// filter, same shape as actor-critic@v1; pinned by test.
				EventType: agentdb.EventTypeWorkerFinished,
				Filter:    agentdb.JSONMap{"worker": actor},
				Worker:    critic,
				Enabled:   true,
			},
		},
		Schedules: []agentdb.Schedule{},
	}, nil
}
