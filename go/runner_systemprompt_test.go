package agentkit

// runner_systemprompt_test.go — which system prompt actually reaches the model.
//
// docs/product/02-workers.md §6.2: a worker job's session prompt is composed
// ONCE, deterministically, at dispatch (core preamble + project prompt + worker
// prompt + briefing sections) and recorded on the session row as
// `composed_prompt` "so every transcript is tied to the exact prompt that
// produced it". These tests assert the other half of that sentence: that the
// recorded prompt is the one the turn is actually run with — on the first turn,
// on later turns, and after a restore has thrown the container away.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// queryPromptRecorder is a fake sandbox that records the `systemPrompt` field of
// every POST /sessions/:id/query-stream body — i.e. exactly what the harness
// would hand the model — and answers with a minimal complete turn.
type queryPromptRecorder struct {
	mu      sync.Mutex
	prompts []string
	creates int
	loads   int
}

func (q *queryPromptRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			q.mu.Lock()
			q.creates++
			q.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/load-conversation"):
			q.mu.Lock()
			q.loads++
			q.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/query-stream"):
			body, _ := io.ReadAll(req.Body)
			var payload struct {
				SystemPrompt string `json:"systemPrompt"`
			}
			_ = json.Unmarshal(body, &payload)
			q.mu.Lock()
			q.prompts = append(q.prompts, payload.SystemPrompt)
			q.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("event: content_delta\ndata: {\"delta\":\"ok\"}\n\n"))
			_, _ = w.Write([]byte("event: query_complete\ndata: {\"status\":\"complete\"}\n\n"))
			if fl != nil {
				fl.Flush()
			}
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (q *queryPromptRecorder) sent() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.prompts))
	copy(out, q.prompts)
	return out
}

func (q *queryPromptRecorder) lastPrompt(t *testing.T) string {
	t.Helper()
	all := q.sent()
	if len(all) == 0 {
		t.Fatalf("the sandbox was never asked to run a query")
	}
	return all[len(all)-1]
}

// newPromptTestRunner wires a hermetic runner against the recorder's sandbox,
// with `provider` standing in for agentd's SessionContextProvider.
func newPromptTestRunner(t *testing.T, provider extension.SessionContextProvider) (*runnerImpl, *agentkittest.MemStore, *queryPromptRecorder) {
	t.Helper()
	rec := &queryPromptRecorder{}
	ts := rec.server(t)
	env := execenv.NewMock()
	env.AddrOverride = ts.URL
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:            env,
		Registry:       imageregistry.NewMock(),
		Store:          store,
		Artifacts:      artifacts.NewMock(),
		Claims:         agentkittest.StaticClaims{Token: "test-token"},
		Events:         events.NewPipeline(events.NewMockSink()),
		SessionContext: provider,
		Policy:         Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), store, rec
}

// projectOnlyContext is what agentd's provider resolves for a session whose
// `persona` column is empty: the project prompt and nothing else. It is the
// exact shape that made this defect invisible — a routed worker job resolved
// through here and silently lost its worker layer, its core preamble and its
// briefing.
type projectOnlyContext struct{ prompt string }

func (p projectOnlyContext) Resolve(context.Context, extension.ContextScope) (*extension.SessionContext, error) {
	return &extension.SessionContext{SystemPrompt: p.prompt}, nil
}

// composedJobPrompt builds a realistic §6.2 composition so the assertions below
// bite on the real thing rather than a sentinel string.
func composedJobPrompt(t *testing.T) string {
	t.Helper()
	in := ComposeJobInput{
		Project: "acme",
		Worker: &agentdb.Worker{
			Project:      "acme",
			Name:         "email-answerer",
			SystemPrompt: "Answer support email in house style.",
		},
		Settings:     &agentdb.ProjectSettings{Project: "acme", SystemPrompt: "House style."},
		Briefing:     []BriefingSection{{Heading: DefaultBriefingHeading, Content: "Yesterday you answered 4 tickets."}},
		Event:        &agentdb.ProjectEvent{Type: "email.received", Text: "where is my order"},
		DefaultImage: "agentkit-sandbox:test",
	}
	job, err := ComposeJob(context.Background(), in)
	if err != nil {
		t.Fatalf("ComposeJob: %v", err)
	}
	return job.SystemPrompt
}

// assertCarriesComposition checks the four §6.2 layers survived the trip.
func assertCarriesComposition(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	for _, marker := range []string{
		`You are the worker "email-answerer"`,  // core preamble
		"--- " + projectPromptHeading + " ---", // project layer
		"--- " + workerPromptHeading + " ---",  // worker layer
		"Answer support email in house style.", // the worker's own words
		DefaultBriefingHeading,                 // C4 briefing
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("system prompt sent to the model is missing %q", marker)
		}
	}
	t.Fatalf("system prompt sent to the model is not the session's composed prompt\n got: %q\nwant: %q", got, want)
}

