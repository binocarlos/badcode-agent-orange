package topology

// blackboard.go — topology library entry 8 (docs/product/10-topology-library.md
// §4, Family B): N peer workers around a shared blackboard. Every contributor
// subscribes to the SAME inbound event type — one event wakes them all — and
// coordination happens only through shared labelled memory: each contributor
// reads the newest board note in its briefing and appends its own note with
// `memory_create`. There is no dispatcher, no routing, no addressing of any
// kind; TestBlackboardHasNoAddressing pins that structurally (no contributor's
// prompt so much as names another).
//
// The board is made of existing parts, verbatim: one shared label
// (`kind=<memory-label>`), the ordinary `worker.briefing` selector on every
// contributor, and the memory tools every worker already holds. The same
// honest limitation as solo-memory applies and is stated in the prompt: a
// briefing section carries the NEWEST matching note, not the whole board —
// so a note must stand alone for whoever reads it next.

import (
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Blackboard's question IDs.
const (
	BlackboardQuestionWorkerPrefix = "worker-prefix"
	BlackboardQuestionWorkerCount  = "worker-count"
	BlackboardQuestionInboundEvent = "inbound-event-type"
	BlackboardQuestionMemoryLabel  = "memory-label"
	BlackboardQuestionMission      = "mission"
)

// blackboardCounts maps the choice answer to its integer — the closed-map
// posture the other counted seeds use.
var blackboardCounts = map[string]int{"2": 2, "3": 3}

func init() {
	Register(&Topology{
		Name:    "blackboard",
		Version: "v1",
		Description: "N peer workers sharing a blackboard (entry 8): all subscribe to the same " +
			"inbound event type, and coordination happens only through shared labelled memory — " +
			"each contributor reads the newest kind=<memory-label> note in its briefing and " +
			"appends its own. No dispatcher, no routing, no addressing anywhere. " +
			"Contributors become <prefix>-1, <prefix>-2, ...",
		Questions: []Question{
			{
				ID:       BlackboardQuestionWorkerPrefix,
				Prompt:   "Name prefix for the contributors (kebab-case); they become <prefix>-1, <prefix>-2, ...",
				Type:     QuestionString,
				Default:  "contributor",
				Required: true,
			},
			{
				ID:       BlackboardQuestionWorkerCount,
				Prompt:   "How many contributors?",
				Type:     QuestionChoice,
				Choices:  []string{"2", "3"},
				Default:  "2",
				Required: true,
			},
			{
				ID:       BlackboardQuestionInboundEvent,
				Prompt:   "The inbound event type that wakes ALL contributors (e.g. board.task).",
				Type:     QuestionString,
				Default:  "board.task",
				Required: true,
			},
			{
				ID: BlackboardQuestionMemoryLabel,
				Prompt: "Label value for the board (a valid label value; notes are written as " +
					"kind=<label> and the newest one is briefed to every contributor).",
				Type:     QuestionString,
				Default:  "blackboard",
				Required: true,
			},
			{
				ID:       BlackboardQuestionMission,
				Prompt:   "What does this team work on? Folded into every contributor's prompt.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderBlackboard,
	})
}

// blackboardPrompt is one contributor's charter. Deliberately identical for
// every contributor except the identity line: peers at a blackboard are
// interchangeable by construction, and the prompt never names another worker —
// the no-addressing property the seed exists to demonstrate.
func blackboardPrompt(mission, name string, ordinal, total int, inbound, label string) string {
	sel := "kind=" + label
	return strings.Join([]string{
		mission,
		fmt.Sprintf("%s contributor %d of %d at a shared blackboard.", stageIdentity(name), ordinal, total),
		"Every " + inbound + " event wakes ALL contributors at once. Nobody routes, nobody is addressed: the board is the only channel between you.",
		"The board is this project's memory under the label " + sel + ". Your briefing section headed \"Your memory briefing: " + sel + "\" carries the NEWEST note any contributor left there (only the newest) — read it before you start.",
		"Do your part of the task, then use memory_create to append a note labelled " + sel + " saying what you did and what the next reader should know. Write it to stand alone.",
	}, "\n")
}

// renderBlackboard is the pure renderer for blackboard@v1.
func renderBlackboard(a Answers) (*Bundle, error) {
	prefix := a[BlackboardQuestionWorkerPrefix].(string)
	countChoice := a[BlackboardQuestionWorkerCount].(string)
	n, ok := blackboardCounts[countChoice]
	if !ok {
		return nil, fmt.Errorf("%w: unknown contributor count %q", ErrRender, countChoice)
	}

	contributors := make([]string, n)
	names := make([]namedWorker, n)
	for i := range contributors {
		contributors[i] = fmt.Sprintf("%s-%d", prefix, i+1)
		names[i] = namedWorker{BlackboardQuestionWorkerPrefix, contributors[i]}
	}
	if err := checkSeedWorkerNames(names); err != nil {
		return nil, err
	}

	mission, err := nonBlankAnswer(a, BlackboardQuestionMission)
	if err != nil {
		return nil, err
	}

	inbound := a[BlackboardQuestionInboundEvent].(string)
	if err := checkSeedEventType(BlackboardQuestionInboundEvent, inbound); err != nil {
		return nil, err
	}

	label := a[BlackboardQuestionMemoryLabel].(string)
	// Same reasoning as solo-memory: the label rides into both the prompt's
	// memory_create instruction and every contributor's briefing selector, and
	// blank would render the meaningless selector `kind=`.
	if strings.TrimSpace(label) == "" {
		return nil, fmt.Errorf("%w: %s must not be blank", ErrRender, BlackboardQuestionMemoryLabel)
	}
	if err := agentdb.ValidateLabelValue(label); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrRender, BlackboardQuestionMemoryLabel, err)
	}

	workers := make([]agentdb.Worker, 0, n)
	subscriptions := make([]agentdb.Subscription, 0, n)
	for i, name := range contributors {
		workers = append(workers, agentdb.Worker{
			Name:         name,
			Description:  fmt.Sprintf("Contributor %d of %d at the blackboard: woken by every %s event, reads the newest kind=%s note in its briefing and appends its own.", i+1, n, inbound, label),
			SystemPrompt: blackboardPrompt(mission, name, i+1, n, inbound, label),
			Briefing:     agentdb.SelectorList{"kind=" + label},
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		})
		subscriptions = append(subscriptions, agentdb.Subscription{
			// Every contributor on the SAME inbound type, unfiltered: one
			// event, N deliveries, no addressing.
			EventType: inbound,
			Worker:    name,
			Enabled:   true,
		})
	}

	return &Bundle{
		Workers:       workers,
		Subscriptions: subscriptions,
		Schedules:     []agentdb.Schedule{},
	}, nil
}
