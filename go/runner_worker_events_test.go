package agentkit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// *agentdb.Store is the production WorkerEventStore — pinned here so the seam
// and the store cannot drift apart silently.
var _ WorkerEventStore = (*agentdb.Store)(nil)

// fakeWorkerEvents is an in-memory WorkerEventStore. Session lookup is delegated
// to the MemStore the Runner already uses, so a test seeds one place only.
type fakeWorkerEvents struct {
	*agentkittest.MemStore

	mu       sync.Mutex
	triggers map[string]*agentdb.ProjectEvent // sessionID -> triggering event
	appended []*agentdb.ProjectEvent
}

func newFakeWorkerEvents(store *agentkittest.MemStore) *fakeWorkerEvents {
	return &fakeWorkerEvents{MemStore: store, triggers: map[string]*agentdb.ProjectEvent{}}
}

func (f *fakeWorkerEvents) SessionTriggerEvent(_ context.Context, sessionID string) (*agentdb.ProjectEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.triggers[sessionID], nil
}

func (f *fakeWorkerEvents) CreateProjectEvent(_ context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *ev
	f.appended = append(f.appended, &cp)
	return &cp, nil
}

func (f *fakeWorkerEvents) events() []*agentdb.ProjectEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*agentdb.ProjectEvent, len(f.appended))
	copy(out, f.appended)
	return out
}

