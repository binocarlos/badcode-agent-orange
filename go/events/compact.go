package events

import "strings"

// transientTypes are dropped during compaction — they're live-only diagnostics
// that should never reach durable storage. Mirrors the orchestrator's
// TRANSIENT_TYPES set (compact-events.ts).
var transientTypes = map[Type]bool{
	Heartbeat:      true,
	ToolProgress:   true,
	ToolInputDelta: true,
	ActivityUpdate: true,
	SystemStatus:   true,
	HookEvent:      true,
	Connected:      true,
}

const maxSearchTextLen = 10_000

// Compact compresses an event stream for storage: drop transient types, drop
// empty user_message events (reconnect artifacts), and merge consecutive
// content_delta / thinking_delta into one event each. Pure — a restored session
// replays the compacted output through the same reducer as the live stream.
//
// Ported from compactEvents() in orchestrator/src/compact-events.ts.
func Compact(in []Envelope) []Envelope {
	out := make([]Envelope, 0, len(in))
	for _, ev := range in {
		if transientTypes[ev.Type] {
			continue
		}
		if ev.Type == UserMessage {
			if content, _ := ev.Data["content"].(string); strings.TrimSpace(content) == "" {
				continue
			}
		}
		if (ev.Type == ContentDelta || ev.Type == ThinkingDelta) && len(out) > 0 && out[len(out)-1].Type == ev.Type {
			prev := &out[len(out)-1]
			prev.Data = mergeDelta(prev.Data, ev.Data)
			continue
		}
		out = append(out, ev)
	}
	return out
}

// mergeDelta concatenates the "delta" string of two consecutive delta events.
func mergeDelta(prev, next map[string]any) map[string]any {
	merged := map[string]any{}
	for k, v := range prev {
		merged[k] = v
	}
	pd, _ := prev["delta"].(string)
	nd, _ := next["delta"].(string)
	merged["delta"] = pd + nd
	return merged
}

// ExtractSearchText concatenates user and assistant text for full-text indexing,
// capped at maxSearchTextLen. Ported from extractSearchText().
func ExtractSearchText(in []Envelope) string {
	var b strings.Builder
	for _, ev := range in {
		switch ev.Type {
		case UserMessage:
			if c, ok := ev.Data["content"].(string); ok {
				b.WriteString(c)
				b.WriteByte(' ')
			}
		case ContentDelta:
			if d, ok := ev.Data["delta"].(string); ok {
				b.WriteString(d)
			}
		}
		if b.Len() >= maxSearchTextLen {
			break
		}
	}
	s := b.String()
	if len(s) > maxSearchTextLen {
		s = s[:maxSearchTextLen]
	}
	return s
}
