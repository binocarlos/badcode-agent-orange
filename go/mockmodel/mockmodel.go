// Package mockmodel is a deterministic, scriptable Anthropic Messages API mock.
// It replays a Script as SSE so tests can drive the agent without a real model.
// Promoted from agent-library/go/systemtest/mockproxy_test.go so both the
// agentkit systemtests and the Platinum integration harness share one mock.
package mockmodel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Block is a single content block in a scripted turn. Type ∈ {"text","tool_use","thinking"}.
type Block struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

// Turn is one assistant response.
type Turn struct {
	Blocks []Block `json:"blocks"`
}

// Script is an ordered list of turns, selected by assistant-message count.
type Script struct {
	Turns []Turn `json:"turns"`
}

// Server is a scripted Anthropic mock. Embed httptest.Server for .URL / .Close().
type Server struct {
	*httptest.Server
	t       *testing.T
	script  *Script
	toolSeq int

	// sessionTurns tracks how many complete SSE responses have been served per
	// session ID (identified by the x-session-id header). This is used as the
	// turn index so that:
	//   • Multiple model calls within a single user turn (e.g. the claude-agent-sdk
	//     making 2 requests internally for one send) all return the same script turn.
	//   • A new user turn (new SendAndCollect call) returns the next script turn.
	//
	// Advance mechanism: on the FIRST request for a session we increment
	// sessionCalls. After a turn boundary (i.e. calls to a new human-level turn)
	// the sessionPromptKey changes (the history grows), so we detect the boundary
	// by comparing the request's "previous conversation" signal. For simplicity
	// we track the total completed responses per session and use a rolling counter
	// with a configurable "calls per user turn" (default 2 for claude-agent-sdk,
	// 1 for the core agentkit harness). CallsPerTurn controls this.
	//
	// Default 0 means: fall back to counting assistant messages (the original
	// mechanism, suitable for the core agentkit harness).
	CallsPerTurn int // 0 = legacy assistant-message counting

	mu           sync.Mutex
	sessionCalls map[string]int // sessionID → total /v1/messages calls received
}

