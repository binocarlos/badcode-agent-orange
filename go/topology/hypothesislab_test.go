package topology

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func hypothesisLabV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("hypothesis-lab", "v1")
	if !ok {
		t.Fatal("hypothesis-lab@v1 not registered")
	}
	return top
}

// The reference render: investigator + methodology-critic + frozen
// fact-checker, with the covariates hint folded into the investigator's
// method requirements and truth arriving only over the checker's own task
// channel.
func TestHypothesisLabRenderDefaults(t *testing.T) {
	b, err := hypothesisLabV1(t).Instantiate(Answers{
		HypothesisLabQuestionCovariatesHint: "age_group — it may drive both sides of a correlation",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 3 {
		t.Fatalf("workers: want 3, got %d", len(b.Workers))
	}
	investigator, critic, checker := b.Workers[0], b.Workers[1], b.Workers[2]
	if investigator.Name != "investigator" || critic.Name != "methodology-critic" || checker.Name != "fact-checker" {
		t.Fatalf("worker names: want investigator/methodology-critic/fact-checker (the defaults), got %q/%q/%q",
			investigator.Name, critic.Name, checker.Name)
	}

	// The instrument — and only the instrument — ships frozen.
	if !checker.Frozen {
		t.Error("the fact-checker must render Frozen: true")
	}
	if investigator.Frozen || critic.Frozen {
		t.Error("only the fact-checker is the instrument; investigator and critic must not render frozen")
	}
	for _, w := range b.Workers {
		if !w.Enabled {
			t.Errorf("worker %q must render enabled", w.Name)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
	}

	// The investigator's method charter: identity phrase, the hint verbatim,
	// and the three demands the critic reviews against.
	if !strings.HasPrefix(investigator.SystemPrompt, "You are investigator,") {
		t.Errorf("investigator prompt must open with its identity phrase; got %q", investigator.SystemPrompt)
	}
	for _, want := range []string{
		"age_group — it may drive both sides of a correlation",
		"State the method",
		"Control for the stated covariates",
		"null result",
		"confiden",
	} {
		if !strings.Contains(investigator.SystemPrompt, want) {
			t.Errorf("investigator prompt must contain %q; got:\n%s", want, investigator.SystemPrompt)
		}
	}

	// The critic judges method, never truth — and never names the checker
	// (prompts become event text downstream; a travelling name is a
	// mock-script contamination channel).
	for _, want := range []string{"METHOD, never the answer", "worker_prompt_write", "rationale", "no ground truth", "frozen instrument"} {
		if !strings.Contains(critic.SystemPrompt, want) {
			t.Errorf("critic prompt must contain %q; got:\n%s", want, critic.SystemPrompt)
		}
	}

	// The checker is a comparator, not an oracle: truth arrives in its input,
	// a missing truth is a refusal, and it says its own config is frozen.
	if !strings.HasPrefix(checker.SystemPrompt, "You are fact-checker,") {
		t.Errorf("checker prompt must open with its identity phrase; got %q", checker.SystemPrompt)
	}
	for _, want := range []string{"ground-truth verdict", "Verdict: match", "Verdict: mismatch", "never generate ground truth", "refuse to judge", "frozen"} {
		if !strings.Contains(checker.SystemPrompt, want) {
			t.Errorf("checker prompt must contain %q; got:\n%s", want, checker.SystemPrompt)
		}
	}

	// The wiring: dataset channel → investigator; investigator's finishes →
	// critic; check channel → checker. Nothing else.
	if len(b.Subscriptions) != 3 {
		t.Fatalf("subscriptions: want 3, got %d", len(b.Subscriptions))
	}
	inbound := b.Subscriptions[0]
	if inbound.EventType != "investigator.task" || inbound.Worker != "investigator" {
		t.Errorf("dataset channel: want investigator.task → investigator, got %s → %s", inbound.EventType, inbound.Worker)
	}
	review := b.Subscriptions[1]
	if review.EventType != agentdb.EventTypeWorkerFinished || review.Worker != "methodology-critic" {
		t.Errorf("review edge: want worker.finished → methodology-critic, got %s → %s", review.EventType, review.Worker)
	}
	if got := review.Filter["worker"]; got != "investigator" {
		t.Errorf("review edge filter: want worker=investigator, got %v", review.Filter)
	}
	check := b.Subscriptions[2]
	if check.EventType != "fact-checker.task" || check.Worker != "fact-checker" {
		t.Errorf("check channel: want fact-checker.task → fact-checker, got %s → %s", check.EventType, check.Worker)
	}

	// Causal isolation, asserted structurally: the critic observes only the
	// investigator; nothing routes the checker's events anywhere; and the
	// checker's only inbound is the harness-side task channel.
	for _, sub := range b.Subscriptions {
		if sub.Filter["worker"] == "fact-checker" {
			t.Errorf("no subscription may deliver the fact-checker's events (%s → %s does)", sub.EventType, sub.Worker)
		}
		if sub.Worker == "methodology-critic" && sub.Filter["worker"] != "investigator" {
			t.Errorf("the critic may observe only the investigator, got filter %v", sub.Filter)
		}
		if sub.Worker == "fact-checker" && sub.EventType != "fact-checker.task" {
			t.Errorf("the checker's only inbound is its harness-side task channel, got %s", sub.EventType)
		}
	}

	if len(b.Schedules) != 0 {
		t.Errorf("schedules: want none, got %d", len(b.Schedules))
	}
	if b.SettingsPatch != nil {
		t.Error("hypothesis-lab must not patch project settings")
	}
}

// The bundle ships NO ground truth, in any form: no memory seeds, no
// preconditions, and no prompt that contains an answer. Held-out truth lives
// harness-side by definition (AGENTS_RESEARCH §4) — a bundle carrying any of
// it would let the loop train on the test.
func TestHypothesisLabBundleCarriesNoTruth(t *testing.T) {
	b, err := hypothesisLabV1(t).Instantiate(Answers{
		HypothesisLabQuestionCovariatesHint: "age_group",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if len(b.MemorySeeds) != 0 {
		t.Errorf("memory seeds: want none (memory is loop-readable), got %d", len(b.MemorySeeds))
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
	// Belt and braces over the whole rendered bundle: no verdict vocabulary.
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{"effect_size", `"effect"`, "ground truth is", "the answer is"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("rendered bundle contains verdict vocabulary %q", leak)
		}
	}
}

// Custom names flow into every referencing row, and the identity phrases
// follow the names (the e2e's mock-script keying depends on both).
func TestHypothesisLabCustomNames(t *testing.T) {
	b, err := hypothesisLabV1(t).Instantiate(Answers{
		HypothesisLabQuestionInvestigatorName: "tp13-inspector",
		HypothesisLabQuestionCriticName:       "tp13-methodist",
		HypothesisLabQuestionCheckerName:      "tp13-verifier",
		HypothesisLabQuestionCovariatesHint:   "age_group",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if b.Workers[2].Name != "tp13-verifier" || !b.Workers[2].Frozen {
		t.Fatalf("checker: want tp13-verifier frozen, got %q frozen=%v", b.Workers[2].Name, b.Workers[2].Frozen)
	}
	if !strings.HasPrefix(b.Workers[0].SystemPrompt, "You are tp13-inspector,") {
		t.Errorf("investigator identity phrase must carry the custom name; got %q", b.Workers[0].SystemPrompt)
	}
	if !strings.HasPrefix(b.Workers[2].SystemPrompt, "You are tp13-verifier,") {
		t.Errorf("checker identity phrase must carry the custom name; got %q", b.Workers[2].SystemPrompt)
	}
	// The critic's prompt names the investigator (it must) but NEVER the
	// checker — the contamination discipline, pinned where it can't rot.
	if !strings.Contains(b.Workers[1].SystemPrompt, "tp13-inspector") {
		t.Error("critic prompt must name the investigator it reviews")
	}
	if strings.Contains(b.Workers[1].SystemPrompt, "tp13-verifier") {
		t.Error("critic prompt must not name the fact-checker (names travel; travelling names contaminate mock-script keying)")
	}
	if got := b.Subscriptions[0].EventType; got != "tp13-inspector.task" {
		t.Errorf("dataset channel: want tp13-inspector.task, got %q", got)
	}
	if got := b.Subscriptions[1].Filter["worker"]; got != "tp13-inspector" {
		t.Errorf("review edge filter: want worker=tp13-inspector, got %v", got)
	}
	if got := b.Subscriptions[2].EventType; got != "tp13-verifier.task" {
		t.Errorf("check channel: want tp13-verifier.task, got %q", got)
	}
}

// Semantic refusals: the three-way naming discipline and the blank hint.
func TestHypothesisLabRenderRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(Answers)
		wantErr string
	}{
		{
			name:    "missing covariates hint",
			mutate:  func(a Answers) { delete(a, HypothesisLabQuestionCovariatesHint) },
			wantErr: `question "covariates-hint" is required`,
		},
		{
			name:    "blank covariates hint",
			mutate:  func(a Answers) { a[HypothesisLabQuestionCovariatesHint] = "  " },
			wantErr: "covariates-hint must not be blank",
		},
		{
			name:    "checker name collides with critic",
			mutate:  func(a Answers) { a[HypothesisLabQuestionCheckerName] = "methodology-critic" },
			wantErr: "must be distinct",
		},
		{
			name: "critic name is a substring of the investigator's",
			mutate: func(a Answers) {
				a[HypothesisLabQuestionInvestigatorName] = "lab-lead"
				a[HypothesisLabQuestionCriticName] = "lab"
			},
			wantErr: "must not be substrings",
		},
		{
			name:    "non-kebab checker name",
			mutate:  func(a Answers) { a[HypothesisLabQuestionCheckerName] = "Fact Checker" },
			wantErr: "not kebab-case",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answers := Answers{
				HypothesisLabQuestionCovariatesHint: "age_group",
			}
			tc.mutate(answers)
			_, err := hypothesisLabV1(t).Instantiate(answers)
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
