package topology

// triagelab.go — topology library entry 14 (docs/product/10-topology-library.md
// §4, Family C: experiment topologies). The coordination org of
// docs/product/19-scenario-library.md §3 (SC-1): a dispatcher routes generated
// tickets whose correct destination the HARNESS computed and held out
// (go/triagelab), a methodology-critic improves HOW the dispatcher decides, and
// a FROZEN route-auditor compares stated routes against ground truth it is
// handed — never truth it generates.
//
// It is hypothesis-lab@v1's sibling by design. That seed measures analytical
// discipline; this one measures the failure class MAST puts at 37% of
// multi-agent failures and which the lab barely exercises — coordination and
// routing. Everything structural is copied deliberately: truth stays
// harness-side, the instrument ships frozen, the critic holds
// worker_prompt_write on exactly one worker and cannot touch the scoreboard,
// and the bundle carries no answers.
//
// # Honesty about the edges (the T4–T7 finding, inherited from supervisor@v1)
//
// A worker cannot post arbitrary typed events: the only routable event its work
// produces is `worker.finished`, whose text is its entire transcript. So the
// honest wiring is the supervisor's — every queue subscribes to the
// DISPATCHER's finish, all of them receive the same transcript, and addressing
// is an output convention stated in the prompts: the dispatcher ends its reply
// with `ROUTE-TO: <queue>`, and each queue acts only when that line names it.
// `escalate` is a legal value of that line and not a queue, which is what makes
// restraint measurable rather than indistinguishable from silence.
//
// # Wiring discipline
//
// The critic observes ONLY the dispatcher's finishes, holds no subscription to
// the auditor, and nothing anywhere routes the auditor's events — the
// instrument is causally isolated from the loop it measures, structurally, and
// pinned by test. The critic's prompt deliberately never names the auditor:
// prompts become event text downstream, and a name that travels is a name that
// matches the wrong mock-script rule (the L2 finding).

