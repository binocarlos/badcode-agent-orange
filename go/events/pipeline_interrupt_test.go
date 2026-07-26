package events

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Interruption (client disconnect / context cancellation)
//
// A real store honours the context it is handed: a cancelled ctx aborts the
// write. ctxSink models that, which is what makes these tests catch the data
// loss the always-succeeding mock sinks in pipeline_test.go could never see.
// ---------------------------------------------------------------------------

type ctxSink struct {
	mu     sync.Mutex
	events []Envelope
	errs   int
}

// seenWriter closes `seen` the first time `want` appears in what the pipeline
// streams to the client. That write happens only after the frame has been
// scanned and collected, so it is a *deterministic* "the pipeline has this
// event now" signal — unlike a sleep, which is a guess about scheduling and
// loses that bet under -race. Cancelling before this fires would test nothing:
// an event the pipeline never received is correctly absent from what it
// persists, so the assertion would be about the test's timing, not the code's
// behaviour.
type seenWriter struct {
	want string
	once sync.Once
	seen chan struct{}
}

func newSeenWriter(want string) *seenWriter {
	return &seenWriter{want: want, seen: make(chan struct{})}
}

func (s *seenWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), s.want) {
		s.once.Do(func() { close(s.seen) })
	}
	return len(p), nil
}

func (c *ctxSink) BeginFlush(_ string) {}
func (c *ctxSink) EndFlush(_ string)   {}

func (c *ctxSink) PersistQueryEvents(ctx context.Context, _, _ string, evs []Envelope, _ string) error {
	if err := ctx.Err(); err != nil {
		c.mu.Lock()
		c.errs++
		c.mu.Unlock()
		return err // exactly what database/sql + GORM do on a cancelled ctx
	}
	c.mu.Lock()
	c.events = append([]Envelope(nil), evs...)
	c.mu.Unlock()
	return nil
}

func (c *ctxSink) snapshot() []Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Envelope(nil), c.events...)
}

func (c *ctxSink) ctxFailures() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errs
}

// leadingUser is the user_message the Runner seeds every turn with.
func leadingUser(text string) []Envelope {
	return []Envelope{{Type: UserMessage, Data: map[string]any{"content": text}}}
}

func hasUserMessage(evs []Envelope, text string) bool {
	for _, e := range evs {
		if e.Type == UserMessage && e.Data["content"] == text {
			return true
		}
	}
	return false
}

func hasAssistantText(evs []Envelope, want string) bool {
	for _, e := range evs {
		for _, k := range []string{"text", "content"} {
			if s, ok := e.Data[k].(string); ok && strings.Contains(s, want) {
				return true
			}
		}
	}
	return false
}

// Interrupted before a single frame arrives: the human's own words must still
// reach the store. This is the worst version of the bug — a person's message
// vanishing because their browser reloaded.
func TestPipeline_CancelBeforeAnyOutput_PersistsUserMessage(t *testing.T) {
	sink := &ctxSink{}
	p := NewPipeline(sink)
	ctx, cancel := context.WithCancel(context.Background())

	pr, pw := io.Pipe()
	go func() {
		<-ctx.Done()
		// The transport aborts the body read when the request ctx is cancelled.
		_ = pw.CloseWithError(context.Canceled)
	}()
	cancel()

	res, _ := p.Run(ctx,
		QueryContext{SessionID: "s1", QueryID: "q1", LeadingEvents: leadingUser("tell me a joke")},
		pr, io.Discard)

	got := sink.snapshot()
	if !hasUserMessage(got, "tell me a joke") {
		t.Fatalf("interrupted turn lost the human's message: persisted %d events (%d cancelled-ctx write failures)",
			len(got), sink.ctxFailures())
	}
	if res.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", res.Status, "cancelled")
	}
}

// Interrupted mid-answer: whatever the assistant already produced is preserved
// rather than discarded, alongside the prompt.
func TestPipeline_CancelMidOutput_PersistsPartialTurn(t *testing.T) {
	sink := &ctxSink{}
	p := NewPipeline(sink)
	ctx, cancel := context.WithCancel(context.Background())

	pr, pw := io.Pipe()
	client := newSeenWriter("why did the")
	go func() {
		_, _ = io.WriteString(pw, "event: assistant\ndata: {\"text\":\"why did the\"}\n\n")
		<-ctx.Done()
		_ = pw.CloseWithError(context.Canceled)
	}()
	go func() {
		<-client.seen // deterministic: the frame is scanned, so cancelling now is mid-output
		cancel()
	}()

	res, _ := p.Run(ctx,
		QueryContext{SessionID: "s1", QueryID: "q1", LeadingEvents: leadingUser("tell me a joke")},
		pr, client)

	got := sink.snapshot()
	if !hasUserMessage(got, "tell me a joke") {
		t.Fatalf("interrupted turn lost the human's message: persisted %d events (%d cancelled-ctx write failures)",
			len(got), sink.ctxFailures())
	}
	if !hasAssistantText(got, "why did the") {
		t.Fatalf("interrupted turn discarded the partial answer: persisted %+v", got)
	}
	if res.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", res.Status, "cancelled")
	}
}

// Interrupted right at the end (the source ends without query_complete, e.g. the
// container died): what was said is still a durable record, and the turn is not
// reported as a clean completion.
func TestPipeline_StreamTruncatedWithoutQueryComplete_PersistsAndIsNotComplete(t *testing.T) {
	sink := &ctxSink{}
	p := NewPipeline(sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	client := newSeenWriter("chicken")
	go func() {
		_, _ = io.WriteString(pw, "event: assistant\ndata: {\"text\":\"why did the chicken\"}\n\n")
		<-client.seen // the pipeline has scanned it; only now is truncation meaningful
		cancel()
		_ = pw.CloseWithError(context.Canceled)
	}()

	res, _ := p.Run(ctx,
		QueryContext{SessionID: "s1", QueryID: "q1", LeadingEvents: leadingUser("tell me a joke")},
		pr, client)

	got := sink.snapshot()
	if !hasUserMessage(got, "tell me a joke") || !hasAssistantText(got, "chicken") {
		t.Fatalf("truncated turn not persisted: %d events (%d cancelled-ctx write failures): %+v",
			len(got), sink.ctxFailures(), got)
	}
	if res.Status == "complete" {
		t.Errorf("Status = %q: a turn cut short must not report a clean completion", res.Status)
	}
}

// Regression floor: a turn that runs all the way to query_complete is unchanged
// — same persisted content, same "complete" status.
func TestPipeline_UninterruptedTurnStillPersistsCompletely(t *testing.T) {
	sink := &ctxSink{}
	p := NewPipeline(sink)
	src := "" +
		"event: assistant\ndata: {\"text\":\"why did the chicken cross the road\"}\n\n" +
		"event: query_complete\ndata: {\"status\":\"complete\"}\n\n"

	res, err := p.Run(context.Background(),
		QueryContext{SessionID: "s1", QueryID: "q1", LeadingEvents: leadingUser("tell me a joke")},
		strings.NewReader(src), io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "complete" {
		t.Errorf("Status = %q, want %q", res.Status, "complete")
	}
	got := sink.snapshot()
	if !hasUserMessage(got, "tell me a joke") || !hasAssistantText(got, "chicken") {
		t.Fatalf("settled turn not fully persisted: %+v", got)
	}
}
