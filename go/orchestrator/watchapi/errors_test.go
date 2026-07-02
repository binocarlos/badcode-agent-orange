package watchapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

var errPort = errors.New("port boom")

// --- failing port fakes ---

type failBoardPort struct{ err error }

func (f failBoardPort) Append(context.Context, agentdb.Changeset) (string, error) { return "", f.err }
func (f failBoardPort) Current(context.Context) (agentdb.Board, error) {
	return agentdb.Board{}, f.err
}
func (f failBoardPort) AsOf(context.Context, string) (agentdb.Board, error) {
	return agentdb.Board{}, f.err
}
func (f failBoardPort) Head(context.Context) (string, error) { return "", f.err }
func (f failBoardPort) Revisions(context.Context) ([]agentdb.BoardRevision, error) {
	return nil, f.err
}

var _ agentdb.BoardStore = failBoardPort{}
var _ RevisionLister = failBoardPort{}

type failTicketsPort struct{ err error }

func (f failTicketsPort) Create(context.Context, orchestrator.Ticket) (string, error) {
	return "", f.err
}
func (f failTicketsPort) Update(context.Context, orchestrator.Ticket) error { return f.err }
func (f failTicketsPort) Get(context.Context, string) (orchestrator.Ticket, error) {
	return orchestrator.Ticket{}, f.err
}
func (f failTicketsPort) List(context.Context, orchestrator.TicketStatus) ([]orchestrator.Ticket, error) {
	return nil, f.err
}

var _ orchestrator.TicketStore = failTicketsPort{}

type failTelemetryPort struct{ err error }

func (f failTelemetryPort) Runs(context.Context) ([]orchestrator.Run, error) { return nil, f.err }

var _ TelemetryReader = failTelemetryPort{}

// --- New: every missing-port validation branch ---

func TestNewRequiresEveryPort(t *testing.T) {
	cases := []struct {
		port string
		nix  func(*Config)
	}{
		{"Board", func(c *Config) { c.Board = nil }},
		{"Revisions", func(c *Config) { c.Revisions = nil }},
		{"Tickets", func(c *Config) { c.Tickets = nil }},
		{"Telemetry", func(c *Config) { c.Telemetry = nil }},
		{"Approver", func(c *Config) { c.Approver = nil }},
		{"Rejecter", func(c *Config) { c.Rejecter = nil }},
		{"Answerer", func(c *Config) { c.Answerer = nil }},
		{"Feedback", func(c *Config) { c.Feedback = nil }},
		{"Trigger", func(c *Config) { c.Trigger = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.port, func(t *testing.T) {
			cfg := newTestConfig()
			tc.nix(&cfg)
			_, err := New(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.port+" is required") {
				t.Fatalf("missing %s: err = %v", tc.port, err)
			}
		})
	}
}

// --- auth edge cases ---

func TestAuthGuardEdgeCases(t *testing.T) {
	cfg := newTestConfig()
	cfg.AuthToken = "secret"
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	cases := []struct {
		name   string
		header string
		path   string
		want   int
	}{
		{"malformed: bare Bearer", "Bearer", "/api/runs", http.StatusUnauthorized},
		{"malformed: wrong scheme", "Basic secret", "/api/runs", http.StatusUnauthorized},
		{"malformed: lowercase scheme", "bearer secret", "/api/runs", http.StatusUnauthorized},
		{"malformed: token only", "secret", "/api/runs", http.StatusUnauthorized},
		{"trailing garbage", "Bearer secret extra", "/api/runs", http.StatusUnauthorized},
		// The guard is header-only by design: a query-param token is NOT accepted.
		{"query-param token unsupported", "", "/api/runs?token=secret", http.StatusUnauthorized},
		{"exact token", "Bearer secret", "/api/runs", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// --- handler 5xx branches via failing ports ---

func TestListTicketsStoreErrorIs500(t *testing.T) {
	h, _ := newTestHandlers(t)
	h.cfg.Tickets = failTicketsPort{err: errPort}
	rec := httptest.NewRecorder()
	h.ListTickets(rec, httptest.NewRequest(http.MethodGet, "/api/tickets", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestListTicketsEmptyStoreReturnsEmptyArrayNotNull(t *testing.T) {
	h, _ := newTestHandlers(t)
	rec := httptest.NewRecorder()
	h.ListTickets(rec, httptest.NewRequest(http.MethodGet, "/api/tickets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("empty list body = %q, want []", got)
	}
}

func TestRevisionsErrorIs500(t *testing.T) {
	h, _ := newTestHandlers(t)
	h.cfg.Revisions = failBoardPort{err: errPort}
	rec := httptest.NewRecorder()
	h.Revisions(rec, httptest.NewRequest(http.MethodGet, "/api/board/revisions", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestCurrentErrorIs500(t *testing.T) {
	h, _ := newTestHandlers(t)
	h.cfg.Board = failBoardPort{err: errPort}
	rec := httptest.NewRecorder()
	h.Current(rec, httptest.NewRequest(http.MethodGet, "/api/board/current", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestRunsErrorIs500(t *testing.T) {
	h, _ := newTestHandlers(t)
	h.cfg.Telemetry = failTelemetryPort{err: errPort}
	rec := httptest.NewRecorder()
	h.Runs(rec, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// --- Feedback 400/500 branches ---

func TestFeedbackBadJSONBodyIs400(t *testing.T) {
	h, _ := newTestHandlers(t)
	rec := httptest.NewRecorder()
	h.Feedback(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(`{not json`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestFeedbackApplierErrorIs500(t *testing.T) {
	h, d := newTestHandlers(t)
	d.feedback.err = errPort
	rec := httptest.NewRecorder()
	body := `{"target_ref":"ticket:t1","note":"be better"}`
	h.Feedback(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// --- missing-{id} guards (direct handler invocation: no route pattern, so
// PathValue("id") is empty) ---

func TestActionHandlersRequireTicketID(t *testing.T) {
	h, _ := newTestHandlers(t)
	cases := []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"approve", h.Approve},
		{"reject", h.Reject},
		{"answer", h.Answer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"note":"n","text":"t"}`)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}