import (
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Triage-lab's question IDs.
const (
	TriageLabQuestionDispatcherName = "dispatcher-name"
	TriageLabQuestionFirstQueueName = "first-queue-name"
	TriageLabQuestionSecondQueue    = "second-queue-name"
	TriageLabQuestionThirdQueue     = "third-queue-name"
	TriageLabQuestionCriticName     = "critic-name"
	TriageLabQuestionAuditorName    = "auditor-name"
	TriageLabQuestionRoutingRules   = "routing-rules"
)

// TriageEscalateRoute is the reserved value of the ROUTE-TO line that means
// "no rule fits, a person should look". It is a ROUTE, not a refusal to give
// one — the whole ambiguity trap rests on the difference, and stating it as a
// constant keeps the charter, the tests and the harness spelling it the same.
const TriageEscalateRoute = "escalate"

func init() {
	Register(&Topology{
		Name:    "triage-lab",
		Version: "v1",
		Description: "The coordination org: a dispatcher routes tickets delivered as " +
			"<dispatcher-name>.task events to one of three queues (or escalates) using stated " +
			"content rules, a methodology-critic reviews its finishes and retunes HOW it decides " +
			"via worker_prompt_write, and a FROZEN route-auditor compares stated routes against " +
			"held-out truth the harness sends it in <auditor-name>.task events. Routing is a " +
			"ROUTE-TO line in the dispatcher's transcript, because workers cannot emit typed " +
			"events. Ground truth lives outside the project; the bundle ships none.",
		Questions: []Question{
			{
				ID:       TriageLabQuestionDispatcherName,
				Prompt:   "Name for the dispatcher — the worker that routes each ticket (kebab-case).",
				Type:     QuestionString,
				Default:  "dispatcher",
				Required: true,
			},
			{
				ID:       TriageLabQuestionFirstQueueName,
				Prompt:   "Name for the first specialist queue (kebab-case).",
				Type:     QuestionString,
				Default:  "billing-desk",
				Required: true,
			},
			{
				ID:       TriageLabQuestionSecondQueue,
				Prompt:   "Name for the second specialist queue (kebab-case).",
				Type:     QuestionString,
				Default:  "outage-desk",
				Required: true,
			},
			{
				ID:       TriageLabQuestionThirdQueue,
				Prompt:   "Name for the third specialist queue (kebab-case).",
				Type:     QuestionString,
				Default:  "access-desk",
				Required: true,
			},
			{
				ID:       TriageLabQuestionCriticName,
				Prompt:   "Name for the methodology critic — the worker that reviews and retunes how the dispatcher decides (kebab-case).",
				Type:     QuestionString,
				Default:  "methodology-critic",
				Required: true,
			},
			{
				ID:       TriageLabQuestionAuditorName,
				Prompt:   "Name for the route-auditor — the frozen comparator of stated routes against held-out truth (kebab-case).",
				Type:     QuestionString,
				Default:  "route-auditor",
				Required: true,
			},
			{
				ID: TriageLabQuestionRoutingRules,
				Prompt: "The content rules that decide a queue — folded verbatim into the dispatcher's charter, and the " +
					"only channel this topology offers into it. Name the queues exactly as you named them above " +
					"(e.g. \"a ticket stating a monetary amount that does not match an agreed one goes to billing-desk\").",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderTriageLab,
	})
}

// triageDispatcherPrompt is the routing charter, with the operator's content
// rules folded in verbatim.
//
// Three things it says that the experiment depends on, in this order:
// the rules ARE the authority; route on stated facts and not on what the ticket
// sounds like; and escalate when nothing fits. Those are the three behaviours a
// keyword router does not have, and they are what the critic gets to improve.
func triageDispatcherPrompt(dispatcher string, queues []string, rules string) string {
	return strings.Join([]string{
		specialistIdentity(dispatcher) + " the dispatcher. Each task event delivers one support ticket. Decide where it belongs and route it.",
		"Your queues are: " + strings.Join(queues, ", ") + ". You may also route to " + TriageEscalateRoute + ", which is a decision and not a failure to make one.",
		"Routing rules — these, and only these, decide a queue:",
		rules,
		"Route on the facts the ticket STATES, never on what it sounds like. A subject line is not a fact, and a ticket written in one queue's vocabulary may belong to another.",
		"When no rule fits what the ticket states, route to " + TriageEscalateRoute + ". A confident guess is worse than an honest escalation, because it looks like a decision.",
		"How routing works here — this is the whole mechanism, there is no other: you cannot post events yourself.",
		"When you finish, your ENTIRE transcript is broadcast (as one worker.finished event) to every queue at once.",
		"End your reply with a line of exactly this form, naming exactly one queue or " + TriageEscalateRoute + ":",
		routeLine("<queue-name>"),
		"Nothing may follow that line. Say which rule you applied before it.",
	}, "\n")
}

// triageQueuePrompt tells one queue who it is and when to act. It opens with
// specialistIdentity(name) — see that helper for why the exact phrase matters.
//
// The last sentence is not politeness: a queue that quietly re-routes a ticket
// it thinks was misrouted destroys the record of what the dispatcher decided,
// which is the only thing this scenario measures.
func triageQueuePrompt(name, dispatcher string) string {
	return strings.Join([]string{
		specialistIdentity(name) + " one of " + dispatcher + "'s queues.",
		"Every delivery you receive is " + dispatcher + "'s full finished transcript — every queue receives the same one.",
		"Handle the ticket only if the transcript contains the line \"" + routeLine(name) + "\".",
		"Otherwise reply with one line saying the ticket went elsewhere, and finish.",
		"Never re-decide the routing. If you think it was wrong, say so in your reply and stop: a queue that quietly takes work it was not given erases the record of what was decided.",
	}, "\n")
}

// triageCriticPrompt reviews METHOD, not answers — the critic has no access to
// ground truth and must not pretend otherwise. It deliberately names neither
// the auditor nor the queues: prompts are event text downstream, and a name
// that travels is a name that matches the wrong mock-script rule.
func triageCriticPrompt(dispatcher string) string {
	return strings.Join([]string{
		"You review " + dispatcher + "'s finished work. Each delivery you receive is " + dispatcher + "'s full transcript.",
		"Judge the METHOD, never the answer: did " + dispatcher + " decide from the facts the ticket STATED, or from what the ticket sounded like?",
		"Did it name the rule it applied? Did it escalate when no rule fitted, instead of guessing a queue?",
		"When the method falls short in a way " + dispatcher + "'s standing orders would keep producing,",
		"use worker_prompt_read and worker_prompt_write to amend " + dispatcher + "'s system prompt, with a rationale",
		"saying what was methodologically wrong. Amend rather than replace: keep every rule already there, and keep its output contract intact.",
		"You hold no ground truth — whether a route was CORRECT is checked outside your reach, by a frozen instrument that is not yours to tune.",
	}, "\n")
}

// triageAuditorPrompt is the comparator's charter: judge the stated route
// against the truth IN ITS INPUT, refuse when the truth is missing, decide
// nothing itself.
func triageAuditorPrompt(name string) string {
	return strings.Join([]string{
		specialistIdentity(name) + " the frozen route-auditor. Each task event you receive carries the route a dispatcher stated AND the held-out correct route for the same ticket, both in the event text.",
		"Compare them and reply with a line \"Verdict: match\" or \"Verdict: mismatch\", plus one sentence saying why.",
		"You never decide a route yourself: if the event text does not state the correct route, say so and refuse to judge.",
		"Your own configuration is frozen so the loop you audit cannot rewrite its own scoreboard.",
	}, "\n")
}

// renderTriageLab is the pure renderer for triage-lab@v1.
func renderTriageLab(a Answers) (*Bundle, error) {
	dispatcher := a[TriageLabQuestionDispatcherName].(string)
	first := a[TriageLabQuestionFirstQueueName].(string)
	second := a[TriageLabQuestionSecondQueue].(string)
	third := a[TriageLabQuestionThirdQueue].(string)
	critic := a[TriageLabQuestionCriticName].(string)
	auditor := a[TriageLabQuestionAuditorName].(string)
	if err := checkSeedWorkerNames([]namedWorker{
		{TriageLabQuestionDispatcherName, dispatcher},
		{TriageLabQuestionFirstQueueName, first},
		{TriageLabQuestionSecondQueue, second},
		{TriageLabQuestionThirdQueue, third},
		{TriageLabQuestionCriticName, critic},
		{TriageLabQuestionAuditorName, auditor},
	}); err != nil {
		return nil, err
	}
	rules, err := nonBlankAnswer(a, TriageLabQuestionRoutingRules)
	if err != nil {
		return nil, err
	}
	queues := []string{first, second, third}

	workers := []agentdb.Worker{
		{
			Name:         dispatcher,
			Description:  "The dispatcher: routes each ticket delivered by " + TaskEventType(dispatcher) + " events to one queue, or escalates. Its METHOD is what the critic improves.",
			SystemPrompt: triageDispatcherPrompt(dispatcher, queues, rules),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		},
	}
	subscriptions := []agentdb.Subscription{
		{
			// The harness delivers each ticket here.
			EventType: TaskEventType(dispatcher),
			Worker:    dispatcher,
			Enabled:   true,
		},
	}
	for _, name := range queues {
		workers = append(workers, agentdb.Worker{
			Name:         name,
			Description:  "A specialist queue on " + dispatcher + "'s desk; handles a ticket only when the dispatcher's ROUTE-TO line names it.",
			SystemPrompt: triageQueuePrompt(name, dispatcher),
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
	workers = append(workers,
		agentdb.Worker{
			Name:         critic,
			Description:  "The methodology critic: reviews " + dispatcher + "'s finished routings and retunes how it decides via worker_prompt_write. It judges method, never truth, and cannot touch the frozen auditor.",
			SystemPrompt: triageCriticPrompt(dispatcher),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
		},
		agentdb.Worker{
			Name:         auditor,
			Description:  "The FROZEN route-auditor: compares a stated route against the held-out truth the harness sends in " + TaskEventType(auditor) + " events. It never decides a route, and no worker may change it — only a human.",
			SystemPrompt: triageAuditorPrompt(auditor),
			MaxInstances: agentdb.DefaultMaxInstances,
			Enabled:      true,
			// The instrument ships frozen from the first moment the org exists —
			// the same guarantee frozen-scorer@v1 established.
			Frozen: true,
		},
	)
	subscriptions = append(subscriptions,
		agentdb.Subscription{
			// The critic observes the dispatcher's finishes — and only those
			// (§8.4: filtered so it never reacts to itself, to a queue, or to
			// the auditor).
			EventType: agentdb.EventTypeWorkerFinished,
			Filter:    agentdb.JSONMap{"worker": dispatcher},
			Worker:    critic,
			Enabled:   true,
		},
		agentdb.Subscription{
			// The harness-side audit channel: stated route + held-out truth
			// arrive together as event text. This is the ONLY route truth takes
			// into the project, and it terminates at the frozen comparator —
			// nothing subscribes to the auditor's own events.
			EventType: TaskEventType(auditor),
			Worker:    auditor,
			Enabled:   true,
		},
	)

	return &Bundle{
		Workers:       workers,
		Subscriptions: subscriptions,
		Schedules:     []agentdb.Schedule{},
	}, nil
}
