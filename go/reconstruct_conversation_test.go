package agentkit

import (
	"testing"

	"github.com/bayes-price/agentkit/events"
)

// TestReconstructConversation verifies that the rehydration helper rebuilds the
// ordered user/assistant message list from persisted query events: user_message
// envelopes become user turns, consecutive content_delta envelopes accumulate
// into one assistant turn flushed at each turn boundary, and all other event
// types are skipped. This is the inverse of how the live SSE stream is captured
// and is what restoreToWorker POSTs to the sandbox /load-conversation endpoint.
func TestReconstructConversation(t *testing.T) {
	delta := func(s string) events.Envelope {
		return events.Envelope{Type: events.ContentDelta, Data: map[string]any{"delta": s}}
	}
	user := func(s string) events.Envelope {
		return events.Envelope{Type: events.UserMessage, Data: map[string]any{"content": s}}
	}

	evs := []events.Envelope{
		// Turn 1.
		user("Remember the codeword BANANA."),
		events.Envelope{Type: events.MessageStart, Data: map[string]any{"messageId": "m1"}},
		delta("Got it — "),
		delta("the codeword is BANANA."),
		events.Envelope{Type: events.ToolUseStart, Data: map[string]any{"toolName": "noop"}}, // skipped
		events.Envelope{Type: events.MessageEnd, Data: map[string]any{"messageId": "m1"}},
		events.Envelope{Type: events.QueryComplete, Data: map[string]any{"status": "completed"}}, // skipped
		// Turn 2.
		user("What was the codeword?"),
		events.Envelope{Type: events.ThinkingDelta, Data: map[string]any{"delta": "recalling..."}}, // skipped
		// delta carried as {"text": ...} map shape, plus a plain-string delta.
		events.Envelope{Type: events.ContentDelta, Data: map[string]any{"delta": map[string]any{"text": "The codeword "}}},
		delta("is BANANA."),
	}

	got := reconstructConversation(evs)

	want := []conversationMessage{
		{Role: "user", Content: "Remember the codeword BANANA."},
		{Role: "assistant", Content: "Got it — the codeword is BANANA."},
		{Role: "user", Content: "What was the codeword?"},
		{Role: "assistant", Content: "The codeword is BANANA."},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestReconstructConversation_DropsEmptyAndStandalone verifies empty user
// messages are dropped, a trailing assistant turn is flushed at end-of-stream,
// and an event stream with no conversational content yields no messages.
func TestReconstructConversation_DropsEmptyAndStandalone(t *testing.T) {
	evs := []events.Envelope{
		{Type: events.UserMessage, Data: map[string]any{"content": "   "}}, // empty -> dropped
		{Type: events.UserMessage, Data: map[string]any{"content": "hello"}},
		{Type: events.ContentDelta, Data: map[string]any{"delta": "hi there"}}, // flushed at end
	}
	got := reconstructConversation(evs)
	want := []conversationMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// No conversational events at all -> empty result (so the caller skips the POST).
	none := reconstructConversation([]events.Envelope{
		{Type: events.SessionInfo, Data: map[string]any{}},
		{Type: events.Heartbeat, Data: map[string]any{}},
	})
	if len(none) != 0 {
		t.Errorf("expected no messages, got %+v", none)
	}
}
