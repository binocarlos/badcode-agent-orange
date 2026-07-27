package topology

// temporalhierarchy.go — topology library entry 10 (docs/product/
// 10-topology-library.md §4, Family B): a temporal hierarchy. Operators are
// the fast, tactical layer — woken by the inbound work event; a strategist is
// the slow, long-horizon layer — its ONLY clock is a deliberately slow cron
// schedule, and its job is to review how the operators work and rewrite their
// prompts (worker_prompt_write, with a rationale) when their standing orders
// keep producing a flaw. The separation of timescales is structural: the
// strategist holds no subscription at all, the operators hold no schedule.
//
// The review channel is memory, not events — and the seed is honest about
// why. A schedule delivers its own Input text, not anyone's transcript, and
// subscribing the strategist to operator finishes would put it on the WORK's
// timescale (every task would wake it), which is exactly what the hierarchy
// exists to avoid. So the operators' charters end with a memory-write
// discipline (append a kind=<label> report after each task) and the
// strategist's worker row carries a briefing selector for the same label —
// the §7 channel solo-memory and blackboard already use, with the same honest
// limitation stated in the prompt: the briefing carries the NEWEST report
// only, so each report must stand alone.

import (
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Temporal-hierarchy's question IDs.
const (
	TemporalQuestionOperatorPrefix = "operator-prefix"
	TemporalQuestionOperatorCount  = "operator-count"
	TemporalQuestionStrategistName = "strategist-name"
	TemporalQuestionCadence        = "strategist-cadence"
	TemporalQuestionInboundEvent   = "inbound-event-type"
	TemporalQuestionMemoryLabel    = "memory-label"
	TemporalQuestionMission        = "mission"
)

// temporalCounts maps the operator-count choice to its integer.
var temporalCounts = map[string]int{"1": 1, "2": 2}

// temporalScheduleInput is what every strategist firing delivers (a schedule
// says what the worker is told, not just when it runs — solo.go's posture).
const temporalScheduleInput = "Scheduled review: read the newest operator report in your briefing, and where an operator's standing orders keep producing a flaw, amend them with worker_prompt_write and a rationale. Then finish."

func init() {
	Register(&Topology{
		Name:    "temporal-hierarchy",
		Version: "v1",
		Description: "A temporal hierarchy (entry 10): operators on fast events, a strategist on a " +
			"slow schedule. Operators answer the inbound work events and append a labelled memory " +
			"report after each task; the strategist — whose only clock is the cron schedule — is " +
			"briefed the newest report and rewrites operator prompts (worker_prompt_write, with a " +
			"rationale) when their standing orders keep producing a flaw. " +
			"Operators become <prefix>-1, <prefix>-2, ...",
		Questions: []Question{
			{
				ID:       TemporalQuestionOperatorPrefix,
				Prompt:   "Name prefix for the operators (kebab-case); they become <prefix>-1, <prefix>-2, ...",
				Type:     QuestionString,
				Default:  "operator",
				Required: true,
			},
			{
				ID:       TemporalQuestionOperatorCount,
				Prompt:   "How many operators?",
				Type:     QuestionChoice,
				Choices:  []string{"1", "2"},
				Default:  "1",
				Required: true,
			},
			{
				ID:       TemporalQuestionStrategistName,
				Prompt:   "Name for the strategist — the slow layer that reviews and retunes the operators (kebab-case).",
				Type:     QuestionString,
				Default:  "strategist",
				Required: true,
			},
			{
				ID:       TemporalQuestionCadence,
				Prompt:   "How often does the strategist review? Deliberately slow — the hierarchy is temporal.",
				Type:     QuestionChoice,
				Choices:  []string{"daily", "weekly"},
				Default:  "weekly",
				Required: true,
			},
			{
				ID:       TemporalQuestionInboundEvent,
				Prompt:   "The inbound event type that brings the operators their work (e.g. work.arrived).",
				Type:     QuestionString,
				Default:  "work.arrived",
				Required: true,
			},
			{
				ID: TemporalQuestionMemoryLabel,
				Prompt: "Label value for the operators' reports (a valid label value; reports are written " +
					"as kind=<label> and the newest one is briefed to the strategist).",
				Type:     QuestionString,
				Default:  "operator-reports",
				Required: true,
			},
			{
				ID:       TemporalQuestionMission,
				Prompt:   "What does this team work on? Folded into every prompt.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderTemporalHierarchy,
	})
}

// temporalOperatorPrompt is one operator's charter: do the work, file the
// report. It deliberately never names the strategist (or a fellow operator) —
// the upward channel is the label, not an address, and the operators' conduct
// must not depend on who reads the reports.
func temporalOperatorPrompt(mission, name string, ordinal, total int, inbound, label string) string {
	sel := "kind=" + label
	return strings.Join([]string{
		mission,
		fmt.Sprintf("%s operator %d of %d — the fast, tactical layer.", stageIdentity(name), ordinal, total),
		"Every " + inbound + " event is a task; handle it completely in your reply.",
		"After each task, use memory_create to append a short note labelled " + sel + " reporting what you did and how you approached it. These reports are the long-horizon layer's only window into your work — write each one to stand alone.",
	}, "\n")
}

// temporalStrategistPrompt is the slow layer's charter: review through the
// briefing, rewrite through worker_prompt_write, never do the work.
func temporalStrategistPrompt(mission, name string, operators []string, cadence, inbound, label string) string {
	sel := "kind=" + label
	return strings.Join([]string{
		mission,
		fmt.Sprintf("%s the strategist — the slow, long-horizon layer over %d operator(s): %s.",
			stageIdentity(name), len(operators), strings.Join(operators, ", ")),
		"You run on a " + cadence + " schedule, not on the work's own events: the operators answer " + inbound + " events; you review how they work. You never answer " + inbound + " events yourself.",
		"Your briefing section headed \"Your memory briefing: " + sel + "\" carries the NEWEST operator report (only the newest — history does not accumulate there). Read it before anything else; it is your only window into the operators' work.",
		"When a report shows a flaw an operator's standing orders would keep producing, use worker_prompt_read and worker_prompt_write to amend that operator's system prompt, with a rationale saying what was wrong. Amend rather than replace: keep every rule already there.",
	}, "\n")
}

// renderTemporalHierarchy is the pure renderer for temporal-hierarchy@v1.
func renderTemporalHierarchy(a Answers) (*Bundle, error) {
	prefix := a[TemporalQuestionOperatorPrefix].(string)
	countChoice := a[TemporalQuestionOperatorCount].(string)
	n, ok := temporalCounts[countChoice]
	if !ok {
		return nil, fmt.Errorf("%w: unknown operator count %q", ErrRender, countChoice)
	}
	strategist := a[TemporalQuestionStrategistName].(string)

	operators := make([]string, n)
	names := make([]namedWorker, 0, n+1)
	for i := range operators {
		operators[i] = fmt.Sprintf("%s-%d", prefix, i+1)
		names = append(names, namedWorker{TemporalQuestionOperatorPrefix, operators[i]})
	}
	names = append(names, namedWorker{TemporalQuestionStrategistName, strategist})
	if err := checkSeedWorkerNames(names); err != nil {
		return nil, err
	}

	mission, err := nonBlankAnswer(a, TemporalQuestionMission)
	if err != nil {
		return nil, err
	}

	inbound := a[TemporalQuestionInboundEvent].(string)
	if err := checkSeedEventType(TemporalQuestionInboundEvent, inbound); err != nil {
		return nil, err
	}

	label := a[TemporalQuestionMemoryLabel].(string)
	// Same reasoning as solo-memory/blackboard: the label rides into both the
	// operators' memory_create instruction and the strategist's briefing
	// selector, and blank would render the meaningless selector `kind=`.
	if strings.TrimSpace(label) == "" {
		return nil, fmt.Errorf("%w: %s must not be blank", ErrRender, TemporalQuestionMemoryLabel)
	}
	if err := agentdb.ValidateLabelValue(label); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrRender, TemporalQuestionMemoryLabel, err)
	}

	cadence := a[TemporalQuestionCadence].(string)
	cron, ok := soloCadenceCron[cadence]
	if !ok {
		// Unreachable through Instantiate (the choice list gates it); kept so
		// a direct Render call cannot emit an empty cron — solo.go's posture.
		return nil, fmt.Errorf("%w: unknown cadence %q", ErrRender, cadence)
	}

	workers := make([]agentdb.Worker, 0, n+1)
	subscriptions := make([]agentdb.Subscription, 0, n)
	for i, name := range operators {
		workers = append(workers, agentdb.Worker{
			Name:         name,
			Description:  fmt.Sprintf("Operator %d of %d — the fast layer: woken by every %s event, files a kind=%s report after each task.", i+1, n, inbound, label),
			SystemPrompt: temporalOperatorPrompt(mission, name, i+1, n, inbound, label),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		})
		subscriptions = append(subscriptions, agentdb.Subscription{
			EventType: inbound,
			Worker:    name,
			Enabled:   true,
		})
	}
	workers = append(workers, agentdb.Worker{
		Name:         strategist,
		Description:  "The strategist — the slow layer: runs on a " + cadence + " schedule (its only clock), is briefed the newest kind=" + label + " report and retunes operator prompts via worker_prompt_write.",
		SystemPrompt: temporalStrategistPrompt(mission, strategist, operators, cadence, inbound, label),
		Briefing:     agentdb.SelectorList{"kind=" + label},
		MaxInstances: agentdb.DefaultMaxInstances,
		Enabled:      true,
	})

	return &Bundle{
		Workers:       workers,
		Subscriptions: subscriptions,
		Schedules: []agentdb.Schedule{{
			Worker:  strategist,
			Cron:    cron,
			Input:   temporalScheduleInput,
			Enabled: true,
		}},
	}, nil
}
