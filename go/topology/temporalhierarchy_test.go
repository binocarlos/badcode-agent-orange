package topology

import (
	"errors"
	"strings"
	"testing"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func temporalV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("temporal-hierarchy", "v1")
	if !ok {
		t.Fatal("temporal-hierarchy@v1 not registered")
	}
	return top
}

// The reference render: one operator on the inbound event, one strategist
// whose only clock is the weekly schedule and whose worker row carries the
// briefing selector for the operators' report label.
func TestTemporalHierarchyRenderDefaults(t *testing.T) {
	b, err := temporalV1(t).Instantiate(Answers{
		TemporalQuestionMission: "Keep the orchard's intake moving.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 2 {
		t.Fatalf("workers: want operator-1 + strategist, got %d", len(b.Workers))
	}
	op, strategist := b.Workers[0], b.Workers[1]
	if op.Name != "operator-1" || strategist.Name != "strategist" {
		t.Fatalf("names: want operator-1/strategist (the defaults), got %q/%q", op.Name, strategist.Name)
	}
	for _, w := range b.Workers {
		if !w.Enabled || w.Frozen {
			t.Errorf("worker %q: want enabled and unfrozen, got enabled=%v frozen=%v", w.Name, w.Enabled, w.Frozen)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
		if !strings.Contains(w.SystemPrompt, "Keep the orchard's intake moving.") {
			t.Errorf("worker %q prompt is missing the mission", w.Name)
		}
	}

	// The channel's two ends: the operator writes kind=operator-reports, the
	// strategist (and only the strategist) is briefed on it.
	if len(op.Briefing) != 0 {
		t.Errorf("the operator carries no briefing — reports flow upward only; got %v", op.Briefing)
	}
	for _, want := range []string{"memory_create", "kind=operator-reports"} {
		if !strings.Contains(op.SystemPrompt, want) {
			t.Errorf("operator prompt must contain %q; got:\n%s", want, op.SystemPrompt)
		}
	}
	if len(strategist.Briefing) != 1 || strategist.Briefing[0] != "kind=operator-reports" {
		t.Errorf("strategist briefing: want [kind=operator-reports], got %v", strategist.Briefing)
	}
	for _, want := range []string{"worker_prompt_read", "worker_prompt_write", "with a rationale", "only the newest"} {
		if !strings.Contains(strategist.SystemPrompt, want) {
			t.Errorf("strategist prompt must contain %q; got:\n%s", want, strategist.SystemPrompt)
		}
	}

	if len(b.Subscriptions) != 1 {
		t.Fatalf("subscriptions: want exactly the operator's inbound edge, got %d", len(b.Subscriptions))
	}
	sub := b.Subscriptions[0]
	if sub.EventType != "work.arrived" || sub.Worker != "operator-1" || len(sub.Filter) != 0 {
		t.Errorf("subscription: want unfiltered work.arrived → operator-1, got %+v", sub)
	}

	if len(b.Schedules) != 1 {
		t.Fatalf("schedules: want exactly the strategist's, got %d", len(b.Schedules))
	}
	sched := b.Schedules[0]
	if sched.Worker != "strategist" || sched.Cron != "0 9 * * 1" {
		t.Errorf("schedule: want strategist weekly (0 9 * * 1), got %+v", sched)
	}
	if sched.Input != temporalScheduleInput {
		t.Errorf("schedule input: want the standing review instruction, got %q", sched.Input)
	}
	if _, err := agentdb.ParseCron(sched.Cron); err != nil {
		t.Errorf("rendered cron must parse: %v", err)
	}

	if b.SettingsPatch != nil || len(b.MemorySeeds) != 0 {
		t.Error("temporal-hierarchy renders no settings patch and no memory seeds")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// The seed's defining property, pinned structurally: the SEPARATION OF
// TIMESCALES. The strategist holds no subscription (its only clock is cron —
// wiring it to operator finishes would put it on the work's timescale); the
// operators hold no schedule; and the naming is one-directional — the
// strategist's charter names every operator (it must know whom to rewrite),
// but no operator names the strategist or a fellow operator.
func TestTemporalHierarchyTimescaleSeparation(t *testing.T) {
	b, err := temporalV1(t).Instantiate(Answers{
		TemporalQuestionOperatorCount: "2",
		TemporalQuestionMission:       "m",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if len(b.Workers) != 3 {
		t.Fatalf("count 2: want 3 workers, got %d", len(b.Workers))
	}
	operators, strategist := b.Workers[:2], b.Workers[2]

	for _, sub := range b.Subscriptions {
		if sub.Worker == strategist.Name {
			t.Errorf("the strategist must hold no subscription — its only clock is the schedule; got %+v", sub)
		}
		if sub.EventType == agentdb.EventTypeWorkerFinished {
			t.Errorf("no worker.finished edge exists in this seed — the review channel is memory; got %+v", sub)
		}
	}
	for _, sched := range b.Schedules {
		if sched.Worker != strategist.Name {
			t.Errorf("only the strategist runs on a schedule; got one for %q", sched.Worker)
		}
	}

	for _, op := range operators {
		if strings.Contains(op.SystemPrompt, strategist.Name) {
			t.Errorf("%s's prompt names the strategist — reports go to a label, not an address", op.Name)
		}
		for _, other := range operators {
			if other.Name != op.Name && strings.Contains(op.SystemPrompt, other.Name) {
				t.Errorf("%s's prompt names fellow operator %s", op.Name, other.Name)
			}
		}
	}
	for _, op := range operators {
		if !strings.Contains(strategist.SystemPrompt, op.Name) {
			t.Errorf("the strategist's prompt must name %s — it cannot rewrite whom it cannot name", op.Name)
		}
	}
}

// The strategist's prompt describes the briefing section by the words
// compose.go really uses — the solo-memory tripwire, inherited.
func TestTemporalHierarchyPromptNamesTheRealBriefingHeading(t *testing.T) {
	b, err := temporalV1(t).Instantiate(Answers{
		TemporalQuestionMemoryLabel: "field-reports",
		TemporalQuestionMission:     "m",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	strategist := b.Workers[len(b.Workers)-1]
	want := agentkit.DefaultBriefingHeading + ": kind=field-reports"
	if !strings.Contains(strategist.SystemPrompt, want) {
		t.Fatalf("strategist prompt must name the real briefing heading %q; got:\n%s", want, strategist.SystemPrompt)
	}
	// And the label reached the other two ends of the channel too.
	if len(strategist.Briefing) != 1 || strategist.Briefing[0] != "kind=field-reports" {
		t.Fatalf("strategist briefing: want [kind=field-reports], got %v", strategist.Briefing)
	}
	if !strings.Contains(b.Workers[0].SystemPrompt, "kind=field-reports") {
		t.Fatal("operator prompt must carry the same label its reports are read under")
	}
}

// The daily cadence maps to the daily cron — and both choices stay slow (no
// hourly: the hierarchy is temporal, and an hourly reviewer is an operator).
func TestTemporalHierarchyCadences(t *testing.T) {
	for cadence, wantCron := range map[string]string{"daily": "0 9 * * *", "weekly": "0 9 * * 1"} {
		b, err := temporalV1(t).Instantiate(Answers{
			TemporalQuestionCadence: cadence,
			TemporalQuestionMission: "m",
		})
		if err != nil {
			t.Fatalf("instantiate (%s): %v", cadence, err)
		}
		if b.Schedules[0].Cron != wantCron {
			t.Errorf("%s: want cron %q, got %q", cadence, wantCron, b.Schedules[0].Cron)
		}
	}
	q := func() Question {
		for _, q := range temporalV1(t).Questions {
			if q.ID == TemporalQuestionCadence {
				return q
			}
		}
		t.Fatal("no cadence question")
		return Question{}
	}()
	if len(q.Choices) != 2 || q.Choices[0] != "daily" || q.Choices[1] != "weekly" {
		t.Fatalf("cadence choices must stay slow (daily/weekly only), got %v", q.Choices)
	}
}

// Renderer refusals: the shared naming/event/label discipline.
func TestTemporalHierarchyRenderRefusals(t *testing.T) {
	tests := []struct {
		name    string
		answers Answers
		wantErr string
	}{
		{
			name:    "missing mission",
			answers: Answers{},
			wantErr: `question "mission" is required`,
		},
		{
			name: "strategist name inside an operator name",
			answers: Answers{
				TemporalQuestionStrategistName: "operator",
				TemporalQuestionMission:        "m",
			},
			wantErr: "must not be substrings",
		},
		{
			name: "inbound in the worker.* namespace",
			answers: Answers{
				TemporalQuestionInboundEvent: "worker.finished",
				TemporalQuestionMission:      "m",
			},
			wantErr: "worker.* namespace",
		},
		{
			name: "label with a space",
			answers: Answers{
				TemporalQuestionMemoryLabel: "the reports",
				TemporalQuestionMission:     "m",
			},
			wantErr: "is invalid",
		},
		{
			name: "blank label",
			answers: Answers{
				TemporalQuestionMemoryLabel: " ",
				TemporalQuestionMission:     "m",
			},
			wantErr: "memory-label must not be blank",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := temporalV1(t).Instantiate(tc.answers)
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
