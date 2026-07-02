package watchapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// TestConcurrentWatchSurfaceRaceSmoke drives the FULL Mux — real MemBoard /
// MemTickets / MemTelemetry, a real ManagerExchange behind /api/trigger, the
// real ApprovalService behind /api/tickets/{id}/approve, and the real
// HumanFeedbackApplier behind /api/feedback — from many goroutines at once so
// `go test -race` sweeps the in-memory stores and the Slice C/D services under
// concurrent HTTP load. Assertions are deliberately light: the invariants under
// test are "no data race" and "the exactly-once publish gate holds".
func TestConcurrentWatchSurfaceRaceSmoke(t *testing.T) {
	ctx := context.Background()
	board := orchestrator.NewMemBoard()
	if _, err := board.Append(ctx, orchestrator.SeedFragment("role-writer", "You are a witty writer.")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tickets := orchestrator.NewMemTickets()
	tel := orchestrator.NewTelemetry()

	planJSON := `[{"title":"draft launch post","objective":"write it","acceptance":"witty"}]`
	router := orchestrator.NewTierRouter(map[orchestrator.ModelTier]orchestrator.Model{
		orchestrator.TierFull: &orchestrator.ScriptedModel{
			Default: "PASS: looks good",
			Rules:   []orchestrator.Rule{{Contains: "Plan this goal", Reply: planJSON}},
		},
		orchestrator.TierMid: &orchestrator.ScriptedModel{Default: "a witty draft"},
	})
	ledger := orchestrator.NewSpawnLedger()
	budget := orchestrator.Budget{MaxDepth: 3, MaxSpawns: 8, TreeTokens: 1_000_000}
	exchange := &orchestrator.ManagerExchange{
		Board: board, Tickets: tickets, Router: router,
		Runtime: &orchestrator.InProcRuntime{Board: board, Router: router,
			Sink: &orchestrator.TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: tel},
		Ledger: ledger, Telemetry: tel,
		Goal: "launch", ProjectID: "p1", ManagerSession: "mgr",
		PlanTier: orchestrator.TierFull, WorkerTier: orchestrator.TierMid,
		VerifyTier: orchestrator.TierFull, WorkerBudget: budget,
		PlanTemplate:   "Plan this goal into tickets as JSON: {{input}}",
		WorkerTemplate: "{{fragment:role-writer}}\nTask: {{input}}",
		MaxAttempts:    2,
	}

	conn := &orchestrator.FakeConnector{Ref: "fake://post/1"}
	svc := orchestrator.NewApprovalService(tickets, conn, tel)

	// A pre-filed pending post so /approve has a live target under contention.
	approveID, err := tickets.Create(ctx, orchestrator.Ticket{Title: "pre-drafted", Status: orchestrator.StatusInProgress})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := orchestrator.FilePendingPost(ctx, tickets, approveID,
		orchestrator.Post{Channel: "main", Text: "the draft"}); err != nil {
		t.Fatalf("file pending post: %v", err)
	}

	h, err := New(Config{
		Board: board, Revisions: board, Tickets: tickets, Telemetry: tel,
		Approver: svc, Rejecter: svc, Answerer: svc,
		Feedback: orchestrator.HumanFeedbackApplier{Board: board,
			Reviser: &orchestrator.ScriptedModel{Default: "Be clever."}},
		Trigger: orchestrator.ExchangeTrigger{Exchange: exchange},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := newAuthServer(t, h)
	defer srv.Close()

	do := func(t *testing.T, method, path, body string) {
		t.Helper()
		var req *http.Request
		var err error
		if method == http.MethodGet {
			req, err = http.NewRequest(method, srv.URL+path, nil)
		} else {
			req, err = http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		}
		if err != nil {
			t.Errorf("%s %s: %v", method, path, err)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("%s %s: %v", method, path, err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= http.StatusInternalServerError && resp.StatusCode != http.StatusBadGateway {
			// Losing approve races legitimately 502 ("no pending post");
			// anything 5xx beyond that is a real failure.
			t.Errorf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}

	const workers = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 3; j++ {
				do(t, http.MethodPost, "/api/trigger", "")
				do(t, http.MethodGet, "/api/tickets", "")
				do(t, http.MethodGet, "/api/tickets?status=needs_human", "")
				do(t, http.MethodPost, "/api/tickets/"+approveID+"/approve", "")
				do(t, http.MethodGet, "/api/board/current", "")
				do(t, http.MethodGet, "/api/board/revisions", "")
				do(t, http.MethodGet, "/api/runs", "")
				do(t, http.MethodPost, "/api/feedback",
					`{"target_ref":"fragment:role-writer","note":"be cleverer"}`)
			}
		}()
	}
	close(start)
	wg.Wait()

	// The publish gate must have held under the stampede: one publish, ever.
	if conn.Calls != 1 {
		t.Fatalf("publish gate broke under concurrency: Connector.Publish called %d times", conn.Calls)
	}
	got, err := tickets.Get(ctx, approveID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != orchestrator.StatusDone || got.PublishedRef != "fake://post/1" {
		t.Fatalf("approved ticket wrong: %+v", got)
	}
}