// newWorkerEventRunner wires a Runner with the DEFAULT event pipeline (so the
// error MarkerHook is registered) against a fake sandbox serving `frames` as the
// turn's SSE stream.
func newWorkerEventRunner(t *testing.T, sessionID string, frames []string) (*runnerImpl, *agentkittest.MemStore, *fakeWorkerEvents) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true}`))
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/"+sessionID+"/query-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			for _, f := range frames {
				_, _ = w.Write([]byte(f))
				if fl != nil {
					fl.Flush()
				}
			}
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(ts.Close)

	env := execenv.NewMock()
	env.AddrOverride = ts.URL
	store := agentkittest.NewMemStore()
	worker := newFakeWorkerEvents(store)
	runner, err := NewRunner(Deps{
		Env:          env,
		Registry:     imageregistry.NewMock(),
		Store:        store,
		Artifacts:    artifacts.NewMock(),
		Claims:       agentkittest.StaticClaims{Token: "test-token"},
		WorkerEvents: worker,
		Policy:       Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), store, worker
}

// completeTurn is a normal SSE turn: two deltas merged into one assistant turn.
var completeTurn = []string{
	"event: content_delta\ndata: {\"delta\":\"Hello \"}\n\n",
	"event: content_delta\ndata: {\"delta\":\"world\"}\n\n",
	"event: query_complete\ndata: {\"status\":\"complete\"}\n\n",
}

// TestWorkerFinishedEvent is work-plan item E2 (the `worker.finished` half): a
// worker job's completed query appends the §8.2 event, carrying the whole
// exchange as its text and the §8.1 envelope. A session with an empty `worker`
// column is a plain interactive session and emits nothing at all.
func TestWorkerFinishedEvent(t *testing.T) {
	tests := []struct {
		name    string
		session agentdb.Session
		trigger *agentdb.ProjectEvent

		wantEvent       bool
		wantDepth       int
		wantInteractive bool
		// emitsAtIdle marks the interactive case: a conversation does not finish
		// when a turn does, so nothing is emitted per message and the event
		// arrives when the archive sweep finds the session gone quiet.
		emitsAtIdle bool
	}{
		{
			name:      "vanilla session emits nothing",
			session:   agentdb.Session{ID: "s1", Customer: "acme"},
			wantEvent: false,
		},
		{
			name:            "human-started worker chat is depth 0, interactive, and emits only at idle",
			session:         agentdb.Session{ID: "s1", Customer: "acme", Worker: "email-answerer"},
			wantEvent:       true,
			wantDepth:       0,
			wantInteractive: true,
			emitsAtIdle:     true,
		},
		{
			name:    "event-triggered job sits one level deeper",
			session: agentdb.Session{ID: "s1", Customer: "acme", Worker: "email-answerer"},
			trigger: &agentdb.ProjectEvent{
				ID: "e1", Project: "acme", Type: "email.received",
				Envelope: agentdb.EventEnvelope{Depth: 2, Source: agentdb.EventSourceExternal},
			},
			wantEvent:       true,
			wantDepth:       3,
			wantInteractive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			r, store, worker := newWorkerEventRunner(t, "s1", completeTurn)
			sess := tt.session
			store.Seed(&sess)
			if tt.trigger != nil {
				worker.triggers["s1"] = tt.trigger
			}

			if _, err := r.CreateSession(ctx, CreateSessionRequest{
				SessionID: "s1", Customer: "acme", Worker: sess.Worker,
			}); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			var buf bytes.Buffer
			if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "hi"}, &buf); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}

			got := worker.events()
			if !tt.wantEvent {
				if len(got) != 0 {
					t.Fatalf("a session with no worker emitted %d events: %#v", len(got), got)
				}
				return
			}
			if tt.emitsAtIdle {
				// The load-bearing half: a chat that has had one message is not
				// a chat that has finished. Emitting here would fire once per
				// message, each time carrying the whole transcript so far.
				if len(got) != 0 {
					t.Fatalf("an interactive session emitted %d events when a turn settled; a conversation concludes when it goes quiet, not per message: %#v", len(got), got)
				}
				r.emitIdleFinish(ctx, "s1")
				got = worker.events()
			}
			if len(got) != 1 {
				t.Fatalf("appended %d events, want 1: %#v", len(got), got)
			}
			ev := got[0]
			if ev.Type != agentdb.EventTypeWorkerFinished {
				t.Errorf("type = %q, want %q", ev.Type, agentdb.EventTypeWorkerFinished)
			}
			if ev.Project != "acme" {
				t.Errorf("project = %q, want acme", ev.Project)
			}
			// The text IS the exchange — that is what the next worker reads.
			if !strings.Contains(ev.Text, "user:\nhi") {
				t.Errorf("transcript missing the user turn: %q", ev.Text)
			}
			if !strings.Contains(ev.Text, "assistant:\nHello world") {
				t.Errorf("transcript missing the merged assistant turn: %q", ev.Text)
			}
			env := ev.Envelope
			if env.Source != agentdb.EventSourceWorker {
				t.Errorf("source = %q, want %q", env.Source, agentdb.EventSourceWorker)
			}
			if env.Worker != "email-answerer" {
				t.Errorf("worker = %q, want email-answerer", env.Worker)
			}
			if env.SessionID != "s1" {
				t.Errorf("session_id = %q, want s1", env.SessionID)
			}
			if env.Depth != tt.wantDepth {
				t.Errorf("depth = %d, want %d", env.Depth, tt.wantDepth)
			}
			if env.Interactive != tt.wantInteractive {
				t.Errorf("interactive = %v, want %v", env.Interactive, tt.wantInteractive)
			}
			if env.AttentionRequested {
				t.Error("attention_requested must be false for a turn that asked for nothing")
			}
			if env.Reason != "" {
				t.Errorf("reason = %q, want empty (only worker.failed carries one)", env.Reason)
			}
		})
	}
}

// TestWorkerFailedEvent is work-plan item E2 (the `worker.failed` half): a
// terminally-errored worker job appends the §8.2 event with reason "error" and
// the error as its text, and the emitter takes the reason as a parameter so
// E3's lease reaper can emit "lost" through the very same stamping.
func TestWorkerFailedEvent(t *testing.T) {
	erroringTurn := []string{
		"event: content_delta\ndata: {\"delta\":\"working\"}\n\n",
		"event: error\ndata: {\"error\":\"model provider returned 503\"}\n\n",
	}
	cancelledTurn := []string{
		"event: content_delta\ndata: {\"delta\":\"working\"}\n\n",
		"event: query_complete\ndata: {\"status\":\"cancelled\"}\n\n",
	}

	t.Run("errored worker job emits worker.failed", func(t *testing.T) {
		ctx := context.Background()
		r, store, worker := newWorkerEventRunner(t, "s1", erroringTurn)
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Worker: "email-answerer"})
		worker.triggers["s1"] = &agentdb.ProjectEvent{
			ID: "e1", Project: "acme", Type: "email.received",
			Envelope: agentdb.EventEnvelope{Depth: 0, Source: agentdb.EventSourceExternal},
		}

		if _, err := r.CreateSession(ctx, CreateSessionRequest{
			SessionID: "s1", Customer: "acme", Worker: "email-answerer",
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		var buf bytes.Buffer
		_ = r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "hi"}, &buf)

		got := worker.events()
		if len(got) != 1 {
			t.Fatalf("appended %d events, want 1: %#v", len(got), got)
		}
		ev := got[0]
		if ev.Type != agentdb.EventTypeWorkerFailed {
			t.Fatalf("type = %q, want %q", ev.Type, agentdb.EventTypeWorkerFailed)
		}
		if ev.Text != "model provider returned 503" {
			t.Errorf("text = %q, want the error from the stream", ev.Text)
		}
		if ev.Envelope.Reason != agentdb.FailureReasonError {
			t.Errorf("reason = %q, want %q", ev.Envelope.Reason, agentdb.FailureReasonError)
		}
		if ev.Envelope.Source != agentdb.EventSourceWorker || ev.Envelope.Worker != "email-answerer" {
			t.Errorf("envelope = %#v, want a worker-sourced envelope", ev.Envelope)
		}
		if ev.Envelope.Depth != 1 {
			t.Errorf("depth = %d, want 1 (trigger depth 0 + 1)", ev.Envelope.Depth)
		}
	})

	t.Run("a vanilla session that errors emits nothing", func(t *testing.T) {
		ctx := context.Background()
		r, store, worker := newWorkerEventRunner(t, "s1", erroringTurn)
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})

		if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s1", Customer: "acme"}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		var buf bytes.Buffer
		_ = r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "hi"}, &buf)
		if got := worker.events(); len(got) != 0 {
			t.Fatalf("a session with no worker emitted %#v", got)
		}
	})

	t.Run("a cancelled turn is not an outcome", func(t *testing.T) {
		// A human pressing stop neither finished nor failed the job.
		ctx := context.Background()
		r, store, worker := newWorkerEventRunner(t, "s1", cancelledTurn)
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Worker: "email-answerer"})

		if _, err := r.CreateSession(ctx, CreateSessionRequest{
			SessionID: "s1", Customer: "acme", Worker: "email-answerer",
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		var buf bytes.Buffer
		_ = r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "hi"}, &buf)
		if got := worker.events(); len(got) != 0 {
			t.Fatalf("a cancelled turn emitted %#v", got)
		}
	})

	t.Run("the reason is a parameter, and it is closed", func(t *testing.T) {
		// E3's lease reaper reuses this exact call with FailureReasonLost.
		store := agentkittest.NewMemStore()
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Worker: "email-answerer"})
		worker := newFakeWorkerEvents(store)
		ctx := context.Background()

		job, ok, err := ResolveWorkerJob(ctx, worker, "s1")
		if err != nil || !ok {
			t.Fatalf("ResolveWorkerJob: ok=%v err=%v", ok, err)
		}
		if _, err := EmitWorkerFailed(ctx, worker, job, agentdb.FailureReasonLost, "lease expired"); err != nil {
			t.Fatalf("EmitWorkerFailed(lost): %v", err)
		}
		got := worker.events()
		if len(got) != 1 {
			t.Fatalf("appended %d events, want 1", len(got))
		}
		if got[0].Envelope.Reason != agentdb.FailureReasonLost {
			t.Errorf("reason = %q, want %q", got[0].Envelope.Reason, agentdb.FailureReasonLost)
		}
		if got[0].Type != agentdb.EventTypeWorkerFailed || got[0].Envelope.Worker != "email-answerer" {
			t.Errorf("reaper event = %#v, want the same stamping as the runner's", got[0])
		}

		if _, err := EmitWorkerFailed(ctx, worker, job, "exploded", "boom"); err == nil {
			t.Fatal("an unknown reason must be refused loudly — the vocabulary is closed")
		}
	})

	t.Run("resolve reports a vanilla session rather than erroring", func(t *testing.T) {
		store := agentkittest.NewMemStore()
		store.Seed(&agentdb.Session{ID: "s1", Customer: "acme"})
		worker := newFakeWorkerEvents(store)
		_, ok, err := ResolveWorkerJob(context.Background(), worker, "s1")
		if err != nil {
			t.Fatalf("ResolveWorkerJob: %v", err)
		}
		if ok {
			t.Error("a session with an empty worker column must report ok=false")
		}
	})
}

// TestArchiveSweepEmitsTheChatFinish is the other half of the interactive
// contract: the archive sweep is what turns "this conversation went quiet" into
// a worker.finished event, so a project needs exactly ONE subscription to hear
// about both dispatched jobs and human conversations.
//
// It also pins the two things that would otherwise double-count or over-fire:
// a chat emits once per idle period however many messages it contained, and a
// second sweep over an already-archived session emits nothing new.
func TestArchiveSweepEmitsTheChatFinish(t *testing.T) {
	ctx := context.Background()
	r, store, worker := newWorkerEventRunner(t, "s1", completeTurn)
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Worker: "marketing-manager"})

	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s1", Customer: "acme", Worker: "marketing-manager",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Three messages in one conversation. Under the old per-turn emission this
	// alone produced three events, each carrying the whole transcript so far.
	for i := 0; i < 3; i++ {
		var buf bytes.Buffer
		if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "hi"}, &buf); err != nil {
			t.Fatalf("SendMessage %d: %v", i, err)
		}
	}
	if got := worker.events(); len(got) != 0 {
		t.Fatalf("a 3-message chat emitted %d events before going idle, want 0", len(got))
	}

	r.deps.Policy.ArchiveTimeout = time.Millisecond
	r.setIdle("s1", time.Hour)
	r.archiveIdleOnce(ctx)

	got := worker.events()
	if len(got) != 1 {
		t.Fatalf("the archive sweep appended %d events, want exactly 1: %#v", len(got), got)
	}
	if got[0].Type != agentdb.EventTypeWorkerFinished {
		t.Errorf("type = %q, want %q", got[0].Type, agentdb.EventTypeWorkerFinished)
	}
	if !got[0].Envelope.Interactive {
		t.Error("a chat's finish must be stamped interactive — it is how a subscription tells a conversation from a job")
	}
	if got[0].Envelope.Worker != "marketing-manager" {
		t.Errorf("worker = %q, want marketing-manager", got[0].Envelope.Worker)
	}
	// The event carries the whole conversation, not just the last turn.
	if n := strings.Count(got[0].Text, "user:\nhi"); n != 3 {
		t.Errorf("transcript carries %d user turns, want all 3", n)
	}

	// A second sweep finds nothing left to archive, so nothing is re-emitted.
	r.archiveIdleOnce(ctx)
	if got := worker.events(); len(got) != 1 {
		t.Errorf("a second sweep emitted again: %d events total, want 1", len(got))
	}
}

// TestDispatchedJobDoesNotEmitTwice guards the seam between the two emission
// points. A dispatched job (one triggered by an event, so Interactive=false)
// emits when its turn settles; if it were ALSO emitted when its container is
// later reclaimed, every job in the project would be counted twice and every
// downstream subscription would fire twice.
func TestDispatchedJobDoesNotEmitTwice(t *testing.T) {
	ctx := context.Background()
	r, store, worker := newWorkerEventRunner(t, "s1", completeTurn)
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Worker: "poster"})
	worker.triggers["s1"] = &agentdb.ProjectEvent{
		ID: "e1", Project: "acme", Type: "post.due",
		Envelope: agentdb.EventEnvelope{Depth: 0, Source: agentdb.EventSourceSchedule},
	}

	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s1", Customer: "acme", Worker: "poster",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var buf bytes.Buffer
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "go"}, &buf); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := worker.events(); len(got) != 1 {
		t.Fatalf("a dispatched job emitted %d events when its turn settled, want 1", len(got))
	}

	r.deps.Policy.ArchiveTimeout = time.Millisecond
	r.setIdle("s1", time.Hour)
	r.archiveIdleOnce(ctx)

	if got := worker.events(); len(got) != 1 {
		t.Errorf("archiving a dispatched job's session emitted a second event: %d total, want 1", len(got))
	}
}
