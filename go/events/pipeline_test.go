package events

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// recordingSink — counts PersistQueryEvents calls (used by flush-cadence test)
// ---------------------------------------------------------------------------

type recordingSink struct {
	mu       sync.Mutex
	persists int
}

func (r *recordingSink) BeginFlush(_ string) {}
func (r *recordingSink) EndFlush(_ string)   {}
func (r *recordingSink) PersistQueryEvents(_ context.Context, _, _ string, _ []Envelope, _ string) error {
	r.mu.Lock()
	r.persists++
	r.mu.Unlock()
	return nil
}
func (r *recordingSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persists
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// qctx is a fixed QueryContext used across pipeline tests.
var qctx = QueryContext{SessionID: "sess-1", QueryID: "q-1"}

// runPipeline feeds src through a pipeline with the given sink and optional
// hooks and returns the Result, any error, and what was written to the client.
func runPipeline(src string, sink Sink, hooks ...struct {
	Type Type
	Hook MarkerHook
}) (Result, error, string) {
	p := NewPipeline(sink, hooks...)
	var client bytes.Buffer
	res, err := p.Run(context.Background(), qctx, strings.NewReader(src), &client)
	return res, err, client.String()
}

// ---------------------------------------------------------------------------
// SSE shape: wrapped JSON  {"type":...,"data":{...}}
// ---------------------------------------------------------------------------

func TestPipeline_WrappedJSONShape(t *testing.T) {
	src := "" +
		`data: {"type":"content_delta","data":{"delta":"hi"}}` + "\n\n" +
		`data: {"type":"query_complete","data":{"status":"complete"}}` + "\n\n"

	sink := NewMockSink()
	res, err, _ := runPipeline(src, sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.EventCount != 2 {
		t.Errorf("EventCount = %d, want 2", res.EventCount)
	}
	persisted := sink.Persisted(qctx.QueryID)
	if len(persisted) != 2 {
		t.Fatalf("persisted %d events, want 2", len(persisted))
	}
	if persisted[0].Type != ContentDelta {
		t.Errorf("first event type = %q, want content_delta", persisted[0].Type)
	}
}

// ---------------------------------------------------------------------------
// SSE shape: event:/data: two-line form
// ---------------------------------------------------------------------------

func TestPipeline_EventDataLineShape(t *testing.T) {
	src := "" +
		"event: content_delta\n" +
		`data: {"delta":"world"}` + "\n\n" +
		"event: query_complete\n" +
		`data: {"status":"complete"}` + "\n\n"

	sink := NewMockSink()
	res, err, _ := runPipeline(src, sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.EventCount != 2 {
		t.Errorf("EventCount = %d, want 2", res.EventCount)
	}
	persisted := sink.Persisted(qctx.QueryID)
	if len(persisted) != 2 {
		t.Fatalf("persisted %d events, want 2", len(persisted))
	}
	if persisted[0].Type != ContentDelta {
		t.Errorf("first event = %q, want content_delta", persisted[0].Type)
	}
}

// ---------------------------------------------------------------------------
// Raw frames are teed to the client writer verbatim
// ---------------------------------------------------------------------------

func TestPipeline_RawFramesTeedToClient(t *testing.T) {
	rawLine := `data: {"type":"content_delta","data":{"delta":"tee me"}}`
	src := rawLine + "\n\n"

	sink := NewMockSink()
	_, _, clientOut := runPipeline(src, sink)

	// Every non-empty line of the input should appear in the client output.
	if !strings.Contains(clientOut, rawLine) {
		t.Errorf("client output does not contain raw frame line\ngot:  %q\nwant: %q", clientOut, rawLine)
	}
}

// ---------------------------------------------------------------------------
// MarkerHook fires for its event type
// ---------------------------------------------------------------------------

func TestPipeline_MarkerHookFires(t *testing.T) {
	src := "" +
		`data: {"type":"artifact_registered","data":{"id":"art-1"}}` + "\n\n" +
		`data: {"type":"content_delta","data":{"delta":"x"}}` + "\n\n" +
		`data: {"type":"query_complete","data":{"status":"complete"}}` + "\n\n"

	var fired []Envelope
	hook := struct {
		Type Type
		Hook MarkerHook
	}{
		Type: ArtifactRegistered,
		Hook: func(_ context.Context, _ QueryContext, ev Envelope) {
			fired = append(fired, ev)
		},
	}

	sink := NewMockSink()
	_, err, _ := runPipeline(src, sink, hook)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fired) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(fired))
	}
	if fired[0].Type != ArtifactRegistered {
		t.Errorf("hook event type = %q, want artifact_registered", fired[0].Type)
	}
	id, _ := fired[0].Data["id"].(string)
	if id != "art-1" {
		t.Errorf("hook event data[id] = %q, want %q", id, "art-1")
	}
}

func TestPipeline_MarkerHookDoesNotFireForOtherTypes(t *testing.T) {
	src := `data: {"type":"content_delta","data":{"delta":"nope"}}` + "\n\n" +
		`data: {"type":"query_complete","data":{"status":"complete"}}` + "\n\n"

	var fired int
	hook := struct {
		Type Type
		Hook MarkerHook
	}{
		Type: ArtifactRegistered,
		Hook: func(_ context.Context, _ QueryContext, ev Envelope) {
			fired++
		},
	}

	sink := NewMockSink()
	if _, err, _ := runPipeline(src, sink, hook); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired != 0 {
		t.Errorf("hook fired %d times, want 0", fired)
	}
}

