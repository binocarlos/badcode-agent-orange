package modelproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The chunk pacing exists for browser tests; in-process tests should not pay it.
func init() { mockChunkPause = 0 }

// post drives a handler with an Anthropic-shaped request body and returns the SSE.
func post(h http.Handler, body string) string {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
	return rec.Body.String()
}

// ── The invariant that protects every existing offline test ─────────────────

// A stack with no script configured must behave EXACTLY as it did before the
// script table existed: the canned single-text-turn stream, and never a
// tool_use. This is the regression guard for "inert unless asked for".
func TestScriptedMockHandler_NoScriptIsUnchanged(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hello"}]}`

	canned := post(MockHandler(), body)
	scripted := post(ScriptedMockHandler(nil), body)
	empty := post(ScriptedMockHandler(&ScriptTable{}), body)

	if canned != mockSSEStream {
		t.Fatalf("MockHandler no longer serves the canned stream:\n%s", canned)
	}
	if scripted != canned {
		t.Fatalf("nil script table changed the response:\n got %s\nwant %s", scripted, canned)
	}
	if empty != canned {
		t.Fatalf("empty script table changed the response:\n got %s\nwant %s", empty, canned)
	}
	if strings.Contains(canned, "tool_use") {
		t.Fatal("the canned stream must not contain a tool_use — that is the gap this file closes")
	}
	// Repeat calls stay identical: no hidden state accumulated anywhere.
	if again := post(ScriptedMockHandler(nil), body); again != canned {
		t.Fatal("the unscripted handler is not stateless across calls")
	}
}

// The unscripted paths that are not model calls must also be untouched.
func TestScriptedMockHandler_HealthAndTelemetryUnchanged(t *testing.T) {
	for _, h := range []http.Handler{MockHandler(), ScriptedMockHandler(&ScriptTable{Rules: []ScriptRule{{Turns: []Turn{{Blocks: []Block{{Type: "text", Text: "x"}}}}}}})} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agent-proxy/health", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "mock-anthropic-proxy") {
			t.Fatalf("health: code=%d body=%s", rec.Code, rec.Body)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/event_logging/x", strings.NewReader(`{}`)))
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "event:") {
			t.Fatalf("telemetry: code=%d body=%s", rec.Code, rec.Body)
		}
	}
}

// ── The capability that unblocks G1 ─────────────────────────────────────────

