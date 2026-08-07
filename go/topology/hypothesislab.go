package topology

// hypothesislab.go — topology library entry 13 (docs/product/10-topology-library.md
// §4, Family C: experiment topologies). The calibration org of
// docs/AGENTS_RESEARCH.md §6: an investigator analyses datasets whose true
// answer the HARNESS computed and held out (go/hypolab), a methodology-critic
// improves HOW the investigator works, and a FROZEN fact-checker judges
// conclusions against ground truth it is handed — never truth it generates.
//
// The load-bearing property is what the bundle does NOT contain: ground truth.
// Nothing here ships answers — no memory seeds, no settings, no preconditions
// — because anything the loop can read, it can train on (AGENTS_RESEARCH §4).
// Truth reaches the fact-checker only when the harness sends it inside a
// <checker>.task event, alongside the conclusion under review. The checker is
// a comparator, not an oracle: its prompt orders it to refuse when no stated
// ground truth arrives with the conclusion.
//
// Wiring discipline (same as frozen-scorer): the critic observes ONLY the
// investigator's finishes, holds no subscription to the fact-checker, and
// nothing anywhere routes the fact-checker's events — the instrument is
// causally isolated from the loop it measures, structurally, and pinned by
// test. Improvement here is meta-level by design: what the critic rewrites is
// the investigator's METHODOLOGY (control for covariates, report nulls
// honestly), which is the axis §8.7 actually claims to operate on.

