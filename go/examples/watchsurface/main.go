// Command watchsurface boots the Slice-E watch/approve/note surface over an
// in-memory board + tickets so a human can open the page, click Approve / leave a
// note, and see the story update. Offline, deterministic, nothing publishes for
// real: the approval gate goes through the real orchestrator.ApprovalService, but
// its Connector is a demo stub that only logs. This is the human-watchable sibling
// of the Slice-0 learningloop example.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
	"github.com/binocarlos/badcode-agent-orange/orchestrator/watchapi"
)

// demoConnector is the world seam for the demo — it logs instead of hitting a real
// channel. It is reachable ONLY through ApprovalService.Approve (the publish gate).
type demoConnector struct{}

func (demoConnector) Publish(_ context.Context, p orchestrator.Post) (string, error) {
	log.Printf("PUBLISH (demo, no real network): channel=%s text=%q", p.Channel, p.Text)
	return "demo://published/" + p.DedupeKey, nil
}

// demoTrigger stands in for the manager loop (Slice C) — it satisfies
// orchestrator.Triggerer (Tick) and just logs.
type demoTrigger struct{}

func (demoTrigger) Tick(context.Context) error {
	log.Println("trigger: (no manager loop wired in this demo)")
	return nil
}

func main() {
	ctx := context.Background()

	board := orchestrator.NewMemBoard()
	_, _ = board.Append(ctx, orchestrator.SeedFragment("routing-guidance", "Be basic."))

	tickets := orchestrator.NewMemTickets()
	tel := orchestrator.NewTelemetry()

	// a Needs-Human ticket carrying a drafted post awaiting approval
	_, _ = tickets.Create(ctx, orchestrator.Ticket{
		Title:       "draft launch post",
		Status:      orchestrator.StatusNeedsHuman,
		PendingPost: mustJSON(orchestrator.Post{Channel: "bluesky", Text: "hello world (draft)"}),
	})

	// the real Slice-D approval service is the Approver, Rejecter AND (§10c §E)
	// Answerer port.
	approval := orchestrator.NewApprovalService(tickets, demoConnector{}, tel)

	// the real Slice-C learning applier: a scripted reviser makes the change visible.
	reviser := &orchestrator.ScriptedModel{
		Default: "Be basic.",
		Rules: []orchestrator.Rule{
			{Contains: "clever", Reply: "Be clever and witty; open with a hook."},
		},
	}
	feedback := orchestrator.HumanFeedbackApplier{Board: board, Reviser: reviser}

	h, err := watchapi.New(watchapi.Config{
		Board:     board,
		Revisions: board,
		Tickets:   tickets,
		Telemetry: tel,
		Approver:  approval,
		Rejecter:  approval,
		Answerer:  approval,
		Feedback:  feedback,
		Trigger:   demoTrigger{},
	})
	if err != nil {
		log.Fatal(err)
	}

	log.Println("watch surface on http://localhost:8099")
	log.Fatal(http.ListenAndServe(":8099", h.Mux()))
}

func mustJSON(p orchestrator.Post) []byte {
	b, err := json.Marshal(p)
	if err != nil {
		log.Fatal(err)
	}
	return b
}