// The headline: a script makes the mock model call a NAMED tool with GIVEN
// arguments, and then finish with text once the tool result comes back.
func TestScriptedMockHandler_EmitsToolUseThenText(t *testing.T) {
	table, err := ParseScriptTable(`{"rules":[{"match":"email-reviewer","turns":[
	  {"blocks":[{"type":"tool_use","name":"mcp__core__worker_prompt_write",
	     "input":{"worker":"email-answerer","system_prompt":"Be warm.","rationale":"curt"}}]},
	  {"blocks":[{"type":"text","text":"Rewrote the answerer's prompt."}]}]}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	h := ScriptedMockHandler(table)

	// Call 1: the worker's composed system prompt names the worker; the
	// conversation holds no assistant reply yet, so this is turn 0.
	first := post(h, `{"system":"You are the worker \"email-reviewer\".","messages":[{"role":"user","content":"review this"}]}`)
	if !strings.Contains(first, `"type":"tool_use"`) {
		t.Fatalf("first turn is not a tool_use:\n%s", first)
	}
	if !strings.Contains(first, `mcp__core__worker_prompt_write`) {
		t.Fatalf("wrong tool name:\n%s", first)
	}
	if !strings.Contains(first, `Be warm.`) || !strings.Contains(first, `email-answerer`) {
		t.Fatalf("tool arguments did not reach the stream:\n%s", first)
	}
	if !strings.Contains(first, `"stop_reason":"tool_use"`) {
		t.Fatalf("a tool-calling turn must stop with tool_use:\n%s", first)
	}

	// Call 2: the harness replays the conversation including the tool result.
	second := post(h, `{"system":"You are the worker \"email-reviewer\".","messages":[
	  {"role":"user","content":"review this"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"toolu_mock_t0_b0","name":"mcp__core__worker_prompt_write","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_mock_t0_b0","content":"ok"}]}]}`)
	if strings.Contains(second, `"type":"tool_use"`) {
		t.Fatalf("second turn must not call the tool again:\n%s", second)
	}
	if !strings.Contains(second, "Rewrote the answerer's prompt.") {
		t.Fatalf("second turn is not the scripted text:\n%s", second)
	}
	if !strings.Contains(second, `"stop_reason":"end_turn"`) {
		t.Fatalf("a text turn must stop with end_turn:\n%s", second)
	}
}

// Identical requests produce identical bytes — no counters, no clock.
func TestScriptedMockHandler_IsDeterministic(t *testing.T) {
	table, err := ParseScriptTable(`{"turns":[{"blocks":[{"type":"tool_use","name":"t","input":{"a":1}}]}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	h := ScriptedMockHandler(table)
	body := `{"messages":[{"role":"user","content":"go"}]}`
	if a, b := post(h, body), post(h, body); a != b {
		t.Fatalf("scripted output is not deterministic:\n%s\n---\n%s", a, b)
	}
}

// A request no rule matches, and a turn index past the end of a matched rule,
// both fall back to the canned stream — never to a stray tool call.
func TestScriptedMockHandler_FallsBackToCanned(t *testing.T) {
	table, err := ParseScriptTable(`{"rules":[{"match":"reviewer","turns":[{"blocks":[
	  {"type":"tool_use","name":"worker_prompt_write","input":{}}]}]}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	h := ScriptedMockHandler(table)

	unmatched := post(h, `{"system":"You are the worker \"answerer\".","messages":[{"role":"user","content":"hi"}]}`)
	if unmatched != mockSSEStream {
		t.Fatalf("an unmatched request must get the canned stream:\n%s", unmatched)
	}
	exhausted := post(h, `{"system":"reviewer","messages":[
	  {"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`)
	if exhausted != mockSSEStream {
		t.Fatalf("running out of script must get the canned stream, not a repeated tool call:\n%s", exhausted)
	}
	if strings.Contains(exhausted, "tool_use") {
		t.Fatal("running out of script emitted a tool call")
	}
}

// ── Parsing and validation ──────────────────────────────────────────────────

func TestParseScriptTable(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantNil bool
		wantErr string
		rules   int
	}{
		{name: "empty is no script", in: "   ", wantNil: true},
		{name: "bare turns become one catch-all rule",
			in:    `{"turns":[{"blocks":[{"type":"text","text":"hi"}]}]}`,
			rules: 1},
		{name: "rules parse", in: `{"rules":[
			{"match":"a","turns":[{"blocks":[{"type":"text","text":"x"}]}]},
			{"match":"b","absent":"z","turns":[{"blocks":[{"type":"thinking","text":"…"}]}]}]}`,
			rules: 2},
		{name: "malformed json", in: `{"turns":`, wantErr: "parse mock model script"},
		{name: "typo rejected", in: `{"turnz":[]}`, wantErr: "unknown field"},
		{name: "no rules", in: `{"rules":[]}`, wantErr: "no rules and no turns"},
		{name: "rule with no turns", in: `{"rules":[{"match":"a","turns":[]}]}`, wantErr: "has no turns"},
		{name: "turn with no blocks", in: `{"turns":[{"blocks":[]}]}`, wantErr: "has no blocks"},
		{name: "unknown block type", in: `{"turns":[{"blocks":[{"type":"image"}]}]}`, wantErr: "unknown type"},
		{name: "tool_use needs a name", in: `{"turns":[{"blocks":[{"type":"tool_use","input":{}}]}]}`, wantErr: "needs a name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseScriptTable(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil table, got %+v", got)
				}
				return
			}
			if got == nil || len(got.Rules) != tc.rules {
				t.Fatalf("want %d rules, got %+v", tc.rules, got)
			}
			if got.Turns != nil {
				t.Fatal("the bare-turns shorthand must be normalised away")
			}
		})
	}
}

