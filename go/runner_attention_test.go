package agentkit

// runner_attention_test.go — the §8.2 `attention_requested` half of
// `worker.finished`, and the clearing semantics.
//
// The stamp's lifetime matches the EMISSION UNIT, and there are two of them:
//   - a dispatched job is one turn, so the stamp is per turn (these tests, each
//     of which seeds a triggering event to sit on that path);
//   - an interactive chat is a conversation, so the stamp rides until the chat
//     goes quiet and the archive sweep emits its finish
//     (TestChatAttentionSurvivesUntilTheConversationEnds, below).

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// TestWorkerFinishedCarriesAttentionRequested is the defect H2 and E2 both
// predicted: a turn in which the worker called `request_human_attention` stamps
// the session (agentdb.CreateAttentionRequest), and §8.2 requires that stamp to
// be copied onto the `worker.finished` envelope.
func TestWorkerFinishedCarriesAttentionRequested(t *testing.T) {
	ctx := context.Background()
	r, store, worker := newWorkerEventRunner(t, "s1", completeTurn)
	// The tool ran during the turn, so the session row is already stamped by the
	// time the turn settles.
	store.Seed(&agentdb.Session{
		ID: "s1", Customer: "acme", Worker: "tweet-author", AttentionRequested: true,
	})

	worker.triggers["s1"] = &agentdb.ProjectEvent{
		ID: "e1", Project: "acme", Type: "tweet.due",
		Envelope: agentdb.EventEnvelope{Source: agentdb.EventSourceSchedule},
	}
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s1", Customer: "acme", Worker: "tweet-author",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var buf bytes.Buffer
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "draft a tweet"}, &buf); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	got := worker.events()
	if len(got) != 1 {
		t.Fatalf("appended %d events, want 1: %#v", len(got), got)
	}
	if !got[0].Envelope.AttentionRequested {
		t.Fatalf("worker.finished must carry attention_requested: true for the turn that asked")
	}
}

// TestAttentionRequestedIsClearedOnceCopied pins the "that turn" semantics of
// §8.2. The stamp is per-TURN, so the emitter consumes it: copy it onto the
// envelope, then clear it. Left set, it would ride every later `worker.finished`
// in the same session and a reviewer subscription filtering on the flag would
// fire for ever — a subtler bug than the one being fixed.
//
// Clearing costs nothing that matters: what a human is actually owed lives in
// the open `attention_requests` row (which the sweep resolves) and in the
// delivery parked at `awaiting_human` (§8.4), neither of which this touches.
func TestAttentionRequestedIsClearedOnceCopied(t *testing.T) {
	ctx := context.Background()
	r, store, worker := newWorkerEventRunner(t, "s1", completeTurn)
	store.Seed(&agentdb.Session{
		ID: "s1", Customer: "acme", Worker: "tweet-author", AttentionRequested: true,
	})
	worker.triggers["s1"] = &agentdb.ProjectEvent{
		ID: "e1", Project: "acme", Type: "tweet.due",
		Envelope: agentdb.EventEnvelope{Source: agentdb.EventSourceSchedule},
	}
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s1", Customer: "acme", Worker: "tweet-author",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var buf bytes.Buffer
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "draft a tweet"}, &buf); err != nil {
		t.Fatalf("SendMessage 1: %v", err)
	}
	sess, err := store.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.AttentionRequested {
		t.Fatalf("the stamp must be cleared once it has been copied onto the envelope")
	}

	// The human replies. This turn did not ask for anything.
	buf.Reset()
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "post it"}, &buf); err != nil {
		t.Fatalf("SendMessage 2: %v", err)
	}
	got := worker.events()
	if len(got) != 2 {
		t.Fatalf("appended %d events, want 2: %#v", len(got), got)
	}
	if !got[0].Envelope.AttentionRequested {
		t.Fatalf("the first turn asked for a human and must say so")
	}
	if got[1].Envelope.AttentionRequested {
		t.Fatalf("a later turn must not inherit the previous turn's attention_requested")
	}
}

