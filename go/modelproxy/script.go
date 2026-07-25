package modelproxy

// The scriptable mock model — how an OFFLINE test makes the model call a tool.
//
// # Why this exists
//
// `MockHandler` serves one fixed canned SSE turn, so a stack running without an
// API key can never emit a `tool_use` block. That made every tool-shaped
// assertion (G1's headline: a reviewer worker rewriting another worker's prompt
// through `worker_prompt_write`) reachable only with a real key — i.e. the
// offline acceptance bar was unreachable by construction.
//
// A *script table* fixes that. It is stack configuration, not API surface:
// agentd reads it from the environment at boot (AGENTKIT_MOCK_MODEL_SCRIPT /
// AGENTKIT_MOCK_MODEL_SCRIPT_FILE, see cmd/agentd/modelproxy.go) and serves
// scripted turns instead of the canned one. With no script configured the
// handler is byte-for-byte today's `MockHandler` — inert unless asked for.
//
// # The shape
//
//	{
//	  "rules": [
//	    { "match":  "email-reviewer",       // substring of the raw request body
//	      "absent": "",                     // optional: rule skipped if PRESENT
//	      "turns":  [
//	        { "blocks": [                   // turn 0 — the model's first reply
//	            {"type":"text","text":"The answerer is curt."},
//	            {"type":"tool_use","name":"mcp__core__worker_prompt_write",
//	             "input":{"worker":"email-answerer","system_prompt":"…","rationale":"…"}} ] },
//	        { "blocks": [                   // turn 1 — after the tool result returns
//	            {"type":"text","text":"Rewrote the answerer's prompt."} ] } ] }
//	  ]
//	}
//
// A bare `{"turns":[…]}` is shorthand for a single catch-all rule.
//
// # Selection, and why it is stateless
//
//  1. WHICH SCRIPT: the first rule whose `match` substring is present in the raw
//     request body (and whose `absent` substring is not) wins. `match:""`
//     matches anything. The natural key is the worker name: it appears in every
//     request's composed system prompt (§6.2), so one stack-level table can
//     drive many workers, each with its own script.
//  2. WHICH TURN — the one and only sequencing mechanism: the turn index is the
//     number of `role:"assistant"` messages in the request body. The model
//     harness replays the whole conversation on every call, so that count IS the
//     turn number. Turn 0 is the model's first reply; turn 1 is what it says
//     once the tool result has come back. No per-session counter, no
//     cross-test contamination, no ordering dependence between parallel sessions
//     or between retries of the same test.
//  3. A request that matches no rule, or whose turn index runs past the rule's
//     turns, gets the canned text response. Running out of script can therefore
//     never produce a stray tool call.
//
// `absent` is a match predicate, NOT a second sequencer: it exists so a rule can
// be kept away from requests it must never answer (an auxiliary harness call, or
// anything already carrying a `tool_result`). Sequencing is always the turn list.
//
// Tool-use ids are derived from the turn and block index rather than a counter,
// so the same request always produces byte-identical SSE.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Block is a single content block in a scripted turn.
// Type is one of "text", "thinking", "tool_use".
type Block struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

// Turn is one complete assistant response: an ordered list of content blocks.
type Turn struct {
	Blocks []Block `json:"blocks"`
}

// Script is an ordered list of turns, indexed by assistant-message count.
type Script struct {
	Turns []Turn `json:"turns"`
}

// ScriptRule is one entry in a ScriptTable: a body predicate plus the turns to
// serve when it holds.
type ScriptRule struct {
	// Match is a substring that must appear in the raw request body for this
	// rule to apply. Empty matches every request.
	Match string `json:"match,omitempty"`
	// Absent is a substring that must NOT appear for this rule to apply. Empty
	// imposes no constraint. It narrows the match; it does not sequence.
	Absent string `json:"absent,omitempty"`
	// Turns are served by assistant-message count: turn 0 is the model's first
	// reply, turn 1 what it says after a tool result. Required, non-empty.
	Turns []Turn `json:"turns"`
}

// ScriptTable is the whole configured script: ordered rules, first match wins.
type ScriptTable struct {
	Rules []ScriptRule `json:"rules,omitempty"`
	// Turns is shorthand for a single catch-all rule. Normalised away by
	// ParseScriptTable; never read after parsing.
	Turns []Turn `json:"turns,omitempty"`
}

var blockTypes = map[string]bool{"text": true, "thinking": true, "tool_use": true}