import (
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Hypothesis-lab's question IDs.
const (
	HypothesisLabQuestionInvestigatorName = "investigator-name"
	HypothesisLabQuestionCriticName       = "critic-name"
	HypothesisLabQuestionCheckerName      = "checker-name"
	HypothesisLabQuestionCovariatesHint   = "covariates-hint"
)

func init() {
	Register(&Topology{
		Name:    "hypothesis-lab",
		Version: "v1",
		Description: "The calibration org: an investigator analyses datasets delivered as " +
			"<investigator-name>.task events, a methodology-critic reviews its finishes and retunes " +
			"its METHOD via worker_prompt_write, and a FROZEN fact-checker compares conclusions " +
			"against held-out ground truth the harness sends it in <checker-name>.task events. " +
			"Ground truth lives outside the project; the bundle ships none.",
		Questions: []Question{
			{
				ID:       HypothesisLabQuestionInvestigatorName,
				Prompt:   "Name for the investigator — the worker that analyses each dataset (kebab-case).",
				Type:     QuestionString,
				Default:  "investigator",
				Required: true,
			},
			{
				ID:       HypothesisLabQuestionCriticName,
				Prompt:   "Name for the methodology critic — the worker that reviews and retunes how the investigator works (kebab-case).",
				Type:     QuestionString,
				Default:  "methodology-critic",
				Required: true,
			},
			{
				ID:       HypothesisLabQuestionCheckerName,
				Prompt:   "Name for the fact-checker — the frozen comparator of conclusions against held-out truth (kebab-case).",
				Type:     QuestionString,
				Default:  "fact-checker",
				Required: true,
			},
			{
				ID:       HypothesisLabQuestionCovariatesHint,
				Prompt:   "Which covariates should the investigator consider controlling for? Folded verbatim into its method requirements (e.g. \"age_group — it may drive both sides of a correlation\").",
				Type:     QuestionString,
				Required: true,
			},
		},
		Render: renderHypothesisLab,
	})
}

// labIdentity is the renderer-guaranteed opening of the investigator's and
// fact-checker's prompts. The phrase "You are <name>," appears nowhere else in
// any prompt this seed renders, so a mock-script rule keyed on it selects
// exactly that worker's own sessions — the identity-phrase discipline the
// supervisor seed established for orgs whose workers inevitably name each
// other.
func labIdentity(name string) string { return "You are " + name + "," }

// investigatorPrompt is the method charter, with the covariates hint folded in
// verbatim. The three demands (state method, control covariates, report
// effect-or-null with confidence) are what the critic reviews against.
func investigatorPrompt(name, hint string) string {
	return strings.Join([]string{
		labIdentity(name) + " the investigator. Each task event delivers a hypothesis and a dataset (CSV in the event text). Analyse the data and report a conclusion.",
		"Method requirements:",
		"- State the method you used.",
		"- Control for the stated covariates before concluding. Covariates to consider: " + hint + ".",
		"- Report either the effect (with its size) or a null result, and say how confident you are.",
		"A null result honestly reached is as valuable as a confirmation.",
	}, "\n")
}

// hypothesisCriticPrompt reviews METHOD, not answers — the critic has no
// access to ground truth and must not pretend otherwise. It deliberately never
// contains the fact-checker's name: prompts are event text downstream, and a
// name that travels is a name that matches the wrong mock-script rule.
func hypothesisCriticPrompt(investigator string) string {
	return strings.Join([]string{
		"You review " + investigator + "'s finished work. Each delivery you receive is " + investigator + "'s full transcript.",
		"Judge the METHOD, never the answer: did " + investigator + " state its method, control for the stated covariates, and report an effect or a null with its confidence?",
		"When the method falls short in a way " + investigator + "'s standing orders would keep producing,",
		"use worker_prompt_read and worker_prompt_write to amend " + investigator + "'s system prompt, with a rationale",
		"saying what was methodologically wrong. Amend rather than replace: keep every rule already there.",
		"You hold no ground truth — whether a conclusion is TRUE is checked outside your reach, by a frozen instrument that is not yours to tune.",
	}, "\n")
}

// checkerPrompt is the comparator's charter: judge conclusion against the
// truth IN ITS INPUT, refuse when the truth is missing, generate nothing.
func checkerPrompt(name string) string {
	return strings.Join([]string{
		labIdentity(name) + " the frozen fact-checker. Each task event you receive carries a conclusion under review AND the held-out ground-truth verdict, both in the event text.",
		"Compare them and reply with a line \"Verdict: match\" or \"Verdict: mismatch\", plus one sentence saying why.",
		"You never generate ground truth yourself: if the event text does not state the ground-truth verdict, say so and refuse to judge.",
		"Your own configuration is frozen so the loop you check cannot rewrite its own scoreboard.",
	}, "\n")
}

// renderHypothesisLab is the pure renderer for hypothesis-lab@v1.
func renderHypothesisLab(a Answers) (*Bundle, error) {
	investigator := a[HypothesisLabQuestionInvestigatorName].(string)
	critic := a[HypothesisLabQuestionCriticName].(string)
	checker := a[HypothesisLabQuestionCheckerName].(string)
	if err := checkSeedWorkerNames([]namedWorker{
		{HypothesisLabQuestionInvestigatorName, investigator},
		{HypothesisLabQuestionCriticName, critic},
		{HypothesisLabQuestionCheckerName, checker},
	}); err != nil {
		return nil, err
	}
	hint, err := nonBlankAnswer(a, HypothesisLabQuestionCovariatesHint)
	if err != nil {
		return nil, err
	}

	return &Bundle{
		Workers: []agentdb.Worker{
			{
				Name:         investigator,
				Description:  "The investigator: analyses each dataset delivered by " + TaskEventType(investigator) + " events. Its METHOD is what the critic improves.",
				SystemPrompt: investigatorPrompt(investigator, hint),
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
			},
			{
				Name:         critic,
				Description:  "The methodology critic: reviews " + investigator + "'s finished investigations and retunes its method via worker_prompt_write. It judges method, never truth, and cannot touch the frozen fact-checker.",
				SystemPrompt: hypothesisCriticPrompt(investigator),
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
			},
			{
				Name:         checker,
				Description:  "The FROZEN fact-checker: compares a conclusion against the held-out ground truth the harness sends in " + TaskEventType(checker) + " events. It never generates truth, and no worker may change it — only a human.",
				SystemPrompt: checkerPrompt(checker),
				MaxInstances: agentdb.DefaultMaxInstances,
				Enabled:      true,
				// The instrument ships frozen from the first moment the org
				// exists — the same guarantee frozen-scorer@v1 established.
				Frozen: true,
			},
		},
		Subscriptions: []agentdb.Subscription{
			{
				// The harness delivers each hypothesis + dataset here.
				EventType: TaskEventType(investigator),
				Worker:    investigator,
				Enabled:   true,
			},
			{
				// The critic observes the investigator's finishes — and only
				// those (§8.4: filtered so it never reacts to itself, nor to
				// the fact-checker).
				EventType: agentdb.EventTypeWorkerFinished,
				Filter:    agentdb.JSONMap{"worker": investigator},
				Worker:    critic,
				Enabled:   true,
			},
			{
				// The harness-side check channel: conclusion + held-out truth
				// arrive together as event text. This is the ONLY route truth
				// takes into the project, and it terminates at the frozen
				// comparator — nothing subscribes to the checker's own events.
				EventType: TaskEventType(checker),
				Worker:    checker,
				Enabled:   true,
			},
		},
		Schedules: []agentdb.Schedule{},
	}, nil
}
