package topology

// solo.go — topology library entry 1 (docs/product/10-topology-library.md §4,
// Family A: controls). One worker, one schedule. It exists to answer "does any
// multi-agent structure beat one good agent?" — and, in this package, to prove
// the machinery end to end and be the basis T4 builds on.

import (
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Solo's question IDs, named so tests and T4 don't scatter string literals.
const (
	SoloQuestionWorkerName = "worker-name"
	SoloQuestionPromptSeed = "prompt-seed"
	SoloQuestionCadence    = "cadence"
)

// soloCadenceCron maps the cadence choice to a 5-field cron expression. The
// fixed 09:00 anchor (stack-local time) is deliberate: a control topology
// should be boring and predictable, and an operator who wants a different hour
// edits the schedule afterwards — it is an ordinary config row.
var soloCadenceCron = map[string]string{
	"hourly": "0 * * * *",
	"daily":  "0 9 * * *",
	"weekly": "0 9 * * 1",
}

// soloScheduleInput is the instruction every firing delivers (the schedule's
// `input` becomes the event text — a schedule says what the worker is told,
// not just when it runs).
const soloScheduleInput = "Scheduled run: carry out the standing instructions in your system prompt, then finish."

func init() {
	Register(&Topology{
		Name:    "solo",
		Version: "v1",
		Description: "One worker on one schedule. The control: does any multi-agent " +
			"structure beat a single good agent? No subscriptions, no shared memory, " +
			"no preconditions.",
		Questions: []Question{
			{
				ID:       SoloQuestionWorkerName,
				Prompt:   "Name for the worker (kebab-case, e.g. daily-writer).",
				Type:     QuestionString,
				Default:  "solo",
				Required: true,
			},
			{
				ID:       SoloQuestionPromptSeed,
				Prompt:   "What should this worker do? This becomes its starting system prompt.",
				Type:     QuestionString,
				Required: true,
			},
			{
				ID:       SoloQuestionCadence,
				Prompt:   "How often should it run?",
				Type:     QuestionChoice,
				Choices:  []string{"hourly", "daily", "weekly"},
				Default:  "daily",
				Required: true,
			},
		},
		Render: renderSolo,
	})
}

// renderSolo is the pure renderer for solo@v1. Answers arrive resolved
// (defaults applied, types checked); semantic checks on the strings live here.
func renderSolo(a Answers) (*Bundle, error) {
	name := a[SoloQuestionWorkerName].(string)
	if err := agentdb.ValidateWorkerName(name); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrRender, SoloQuestionWorkerName, err)
	}
	prompt := a[SoloQuestionPromptSeed].(string)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("%w: %s must not be blank", ErrRender, SoloQuestionPromptSeed)
	}
	cadence := a[SoloQuestionCadence].(string)
	cron, ok := soloCadenceCron[cadence]
	if !ok {
		// Unreachable through Instantiate (the choice list gates it); kept so a
		// direct Render call cannot emit an empty cron.
		return nil, fmt.Errorf("%w: unknown cadence %q", ErrRender, cadence)
	}
	return &Bundle{
		Workers: []agentdb.Worker{{
			Name:         name,
			Description:  "The solo topology's single worker. Runs on a " + cadence + " schedule; no other workers exist.",
			SystemPrompt: prompt,
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		}},
		Subscriptions: []agentdb.Subscription{},
		Schedules: []agentdb.Schedule{{
			Worker:  name,
			Cron:    cron,
			Input:   soloScheduleInput,
			Enabled: true,
		}},
	}, nil
}
