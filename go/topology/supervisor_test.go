package topology

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func supervisorV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("supervisor", "v1")
	if !ok {
		t.Fatal("supervisor@v1 not registered")
	}
	return top
}

// The reference render: defaults everywhere except the mission. Dispatcher +
// two specialists, one inbound route, one broadcast edge per specialist.
func TestSupervisorRenderDefaults(t *testing.T) {
	b, err := supervisorV1(t).Instantiate(Answers{
		SupervisorQuestionMission: "You triage questions about the fruit catalogue.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 3 {
		t.Fatalf("workers: want 3 (dispatcher + 2 specialists, the default count), got %d", len(b.Workers))
	}
	dispatcher := b.Workers[0]
	if dispatcher.Name != "dispatcher" {
		t.Fatalf("dispatcher name: want the default, got %q", dispatcher.Name)
	}
	if b.Workers[1].Name != "specialist-1" || b.Workers[2].Name != "specialist-2" {
		t.Fatalf("specialist names: want specialist-1/specialist-2, got %q/%q", b.Workers[1].Name, b.Workers[2].Name)
	}
	for _, w := range b.Workers {
		if !w.Enabled {
			t.Errorf("worker %q must render enabled", w.Name)
		}
		if w.Frozen {
			t.Errorf("worker %q must not render frozen", w.Name)
		}
		if w.MaxInstances != agentdb.DefaultMaxInstances {
			t.Errorf("worker %q max_instances: want %d, got %d", w.Name, agentdb.DefaultMaxInstances, w.MaxInstances)
		}
		if !strings.Contains(w.SystemPrompt, "You triage questions about the fruit catalogue.") {
			t.Errorf("worker %q prompt must fold in the mission; got:\n%s", w.Name, w.SystemPrompt)
		}
	}

	// The dispatcher's prompt documents the convention HONESTLY: it lists the
	// workforce, states that worker.finished broadcast is the only emission
	// that exists, and gives the ROUTE-TO form.
	for _, want := range []string{
		"specialist-1, specialist-2",
		"you cannot post events yourself",
		"worker.finished",
		"ROUTE-TO: <specialist-name>",
	} {
		if !strings.Contains(dispatcher.SystemPrompt, want) {
			t.Errorf("dispatcher prompt must contain %q; got:\n%s", want, dispatcher.SystemPrompt)
		}
	}
	// …and must NOT contain any specialist's identity phrase — that phrase is
	// each specialist's unique body-substring key (see specialistIdentity).
	for _, name := range []string{"specialist-1", "specialist-2"} {
		if strings.Contains(dispatcher.SystemPrompt, specialistIdentity(name)) {
			t.Errorf("dispatcher prompt must not contain %q — it is %s's unique key", specialistIdentity(name), name)
		}
	}
	// Each specialist's prompt opens with its identity phrase and names its own
	// route line, and only its own.
	for i, w := range b.Workers[1:] {
		if !strings.Contains(w.SystemPrompt, specialistIdentity(w.Name)) {
			t.Errorf("specialist %q prompt must contain its identity phrase %q", w.Name, specialistIdentity(w.Name))
		}
		if !strings.Contains(w.SystemPrompt, routeLine(w.Name)) {
			t.Errorf("specialist %q prompt must name its route line %q", w.Name, routeLine(w.Name))
		}
		other := b.Workers[1+(1-i)].Name
		if strings.Contains(w.SystemPrompt, other) {
			t.Errorf("specialist %q prompt must not name its sibling %q (names partition mock-script rules)", w.Name, other)
		}
	}

	if len(b.Subscriptions) != 3 {
		t.Fatalf("subscriptions: want 3 (inbound + one broadcast edge per specialist), got %d", len(b.Subscriptions))
	}
	inbound := b.Subscriptions[0]
	if inbound.EventType != "task.requested" || inbound.Worker != "dispatcher" {
		t.Errorf("inbound: want task.requested → dispatcher, got %s → %s", inbound.EventType, inbound.Worker)
	}
	for i, sub := range b.Subscriptions[1:] {
		wantWorker := fmt.Sprintf("specialist-%d", i+1)
		if sub.EventType != agentdb.EventTypeWorkerFinished || sub.Worker != wantWorker {
			t.Errorf("edge %d: want worker.finished → %s, got %s → %s", i, wantWorker, sub.EventType, sub.Worker)
		}
		if got := sub.Filter["worker"]; got != "dispatcher" {
			t.Errorf("edge %d filter: want worker=dispatcher (specialists hear the dispatcher, not each other), got %v", i, sub.Filter)
		}
	}
	for _, s := range b.Subscriptions {
		if !s.Enabled {
			t.Errorf("subscription %s → %s must render enabled", s.EventType, s.Worker)
		}
	}

	if len(b.Schedules) != 0 {
		t.Errorf("schedules: want none, got %d", len(b.Schedules))
	}
	if b.SettingsPatch != nil {
		t.Error("supervisor must not patch project settings")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// Three specialists: one more worker, one more edge, and the dispatcher's
// roster names all three.
func TestSupervisorThreeSpecialists(t *testing.T) {
	b, err := supervisorV1(t).Instantiate(Answers{
		SupervisorQuestionMission:          "m",
		SupervisorQuestionSpecialistCount:  "3",
		SupervisorQuestionDispatcherName:   "tp6-desk",
		SupervisorQuestionSpecialistPrefix: "tp6-hand",
		SupervisorQuestionInboundEvent:     "tp6.question",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if len(b.Workers) != 4 {
		t.Fatalf("workers: want 4, got %d", len(b.Workers))
	}
	if len(b.Subscriptions) != 4 {
		t.Fatalf("subscriptions: want 4, got %d", len(b.Subscriptions))
	}
	if !strings.Contains(b.Workers[0].SystemPrompt, "tp6-hand-1, tp6-hand-2, tp6-hand-3") {
		t.Errorf("dispatcher roster must list all three specialists; got:\n%s", b.Workers[0].SystemPrompt)
	}
	if b.Subscriptions[0].EventType != "tp6.question" {
		t.Errorf("inbound event: want tp6.question, got %q", b.Subscriptions[0].EventType)
	}
	for _, sub := range b.Subscriptions[1:] {
		if got := sub.Filter["worker"]; got != "tp6-desk" {
			t.Errorf("edge filter: want worker=tp6-desk, got %v", got)
		}
	}
}

// Semantic refusals: names, mission, and the inbound-event rules (no
// wildcards, no worker.* — a dispatcher woken by its own specialists finishing
// is a loop, not an org).
func TestSupervisorRenderRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(Answers)
		wantErr string
	}{
		{
			name:    "blank mission",
			mutate:  func(a Answers) { a[SupervisorQuestionMission] = "   " },
			wantErr: "mission must not be blank",
		},
		{
			name:    "non-kebab dispatcher name",
			mutate:  func(a Answers) { a[SupervisorQuestionDispatcherName] = "The Desk" },
			wantErr: "not kebab-case",
		},
		{
			name: "specialist prefix contains the dispatcher name",
			mutate: func(a Answers) {
				a[SupervisorQuestionDispatcherName] = "desk"
				a[SupervisorQuestionSpecialistPrefix] = "desk"
			},
			wantErr: "must not be substrings",
		},
		{
			name:    "wildcard inbound event",
			mutate:  func(a Answers) { a[SupervisorQuestionInboundEvent] = "task.*" },
			wantErr: "not a wildcard",
		},
		{
			name:    "worker.* inbound event",
			mutate:  func(a Answers) { a[SupervisorQuestionInboundEvent] = "worker.finished" },
			wantErr: "worker.* namespace",
		},
		{
			name:    "whitespace-wrapped inbound event",
			mutate:  func(a Answers) { a[SupervisorQuestionInboundEvent] = " task.requested" },
			wantErr: "surrounding whitespace",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answers := Answers{SupervisorQuestionMission: "m"}
			tc.mutate(answers)
			_, err := supervisorV1(t).Instantiate(answers)
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
