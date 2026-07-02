package orchestrator

import (
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestComposeResolvesFragmentsAndInput(t *testing.T) {
	board := agentdb.Board{Revision: "r1", Fragments: []agentdb.BoardPromptFragment{
		{ID: "routing-guidance", Body: "Be clever."},
	}}
	out, err := Compose(board, "{{fragment:routing-guidance}}\nGoal: {{input}}", "grow the brand")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if out != "Be clever.\nGoal: grow the brand" {
		t.Fatalf("composed = %q", out)
	}

	if _, err := Compose(board, "{{fragment:missing}}", "x"); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-fragment error, got %v", err)
	}
}
