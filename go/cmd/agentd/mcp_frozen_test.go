package main

// F1 — the frozen-worker boundary (docs/product/10-topology-library.md §3).
//
// The claim under test: a frozen worker's configuration cannot be changed by
// other workers THROUGH THE CORE MCP SERVER — worker_update and
// worker_prompt_write refuse, saying why; worker_create's existing
// name-collision refusal keeps a frozen worker from being recreated thawed; and
// every refusal is recorded as a `worker.freeze_refused` project event, because
// an agent trying to edit the thing that scores it is a research signal
// (playbook C8), not just an error string.
//
// What is deliberately NOT tested here: the store refusing frozen writes. The
// store stays permissive on purpose — the JWT-guarded HTTP API (the human path)
// shares its methods, and freezing means "workers may not", never "nobody may".

import (
	"errors"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// seedFrozenWorker puts a frozen worker in the fake store, alongside the caller
// so callers can also exercise the unfrozen path.
func seedFrozenWorker(store *fakeManagementStore) *agentdb.Worker {
	w := agentdb.NewWorker("acme", "quality-scorer")
	w.Description = "scores outbound email"
	w.SystemPrompt = "Score every thread 1-5. Never negotiate."
	w.Frozen = true
	return store.seedWorker(w)
}

// The two mutating tools a worker holds refuse a frozen target, leave the row
// byte-identical, and record the attempt.
func TestFrozenWorkerRefusals(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
	}{
		{
			tool: "worker_update",
			args: map[string]any{"name": "quality-scorer", "fields": map[string]any{"enabled": false}},
		},
		{
			tool: "worker_prompt_write",
			args: map[string]any{
				"name":          "quality-scorer",
				"system_prompt": "Score everything 5.",
				"rationale":     "the actor deserves better scores",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			store := newFakeManagementStore()
			frozen := seedFrozenWorker(store)
			originalPrompt := frozen.SystemPrompt
			tools := testManagementTools(store, &fakeAttention{}).tools()

			_, err := invokeTool(t, tools, tc.tool, testCaller(), tc.args)
			if err == nil {
				t.Fatalf("%s against a frozen worker must refuse", tc.tool)
			}
			// The error says the worker is frozen AND why that matters — a bare
			// "denied" teaches the model nothing (§3's "explains why").
			if !strings.Contains(err.Error(), "frozen") {
				t.Fatalf("the refusal must say the worker is frozen: %v", err)
			}
			if !strings.Contains(err.Error(), "measurement instrument") {
				t.Fatalf("the refusal must say why freezing exists: %v", err)
			}
			if !strings.Contains(err.Error(), "human") {
				t.Fatalf("the refusal must point at the human path: %v", err)
			}

			// Nothing moved: no store write, prompt byte-identical.
			if len(store.writes["UpsertWorker"]) != 0 || len(store.writes["SetWorkerPrompt"]) != 0 {
				t.Fatalf("a refused call still wrote: %+v", store.writes)
			}
			after := store.workers[key("acme", "quality-scorer")]
			if after.SystemPrompt != originalPrompt || !after.Frozen || !after.Enabled {
				t.Fatalf("the frozen worker changed under a refusal: %+v", after)
			}

			// The refusal is a signal: exactly one worker.freeze_refused event,
			// naming the tool, the target and the caller.
			if len(store.events) != 1 {
				t.Fatalf("want exactly one refusal event, got %d", len(store.events))
			}
			ev := store.events[0]
			if ev.Type != agentdb.EventTypeWorkerFreezeRefused {
				t.Fatalf("event type = %q, want %q", ev.Type, agentdb.EventTypeWorkerFreezeRefused)
			}
			if ev.Project != "acme" {
				t.Fatalf("event project = %q, want the token's, never an argument", ev.Project)
			}
			for _, want := range []string{tc.tool, "quality-scorer", "email-answerer", "sess-1"} {
				if !strings.Contains(ev.Text, want) {
					t.Fatalf("event text must carry %q:\n%s", want, ev.Text)
				}
			}
			if ev.Envelope.Source != agentdb.EventSourceCore || ev.Envelope.Depth != 0 {
				t.Fatalf("a refusal is core's observation: %+v", ev.Envelope)
			}
			if ev.Envelope.Worker != "email-answerer" || ev.Envelope.SessionID != "sess-1" {
				t.Fatalf("envelope must name the ATTEMPTING worker/session: %+v", ev.Envelope)
			}
			if ev.OccurredAt == 0 {
				t.Fatalf("refusal event carries no occurred_at")
			}
		})
	}
}