// sseResponse is a minimal valid Anthropic Messages API streaming response.
// Kept for backward compat / nil-script fallback.
const sseResponse = "" +
	"event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_mock001","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: ping\n" +
	`data: {"type":"ping"}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello from the mock proxy"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":6}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// New returns a static (nil-script) mock that always replies with one text block.
func New(t *testing.T) *Server { return NewScripted(t, nil) }

// NewScripted returns a mock replaying script (nil → static fallback).
// For the Platinum sandbox (claude-agent-sdk harness) set srv.CallsPerTurn = 2
// after construction; see the package-level docstring.
func NewScripted(t *testing.T, script *Script) *Server {
	t.Helper()
	sp := &Server{t: t, script: script, sessionCalls: make(map[string]int)}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Accept both /v1/messages (direct SDK use in tests) and
		// /anthropic/v1/messages (via goapi's agent-proxy which prepends /anthropic).
		case r.Method == http.MethodPost &&
			(r.URL.Path == "/v1/messages" || r.URL.Path == "/anthropic/v1/messages"):
			sp.handleMessages(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Logf("[mockmodel] unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	sp.Server = srv
	return sp
}

// handleMessages serves a single /v1/messages request.
// Turn selection strategy:
//   - CallsPerTurn > 0: use a per-session call counter. turnIdx = callCount / CallsPerTurn.
//     This is correct for the Platinum claude-agent-sdk harness, which makes
//     CallsPerTurn model calls per user-level "send" (currently 2).
//   - CallsPerTurn == 0 (default): count "assistant" messages in the request body.
//     This is correct for the core agentkit harness and direct SDK tests.
func (sp *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("x-session-id")
	sp.t.Logf("[mockmodel] POST /v1/messages x-session-id=%q", sessionID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("x-session-id", sessionID)
	w.WriteHeader(http.StatusOK)

	flusher, hasFlusher := w.(http.Flusher)

	// If no script, fall back to static response.
	if sp.script == nil {
		for _, chunk := range splitSSE(sseResponse) {
			_, _ = fmt.Fprint(w, chunk)
			if hasFlusher {
				flusher.Flush()
			}
		}
		return
	}

	// Determine turn index.
	var turnIdx int
	if sp.CallsPerTurn > 0 && sessionID != "" {
		// Counter-based: count total model calls for this session and divide by
		// CallsPerTurn to get the user-level turn index.
		sp.mu.Lock()
		callCount := sp.sessionCalls[sessionID]
		sp.sessionCalls[sessionID] = callCount + 1
		sp.mu.Unlock()
		turnIdx = callCount / sp.CallsPerTurn
		sp.t.Logf("[mockmodel] callCount=%d CallsPerTurn=%d → turn index = %d (script has %d turns)",
			callCount, sp.CallsPerTurn, turnIdx, len(sp.script.Turns))
	} else {
		// Legacy: count assistant messages in the request body.
		turnIdx = sp.countAssistantMessages(r.Body)
		sp.t.Logf("[mockmodel] turn index = %d (script has %d turns)", turnIdx, len(sp.script.Turns))
	}

	if turnIdx >= len(sp.script.Turns) {
		// Past end of script — return a simple end_turn response.
		for _, chunk := range splitSSE(sseResponse) {
			_, _ = fmt.Fprint(w, chunk)
			if hasFlusher {
				flusher.Flush()
			}
		}
		return
	}

	turn := sp.script.Turns[turnIdx]
	sse := sp.buildTurnSSE(turn)
	for _, chunk := range splitSSE(sse) {
		_, _ = fmt.Fprint(w, chunk)
		if hasFlusher {
			flusher.Flush()
		}
	}
}

// countAssistantMessages reads the request body and counts messages with role=="assistant".
func (sp *Server) countAssistantMessages(body io.ReadCloser) int {
	if body == nil {
		return 0
	}
	defer body.Close()

	var req struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		sp.t.Logf("[mockmodel] could not parse request body: %v", err)
		return 0
	}

	count := 0
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			count++
		}
	}
	return count
}

// buildTurnSSE generates a complete SSE stream for one script turn.
func (sp *Server) buildTurnSSE(turn Turn) string {
	var sb strings.Builder

	// Determine stop reason.
	stopReason := "end_turn"
	for _, b := range turn.Blocks {
		if b.Type == "tool_use" {
			stopReason = "tool_use"
			break
		}
	}

	msgID := fmt.Sprintf("msg_mock_%d", sp.toolSeq+1)

	sb.WriteString("event: message_start\n")
	sb.WriteString(fmt.Sprintf(
		`data: {"type":"message_start","message":{"id":%q,"type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
		msgID,
	))
	sb.WriteString("\n\n")

	sb.WriteString("event: ping\n")
	sb.WriteString(`data: {"type":"ping"}`)
	sb.WriteString("\n\n")

	for i, block := range turn.Blocks {
		switch block.Type {
		case "text":
			sb.WriteString("event: content_block_start\n")
			sb.WriteString(fmt.Sprintf(
				`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`,
				i,
			))
			sb.WriteString("\n\n")

			sb.WriteString("event: content_block_delta\n")
			sb.WriteString(fmt.Sprintf(
				`data: {"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`,
				i, mustJSON(block.Text),
			))
			sb.WriteString("\n\n")

			sb.WriteString("event: content_block_stop\n")
			sb.WriteString(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, i))
			sb.WriteString("\n\n")

		case "thinking":
			sb.WriteString("event: content_block_start\n")
			sb.WriteString(fmt.Sprintf(
				`data: {"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`,
				i,
			))
			sb.WriteString("\n\n")

			sb.WriteString("event: content_block_delta\n")
			sb.WriteString(fmt.Sprintf(
				`data: {"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":%s}}`,
				i, mustJSON(block.Text),
			))
			sb.WriteString("\n\n")

			sb.WriteString("event: content_block_stop\n")
			sb.WriteString(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, i))
			sb.WriteString("\n\n")

		case "tool_use":
			sp.toolSeq++
			toolID := fmt.Sprintf("toolu_mock_%d", sp.toolSeq)
			inputJSON := mustJSON(block.Input)

			sb.WriteString("event: content_block_start\n")
			sb.WriteString(fmt.Sprintf(
				`data: {"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%s,"name":%s,"input":{}}}`,
				i, mustJSON(toolID), mustJSON(block.Name),
			))
			sb.WriteString("\n\n")

			sb.WriteString("event: content_block_delta\n")
			sb.WriteString(fmt.Sprintf(
				`data: {"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`,
				i, mustJSON(inputJSON),
			))
			sb.WriteString("\n\n")

			sb.WriteString("event: content_block_stop\n")
			sb.WriteString(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, i))
			sb.WriteString("\n\n")
		}
	}

	sb.WriteString("event: message_delta\n")
	sb.WriteString(fmt.Sprintf(
		`data: {"type":"message_delta","delta":{"stop_reason":%s,"stop_sequence":null},"usage":{"output_tokens":10}}`,
		mustJSON(stopReason),
	))
	sb.WriteString("\n\n")

	sb.WriteString("event: message_stop\n")
	sb.WriteString(`data: {"type":"message_stop"}`)
	sb.WriteString("\n\n")

	return sb.String()
}

// mustJSON returns the JSON encoding of v as a string, panicking on error.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return string(b)
}

// splitSSE splits an SSE stream into individual events (split on \n\n).
func splitSSE(stream string) []string {
	var chunks []string
	for {
		idx := strings.Index(stream, "\n\n")
		if idx < 0 {
			if len(stream) > 0 {
				chunks = append(chunks, stream)
			}
			break
		}
		chunks = append(chunks, stream[:idx+2])
		stream = stream[idx+2:]
	}
	return chunks
}
