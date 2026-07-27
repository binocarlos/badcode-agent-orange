package topology

// assemblyline.go — topology library entry 6 (docs/product/10-topology-library.md
// §4, Family B): a fixed pipeline of 2–3 stages. Stage 1 is woken by the
// inbound event; each later stage subscribes to the PREVIOUS stage's
// `worker.finished`, filtered to it — a chain of hand-offs where every stage
// transforms what the one before produced.
//
// # Honesty about the relay (the T6 ROUTE-TO discovery, applied)
//
// Workers cannot emit arbitrary typed events: the only routable thing a
// worker's work produces is `worker.finished`, whose text is its ENTIRE
// finished transcript (docs/18-workers-memory-events.md §8.2). So the hand-off
// here is the transcript itself — stage k's reply IS stage k+1's input, whole
// and unedited, and the prompts say exactly that. A prompt promising a
// "pass this object downstream" mechanism that does not exist would be
// teaching the model to fail; instead each stage is told to make its reply
// carry everything the next stage needs, because the reply is the baton.

import (
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Assembly-line's question IDs.
const (
	AssemblyLineQuestionStagePrefix  = "stage-prefix"
	AssemblyLineQuestionStageCount   = "stage-count"
	AssemblyLineQuestionInboundEvent = "inbound-event-type"
	AssemblyLineQuestionMission      = "mission"
)

// stageIdentity opens every stage's prompt — the same identity-phrase
// discipline supervisor.go established: stage prompts name their NEIGHBOURS
// by bare name (the relay must say who hands to whom), so the phrase
// "You are <name>," is the one string guaranteed unique to each stage's own
// prompt, and body-substring machinery (mock scripts) can partition the
// stages' requests even though each request names two of them.
func stageIdentity(name string) string { return "You are " + name + "," }

// stageCounts maps the choice answer to its integer — the closed-map posture
// solo's cadences and supervisor's counts use.
var stageCounts = map[string]int{"2": 2, "3": 3}

func init() {
	Register(&Topology{
		Name:    "assembly-line",
		Version: "v1",
		Description: "A fixed pipeline of 2-3 stages (the chain). Stage 1 is woken by the inbound " +
			"event; each later stage subscribes to the previous stage's worker.finished, so every " +
			"finished transcript is the next stage's input — the transcript is the hand-off, " +
			"since workers cannot emit arbitrary typed events. Stages become <prefix>-1, <prefix>-2, ...",
		Questions: []Question{
			{
				ID:       AssemblyLineQuestionStagePrefix,
				Prompt:   "Name prefix for the stages (kebab-case); they become <prefix>-1, <prefix>-2, ...",
				Type:     QuestionString,
				Default:  "stage",
				Required: true,
			},
			{
				ID:       AssemblyLineQuestionStageCount,
				Prompt:   "How many stages?",
				Type:     QuestionChoice,
				Choices:  []string{"2", "3"},
				Default:  "2",
				Required: true,
			},
			{
				ID:       AssemblyLineQuestionInboundEvent,
				Prompt:   "The inbound event type that feeds the first stage (e.g. work.arrived).",
				Type:     QuestionString,
				Default:  "work.arrived",
				Required: true,
			},
			{
				ID:       AssemblyLineQuestionMission,
				Prompt:   "What does this line produce? Folded into every stage's prompt.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderAssemblyLine,
	})
}

// renderAssemblyLine is the pure renderer for assembly-line@v1.
func renderAssemblyLine(a Answers) (*Bundle, error) {
	prefix := a[AssemblyLineQuestionStagePrefix].(string)
	countChoice := a[AssemblyLineQuestionStageCount].(string)
	n, ok := stageCounts[countChoice]
	if !ok {
		return nil, fmt.Errorf("%w: unknown stage count %q", ErrRender, countChoice)
	}

	stages := make([]string, n)
	names := make([]namedWorker, n)
	for i := range stages {
		stages[i] = fmt.Sprintf("%s-%d", prefix, i+1)
		names[i] = namedWorker{AssemblyLineQuestionStagePrefix, stages[i]}
	}
	if err := checkSeedWorkerNames(names); err != nil {
		return nil, err
	}

	mission, err := nonBlankAnswer(a, AssemblyLineQuestionMission)
	if err != nil {
		return nil, err
	}

	inbound := a[AssemblyLineQuestionInboundEvent].(string)
	if err := checkSeedEventType(AssemblyLineQuestionInboundEvent, inbound); err != nil {
		return nil, err
	}

	workers := make([]agentdb.Worker, 0, n)
	subscriptions := make([]agentdb.Subscription, 0, n)
	for i, name := range stages {
		prev, next := "", ""
		if i > 0 {
			prev = stages[i-1]
		}
		if i < n-1 {
			next = stages[i+1]
		}
		desc := fmt.Sprintf("Stage %d of %d on the line.", i+1, n)
		switch {
		case prev == "":
			desc += " Woken by " + inbound + " events; its finished transcript is " + next + "'s input."
		case next == "":
			desc += " Woken by " + prev + "'s finishes; the final stage — its reply is the line's output."
		default:
			desc += " Woken by " + prev + "'s finishes; its finished transcript is " + next + "'s input."
		}
		workers = append(workers, agentdb.Worker{
			Name:         name,
			Description:  desc,
			SystemPrompt: stagePrompt(mission, name, i+1, n, prev, next, inbound),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		})
		if prev == "" {
			subscriptions = append(subscriptions, agentdb.Subscription{
				EventType: inbound,
				Worker:    name,
				Enabled:   true,
			})
		} else {
			subscriptions = append(subscriptions, agentdb.Subscription{
				// Filtered to the previous stage so the chain is a chain: stage
				// k hears stage k-1 and nothing else — not itself, not the
				// stages beyond (§8.4 discipline, chain-shaped).
				EventType: agentdb.EventTypeWorkerFinished,
				Filter:    agentdb.JSONMap{"worker": prev},
				Worker:    name,
				Enabled:   true,
			})
		}
	}

	return &Bundle{
		Workers:       workers,
		Subscriptions: subscriptions,
		Schedules:     []agentdb.Schedule{},
	}, nil
}

// stagePrompt tells one stage who it is, where its input really comes from and
// where its output really goes — the transcript relay, described honestly.
func stagePrompt(mission, name string, ordinal, total int, prev, next, inbound string) string {
	lines := []string{
		mission,
		fmt.Sprintf("%s stage %d of %d on an assembly line.", stageIdentity(name), ordinal, total),
	}
	if prev == "" {
		lines = append(lines, "Work arrives as "+inbound+" events; the event text is the job as the outside world submitted it.")
	} else {
		lines = append(lines,
			"Every delivery you receive is "+prev+"'s full finished transcript — the previous stage's reply is your input, whole and unedited. There is no other hand-off channel.")
	}
	if next == "" {
		lines = append(lines,
			"Do the final stage's part. You are the last stage: your finished transcript is not routed to any further worker, so your reply is the line's output.")
	} else {
		lines = append(lines,
			"Do your stage's part only — do not do "+next+"'s work.",
			"When you finish, your ENTIRE transcript is delivered (as one worker.finished event) to "+next+". The transcript IS the hand-off — you cannot post events yourself — so state everything "+next+" needs explicitly in your reply.")
	}
	return strings.Join(lines, "\n")
}
