package watchapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// --- recording fakes for the action ports ---

type fakeApprover struct {
	calls []string
	ref   string
	err   error
}

func (f *fakeApprover) Approve(_ context.Context, id string) (string, error) {
	f.calls = append(f.calls, id)
	return f.ref, f.err
}

type rejectCall struct{ ID, Note string }

type fakeRejecter struct {
	calls []rejectCall
	err   error
}

func (f *fakeRejecter) Reject(_ context.Context, id, note string) (orchestrator.HumanFeedback, error) {
	f.calls = append(f.calls, rejectCall{id, note})
	if f.err != nil {
		return orchestrator.HumanFeedback{}, f.err
	}
	// mirrors orchestrator.ApprovalService.Reject: the note becomes ticket-targeted feedback.
	return orchestrator.HumanFeedback{TargetRef: "ticket:" + id, Note: note}, nil
}

type fakeFeedback struct {
	got []orchestrator.HumanFeedback
	rev string
	err error
}

func (f *fakeFeedback) Apply(_ context.Context, fb orchestrator.HumanFeedback) (string, error) {
	f.got = append(f.got, fb)
	return f.rev, f.err
}

type fakeTrigger struct {
	n   int
	err error
}

func (f *fakeTrigger) Tick(context.Context) error { f.n++; return f.err }

// newTestConfig wires a fully in-memory, deterministic Config.
func newTestConfig() Config {
	board := orchestrator.NewMemBoard()
	return Config{
		Board:     board,
		Revisions: board,
		Tickets:   orchestrator.NewMemTickets(),
		Telemetry: orchestrator.NewTelemetry(),
		Approver:  &fakeApprover{ref: "at://did/post/1"},
		Rejecter:  &fakeRejecter{},
		Feedback:  &fakeFeedback{rev: "r2"},
		Trigger:   &fakeTrigger{},
	}
}

// testDeps carries the concrete fakes/stores back to a test for assertions.
type testDeps struct {
	cfg      Config
	board    *orchestrator.MemBoard
	tickets  *orchestrator.MemTickets
	tel      *orchestrator.MemTelemetry
	approver *fakeApprover
	rejecter *fakeRejecter
	feedback *fakeFeedback
	trigger  *fakeTrigger
}

func newTestHandlers(t *testing.T) (*Handlers, testDeps) {
	t.Helper()
	board := orchestrator.NewMemBoard()
	tickets := orchestrator.NewMemTickets()
	tel := orchestrator.NewTelemetry()
	ap := &fakeApprover{ref: "at://did/post/1"}
	rj := &fakeRejecter{}
	fb := &fakeFeedback{rev: "r2"}
	tr := &fakeTrigger{}
	cfg := Config{Board: board, Revisions: board, Tickets: tickets, Telemetry: tel,
		Approver: ap, Rejecter: rj, Feedback: fb, Trigger: tr}
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, testDeps{cfg, board, tickets, tel, ap, rj, fb, tr}
}

// --- HTTP test helpers (Task 12) ---

func newAuthServer(t *testing.T, h *Handlers) *httptest.Server {
	t.Helper()
	return httptest.NewServer(h.Mux())
}

func getJSON(t *testing.T, srv *httptest.Server, path string, out any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil || resp.StatusCode/100 != 2 {
		t.Fatalf("GET %s: err=%v code=%v", path, err, statusOf(resp))
	}
	defer resp.Body.Close()
	decode(t, resp.Body, out)
}

func postJSON(t *testing.T, srv *httptest.Server, path, body string, out any) {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode/100 != 2 {
		t.Fatalf("POST %s: err=%v code=%v", path, err, statusOf(resp))
	}
	defer resp.Body.Close()
	if out != nil {
		decode(t, resp.Body, out)
	}
}

func decode(t *testing.T, r io.Reader, out any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}