// ---------------------------------------------------------------------------
// query_complete sets Result.Status
// ---------------------------------------------------------------------------

func TestPipeline_QueryCompleteStatusPropagated(t *testing.T) {
	for _, status := range []string{"complete", "error", "cancelled"} {
		src := "data: {\"type\":\"query_complete\",\"data\":{\"status\":\"" + status + "\"}}\n\n"
		sink := NewMockSink()
		res, err, _ := runPipeline(src, sink)
		if err != nil {
			t.Fatalf("[%s] unexpected error: %v", status, err)
		}
		if res.Status != status {
			t.Errorf("[%s] Result.Status = %q, want %q", status, res.Status, status)
		}
	}
}

func TestPipeline_DefaultStatusIsComplete(t *testing.T) {
	// No query_complete at all — should still default to "complete".
	src := `data: {"type":"content_delta","data":{"delta":"hi"}}` + "\n\n"
	sink := NewMockSink()
	res, _, _ := runPipeline(src, sink)
	if res.Status != "complete" {
		t.Errorf("default status = %q, want %q", res.Status, "complete")
	}
}

// ---------------------------------------------------------------------------
// Flush guard: BeginFlush/EndFlush ran around persist
// ---------------------------------------------------------------------------

func TestPipeline_FlushGuardRan(t *testing.T) {
	src := "" +
		`data: {"type":"content_delta","data":{"delta":"hi"}}` + "\n\n" +
		`data: {"type":"query_complete","data":{"status":"complete"}}` + "\n\n"

	sink := NewMockSink()
	if _, err, _ := runPipeline(src, sink); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sink.MaxConcurrentFlushes() < 1 {
		t.Errorf("MaxConcurrentFlushes = %d, want >= 1 (flush guard must have run)", sink.MaxConcurrentFlushes())
	}
	if sink.PendingFlushes() != 0 {
		t.Errorf("PendingFlushes = %d, want 0 at rest (EndFlush must balance BeginFlush)", sink.PendingFlushes())
	}
}

func TestPipeline_FlushGuardBalancedOnEmptyStream(t *testing.T) {
	// Empty stream → persist is skipped (len(collected)==0), so PendingFlushes
	// must remain 0 (BeginFlush was never called without matching EndFlush).
	sink := NewMockSink()
	if _, err, _ := runPipeline("", sink); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.PendingFlushes() != 0 {
		t.Errorf("PendingFlushes = %d after empty stream, want 0", sink.PendingFlushes())
	}
}

// ---------------------------------------------------------------------------
// Compaction through the pipeline
// ---------------------------------------------------------------------------

func TestPipeline_CompactionAppliedBeforePersist(t *testing.T) {
	// Two consecutive content_delta + a heartbeat + query_complete.
	// After compaction: 1 merged delta + 1 query_complete = 2 events.
	src := "" +
		"event: content_delta\n" + `data: {"delta":"A"}` + "\n\n" +
		"event: content_delta\n" + `data: {"delta":"B"}` + "\n\n" +
		"event: heartbeat\n" + `data: {}` + "\n\n" +
		`data: {"type":"query_complete","data":{"status":"complete"}}` + "\n\n"

	sink := NewMockSink()
	res, err, _ := runPipeline(src, sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Raw event count = 4 (all parsed frames including heartbeat).
	if res.EventCount != 4 {
		t.Errorf("EventCount = %d, want 4", res.EventCount)
	}

	persisted := sink.Persisted(qctx.QueryID)
	// After compaction: merged delta + query_complete = 2.
	if len(persisted) != 2 {
		t.Fatalf("persisted %d events after compaction, want 2: %#v", len(persisted), persisted)
	}
	if persisted[0].Type != ContentDelta {
		t.Errorf("first persisted type = %q, want content_delta", persisted[0].Type)
	}
	d, _ := persisted[0].Data["delta"].(string)
	if d != "AB" {
		t.Errorf("merged delta = %q, want %q", d, "AB")
	}
}

// ---------------------------------------------------------------------------
// Nil sink — no panic
// ---------------------------------------------------------------------------

func TestPipeline_NilSinkDoesNotPanic(t *testing.T) {
	src := `data: {"type":"content_delta","data":{"delta":"hi"}}` + "\n\n"
	p := NewPipeline(nil)
	var client bytes.Buffer
	res, err := p.Run(context.Background(), qctx, strings.NewReader(src), &client)
	if err != nil {
		t.Fatalf("unexpected error with nil sink: %v", err)
	}
	if res.EventCount != 1 {
		t.Errorf("EventCount = %d, want 1", res.EventCount)
	}
}

// ---------------------------------------------------------------------------
// Mid-query flush ticker (NewPipelineWithCadence)
// ---------------------------------------------------------------------------

func TestPipelineFlushesMidQuery(t *testing.T) {
	rec := &recordingSink{}
	p := NewPipelineWithCadence(rec, 10*time.Millisecond) // new constructor
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, "event: assistant\ndata: {\"text\":\"a\"}\n\n")
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(pw, "event: query_complete\ndata: {\"status\":\"complete\"}\n\n")
		pw.Close()
	}()
	_, _ = p.Run(context.Background(), QueryContext{SessionID: "s1", QueryID: "q1"}, pr, io.Discard)
	if rec.count() < 2 {
		t.Fatalf("expected a mid-query flush, got %d persists", rec.count())
	}
}
