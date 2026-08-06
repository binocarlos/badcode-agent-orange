package events

import "encoding/json"

// Splice merges the events of a NEW generation of one turn onto what an EARLIER
// generation of the same turn already persisted.
//
// A "generation" is one pipeline.Run over one SSE attachment. Within a single
// generation the pipeline persists CUMULATIVE snapshots and the store upserts by
// (session, query), so each write supersedes the last — no merge needed. Across
// generations that is wrong: a reconnect after agentd died mid-turn drains the
// sandbox's replay buffer, which holds only the events emitted SINCE the previous
// stream detached (the in-image StreamService buffers only while no stream is
// attached). Writing that suffix as the whole row would erase the pre-crash
// prefix — including the human's own prompt, seeded before the turn was
// dispatched. So the new generation's events are appended to the old ones.
//
// The join is an overlap splice rather than a blind append: the largest k for
// which the last k of base equals the first k of incoming is treated as the same
// events seen twice, and only incoming[k:] is appended. k is normally 0 (the
// sandbox never hands the same event to two attachments), so this is belt and
// braces against a store that returns a base already containing the tail — for
// example an idempotent re-run of the same reconnect.
//
// Only `incoming` is Compacted, and the join is left as two adjacent delta
// envelopes rather than fused into one. That is deliberate: fusing them would
// destroy the very boundary the next splice needs to recognise, so a third
// generation of the same turn would append text it had already recorded. The
// browser's reducer concatenates consecutive deltas anyway, so a sentence
// interrupted by a crash still replays as one message.
//
// Equality is over the marshalled envelope (type, data, timestamp). Two genuinely
// distinct events would have to be byte-identical INCLUDING the sandbox's
// millisecond timestamp to collide, and then only if they sat exactly on the
// boundary.
func Splice(base, incoming []Envelope) []Envelope {
	incoming = Compact(incoming)
	if len(base) == 0 {
		return incoming
	}
	if len(incoming) == 0 {
		return base
	}
	maxK := len(base)
	if len(incoming) < maxK {
		maxK = len(incoming)
	}
	baseKeys := envelopeKeys(base)
	inKeys := envelopeKeys(incoming)
	overlap := 0
	for k := maxK; k > 0; k-- {
		if equalKeys(baseKeys[len(baseKeys)-k:], inKeys[:k]) {
			overlap = k
			break
		}
	}
	out := make([]Envelope, 0, len(base)+len(incoming)-overlap)
	out = append(out, base...)
	out = append(out, incoming[overlap:]...)
	return out
}

func envelopeKeys(evs []Envelope) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			// Unmarshalable data cannot be compared; make it unique so it is
			// never treated as an overlap (append is the safe direction — it
			// keeps the event, at worst twice, instead of dropping it).
			out[i] = "\x00unmarshalable"
			continue
		}
		out[i] = string(b)
	}
	return out
}

func equalKeys(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] || a[i] == "\x00unmarshalable" {
			return false
		}
	}
	return true
}
