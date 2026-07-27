package topology

import (
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func selfOrganizingV1(t *testing.T) *Topology {
	t.Helper()
	top, ok := Get("self-organizing", "v1")
	if !ok {
		t.Fatal("self-organizing@v1 not registered")
	}
	return top
}

// The reference render: the smallest bundle in the library — one founder, one
// inbound subscription, nothing else. The org chart is not the seed's to draw.
func TestSelfOrganizingRenderDefaults(t *testing.T) {
	b, err := selfOrganizingV1(t).Instantiate(Answers{
		SelfOrganizingQuestionMission: "Run the orchard's paperwork.",
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	if len(b.Workers) != 1 {
		t.Fatalf("workers: want exactly the founder, got %d", len(b.Workers))
	}
	w := b.Workers[0]
	if w.Name != "founder" {
		t.Fatalf("founder name: want the default founder, got %q", w.Name)
	}
	if !w.Enabled || w.Frozen {
		t.Errorf("founder: want enabled and unfrozen, got enabled=%v frozen=%v", w.Enabled, w.Frozen)
	}
	if w.MaxInstances != agentdb.DefaultMaxInstances {
		t.Errorf("founder max_instances: want %d, got %d", agentdb.DefaultMaxInstances, w.MaxInstances)
	}
	if len(w.Briefing) != 0 {
		t.Errorf("founder carries no briefing selector; got %v", w.Briefing)
	}
	if !strings.Contains(w.SystemPrompt, "Run the orchard's paperwork.") {
		t.Error("founder prompt is missing the mission")
	}

	if len(b.Subscriptions) != 1 {
		t.Fatalf("subscriptions: want exactly the inbound edge, got %d", len(b.Subscriptions))
	}
	sub := b.Subscriptions[0]
	if sub.EventType != "org.mission" || sub.Worker != "founder" || len(sub.Filter) != 0 {
		t.Errorf("subscription: want unfiltered org.mission → founder, got %+v", sub)
	}

	if len(b.Schedules) != 0 || len(b.MemorySeeds) != 0 {
		t.Error("self-organizing renders no schedules and no memory seeds")
	}
	if len(b.Preconditions.Images) != 0 || len(b.Preconditions.Skills) != 0 {
		t.Errorf("preconditions: want none, got %+v", b.Preconditions)
	}
}

// Decision D3, pinned: the pool is UNCAPPED. The bundle ships no settings
// patch — no MaxConcurrentJobs override, no token-cap override — so the only
// brakes are whatever the project already has. A future "helpful" cap here
// would quietly reverse a recorded decision; this test is where it trips.
func TestSelfOrganizingIsUncapped(t *testing.T) {
	b, err := selfOrganizingV1(t).Instantiate(Answers{SelfOrganizingQuestionMission: "m"})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if b.SettingsPatch != nil {
		t.Fatalf("D3 says uncapped: the bundle must carry NO settings patch, got %+v", b.SettingsPatch)
	}
}

// The charter's honesty, pinned: the founder's prompt is a description of the
// management surface every session actually holds (cmd/agentd/mcp_management.go),
// so every tool it names must be in the lists the prompt is built from, and it
// must not promise the one management verb that does not exist (worker_delete
// — the F1 discovery: §9 never gave workers one; retirement is
// worker_update {enabled:false}).
func TestSelfOrganizingPromptDescribesTheRealSurface(t *testing.T) {
	b, err := selfOrganizingV1(t).Instantiate(Answers{SelfOrganizingQuestionMission: "m"})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	prompt := b.Workers[0].SystemPrompt

	var all []string
	all = append(all, selfOrganizingWorkerTools...)
	all = append(all, selfOrganizingSubscriptionTools...)
	all = append(all, selfOrganizingScheduleTools...)
	// The surface as registered by mcp_management.go, minus
	// request_human_attention (not a management verb) and the project_prompt
	// pair (the project's charter is not the founder's to rewrite) — if the
	// core tool surface changes, change the lists in selforganizing.go and
	// this pin together.
	want := []string{
		"worker_list", "worker_create", "worker_update", "worker_prompt_read", "worker_prompt_write",
		"subscription_list", "subscription_create", "subscription_delete",
		"schedule_list", "schedule_create", "schedule_update", "schedule_delete",
	}
	if len(all) != len(want) {
		t.Fatalf("tool lists drifted: want %d tools, got %d (%v)", len(want), len(all), all)
	}
	for i, name := range want {
		if all[i] != name {
			t.Fatalf("tool lists drifted at %d: want %q, got %q", i, name, all[i])
		}
	}
	for _, name := range want {
		if !strings.Contains(prompt, name) {
			t.Errorf("founder prompt must name the real tool %q; got:\n%s", name, prompt)
		}
	}

	// worker_delete may appear exactly once — inside the disclaimer that says
	// it does not exist (asserted below). A second occurrence would be the
	// prompt offering the tool somewhere else.
	if n := strings.Count(prompt, "worker_delete"); n != 1 {
		t.Errorf("worker_delete must appear exactly once (the disclaimer), got %d occurrences", n)
	}
	for _, want := range []string{
		"There is no worker_delete tool",
		"hiring is not overwriting",               // worker_create's collision refusal
		"An unwired worker never runs",            // subscriptions are not optional
		"hires do not inherit yours",              // each prompt stands alone
		"uncapped by design",                      // D3, said out loud
		"the only brakes are the project's own",   // where the real limits live
		"rows a human froze refuse worker writes", // the frozen boundary
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("founder prompt must contain %q; got:\n%s", want, prompt)
		}
	}
}

// Renderer refusals: the shared naming/event discipline.
func TestSelfOrganizingRenderRefusals(t *testing.T) {
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
			name: "blank mission",
			answers: Answers{
				SelfOrganizingQuestionMission: "  ",
			},
			wantErr: "mission must not be blank",
		},
		{
			name: "non-kebab founder name",
			answers: Answers{
				SelfOrganizingQuestionFounderName: "The Founder",
				SelfOrganizingQuestionMission:     "m",
			},
			wantErr: "not kebab-case",
		},
		{
			name: "inbound in the worker.* namespace",
			answers: Answers{
				SelfOrganizingQuestionInboundEvent: "worker.finished",
				SelfOrganizingQuestionMission:      "m",
			},
			wantErr: "worker.* namespace",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := selfOrganizingV1(t).Instantiate(tc.answers)
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