// ParseScriptTable decodes and validates a script table. An empty (or
// whitespace-only) input returns (nil, nil): no script configured. Unknown
// fields are rejected — a typo in a test's script must fail loudly at boot
// rather than silently serve the canned response.
func ParseScriptTable(s string) (*ScriptTable, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var t ScriptTable
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("modelproxy: parse mock model script: %w", err)
	}
	if len(t.Rules) == 0 && len(t.Turns) > 0 {
		t.Rules = []ScriptRule{{Turns: t.Turns}}
	}
	t.Turns = nil
	if len(t.Rules) == 0 {
		return nil, fmt.Errorf("modelproxy: mock model script has no rules and no turns")
	}
	for i, r := range t.Rules {
		if len(r.Turns) == 0 {
			return nil, fmt.Errorf("modelproxy: mock model script rule %d (match %q) has no turns", i, r.Match)
		}
		for j, turn := range r.Turns {
			if len(turn.Blocks) == 0 {
				return nil, fmt.Errorf("modelproxy: mock model script rule %d turn %d has no blocks", i, j)
			}
			for k, b := range turn.Blocks {
				if !blockTypes[b.Type] {
					return nil, fmt.Errorf("modelproxy: mock model script rule %d turn %d block %d: unknown type %q (want text, thinking or tool_use)", i, j, k, b.Type)
				}
				if b.Type == "tool_use" && strings.TrimSpace(b.Name) == "" {
					return nil, fmt.Errorf("modelproxy: mock model script rule %d turn %d block %d: tool_use needs a name", i, j, k)
				}
			}
		}
	}
	return &t, nil
}

// Select returns the turns of the first rule matching this request body, or nil
// when no rule applies.
func (t *ScriptTable) Select(body []byte) []Turn {
	if t == nil {
		return nil
	}
	for _, r := range t.Rules {
		if r.Match != "" && !bytes.Contains(body, []byte(r.Match)) {
			continue
		}
		if r.Absent != "" && bytes.Contains(body, []byte(r.Absent)) {
			continue
		}
		return r.Turns
	}
	return nil
}

// CountAssistantMessages returns the number of role:"assistant" messages in an
// Anthropic Messages request body — the turn index (see the file comment).
// An unparseable body counts as zero, which serves the first turn.
func CountAssistantMessages(body []byte) int {
	var req struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0
	}
	n := 0
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// TurnSSE renders one scripted turn as a complete Anthropic streaming response.
// turnIdx only seeds the message and tool-use ids, so identical input always
// yields identical output.
func TurnSSE(turn Turn, turnIdx int) string {
	stopReason := "end_turn"
	for _, b := range turn.Blocks {
		if b.Type == "tool_use" {
			stopReason = "tool_use"
			break
		}
	}

	var sb strings.Builder
	sb.WriteString("event: message_start\n")
	fmt.Fprintf(&sb,
		`data: {"type":"message_start","message":{"id":%s,"type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
		mustJSON(fmt.Sprintf("msg_mock_%d", turnIdx+1)))
	sb.WriteString("\n\n")

	sb.WriteString("event: ping\n")
	sb.WriteString(`data: {"type":"ping"}`)
	sb.WriteString("\n\n")

	for i, block := range turn.Blocks {
		switch block.Type {
		case "text":
			sb.WriteString("event: content_block_start\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, i)
			sb.WriteString("\n\n")
			sb.WriteString("event: content_block_delta\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, i, mustJSON(block.Text))
			sb.WriteString("\n\n")
			sb.WriteString("event: content_block_stop\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_stop","index":%d}`, i)
			sb.WriteString("\n\n")

		case "thinking":
			sb.WriteString("event: content_block_start\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, i)
			sb.WriteString("\n\n")
			sb.WriteString("event: content_block_delta\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":%s}}`, i, mustJSON(block.Text))
			sb.WriteString("\n\n")
			sb.WriteString("event: content_block_stop\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_stop","index":%d}`, i)
			sb.WriteString("\n\n")

		case "tool_use":
			input := block.Input
			if input == nil {
				input = map[string]any{}
			}
			sb.WriteString("event: content_block_start\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%s,"name":%s,"input":{}}}`,
				i, mustJSON(fmt.Sprintf("toolu_mock_t%d_b%d", turnIdx, i)), mustJSON(block.Name))
			sb.WriteString("\n\n")
			sb.WriteString("event: content_block_delta\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`,
				i, mustJSON(mustJSON(input)))
			sb.WriteString("\n\n")
			sb.WriteString("event: content_block_stop\n")
			fmt.Fprintf(&sb, `data: {"type":"content_block_stop","index":%d}`, i)
			sb.WriteString("\n\n")
		}
	}

	sb.WriteString("event: message_delta\n")
	fmt.Fprintf(&sb, `data: {"type":"message_delta","delta":{"stop_reason":%s,"stop_sequence":null},"usage":{"output_tokens":10}}`, mustJSON(stopReason))
	sb.WriteString("\n\n")
	sb.WriteString("event: message_stop\n")
	sb.WriteString(`data: {"type":"message_stop"}`)
	sb.WriteString("\n\n")
	return sb.String()
}

// mustJSON returns the JSON encoding of v, panicking on error (the inputs here
// are strings and already-decoded JSON, so an error is a programming fault).
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("modelproxy: mustJSON: %v", err))
	}
	return string(b)
}

// SplitSSE splits a monolithic SSE stream into per-event chunks (each ending
// "\n\n"). Exported so the test-side mock (go/mockmodel) shares one implementation.
func SplitSSE(stream string) []string { return splitSSE(stream) }
