//go:build integration

package systemtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseResponse is a minimal valid Anthropic Messages API streaming response.
// Kept for backward compat with tests that use newMockProxy directly.
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

// mockBlock represents a single content block in a scripted turn.
type mockBlock struct {
	Type  string                 // "text", "tool_use", "thinking"
	Text  string                 // used for text / thinking blocks
	Name  string                 // used for tool_use blocks
	Input map[string]interface{} // used for tool_use blocks
}

// mockTurn represents one LLM response turn.
type mockTurn struct {
	Blocks []mockBlock
}

// mockScript is an ordered list of turns to replay.
type mockScript struct {
	Turns []mockTurn
}

// scriptedMockProxy is a stateful HTTP test server that replays a script.
type scriptedMockProxy struct {
	*httptest.Server
	t          *testing.T
	script     *mockScript
	toolSeq    int // auto-incrementing tool ID counter
}

// newMockProxy creates a backward-compatible static mock proxy (nil script).
func newMockProxy(t *testing.T) *httptest.Server {
	t.Helper()
	return newScriptedMockProxy(t, nil)
}

// newScriptedMockProxy creates a mock proxy that replays the given script.
// If script is nil it falls back to the static sseResponse.
func newScriptedMockProxy(t *testing.T, script *mockScript) *httptest.Server {
	t.Helper()

	sp := &scriptedMockProxy{
		t:      t,
		script: script,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
			sp.handleMessages(w, r)

		case r.Method == http.MethodGet && r.URL.Path == "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))

		default:
			t.Logf("[mockproxy] unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewUnstartedServer(handler)
	srv.Start()
	sp.Server = srv
	return srv
}

// handleMessages serves a single /v1/messages request.
// It picks the script turn by counting existing "assistant" messages in the
// request body (i.e. how many assistant turns have already been exchanged).
func (sp *scriptedMockProxy) handleMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("x-session-id")
	sp.t.Logf("[mockproxy] POST /v1/messages x-session-id=%q", sessionID)

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

	// Determine turn index by counting assistant messages already present.
	turnIdx := sp.countAssistantMessages(r.Body)
	sp.t.Logf("[mockproxy] turn index = %d (script has %d turns)", turnIdx, len(sp.script.Turns))

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
func (sp *scriptedMockProxy) countAssistantMessages(body io.ReadCloser) int {
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
		sp.t.Logf("[mockproxy] could not parse request body: %v", err)
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
func (sp *scriptedMockProxy) buildTurnSSE(turn mockTurn) string {
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
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return string(b)
}

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
