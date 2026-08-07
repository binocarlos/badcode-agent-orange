package topology

import (
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func debateV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("debate", "v1")
	if !ok {
		t.Fatal("debate@v1 not registered")
	}
	return top
}

// The reference render: N debaters unfiltered on the shared inbound type, one
// aggregator with one equality-filtered worker.finished edge per debater.
func TestDebateRenderDefaults(t *testing.T) {
	b, err := debateV1(t).Instantiate(Answers{
		DebateQuestionMission: "Settle questions about orchard storage.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 3 {
		t.Fatalf("workers: want 3 (2 debaters + aggregator), got %d", len(b.Workers))
	}
	wantNames := []string{"debater-1", "debater-2", "aggregator"}
	for i, w := range b.Workers {
		if w.Name != wantNames[i] {
			t.Errorf("worker %d: want %q, got %q", i, wantNames[i], w.Name)
		}
		if !w.Enabled || w.Frozen {
			t.Errorf("worker %q: want enabled and unfrozen, got enabled=%v frozen=%v", w.Name, w.Enabled, w.Frozen)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
		if len(w.Briefing) != 0 {
			t.Errorf("worker %q: debate has no memory channel, got briefing %v", w.Name, w.Briefing)
		}
		if !strings.Contains(w.SystemPrompt, "Settle questions about orchard storage.") {
			t.Errorf("worker %q prompt is missing the mission", w.Name)
		}
	}

	if len(b.Subscriptions) != 4 {
		t.Fatalf("subscriptions: want 4 (2 inbound + 2 review edges), got %d", len(b.Subscriptions))
	}
	// The debaters: same inbound type, unfiltered — one question wakes all.
	for i, sub := range b.Subscriptions[:2] {
		if sub.EventType != "debate.question" || sub.Worker != wantNames[i] || len(sub.Filter) != 0 {
			t.Errorf("debater subscription %d: want unfiltered debate.question for %s, got %+v", i, wantNames[i], sub)
		}
	}
	// The aggregator: worker.finished per debater, equality-filtered.
	for i, sub := range b.Subscriptions[2:] {
		if sub.EventType != agentdb.EventTypeWorkerFinished || sub.Worker != "aggregator" {
			t.Errorf("review edge %d: want worker.finished → aggregator, got %+v", i, sub)
		}
		if got := sub.Filter["worker"]; got != wantNames[i] {
			t.Errorf("review edge %d filter: want worker=%s, got %v", i, wantNames[i], sub.Filter)
		}
		if len(sub.Filter) != 1 {
			t.Errorf("review edge %d: want exactly the worker equality filter, got %v", i, sub.Filter)
		}
	}

	if len(b.Schedules) != 0 || b.SettingsPatch != nil || len(b.MemorySeeds) != 0 {
		t.Error("debate renders no schedules, no settings patch and no memory seeds")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// The seed's defining property, pinned structurally: INDEPENDENCE (entry 7's
// caveat — debate collapses into groupthink when debaters see each other). No
// debater's prompt names another debater; no channel delivers any debater's
// output to another debater; the aggregator hears each debater exactly once
// and nothing routes the aggregator's own finishes anywhere.
func TestDebateIndependence(t *testing.T) {
	b, err := debateV1(t).Instantiate(Answers{
		DebateQuestionDebaterCount: "3",
		DebateQuestionMission:      "m",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if len(b.Workers) != 4 || len(b.Subscriptions) != 6 {
		t.Fatalf("count 3: want 4 workers / 6 subscriptions, got %d / %d", len(b.Workers), len(b.Subscriptions))
	}

	debaters := b.Workers[:3]
	aggregator := b.Workers[3]
	for _, w := range debaters {
		for _, other := range debaters {
			if other.Name != w.Name && strings.Contains(w.SystemPrompt, other.Name) {
				t.Errorf("%s's prompt names fellow debater %s — independence must be structural", w.Name, other.Name)
			}
		}
	}
	// The debaters are interchangeable: prompts identical up to identity.
	norm := func(w agentdb.Worker, ordinal string) string {
		s := strings.Replace(w.SystemPrompt, stageIdentity(w.Name)+" debater", "You are X, debater", 1)
		return strings.Replace(s, ordinal+" of 3", "N of 3", 1)
	}
	first := norm(debaters[0], "1")
	for i, w := range debaters[1:] {
		if got := norm(w, []string{"2", "3"}[i]); got != first {
			t.Errorf("debater prompts must be identical up to identity; %s differs:\n%s\nvs\n%s", w.Name, got, first)
		}
	}

	// Delivery-side independence: no subscription delivers worker.finished to
	// a debater, and the aggregator's edges cover each debater exactly once.
	seen := map[string]int{}
	for _, sub := range b.Subscriptions {
		if sub.EventType == agentdb.EventTypeWorkerFinished {
			if sub.Worker != aggregator.Name {
				t.Errorf("a worker.finished edge reaches %s — only the aggregator judges; got %+v", sub.Worker, sub)
			}
			source, _ := sub.Filter["worker"].(string)
			seen[source]++
			if source == aggregator.Name {
				t.Errorf("the aggregator must never hear its own finishes; got %+v", sub)
			}
		}
	}
	for _, d := range debaters {
		if seen[d.Name] != 1 {
			t.Errorf("aggregator must hear %s exactly once, got %d edges", d.Name, seen[d.Name])
		}
	}
}

// The N-firings honesty, pinned: the aggregator's charter must describe the
// shape it actually runs under — woken once per debater, one transcript per
// delivery — because no mechanism hands it the whole panel at once.
func TestDebateAggregatorHonesty(t *testing.T) {
	b, err := debateV1(t).Instantiate(Answers{
		DebateQuestionDebaterCount: "3",
		DebateQuestionMission:      "m",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	prompt := b.Workers[3].SystemPrompt
	for _, want := range []string{
		"wakes you 3 times — once per debater",
		"a SINGLE debater's full finished transcript",
		"There is no channel that hands you all the arguments together",
		"note dissent explicitly",
		"debater-1, debater-2, debater-3", // the roster: the judge knows the panel
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("aggregator prompt must contain %q; got:\n%s", want, prompt)
		}
	}
	// And each debater is told the transcript is the only channel to the judge.
	for _, w := range b.Workers[:3] {
		for _, want := range []string{"you cannot post events yourself", "aggregator"} {
			if !strings.Contains(w.SystemPrompt, want) {
				t.Errorf("%s prompt must contain %q; got:\n%s", w.Name, want, w.SystemPrompt)
			}
		}
	}
}

// Renderer refusals: the shared naming/event discipline.
func TestDebateRenderRefusals(t *testing.T) {
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
			name: "non-kebab prefix",
			answers: Answers{
				DebateQuestionDebaterPrefix: "Loud Voice",
				DebateQuestionMission:       "m",
			},
			wantErr: "not kebab-case",
		},
		{
			name: "aggregator name inside a debater name",
			answers: Answers{
				DebateQuestionAggregatorName: "debater",
				DebateQuestionMission:        "m",
			},
			wantErr: "must not be substrings",
		},
		{
			name: "inbound in the worker.* namespace",
			answers: Answers{
				DebateQuestionInboundEvent: "worker.finished",
				DebateQuestionMission:      "m",
			},
			wantErr: "worker.* namespace",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := debateV1(t).Instantiate(tc.answers)
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
