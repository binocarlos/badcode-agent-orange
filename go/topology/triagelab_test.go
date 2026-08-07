package topology

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func triageLabV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("triage-lab", "v1")
	if !ok {
		t.Fatal("triage-lab@v1 not registered")
	}
	return top
}

const triageRules = "a ticket stating a monetary amount that does not match an agreed one goes to billing-desk; " +
	"a ticket stating an HTTP 5xx or a timeout goes to outage-desk; " +
	"a ticket stating that someone cannot sign in or was denied permission goes to access-desk"

// The reference render: dispatcher + three queues + methodology-critic + frozen
// route-auditor, with the routing rules folded into the dispatcher's charter
// and truth arriving only over the auditor's own task channel.
func TestTriageLabRenderDefaults(t *testing.T) {
	b, err := triageLabV1(t).Instantiate(Answers{
		TriageLabQuestionRoutingRules: triageRules,
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 6 {
		t.Fatalf("workers: want 6, got %d", len(b.Workers))
	}
	dispatcher := b.Workers[0]
	queues := b.Workers[1:4]
	critic, auditor := b.Workers[4], b.Workers[5]
	wantNames := []string{"dispatcher", "billing-desk", "outage-desk", "access-desk", "methodology-critic", "route-auditor"}
	for i, want := range wantNames {
		if b.Workers[i].Name != want {
			t.Fatalf("worker %d: want %q (the default), got %q", i, want, b.Workers[i].Name)
		}
	}

	// The instrument — and only the instrument — ships frozen.
	if !auditor.Frozen {
		t.Error("the route-auditor must render Frozen: true")
	}
	for _, w := range b.Workers[:5] {
		if w.Frozen {
			t.Errorf("only the auditor is the instrument; %q must not render frozen", w.Name)
		}
	}
	for _, w := range b.Workers {
		if !w.Enabled {
			t.Errorf("worker %q must render enabled", w.Name)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
	}

	// The dispatcher's charter: identity phrase, the roster, the rules verbatim,
	// the three behaviours a keyword router does not have, and the output
	// contract.
	if !strings.HasPrefix(dispatcher.SystemPrompt, "You are dispatcher,") {
		t.Errorf("dispatcher prompt must open with its identity phrase; got %q", dispatcher.SystemPrompt)
	}
	for _, want := range []string{
		triageRules,
		"billing-desk, outage-desk, access-desk",
		"facts the ticket STATES",
		"subject line is not a fact",
		"route to escalate",
		"ROUTE-TO: <queue-name>",
		"Nothing may follow that line",
		"which rule you applied",
	} {
		if !strings.Contains(dispatcher.SystemPrompt, want) {
			t.Errorf("dispatcher prompt must contain %q; got:\n%s", want, dispatcher.SystemPrompt)
		}
	}

	// Each queue: its own identity phrase, its own ROUTE-TO line, and the
	// standing order not to re-route.
	for _, q := range queues {
		if !strings.HasPrefix(q.SystemPrompt, "You are "+q.Name+",") {
			t.Errorf("queue %q must open with its identity phrase; got %q", q.Name, q.SystemPrompt)
		}
		if !strings.Contains(q.SystemPrompt, "\"ROUTE-TO: "+q.Name+"\"") {
			t.Errorf("queue %q must be told to act only on its own ROUTE-TO line; got:\n%s", q.Name, q.SystemPrompt)
		}
		if !strings.Contains(q.SystemPrompt, "Never re-decide the routing") {
			t.Errorf("queue %q must be forbidden from re-routing (it would erase the record); got:\n%s", q.Name, q.SystemPrompt)
		}
	}

	// The critic judges method, never truth.
	for _, want := range []string{"METHOD, never the answer", "worker_prompt_write", "rationale", "no ground truth", "frozen instrument", "escalate"} {
		if !strings.Contains(critic.SystemPrompt, want) {
			t.Errorf("critic prompt must contain %q; got:\n%s", want, critic.SystemPrompt)
		}
	}

	// The auditor is a comparator, not an oracle.
	if !strings.HasPrefix(auditor.SystemPrompt, "You are route-auditor,") {
		t.Errorf("auditor prompt must open with its identity phrase; got %q", auditor.SystemPrompt)
	}
	for _, want := range []string{"held-out correct route", "Verdict: match", "Verdict: mismatch", "never decide a route yourself", "refuse to judge", "frozen"} {
		if !strings.Contains(auditor.SystemPrompt, want) {
			t.Errorf("auditor prompt must contain %q; got:\n%s", want, auditor.SystemPrompt)
		}
	}

	// The wiring: ticket channel → dispatcher; dispatcher's finishes → each
	// queue and the critic; audit channel → auditor. Nothing else.
	if len(b.Subscriptions) != 6 {
		t.Fatalf("subscriptions: want 6, got %d", len(b.Subscriptions))
	}
	inbound := b.Subscriptions[0]
	if inbound.EventType != "dispatcher.task" || inbound.Worker != "dispatcher" {
		t.Errorf("ticket channel: want dispatcher.task → dispatcher, got %s → %s", inbound.EventType, inbound.Worker)
	}
	for i, q := range wantNames[1:4] {
		sub := b.Subscriptions[1+i]
		if sub.EventType != agentdb.EventTypeWorkerFinished || sub.Worker != q {
			t.Errorf("fan-out %d: want worker.finished → %s, got %s → %s", i, q, sub.EventType, sub.Worker)
		}
		if got := sub.Filter["worker"]; got != "dispatcher" {
			t.Errorf("fan-out %d filter: want worker=dispatcher, got %v", i, sub.Filter)
		}
	}
	review := b.Subscriptions[4]
	if review.EventType != agentdb.EventTypeWorkerFinished || review.Worker != "methodology-critic" {
		t.Errorf("review edge: want worker.finished → methodology-critic, got %s → %s", review.EventType, review.Worker)
	}
	if got := review.Filter["worker"]; got != "dispatcher" {
		t.Errorf("review edge filter: want worker=dispatcher, got %v", review.Filter)
	}
	audit := b.Subscriptions[5]
	if audit.EventType != "route-auditor.task" || audit.Worker != "route-auditor" {
		t.Errorf("audit channel: want route-auditor.task → route-auditor, got %s → %s", audit.EventType, audit.Worker)
	}

	// Causal isolation, asserted structurally: nothing routes the auditor's
	// events anywhere, the critic observes only the dispatcher, and the
	// auditor's only inbound is the harness-side channel.
	for _, sub := range b.Subscriptions {
		if sub.Filter["worker"] == "route-auditor" {
			t.Errorf("no subscription may deliver the auditor's events (%s → %s does)", sub.EventType, sub.Worker)
		}
		if sub.Worker == "methodology-critic" && sub.Filter["worker"] != "dispatcher" {
			t.Errorf("the critic may observe only the dispatcher, got filter %v", sub.Filter)
		}
		if sub.Worker == "route-auditor" && sub.EventType != "route-auditor.task" {
			t.Errorf("the auditor's only inbound is its harness-side task channel, got %s", sub.EventType)
		}
	}

	if len(b.Schedules) != 0 {
		t.Errorf("schedules: want none, got %d", len(b.Schedules))
	}
	if b.SettingsPatch != nil {
		t.Error("triage-lab must not patch project settings")
	}
}

// The bundle ships NO ground truth, in any form: no memory seeds, no
// preconditions, and no prompt that contains an answer. Held-out truth lives
// harness-side by definition (AGENTS_RESEARCH §4) — a bundle carrying any of it
// would let the loop train on the test.
func TestTriageLabBundleCarriesNoTruth(t *testing.T) {
	b, err := triageLabV1(t).Instantiate(Answers{TriageLabQuestionRoutingRules: triageRules})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if len(b.MemorySeeds) != 0 {
		t.Errorf("memory seeds: want none (memory is loop-readable), got %d", len(b.MemorySeeds))
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Vocabulary a truth channel would use. The rendered bundle legitimately
	// contains the word "route" (it is a routing org), so what is banned is the
	// language of the ANSWER — a correct route, a held-out one, a ticket id.
	for _, leak := range []string{"correct route is", "the answer is", "held-out route", "ground truth is", "misdirect", "decoy"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("rendered bundle contains truth vocabulary %q", leak)
		}
	}
}

// Custom names flow into every referencing row, and the identity phrases follow
// the names (the e2e's mock-script keying depends on both).
func TestTriageLabCustomNames(t *testing.T) {
	b, err := triageLabV1(t).Instantiate(Answers{
		TriageLabQuestionDispatcherName: "tp14-router",
		TriageLabQuestionFirstQueueName: "tp14-money",
		TriageLabQuestionSecondQueue:    "tp14-service",
		TriageLabQuestionThirdQueue:     "tp14-entry",
		TriageLabQuestionCriticName:     "tp14-methodist",
		TriageLabQuestionAuditorName:    "tp14-verifier",
		TriageLabQuestionRoutingRules:   "money goes to tp14-money; service goes to tp14-service; entry goes to tp14-entry",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if b.Workers[5].Name != "tp14-verifier" || !b.Workers[5].Frozen {
		t.Fatalf("auditor: want tp14-verifier frozen, got %q frozen=%v", b.Workers[5].Name, b.Workers[5].Frozen)
	}
	if !strings.HasPrefix(b.Workers[0].SystemPrompt, "You are tp14-router,") {
		t.Errorf("dispatcher identity phrase must carry the custom name; got %q", b.Workers[0].SystemPrompt)
	}
	if !strings.HasPrefix(b.Workers[5].SystemPrompt, "You are tp14-verifier,") {
		t.Errorf("auditor identity phrase must carry the custom name; got %q", b.Workers[5].SystemPrompt)
	}
	// The critic's prompt names the dispatcher (it must) but NEVER the auditor
	// or a queue — the contamination discipline, pinned where it can't rot. A
	// scripted rewrite carries the critic's whole prompt into later bodies, and
	// a travelling name matches the wrong rule (L2, generalised in R2).
	critic := b.Workers[4].SystemPrompt
	if !strings.Contains(critic, "tp14-router") {
		t.Error("critic prompt must name the dispatcher it reviews")
	}
	for _, forbidden := range []string{"tp14-verifier", "tp14-money", "tp14-service", "tp14-entry"} {
		if strings.Contains(critic, forbidden) {
			t.Errorf("critic prompt must not name %q (names travel; travelling names contaminate mock-script keying)", forbidden)
		}
	}
	if got := b.Subscriptions[0].EventType; got != "tp14-router.task" {
		t.Errorf("ticket channel: want tp14-router.task, got %q", got)
	}
	for i := 1; i <= 4; i++ {
		if got := b.Subscriptions[i].Filter["worker"]; got != "tp14-router" {
			t.Errorf("subscription %d filter: want worker=tp14-router, got %v", i, got)
		}
	}
	if got := b.Subscriptions[5].EventType; got != "tp14-verifier.task" {
		t.Errorf("audit channel: want tp14-verifier.task, got %q", got)
	}
}

// The identity phrase must select exactly one worker's own sessions. With six
// workers naming each other in a star, that property is what makes the whole
// org mock-scriptable — and it is one careless prompt edit from being false.
func TestTriageLabIdentityPhrasesAreUnique(t *testing.T) {
	b, err := triageLabV1(t).Instantiate(Answers{TriageLabQuestionRoutingRules: triageRules})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	for _, w := range b.Workers {
		phrase := "You are " + w.Name + ","
		holders := 0
		for _, other := range b.Workers {
			if strings.Contains(other.SystemPrompt, phrase) {
				holders++
			}
		}
		switch w.Name {
		case "methodology-critic":
			// The critic deliberately has none: its rules key on "You review
			// <dispatcher>", because a rewrite's payload carries its target's
			// identity phrase into the critic's own later request bodies.
			if holders != 0 {
				t.Errorf("the critic must not carry an identity phrase (%d prompts hold %q)", holders, phrase)
			}
		default:
			if holders != 1 {
				t.Errorf("%q's identity phrase appears in %d prompts, want exactly 1", w.Name, holders)
			}
		}
	}
	// The critic's own key must likewise be unique.
	key := "You review dispatcher"
	holders := 0
	for _, w := range b.Workers {
		if strings.Contains(w.SystemPrompt, key) {
			holders++
		}
	}
	if holders != 1 {
		t.Errorf("%q appears in %d prompts, want exactly 1", key, holders)
	}
}

// Semantic refusals: the six-way naming discipline and the blank rules answer.
func TestTriageLabRenderRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(Answers)
		wantErr string
	}{
		{
			name:    "missing routing rules",
			mutate:  func(a Answers) { delete(a, TriageLabQuestionRoutingRules) },
			wantErr: `question "routing-rules" is required`,
		},
		{
			name:    "blank routing rules",
			mutate:  func(a Answers) { a[TriageLabQuestionRoutingRules] = "  " },
			wantErr: "routing-rules must not be blank",
		},
		{
			name:    "two queues share a name",
			mutate:  func(a Answers) { a[TriageLabQuestionSecondQueue] = "billing-desk" },
			wantErr: "must be distinct",
		},
		{
			name:    "auditor name collides with the critic",
			mutate:  func(a Answers) { a[TriageLabQuestionAuditorName] = "methodology-critic" },
			wantErr: "must be distinct",
		},
		{
			name: "a queue name is a substring of the dispatcher's",
			mutate: func(a Answers) {
				a[TriageLabQuestionDispatcherName] = "desk-lead"
				a[TriageLabQuestionFirstQueueName] = "desk"
			},
			wantErr: "must not be substrings",
		},
		{
			name:    "non-kebab auditor name",
			mutate:  func(a Answers) { a[TriageLabQuestionAuditorName] = "Route Auditor" },
			wantErr: "not kebab-case",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answers := Answers{TriageLabQuestionRoutingRules: triageRules}
			tc.mutate(answers)
			_, err := triageLabV1(t).Instantiate(answers)
			if err == nil {
				t.Fatalf("want error containing %q, got a bundle", tc.wantErr)
			}
			if !errors.Is(err, ErrBadAnswers) && !errors.Is(err, ErrRender) {
				t.Fatalf("error wraps neither ErrBadAnswers nor ErrRender: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// triage-lab's dispatcher must be able to say `escalate`, and every prompt that
// mentions the option must spell it the same way. A charter offering restraint
// under two spellings offers it under neither.
func TestTriageLabEscalateIsOneWord(t *testing.T) {
	b, err := triageLabV1(t).Instantiate(Answers{TriageLabQuestionRoutingRules: triageRules})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if TriageEscalateRoute != "escalate" {
		t.Fatalf("the reserved route is %q; the harness and the metrics assume %q", TriageEscalateRoute, "escalate")
	}
	prompt := b.Workers[0].SystemPrompt
	if n := strings.Count(prompt, TriageEscalateRoute); n < 3 {
		t.Errorf("the dispatcher's charter mentions %q %d times; restraint needs saying more than once", TriageEscalateRoute, n)
	}
	// It is offered as a ROUTE, not as a way out of routing — the distinction
	// the ambiguity trap measures.
	if !strings.Contains(prompt, "a decision and not a failure to make one") {
		t.Errorf("the charter must say escalating IS a decision; got:\n%s", prompt)
	}
}
