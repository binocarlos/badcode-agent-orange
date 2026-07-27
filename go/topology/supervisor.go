package topology

// supervisor.go — topology library entry 5 (docs/product/10-topology-library.md
// §4, Family B): a dispatcher worker plus N specialists — the star shape most
// production deployments reach for.
//
// # Honesty about the edges
//
// The catalogue row says "dispatcher fans out by emitting typed events", but a
// worker cannot post arbitrary typed events today: the only routable event a
// worker's work produces is `worker.finished`, whose text is its entire
// finished transcript (docs/18-workers-memory-events.md §8.2). So the honest
// wiring is: every specialist subscribes to the DISPATCHER's `worker.finished`
// (filtered to it), all of them receive the same transcript, and addressing is
// an output convention documented in the prompts — the dispatcher ends its
// reply with a `ROUTE-TO: <specialist>` line, and each specialist acts only
// when that line names it. The prompts say exactly this, because a prompt that
// promises event-emission machinery that does not exist would be teaching the
// model to fail.

import (
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Supervisor's question IDs.
const (
	SupervisorQuestionDispatcherName   = "dispatcher-name"
	SupervisorQuestionSpecialistPrefix = "specialist-prefix"
	SupervisorQuestionSpecialistCount  = "specialist-count"
	SupervisorQuestionInboundEvent     = "inbound-event-type"
	SupervisorQuestionMission          = "mission"
)

// routeLine is the addressing convention the prompts document: the dispatcher
// ends its reply with this line naming exactly one specialist.
func routeLine(specialist string) string { return "ROUTE-TO: " + specialist }

// specialistIdentity opens every specialist's prompt. Pinned as a helper (and
// by test) because it doubles as the unique per-specialist key: the dispatcher's
// prompt lists specialist NAMES but never contains this phrase, so body-substring
// machinery (mock scripts) can partition specialist requests from the
// dispatcher's even though each side's requests contain the other's name.
func specialistIdentity(name string) string { return "You are " + name + "," }

func init() {
	Register(&Topology{
		Name:    "supervisor",
		Version: "v1",
		Description: "A dispatcher and N specialists (the star). The dispatcher is woken by an " +
			"inbound event type; its finished transcript is broadcast to every specialist, and " +
			"a ROUTE-TO line in the transcript says which one should act — an output convention " +
			"documented in the prompts, since workers cannot emit arbitrary typed events.",
		Questions: []Question{
			{
				ID:       SupervisorQuestionDispatcherName,
				Prompt:   "Name for the dispatcher (kebab-case).",
				Type:     QuestionString,
				Default:  "dispatcher",
				Required: true,
			},
			{
				ID: SupervisorQuestionSpecialistPrefix,
				Prompt: "Name prefix for the specialists (kebab-case); they become <prefix>-1, <prefix>-2, ... " +
					"It must not overlap the dispatcher's name.",
				Type:     QuestionString,
				Default:  "specialist",
				Required: true,
			},
			{
				ID:       SupervisorQuestionSpecialistCount,
				Prompt:   "How many specialists?",
				Type:     QuestionChoice,
				Choices:  []string{"2", "3"},
				Default:  "2",
				Required: true,
			},
			{
				ID:       SupervisorQuestionInboundEvent,
				Prompt:   "The inbound event type that wakes the dispatcher (e.g. task.requested).",
				Type:     QuestionString,
				Default:  "task.requested",
				Required: true,
			},
			{
				ID:       SupervisorQuestionMission,
				Prompt:   "What does this team handle? Folded into every worker's prompt.",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderSupervisor,
	})
}

// specialistCounts maps the choice answer to its integer. A closed map, like
// solo's cadence table: the choice list gates Instantiate, and a direct Render
// call with an unlisted count fails loudly rather than rendering zero workers.
var specialistCounts = map[string]int{"2": 2, "3": 3}

// renderSupervisor is the pure renderer for supervisor@v1.
func renderSupervisor(a Answers) (*Bundle, error) {
	dispatcher := a[SupervisorQuestionDispatcherName].(string)
	prefix := a[SupervisorQuestionSpecialistPrefix].(string)
	countChoice := a[SupervisorQuestionSpecialistCount].(string)
	n, ok := specialistCounts[countChoice]
	if !ok {
		return nil, fmt.Errorf("%w: unknown specialist count %q", ErrRender, countChoice)
	}

	specialists := make([]string, n)
	names := []namedWorker{{SupervisorQuestionDispatcherName, dispatcher}}
	for i := range specialists {
		specialists[i] = fmt.Sprintf("%s-%d", prefix, i+1)
		names = append(names, namedWorker{SupervisorQuestionSpecialistPrefix, specialists[i]})
	}
	if err := checkSeedWorkerNames(names); err != nil {
		return nil, err
	}

	mission, err := nonBlankAnswer(a, SupervisorQuestionMission)
	if err != nil {
		return nil, err
	}

	inbound := a[SupervisorQuestionInboundEvent].(string)
	if err := checkSeedEventType(SupervisorQuestionInboundEvent, inbound); err != nil {
		return nil, err
	}

	workers := []agentdb.Worker{{
		Name:         dispatcher,
		Description:  "The dispatcher: triages every inbound " + inbound + " event and routes it to one specialist via the ROUTE-TO line.",
		SystemPrompt: dispatcherPrompt(mission, dispatcher, specialists),
		MaxInstances: agentdb.DefaultMaxInstances,
		Enabled:      true,
	}}
	subscriptions := []agentdb.Subscription{{
		EventType: inbound,
		Worker:    dispatcher,
		Enabled:   true,
	}}
	for i, name := range specialists {
		workers = append(workers, agentdb.Worker{
			Name:         name,
			Description:  fmt.Sprintf("Specialist %d of %d on %s's team; acts only when the dispatcher's ROUTE-TO line names it.", i+1, n, dispatcher),
			SystemPrompt: specialistPrompt(mission, name, i+1, n, dispatcher),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		})
		subscriptions = append(subscriptions, agentdb.Subscription{
			EventType: agentdb.EventTypeWorkerFinished,
			Filter:    agentdb.JSONMap{"worker": dispatcher},
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

// dispatcherPrompt documents the whole convention, honestly: how work arrives,
// what "emitting" really is here, and how to address a specialist.
func dispatcherPrompt(mission, dispatcher string, specialists []string) string {
	return strings.Join([]string{
		mission,
		fmt.Sprintf("You are %s, the dispatcher: you take each incoming request and decide which specialist should handle it.", dispatcher),
		"Your specialists are: " + strings.Join(specialists, ", ") + ".",
		"How routing works here — this is the whole mechanism, there is no other: you cannot post events yourself.",
		"When you finish, your ENTIRE transcript is broadcast (as one worker.finished event) to every specialist at once.",
		"To route the request, end your reply with a line of exactly this form, naming exactly one specialist:",
		routeLine("<specialist-name>"),
		"Each specialist acts only when that line names it, and declines otherwise. Do not do the specialist's work yourself.",
	}, "\n")
}

// specialistPrompt tells one specialist who it is and when to act. It opens
// with specialistIdentity(name) — see that helper for why the exact phrase
// matters.
func specialistPrompt(mission, name string, ordinal, total int, dispatcher string) string {
	return strings.Join([]string{
		mission,
		fmt.Sprintf("%s specialist %d of %d on the dispatcher %s's team.", specialistIdentity(name), ordinal, total, dispatcher),
		fmt.Sprintf("Every delivery you receive is %s's full finished transcript — every specialist receives the same one.", dispatcher),
		fmt.Sprintf("Act on the request only if the transcript contains the line %q.", routeLine(name)),
		"Otherwise reply with one line saying the item was routed to someone else, and finish.",
	}, "\n")
}

// checkSeedEventType is the renderer-side rule on an operator-supplied inbound
// event type. Stricter than the subscription store on purpose: a seed's
// trigger must be a plain external type — no wildcards (a seed's wiring should
// be exact), and not in the `worker.` namespace core emits, because a
// dispatcher subscribed to worker.finished would be woken by its own
// specialists finishing, a loop no one asked for.
func checkSeedEventType(qid, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%w: %s must not be blank", ErrRender, qid)
	}
	if v != strings.TrimSpace(v) {
		return fmt.Errorf("%w: %s must not have surrounding whitespace", ErrRender, qid)
	}
	if strings.Contains(v, "*") {
		return fmt.Errorf("%w: %s must be a plain event type, not a wildcard pattern", ErrRender, qid)
	}
	if strings.HasPrefix(v, "worker.") {
		return fmt.Errorf("%w: %s must not be in the worker.* namespace core emits (a dispatcher woken by worker.finished would react to its own specialists)", ErrRender, qid)
	}
	return nil
}
