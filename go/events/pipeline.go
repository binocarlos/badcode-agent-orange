package events

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// pipeline is the default EventPipeline: it tees the in-image SSE stream to the
// client, parses frames, compacts them, and persists via the Sink under the flush
// guard at end-of-query. Periodic mid-query flushing (every ~2s, as the
// orchestrator does) is supported via flushCadence — see NewPipelineWithCadence.
//
// Ported in spirit from orchestrator/src/message-capture.ts + the proxySSEStream
// tee in goapi/pkg/server/agent.go.
type pipeline struct {
	sink         Sink
	hooks        map[Type][]MarkerHook
	flushCadence time.Duration // 0 = end-only persist (default)
}

// NewPipeline builds the default pipeline (end-of-query persist only).
// hooks fire as matching events stream.
func NewPipeline(sink Sink, hooks ...struct {
	Type Type
	Hook MarkerHook
}) EventPipeline {
	p := &pipeline{sink: sink, hooks: map[Type][]MarkerHook{}}
	for _, h := range hooks {
		p.hooks[h.Type] = append(p.hooks[h.Type], h.Hook)
	}
	return p
}

// NewPipelineWithCadence builds a pipeline that flushes collected events to the
// Sink every d (crash-safety). An end-of-query persist always runs as well.
// hooks fire as matching events stream.
func NewPipelineWithCadence(sink Sink, d time.Duration, hooks ...struct {
	Type Type
	Hook MarkerHook
}) EventPipeline {
	p := &pipeline{sink: sink, hooks: map[Type][]MarkerHook{}, flushCadence: d}
	for _, h := range hooks {
		p.hooks[h.Type] = append(p.hooks[h.Type], h.Hook)
	}
	return p
}

func (p *pipeline) Run(ctx context.Context, q QueryContext, src io.Reader, client io.Writer) (Result, error) {
	var mu sync.Mutex
	var collected []Envelope
	res := Result{Status: "complete"}

	// Seed with any host-supplied leading events (e.g. the user_message for this
	// turn). They are persisted/compacted in order ahead of the streamed events
	// but are deliberately NOT teed to the client writer below — the live client
	// already shows the prompt optimistically.
	if len(q.LeadingEvents) > 0 {
		collected = append(collected, q.LeadingEvents...)
	}

	// Start the periodic flush ticker if a cadence was set.
	// done is closed by defer before Run returns, which unblocks the goroutine's
	// select and causes it to exit — preventing a goroutine leak on every Run call
	// (ticker.Stop drains pending ticks but does NOT close ticker.C).
	if p.flushCadence > 0 && p.sink != nil {
		done := make(chan struct{})
		defer close(done)
		ticker := time.NewTicker(p.flushCadence)
		defer ticker.Stop()
		go func() {
			for {
				select {
				case <-ticker.C:
					mu.Lock()
					snap := make([]Envelope, len(collected))
					copy(snap, collected)
					mu.Unlock()
					if len(snap) == 0 {
						continue
					}
					compacted := Compact(snap)
					searchText := ExtractSearchText(compacted)
					p.sink.BeginFlush(q.SessionID)
					_ = p.sink.PersistQueryEvents(ctx, q.SessionID, q.QueryID, compacted, searchText)
					p.sink.EndFlush(q.SessionID)
				case <-done:
					return
				}
			}
		}()
	}

	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var curEvent string
	for sc.Scan() {
		select {
		case <-ctx.Done():
			res.Status = "cancelled"
			mu.Lock()
			snap := make([]Envelope, len(collected))
			copy(snap, collected)
			mu.Unlock()
			return p.persist(ctx, q, snap, res)
		default:
		}
		line := sc.Text()

		// Tee the raw frame to the client verbatim.
		if client != nil {
			_, _ = io.WriteString(client, line+"\n")
		}

		switch {
		case strings.HasPrefix(line, "event:"):
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			ev, ok := parseDataLine(strings.TrimSpace(strings.TrimPrefix(line, "data:")), curEvent)
			if !ok {
				continue
			}
			mu.Lock()
			collected = append(collected, ev)
			mu.Unlock()
			res.EventCount++
			for _, h := range p.hooks[ev.Type] {
				h(ctx, q, ev)
			}
			if ev.Type == QueryComplete {
				res.Status, _ = ev.Data["status"].(string)
				if res.Status == "" {
					res.Status = "complete"
				}
				// The query is done — don't wait for the SSE connection to close
				// naturally (the sandbox keeps it open for heartbeats). Break out
				// so callers aren't held for the full HTTP client timeout.
				mu.Lock()
				snap := make([]Envelope, len(collected))
				copy(snap, collected)
				mu.Unlock()
				return p.persist(ctx, q, snap, res)
			}
			if ev.Type == Error {
				res.Status = "error"
			}
		case line == "":
			curEvent = ""
		}
	}
	mu.Lock()
	snap := make([]Envelope, len(collected))
	copy(snap, collected)
	mu.Unlock()
	if err := sc.Err(); err != nil {
		return p.persist(ctx, q, snap, res)
	}
	return p.persist(ctx, q, snap, res)
}

// parseDataLine handles both SSE shapes the in-image agent emits:
//   - wrapped:  data: {"type":"content_delta","data":{...}}
//   - direct:   event: content_delta\n data: {...}
func parseDataLine(data, curEvent string) (Envelope, bool) {
	if data == "" {
		return Envelope{}, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return Envelope{}, false
	}
	if t, ok := raw["type"].(string); ok {
		d, _ := raw["data"].(map[string]any)
		if d == nil {
			d = map[string]any{}
		}
		ts, _ := raw["timestamp"].(string)
		return Envelope{Type: Type(t), Data: d, Timestamp: ts}, true
	}
	if curEvent == "" {
		return Envelope{}, false
	}
	return Envelope{Type: Type(curEvent), Data: raw}, true
}

func (p *pipeline) persist(ctx context.Context, q QueryContext, collected []Envelope, res Result) (Result, error) {
	if p.sink == nil || len(collected) == 0 {
		return res, nil
	}
	compacted := Compact(collected)
	searchText := ExtractSearchText(compacted)
	p.sink.BeginFlush(q.SessionID)
	defer p.sink.EndFlush(q.SessionID)
	if err := p.sink.PersistQueryEvents(ctx, q.SessionID, q.QueryID, compacted, searchText); err != nil {
		res.Status = "error"
		return res, err
	}
	res.PersistedCount = len(compacted)
	return res, nil
}
