package mockmodel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// postMessages sends a minimal Anthropic /v1/messages request carrying `assistantTurns`
// prior assistant messages, and returns the raw SSE body.
func postMessages(t *testing.T, url string, assistantTurns int) string {
	t.Helper()
	msgs := []map[string]any{{"role": "user", "content": "hi"}}
	for i := 0; i < assistantTurns; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": "prev"})
		msgs = append(msgs, map[string]any{"role": "user", "content": "more"})
	}
	body, _ := json.Marshal(map[string]any{"model": "claude", "messages": msgs})
	resp, err := http.Post(url+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		sb.WriteString(sc.Text())
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestScriptedTurnSelectionByAssistantCount(t *testing.T) {
	script := &Script{Turns: []Turn{
		{Blocks: []Block{{Type: "text", Text: "FIRST_TURN"}}},
		{Blocks: []Block{{Type: "text", Text: "SECOND_TURN"}}},
	}}
	srv := NewScripted(t, script)
	defer srv.Close()

	if out := postMessages(t, srv.URL, 0); !strings.Contains(out, "FIRST_TURN") {
		t.Errorf("turn 0: expected FIRST_TURN, got: %s", out)
	}
	if out := postMessages(t, srv.URL, 1); !strings.Contains(out, "SECOND_TURN") {
		t.Errorf("turn 1: expected SECOND_TURN, got: %s", out)
	}
}

func TestScriptedToolUseEmitsToolBlock(t *testing.T) {
	script := &Script{Turns: []Turn{
		{Blocks: []Block{{Type: "tool_use", Name: "render_table", Input: map[string]any{"title": "T1"}}}},
	}}
	srv := NewScripted(t, script)
	defer srv.Close()

	out := postMessages(t, srv.URL, 0)
	if !strings.Contains(out, `"type":"tool_use"`) || !strings.Contains(out, "render_table") {
		t.Errorf("expected tool_use/render_table in SSE, got: %s", out)
	}
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Errorf("expected stop_reason tool_use, got: %s", out)
	}
}
