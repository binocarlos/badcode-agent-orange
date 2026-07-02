// Package watchapi is the Slice-E watch/approve/note HTTP surface (contracts §8).
// It is simultaneously a control panel (approve/reject drafted posts), a teacher's
// desk (leave a note that edits guidance), and a content artifact (the legible
// board-revision + telemetry "watch it learn" story). The web client is a thin
// consumer of these routes; the API is the contract.
//
// Handlers depend ONLY on the small ports below, never on concrete impls, so the
// surface is testable with in-memory fakes and needs no Postgres board, manager
// loop, or real connector. Slice E NEVER touches a Connector: approve reaches
// publishing exclusively through the injected Approver port, so the publish-
// approval floor (contracts §7.3) stays in mechanism in Slice D's ApprovalService.
package watchapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// RevisionLister lists the board changeset log for the story timeline. The frozen
// agentdb.BoardStore lacks a list method; MemBoard and the Postgres board both
// satisfy this narrow port (contracts §10b E-2).
type RevisionLister interface {
	Revisions(ctx context.Context) ([]agentdb.BoardRevision, error)
}

// TelemetryReader is the read side of the run log (contracts §5 Telemetry.Runs,
// §10b E-1 ctx+error). *orchestrator.MemTelemetry satisfies it.
type TelemetryReader interface {
	Runs(ctx context.Context) ([]orchestrator.Run, error)
}

// Approver runs the Slice-D approval→publish flow for one Needs-Human ticket and
// returns the published ref. Slice E calls THROUGH this port and never touches a
// Connector — the publish-approval floor (contracts §7.3) stays in mechanism.
// *orchestrator.ApprovalService satisfies it.
type Approver interface {
	Approve(ctx context.Context, ticketID string) (ref string, err error)
}

// Rejecter discards a drafted post WITHOUT publishing and returns the human's note
// as a HumanFeedback targeting the ticket (Slice E then feeds it to the
// FeedbackApplier). *orchestrator.ApprovalService satisfies it (its Reject clears
// the pending post and returns the ticket to Todo).
type Rejecter interface {
	Reject(ctx context.Context, ticketID, note string) (orchestrator.HumanFeedback, error)
}

// Answerer resumes an escalated needs_human ticket with the human's answer
// (§10c §E): the text lands in AttemptNotes and the ticket re-enters the queue.
// Valid only WITHOUT a PendingPost — a drafted post is approved or rejected,
// never answered. *orchestrator.ApprovalService satisfies it.
type Answerer interface {
	Answer(ctx context.Context, ticketID, text string) error
}

// Config wires the ports. AuthToken "" disables the guard (local dev only).
// Feedback and Trigger are the frozen orchestrator seams so the real Slice-C
// impls (HumanFeedbackApplier, ExchangeTrigger) bind directly.
type Config struct {
	Board     agentdb.BoardStore
	Revisions RevisionLister
	Tickets   orchestrator.TicketStore
	Telemetry TelemetryReader
	Approver  Approver
	Rejecter  Rejecter
	Answerer  Answerer
	Feedback  orchestrator.FeedbackApplier
	Trigger   orchestrator.Triggerer
	AuthToken string
}

// Handlers is the mountable handler set.
type Handlers struct {
	cfg Config
}

// New validates required ports and returns the handler set.
func New(cfg Config) (*Handlers, error) {
	switch {
	case cfg.Board == nil:
		return nil, errors.New("watchapi: Board is required")
	case cfg.Revisions == nil:
		return nil, errors.New("watchapi: Revisions is required")
	case cfg.Tickets == nil:
		return nil, errors.New("watchapi: Tickets is required")
	case cfg.Telemetry == nil:
		return nil, errors.New("watchapi: Telemetry is required")
	case cfg.Approver == nil:
		return nil, errors.New("watchapi: Approver is required")
	case cfg.Rejecter == nil:
		return nil, errors.New("watchapi: Rejecter is required")
	case cfg.Answerer == nil:
		return nil, errors.New("watchapi: Answerer is required")
	case cfg.Feedback == nil:
		return nil, errors.New("watchapi: Feedback is required")
	case cfg.Trigger == nil:
		return nil, errors.New("watchapi: Trigger is required")
	}
	return &Handlers{cfg: cfg}, nil
}

// Mux registers the §8 routes (go1.22 method+path patterns; §10c §E adds
// /answer) plus the thin embedded web client, all behind the shared-token guard.
func (h *Handlers) Mux() *http.ServeMux {
	m := http.NewServeMux()
	// §8 API
	m.HandleFunc("GET /api/tickets", h.ListTickets)
	m.HandleFunc("POST /api/tickets/{id}/approve", h.Approve)
	m.HandleFunc("POST /api/tickets/{id}/reject", h.Reject)
	m.HandleFunc("POST /api/tickets/{id}/answer", h.Answer)
	m.HandleFunc("POST /api/feedback", h.Feedback)
	m.HandleFunc("GET /api/board/revisions", h.Revisions)
	m.HandleFunc("GET /api/board/current", h.Current)
	m.HandleFunc("GET /api/runs", h.Runs)
	m.HandleFunc("POST /api/trigger", h.Trigger)
	// thin web client (pure consumer of the §8 API)
	m.HandleFunc("GET /app.js", h.serveWeb)
	m.HandleFunc("GET /", h.serveWeb)

	// wrap every route in the shared-token guard
	guarded := http.NewServeMux()
	guarded.Handle("/", h.auth(m))
	return guarded
}

// auth enforces a single shared bearer token when configured (empty = disabled).
func (h *Handlers) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.AuthToken != "" && r.Header.Get("Authorization") != "Bearer "+h.cfg.AuthToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}
