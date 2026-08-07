package events

import "testing"

func delta(s string) Envelope {
	return Envelope{Type: ContentDelta, Data: map[string]any{"delta": s}}
}

func text(evs []Envelope) string {
	out := ""
	for _, e := range evs {
		if s, ok := e.Data["delta"].(string); ok {
			out += s
		}
		if s, ok := e.Data["content"].(string); ok {
			out += "[" + s + "]"
		}
	}
	return out
}

func TestSplice(t *testing.T) {
	user := Envelope{Type: UserMessage, Data: map[string]any{"content": "q"}}
	cases := []struct {
		name      string
		base      []Envelope
		incoming  []Envelope
		wantText  string
		wantCount int
	}{
		{
			// The crash case: the sandbox buffers only what it emitted after the
			// previous stream detached, so the two halves are disjoint.
			name:      "disjoint suffix appends and the sentence reads whole",
			base:      []Envelope{user, delta("about an ")},
			incoming:  []Envelope{delta("hour")},
			wantText:  "[q]about an hour",
			wantCount: 3,
		},
		{
			// The re-run case: the same reconnect drains the same buffer twice.
			name:      "identical incoming is absorbed, not appended",
			base:      []Envelope{user, delta("hello")},
			incoming:  []Envelope{user, delta("hello")},
			wantText:  "[q]hello",
			wantCount: 2,
		},
		{
			name:      "an incoming already at the tail of base is absorbed",
			base:      []Envelope{user, delta("a"), delta("b")},
			incoming:  []Envelope{delta("b")},
			wantText:  "[q]ab",
			wantCount: 3,
		},
		{
			name:      "empty base is just the incoming",
			base:      nil,
			incoming:  []Envelope{user, delta("x")},
			wantText:  "[q]x",
			wantCount: 2,
		},
		{
			name:      "empty incoming leaves the base intact",
			base:      []Envelope{user, delta("x")},
			incoming:  nil,
			wantText:  "[q]x",
			wantCount: 2,
		},
		{
			// Transients never reach storage, whichever side they arrive on.
			name:      "heartbeats are dropped by the compaction the splice ends with",
			base:      []Envelope{user},
			incoming:  []Envelope{{Type: Heartbeat, Data: map[string]any{}}, delta("y")},
			wantText:  "[q]y",
			wantCount: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Splice(tc.base, tc.incoming)
			if len(got) != tc.wantCount {
				t.Fatalf("got %d events, want %d: %+v", len(got), tc.wantCount, got)
			}
			if s := text(got); s != tc.wantText {
				t.Fatalf("got text %q, want %q", s, tc.wantText)
			}
		})
	}
}

// Splicing must be idempotent: applying the same incoming to a base that already
// absorbed it changes nothing. This is what makes a cadence flush inside a
// reconnect safe — every flush re-writes the whole merged turn.
func TestSpliceIsIdempotent(t *testing.T) {
	base := []Envelope{{Type: UserMessage, Data: map[string]any{"content": "q"}}, delta("one")}
	incoming := []Envelope{delta("two"), {Type: QueryComplete, Data: map[string]any{"status": "complete"}}}
	once := Splice(base, incoming)
	twice := Splice(once, incoming)
	if len(once) != len(twice) {
		t.Fatalf("second splice changed the turn: %d then %d events", len(once), len(twice))
	}
	if text(once) != text(twice) {
		t.Fatalf("second splice changed the text: %q then %q", text(once), text(twice))
	}
}

// Distinct events that merely look similar must not be mistaken for an overlap.
func TestSpliceKeepsGenuinelyRepeatedDeltas(t *testing.T) {
	base := []Envelope{delta("ha")}
	incoming := []Envelope{delta("ha")}
	got := Splice(base, incoming)
	// The whole of incoming equals the whole tail of base, so it is absorbed —
	// documented behaviour, and the safe direction when the alternative is
	// recording the model saying everything twice.
	if text(got) != "ha" {
		t.Fatalf("got %q, want %q", text(got), "ha")
	}
	// With a distinguishing timestamp it is kept.
	b2 := []Envelope{{Type: ContentDelta, Data: map[string]any{"delta": "ha"}, Timestamp: "t1"}}
	i2 := []Envelope{{Type: ContentDelta, Data: map[string]any{"delta": "ha"}, Timestamp: "t2"}}
	if got := text(Splice(b2, i2)); got != "haha" {
		t.Fatalf("got %q, want %q", got, "haha")
	}
}
