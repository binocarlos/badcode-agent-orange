package mockmodel

import "testing"

// ScriptBuilder builds a Script declaratively via a fluent API.
type ScriptBuilder struct{ s *Script }

// TurnBuilder builds a single Turn within a ScriptBuilder.
type TurnBuilder struct {
	sb  *ScriptBuilder
	cur *Turn
}

// NewScript returns a new empty ScriptBuilder.
func NewScript() *ScriptBuilder { return &ScriptBuilder{s: &Script{}} }

// Turn appends a new (empty) Turn and returns its builder.
func (b *ScriptBuilder) Turn() *TurnBuilder {
	b.s.Turns = append(b.s.Turns, Turn{})
	return &TurnBuilder{sb: b, cur: &b.s.Turns[len(b.s.Turns)-1]}
}

// Build returns the finished Script.
func (b *ScriptBuilder) Build() *Script { return b.s }

// Turn allows chaining a new Turn directly off a TurnBuilder.
func (tb *TurnBuilder) Turn() *TurnBuilder { return tb.sb.Turn() }

// Build returns the finished Script (delegates to ScriptBuilder).
func (tb *TurnBuilder) Build() *Script { return tb.sb.Build() }

// Text appends a text block to the current turn.
func (tb *TurnBuilder) Text(s string) *TurnBuilder {
	tb.cur.Blocks = append(tb.cur.Blocks, Block{Type: "text", Text: s})
	return tb
}

// ThenText is an alias for Text, used after ToolUse for readability.
func (tb *TurnBuilder) ThenText(s string) *TurnBuilder { return tb.Text(s) }

// ToolUse appends a tool_use block to the current turn.
func (tb *TurnBuilder) ToolUse(name string, input map[string]any) *TurnBuilder {
	tb.cur.Blocks = append(tb.cur.Blocks, Block{Type: "tool_use", Name: name, Input: input})
	return tb
}

// StreamText splits s into chunkChars-sized text blocks so the mock emits a
// separate content_block_delta per chunk — i.e. a genuinely streamed response.
func (tb *TurnBuilder) StreamText(s string, chunkChars int) *TurnBuilder {
	if chunkChars <= 0 {
		chunkChars = 8
	}
	r := []rune(s)
	for i := 0; i < len(r); i += chunkChars {
		end := i + chunkChars
		if end > len(r) {
			end = len(r)
		}
		tb.cur.Blocks = append(tb.cur.Blocks, Block{Type: "text", Text: string(r[i:end])})
	}
	return tb
}

// SSEForTurn returns the exact SSE the mock would emit for turn i. Used to write
// the shared fixture the frontend jsdom test replays.
func (s *Script) SSEForTurn(t *testing.T, i int) string {
	t.Helper()
	sp := &Server{t: t}
	return sp.buildTurnSSE(s.Turns[i])
}
