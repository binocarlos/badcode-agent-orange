package topology

// selforganizing.go — topology library entry 9 (docs/product/10-topology-library.md
// §4, Family B): the self-organizing pool. The bundle is deliberately the
// smallest in the library — ONE founder worker and ONE inbound subscription —
// because the org chart is not the seed's to draw: roles emerge at runtime,
// with the founder hiring and wiring through the management tools the core
// MCP server already mounts for every session (mcpserver.go: worker_*,
// subscription_*, schedule_* — nothing here grants them, nothing could revoke
// them). The seed's entire contribution is the PROMPT: an honest charter over
// that existing surface.
//
// D3 (decided by Kai, 2026-07-27): the pool is UNCAPPED. The renderer ships
// no SettingsPatch — no MaxConcurrentJobs override, no token-cap override —
// so the only brakes are whatever the project already has. Most faithful to
// the research question (emergent structure beat designed structure only
// above a capability threshold), highest runaway risk, accepted; the e2e runs
// against a deliberately narrowed port pool for exactly that reason.
//
// Honesty notes folded into the prompt, each pinned by test:
//   - there is NO worker_delete tool (§9 never gave workers one; the F1
//     discovery) — retirement is worker_update {enabled: false};
//   - worker_create refuses an existing name ("hiring is not overwriting");
//   - an unwired worker never runs — hiring without subscription_create (or
//     schedule_create) builds an org of statues;
//   - frozen rows refuse worker writes.

import (
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Self-organizing's question IDs.
const (
	SelfOrganizingQuestionFounderName  = "founder-name"
	SelfOrganizingQuestionInboundEvent = "inbound-event-type"
	SelfOrganizingQuestionMission      = "mission"
)

// The management surface the founder's charter describes — the core MCP tools
// every session in a product-layer project already holds (cmd/agentd/
// mcp_management.go registrations). The prompt is BUILT from these lists so
// the charter and the tests cannot drift apart; if the tool surface ever
// changes, change these lists and the prompt follows.
var (
	selfOrganizingWorkerTools       = []string{"worker_list", "worker_create", "worker_update", "worker_prompt_read", "worker_prompt_write"}
	selfOrganizingSubscriptionTools = []string{"subscription_list", "subscription_create", "subscription_delete"}
	selfOrganizingScheduleTools     = []string{"schedule_list", "schedule_create", "schedule_update", "schedule_delete"}
)

func init() {
	Register(&Topology{
		Name:    "self-organizing",
		Version: "v1",
		Description: "The self-organizing pool (entry 9): one founder worker on one inbound " +
			"subscription, and nothing else — the org chart is not pre-drawn. The founder's " +
			"charter tells it to hire by creating workers and wiring subscriptions/schedules " +
			"through the management tools every worker already holds; roles emerge at runtime. " +
			"UNCAPPED by design (D3): no concurrency or token override ships with the seed — " +
			"the only brakes are the project's own.",
		Questions: []Question{
			{
				ID:       SelfOrganizingQuestionFounderName,
				Prompt:   "Name for the founder — the first (and initially only) worker (kebab-case).",
				Type:     QuestionString,
				Default:  "founder",
				Required: true,
			},
			{
				ID:       SelfOrganizingQuestionInboundEvent,
				Prompt:   "The inbound event type that brings the pool its missions (e.g. org.mission).",
				Type:     QuestionString,
				Default:  "org.mission",
				Required: true,
			},
			{
				ID:       SelfOrganizingQuestionMission,
				Prompt:   "What is this pool for? The founder decides the org; this is what the org is FOR.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderSelfOrganizing,
	})
}

// founderPrompt is the seed's whole contribution: an honest charter over the
// management surface every worker already holds.
func founderPrompt(mission, name, inbound string) string {
	return strings.Join([]string{
		mission,
		stageIdentity(name) + " the founding worker of a self-organizing pool. There is no fixed org chart: you decide the organization, and you build it with the management tools every worker in this project already holds:",
		"- " + strings.Join(selfOrganizingWorkerTools, ", ") + " — hire workers and tune their standing orders. worker_create refuses a name that already exists (hiring is not overwriting); worker_prompt_write always takes a rationale; rows a human froze refuse worker writes.",
		"- " + strings.Join(selfOrganizingSubscriptionTools, ", ") + " — wire who is woken by which events. An unwired worker never runs: wire each hire's wake-up when you create it, or you have hired a statue.",
		"- " + strings.Join(selfOrganizingScheduleTools, ", ") + " — give workers a clock when their duty is periodic rather than event-driven.",
		"There is no worker_delete tool: to retire a worker, disable it with worker_update.",
		"Every " + inbound + " event is a mission for the pool. Judge what the mission needs: do it yourself, or hire the roles it calls for — put in each hire's system prompt everything it needs to know, because hires do not inherit yours. You may reorganize on later missions: retune, rewire, retire.",
		"The pool is uncapped by design: no structural limit stops you from growing it, and the only brakes are the project's own (concurrent-job and token caps). Grow it deliberately, and only as far as the mission warrants.",
	}, "\n")
}

// renderSelfOrganizing is the pure renderer for self-organizing@v1.
func renderSelfOrganizing(a Answers) (*Bundle, error) {
	name := a[SelfOrganizingQuestionFounderName].(string)
	if err := checkSeedWorkerNames([]namedWorker{{SelfOrganizingQuestionFounderName, name}}); err != nil {
		return nil, err
	}
	mission, err := nonBlankAnswer(a, SelfOrganizingQuestionMission)
	if err != nil {
		return nil, err
	}
	inbound := a[SelfOrganizingQuestionInboundEvent].(string)
	if err := checkSeedEventType(SelfOrganizingQuestionInboundEvent, inbound); err != nil {
		return nil, err
	}

	return &Bundle{
		Workers: []agentdb.Worker{{
			Name:         name,
			Description:  "The founder of the self-organizing pool: woken by " + inbound + " events, holds the charter to hire, wire and reorganize through the shared management tools. The rest of the org emerges at runtime.",
			SystemPrompt: founderPrompt(mission, name, inbound),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		}},
		Subscriptions: []agentdb.Subscription{{
			EventType: inbound,
			Worker:    name,
			Enabled:   true,
		}},
		Schedules: []agentdb.Schedule{},
		// NO SettingsPatch — that absence IS decision D3 (uncapped), pinned by
		// TestSelfOrganizingIsUncapped. Adding a concurrency or token override
		// here would quietly reverse a recorded decision.
	}, nil
}
