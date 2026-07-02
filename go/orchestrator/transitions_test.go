package orchestrator

import (
	"testing"
)

// §10c §D: Transition is the ONLY mutator of Status/Attempts/AttemptNotes. These
// tests pin every event's lane rule, the uniform attempts accounting, and the
// MaxAttempts zero-value convention.

func TestTransitionLaneRules(t *testing.T) {
	cases := []struct {
		name        string
		ticket      Ticket
		ev          TicketEvent
		note        string
		maxAttempts int

		wantStatus   TicketStatus
		wantAttempts int
		wantNotes    int
	}{
		{name: "spawned: todo → in_progress",
			ticket: Ticket{Status: StatusTodo}, ev: EvSpawned,
			maxAttempts: 2, wantStatus: StatusInProgress},
		{name: "delivered: in_progress → in_review",
			ticket: Ticket{Status: StatusInProgress}, ev: EvDelivered,
			maxAttempts: 2, wantStatus: StatusInReview},
		{name: "escalated: in_progress → needs_human",
			ticket: Ticket{Status: StatusInProgress}, ev: EvEscalated,
			maxAttempts: 2, wantStatus: StatusNeedsHuman},
		{name: "verify_passed internal → done",
			ticket: Ticket{Status: StatusInReview, Disposition: DispositionInternal}, ev: EvVerifyPassed,
			maxAttempts: 2, wantStatus: StatusDone},
		{name: "verify_passed empty disposition ≡ internal → done",
			ticket: Ticket{Status: StatusInReview}, ev: EvVerifyPassed,
			maxAttempts: 2, wantStatus: StatusDone},
		{name: "verify_passed publish → needs_human (the disposition hop)",
			ticket: Ticket{Status: StatusInReview, Disposition: DispositionPublish}, ev: EvVerifyPassed,
			maxAttempts: 2, wantStatus: StatusNeedsHuman},
		{name: "verify_failed below cap → todo, +attempt, +note",
			ticket: Ticket{Status: StatusInReview}, ev: EvVerifyFailed, note: "FAIL: too dull",
			maxAttempts: 2, wantStatus: StatusTodo, wantAttempts: 1, wantNotes: 1},
		{name: "verify_failed at cap → needs_human",
			ticket: Ticket{Status: StatusInReview, Attempts: 1}, ev: EvVerifyFailed, note: "FAIL: still dull",
			maxAttempts: 2, wantStatus: StatusNeedsHuman, wantAttempts: 2, wantNotes: 1},
		{name: "rejected below cap → todo, +attempt, +note (fixes the Reject bypass)",
			ticket: Ticket{Status: StatusNeedsHuman}, ev: EvRejected, note: "too salesy",
			maxAttempts: 2, wantStatus: StatusTodo, wantAttempts: 1, wantNotes: 1},
		{name: "rejected at cap → needs_human",
			ticket: Ticket{Status: StatusNeedsHuman, Attempts: 1}, ev: EvRejected, note: "still salesy",
			maxAttempts: 2, wantStatus: StatusNeedsHuman, wantAttempts: 2, wantNotes: 1},
		{name: "approved: needs_human → done",
			ticket: Ticket{Status: StatusNeedsHuman}, ev: EvApproved,
			maxAttempts: 2, wantStatus: StatusDone},
		{name: "answered: needs_human → todo, +note, NO attempt increment",
			ticket: Ticket{Status: StatusNeedsHuman, Attempts: 1}, ev: EvAnswered, note: "use a formal tone",
			maxAttempts: 2, wantStatus: StatusTodo, wantAttempts: 1, wantNotes: 1},
		{name: "spawn_failed: in_progress → todo, NO attempt increment",
			ticket: Ticket{Status: StatusInProgress, Attempts: 1}, ev: EvSpawnFailed,
			maxAttempts: 2, wantStatus: StatusTodo, wantAttempts: 1},
		{name: "floor_refused: todo → needs_human (fail-loud)",
			ticket: Ticket{Status: StatusTodo}, ev: EvFloorRefused,
			maxAttempts: 2, wantStatus: StatusNeedsHuman},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := tc.ticket
			Transition(&tk, tc.ev, tc.note, tc.maxAttempts)
			if tk.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", tk.Status, tc.wantStatus)
			}
			if tk.Attempts != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", tk.Attempts, tc.wantAttempts)
			}
			if len(tk.AttemptNotes) != tc.wantNotes {
				t.Fatalf("notes = %v, want %d", tk.AttemptNotes, tc.wantNotes)
			}
			if tc.wantNotes > 0 && tk.AttemptNotes[len(tk.AttemptNotes)-1] != tc.note {
				t.Fatalf("last note = %q, want %q", tk.AttemptNotes[len(tk.AttemptNotes)-1], tc.note)
			}
			if tk.UpdatedAt == 0 {
				t.Fatalf("UpdatedAt not stamped")
			}
		})
	}
}

func TestTransitionMaxAttemptsZeroMeansDefault(t *testing.T) {
	if DefaultMaxAttempts != 2 {
		t.Fatalf("DefaultMaxAttempts = %d, want 2", DefaultMaxAttempts)
	}
	tk := Ticket{Status: StatusInReview}
	Transition(&tk, EvVerifyFailed, "FAIL: n1", 0) // 0 resolves to the default (2)
	if tk.Status != StatusTodo || tk.Attempts != 1 {
		t.Fatalf("first fail: %s/%d, want todo/1", tk.Status, tk.Attempts)
	}
	tk.Status = StatusInReview
	Transition(&tk, EvVerifyFailed, "FAIL: n2", 0)
	if tk.Status != StatusNeedsHuman || tk.Attempts != 2 {
		t.Fatalf("second fail: %s/%d, want needs_human/2", tk.Status, tk.Attempts)
	}
	if ResolveMaxAttempts(0) != DefaultMaxAttempts || ResolveMaxAttempts(5) != 5 {
		t.Fatalf("ResolveMaxAttempts convention broken")
	}
}

func TestTransitionSkipsEmptyNotes(t *testing.T) {
	tk := Ticket{Status: StatusNeedsHuman}
	Transition(&tk, EvRejected, "", 2) // empty-note reject is allowed (§8) — no empty bullet
	if len(tk.AttemptNotes) != 0 {
		t.Fatalf("empty note appended: %v", tk.AttemptNotes)
	}
	if tk.Attempts != 1 || tk.Status != StatusTodo {
		t.Fatalf("empty-note reject still counts the attempt: %s/%d", tk.Status, tk.Attempts)
	}
}

func TestTransitionNotesAccumulate(t *testing.T) {
	tk := Ticket{Status: StatusInReview}
	Transition(&tk, EvVerifyFailed, "FAIL: reason one", 3)
	tk.Status = StatusNeedsHuman
	Transition(&tk, EvAnswered, "answer two", 3)
	if len(tk.AttemptNotes) != 2 || tk.AttemptNotes[0] != "FAIL: reason one" || tk.AttemptNotes[1] != "answer two" {
		t.Fatalf("notes did not accumulate: %v", tk.AttemptNotes)
	}
}