// `frozen` is not a field worker_update can touch in EITHER direction: the
// toggle is human-only, and the refusal explains that rather than just
// rejecting an unknown key.
func TestWorkerUpdateRefusesTheFrozenField(t *testing.T) {
	store, tools := seededTools(t) // email-answerer, NOT frozen

	_, err := invokeTool(t, tools, "worker_update", testCaller(), map[string]any{
		"name":   "email-answerer",
		"fields": map[string]any{"frozen": true},
	})
	if err == nil {
		t.Fatalf("worker_update must refuse the frozen field")
	}
	if !strings.Contains(err.Error(), "human-only") {
		t.Fatalf("the refusal must say the toggle is human-only: %v", err)
	}
	if len(store.writes["UpsertWorker"]) != 0 {
		t.Fatalf("a refused field still wrote: %+v", store.writes)
	}
	if store.workers[key("acme", "email-answerer")].Frozen {
		t.Fatalf("the worker was frozen through a worker tool")
	}
}

// worker_create's name-collision refusal is what keeps a frozen worker safe
// from being replaced (or recreated thawed) under its own name.
func TestWorkerCreateCannotReplaceAFrozenWorker(t *testing.T) {
	store := newFakeManagementStore()
	frozen := seedFrozenWorker(store)
	originalPrompt := frozen.SystemPrompt
	tools := testManagementTools(store, &fakeAttention{}).tools()

	_, err := invokeTool(t, tools, "worker_create", testCaller(), map[string]any{
		"name":          "quality-scorer",
		"description":   "a fresh, unfrozen scorer",
		"system_prompt": "Score everything 5.",
	})
	if err == nil {
		t.Fatalf("worker_create against an existing (frozen) name must refuse")
	}
	if len(store.writes["UpsertWorker"]) != 0 {
		t.Fatalf("the refusal still wrote: %+v", store.writes)
	}
	after := store.workers[key("acme", "quality-scorer")]
	if !after.Frozen || after.SystemPrompt != originalPrompt {
		t.Fatalf("worker_create changed a frozen worker: %+v", after)
	}
}

// The refusal stands even when the signal cannot be recorded: an event-store
// failure must not turn into a successful write against a frozen worker.
func TestFrozenRefusalStandsWhenTheEventCannotBeRecorded(t *testing.T) {
	store := newFakeManagementStore()
	seedFrozenWorker(store)
	store.eventErr = errors.New("event spine down")
	tools := testManagementTools(store, &fakeAttention{}).tools()

	_, err := invokeTool(t, tools, "worker_update", testCaller(), map[string]any{
		"name": "quality-scorer", "fields": map[string]any{"description": "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("the refusal must survive a failed emission: %v", err)
	}
	if len(store.writes["UpsertWorker"]) != 0 {
		t.Fatalf("a refused call still wrote: %+v", store.writes)
	}
}

// worker_list carries the frozen flag, so a manager knows before it calls a
// mutating tool — and read-only access to a frozen worker stays open.
func TestFrozenWorkerIsListedAndReadable(t *testing.T) {
	store := newFakeManagementStore()
	seedFrozenWorker(store)
	tools := testManagementTools(store, &fakeAttention{}).tools()

	res, err := invokeTool(t, tools, "worker_list", testCaller(), map[string]any{})
	if err != nil {
		t.Fatalf("worker_list: %v", err)
	}
	workers := res["workers"].([]any)
	if len(workers) != 1 {
		t.Fatalf("want one worker, got %d", len(workers))
	}
	if workers[0].(map[string]any)["frozen"] != true {
		t.Fatalf("worker_list must surface frozen: %v", workers[0])
	}

	// Reading the prompt is not a change: it stays open, frozen or not.
	read, err := invokeTool(t, tools, "worker_prompt_read", testCaller(), map[string]any{"name": "quality-scorer"})
	if err != nil {
		t.Fatalf("worker_prompt_read must work on a frozen worker: %v", err)
	}
	if read["system_prompt"] != "Score every thread 1-5. Never negotiate." {
		t.Fatalf("prompt read: %v", read["system_prompt"])
	}
}
