package topology

// escalation.go — topology library entry 11 (docs/product/10-topology-library.md
// §4, Family B): the practical shape for real work (§8.8's marketing manager).
// One worker, one inbound subscription, and a charter that draws the line the
// runtime already enforces: handle the routine yourself; for anything
// irreversible, out of policy, or uncertain, call `request_human_attention` —
// the core MCP tool every session holds — and the delivery PARKS at
// `awaiting_human` (a pause, not an end: §8.4/§9) until a person answers.
//
// The seed builds nothing: the tool, the parked status and the
// attention_requested envelope stamp all exist. Its contribution is the
// prompt's honest description of when to stop and what stopping means — a
// worker that believes pausing is failure will guess instead, which is the
// exact defect (MAST FM-2.2, doc 11 S4) the escalation shape exists to avoid.

import (
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Escalation's question IDs.
const (
	EscalationQuestionWorkerName   = "worker-name"
	EscalationQuestionInboundEvent = "inbound-event-type"
	EscalationQuestionMission      = "mission"
)

func init() {
	Register(&Topology{
		Name:    "escalation",
		Version: "v1",
		Description: "One worker with an escalation valve (entry 11): it handles routine cases from " +
			"the inbound event itself, and calls request_human_attention — parking the job at " +
			"awaiting_human — for anything irreversible, out of policy, or uncertain. The practical " +
			"shape for real work; exercises the awaiting_human path end to end.",
		Questions: []Question{
			{
				ID:       EscalationQuestionWorkerName,
				Prompt:   "Name for the worker (kebab-case, e.g. case-handler).",
				Type:     QuestionString,
				Default:  "handler",
				Required: true,
			},
			{
				ID:       EscalationQuestionInboundEvent,
				Prompt:   "The inbound event type that brings the cases (e.g. case.arrived).",
				Type:     QuestionString,
				Default:  "case.arrived",
				Required: true,
			},
			{
				ID:       EscalationQuestionMission,
				Prompt:   "What cases does this worker handle, and what does its policy cover? Folded into its prompt.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderEscalation,
	})
}

// escalationPrompt is the worker's charter: the routine/escalate line, and the
// honest meaning of the pause.
func escalationPrompt(mission, name, inbound string) string {
	return strings.Join([]string{
		mission,
		stageIdentity(name) + " the escalation topology's single worker: handle the routine yourself, pause for a human on everything else.",
		"Every " + inbound + " event is a case. When it is routine — reversible, inside policy, and you are sure of the answer — handle it completely and say what you did.",
		"When it is not — irreversible, outside policy, or you are uncertain — do NOT guess and do NOT act: call request_human_attention with a message saying exactly what you need decided and why you stopped. The job then parks awaiting a human instead of finishing. That pause is the correct outcome, not a failure.",
	}, "\n")
}

// renderEscalation is the pure renderer for escalation@v1.
func renderEscalation(a Answers) (*Bundle, error) {
	name := a[EscalationQuestionWorkerName].(string)
	if err := checkSeedWorkerNames([]namedWorker{{EscalationQuestionWorkerName, name}}); err != nil {
		return nil, err
	}
	mission, err := nonBlankAnswer(a, EscalationQuestionMission)
	if err != nil {
		return nil, err
	}
	inbound := a[EscalationQuestionInboundEvent].(string)
	if err := checkSeedEventType(EscalationQuestionInboundEvent, inbound); err != nil {
		return nil, err
	}

	return &Bundle{
		Workers: []agentdb.Worker{{
			Name:         name,
			Description:  "The escalation worker: handles routine " + inbound + " cases itself and calls request_human_attention — parking the job at awaiting_human — for anything irreversible, out of policy, or uncertain.",
			SystemPrompt: escalationPrompt(mission, name, inbound),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		}},
		Subscriptions: []agentdb.Subscription{{
			EventType: inbound,
			Worker:    name,
			Enabled:   true,
		}},
		Schedules: []agentdb.Schedule{},
	}, nil
}