// TestAttentionStampIsClearedByAFailedTurnToo: the stamp never outlives the
// turn that set it, even when that turn errors. `worker.failed` carries no
// attention_requested field, so the alternative — keep the stamp and let the
// NEXT turn report it — would emit a `worker.finished` claiming a turn asked for
// a human when it did not, which is precisely what §8.2's "that turn" forbids.
// Nothing is lost by clearing: the webhook already fired, the
// `attention_requests` row is still open, and the human still has the permalink.
func TestAttentionStampIsClearedByAFailedTurnToo(t *testing.T) {
	ctx := context.Background()
	erroringTurn := []string{
		"event: content_delta\ndata: {\"delta\":\"drafting\"}\n\n",
		"event: error\ndata: {\"error\":\"model provider returned 503\"}\n\n",
	}
	r, store, worker := newWorkerEventRunner(t, "s1", erroringTurn)
	store.Seed(&agentdb.Session{
		ID: "s1", Customer: "acme", Worker: "tweet-author", AttentionRequested: true,
	})
	worker.triggers["s1"] = &agentdb.ProjectEvent{
		ID: "e1", Project: "acme", Type: "tweet.due",
		Envelope: agentdb.EventEnvelope{Source: agentdb.EventSourceSchedule},
	}
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s1", Customer: "acme", Worker: "tweet-author",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var buf bytes.Buffer
	_ = r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "draft a tweet"}, &buf)

	got := worker.events()
	if len(got) != 1 || got[0].Type != agentdb.EventTypeWorkerFailed {
		t.Fatalf("want one worker.failed, got %#v", got)
	}
	sess, err := store.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.AttentionRequested {
		t.Fatalf("the stamp must not outlive its turn, even when the turn errored")
	}
}

// TestChatAttentionSurvivesUntilTheConversationEnds is the interactive half, and
// the bug it guards is one the idle-emission change created.
//
// A chat no longer emits worker.finished per turn, so consuming the stamp at the
// end of a turn — as a dispatched job does — would throw it away before anything
// had reported it: a worker that called request_human_attention mid-conversation
// would leave no trace on the event spine at all, and any subscription watching
// for it would never fire. The stamp therefore rides until the conversation's
// own finish event, and is consumed there.
func TestChatAttentionSurvivesUntilTheConversationEnds(t *testing.T) {
	ctx := context.Background()
	r, store, worker := newWorkerEventRunner(t, "s1", completeTurn)
	// No trigger event: this is a human chat, not a dispatched job.
	store.Seed(&agentdb.Session{
		ID: "s1", Customer: "acme", Worker: "marketing-manager", AttentionRequested: true,
	})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s1", Customer: "acme", Worker: "marketing-manager",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// The turn in which the worker asked, and a later turn that asked nothing.
	for _, msg := range []string{"draft a tweet", "thanks"} {
		var buf bytes.Buffer
		if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: msg}, &buf); err != nil {
			t.Fatalf("SendMessage(%q): %v", msg, err)
		}
	}
	sess, err := store.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !sess.AttentionRequested {
		t.Fatal("a chat's attention stamp must survive the turn — the conversation has not finished yet, so nothing has reported it")
	}

	r.deps.Policy.ArchiveTimeout = time.Millisecond
	r.setIdle("s1", time.Hour)
	r.archiveIdleOnce(ctx)

	got := worker.events()
	if len(got) != 1 {
		t.Fatalf("appended %d events, want 1: %#v", len(got), got)
	}
	if !got[0].Envelope.AttentionRequested {
		t.Error("the conversation asked for a human and its finish event must say so")
	}

	// Consumed, exactly as a job consumes it: a chat picked back up starts clean.
	sess, err = store.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.AttentionRequested {
		t.Error("the stamp must be consumed once the conversation's finish has carried it")
	}
}
