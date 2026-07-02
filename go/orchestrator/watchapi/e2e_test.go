package watchapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestWatchSurfaceEndToEnd(t *testing.T) {
	h, d := newTestHandlers(t)
	ctx := context.Background()
	_, _ = d.board.Append(ctx, orchestrator.SeedFragment("routing-guidance", "Be basic."))
	id, _ := d.tickets.Create(ctx, orchestrator.Ticket{
		Title: "draft post", Status: orchestrator.StatusNeedsHuman,
		PendingPost: json.RawMessage(`{"channel":"bluesky","text":"hi"}`),
	})

	srv := newAuthServer(t, h) // no token
	defer srv.Close()

	// 1. the desk shows the pending approval
	var tickets []orchestrator.Ticket
	getJSON(t, srv, "/api/tickets?status=needs_human", &tickets)
	if len(tickets) != 1 || tickets[0].ID != id {
		t.Fatalf("pending approvals: %+v", tickets)
	}

	// 2. leave a note → a new board revision
	var fbOut struct {
		Revision string `json:"revision"`
	}
	postJSON(t, srv, "/api/feedback", `{"target_ref":"fragment:routing-guidance","note":"be clever"}`, &fbOut)
	if fbOut.Revision == "" {
		t.Fatalf("no revision from feedback")
	}
	if len(d.feedback.got) != 1 {
		t.Fatalf("feedback not applied: %+v", d.feedback.got)
	}

	// 3. the story timeline reflects the seed revision
	var revs []RevisionDTO
	getJSON(t, srv, "/api/board/revisions", &revs)
	if len(revs) < 1 {
		t.Fatalf("empty timeline")
	}

	// 4. approve → publish flow returns a ref (through the injected gate, never a Connector)
	var apOut struct {
		Ref string `json:"ref"`
	}
	postJSON(t, srv, "/api/tickets/"+id+"/approve", ``, &apOut)
	if apOut.Ref == "" || len(d.approver.calls) != 1 || d.approver.calls[0] != id {
		t.Fatalf("approve did not run the gate: %+v calls=%+v", apOut, d.approver.calls)
	}

	// 5. trigger a tick
	req, _ := http.NewRequest("POST", srv.URL+"/api/trigger", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusAccepted || d.trigger.n != 1 {
		t.Fatalf("trigger failed: %d n=%d", resp.StatusCode, d.trigger.n)
	}
}
