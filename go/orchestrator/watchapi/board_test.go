package watchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestRevisionsAndCurrentRenderTheStory(t *testing.T) {
	h, d := newTestHandlers(t)
	ctx := context.Background()
	_, _ = d.board.Append(ctx, orchestrator.SeedFragment("routing-guidance", "Be basic."))
	_, _ = d.board.Append(ctx, agentdb.Changeset{Author: "human-feedback", Message: "be clever",
		Ops: orchestrator.SeedFragment("routing-guidance", "Be clever.").Ops})

	// revisions
	rec := httptest.NewRecorder()
	h.Revisions(rec, httptest.NewRequest("GET", "/api/board/revisions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("revisions status %d", rec.Code)
	}
	var revs []RevisionDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &revs)
	if len(revs) != 2 || revs[1].Author != "human-feedback" || revs[1].Message != "be clever" {
		t.Fatalf("revisions wrong: %+v", revs)
	}
	if revs[0].Seq != 1 || revs[1].Seq != 2 {
		t.Fatalf("seq wrong: %+v", revs)
	}

	// current
	rec = httptest.NewRecorder()
	h.Current(rec, httptest.NewRequest("GET", "/api/board/current", nil))
	var board BoardDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &board)
	if board.Revision != "r2" || len(board.Fragments) != 1 || board.Fragments[0].Body != "Be clever." {
		t.Fatalf("current wrong: %+v", board)
	}
}
