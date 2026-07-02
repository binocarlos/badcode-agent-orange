package orchestrator

import "time"

// transitions.go is the §10c §D ticket state machine: ONE transition function that
// is the ONLY mutator of Ticket.Status / Attempts / AttemptNotes (stamping
// UpdatedAt). Every mutation site — TicketResultSink.Deliver, reconcile,
// chooseAndSpawn, ApprovalService.{Approve,Reject,Answer} — routes through it, so
// the lane rules and attempts accounting live in exactly one place. Approval-side
// bookkeeping (PendingPost / PublishedRef / Result payloads) stays with its owner;
// Transition owns lanes + attempts + notes.

// TicketEvent is everything that can move a ticket between lanes.
type TicketEvent string

const (
	EvSpawned      TicketEvent = "spawned"       // todo → in_progress
	EvDelivered    TicketEvent = "delivered"     // in_progress → in_review (ResultDone)
	EvEscalated    TicketEvent = "escalated"     // in_progress → needs_human (worker question/failure)
	EvVerifyPassed TicketEvent = "verify_passed" // in_review → done (internal) | needs_human+PendingPost (publish)
	EvVerifyFailed TicketEvent = "verify_failed" // in_review → todo (+note, +attempt) | needs_human at cap
	EvRejected     TicketEvent = "rejected"      // needs_human → todo (+note, +attempt) | needs_human at cap
	EvApproved     TicketEvent = "approved"      // needs_human → done (+PublishedRef)
	EvAnswered     TicketEvent = "answered"      // needs_human → todo (+note; NO attempt increment)
	EvSpawnFailed  TicketEvent = "spawn_failed"  // in_progress → todo (transient revert; NO attempt increment)
	EvFloorRefused TicketEvent = "floor_refused" // todo → needs_human (fail-loud)
)

// DefaultMaxAttempts is the §10c §D zero-value convention: a configured
// MaxAttempts of 0 means this default.
const DefaultMaxAttempts = 2

// ResolveMaxAttempts maps the zero-value config to DefaultMaxAttempts — the ONE
// place the MaxAttempts==0 convention is interpreted. Transition applies it as a
// safety net, so no caller can accidentally turn 0 into "always at cap".
func ResolveMaxAttempts(configured int) int {
	if configured <= 0 {
		return DefaultMaxAttempts
	}
	return configured
}

// Transition applies ev to t. It is the ONLY place Status/Attempts/AttemptNotes
// change (and it stamps UpdatedAt). maxAttempts is the resolved retry cap,
// consulted by EvVerifyFailed/EvRejected only; pass the exchange's configured
// value (0 resolves to DefaultMaxAttempts). Rules it centralizes:
//
//   - Attempts accounting is uniform: EvVerifyFailed AND EvRejected increment
//     Attempts and append the note; at Attempts >= maxAttempts both go
//     needs_human instead of todo (fixes the Reject bypass).
//   - EvAnswered and EvSpawnFailed do NOT increment (an answer is new
//     information; a transient error is not the model's failure).
//   - EvVerifyPassed consults Disposition: publish → needs_human (the caller
//     files the PendingPost — the one wire into the approval gate); internal/""
//     → done.
//   - Notes: a non-empty note is appended to AttemptNotes (verify Reasons,
//     reject notes, human answers — the ticket-level learning loop feed).
func Transition(t *Ticket, ev TicketEvent, note string, maxAttempts int) {
	switch ev {
	case EvSpawned:
		t.Status = StatusInProgress
	case EvDelivered:
		t.Status = StatusInReview
	case EvEscalated:
		t.Status = StatusNeedsHuman
	case EvVerifyPassed:
		if t.Disposition == DispositionPublish {
			t.Status = StatusNeedsHuman
		} else {
			t.Status = StatusDone
		}
	case EvVerifyFailed, EvRejected:
		t.Attempts++
		appendNote(t, note)
		if t.Attempts >= ResolveMaxAttempts(maxAttempts) {
			t.Status = StatusNeedsHuman // fail-loud: out of attempts
		} else {
			t.Status = StatusTodo // back to the queue for another attempt
		}
	case EvApproved:
		t.Status = StatusDone
	case EvAnswered:
		appendNote(t, note)
		t.Status = StatusTodo
	case EvSpawnFailed:
		t.Status = StatusTodo
	case EvFloorRefused:
		t.Status = StatusNeedsHuman
	}
	t.UpdatedAt = time.Now().Unix()
}

// unblock is the deps-satisfied Blocked→Todo move (contracts §2). It is not a
// TicketEvent — the frozen §10c §D vocabulary covers the work-lifecycle events —
// but the mutation lives here so transitions.go stays the only Status writer.
func unblock(t *Ticket) {
	t.Status = StatusTodo
	t.UpdatedAt = time.Now().Unix()
}

func appendNote(t *Ticket, note string) {
	if note == "" {
		return
	}
	t.AttemptNotes = append(t.AttemptNotes, note)
}
