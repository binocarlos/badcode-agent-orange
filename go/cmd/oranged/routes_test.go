package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
	"github.com/binocarlos/badcode-agent-orange/orchestrator/watchapi"
)

// newTestServer stands up the full root mux — daemon cockpit routes over the
// real watchapi surface — exactly as main.go wires it.
func newTestServer(t *testing.T, token string) (*httptest.Server, *Daemon) {
	t.Helper()
	d, board, tickets, tel := newTestDaemon(t)
	connector := &fileConnector{path: filepath.Join(t.TempDir(), "published.jsonl")}
	approval := orchestrator.NewApprovalService(tickets, connector, tel)
	feedback := orchestrator.HumanFeedbackApplier{Board: board,
		Reviser: &orchestrator.ScriptedModel{Default: "revised guidance"}}
	watch, err := watchapi.New(watchapi.Config{
		Board: board, Revisions: board, Tickets: tickets, Telemetry: tel,
		Approver: approval, Rejecter: approval, Answerer: approval,
		Feedback: feedback, Trigger: d, AuthToken: token,
	})
	if err != nil {
		t.Fatalf("watchapi: %v", err)
	}
	srv := httptest.NewServer(newRootMux(d, watch.Mux(), token))
	t.Cleanup(srv.Close)
	return srv, d
}

func TestGoalRoundTripOverHTTP(t *testing.T) {
	srv, d := newTestServer(t, "")

	resp, err := http.Post(srv.URL+"/api/goal", "application/json",
		strings.NewReader(`{"goal":"research the stock hypothesis"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/goal: err=%v code=%d", err, resp.StatusCode)
	}
	var set map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&set)
	if set["goal"] != "research the stock hypothesis" || set["revision"] == "" {
		t.Fatalf("set response wrong: %+v", set)
	}
	if d.Goal() != "research the stock hypothesis" {
		t.Fatalf("daemon goal not set: %q", d.Goal())
	}

	resp, _ = http.Get(srv.URL + "/api/goal")
	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["goal"] != "research the stock hypothesis" {
		t.Fatalf("GET /api/goal = %+v", got)
	}

	// Empty goal → 400.
	resp, _ = http.Post(srv.URL+"/api/goal", "application/json", strings.NewReader(`{"goal":"  "}`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty goal must 400, got %d", resp.StatusCode)
	}
}

func TestTriggerReachesDaemonAndStatusRecordsIt(t *testing.T) {
	srv, _ := newTestServer(t, "")
	if _, err := http.Post(srv.URL+"/api/goal", "application/json",
		strings.NewReader(`{"goal":"write a launch post"}`)); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	// The watchapi trigger route falls through to the daemon (its Triggerer),
	// so the tick lands in the status ring rather than being discarded.
	resp, err := http.Post(srv.URL+"/api/trigger", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/trigger: err=%v code=%d", err, resp.StatusCode)
	}
	resp, _ = http.Get(srv.URL + "/api/status")
	var st statusView
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if st.Goal != "write a launch post" || len(st.Ticks) != 1 || st.Ticks[0].Report.Planned != 1 {
		t.Fatalf("status wrong: %+v", st)
	}
}

func TestFallthroughToWatchapi(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp, err := http.Get(srv.URL + "/api/tickets")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tickets must reach watchapi: err=%v code=%d", err, resp.StatusCode)
	}
	resp, _ = http.Get(srv.URL + "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web UI must be served: %d", resp.StatusCode)
	}
}

func TestConsultantReviewRoute(t *testing.T) {
	srv, d := newTestServer(t, "")
	// No evidence → skipped.
	resp, _ := http.Post(srv.URL+"/api/consultant/review", "application/json", nil)
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["skipped"] != true {
		t.Fatalf("want skipped without evidence: %+v", out)
	}
	// With evidence → reviewed (scripted consultant says OK → no advice).
	_, _ = d.tel.Record(context.Background(), orchestrator.Run{Scope: "worker", Output: "a draft"})
	resp, _ = http.Post(srv.URL+"/api/consultant/review", "application/json", nil)
	out = nil
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["skipped"] != false || out["advised"] != false {
		t.Fatalf("want a non-advising review: %+v", out)
	}
}

func TestAuthGuardsDaemonRoutes(t *testing.T) {
	srv, _ := newTestServer(t, "secret")
	// Unauthenticated → 401 on both daemon and watchapi routes.
	for _, path := range []string{"/api/status", "/api/tickets"} {
		resp, _ := http.Get(srv.URL + path)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s without token = %d, want 401", path, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized GET /api/status: err=%v code=%d", err, resp.StatusCode)
	}
}
