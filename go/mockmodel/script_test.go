package mockmodel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuilderProducesTurnsAndIsJSONPortable(t *testing.T) {
	s := NewScript().
		Turn().Text("a joke").
		Turn().StreamText("a long story here", 4).
		Turn().ToolUse("render_table", map[string]any{"title": "X"}).ThenText("done").
		Build()

	if len(s.Turns) != 3 {
		t.Fatalf("want 3 turns, got %d", len(s.Turns))
	}
	// StreamText splits into >1 text block so the mock emits multiple deltas.
	if len(s.Turns[1].Blocks) < 2 {
		t.Errorf("StreamText should split into multiple blocks, got %d", len(s.Turns[1].Blocks))
	}
	// turn 2 = tool_use + trailing text
	if s.Turns[2].Blocks[0].Type != "tool_use" || s.Turns[2].Blocks[1].Type != "text" {
		t.Errorf("ToolUse().ThenText() shape wrong: %+v", s.Turns[2].Blocks)
	}
	// Portable: round-trips through JSON unchanged (shared with the TS mock-server schema).
	b, _ := json.Marshal(s)
	var back Script
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("json round-trip: %v", err)
	}
	if len(back.Turns) != 3 {
		t.Errorf("round-trip lost turns")
	}
}

func TestSSEForTurnMatchesServerOutput(t *testing.T) {
	s := NewScript().Turn().Text("HELLO_FIXTURE").Build()
	sse := s.SSEForTurn(t, 0)
	if !strings.Contains(sse, "HELLO_FIXTURE") || !strings.Contains(sse, "message_stop") {
		t.Errorf("SSEForTurn missing content/terminator: %s", sse)
	}
}
