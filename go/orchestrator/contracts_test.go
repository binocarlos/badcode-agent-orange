package orchestrator

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestClassifyWorkerOutput(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantStatus ResultStatus
		wantText   string
	}{
		{"plain artifact is done", "here is the draft post", ResultDone, "here is the draft post"},
		{"escalate prefix is escalation", "ESCALATE: which tone do you want?", ResultEscalated, "which tone do you want?"},
		{"escalate trims surrounding space", "ESCALATE:   need a decision  ", ResultEscalated, "need a decision"},
		{"prefix mid-text is not an escalation", "the plan is to ESCALATE: later", ResultDone, "the plan is to ESCALATE: later"},
		{"empty output is done, not failed", "", ResultDone, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, text := ClassifyWorkerOutput(tc.raw)
			if status != tc.wantStatus || text != tc.wantText {
				t.Fatalf("ClassifyWorkerOutput(%q) = (%q, %q), want (%q, %q)",
					tc.raw, status, text, tc.wantStatus, tc.wantText)
			}
		})
	}
}

func TestMemBoardRevisionsInSeqOrder(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	r1, _ := board.Append(ctx, SeedFragment("routing-guidance", "Be basic."))
	r2, _ := board.Append(ctx, agentdb.Changeset{Author: "human-feedback", Message: "be clever",
		Ops: []agentdb.Op{fragOp(agentdb.OpUpdate, "routing-guidance", "Be clever.")}})

	revs, err := board.Revisions(ctx)
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if len(revs) != 2 || revs[0].ID != r1 || revs[1].ID != r2 {
		t.Fatalf("revisions not in seq order: %+v (want %s, %s)", revs, r1, r2)
	}
	if revs[0].Seq >= revs[1].Seq {
		t.Fatalf("seq not ascending: %d then %d", revs[0].Seq, revs[1].Seq)
	}
	// The timeline carries the story: author + message per revision.
	if revs[0].Author != "human" || revs[1].Message != "be clever" {
		t.Fatalf("revision metadata lost: %+v", revs)
	}
}