func TestScriptTableSelect(t *testing.T) {
	table := &ScriptTable{Rules: []ScriptRule{
		{Match: "reviewer", Absent: "tool_result", Turns: []Turn{{Blocks: []Block{{Type: "text", Text: "first"}}}}},
		{Match: "reviewer", Turns: []Turn{{Blocks: []Block{{Type: "text", Text: "second"}}}}},
		{Turns: []Turn{{Blocks: []Block{{Type: "text", Text: "catchall"}}}}},
	}}
	tests := []struct{ name, body, want string }{
		{"first rule wins", `reviewer here`, "first"},
		{"absent excludes the first", `reviewer with tool_result`, "second"},
		{"empty match is a catch-all", `someone else`, "catchall"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			turns := table.Select([]byte(tc.body))
			if len(turns) == 0 || turns[0].Blocks[0].Text != tc.want {
				t.Fatalf("got %+v, want %q", turns, tc.want)
			}
		})
	}
	var nilTable *ScriptTable
	if nilTable.Select([]byte("x")) != nil {
		t.Fatal("a nil table must select nothing")
	}
	noMatch := &ScriptTable{Rules: []ScriptRule{{Match: "zzz", Turns: []Turn{{Blocks: []Block{{Type: "text"}}}}}}}
	if noMatch.Select([]byte("x")) != nil {
		t.Fatal("a table with no matching rule must select nothing")
	}
}

func TestCountAssistantMessages(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"first call", `{"messages":[{"role":"user","content":"a"}]}`, 0},
		{"after one reply", `{"messages":[{"role":"user"},{"role":"assistant"},{"role":"user"}]}`, 1},
		{"after two", `{"messages":[{"role":"user"},{"role":"assistant"},{"role":"user"},{"role":"assistant"},{"role":"user"}]}`, 2},
		{"unparseable counts as the first call", `not json`, 0},
		{"no messages", `{}`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountAssistantMessages([]byte(tc.body)); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// Every rendered turn must be a valid Anthropic SSE stream: each data: line
// parses as JSON and the stream is bracketed by message_start/message_stop.
func TestTurnSSE_IsWellFormed(t *testing.T) {
	turn := Turn{Blocks: []Block{
		{Type: "thinking", Text: "hm"},
		{Type: "text", Text: `quotes " and \ backslashes`},
		{Type: "tool_use", Name: "memory_write", Input: map[string]any{"labels": []any{"kind=lesson"}}},
	}}
	sse := TurnSSE(turn, 3)
	if !strings.HasPrefix(sse, "event: message_start\n") || !strings.HasSuffix(sse, "data: {\"type\":\"message_stop\"}\n\n") {
		t.Fatalf("stream is not bracketed:\n%s", sse)
	}
	n := 0
	for _, line := range strings.Split(sse, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		n++
		var v map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
			t.Fatalf("data line is not JSON (%v): %s", err, line)
		}
	}
	if n < 8 {
		t.Fatalf("expected at least 8 data lines, got %d", n)
	}
	// tool ids are derived from the indices, so they are stable and unique.
	if !strings.Contains(sse, "toolu_mock_t3_b2") {
		t.Fatalf("tool id is not index-derived:\n%s", sse)
	}
	// A tool_use block with no input still emits a valid empty object.
	if got := TurnSSE(Turn{Blocks: []Block{{Type: "tool_use", Name: "t"}}}, 0); !strings.Contains(got, `partial_json":"{}"`) {
		t.Fatalf("empty tool input did not render as {}:\n%s", got)
	}
}

// The pacing knob is a test affordance only; production must keep the delay
// that makes downstream consumers observably stream.
func TestMockChunkPauseDefault(t *testing.T) {
	if defaultMockChunkPause != 150*time.Millisecond {
		t.Fatalf("production chunk pacing changed: %v", defaultMockChunkPause)
	}
}
