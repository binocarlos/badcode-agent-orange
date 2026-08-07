package main

// dispatch_prompt_test.go — the router half of "the composed prompt is the
// session's prompt".
//
// go/runner_systemprompt_test.go proves the engine runs a session's turns with
// its `composed_prompt`. These two tests pin the two things agentd must do for
// that to mean anything: write the column, and leave `persona` alone.

import (
	"context"
	"strings"
	"testing"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// startOneJob routes a single event to one worker through the production
// starter and returns the session row it wrote plus the CreateSession request.
func startOneJob(t *testing.T, project, workerName string) (*agentdb.Session, agentkit.CreateSessionRequest) {
	t.Helper()
	store := newFakeRouterStore()
	seedWorker(store, project, workerName, 1)
	store.addSubscription(&agentdb.Subscription{
		Project: project, EventType: "email.received", Worker: workerName, Enabled: true,
	})
	store.settings[project] = &agentdb.ProjectSettings{Project: project, SystemPrompt: "House style."}

	runner := &stubRunner{}
	starter := newRunnerSessionStarter(runner, store).withLeases(store)
	starter.now = store.now
	starter.logf = quietf
	starter.run = func(fn func()) { fn() }
	starter.newID = func() string { return "job-1" }

	rt, _ := newTestRouter(store, starter)
	postEvent(t, store, project, "email.received", "a customer wrote in", agentdb.EventEnvelope{Depth: 0})
	if err := rt.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	sess := store.session("job-1")
	if sess == nil {
		t.Fatalf("the router started no job")
	}
	if len(runner.created) != 1 {
		t.Fatalf("expected exactly one CreateSession, got %d", len(runner.created))
	}
	return sess, runner.created[0]
}

// TestDispatchPersistsTheExactPromptItLaunchesWith: the string on the row and
// the string handed to the Runner must be the SAME string. They are the two
// ends of the same wire now — the row is what every turn runs with, the request
// is what the create was made with — and a divergence between them would mean a
// transcript attributed to a prompt that never ran.
func TestDispatchPersistsTheExactPromptItLaunchesWith(t *testing.T) {
	sess, created := startOneJob(t, "acme", "answerer")

	if sess.ComposedPrompt == "" {
		t.Fatalf("the session row carries no composed_prompt — the job would run on the "+
			"SessionContextProvider's resolution instead: %+v", sess)
	}
	if sess.ComposedPrompt != created.SystemPrompt {
		t.Fatalf("row and request disagree about the session's prompt\n row: %q\nreq: %q",
			sess.ComposedPrompt, created.SystemPrompt)
	}
	// Sanity: it really is the §6.2 composition, not just the worker's prompt.
	for _, marker := range []string{
		`You are the worker "answerer"`, // core preamble
		"House style.",                  // project layer
		"you are answerer",              // worker layer
	} {
		if !strings.Contains(sess.ComposedPrompt, marker) {
			t.Errorf("composed_prompt is missing %q:\n%s", marker, sess.ComposedPrompt)
		}
	}
	if sess.Worker != "answerer" {
		t.Fatalf("session.worker = %q, want answerer", sess.Worker)
	}
}

// TestDispatchLeavesPersonaEmpty pins a deliberate omission, because "the row is
// missing its persona" reads like a bug until you know why.
//
// `persona` is the key sessioncontext.go re-resolves a worker's prompt, image
// and MCP layer from, live, at every turn. A composed job has already had all
// three composed and pinned at dispatch. Setting `persona` too would give the
// session two worker identities that can drift apart — `worker` (what the job
// is, and what the core MCP server attributes its writes to) and `persona` (a
// re-resolution of configuration that may have been rewritten since). One
// mechanism: `worker` names the worker, `composed_prompt` carries its prompt.
func TestDispatchLeavesPersonaEmpty(t *testing.T) {
	sess, created := startOneJob(t, "acme", "answerer")

	if sess.Persona != "" {
		t.Fatalf("a routed job set session.persona = %q. That makes the SessionContextProvider "+
			"re-resolve this worker's config every turn, alongside the composed prompt that "+
			"already contains it — two mechanisms that can disagree. The worker's identity "+
			"lives in session.worker (which is %q).", sess.Persona, sess.Worker)
	}
	if created.Persona != "" {
		t.Fatalf("CreateSessionRequest.Persona = %q, want empty for a composed job", created.Persona)
	}
}
