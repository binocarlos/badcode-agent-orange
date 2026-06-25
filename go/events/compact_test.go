package events

import (
	"strings"
	"testing"
)

// helper to build a simple Envelope quickly.
func env(t Type, data map[string]any) Envelope {
	if data == nil {
		data = map[string]any{}
	}
	return Envelope{Type: t, Data: data}
}

// ---------------------------------------------------------------------------
// Compact — transient drop
// ---------------------------------------------------------------------------

func TestCompact_DropsTransientTypes(t *testing.T) {
	transients := []Type{
		Heartbeat, ToolProgress, ToolInputDelta, ActivityUpdate, SystemStatus, HookEvent, Connected,
	}
	in := make([]Envelope, len(transients))
	for i, typ := range transients {
		in[i] = env(typ, nil)
	}
	// Add a non-transient event so the output is not trivially empty.
	in = append(in, env(MessageEnd, nil))

	out := Compact(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 event after dropping all transients, got %d: %#v", len(out), out)
	}
	if out[0].Type != MessageEnd {
		t.Errorf("surviving event = %q, want %q", out[0].Type, MessageEnd)
	}
}

// ---------------------------------------------------------------------------
// Compact — consecutive delta merging
// ---------------------------------------------------------------------------

func TestCompact_MergesConsecutiveContentDelta(t *testing.T) {
	in := []Envelope{
		env(ContentDelta, map[string]any{"delta": "Hello "}),
		env(ContentDelta, map[string]any{"delta": "world"}),
		env(ContentDelta, map[string]any{"delta": "!"}),
	}
	out := Compact(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged event, got %d", len(out))
	}
	d, _ := out[0].Data["delta"].(string)
	if d != "Hello world!" {
		t.Errorf("merged delta = %q, want %q", d, "Hello world!")
	}
}

func TestCompact_MergesConsecutiveThinkingDelta(t *testing.T) {
	in := []Envelope{
		env(ThinkingDelta, map[string]any{"delta": "think "}),
		env(ThinkingDelta, map[string]any{"delta": "more"}),
	}
	out := Compact(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged thinking event, got %d", len(out))
	}
	d, _ := out[0].Data["delta"].(string)
	if d != "think more" {
		t.Errorf("merged delta = %q, want %q", d, "think more")
	}
}

func TestCompact_DoesNotMergeNonConsecutiveContentDelta(t *testing.T) {
	// A MessageEnd in between must prevent merging.
	in := []Envelope{
		env(ContentDelta, map[string]any{"delta": "A"}),
		env(MessageEnd, nil),
		env(ContentDelta, map[string]any{"delta": "B"}),
	}
	out := Compact(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 events (no merge across MessageEnd), got %d", len(out))
	}
}

func TestCompact_DoesNotMergeContentAndThinkingDelta(t *testing.T) {
	// content_delta followed by thinking_delta must NOT be merged.
	in := []Envelope{
		env(ContentDelta, map[string]any{"delta": "A"}),
		env(ThinkingDelta, map[string]any{"delta": "B"}),
	}
	out := Compact(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 separate events, got %d", len(out))
	}
}

// ---------------------------------------------------------------------------
// Compact — empty user_message drop
// ---------------------------------------------------------------------------

func TestCompact_DropsEmptyUserMessage(t *testing.T) {
	in := []Envelope{
		env(UserMessage, map[string]any{"content": ""}),
		env(UserMessage, map[string]any{"content": "   "}),
		env(UserMessage, map[string]any{"content": "real message"}),
	}
	out := Compact(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 user_message (non-empty) to survive, got %d", len(out))
	}
	c, _ := out[0].Data["content"].(string)
	if c != "real message" {
		t.Errorf("unexpected surviving content = %q", c)
	}
}

func TestCompact_DropsUserMessageMissingContent(t *testing.T) {
	// user_message with no "content" key at all should be dropped (TrimSpace("") == "").
	in := []Envelope{
		env(UserMessage, nil),
		env(MessageEnd, nil),
	}
	out := Compact(in)
	if len(out) != 1 || out[0].Type != MessageEnd {
		t.Fatalf("expected only MessageEnd to survive, got %#v", out)
	}
}

// ---------------------------------------------------------------------------
// Compact — terminal events are kept
// ---------------------------------------------------------------------------

func TestCompact_KeepsTerminalEvents(t *testing.T) {
	in := []Envelope{
		env(QueryComplete, map[string]any{"status": "complete"}),
		env(Error, map[string]any{"message": "oops"}),
	}
	out := Compact(in)
	if len(out) != 2 {
		t.Fatalf("expected both terminal events, got %d", len(out))
	}
	if out[0].Type != QueryComplete || out[1].Type != Error {
		t.Errorf("unexpected types: %q %q", out[0].Type, out[1].Type)
	}
}

// ---------------------------------------------------------------------------
// ExtractSearchText
// ---------------------------------------------------------------------------

func TestExtractSearchText_ConcatenatesUserAndAssistant(t *testing.T) {
	in := []Envelope{
		env(UserMessage, map[string]any{"content": "what is the sky?"}),
		env(ContentDelta, map[string]any{"delta": "It is blue."}),
	}
	got := ExtractSearchText(in)
	if !strings.Contains(got, "what is the sky?") {
		t.Errorf("search text missing user content: %q", got)
	}
	if !strings.Contains(got, "It is blue.") {
		t.Errorf("search text missing assistant delta: %q", got)
	}
}

func TestExtractSearchText_CapsAtMaxLen(t *testing.T) {
	// Build a single delta larger than maxSearchTextLen.
	big := strings.Repeat("x", maxSearchTextLen+1000)
	in := []Envelope{
		env(ContentDelta, map[string]any{"delta": big}),
	}
	got := ExtractSearchText(in)
	if len(got) > maxSearchTextLen {
		t.Errorf("search text length = %d, want <= %d", len(got), maxSearchTextLen)
	}
}

func TestExtractSearchText_IgnoresNonTextEvents(t *testing.T) {
	in := []Envelope{
		env(Heartbeat, nil),
		env(ToolUseStart, map[string]any{"name": "search"}),
		env(UserMessage, map[string]any{"content": "hello"}),
	}
	got := ExtractSearchText(in)
	if strings.Contains(got, "search") {
		t.Errorf("search text should not include tool names: %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("search text should include user message: %q", got)
	}
}
