package agentkit

import (
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/events"
)

// TestReconstructTranscriptCarriesToolActivity is the counterpart to
// TestReconstructConversation, and pins the distinction between the two
// reconstructions: rehydration is told only what was SAID (it is a contract with
// the harness, and synthesised tool lines would put words in a resumed model's
// mouth), while the transcript — what a worker.finished event carries, and
// therefore what every downstream archiving, reviewing and critiquing worker
// reads — also carries what was DONE.
//
// The omission this test exists to prevent was not cosmetic. Without tool lines
// the entire downstream evidence base is model narration, which is frequently
// unfaithful to the actions that produced an outcome.
func TestReconstructTranscriptCarriesToolActivity(t *testing.T) {
	delta := func(s string) events.Envelope {
		return events.Envelope{Type: events.ContentDelta, Data: map[string]any{"delta": s}}
	}
	evs := []events.Envelope{
		{Type: events.UserMessage, Data: map[string]any{"content": "Post the update."}},
		delta("Posting now."),
		{Type: events.ToolUseStart, Data: map[string]any{
			"toolCallId": "t1", "toolName": "instagram_post",
			"input": map[string]any{"caption": "new work"},
		}},
		{Type: events.ToolUseEnd, Data: map[string]any{
			"toolCallId": "t1", "output": "https://example.test/p/1",
		}},
		{Type: events.ToolUseStart, Data: map[string]any{
			"toolCallId": "t2", "toolName": "twitter_post", "input": map[string]any{"text": "hi"},
		}},
		{Type: events.ToolUseEnd, Data: map[string]any{
			"toolCallId": "t2", "isError": true, "output": "rate limited",
		}},
		delta(" Done."),
	}

	// The rehydration shape is unchanged: no tool activity at all.
	rehydrated := reconstructConversation(evs)
	for _, m := range rehydrated {
		if strings.Contains(m.Content, "[tool]") {
			t.Fatalf("rehydration must carry no tool activity, got %q", m.Content)
		}
	}

	got := renderConversation(reconstructTranscript(evs))
	for _, want := range []string{
		`[tool] instagram_post({"caption":"new work"}) → ok: https://example.test/p/1`,
		`[tool] twitter_post({"text":"hi"}) → error: rate limited`,
		"Posting now.",
		"Done.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q\n--- got ---\n%s", want, got)
		}
	}

	// Ordering is faithful: the successful post is recorded before the failed one,
	// and both sit inside the assistant turn that made them.
	if i, j := strings.Index(got, "instagram_post"), strings.Index(got, "twitter_post"); i < 0 || j < 0 || i > j {
		t.Errorf("tool lines out of order: instagram at %d, twitter at %d\n%s", i, j, got)
	}
}

// TestReconstructTranscriptBoundsToolPayloads pins the caps. A transcript is the
// verbatim text of a worker.finished event, which is read as the next worker's
// first message — so a single large file write or command output must never be
// able to become the payload of an event.
func TestReconstructTranscriptBoundsToolPayloads(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	evs := []events.Envelope{
		{Type: events.ToolUseStart, Data: map[string]any{
			"toolCallId": "t1", "toolName": "write_file", "input": map[string]any{"body": huge},
		}},
		{Type: events.ToolUseEnd, Data: map[string]any{"toolCallId": "t1", "output": huge}},
	}
	got := renderConversation(reconstructTranscript(evs))
	if len(got) > 2*(maxToolArgsChars+maxToolOutputChars)+200 {
		t.Errorf("tool line is unbounded: %d chars", len(got))
	}
	if strings.Count(got, "…") != 2 {
		t.Errorf("want both args and output marked as truncated, got %q", got)
	}
	if strings.Contains(got, "\n[tool]") && strings.Count(got, "[tool]") != 1 {
		t.Errorf("want exactly one tool line, got %q", got)
	}
}

// TestReconstructTranscriptRendersUnfinishedTools covers the crash case: a tool
// that started and never reported back must still appear, because a transcript
// that silently omits an action is worse than one that admits it does not know
// the outcome. This is the shape a `lost` session leaves behind.
func TestReconstructTranscriptRendersUnfinishedTools(t *testing.T) {
	evs := []events.Envelope{
		{Type: events.ToolUseStart, Data: map[string]any{
			"toolCallId": "t1", "toolName": "send_email", "input": map[string]any{"to": "a@b.test"},
		}},
	}
	got := renderConversation(reconstructTranscript(evs))
	if !strings.Contains(got, `[tool] send_email({"to":"a@b.test"}) → (no result)`) {
		t.Errorf("unfinished tool not rendered: %q", got)
	}
}

// TestReconstructTranscriptCollapsesMultilinePayloads keeps a tool line a line.
// A multi-line command or output would otherwise be indistinguishable from the
// conversation around it — which is precisely the confusion a reviewing worker
// must not be exposed to.
func TestReconstructTranscriptCollapsesMultilinePayloads(t *testing.T) {
	evs := []events.Envelope{
		{Type: events.ToolUseStart, Data: map[string]any{
			"toolCallId": "t1", "toolName": "bash", "input": "ls -la\nrm -rf /tmp/x",
		}},
		{Type: events.ToolUseEnd, Data: map[string]any{"toolCallId": "t1", "output": "a\nb\nc"}},
	}
	got := renderConversation(reconstructTranscript(evs))
	body := strings.TrimPrefix(got, "assistant:\n")
	if strings.Contains(body, "\n") {
		t.Errorf("tool line spans several lines: %q", got)
	}
	if !strings.Contains(got, "ls -la rm -rf /tmp/x") || !strings.Contains(got, "ok: a b c") {
		t.Errorf("payload not collapsed onto one line: %q", got)
	}
}
