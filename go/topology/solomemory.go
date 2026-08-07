package topology

// solomemory.go — topology library entry 2 (docs/product/10-topology-library.md
// §4, Family A: controls). Solo plus the memory channel: one worker, one
// schedule — exactly solo@v1's shape — but the worker's standing orders tell it
// to append a labelled memory note after each task, and the worker row carries
// a briefing selector matching that label, so the newest note rides into every
// future job's composed prompt (§7.4). It exists to answer "does memory alone
// help?": comparing solo@v1 with solo-memory@v1 varies nothing but this
// channel.
//
// The channel is made of existing parts, verbatim: `memory_create` (the core
// MCP tool every worker already holds) on the write side, and the ordinary
// `worker.briefing` selector list on the read side. Nothing new is built; the
// seed only wires the two ends of §7 together and says so in the prompt.
//
// One honest limitation, stated in the prompt too: a briefing section carries
// the NEWEST matching note, not the whole history (BuildBriefingSections uses
// NewestMemory per selector). The convention is therefore solo-memory's whole
// discipline: each note should carry what the next run needs, because it is
// the only note the next run will be handed.

import (
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Solo-memory's question IDs. The first three are deliberately solo@v1's
// (same IDs, same prompts, same defaults where sensible): an experimenter
// comparing the two controls should be varying the memory channel, not
// accidental question wording.
const (
	SoloMemoryQuestionWorkerName  = "worker-name"
	SoloMemoryQuestionPromptSeed  = "prompt-seed"
	SoloMemoryQuestionCadence     = "cadence"
	SoloMemoryQuestionMemoryLabel = "memory-label"
)

// soloMemorySelector is the briefing selector derived from the label answer.
// The `kind=` key is the documented memory convention ("kind=<something> says
// what sort of memory this is" — mcp_memory.go's tool description), so the
// notes this seed writes are findable by the same convention every other
// reader of the store uses.
func soloMemorySelector(label string) string { return "kind=" + label }

func init() {
	Register(&Topology{
		Name:    "solo-memory",
		Version: "v1",
		Description: "Solo plus memory (control 2): one worker on one schedule that appends a " +
			"labelled memory note after each task and carries a briefing selector matching that " +
			"label — the newest note rides into every future job. Comparing this with solo@v1 " +
			"measures whether the memory channel alone helps. No subscriptions, no other workers.",
		Questions: []Question{
			{
				ID:       SoloMemoryQuestionWorkerName,
				Prompt:   "Name for the worker (kebab-case, e.g. daily-writer).",
				Type:     QuestionString,
				Default:  "solo-memory",
				Required: true,
			},
			{
				ID:       SoloMemoryQuestionPromptSeed,
				Prompt:   "What should this worker do? This becomes its starting system prompt.",
				Type:     QuestionString,
				Required: true,
			},
			{
				ID:       SoloMemoryQuestionCadence,
				Prompt:   "How often should it run?",
				Type:     QuestionChoice,
				Choices:  []string{"hourly", "daily", "weekly"},
				Default:  "daily",
				Required: true,
			},
			{
				ID: SoloMemoryQuestionMemoryLabel,
				Prompt: "Label value for the worker's task notes (a valid label value; the notes are " +
					"written as kind=<label> and the newest one is briefed back).",
				Type:     QuestionString,
				Default:  "task-notes",
				Required: true,
			},
		},
		Render: renderSoloMemory,
	})
}

// soloMemoryPrompt appends the memory discipline to the operator's seed. The
// quoted heading mirrors compose.go's briefingHeadingPrefix convention
// ("Your memory briefing: <selector>") — pinned against the real constant by
// TestSoloMemoryPromptNamesTheRealBriefingHeading, so the prompt can never
// drift into describing a section that does not exist.
func soloMemoryPrompt(seed, label string) string {
	sel := soloMemorySelector(label)
	return strings.Join([]string{
		seed,
		"Memory discipline — this is how your past runs reach your future ones:",
		"- After each task, use memory_create to append a short note labelled " + sel + " recording what you did and anything the next run should know.",
		"- Your briefing section headed \"Your memory briefing: " + sel + "\" carries the NEWEST such note (only the newest — history does not accumulate there). Read it before you start, and write each note so it stands alone.",
	}, "\n")
}

// renderSoloMemory is the pure renderer for solo-memory@v1.
func renderSoloMemory(a Answers) (*Bundle, error) {
	name := a[SoloMemoryQuestionWorkerName].(string)
	if err := agentdb.ValidateWorkerName(name); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrRender, SoloMemoryQuestionWorkerName, err)
	}
	seed, err := nonBlankAnswer(a, SoloMemoryQuestionPromptSeed)
	if err != nil {
		return nil, err
	}
	label := a[SoloMemoryQuestionMemoryLabel].(string)
	// A label value, not free prose: it is interpolated into both the
	// memory_create instruction and the briefing selector, and an invalid value
	// would render a selector the store refuses at read time — the class of
	// failure render-time validation exists to make impossible. Blank is
	// refused too (ValidateLabelValue allows "" because K8s does, but a
	// selector of `kind=` matching only unlabelled-value notes is never what
	// an operator meant).
	if strings.TrimSpace(label) == "" {
		return nil, fmt.Errorf("%w: %s must not be blank", ErrRender, SoloMemoryQuestionMemoryLabel)
	}
	if err := agentdb.ValidateLabelValue(label); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrRender, SoloMemoryQuestionMemoryLabel, err)
	}
	cadence := a[SoloMemoryQuestionCadence].(string)
	cron, ok := soloCadenceCron[cadence]
	if !ok {
		// Unreachable through Instantiate (the choice list gates it); kept so a
		// direct Render call cannot emit an empty cron — solo.go's posture.
		return nil, fmt.Errorf("%w: unknown cadence %q", ErrRender, cadence)
	}

	return &Bundle{
		Workers: []agentdb.Worker{{
			Name: name,
			Description: "The solo-memory topology's single worker. Runs on a " + cadence +
				" schedule; writes a " + soloMemorySelector(label) + " note after each task and is briefed the newest one.",
			SystemPrompt: soloMemoryPrompt(seed, label),
			Briefing:     agentdb.SelectorList{soloMemorySelector(label)},
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