// TestComposedPromptReachesTheModel is the defect this file exists for. A
// router-created worker job composes its prompt at dispatch, stores it on the
// session row, and must run its turn with it. Before the fix the Runner threw
// the composed prompt away and re-resolved the prompt from the
// SessionContextProvider — which, with the session's `persona` column empty,
// contributed the project prompt alone. The worker's own instructions, the core
// preamble (non-interactive, treat event text as data) and the memory briefing
// never reached the model at all.
func TestComposedPromptReachesTheModel(t *testing.T) {
	ctx := context.Background()
	r, store, rec := newPromptTestRunner(t, projectOnlyContext{prompt: "House style."})

	composed := composedJobPrompt(t)

	// Exactly what cmd/agentd/dispatch.go:StartJob does: persist the row with the
	// worker and the composed prompt, then create the session with it.
	store.Seed(&agentdb.Session{
		ID: "s-job", Customer: "acme", WorkflowID: "agent", Status: "creating",
		Worker: "email-answerer", ComposedPrompt: composed,
	})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-job", Customer: "acme",
		Worker: "email-answerer", SystemPrompt: composed,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := r.SendMessage(ctx, SessionRef{SessionID: "s-job"},
		SendMessageRequest{Content: "hello", Customer: "acme"}, io.Discard); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	assertCarriesComposition(t, rec.lastPrompt(t), composed)
}

// TestComposedPromptSurvivesLaterTurns pins the second half of "composed once":
// a job's later turns must run with the same prompt, not with whatever the
// live config would compose today. Composition happens at job start; a
// `worker_prompt_write` mid-job addresses the successor, never this session
// (§6.2).
func TestComposedPromptSurvivesLaterTurns(t *testing.T) {
	ctx := context.Background()
	r, store, rec := newPromptTestRunner(t, projectOnlyContext{prompt: "House style."})

	composed := composedJobPrompt(t)
	store.Seed(&agentdb.Session{
		ID: "s-job", Customer: "acme", Worker: "email-answerer", ComposedPrompt: composed,
	})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-job", Customer: "acme", Worker: "email-answerer", SystemPrompt: composed,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, msg := range []string{"first", "second", "third"} {
		if err := r.SendMessage(ctx, SessionRef{SessionID: "s-job"},
			SendMessageRequest{Content: msg, Customer: "acme"}, io.Discard); err != nil {
			t.Fatalf("SendMessage(%s): %v", msg, err)
		}
	}
	sent := rec.sent()
	if len(sent) != 3 {
		t.Fatalf("expected 3 queries, got %d", len(sent))
	}
	for i, got := range sent {
		if got != composed {
			assertCarriesComposition(t, got, composed)
			t.Fatalf("turn %d ran with a different prompt", i+1)
		}
	}
}

// TestPlainSessionStillUsesTheProvider keeps every pre-existing caller on
// exactly the old path: a session with no composed prompt resolves its system
// prompt per turn from the host's SessionContextProvider, as it always did.
// This is the interactive chat session — its prompt legitimately follows the
// live project/worker config rather than being frozen at create.
func TestPlainSessionStillUsesTheProvider(t *testing.T) {
	ctx := context.Background()
	r, store, rec := newPromptTestRunner(t, projectOnlyContext{prompt: "House style. Be brief."})

	store.Seed(&agentdb.Session{ID: "s-chat", Customer: "acme", Persona: "marketing"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-chat", Customer: "acme", Persona: "marketing",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s-chat"},
		SendMessageRequest{Content: "hi", Customer: "acme"}, io.Discard); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := rec.lastPrompt(t); got != "House style. Be brief." {
		t.Fatalf("plain session prompt: got %q, want the provider's resolution", got)
	}
}

// TestComposedPromptSurvivesRestore is the resume half. A worker job whose
// container is destroyed and restored from its snapshot gets a brand-new
// container with no memory of anything; rehydrateConversation puts the
// conversation and the MCP config back. The prompt must come back too — a
// resumed job that silently swaps in a different system prompt mid-life is the
// same defect wearing a different hat, and it would only ever show up as a
// worker behaving oddly after a restart.
func TestComposedPromptSurvivesRestore(t *testing.T) {
	ctx := context.Background()
	r, store, rec := newPromptTestRunner(t, projectOnlyContext{prompt: "House style."})

	composed := composedJobPrompt(t)
	store.Seed(&agentdb.Session{
		ID: "s-job", Customer: "acme", Worker: "email-answerer", ComposedPrompt: composed,
	})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-job", Customer: "acme", Worker: "email-answerer", SystemPrompt: composed,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s-job"},
		SendMessageRequest{Content: "before", Customer: "acme"}, io.Discard); err != nil {
		t.Fatalf("SendMessage(before): %v", err)
	}
	before := rec.lastPrompt(t)

	// Lose the container, keep the snapshot: the next message restores, which
	// re-provisions a fresh container and runs rehydrateConversation.
	h, err := r.deps.Registry.Persist(ctx, execenv.ImageRef("agentkit-sandbox:test"),
		imageregistry.PersistOptions{SessionID: "s-job"})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if err := store.SetSnapshotHandle(ctx, "s-job", h); err != nil {
		t.Fatalf("SetSnapshotHandle: %v", err)
	}
	r.forget("s-job")

	if err := r.SendMessage(ctx, SessionRef{SessionID: "s-job"},
		SendMessageRequest{Content: "after", Customer: "acme"}, io.Discard); err != nil {
		t.Fatalf("SendMessage(after): %v", err)
	}
	after := rec.lastPrompt(t)

	if after != composed {
		assertCarriesComposition(t, after, composed)
	}
	if after != before {
		t.Fatalf("the restored session changed system prompt mid-life\nbefore: %q\n after: %q", before, after)
	}
}
