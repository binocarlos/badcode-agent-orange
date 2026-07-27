package topology

// debate.go — topology library entry 7 (docs/product/10-topology-library.md
// §4, Family B): a debate committee. N debaters all subscribe — unfiltered —
// to the SAME inbound question event, so one question wakes every debater at
// once and independently; an aggregator subscribes to each debater's
// `worker.finished` separately (one equality-filtered subscription per
// debater, §8.4 discipline) and judges the arguments.
//
// Two honesty points, both stated in the prompts because a prompt promising a
// mechanism that does not exist teaches the model to fail:
//
//   - Independence is enforced structurally, not hoped for (entry 7's caveat:
//     debate collapses into groupthink when debaters see each other). No
//     debater's prompt names another debater, and no channel delivers one
//     debater's output to another — TestDebateIndependence pins both.
//   - The aggregator fires N TIMES per question, once per debater's finish,
//     and each delivery is a SINGLE debater's full transcript (the transcript
//     relay — the only routable worker output is worker.finished). There is no
//     mechanism that hands it all N arguments at once, so its charter is to
//     judge per-argument and carry a running verdict, and it says so.

import (
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Debate's question IDs.
const (
	DebateQuestionDebaterPrefix  = "debater-prefix"
	DebateQuestionDebaterCount   = "debater-count"
	DebateQuestionAggregatorName = "aggregator-name"
	DebateQuestionInboundEvent   = "inbound-event-type"
	DebateQuestionMission        = "mission"
)

// debateCounts maps the choice answer to its integer — the closed-map posture
// the other counted seeds use.
var debateCounts = map[string]int{"2": 2, "3": 3}

func init() {
	Register(&Topology{
		Name:    "debate",
		Version: "v1",
		Description: "A debate committee (entry 7): N debaters all subscribe unfiltered to the same " +
			"inbound question event — one question wakes every debater at once, independently — and " +
			"an aggregator subscribes to each debater's worker.finished separately, weighing each " +
			"argument and keeping a verdict. Debaters never see or name each other (independence is " +
			"structural), and the aggregator is woken once per debater, one transcript at a time. " +
			"Debaters become <prefix>-1, <prefix>-2, ...",
		Questions: []Question{
			{
				ID:       DebateQuestionDebaterPrefix,
				Prompt:   "Name prefix for the debaters (kebab-case); they become <prefix>-1, <prefix>-2, ...",
				Type:     QuestionString,
				Default:  "debater",
				Required: true,
			},
			{
				ID:       DebateQuestionDebaterCount,
				Prompt:   "How many debaters?",
				Type:     QuestionChoice,
				Choices:  []string{"2", "3"},
				Default:  "2",
				Required: true,
			},
			{
				ID:       DebateQuestionAggregatorName,
				Prompt:   "Name for the aggregator — the worker that judges the arguments (kebab-case).",
				Type:     QuestionString,
				Default:  "aggregator",
				Required: true,
			},
			{
				ID:       DebateQuestionInboundEvent,
				Prompt:   "The inbound event type that puts a question to the committee (e.g. debate.question).",
				Type:     QuestionString,
				Default:  "debate.question",
				Required: true,
			},
			{
				ID:       DebateQuestionMission,
				Prompt:   "What does this committee debate? Folded into every prompt.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderDebate,
	})
}

// debaterPrompt is one debater's charter. Identical for every debater except
// the identity line — debaters are interchangeable by construction — and it
// never names a fellow debater: independence is the property the seed exists
// to enforce (TestDebateIndependence pins it).
func debaterPrompt(mission, name string, ordinal, total int, inbound, aggregator string) string {
	return strings.Join([]string{
		mission,
		fmt.Sprintf("%s debater %d of %d on a debate committee.", stageIdentity(name), ordinal, total),
		"Every " + inbound + " event is a question put to the committee. It wakes ALL debaters at once and independently: you do not see the other debaters' arguments and your standing orders do not name them — independence is enforced, not hoped for.",
		"Argue the question on its merits: state your position and your strongest reasons, and make your reply carry your whole case.",
		"When you finish, your ENTIRE transcript is delivered (as one worker.finished event) to " + aggregator + ", the aggregator. The transcript is your argument's only channel — you cannot post events yourself.",
	}, "\n")
}

// debateAggregatorPrompt is the judge's charter — honest about the N-firings
// shape, because that shape is structural: the aggregator cannot be handed the
// whole panel at once, only one finished transcript per firing.
func debateAggregatorPrompt(mission, name string, debaters []string, inbound string) string {
	return strings.Join([]string{
		mission,
		fmt.Sprintf("%s the aggregator of a %d-debater committee. The debaters are: %s.",
			stageIdentity(name), len(debaters), strings.Join(debaters, ", ")),
		fmt.Sprintf("You are subscribed to each debater's finish separately, so one %s question wakes you %d times — once per debater — and each delivery you receive is a SINGLE debater's full finished transcript, never the whole panel at once. There is no channel that hands you all the arguments together.",
			inbound, len(debaters)),
		"Weigh the argument the transcript carries against the question on its merits. Give your verdict on the question as it stands after this argument, say which way this debater pushed it, and note dissent explicitly wherever the argument cuts against that verdict.",
		"You judge arguments; you never argue the question yourself and you never answer " + inbound + " events directly.",
	}, "\n")
}

// renderDebate is the pure renderer for debate@v1.
func renderDebate(a Answers) (*Bundle, error) {
	prefix := a[DebateQuestionDebaterPrefix].(string)
	countChoice := a[DebateQuestionDebaterCount].(string)
	n, ok := debateCounts[countChoice]
	if !ok {
		return nil, fmt.Errorf("%w: unknown debater count %q", ErrRender, countChoice)
	}
	aggregator := a[DebateQuestionAggregatorName].(string)

	debaters := make([]string, n)
	names := make([]namedWorker, 0, n+1)
	for i := range debaters {
		debaters[i] = fmt.Sprintf("%s-%d", prefix, i+1)
		names = append(names, namedWorker{DebateQuestionDebaterPrefix, debaters[i]})
	}
	names = append(names, namedWorker{DebateQuestionAggregatorName, aggregator})
	if err := checkSeedWorkerNames(names); err != nil {
		return nil, err
	}

	mission, err := nonBlankAnswer(a, DebateQuestionMission)
	if err != nil {
		return nil, err
	}

	inbound := a[DebateQuestionInboundEvent].(string)
	if err := checkSeedEventType(DebateQuestionInboundEvent, inbound); err != nil {
		return nil, err
	}

	workers := make([]agentdb.Worker, 0, n+1)
	subscriptions := make([]agentdb.Subscription, 0, 2*n)
	for i, name := range debaters {
		workers = append(workers, agentdb.Worker{
			Name:         name,
			Description:  fmt.Sprintf("Debater %d of %d: woken by every %s event, argues the question independently; its finished transcript is judged by %s.", i+1, n, inbound, aggregator),
			SystemPrompt: debaterPrompt(mission, name, i+1, n, inbound, aggregator),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		})
		subscriptions = append(subscriptions, agentdb.Subscription{
			// Every debater on the SAME inbound type, unfiltered: one
			// question, N independent arguments.
			EventType: inbound,
			Worker:    name,
			Enabled:   true,
		})
	}
	workers = append(workers, agentdb.Worker{
		Name:         aggregator,
		Description:  fmt.Sprintf("The aggregator: woken once per debater's finish (%d firings per question), weighs each argument and keeps the committee's verdict, noting dissent.", n),
		SystemPrompt: debateAggregatorPrompt(mission, aggregator, debaters, inbound),
		MaxInstances: agentdb.DefaultMaxInstances,
		Enabled:      true,
	})
	for _, name := range debaters {
		subscriptions = append(subscriptions, agentdb.Subscription{
			// One equality-filtered edge per debater: the aggregator hears
			// each debater exactly once per question and never its own finish.
			EventType: agentdb.EventTypeWorkerFinished,
			Filter:    agentdb.JSONMap{"worker": name},
			Worker:    aggregator,
			Enabled:   true,
		})
	}

	return &Bundle{
		Workers:       workers,
		Subscriptions: subscriptions,
		Schedules:     []agentdb.Schedule{},
	}, nil
}
