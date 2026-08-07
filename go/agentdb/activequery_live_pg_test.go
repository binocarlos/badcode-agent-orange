package agentdb

import (
	"context"
	"testing"
)

// D5 — the in-flight turn's two ids, against the real schema (migration 039).
//
// One store, one session, no concurrency: the point is the columns and the
// conditional clear, and the shared test database is close enough to its
// connection ceiling that a test which opens pools per case is a liability.
func TestActiveQueryRoundTripsAndClearsOnlyItsOwnTurn(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	sess := newLiveSession(t, s, "d5-activequery", "d5@example.com")

	// A fresh session has no turn in flight.
	qid, sbx, err := s.GetActiveQuery(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetActiveQuery: %v", err)
	}
	if qid != "" || sbx != "" {
		t.Fatalf("a session that never ran a turn reports one in flight: %q/%q", qid, sbx)
	}

	// The runner knows its own id before it dispatches; the sandbox's arrives a
	// frame later. Both writes must land, and the second must not lose the first.
	if err := s.SetActiveQuery(ctx, sess.ID, "q-"+sess.ID+"-1", ""); err != nil {
		t.Fatalf("SetActiveQuery: %v", err)
	}
	if err := s.SetActiveQuery(ctx, sess.ID, "q-"+sess.ID+"-1", "sbx-uuid"); err != nil {
		t.Fatalf("SetActiveQuery (join): %v", err)
	}
	qid, sbx, err = s.GetActiveQuery(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetActiveQuery: %v", err)
	}
	if qid != "q-"+sess.ID+"-1" || sbx != "sbx-uuid" {
		t.Fatalf("the two ids did not round-trip: %q/%q", qid, sbx)
	}

	// A superseded turn must not be able to clear the live one: it unwinds after
	// the next turn has already started, and an unconditional clear would leave
	// the running turn unreconnectable.
	if err := s.ClearActiveQuery(ctx, sess.ID, "q-"+sess.ID+"-0"); err != nil {
		t.Fatalf("ClearActiveQuery (stale): %v", err)
	}
	qid, sbx, err = s.GetActiveQuery(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetActiveQuery: %v", err)
	}
	if qid == "" || sbx == "" {
		t.Fatalf("a stale turn cleared the live one's ids: %q/%q", qid, sbx)
	}

	// Its own turn, it may clear.
	if err := s.ClearActiveQuery(ctx, sess.ID, "q-"+sess.ID+"-1"); err != nil {
		t.Fatalf("ClearActiveQuery: %v", err)
	}
	qid, sbx, err = s.GetActiveQuery(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetActiveQuery: %v", err)
	}
	if qid != "" || sbx != "" {
		t.Fatalf("the settled turn is still on record: %q/%q", qid, sbx)
	}

	// Reading an unknown session is a question, not an error: the answer is
	// "nothing is running".
	qid, sbx, err = s.GetActiveQuery(ctx, "no-such-session")
	if err != nil || qid != "" || sbx != "" {
		t.Fatalf("GetActiveQuery on a missing session: %q/%q err=%v", qid, sbx, err)
	}
}
